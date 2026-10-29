package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Saved queries belong to a saved connection and are kept, encrypted, in
// cactus.conn.db (see lock.go). Only read-only queries can be saved: the same
// check as running a query (checkReadOnly), which runs again when they run.

const (
	maxQueryName = 100
	maxQueryDesc = 1000
	maxQuerySQL  = 200_000
)

// querySession returns the session of a request, which must have been opened
// from a saved connection.
func (s *server) querySession(w http.ResponseWriter, r *http.Request, id string) (*session, bool) {
	ss, err := s.session(r, id)
	if err != nil {
		writeErr(w, 401, err)
		return nil, false
	}
	if ss.savedID == 0 {
		writeErr(w, 400, errors.New("queries can only be saved for a saved connection: save this connection first"))
		return nil, false
	}
	return ss, true
}

func (s *server) handleListQueries(w http.ResponseWriter, r *http.Request) {
	ss, err := s.session(r, r.URL.Query().Get("session"))
	if err != nil {
		writeErr(w, 401, err)
		return
	}
	if ss.savedID == 0 {
		writeJSON(w, 200, map[string]any{"saved": false, "queries": []SavedQuery{}})
		return
	}
	qs, err := s.store.Queries(ss.savedID, ss.connBox)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"saved": true, "connection": ss.label, "queries": qs})
}

// handleSaveQuery adds a query to the session's saved connection, or changes
// one (id), after checking it is read-only.
func (s *server) handleSaveQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string `json:"session"`
		SavedQuery
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	ss, ok := s.querySession(w, r, req.Session)
	if !ok {
		return
	}
	q := req.SavedQuery
	q.Name = strings.TrimSpace(q.Name)
	q.Description = strings.TrimSpace(q.Description)
	q.SQL = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(q.SQL), ";"))
	switch {
	case q.Name == "":
		writeErr(w, 400, errors.New("give the query a name"))
		return
	case len([]rune(q.Name)) > maxQueryName:
		writeErr(w, 400, fmt.Errorf("the name is too long (at most %d characters)", maxQueryName))
		return
	case len([]rune(q.Description)) > maxQueryDesc:
		writeErr(w, 400, fmt.Errorf("the description is too long (at most %d characters)", maxQueryDesc))
		return
	case q.SQL == "":
		writeErr(w, 400, errors.New("the query is empty"))
		return
	case len(q.SQL) > maxQuerySQL:
		writeErr(w, 400, errors.New("the query is too long"))
		return
	}
	if err := checkReadOnly(ss.engine, q.SQL); err != nil {
		logAction(r, "[%s] save query %q REFUSED: %v", ss.label, q.Name, err)
		writeErr(w, 400, fmt.Errorf("only read-only queries can be saved: %w", err))
		return
	}
	id, err := s.store.SaveQuery(ss.savedID, ss.connBox, q)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeErr(w, 404, errors.New("saved query not found"))
		return
	case err != nil:
		writeErr(w, 400, err)
		return
	}
	logAction(r, "[%s] saved query %q", ss.label, q.Name)
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *server) handleDeleteQuery(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.querySession(w, r, r.URL.Query().Get("session"))
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, errors.New("bad id"))
		return
	}
	if err := s.store.DeleteQuery(ss.savedID, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeErr(w, 404, errors.New("saved query not found"))
		} else {
			writeErr(w, 500, err)
		}
		return
	}
	logAction(r, "[%s] deleted saved query %d", ss.label, id)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
