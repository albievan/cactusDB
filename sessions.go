package main

import (
	"log"
	"time"
)

// A page left alone disconnects itself after the idle time (Settings, default
// 15 minutes). A browser tab that was closed or reloaded can't, so the server
// also closes every session unused for the idle time: otherwise its database
// connections would stay open as long as cactusdb runs, counting against the
// database server's connection limit. The server waits a little longer than
// the page, so the page is the one that reports it.
const serverIdleGrace = 2 * time.Minute

// expireSessions closes, every minute, the sessions idle for longer than the
// idle time, unless a query or an export is still using them.
func (s *server) expireSessions() {
	for range time.Tick(time.Minute) {
		if m := s.idleMinutes(); m > 0 {
			s.closeIdleSessions(time.Duration(m)*time.Minute + serverIdleGrace)
		}
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
