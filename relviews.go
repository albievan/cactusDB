package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The saved views of DB Relationships: a name and what the page needs to
// draw the view again (the objects chosen, the types and switches, where
// each object was put, the zoom), which the server keeps as it is. They
// belong to a saved connection and are sealed like its saved queries (see
// lock.go); the page keeps those of a connection that isn't saved in the
// browser.

const (
	maxRelViewName = 100
	maxRelView     = 4 << 20 // bytes of a view's JSON
)

// RelView is a saved view of DB Relationships.
type RelView struct {
	ID      int64           `json:"id"`
	Name    string          `json:"name"`
	View    json.RawMessage `json:"view"`
	Created string          `json:"created"`
	Updated string          `json:"updated"`
}

type relViewData struct {
	Name string          `json:"name"`
	View json.RawMessage `json:"view"`
}

// RelViews lists a connection's saved views by name.
func (s *Store) RelViews(connID int64, box *secretBox) ([]RelView, error) {
	rows, err := s.DB().Query(`SELECT id, data, created_at, updated_at FROM rel_views WHERE conn_id = ?`, connID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RelView{}
	for rows.Next() {
		var v RelView
		var data string
		if err := rows.Scan(&v.ID, &data, &v.Created, &v.Updated); err != nil {
			return nil, err
		}
		plain, err := s.openData(box, "relview", data)
		if err != nil {
			return nil, err
		}
		var d relViewData
		if err := json.Unmarshal([]byte(plain), &d); err != nil {
			return nil, err
		}
		v.Name, v.View = d.Name, d.View
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && strings.ToLower(out[j].Name) < strings.ToLower(out[j-1].Name); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// SaveRelView adds a view, or replaces one: by id, or by name (saving a view
// under the name of another replaces that one). It returns the view's id.
func (s *Store) SaveRelView(connID int64, box *secretBox, v RelView) (int64, error) {
	existing, err := s.RelViews(connID, box)
	if err != nil {
		return 0, err
	}
	found := false
	for _, e := range existing {
		if v.ID != 0 && e.ID == v.ID {
			found = true
		} else if v.ID == 0 && strings.EqualFold(strings.TrimSpace(e.Name), v.Name) {
			v.ID, found = e.ID, true
		} else if strings.EqualFold(strings.TrimSpace(e.Name), v.Name) {
			return 0, fmt.Errorf("this connection already has a view named %q", e.Name)
		}
	}
	if v.ID != 0 && !found {
		return 0, sql.ErrNoRows
	}
	b, _ := json.Marshal(relViewData{Name: v.Name, View: v.View})
	data, now := s.sealData(box, "relview", string(b)), time.Now().Format(time.RFC3339)
	if v.ID == 0 {
		res, err := s.DB().Exec(`INSERT INTO rel_views (conn_id, data, created_at, updated_at) VALUES (?, ?, ?, ?)`, connID, data, now, now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err = s.DB().Exec(`UPDATE rel_views SET data = ?, updated_at = ? WHERE id = ? AND conn_id = ?`, data, now, v.ID, connID)
	return v.ID, err
}

// DeleteRelView deletes one of the connection's saved views.
func (s *Store) DeleteRelView(connID, id int64) error {
	res, err := s.DB().Exec(`DELETE FROM rel_views WHERE id = ? AND conn_id = ?`, id, connID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// handleRelViews lists the saved views of the session's connection; saved
// is false for a connection that isn't saved (the page keeps its views).
func (s *server) handleRelViews(w http.ResponseWriter, r *http.Request) {
	ss, err := s.session(r, r.URL.Query().Get("session"))
	if err != nil {
		writeErr(w, 401, err)
		return
	}
	if ss.savedID == 0 {
		writeJSON(w, 200, map[string]any{"saved": false, "views": []RelView{}})
		return
	}
	vs, err := s.store.RelViews(ss.savedID, ss.connBox)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"saved": true, "views": vs})
}

func (s *server) handleSaveRelView(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string `json:"session"`
		RelView
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
	v := req.RelView
	v.Name = strings.TrimSpace(v.Name)
	switch {
	case ss.savedID == 0:
		writeErr(w, 400, errors.New("views are kept on the server only for a saved connection"))
		return
	case v.Name == "":
		writeErr(w, 400, errors.New("give the view a name"))
		return
	case len([]rune(v.Name)) > maxRelViewName:
		writeErr(w, 400, fmt.Errorf("the name is too long (at most %d characters)", maxRelViewName))
		return
	case len(v.View) == 0 || !json.Valid(v.View):
		writeErr(w, 400, errors.New("the view is missing"))
		return
	case len(v.View) > maxRelView:
		writeErr(w, 400, errors.New("the view is too large to save"))
		return
	}
	id, err := s.store.SaveRelView(ss.savedID, ss.connBox, v)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeErr(w, 404, errors.New("saved view not found"))
		return
	case err != nil:
		writeErr(w, 400, err)
		return
	}
	logAction(r, "[%s] saved relationships view %d", ss.label, id)
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *server) handleDeleteRelView(w http.ResponseWriter, r *http.Request) {
	ss, err := s.session(r, r.URL.Query().Get("session"))
	if err != nil {
		writeErr(w, 401, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || ss.savedID == 0 {
		writeErr(w, 400, errors.New("bad id"))
		return
	}
	if err := s.store.DeleteRelView(ss.savedID, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeErr(w, 404, errors.New("saved view not found"))
		} else {
			writeErr(w, 500, err)
		}
		return
	}
	logAction(r, "[%s] deleted relationships view %d", ss.label, id)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
