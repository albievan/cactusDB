package main

import (
	"log"
	"time"
)

// sessionIdle is how long a session may go without a request before it is
// closed. A browser tab that was closed or reloaded never says goodbye, and
// its database connections would otherwise stay open as long as cactusdb
// runs, counting against the database server's connection limit.
const sessionIdle = 30 * time.Minute

// expireSessions closes, every minute, the sessions idle for longer than
// idle, unless a query or an export is still using them.
func (s *server) expireSessions(idle time.Duration) {
	for range time.Tick(time.Minute) {
		s.closeIdleSessions(idle)
	}
}

func (s *server) closeIdleSessions(idle time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, ss := range s.sessions {
		if time.Since(ss.lastUsed) < idle || ss.db.Stats().InUse > 0 || s.exporting(id) {
			continue
		}
		log.Printf("closing session %.8s of %q: unused for %s", id, ss.label, time.Since(ss.lastUsed).Round(time.Minute))
		ss.db.Close()
		delete(s.sessions, id)
	}
}

// exporting reports whether an "Export all" of the session is still running.
// The caller holds s.mu.
func (s *server) exporting(session string) bool {
	for _, b := range s.batches {
		if b.session != session {
			continue
		}
		select {
		case <-b.done:
		default:
			return true
		}
	}
	return false
}
