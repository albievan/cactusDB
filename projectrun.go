package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Running a project query: the rows it needs are read from each source
// database (read-only, as any query: see streamRows), copied into a private
// SQLite file per database alias (the staging), and the query runs there, on
// a read-only connection that attaches those files read-only under their
// aliases, so fb.customer resolves as written. The staging of the last run
// is kept until the next run or until the project closes, for the grid and
// the exports (they hold exactly the rows shown). Nothing is ever written to
// a linked database.

type runTable struct {
	Alias  string   `json:"alias"`
	Name   string   `json:"name"`
	Rows   int64    `json:"rows"`
	Done   bool     `json:"done"`
	Pushed []string `json:"pushed,omitempty"` // conditions sent to the source
	Cols   int      `json:"columns"`
	Notes  []string `json:"notes,omitempty"`
}

type projectRun struct {
	mu      sync.Mutex
	sql     string
	state   string // loading, querying, done, failed, cancelled
	err     string
	errKind string // query (the SQL is wrong), source (a database failed), limit
	tables  []*runTable
	started time.Time
	elapsed time.Duration
	result  map[string]any
	limit   int

	cancel context.CancelFunc
	done   chan struct{}
	dir    string  // the staging folder
	qdb    *sql.DB // the read-only query connection (for exports)
	bytes  int64   // staged so far (approximate)
}

func (r *projectRun) running() bool {
	select {
	case <-r.done:
		return false
	default:
		return true
	}
}

// close cancels the run and removes its staging.
func (r *projectRun) close() {
	r.cancel()
	<-r.done
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.qdb != nil {
		r.qdb.Close()
		r.qdb = nil
	}
	if r.dir != "" {
		os.RemoveAll(r.dir)
		r.dir = ""
	}
}

func (r *projectRun) fail(kind string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == "cancelled" {
		return
	}
	r.state, r.errKind, r.err = "failed", kind, err.Error()
}

func (r *projectRun) snapshot() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	tables := make([]runTable, len(r.tables))
	for i, t := range r.tables {
		tables[i] = *t
	}
	out := map[string]any{"state": r.state, "sql": r.sql, "tables": tables, "elapsedMs": r.elapsed.Milliseconds()}
	if r.state == "loading" || r.state == "querying" {
		out["elapsedMs"] = time.Since(r.started).Milliseconds()
	}
	if r.err != "" {
		out["error"], out["errorKind"] = r.err, r.errKind
	}
	if r.result != nil {
		out["result"] = r.result
	}
	return out
}

// placeholderFor is the n-th (from 1) parameter placeholder of an engine.
func placeholderFor(engine string, n int) string {
	switch engine {
	case EnginePostgres:
		return "$" + strconv.Itoa(n)
	case EngineMSSQL:
		return "@p" + strconv.Itoa(n)
	case EngineOracle:
		return ":" + strconv.Itoa(n)
	}
	return "?"
}

// caseInsensitive: text in these databases compares case-insensitively by
// default, so its staged copy does too (COLLATE NOCASE).
func caseInsensitive(flavor string) bool {
	switch flavor {
	case EngineMySQL, EngineMariaDB, EngineMSSQL, EngineAzureSQL, EngineAccess:
		return true
	}
	return false
}

// stagedType is the SQLite type of a staged column ("" keeps each value as
// it is: decimals are stored exact, see stageValue).
func stagedType(kind string) string {
	switch kind {
	case kInt16, kInt32, kInt64, kBool:
		return "INTEGER"
	case kFloat32, kFloat64:
		return "REAL"
	case kChar, kVarchar, kText, kUUID, kJSON, kDate, kTime, kTimestamp, kTimestampTZ:
		return "TEXT"
	case kBinary, kVarbinary, kBlob:
		return "BLOB"
	}
	return ""
}

// stageValue converts a source value for the staging: dates as ISO 8601 text,
// booleans as 0/1, decimals as integers or floats when that is exact (15
// significant digits) and as text otherwise.
func stageValue(v any, c *mCol, trim bool) any {
	v = convertValue(v, c, EngineSQLite)
	switch x := v.(type) {
	case bool:
		if x {
			return int64(1)
		}
		return int64(0)
	case string:
		if trim {
			x = strings.TrimRight(x, " ")
		}
		if c.Kind == kDecimal {
			return exactNumber(x)
		}
		return x
	case json.Number:
		return exactNumber(string(x))
	case time.Time:
		return x.UTC().Format("2006-01-02 15:04:05.999999")
	}
	return v
}

// exactNumber is s as an int64 or float64 when either holds it exactly, else s.
func exactNumber(s string) any {
	s = strings.TrimSpace(s)
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	digits := 0
	seenNonZero := false
	for _, c := range strings.TrimLeft(s, "+-") {
		if c == 'e' || c == 'E' {
			break
		}
		if c >= '0' && c <= '9' {
			if c != '0' {
				seenNonZero = true
			}
			if seenNonZero {
				digits++
			}
		}
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && digits <= 15 && !math.IsInf(f, 0) {
		if f == math.Trunc(f) && math.Abs(f) < 1<<53 && !strings.ContainsAny(s, ".eE") {
			return int64(f)
		}
		return f
	}
	return s
}

func approxSize(v any) int64 {
	switch x := v.(type) {
	case string:
		return int64(len(x)) + 2
	case []byte:
		return int64(len(x)) + 2
	}
	return 9
}

// ---- handlers -------------------------------------------------------------------

func (s *server) projectRunRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/project/run", s.handleProjectRun)
	mux.HandleFunc("GET /api/project/run", s.handleProjectRunState)
	mux.HandleFunc("POST /api/project/cancel", s.handleProjectCancel)
	mux.HandleFunc("POST /api/project/export", s.handleProjectExport)
	s.exportAllRoutes(mux)
	mux.HandleFunc("POST /api/project/refresh", s.handleProjectRefresh)
}

// columnsOf reads (once per open project, until Refresh) a table's columns
// and their types from its database's catalog. The caller holds ps.mu.
func (ps *projectSession) columnsOf(ctx context.Context, pt *projectTable) (*mTable, error) {
	if ps.cols == nil {
		ps.cols = map[string]*mTable{}
	}
	key := strings.ToLower(pt.Alias + "\x00" + pt.Source)
	if t := ps.cols[key]; t != nil {
		return t, nil
	}
	ss := ps.links[pt.Alias]
	if ss == nil {
		return nil, fmt.Errorf("%s isn't connected", pt.Alias)
	}
	t, err := readTable(ctx, ss.engine, ss.db, pt.Source)
	if err != nil {
		return nil, fmt.Errorf("%s.%s: %w", pt.Alias, pt.Name, err)
	}
	ps.cols[key] = t
	return t, nil
}

func (s *server) handleProjectRefresh(w http.ResponseWriter, r *http.Request) {
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
	ps.cols = nil
	ps.mu.Unlock()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// handleProjectRun checks the query, works out what to read, and starts
// loading it in the background; the page follows it with GET.
func (s *server) handleProjectRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string `json:"project"`
		SQL     string `json:"sql"`
		Limit   int    `json:"limit"`
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
	q := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(req.SQL), ";"))
	if err := checkProjectQuery(q); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error(), "errorKind": "query"})
		return
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	run, jobs, rctx, fail := ps.prepareRun(r.Context(), q, req.Limit)
	if fail != nil {
		writeJSON(w, 400, fail)
		return
	}
	if ps.run != nil {
		ps.run.close()
	}
	ps.run = run
	limits := ps.data.Limits
	logAction(r, "[project %s] run: %s", ps.name, oneLine(q))
	s.event(r, "query", false, "[project %s] run: %s", ps.name, oneLine(q))
	go run.execute(rctx, jobs, limits)
	writeJSON(w, 200, run.snapshot())
}

// prepareRun plans a run of the query q: each table to read, its columns
// and the conditions sent to the source. The caller holds ps.mu and starts
// it (go run.execute(rctx, jobs, ps.data.Limits)); on a problem, the answer
// for the client.
func (ps *projectSession) prepareRun(ctx context.Context, q string, limit int) (*projectRun, []stageJob, context.Context, map[string]any) {
	pq, err := analyzeProjectQuery(q, &ps.data)
	if err != nil {
		m := map[string]any{"error": err.Error(), "errorKind": "query"}
		var nip *errNotInProject
		if errors.As(err, &nip) {
			m["addTable"] = map[string]string{"alias": nip.Alias, "table": nip.Table}
		}
		return nil, nil, nil, m
	}
	// Every database the query needs must be connected.
	for _, ref := range pq.refs {
		if ps.links[ref.table.Alias] == nil {
			why := ps.failed[ref.table.Alias]
			if ps.broken[ref.table.Alias] {
				why = "its saved connection was deleted"
			}
			return nil, nil, nil, map[string]any{"error": fmt.Sprintf("the database %s isn't connected (%s): %s.%s can't be read", ref.table.Alias,
				cmpOr(why, "not connected"), ref.table.Alias, ref.table.Name), "errorKind": "source"}
		}
	}
	// What to read: each table once, its needed columns, conditions pushed down.
	type plan struct {
		pt   *projectTable
		t    *mTable
		cols []string
	}
	var plans []*plan
	kinds := map[*projectTable]*mTable{}
	for _, ref := range pq.refs {
		if _, ok := kinds[ref.table]; ok {
			continue
		}
		t, err := ps.columnsOf(ctx, ref.table)
		if err != nil {
			return nil, nil, nil, map[string]any{"error": err.Error(), "errorKind": "source"}
		}
		kinds[ref.table] = t
		all := make([]string, len(t.Cols))
		for i, c := range t.Cols {
			all[i] = c.Src
		}
		plans = append(plans, &plan{pt: ref.table, t: t, cols: pq.needed(ref.table, all)})
	}
	push := pq.pushdown(func(pt *projectTable, col string) (string, bool) {
		if t := kinds[pt]; t != nil {
			if c := t.col(col); c != nil && backendOf(ps.links[pt.Alias].db) == nil {
				return c.Kind, true
			}
		}
		return "", false
	}, func(pt *projectTable) bool { return ps.links[pt.Alias].engine == EngineSQLite })

	limits := ps.data.Limits
	rctx, cancel := context.WithTimeout(context.Background(), time.Duration(limits.TimeoutSec)*time.Second)
	run := &projectRun{sql: q, state: "loading", started: time.Now(), cancel: cancel, done: make(chan struct{}), limit: limit}
	for _, p := range plans {
		rt := &runTable{Alias: p.pt.Alias, Name: p.pt.Name, Cols: len(p.cols)}
		for _, c := range push[p.pt] {
			rt.Pushed = append(rt.Pushed, c.Text)
		}
		run.tables = append(run.tables, rt)
	}
	links := map[string]*session{}
	for a, ss := range ps.links {
		links[a] = ss
	}
	jobs := make([]stageJob, len(plans))
	for i, p := range plans {
		jobs[i] = stageJob{pt: p.pt, t: p.t, cols: p.cols, conds: push[p.pt], ss: links[p.pt.Alias], rt: run.tables[i]}
	}
	return run, jobs, rctx, nil
}

type stageJob struct {
	pt    *projectTable
	t     *mTable
	cols  []string
	conds []pushCond
	ss    *session
	rt    *runTable
}

// execute loads every table, then runs the query.
func (run *projectRun) execute(ctx context.Context, jobs []stageJob, limits projectLimits) {
	defer close(run.done)
	defer func() {
		run.mu.Lock()
		run.elapsed = time.Since(run.started)
		run.mu.Unlock()
	}()
	dir, err := os.MkdirTemp("", "cactusdb-project-")
	if err != nil {
		run.fail("source", fmt.Errorf("can't create the staging folder: %w", err))
		return
	}
	run.mu.Lock()
	run.dir = dir
	run.mu.Unlock()
	files := map[string]*sql.DB{}
	defer func() {
		for _, db := range files {
			db.Close()
		}
	}()
	for _, j := range jobs {
		db := files[j.pt.Alias]
		if db == nil {
			db, err = sql.Open("sqlite", "file:"+filepath.Join(dir, safeFileName(j.pt.Alias)+".db")+"?_pragma=journal_mode(OFF)&_pragma=synchronous(OFF)")
			if err != nil {
				run.fail("source", err)
				return
			}
			db.SetMaxOpenConns(1)
			files[j.pt.Alias] = db
		}
		if err := run.stage(ctx, db, j, limits); err != nil {
			switch {
			case errors.Is(ctx.Err(), context.DeadlineExceeded):
				run.fail("limit", fmt.Errorf("the run took longer than %d seconds (the limit in the project's Limits) while reading %s.%s", limits.TimeoutSec, j.pt.Alias, j.pt.Name))
			case ctx.Err() != nil:
			default:
				var le *limitErr
				if errors.As(err, &le) {
					run.fail("limit", err)
				} else {
					run.fail("source", fmt.Errorf("%s.%s: %w", j.pt.Alias, j.pt.Name, err))
				}
			}
			return
		}
	}
	for _, db := range files {
		db.Close()
	}
	files = map[string]*sql.DB{}
	// The query: a read-only connection with each staged file attached
	// read-only, then query_only, so nothing can change even the copies.
	run.mu.Lock()
	run.state = "querying"
	run.mu.Unlock()
	qdb, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		run.fail("source", err)
		return
	}
	qdb.SetMaxOpenConns(1)
	qdb.SetConnMaxIdleTime(0)
	attached := map[string]bool{}
	for _, j := range jobs {
		if attached[j.pt.Alias] {
			continue
		}
		attached[j.pt.Alias] = true
		u := (&url.URL{Scheme: "file", Path: filepath.Join(dir, safeFileName(j.pt.Alias)+".db"), RawQuery: "mode=ro"}).String()
		if _, err := qdb.ExecContext(ctx, `ATTACH DATABASE ? AS "`+strings.ReplaceAll(j.pt.Alias, `"`, `""`)+`"`, u); err != nil {
			qdb.Close()
			run.fail("source", fmt.Errorf("attaching the staged copy of %s: %w", j.pt.Alias, err))
			return
		}
	}
	if _, err := qdb.ExecContext(ctx, "PRAGMA query_only = 1"); err != nil {
		qdb.Close()
		run.fail("source", err)
		return
	}
	run.mu.Lock()
	run.qdb = qdb
	limit := run.limit
	q := run.sql
	run.mu.Unlock()
	if limit > maxProjectShow {
		limit = maxProjectShow
	}
	if limit <= 0 {
		limit = 1000
	}
	start := time.Now()
	var cols []string
	rows := [][]any{}
	// Every row the query returns is counted (up to maxProjectCount, so a
	// runaway join stops); only the first limit are kept.
	total, more := 0, false
	err = streamQuery(ctx, EngineSQLite, qdb, q, func(c []string) error { cols = c; return nil }, func(row []any) (bool, error) {
		if total >= maxProjectCount {
			more = true
			return false, nil
		}
		total++
		if len(rows) < limit {
			rows = append(rows, row)
		}
		return true, nil
	})
	if err != nil {
		if ctx.Err() == nil {
			run.fail("query", fmt.Errorf("the query (SQLite's SQL): %w", err))
		}
		return
	}
	if cols == nil {
		cols = []string{}
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.state == "cancelled" {
		return
	}
	run.state = "done"
	run.result = map[string]any{"sql": q, "columns": cols, "rows": rows, "total": total, "totalMore": more, "truncated": total > len(rows), "limit": limit,
		"elapsedMs": time.Since(start).Milliseconds()}
}

// maxProjectShow caps the rows a project query sends to the grid ("All").
const maxProjectShow = 1_000_000

// maxProjectCount caps the rows a project query counts past those it shows.
const maxProjectCount = 10_000_000

type limitErr struct{ msg string }

func (e *limitErr) Error() string { return e.msg }

func safeFileName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			return r
		}
		return '_'
	}, s)
}

// stage copies one table's needed columns (and rows) into the alias's file.
func (run *projectRun) stage(ctx context.Context, db *sql.DB, j stageJob, limits projectLimits) error {
	engine := j.ss.engine
	cols := make([]*mCol, len(j.cols))
	for i, name := range j.cols {
		cols[i] = j.t.col(name)
		if cols[i] == nil {
			cols[i] = &mCol{Src: name}
		}
	}
	nocase := caseInsensitive(j.ss.flavor)
	var def strings.Builder
	def.WriteString(`CREATE TABLE ` + quoteIdent(EngineSQLite, j.pt.Name) + ` (`)
	for i, c := range cols {
		if i > 0 {
			def.WriteString(", ")
		}
		typ := stagedType(c.Kind)
		def.WriteString(quoteIdent(EngineSQLite, c.Src))
		if typ != "" {
			def.WriteString(" " + typ)
		}
		if nocase && (typ == "TEXT" && (c.Kind == kChar || c.Kind == kVarchar || c.Kind == kText)) {
			def.WriteString(" COLLATE NOCASE")
		}
	}
	def.WriteString(")")
	if _, err := db.ExecContext(ctx, def.String()); err != nil {
		return err
	}
	// The read: only cactusdb's own SELECT, with quoted names and bound values.
	var q string
	var args []any
	if backendOf(j.ss.db) != nil {
		var err error
		if q, err = buildSelect(ctx, engine, j.ss.db, j.pt.Source); err != nil {
			return err
		}
	} else {
		quoted := make([]string, len(cols))
		for i, c := range cols {
			quoted[i] = quoteIdent(engine, c.Src)
		}
		q = "SELECT " + strings.Join(quoted, ", ") + " FROM " + fromOnly(engine, j.ss.db) + quoteObject(engine, j.pt.Source)
		for i, c := range j.conds {
			if i == 0 {
				q += " WHERE "
			} else {
				q += " AND "
			}
			col := quoteIdent(engine, j.t.col(c.Col).Src)
			switch c.Op {
			case "IS NULL", "IS NOT NULL":
				q += col + " " + c.Op
			case "BETWEEN":
				q += col + " BETWEEN " + placeholderFor(engine, len(args)+1) + " AND " + placeholderFor(engine, len(args)+2)
				args = append(args, c.Args...)
			default:
				q += col + " " + c.Op + " " + placeholderFor(engine, len(args)+1)
				args = append(args, c.Args...)
			}
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var stmt *sql.Stmt
	var srcIdx []int // backends: which source column feeds each staged column
	trim := make([]bool, len(cols))
	for i, c := range cols {
		trim[i] = j.ss.flavor == EngineFirebird && c.Kind == kChar
	}
	maxBytes := limits.MaxMB << 20
	var n int64
	err = streamRowsArgs(ctx, engine, j.ss.db, q, args, func(names, _ []string) error {
		if backendOf(j.ss.db) != nil {
			srcIdx = make([]int, len(cols))
			for i, c := range cols {
				srcIdx[i] = -1
				for k, name := range names {
					if strings.EqualFold(name, c.Src) {
						srcIdx[i] = k
					}
				}
			}
		}
		ph := strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")
		var err error
		stmt, err = tx.PrepareContext(ctx, `INSERT INTO `+quoteIdent(EngineSQLite, j.pt.Name)+` VALUES (`+ph+`)`)
		return err
	}, func(raw []any) (bool, error) {
		n++
		if n > limits.MaxRows {
			return false, &limitErr{fmt.Sprintf("%s.%s has more than %d rows (the rows-per-table limit in the project's Limits): the query was stopped, never run on part of a table",
				j.pt.Alias, j.pt.Name, limits.MaxRows)}
		}
		vals := make([]any, len(cols))
		var size int64
		for i, c := range cols {
			v := any(nil)
			if srcIdx != nil {
				if srcIdx[i] >= 0 {
					v = raw[srcIdx[i]]
				}
			} else {
				v = raw[i]
			}
			vals[i] = stageValue(v, c, trim[i])
			size += approxSize(vals[i])
		}
		run.mu.Lock()
		run.bytes += size
		over := run.bytes > maxBytes
		run.mu.Unlock()
		if over {
			return false, &limitErr{fmt.Sprintf("the copies of the tables passed %d MB while reading %s.%s (the staging limit in the project's Limits): the query was stopped",
				limits.MaxMB, j.pt.Alias, j.pt.Name)}
		}
		if _, err := stmt.ExecContext(ctx, vals...); err != nil {
			return false, err
		}
		if n%500 == 0 {
			run.mu.Lock()
			j.rt.Rows = n
			run.mu.Unlock()
		}
		return true, nil
	})
	if stmt != nil {
		defer stmt.Close()
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	run.mu.Lock()
	j.rt.Rows, j.rt.Done = n, true
	run.mu.Unlock()
	return nil
}

func (s *server) handleProjectRunState(w http.ResponseWriter, r *http.Request) {
	ps, err := s.project(r, "")
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	ps.mu.Lock()
	run := ps.run
	ps.mu.Unlock()
	if run == nil {
		writeJSON(w, 200, map[string]any{"state": ""})
		return
	}
	writeJSON(w, 200, run.snapshot())
}

func (s *server) handleProjectCancel(w http.ResponseWriter, r *http.Request) {
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
	run := ps.run
	ps.run = nil
	ps.mu.Unlock()
	if run != nil {
		run.mu.Lock()
		if run.state == "loading" || run.state == "querying" {
			run.state = "cancelled"
		}
		run.mu.Unlock()
		run.close() // stops the reads and removes the staging
		logAction(r, "[project %s] run cancelled", ps.name)
	}
	writeJSON(w, 200, map[string]any{"state": "cancelled"})
}

// handleProjectExport writes the result of the last run (all its rows, from
// the same staged data as the grid) as xlsx, csv, xml or json, or all four,
// to <export folder>/projects/<project>/<format>/.
func (s *server) handleProjectExport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string `json:"project"`
		SQL     string `json:"sql"`
		Format  string `json:"format"`
		Name    string `json:"name"`
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
	run, name := ps.run, ps.name
	ps.mu.Unlock()
	q := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(req.SQL), ";"))
	if run == nil {
		writeErr(w, 409, errors.New("run the query first: an export holds the rows of the last run"))
		return
	}
	run.mu.Lock()
	state, qdb, ran := run.state, run.qdb, run.sql
	run.mu.Unlock()
	if state != "done" || qdb == nil {
		writeErr(w, 409, errors.New("run the query first: an export holds the rows of the last run"))
		return
	}
	if q != ran {
		writeErr(w, 409, errors.New("the query changed since it ran: run it again, then export"))
		return
	}
	files, err := s.exportProjectResult(r, r.Context(), name, qdb, ran, req.Format, req.Name)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"files": files})
}

// exportProjectResult writes all the rows of a finished run's query ran (on
// its read-only connection qdb) as format (xlsx, csv, xml, json or all) to
// <export folder>/projects/<project>/<format>/.
func (s *server) exportProjectResult(r *http.Request, ctx context.Context, project string, qdb *sql.DB, ran, format, base string) ([]map[string]any, error) {
	formats := []string{format}
	if format == "all" {
		formats = []string{"xlsx", "csv", "xml", "json"}
	}
	dir := filepath.Join(s.exportFolder(), projectsSubdir, connFolderName(project))
	base = strings.TrimSpace(base)
	if base == "" {
		base = "query"
	}
	var files []map[string]any
	groups := projectGroupRows(ran) // "-- group rows by": the exports keep the groups
	for _, f := range formats {
		var path string
		var rows int
		var err error
		if len(groups) > 0 {
			path, rows, err = exportGroupedToDir(ctx, dir, qdb, ran, f, base, groups)
		} else {
			path, rows, err = exportToDir(ctx, dir, EngineSQLite, qdb, ran, f, base, nil)
		}
		if err != nil {
			logAction(r, "[project %s] export %s FAILED: %v", project, f, err)
			return files, err
		}
		logAction(r, "[project %s] export %s: %d row(s) to %s", project, f, rows, path)
		s.event(r, "export", false, "[project %s] export %s: %d row(s) to %s", project, f, rows, path)
		files = append(files, map[string]any{"path": path, "file": filepath.Base(path), "folder": filepath.Dir(path), "rows": rows, "download": downloadURL(path)})
	}
	return files, nil
}

// findProjectExport finds an export of a project in the export folders
// (the newest folder first), or "".
func (s *server) findProjectExport(project, kind, file string) string {
	if !validFileName(file) || !exportSubdirs[kind] {
		return ""
	}
	for _, dir := range s.exportFolders() {
		if p := filepath.Join(exportSubdir(filepath.Join(dir, projectsSubdir, project), kind), file); fileExists(p) {
			return p
		}
	}
	return ""
}

func serveExport(w http.ResponseWriter, r *http.Request, path string) {
	logAction(r, "download %s", path)
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(path)+`"`)
	http.ServeFile(w, r, path)
}
