package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Saved queries of a project (with their versions and diagram layout,
// sealed with the project key), and what the query designer needs to know
// of the project's tables: their columns, keys, and the joins worth offering.

type projectQueryData struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	SQL         string          `json:"sql"`
	Layout      json.RawMessage `json:"layout,omitempty"` // the diagram: table positions, sizes, collapsed (the page's own format)
}

type ProjectQuery struct {
	ID      int64  `json:"id"`
	Created string `json:"created"`
	Updated string `json:"updated"`
	Version int    `json:"version"`
	projectQueryData
}

type ProjectQueryVersion struct {
	Version int    `json:"version"`
	Saved   string `json:"saved"`
	projectQueryData
}

func (s *Store) sealProjectQuery(key []byte, d projectQueryData) string {
	b, _ := json.Marshal(d)
	return s.sealData(projectBox(key), "projectquery", string(b))
}

func (s *Store) openProjectQuery(key []byte, data string) (projectQueryData, error) {
	var d projectQueryData
	plain, err := s.openData(projectBox(key), "projectquery", data)
	if err != nil {
		return d, err
	}
	return d, json.Unmarshal([]byte(plain), &d)
}

// ProjectQueries lists a project's saved queries by name.
func (s *Store) ProjectQueries(pid int64, key []byte) ([]ProjectQuery, error) {
	rows, err := s.DB().Query(`SELECT id, data, created_at, updated_at,
		(SELECT COALESCE(MAX(version), 1) FROM project_query_versions v WHERE v.query_id = project_queries.id)
		FROM project_queries WHERE project_id = ?`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectQuery{}
	for rows.Next() {
		var q ProjectQuery
		var data string
		if err := rows.Scan(&q.ID, &data, &q.Created, &q.Updated, &q.Version); err != nil {
			return nil, err
		}
		if q.projectQueryData, err = s.openProjectQuery(key, data); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	slices.SortFunc(out, func(a, b ProjectQuery) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out, rows.Err()
}

// SaveProjectQuery adds (q.ID 0) or changes a saved query of a project; a
// changed save is kept as a new version (layout-only changes are not).
func (s *Store) SaveProjectQuery(pid int64, key []byte, q ProjectQuery) (int64, int, error) {
	q.Name, q.Description = strings.TrimSpace(q.Name), strings.TrimSpace(q.Description)
	switch {
	case q.Name == "":
		return 0, 0, errors.New("a saved query needs a name")
	case len(q.Name) > 100:
		return 0, 0, errors.New("the name is longer than 100 characters")
	case len(q.Description) > 1000:
		return 0, 0, errors.New("the description is longer than 1000 characters")
	case strings.TrimSpace(q.SQL) == "":
		return 0, 0, errors.New("the query is empty")
	case len(q.SQL) > 200000:
		return 0, 0, errors.New("the query is longer than 200000 characters")
	}
	existing, err := s.ProjectQueries(pid, key)
	if err != nil {
		return 0, 0, err
	}
	var old *ProjectQuery
	for i, e := range existing {
		if e.ID == q.ID && q.ID != 0 {
			old = &existing[i]
		} else if strings.EqualFold(e.Name, q.Name) {
			return 0, 0, fmt.Errorf("the project has a saved query named %q already", e.Name)
		}
	}
	if q.ID != 0 && old == nil {
		return 0, 0, sql.ErrNoRows
	}
	now := time.Now().UTC().Format(time.RFC3339)
	data := s.sealProjectQuery(key, q.projectQueryData)
	tx, err := s.DB().Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	version := 1
	if old == nil {
		res, err := tx.Exec(`INSERT INTO project_queries (project_id, data, created_at, updated_at) VALUES (?, ?, ?, ?)`, pid, data, now, now)
		if err != nil {
			return 0, 0, err
		}
		q.ID, _ = res.LastInsertId()
	} else {
		if _, err := tx.Exec(`UPDATE project_queries SET data = ?, updated_at = ? WHERE id = ? AND project_id = ?`, data, now, q.ID, pid); err != nil {
			return 0, 0, err
		}
		version = old.Version
	}
	changed := old == nil || old.Name != q.Name || old.Description != q.Description || old.SQL != q.SQL
	if changed {
		if err := tx.QueryRow(`SELECT COALESCE(MAX(version), 0) + 1 FROM project_query_versions WHERE query_id = ?`, q.ID).Scan(&version); err != nil {
			return 0, 0, err
		}
		if _, err := tx.Exec(`INSERT INTO project_query_versions (query_id, project_id, version, data, saved_at) VALUES (?, ?, ?, ?, ?)`,
			q.ID, pid, version, data, now); err != nil {
			return 0, 0, err
		}
		if _, err := tx.Exec(`DELETE FROM project_query_versions WHERE query_id = ? AND version <= ?`, q.ID, version-maxQueryVersions); err != nil {
			return 0, 0, err
		}
	}
	return q.ID, version, tx.Commit()
}

func (s *Store) ProjectQueryVersions(pid int64, key []byte, id int64) ([]ProjectQueryVersion, error) {
	rows, err := s.DB().Query(`SELECT version, data, saved_at FROM project_query_versions WHERE query_id = ? AND project_id = ? ORDER BY version DESC`, id, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectQueryVersion{}
	for rows.Next() {
		var v ProjectQueryVersion
		var data string
		if err := rows.Scan(&v.Version, &data, &v.Saved); err != nil {
			return nil, err
		}
		if v.projectQueryData, err = s.openProjectQuery(key, data); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) DeleteProjectQuery(pid, id int64) error {
	res, err := s.DB().Exec(`DELETE FROM project_queries WHERE id = ? AND project_id = ?`, id, pid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	_, err = s.DB().Exec(`DELETE FROM project_query_versions WHERE query_id = ?`, id)
	return err
}

// ---- handlers -----------------------------------------------------------------

func (s *server) projectQueryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/project/queries", s.handleProjectQueries)
	mux.HandleFunc("POST /api/project/queries", s.handleSaveProjectQuery)
	mux.HandleFunc("GET /api/project/queries/{id}/versions", s.handleProjectQueryVersions)
	mux.HandleFunc("POST /api/project/queries/{id}/delete", s.handleDeleteProjectQuery)
	mux.HandleFunc("GET /api/project/columns", s.handleProjectColumns)
	mux.HandleFunc("POST /api/project/joins", s.handleProjectJoins)
}

func (s *server) handleProjectQueries(w http.ResponseWriter, r *http.Request) {
	ps, err := s.project(r, "")
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	list, err := s.store.ProjectQueries(ps.projectID, ps.key)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"queries": list})
}

func (s *server) handleSaveProjectQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string `json:"project"`
		ProjectQuery
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	ps, err := s.project(r, req.Project)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := checkProjectQuery(req.SQL); err != nil {
		writeJSON(w, 400, map[string]any{"error": "only a read query can be saved: " + err.Error()})
		return
	}
	id, version, err := s.store.SaveProjectQuery(ps.projectID, ps.key, req.ProjectQuery)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	logAction(r, "[project %s] save query %q (v%d)", ps.name, strings.TrimSpace(req.Name), version)
	s.event(r, "query", false, "[project %s] saved the query %q", ps.name, strings.TrimSpace(req.Name))
	writeJSON(w, 200, map[string]any{"id": id, "version": version})
}

func (s *server) handleProjectQueryVersions(w http.ResponseWriter, r *http.Request) {
	ps, err := s.project(r, "")
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, errors.New("bad id"))
		return
	}
	vs, err := s.store.ProjectQueryVersions(ps.projectID, ps.key, id)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"versions": vs})
}

func (s *server) handleDeleteProjectQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string `json:"project"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	ps, err := s.project(r, req.Project)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, errors.New("bad id"))
		return
	}
	if err := s.store.DeleteProjectQuery(ps.projectID, id); err != nil {
		writeErr(w, 404, errors.New("saved query not found"))
		return
	}
	logAction(r, "[project %s] delete saved query %d", ps.name, id)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// projectColumn is a column as the query designer shows it.
type projectColumn struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Kind     string `json:"kind"`
	Key      bool   `json:"key,omitempty"` // part of the primary key
	Nullable bool   `json:"nullable"`
}

// tableOf finds a project table by alias and project name. The caller holds ps.mu.
func (ps *projectSession) tableOf(alias, name string) *projectTable {
	for i := range ps.data.Tables {
		t := &ps.data.Tables[i]
		if strings.EqualFold(t.Alias, alias) && strings.EqualFold(t.Name, name) {
			return t
		}
	}
	return nil
}

func columnsOfTable(t *mTable) []projectColumn {
	out := make([]projectColumn, len(t.Cols))
	for i, c := range t.Cols {
		out[i] = projectColumn{Name: c.Src, Type: c.SrcType, Kind: c.Kind, Nullable: c.Nullable,
			Key: slices.ContainsFunc(t.PK, func(k string) bool { return strings.EqualFold(k, c.Src) })}
	}
	return out
}

// handleProjectColumns gives the columns of every table of the project that
// is connected (read once, see columnsOf), for the designer and suggestions.
func (s *server) handleProjectColumns(w http.ResponseWriter, r *http.Request) {
	ps, err := s.project(r, "")
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	alias, name := r.URL.Query().Get("alias"), r.URL.Query().Get("table")
	ps.mu.Lock()
	defer ps.mu.Unlock()
	out := map[string]any{}
	for i := range ps.data.Tables {
		pt := &ps.data.Tables[i]
		if (alias != "" && !strings.EqualFold(pt.Alias, alias)) || (name != "" && !strings.EqualFold(pt.Name, name)) || ps.links[pt.Alias] == nil {
			continue
		}
		t, err := ps.columnsOf(r.Context(), pt)
		if err != nil {
			out[pt.Alias+"."+pt.Name] = map[string]any{"error": err.Error()}
			continue
		}
		out[pt.Alias+"."+pt.Name] = map[string]any{"columns": columnsOfTable(t), "fks": ps.foreignKeysOf(pt, t)}
	}
	writeJSON(w, 200, map[string]any{"tables": out})
}

// projectFK is a foreign key as the query designer shows it: Table is the
// project's name of the table it points at, empty when that table isn't in
// the project (RefSource then says which it is).
type projectFK struct {
	Name      string   `json:"name"`
	Cols      []string `json:"cols"`
	Table     string   `json:"table,omitempty"`
	RefSource string   `json:"refSource"`
	RefCols   []string `json:"refCols"`
}

// foreignKeysOf lists a table's foreign keys. The caller holds ps.mu.
func (ps *projectSession) foreignKeysOf(pt *projectTable, t *mTable) []projectFK {
	out := []projectFK{}
	for _, fk := range t.FKs {
		f := projectFK{Name: fk.Name, Cols: fk.Cols, RefSource: fk.RefTable, RefCols: fk.RefCols}
		for _, o := range ps.data.Tables {
			if strings.EqualFold(o.Alias, pt.Alias) && (strings.EqualFold(o.Source, fk.RefTable) || strings.EqualFold(o.Name, fk.RefTable)) {
				f.Table = o.Name
				break
			}
		}
		out = append(out, f)
	}
	return out
}

type joinSuggestion struct {
	Pairs [][2]string `json:"pairs"` // column of a, column of b
	Why   string      `json:"why"`
}

// handleProjectJoins offers joins between two project tables: their foreign
// keys (same database), then columns that name the other table's key
// (customer_id → customer.id) or have the same name, with types that can be
// compared (across databases too). Offered only: the page applies one on a click.
func (s *server) handleProjectJoins(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string            `json:"project"`
		A       map[string]string `json:"a"`
		B       map[string]string `json:"b"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	ps, err := s.project(r, req.Project)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	pa, pb := ps.tableOf(req.A["alias"], req.A["name"]), ps.tableOf(req.B["alias"], req.B["name"])
	if pa == nil || pb == nil {
		writeErr(w, 400, errors.New("both tables must be tables of the project"))
		return
	}
	ta, err := ps.columnsOf(r.Context(), pa)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	tb, err := ps.columnsOf(r.Context(), pb)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"suggestions": suggestJoins(pa, ta, pb, tb)})
}

func suggestJoins(pa *projectTable, ta *mTable, pb *projectTable, tb *mTable) []joinSuggestion {
	var out []joinSuggestion
	seen := map[string]bool{}
	add := func(why string, pairs [][2]string) {
		k := fmt.Sprint(pairs)
		if len(pairs) == 0 || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, joinSuggestion{Pairs: pairs, Why: why})
	}
	if strings.EqualFold(pa.Alias, pb.Alias) { // foreign keys, in both directions
		for _, fk := range ta.FKs {
			if strings.EqualFold(fk.RefTable, pb.Source) && len(fk.Cols) == len(fk.RefCols) {
				var p [][2]string
				for i := range fk.Cols {
					p = append(p, [2]string{fk.Cols[i], fk.RefCols[i]})
				}
				add("foreign key "+fk.Name, p)
			}
		}
		for _, fk := range tb.FKs {
			if strings.EqualFold(fk.RefTable, pa.Source) && len(fk.Cols) == len(fk.RefCols) {
				var p [][2]string
				for i := range fk.Cols {
					p = append(p, [2]string{fk.RefCols[i], fk.Cols[i]})
				}
				add("foreign key "+fk.Name, p)
			}
		}
	}
	// <table>_id or <table>id pointing at the other table's single-column key.
	names := func(pt *projectTable) []string {
		n := strings.ToLower(pt.Name)
		out := []string{n}
		if strings.HasSuffix(n, "s") {
			out = append(out, strings.TrimSuffix(n, "s"))
		}
		return out
	}
	keyOf := func(t *mTable) string {
		if len(t.PK) == 1 {
			return t.PK[0]
		}
		if c := t.col("id"); c != nil {
			return c.Src
		}
		return ""
	}
	pointer := func(from *mTable, to *projectTable, toT *mTable, flip bool) {
		k := keyOf(toT)
		if k == "" {
			return
		}
		for _, c := range from.Cols {
			l := strings.ToLower(c.Src)
			for _, n := range names(to) {
				if l == n+"_id" || l == n+"id" || l == n+"_"+strings.ToLower(k) {
					if comparableKinds(c.Kind, toT.col(k).Kind) {
						p := [2]string{c.Src, k}
						if flip {
							p = [2]string{k, c.Src}
						}
						add(fmt.Sprintf("%s names %s's key", c.Src, to.Name), [][2]string{p})
					}
				}
			}
		}
	}
	pointer(ta, pb, tb, false)
	pointer(tb, pa, ta, true)
	// The same, not generic, column name with comparable types.
	generic := map[string]bool{"id": true, "name": true, "description": true, "created": true, "updated": true, "created_at": true,
		"updated_at": true, "modified": true, "status": true, "type": true, "code": true, "notes": true, "active": true}
	for _, c := range ta.Cols {
		if generic[strings.ToLower(c.Src)] {
			continue
		}
		if o := tb.col(c.Src); o != nil && comparableKinds(c.Kind, o.Kind) {
			add("the same column name", [][2]string{{c.Src, o.Src}})
		}
	}
	return out
}

// comparableKinds says whether columns of these kinds can be compared
// sensibly (text with number, or a date with a number, can't).
func comparableKinds(a, b string) bool {
	group := func(k string) string {
		switch {
		case isNumericKind(k) || k == kBool:
			return "number"
		case isTextKind(k) || k == kUUID || k == kJSON:
			return "text"
		case k == kDate || k == kTimestamp || k == kTimestampTZ:
			return "date"
		case k == kTime:
			return "time"
		case k == kBinary || k == kVarbinary || k == kBlob:
			return "binary"
		}
		return ""
	}
	ga, gb := group(a), group(b)
	return ga == "" || gb == "" || ga == gb
}
