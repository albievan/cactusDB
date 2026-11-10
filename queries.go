package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
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
	// The database checks it first (see checkQuery); one it couldn't check in
	// time is saved all the same, and the answer says so.
	how, err := checkQuery(r.Context(), ss.engine, ss.db, q.SQL)
	if err != nil && !errors.Is(err, errCheckTimeout) {
		logAction(r, "[%s] save query %q REFUSED: %v", ss.label, q.Name, err)
		writeErr(w, 400, fmt.Errorf("not saved: the query isn't valid: %w", err))
		return
	}
	if err != nil {
		how = ""
	}
	id, version, err := s.store.SaveQuery(ss.savedID, ss.connBox, q)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeErr(w, 404, errors.New("saved query not found"))
		return
	case err != nil:
		writeErr(w, 400, err)
		return
	}
	logAction(r, "[%s] saved query %q (version %d)", ss.label, q.Name, version)
	// Not the query's name: it is kept encrypted with the connection password.
	if req.ID == 0 {
		s.notify(r, "queries", ss.savedID, "added a saved query to “%s”", ss.label)
	} else {
		s.notify(r, "queries", ss.savedID, "changed a saved query of “%s”", ss.label)
	}
	writeJSON(w, 200, map[string]any{"id": id, "version": version, "checked": how})
}

// handleQueryVersions lists the saved versions of a query, newest first.
func (s *server) handleQueryVersions(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.querySession(w, r, r.URL.Query().Get("session"))
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, errors.New("bad id"))
		return
	}
	vs, err := s.store.QueryVersions(ss.savedID, ss.connBox, id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeErr(w, 404, errors.New("saved query not found"))
	case err != nil:
		writeErr(w, 500, err)
	default:
		writeJSON(w, 200, map[string]any{"versions": vs})
	}
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
	s.notify(r, "queries", ss.savedID, "deleted a saved query of “%s”", ss.label)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// checkTimeout bounds checking a query: one that has to be started to be
// checked is stopped once its columns are known, or after this long.
const checkTimeout = 15 * time.Second

var (
	errChecked      = errors.New("checked")
	errCheckTimeout = fmt.Errorf("the database took more than %s to start the query: it couldn't be checked", checkTimeout)
)

// checkQuery asks the database whether q is a valid query, after
// checkReadOnly, without running it where the database can: SQLite,
// PostgreSQL and its relatives, MySQL, MariaDB and Firebird prepare it; SQL
// Server describes it. Any other database (Oracle's driver prepares only in
// the client) starts it, read-only like any query, and it is stopped as soon
// as its columns are known: errCheckTimeout if that takes too long, as it can
// for a sorted query over a large table. how says which was done.
func checkQuery(ctx context.Context, engine string, db *sql.DB, q string) (how string, err error) {
	if err := checkReadOnly(engine, q); err != nil {
		return "", fmt.Errorf("only read-only queries can be saved: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	if backendOf(db) == nil {
		switch engine {
		case EngineSQLite, EnginePostgres, EngineMySQL, EngineMariaDB, EngineFirebird:
			st, err := db.PrepareContext(ctx, q)
			var me *mysql.MySQLError
			if errors.As(err, &me) && me.Number == 1295 { // a statement MySQL can't prepare (some SHOWs): start it instead
				break
			}
			if err != nil {
				return "", err
			}
			st.Close()
			return "prepared by the database, not run", nil
		case EngineMSSQL:
			rows, err := db.QueryContext(ctx, "EXEC sp_describe_first_result_set @tsql = @p1", q)
			if err != nil {
				return "", err
			}
			defer rows.Close()
			for rows.Next() {
			}
			if err := rows.Err(); err != nil {
				return "", err
			}
			return "described by the database, not run", nil
		}
	}
	err = streamRows(ctx, engine, db, q, func(cols, types []string) error { return errChecked }, nil)
	if err != nil && !errors.Is(err, errChecked) {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", errCheckTimeout
		}
		return "", err
	}
	return "started by the database and stopped before reading any rows", nil
}

// handleCheckQuery checks a query (see checkQuery) for the save query dialog.
// A query that isn't valid is answered with valid: false and the database's error.
func (s *server) handleCheckQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string `json:"session"`
		SQL     string `json:"sql"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	ss, err := s.session(r, req.Session)
	if err != nil {
		writeErr(w, 401, err)
		return
	}
	q := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(req.SQL), ";"))
	switch {
	case q == "":
		writeJSON(w, 200, map[string]any{"valid": false, "error": "the query is empty"})
		return
	case len(q) > maxQuerySQL:
		writeJSON(w, 200, map[string]any{"valid": false, "error": "the query is too long"})
		return
	}
	how, err := checkQuery(r.Context(), ss.engine, ss.db, q)
	if err != nil {
		logAction(r, "[%s] check query: NOT VALID: %v", ss.label, err)
		writeJSON(w, 200, map[string]any{"valid": false, "error": err.Error()})
		return
	}
	logAction(r, "[%s] check query: valid (%s)", ss.label, how)
	writeJSON(w, 200, map[string]any{"valid": true, "how": how})
}
