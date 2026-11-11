package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Events: what people want to look back on — messages and e-mails sent, sign-ins
// (and the failed ones), requests for access, users, exports, deleted files,
// saved connections and queries, settings, containers and updates. The page
// shows them under the Events button at the top; the usage log (usage.go) is
// the far more detailed record of every click.
//
// Who did it, from which IP address and what it says are one encrypted record
// (with the key of the saved connections); only the time, the kind and
// whether it failed are plain, to find and filter them. Events are kept for
// eventsKeep.

const eventsSchema = `CREATE TABLE IF NOT EXISTS events (
	id     INTEGER PRIMARY KEY AUTOINCREMENT,
	at     TEXT NOT NULL,               -- UTC, RFC 3339
	kind   TEXT NOT NULL,               -- signin, message, email, export, … (eventKinds)
	failed INTEGER NOT NULL DEFAULT 0,
	data   TEXT NOT NULL                -- encrypted eventData
);
CREATE INDEX IF NOT EXISTS events_at ON events(at)`

const (
	eventsKeep     = 90 * 24 * time.Hour
	eventsPage     = 200  // events in one answer
	eventsScanMax  = 5000 // rows read to find a page of a text search
	eventsPruneGap = 500  // events written between two prunes
)

// eventKinds are the kinds the page can filter by, in its order.
var eventKinds = []string{
	"message", "email", "signin", "signin-failed", "signout", "password", "register", "user",
	"export", "files", "connection", "query", "settings", "container", "server",
}

type eventData struct {
	User string `json:"user,omitempty"` // the signed-in user, or the name of the page
	IP   string `json:"ip,omitempty"`
	Text string `json:"text"`
}

type eventView struct {
	ID     int64     `json:"id"`
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`
	Failed bool      `json:"failed,omitempty"`
	eventData
}

var eventsWritten atomic.Int64

func (s *Store) migrateEvents() error {
	_, err := s.DB().Exec(eventsSchema)
	if err == nil {
		s.pruneEvents()
	}
	return err
}

func (s *Store) pruneEvents() {
	s.DB().Exec(`DELETE FROM events WHERE at < ?`, time.Now().Add(-eventsKeep).UTC().Format(time.RFC3339))
}

func (s *Store) addEvent(kind string, failed bool, d eventData) error {
	b, _ := json.Marshal(d)
	_, err := s.DB().Exec(`INSERT INTO events (at, kind, failed, data) VALUES (?, ?, ?, ?)`,
		time.Now().UTC().Format(time.RFC3339Nano), kind, failed, s.box.seal("event", string(b)))
	if eventsWritten.Add(1)%eventsPruneGap == 0 {
		s.pruneEvents()
	}
	return err
}

// event records what the person of r did: the signed-in user, otherwise
// the name the page goes by (if any). r may be nil for cactusdb itself.
func (s *server) event(r *http.Request, kind string, failed bool, format string, args ...any) {
	user := ""
	if r != nil {
		if u, ok := currentUser(r); ok {
			user = u.Username
		} else if id := r.Header.Get("X-Cactusdb-Client"); id != "" {
			s.presence.mu.Lock()
			if c := s.presence.clients[id]; c != nil {
				user = c.name
			}
			s.presence.mu.Unlock()
		}
	}
	s.eventAs(r, user, kind, failed, format, args...)
}

// eventAs records an event of user (e.g. the address a sign-in failed for).
func (s *server) eventAs(r *http.Request, user, kind string, failed bool, format string, args ...any) {
	if s.store == nil {
		return
	}
	d := eventData{User: user, Text: fmt.Sprintf(format, args...)}
	if r != nil {
		d.IP = clientIP(r)
	}
	if err := s.store.addEvent(kind, failed, d); err != nil {
		log.Printf("event %s not recorded: %v", kind, err)
	}
}

// handleEvents answers the newest events, older than before (an id) when
// given, of the kinds (comma-separated), on the day (YYYY-MM-DD, local time
// of the page via tz minutes) and holding q (in who, IP or text).
func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	qv := r.URL.Query()
	where, args := []string{"1=1"}, []any{}
	if b, err := strconv.ParseInt(qv.Get("before"), 10, 64); err == nil && b > 0 {
		where, args = append(where, "id < ?"), append(args, b)
	}
	if k := strings.TrimSpace(qv.Get("kind")); k != "" {
		var ph []string
		for _, kind := range strings.Split(k, ",") {
			ph, args = append(ph, "?"), append(args, kind)
		}
		where = append(where, "kind IN ("+strings.Join(ph, ",")+")")
	}
	if qv.Get("failed") == "1" {
		where = append(where, "failed = 1")
	}
	if day := qv.Get("day"); day != "" {
		start, err := time.Parse("2006-01-02", day)
		if err != nil {
			writeErr(w, 400, errors.New("day: expected YYYY-MM-DD"))
			return
		}
		tz, _ := strconv.Atoi(qv.Get("tz")) // the page's getTimezoneOffset(): minutes behind UTC
		start = start.Add(time.Duration(tz) * time.Minute)
		where = append(where, "at >= ? AND at < ?")
		args = append(args, start.Format(time.RFC3339), start.Add(24*time.Hour).Format(time.RFC3339))
	}
	needle := strings.ToLower(strings.TrimSpace(qv.Get("q")))
	limit := eventsPage
	if needle != "" {
		limit = eventsScanMax
	}
	rows, err := s.store.DB().Query(`SELECT id, at, kind, failed, data FROM events WHERE `+strings.Join(where, " AND ")+
		` ORDER BY id DESC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	defer rows.Close()
	out, read, more, lastID := []eventView{}, 0, false, int64(0)
	for rows.Next() {
		read++
		if read > limit || len(out) == eventsPage {
			more = true
			break
		}
		var e eventView
		var at, sealed string
		if err := rows.Scan(&e.ID, &at, &e.Kind, &e.Failed, &sealed); err != nil {
			writeErr(w, 500, err)
			return
		}
		lastID = e.ID
		e.At, _ = time.Parse(time.RFC3339Nano, at)
		plain, err := s.store.box.open("event", sealed)
		if err != nil || json.Unmarshal([]byte(plain), &e.eventData) != nil {
			e.Text = "(this event can't be decrypted)"
		}
		if needle != "" && !strings.Contains(strings.ToLower(e.User+"\x00"+e.IP+"\x00"+e.Text), needle) {
			continue
		}
		out = append(out, e)
	}
	// A text search stops after eventsScanMax rows: "more" continues from there.
	next := int64(0)
	if more {
		next = lastID
		if len(out) == eventsPage {
			next = out[len(out)-1].ID
		}
	}
	writeJSON(w, 200, map[string]any{"events": out, "next": next, "kinds": eventKinds, "keepDays": int(eventsKeep / (24 * time.Hour))})
}
