package main

import (
	"database/sql"
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
func startUsageLog(db *sql.DB) error {
	if _, err := db.Exec(usageSchema); err != nil {
		return err
	}
	ch := make(chan usageEvent, usageQueue)
	usageEvents = ch
	go writeUsage(db, ch)
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

func writeUsage(db *sql.DB, ch <-chan usageEvent) {
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
		if err := insertUsage(db, batch); err != nil {
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
	"GET /api/settings":                 "read settings",
	"POST /api/settings":                "save settings",
	"POST /api/shutdown":                "shut down",
	"POST /api/connect":                 "connect",
	"POST /api/disconnect":              "disconnect",
	"POST /api/testconn":                "test connection",
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
}

// usageSkipped are requests that aren't something the user did: progress
// polls, and the Logs tab reading the log (live, every few seconds; opening
// the tab is a ui event).
func usageSkipped(r *http.Request) bool {
	return r.Method == "GET" && (strings.HasPrefix(r.URL.Path, "/api/exportall/") ||
		strings.HasPrefix(r.URL.Path, "/api/compare/") || r.URL.Path == "/api/logs")
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
