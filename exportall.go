package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Export all queries of a project: every saved query runs and is exported on
// its own staging copy, several at once - one worker per CPU core, at least
// exportAllMinThreads - so the reading, querying and writing of the queries
// overlap. The project's own run (the grid) is left alone.

const exportAllMinThreads = 5

// exportAllThreads is how many queries are exported at once.
func exportAllThreads() int { return max(runtime.NumCPU(), exportAllMinThreads) }

type exportAllItem struct {
	Name   string           `json:"name"`
	State  string           `json:"state"` // waiting, loading, querying, exporting, done, failed, cancelled
	Tables []runTable       `json:"tables,omitempty"`
	Rows   int              `json:"rows"`
	Files  []map[string]any `json:"files,omitempty"`
	Error  string           `json:"error,omitempty"`
	Ms     int64            `json:"elapsedMs,omitempty"`

	sql string
	run *projectRun // while it runs: its tables' progress
}

type exportAllJob struct {
	mu      sync.Mutex
	format  string
	threads int
	items   []*exportAllItem
	started time.Time
	elapsed time.Duration
	cancel  context.CancelFunc
	done    chan struct{}
}

func (j *exportAllJob) running() bool {
	select {
	case <-j.done:
		return false
	default:
		return true
	}
}

func (j *exportAllJob) snapshot() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	items := make([]exportAllItem, len(j.items))
	active := 0
	for i, it := range j.items {
		items[i] = *it
		if it.run != nil {
			if t := it.run.snapshot()["tables"].([]runTable); len(t) > 0 {
				items[i].Tables = t
			}
		}
		if it.State == "loading" || it.State == "querying" || it.State == "exporting" {
			active++
		}
	}
	el := j.elapsed
	if j.running() {
		el = time.Since(j.started)
	}
	return map[string]any{"running": j.running(), "format": j.format, "threads": j.threads, "active": active, "items": items, "elapsedMs": el.Milliseconds()}
}

func (j *exportAllJob) set(f func()) {
	j.mu.Lock()
	defer j.mu.Unlock()
	f()
}

func (s *server) exportAllRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/project/exportall", s.handlePrjExportAll)
	mux.HandleFunc("GET /api/project/exportall", s.handlePrjExportAllState)
	mux.HandleFunc("POST /api/project/exportall/cancel", s.handlePrjExportAllCancel)
}

func (s *server) handlePrjExportAll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string `json:"project"`
		Format  string `json:"format"`
		Queries []struct {
			Name string `json:"name"`
			SQL  string `json:"sql"`
		} `json:"queries"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if _, ok := exportFormats[req.Format]; !ok && req.Format != "all" {
		writeErr(w, 400, fmt.Errorf("unknown export format %q", req.Format))
		return
	}
	if len(req.Queries) == 0 {
		writeErr(w, 400, errors.New("no queries to export"))
		return
	}
	ps, err := s.project(r, req.Project)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &exportAllJob{format: req.Format, threads: min(exportAllThreads(), len(req.Queries)), started: time.Now(), cancel: cancel, done: make(chan struct{})}
	for _, q := range req.Queries {
		job.items = append(job.items, &exportAllItem{Name: q.Name, State: "waiting", sql: strings.TrimSpace(strings.TrimRight(strings.TrimSpace(q.SQL), ";"))})
	}
	ps.mu.Lock()
	if ps.exportAll != nil && ps.exportAll.running() {
		ps.mu.Unlock()
		cancel()
		writeErr(w, 409, errors.New("the project's queries are being exported already"))
		return
	}
	ps.exportAll = job
	name := ps.name
	ps.mu.Unlock()
	logAction(r, "[project %s] export all: %d queries to %s, %d at once", name, len(job.items), req.Format, job.threads)
	r = r.WithContext(context.WithoutCancel(r.Context())) // the workers log after this answer
	work := make(chan *exportAllItem)
	var wg sync.WaitGroup
	for range job.threads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range work {
				s.exportAllOne(r, ctx, ps, job, it)
			}
		}()
	}
	go func() {
		defer close(job.done)
		defer cancel()
		for _, it := range job.items {
			if ctx.Err() != nil {
				job.set(func() { it.State = "cancelled" })
				continue
			}
			select {
			case work <- it:
			case <-ctx.Done():
				job.set(func() { it.State = "cancelled" })
			}
		}
		close(work)
		wg.Wait()
		job.mu.Lock()
		job.elapsed = time.Since(job.started)
		job.mu.Unlock()
	}()
	writeJSON(w, 200, job.snapshot())
}

// exportAllOne runs one query on a staging copy of its own and exports it.
func (s *server) exportAllOne(r *http.Request, ctx context.Context, ps *projectSession, job *exportAllJob, it *exportAllItem) {
	start := time.Now()
	fail := func(err error) {
		job.set(func() {
			it.run = nil
			it.Ms = time.Since(start).Milliseconds()
			if ctx.Err() != nil {
				it.State = "cancelled"
				return
			}
			it.State, it.Error = "failed", err.Error()
		})
	}
	if err := checkProjectQuery(it.sql); err != nil {
		fail(err)
		return
	}
	ps.mu.Lock()
	run, jobs, rctx, bad := ps.prepareRun(ctx, it.sql, 1)
	limits, name := ps.data.Limits, ps.name
	ps.mu.Unlock()
	if bad != nil {
		fail(errors.New(fmt.Sprint(bad["error"])))
		return
	}
	defer run.close()
	stop := context.AfterFunc(ctx, run.cancel) // Cancel stops the reads too
	defer stop()
	job.set(func() { it.State, it.run = "loading", run })
	run.execute(rctx, jobs, limits)
	run.mu.Lock()
	state, errText, qdb := run.state, run.err, run.qdb
	run.mu.Unlock()
	if state != "done" || qdb == nil {
		fail(errors.New(cmpOr(errText, "cancelled")))
		return
	}
	job.set(func() { it.State, it.Tables = "exporting", run.snapshot()["tables"].([]runTable) })
	files, err := s.exportProjectResult(r, ctx, name, qdb, it.sql, job.format, it.Name)
	if err != nil {
		fail(err)
		return
	}
	job.set(func() {
		it.State, it.Files, it.run, it.Ms = "done", files, nil, time.Since(start).Milliseconds()
		if len(files) > 0 {
			it.Rows, _ = files[0]["rows"].(int)
		}
	})
}

func (s *server) handlePrjExportAllState(w http.ResponseWriter, r *http.Request) {
	ps, err := s.project(r, "")
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	ps.mu.Lock()
	job := ps.exportAll
	ps.mu.Unlock()
	if job == nil {
		writeJSON(w, 200, map[string]any{"running": false, "threads": exportAllThreads()})
		return
	}
	writeJSON(w, 200, job.snapshot())
}

func (s *server) handlePrjExportAllCancel(w http.ResponseWriter, r *http.Request) {
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
	ps.mu.Lock()
	job := ps.exportAll
	ps.mu.Unlock()
	if job != nil {
		job.cancel()
		logAction(r, "[project %s] export all cancelled", ps.name)
	}
	writeJSON(w, 200, map[string]any{"cancelled": job != nil})
}
