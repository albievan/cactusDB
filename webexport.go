package main

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"time"
)

// "Export all tables" in the web UI: the tables are exported in parallel in
// the background, and the page polls for each table's progress.

const webExportThreads = 4

type webBatch struct {
	*exportBatch
	session string
	label   string
	dir     string // the export folder when the export started
	cancel  context.CancelFunc
}

var jobStateNames = map[int32]string{jobWaiting: "waiting", jobCounting: "counting", jobRunning: "running", jobDone: "done", jobFailed: "failed"}

// handleExportAll starts exporting every table of the session's database.
func (s *server) handleExportAll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string `json:"session"`
		Format  string `json:"format"`
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
	format, err := parseExportFormat(req.Format)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	groups, err := listObjects(r.Context(), ss.engine, ss.db)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	var targets []tableRef
	for _, g := range groups {
		if g.Type == ObjTable {
			for _, t := range g.Items {
				targets = append(targets, tableRef{typ: ObjTable, name: t})
			}
		}
	}
	if len(targets) == 0 {
		writeErr(w, 400, errors.New("the database has no tables"))
		return
	}

	// The export outlives this request; Disconnect cancels it.
	ctx, cancel := context.WithCancel(context.Background())
	id := newID()
	label := ss.label
	logAction(r, "[%s] export all %d table(s) as %s, job %.8s", label, len(targets), format, id)
	dir := s.exportFolder()
	b := startExportBatch(ctx, ss.engine, ss.db, dir, format, targets, webExportThreads, func(j *exportJob) {
		if j.err != nil {
			logAction(r, "[%s] export %s FAILED: %v", label, j.table.name, j.err)
		} else {
			logAction(r, "[%s] export %s: %d row(s) to %s", label, j.table.name, j.rows.Load(), j.path)
		}
	})
	s.mu.Lock()
	if s.batches == nil {
		s.batches = map[string]*webBatch{}
	}
	s.batches[id] = &webBatch{exportBatch: b, session: req.Session, label: label, dir: dir, cancel: cancel}
	s.mu.Unlock()
	go func() {
		<-b.done
		cancel()
		time.AfterFunc(30*time.Minute, func() { // keep the result for late polls
			s.mu.Lock()
			delete(s.batches, id)
			s.mu.Unlock()
		})
	}()
	writeJSON(w, 200, map[string]any{"job": id, "tables": len(targets), "format": format})
}

// handleExportAllStatus reports the progress of each table of an export.
func (s *server) handleExportAllStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	b := s.batches[r.PathValue("job")]
	s.mu.Unlock()
	if b == nil {
		writeErr(w, 404, errors.New("export not found"))
		return
	}
	done := false
	select {
	case <-b.done:
		done = true
	default:
	}
	tables := make([]map[string]any, len(b.jobs))
	for i, j := range b.jobs {
		state := j.state.Load()
		t := map[string]any{"name": j.table.name, "state": jobStateNames[state], "rows": j.rows.Load(), "total": j.total.Load()}
		switch state {
		case jobDone:
			t["file"], t["path"], t["download"] = filepath.Base(j.path), j.path, downloadURL(j.path)
		case jobFailed:
			t["error"] = j.err.Error()
		}
		tables[i] = t
	}
	writeJSON(w, 200, map[string]any{"done": done, "format": b.format, "folder": exportSubdir(b.dir, exportFormats[b.format]), "tables": tables})
}

// cancelBatches stops the exports of a session that is disconnecting.
// The caller holds s.mu.
func (s *server) cancelBatches(session string) {
	for _, b := range s.batches {
		if b.session == session {
			b.cancel()
		}
	}
}
