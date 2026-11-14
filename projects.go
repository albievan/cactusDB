package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Projects (☰ → Projects): several saved connections linked under aliases,
// a chosen set of their tables, and queries that join tables of different
// databases (run in a temporary SQLite database, see projectrun.go). The
// storage and its sealing are in projects_store.go.
//
// An open project is a projectSession: one ordinary read-only *session per
// linked database, kept out of server.sessions so the single-database routes
// can't reach them.

type projectSession struct {
	id        string
	projectID int64
	key       []byte

	mu       sync.Mutex
	name     string
	data     projectData
	boxes    map[int64]*secretBox // the key of each linked connection (nil: it has no password)
	conns    map[int64]SavedConn  // each linked connection, unlocked (for Retry)
	links    map[string]*session  // by alias: the connected ones
	failed   map[string]string    // by alias: why it isn't connected
	broken   map[string]bool      // by alias: its saved connection was deleted
	lastUsed time.Time
	run      *projectRun        // the last run (projectrun.go)
	cols     map[string]*mTable // each table's columns, read once (until Refresh)
}

// linkable says whether a database type can be linked into a project: the
// SQL-speaking ones.
func linkable(engine string) bool {
	e := engineByID(engine)
	if e == nil {
		return false
	}
	return e.Group == "Relational" || e.Group == "Cloud" || dialect(engine) == EnginePostgres
}

func (s *server) projectMap() map[string]*projectSession {
	if s.projects == nil {
		s.projects = map[string]*projectSession{}
	}
	return s.projects
}

// project returns the open project session id (from the body, or ?project=).
func (s *server) project(r *http.Request, id string) (*projectSession, error) {
	if id == "" {
		id = r.URL.Query().Get("project")
	}
	s.mu.Lock()
	ps, ok := s.projectMap()[id]
	s.mu.Unlock()
	if !ok {
		return nil, errors.New("the project isn't open (closed or expired) - open it again")
	}
	ps.mu.Lock()
	ps.lastUsed = time.Now()
	ps.mu.Unlock()
	return ps, nil
}

// closeLinks disconnects every linked database. The caller holds ps.mu.
func (ps *projectSession) closeLinks() {
	for a, ss := range ps.links {
		ss.db.Close()
		delete(ps.links, a)
	}
}

func (s *server) closeProject(id string) {
	s.mu.Lock()
	ps, ok := s.projectMap()[id]
	delete(s.projectMap(), id)
	s.mu.Unlock()
	if !ok {
		return
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.run != nil {
		ps.run.close()
		ps.run = nil
	}
	ps.closeLinks()
}

// closeIdleProjects closes projects unused for idle (see closeIdleSessions).
func (s *server) closeIdleProjects(idle time.Duration) {
	s.mu.Lock()
	var old []string
	for id, ps := range s.projectMap() {
		ps.mu.Lock()
		busy := ps.run != nil && ps.run.running()
		if !busy && time.Since(ps.lastUsed) >= idle {
			old = append(old, id)
		}
		ps.mu.Unlock()
	}
	s.mu.Unlock()
	for _, id := range old {
		log.Printf("closing project session %.8s: unused for %s", id, idle)
		s.closeProject(id)
	}
}

func (s *server) closeAllProjects() {
	s.mu.Lock()
	var ids []string
	for id := range s.projectMap() {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.closeProject(id)
	}
}

// ---- unlocking ----------------------------------------------------------------

// projectUnlock is what the page sends to open a project, or to link
// password-protected connections: a password per connection id, or the
// superuser password for all of them.
type projectUnlock struct {
	LockPasswords map[string]string `json:"lockPasswords"`
	SuperPassword string            `json:"superPassword"`
}

// unlockLinks unlocks the saved connections ids. A deleted one is reported
// in missing. On a wrong or missing password it answers the request (403
// with locked, savedId and name, like a single connection) and returns false.
func (s *server) unlockLinks(w http.ResponseWriter, r *http.Request, ids []int64, u projectUnlock) (map[int64]SavedConn, map[int64]*secretBox, []int64, bool) {
	conns := map[int64]SavedConn{}
	boxes := map[int64]*secretBox{}
	var missing []int64
	for _, id := range ids {
		c, err := s.store.Get(id)
		if errors.Is(err, sql.ErrNoRows) {
			missing = append(missing, id)
			continue
		}
		if err != nil {
			writeErr(w, 500, err)
			return nil, nil, nil, false
		}
		pw := u.LockPasswords[strconv.FormatInt(id, 10)]
		super := u.SuperPassword
		if pw != "" {
			super = ""
		}
		if c.Lock != "" && pw == "" && super == "" {
			writeJSON(w, 403, map[string]any{"error": fmt.Sprintf("the connection %q needs its password", c.Name),
				"locked": true, "savedId": id, "name": c.Name})
			return nil, nil, nil, false
		}
		// unlockConnAs answers a failure itself; the answer is held back to
		// add which connection it was, so the page asks for the right one.
		cw := &heldAnswer{h: http.Header{}, status: 200}
		uc, box, ok := s.unlockConnAs(cw, r, id, pw, super)
		if !ok {
			var m map[string]any
			if json.Unmarshal(cw.body.Bytes(), &m) != nil {
				m = map[string]any{"error": strings.TrimSpace(cw.body.String())}
			}
			if m["locked"] == true || cw.status == 403 {
				m["savedId"], m["name"] = id, c.Name
			}
			for k, v := range cw.h {
				if k != "Content-Length" && k != "Content-Type" {
					w.Header()[k] = v
				}
			}
			writeJSON(w, cw.status, m)
			return nil, nil, nil, false
		}
		conns[id], boxes[id] = uc, box
	}
	return conns, boxes, missing, true
}

// heldAnswer is a ResponseWriter that keeps the answer instead of sending it.
type heldAnswer struct {
	h      http.Header
	status int
	body   bytes.Buffer
}

func (a *heldAnswer) Header() http.Header         { return a.h }
func (a *heldAnswer) WriteHeader(status int)      { a.status = status }
func (a *heldAnswer) Write(b []byte) (int, error) { return a.body.Write(b) }

// connectLinks connects every link of ps that isn't connected yet, in
// parallel, each read-only like a single connection. The caller holds ps.mu.
func (ps *projectSession) connectLinks(ctx context.Context, only string) {
	type result struct {
		alias string
		ss    *session
		err   error
	}
	var wg sync.WaitGroup
	results := make(chan result, len(ps.data.Links))
	for _, l := range ps.data.Links {
		if (only != "" && l.Alias != only) || ps.links[l.Alias] != nil {
			continue
		}
		c, ok := ps.conns[l.ConnID]
		if !ok {
			ps.broken[l.Alias] = true
			continue
		}
		if !linkable(c.Engine) {
			ps.failed[l.Alias] = fmt.Sprintf("%s databases can't be linked into a project yet (only SQL databases)", engineName(c.Engine))
			continue
		}
		wg.Add(1)
		go func(alias string, c SavedConn) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			db, err := openDB(cctx, c.ConnParams)
			if err != nil {
				results <- result{alias: alias, err: explainConnErr(c.ConnParams, err)}
				return
			}
			results <- result{alias: alias, ss: &session{engine: dialect(c.Engine), flavor: c.Engine, label: c.Name,
				dbName: ddlFileName(c.ConnParams), folder: connFolderName(c.Name), db: db, savedID: c.ID,
				connBox: ps.boxes[c.ID], lastUsed: time.Now()}}
		}(l.Alias, c)
	}
	wg.Wait()
	close(results)
	for res := range results {
		if res.err != nil {
			ps.failed[res.alias] = res.err.Error()
			continue
		}
		delete(ps.failed, res.alias)
		ps.links[res.alias] = res.ss
	}
}

// state is what the page shows of an open project. The caller holds ps.mu.
func (ps *projectSession) state() map[string]any {
	links := []map[string]any{}
	for _, l := range ps.data.Links {
		m := map[string]any{"alias": l.Alias, "connId": l.ConnID}
		if c, ok := ps.conns[l.ConnID]; ok {
			m["name"], m["engine"], m["engineName"] = c.Name, c.Engine, engineName(c.Engine)
		}
		switch {
		case ps.broken[l.Alias]:
			m["state"], m["error"] = "broken", "its saved connection was deleted: re-point it at another one"
		case ps.links[l.Alias] != nil:
			m["state"] = "connected"
		case ps.failed[l.Alias] != "":
			m["state"], m["error"] = "failed", ps.failed[l.Alias]
		default:
			m["state"] = "connecting"
		}
		links = append(links, m)
	}
	return map[string]any{"project": ps.id, "id": ps.projectID, "name": ps.name, "description": ps.data.Description,
		"links": links, "tables": ps.data.Tables, "limits": ps.data.Limits, "maxLinks": maxProjectLinks}
}

// ---- handlers -----------------------------------------------------------------

func (s *server) projectRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects", s.handleListProjects)
	mux.HandleFunc("POST /api/projects", s.handleCreateProject)
	mux.HandleFunc("POST /api/projects/{id}/open", s.handleOpenProject)
	mux.HandleFunc("GET /api/project", s.handleProjectState)
	mux.HandleFunc("POST /api/project/save", s.handleSaveProject)
	mux.HandleFunc("POST /api/project/retry", s.handleRetryProjectLink)
	mux.HandleFunc("POST /api/project/close", s.handleCloseProject)
	mux.HandleFunc("POST /api/project/delete", s.handleDeleteProject)
	mux.HandleFunc("GET /api/project/objects", s.handleProjectObjects)
	s.projectRunRoutes(mux)
}

func (s *server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.Projects()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	var ok []string
	for _, e := range engines {
		if linkable(e.ID) {
			ok = append(ok, e.ID)
		}
	}
	writeJSON(w, 200, map[string]any{"projects": list, "maxLinks": maxProjectLinks, "linkable": ok})
}

// handleCreateProject adds an empty project; the page then opens it.
func (s *server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	d := projectData{Description: strings.TrimSpace(req.Description), Links: []projectLink{}, Tables: []projectTable{}, Limits: defaultProjectLimits()}
	if err := checkProjectData(&d); err != nil {
		writeErr(w, 400, err)
		return
	}
	id, _, err := s.store.CreateProject(req.Name, d, nil)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	logAction(r, "create project %q", strings.TrimSpace(req.Name))
	s.event(r, "project", false, "created the project %q", strings.TrimSpace(req.Name))
	writeJSON(w, 200, map[string]any{"id": id})
}

// handleOpenProject unlocks every linked connection (their passwords, or the
// superuser password), opens the project's key and connects every link. A
// link that can't connect leaves the project open with that link marked; a
// missing password answers 403 (the page asks for it and tries again).
func (s *server) handleOpenProject(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, errors.New("bad id"))
		return
	}
	var req projectUnlock
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	ids, err := s.store.projectLinks(id)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	conns, boxes, _, ok := s.unlockLinks(w, r, ids, req)
	if !ok {
		return
	}
	key, err := s.store.ProjectKey(id, boxes)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeErr(w, 404, errors.New("project not found"))
		return
	case err != nil:
		writeErr(w, 500, fmt.Errorf("can't open the project: %w", err))
		return
	}
	p, err := s.store.GetProject(id, key)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	ps := &projectSession{id: newID(), projectID: id, key: key, name: p.Name, data: p.projectData, boxes: boxes, conns: conns,
		links: map[string]*session{}, failed: map[string]string{}, broken: map[string]bool{}, lastUsed: time.Now()}
	ps.mu.Lock()
	ps.connectLinks(r.Context(), "")
	st := ps.state()
	ps.mu.Unlock()
	s.mu.Lock()
	s.projectMap()[ps.id] = ps
	s.mu.Unlock()
	logAction(r, "open project %q (%d databases)", p.Name, len(p.Links))
	s.event(r, "project", false, "opened the project %q", p.Name)
	writeJSON(w, 200, st)
}

func (s *server) handleProjectState(w http.ResponseWriter, r *http.Request) {
	ps, err := s.project(r, "")
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	writeJSON(w, 200, ps.state())
}

// handleSaveProject saves the open project: name, description, links,
// tables and limits. Newly linked connections are unlocked (their passwords
// in the request) and connected; removed ones are disconnected. With
// duplicate, a copy is saved under the new name instead (and opened).
func (s *server) handleSaveProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project     string         `json:"project"`
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Links       []projectLink  `json:"links"`
		Tables      []projectTable `json:"tables"`
		Limits      projectLimits  `json:"limits"`
		Duplicate   bool           `json:"duplicate"`
		projectUnlock
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
	d := projectData{Description: strings.TrimSpace(req.Description), Links: req.Links, Tables: req.Tables, Limits: req.Limits}
	if d.Links == nil {
		d.Links = []projectLink{}
	}
	if d.Tables == nil {
		d.Tables = []projectTable{}
	}
	if err := checkProjectData(&d); err != nil {
		writeErr(w, 400, err)
		return
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	var added []int64
	for _, l := range d.Links {
		if _, ok := ps.boxes[l.ConnID]; !ok {
			added = append(added, l.ConnID)
		}
	}
	conns, boxes, missing, ok := s.unlockLinks(w, r, added, req.projectUnlock)
	if !ok {
		return
	}
	if len(missing) > 0 {
		writeErr(w, 400, errors.New("that saved connection doesn't exist any more"))
		return
	}
	for _, c := range conns {
		if !linkable(c.Engine) {
			writeErr(w, 400, fmt.Errorf("%s databases (%q) can't be linked into a project yet: only SQL databases can", engineName(c.Engine), c.Name))
			return
		}
	}
	allBoxes := map[int64]*secretBox{}
	for id, b := range ps.boxes {
		allBoxes[id] = b
	}
	for id, b := range boxes {
		allBoxes[id] = b
	}
	for _, l := range d.Links { // a broken link can't be kept: it has no key to split to
		if _, ok := allBoxes[l.ConnID]; !ok {
			writeErr(w, 400, fmt.Errorf("the link %q points at a deleted connection: re-point or remove it first", l.Alias))
			return
		}
	}
	if req.Duplicate {
		id, _, err := s.store.CreateProject(req.Name, d, allBoxes)
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		logAction(r, "duplicate project %q as %q", ps.name, req.Name)
		s.event(r, "project", false, "duplicated the project %q as %q", ps.name, strings.TrimSpace(req.Name))
		writeJSON(w, 200, map[string]any{"id": id, "duplicated": true})
		return
	}
	if err := s.store.SaveProject(ps.projectID, ps.key, req.Name, d, allBoxes); err != nil {
		writeErr(w, 400, err)
		return
	}
	// Follow the links: disconnect removed ones and ones re-pointed or renamed.
	for alias, ss := range ps.links {
		keep := slices.ContainsFunc(d.Links, func(l projectLink) bool { return l.Alias == alias && l.ConnID == ss.savedID })
		if !keep {
			ss.db.Close()
			delete(ps.links, alias)
		}
	}
	ps.failed, ps.broken = map[string]string{}, map[string]bool{}
	for id, c := range conns {
		ps.conns[id] = c
	}
	ps.boxes = allBoxes
	oldName := ps.name
	ps.name, ps.data = strings.TrimSpace(req.Name), d
	ps.connectLinks(r.Context(), "")
	if oldName != ps.name {
		logAction(r, "rename project %q to %q", oldName, ps.name)
	}
	logAction(r, "save project %q (%d databases, %d tables)", ps.name, len(d.Links), len(d.Tables))
	s.event(r, "project", false, "saved the project %q", ps.name)
	writeJSON(w, 200, ps.state())
}

func (s *server) handleRetryProjectLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string `json:"project"`
		Alias   string `json:"alias"`
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
	delete(ps.failed, req.Alias)
	ps.connectLinks(r.Context(), req.Alias)
	writeJSON(w, 200, ps.state())
}

func (s *server) handleCloseProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string `json:"project"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.closeProject(req.Project)
	logAction(r, "close project session %.8s", req.Project)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// handleDeleteProject deletes the open project (opening it needed every
// linked connection's password, so deleting does too) and closes it.
func (s *server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
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
	if err := s.store.DeleteProject(ps.projectID); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.closeProject(ps.id)
	logAction(r, "delete project %q", ps.name)
	s.event(r, "project", false, "deleted the project %q", ps.name)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// handleProjectObjects lists the tables and views of one linked database.
func (s *server) handleProjectObjects(w http.ResponseWriter, r *http.Request) {
	ps, err := s.project(r, "")
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	alias := r.URL.Query().Get("alias")
	ps.mu.Lock()
	ss := ps.links[alias]
	ps.mu.Unlock()
	if ss == nil {
		writeErr(w, 400, fmt.Errorf("%q isn't connected", alias))
		return
	}
	objects, err := listObjects(r.Context(), ss.engine, ss.db)
	if err != nil {
		writeErr(w, 400, fmt.Errorf("%s: %w", alias, err))
		return
	}
	writeJSON(w, 200, map[string]any{"alias": alias, "objects": objects})
}
