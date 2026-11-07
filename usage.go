package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// The usage log: every function used in the app, one row per event in the
// usage_events table of cactus.conn.db, with the client's IP address and
// session. cactusdb only writes it, it has no page or API to read it: open
// cactus.conn.db with any SQLite tool (or as a SQLite connection in
// cactusdb) to explore it.
//
// Two sources write to it:
//   - server: every API call (connect, open a table, run a query, export,
//     compare, …), with its result, duration and what it did, as in the log;
//   - ui: what only happens in the page (tabs, menu items, buttons, sorting,
//     check boxes), sent by the page in batches to POST /api/usage.
//
// Rows are written in batches by one goroutine, so a request never waits
// for the database; when it can't keep up, events are dropped rather than
// holding anything up.

const usageSchema = `CREATE TABLE IF NOT EXISTS usage_events (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	at          TEXT NOT NULL,              -- UTC, 2026-11-07 08:15:02.123
	source      TEXT NOT NULL,              -- server: an API call; ui: something done in the page
	function    TEXT NOT NULL,              -- connect, open table, run query, tab: logs, menu: compare, …
	detail      TEXT NOT NULL DEFAULT '',   -- what it did, as in the log (SQL shortened, never passwords)
	client_ip   TEXT NOT NULL DEFAULT '',
	session_id  TEXT NOT NULL DEFAULT '',   -- the database session: first 8 characters, as in the log
	page_id     TEXT NOT NULL DEFAULT '',   -- one browser tab, from opening the page until it is closed or reloaded
	connection  TEXT NOT NULL DEFAULT '',   -- the connection's name
	engine      TEXT NOT NULL DEFAULT '',
	method      TEXT NOT NULL DEFAULT '',
	path        TEXT NOT NULL DEFAULT '',
	status      INTEGER,                    -- HTTP result of an API call
	duration_ms INTEGER,
	user_agent  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS usage_events_at ON usage_events(at);
CREATE INDEX IF NOT EXISTS usage_events_function ON usage_events(function);
CREATE INDEX IF NOT EXISTS usage_events_session ON usage_events(session_id)`

type usageEvent struct {
	at                                    time.Time
	source, function, detail              string
	ip, session, page, connection, engine string
	method, path, agent                   string
	status                                int // 0: none (a ui event)
	ms                                    int64
}

const (
	usageQueue = 2000 // events waiting to be written
	usageBatch = 200  // written in one transaction
)

// usageEvents is where events go; nil (as in tests) records nothing.
var usageEvents chan usageEvent

// startUsageLog creates the table and starts the goroutine writing to it.
func startUsageLog(st *Store) error {
	if _, err := st.DB().Exec(usageSchema); err != nil {
		return err
	}
	ch := make(chan usageEvent, usageQueue)
	usageEvents = ch
	go writeUsage(st, ch)
	return nil
}

// recordUsage queues ev without ever waiting.
func recordUsage(ev usageEvent) {
	ch := usageEvents
	if ch == nil {
		return
	}
	select {
	case ch <- ev:
	default: // the writer is behind: drop it
	}
}

func writeUsage(st *Store, ch <-chan usageEvent) {
	failed := false
	for ev := range ch {
		batch := []usageEvent{ev}
	more:
		for len(batch) < usageBatch {
			select {
			case e := <-ch:
				batch = append(batch, e)
			default:
				break more
			}
		}
		err := insertUsage(st.DB(), batch)
		if err != nil && strings.Contains(err.Error(), "database is closed") {
			err = insertUsage(st.DB(), batch) // the file was just moved: write to the new one
		}
		if err != nil {
			if !failed { // once, not for every batch
				log.Printf("warning: the usage log can't be written: %v", err)
			}
			failed = true
		} else {
			failed = false
		}
		time.Sleep(200 * time.Millisecond) // gather the next batch
	}
}

func insertUsage(db *sql.DB, batch []usageEvent) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := tx.Prepare(`INSERT INTO usage_events (at, source, function, detail, client_ip, session_id, page_id,
		connection, engine, method, path, status, duration_ms, user_agent) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, e := range batch {
		var status, ms any
		if e.status != 0 {
			status, ms = e.status, e.ms
		}
		if _, err := st.Exec(e.at.UTC().Format("2006-01-02 15:04:05.000"), e.source, e.function, e.detail, e.ip,
			shortSession(e.session), e.page, e.connection, e.engine, e.method, e.path, status, ms, e.agent); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// shortSession is the first 8 characters of a session id, as in the log:
// the whole id works like a password while the session lasts.
func shortSession(id string) string {
	return id[:min(8, len(id))]
}

// usageFunctions names the function behind each API route.
var usageFunctions = map[string]string{
	"GET /api/engines":                  "list database types",
	"GET /api/drivers":                  "show drivers",
	"GET /api/info":                     "open page",
	"GET /api/logs/zip":                 "download logs",
	"GET /api/update":                   "check for update",
	"POST /api/upgrade":                 "install update",
	"POST /api/upgrade/upload":          "install update from browser",
	"GET /api/settings":                 "read settings",
	"POST /api/settings":                "save settings",
	"POST /api/shutdown":                "shut down",
	"POST /api/connect":                 "connect",
	"POST /api/disconnect":              "disconnect",
	"POST /api/testconn":                "test connection",
	"POST /api/refresh":                 "refresh connection and objects",
	"GET /api/objects":                  "list objects",
	"GET /api/tablestats":               "table rows and sizes",
	"GET /api/definition":               "show definition",
	"GET /api/select":                   "open table",
	"POST /api/query":                   "run query",
	"POST /api/export":                  "export",
	"POST /api/exportddl":               "export DDL",
	"POST /api/backup":                  "backup",
	"POST /api/exportall":               "export all tables",
	"GET /api/exports/{path...}":        "download export",
	"GET /api/files":                    "list exported files",
	"POST /api/files/zip":               "zip exported files",
	"GET /api/files/zip/{id}":           "download zip",
	"POST /api/files/preview":           "preview exported file",
	"POST /api/files/delete":            "delete exported files",
	"POST /api/compare":                 "compare databases",
	"GET /api/connections":              "list saved connections",
	"POST /api/connections":             "save connection",
	"DELETE /api/connections/{id}":      "delete connection",
	"POST /api/connections/{id}/delete": "delete connection",
	"POST /api/connections/{id}/unlock": "unlock connection",
	"GET /api/superuser":                "superuser status",
	"POST /api/superuser/reveal":        "superuser reveal",
	"GET /api/queries":                  "list saved queries",
	"POST /api/queries":                 "save query",
	"DELETE /api/queries/{id}":          "delete saved query",
	"POST /api/queries/{id}/delete":     "delete saved query",
	"GET /api/usage/summary":            "usage log summary",
	"POST /api/usage/delete":            "usage log housekeeping",
}

// usageSkipped are requests that aren't something the user did: progress
// polls, and the Logs tab reading the log (live, every few seconds; opening
// the tab is a ui event).
func usageSkipped(r *http.Request) bool {
	return r.Method == "GET" && (strings.HasPrefix(r.URL.Path, "/api/exportall/") ||
		strings.HasPrefix(r.URL.Path, "/api/compare/") || r.URL.Path == "/api/logs" || r.URL.Path == "/api/upgrade")
}

// recordRequest records API call r, answered with status after d.
func recordRequest(r *http.Request, rl *reqLog, status int, d time.Duration) {
	if usageEvents == nil || usageSkipped(r) {
		return
	}
	rl.mu.Lock()
	detail, conn, engine := strings.Join(rl.details, " | "), rl.connection, rl.engine
	rl.mu.Unlock()
	fn := usageFunctions[r.Pattern]
	if fn == "" {
		fn = strings.TrimSpace(r.Method + " " + r.URL.Path)
	}
	recordUsage(usageEvent{at: time.Now().Add(-d), source: "server", function: fn,
		detail: clip(detail, 2000), ip: clientIP(r), session: rl.session,
		page: clip(r.Header.Get("X-Cactusdb-Page"), 40), connection: conn, engine: engine,
		method: r.Method, path: r.URL.Path, status: status, ms: d.Milliseconds(), agent: clip(r.UserAgent(), 300)})
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// handleUsage takes the page's events: what was clicked or changed in the
// page. It always answers 204: the page doesn't wait for it or retry.
func (s *server) handleUsage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Page    string `json:"page"`
		Session string `json:"session"` // of every event without its own
		Events  []struct {
			Fn      string `json:"fn"`
			Detail  string `json:"detail"`
			Session string `json:"session"` // the session when it happened
			// Its connection, as the page knows it: the session may be
			// closed by the time the event is sent.
			Connection string `json:"connection"`
			Engine     string `json:"engine"`
			Ago        int64  `json:"ago"` // milliseconds before sending
		} `json:"events"`
	}
	if readJSON(r, &req) == nil {
		now := time.Now()
		for i, e := range req.Events {
			if i == 50 || e.Fn == "" {
				break
			}
			if e.Session == "" {
				e.Session = req.Session
			}
			conn, engine := e.Connection, e.Engine
			s.mu.Lock()
			if ss, ok := s.sessions[e.Session]; ok {
				conn, engine = ss.label, ss.engine
			}
			s.mu.Unlock()
			ago := time.Duration(min(max(e.Ago, 0), 600000)) * time.Millisecond
			recordUsage(usageEvent{at: now.Add(-ago), source: "ui", function: clip(e.Fn, 100), detail: clip(e.Detail, 300),
				ip: clientIP(r), session: e.Session, page: clip(req.Page, 40), connection: conn, engine: engine,
				agent: clip(r.UserAgent(), 300)})
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// Housekeeping (☰ → Settings): how many events there are, and deleting
// those of a date range. Still no way to read the events themselves.

func (s *server) usageSummary() (map[string]any, error) {
	var n int64
	var first, last sql.NullString
	err := s.store.DB().QueryRow(`SELECT COUNT(*), MIN(at), MAX(at) FROM usage_events`).Scan(&n, &first, &last)
	return map[string]any{"count": n, "first": first.String, "last": last.String}, err
}

func (s *server) handleUsageSummary(w http.ResponseWriter, r *http.Request) {
	sum, err := s.usageSummary()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, sum)
}

// handleUsageDelete deletes the events from "from" up to (not including)
// "to", both times the page computed from local days; an empty one is
// open-ended. Only JSON, so other websites can't send it.
func (s *server) handleUsageDelete(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeErr(w, 415, errors.New("expected a JSON request"))
		return
	}
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	bound := func(v string) (string, error) {
		if v == "" {
			return "", nil
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return "", fmt.Errorf("bad date %q", v)
		}
		return t.UTC().Format("2006-01-02 15:04:05.000"), nil
	}
	from, err := bound(req.From)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	to, err := bound(req.To)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if from != "" && to != "" && from >= to {
		writeErr(w, 400, errors.New("the first day must be before the last one"))
		return
	}
	q, args := `DELETE FROM usage_events WHERE 1 = 1`, []any{}
	if from != "" {
		q, args = q+` AND at >= ?`, append(args, from)
	}
	if to != "" {
		q, args = q+` AND at < ?`, append(args, to)
	}
	res, err := s.store.DB().Exec(q, args...)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	n, _ := res.RowsAffected()
	logAction(r, "usage log: deleted %d event(s) from %s to %s (UTC)", n, orOpen(from), orOpen(to))
	sum, err := s.usageSummary()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	sum["deleted"] = n
	writeJSON(w, 200, sum)
}

func orOpen(v string) string {
	if v == "" {
		return "(open)"
	}
	return v
}
