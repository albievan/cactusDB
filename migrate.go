package main

// ☰ → Migrate: copy a database's structure and data to a new database of
// another engine, e.g. Firebird to MariaDB. The source is only read (in
// read-only transactions, like every query of cactusdb). cactusdb writes
// only to the target database it creates for the migration, and refuses
// one that already exists, so no existing database is changed; that needs
// the superuser password. "Script only" writes the whole migration as an
// SQL script instead, and writes to no database.
//
// The order: create the database, the sequences and the tables (columns,
// identity, primary key), copy the rows, then the unique constraints,
// indexes, foreign keys and checks, set the identity counters, and last
// the views, functions, procedures and triggers (migrate_code.go).

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
)

type migrateReq struct {
	Source        compareSource  `json:"source"`
	Target        *compareSource `json:"target"`       // the target server, a saved connection (not for SQLite or a script)
	TargetEngine  string         `json:"targetEngine"` // with no target connection: SQLite, or a script for this engine
	NewDatabase   string         `json:"newDatabase"`  // its name (a path for SQLite and Firebird)
	SuperPassword string         `json:"superPassword"`
	ScriptOnly    bool           `json:"scriptOnly"`
	Names         string         `json:"names"` // "", "lower" or "upper": the names' case in the target
	Data          bool           `json:"data"`
	Views         bool           `json:"views"`
	Routines      bool           `json:"routines"`
	Triggers      bool           `json:"triggers"`
	Sequences     bool           `json:"sequences"`
}

// migItem is one object of the report.
type migItem struct {
	Type   string   `json:"type"`
	Name   string   `json:"name"`   // the source's name
	Target string   `json:"target"` // the target's
	Status string   `json:"status"` // done, check (made, but read the notes), manual (to do by hand), failed, skipped
	Notes  []string `json:"notes,omitempty"`
	Rows   int64    `json:"rows,omitempty"`
	Error  string   `json:"error,omitempty"`
	Source string   `json:"source,omitempty"` // the source's definition (views and code)
	SQL    string   `json:"sql,omitempty"`    // what was run (or written to the script)
}

type migrateJob struct {
	mu         sync.Mutex
	created    time.Time
	cancel     context.CancelFunc
	phase      string
	current    string
	done       int
	total      int
	rows       int64
	items      []*migItem
	log        []string
	finished   bool
	err        string
	script     string // the script's path
	seconds    float64
	target     map[string]any // the new database, to add it as a connection
	scriptOnly bool
}

func (j *migrateJob) logf(format string, args ...any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.log = append(j.log, time.Now().Format("15:04:05")+"  "+fmt.Sprintf(format, args...))
}

// migrateTargets are the engines a migration can write to.
var migrateTargets = map[string]bool{EngineMySQL: true, EngineMariaDB: true, EnginePostgres: true, EnginePgvector: true,
	EngineAzurePG: true, EngineHorizonDB: true, EngineMSSQL: true, EngineSQLite: true, EngineFirebird: true}

// migrateSources are the engines a migration can read: SQL databases.
func migrateSource(engine string) bool {
	switch dialect(engine) {
	case EngineSQLite, EngineMySQL, EngineMariaDB, EnginePostgres, EngineMSSQL, EngineFirebird, EngineOracle, EngineAccess, EngineInformix:
		return true
	}
	return false
}

var reSubSecond = regexp.MustCompile(`(\.\d{4})\d+`)

var reDBName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$-]{0,62}$`)

func (s *server) handleMigrate(w http.ResponseWriter, r *http.Request) {
	var req migrateReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	req.NewDatabase = strings.TrimSpace(req.NewDatabase)
	if !req.ScriptOnly && !s.superuserOK(w, r, req.SuperPassword, "migrate a database") {
		return
	}
	// The superuser password also opens the saved connections.
	if req.Source.SavedID != 0 && req.Source.LockPassword == "" && req.Source.SuperPassword == "" {
		req.Source.SuperPassword = req.SuperPassword
	}
	var tp ConnParams
	if req.Target != nil && req.Target.SavedID != 0 && !req.ScriptOnly {
		if req.Target.LockPassword == "" && req.Target.SuperPassword == "" {
			req.Target.SuperPassword = req.SuperPassword
		}
		c, _, ok := s.unlockConnAs(w, r, req.Target.SavedID, req.Target.LockPassword, req.Target.SuperPassword)
		if !ok {
			return
		}
		tp = c.ConnParams
		req.TargetEngine = c.Engine
	}
	te := req.TargetEngine
	switch {
	case !migrateTargets[te]:
		writeErr(w, 400, fmt.Errorf("cactusdb can't migrate to %s: choose MySQL, MariaDB, PostgreSQL, SQL Server, SQLite or Firebird", cmp0(engineName(te), "that database")))
		return
	case req.NewDatabase == "":
		writeErr(w, 400, errors.New("enter the name of the new database"))
		return
	case te == EngineSQLite && !req.ScriptOnly:
		if !filepath.IsAbs(req.NewDatabase) {
			writeErr(w, 400, errors.New("the new SQLite database: enter a full path on the cactusdb server"))
			return
		}
		if fileExists(req.NewDatabase) {
			writeErr(w, 409, fmt.Errorf("%s already exists: cactusdb only migrates into a new database", req.NewDatabase))
			return
		}
		if fi, err := os.Stat(filepath.Dir(req.NewDatabase)); err != nil || !fi.IsDir() {
			writeErr(w, 400, fmt.Errorf("the folder %s doesn't exist", filepath.Dir(req.NewDatabase)))
			return
		}
	case te == EngineFirebird:
	case !reDBName.MatchString(req.NewDatabase):
		writeErr(w, 400, errors.New("the new database's name: letters, digits, _ and - (up to 63), starting with a letter"))
		return
	}
	if !req.ScriptOnly && te != EngineSQLite && tp.Engine == "" {
		writeErr(w, 400, errors.New("choose the target server: a saved connection to it"))
		return
	}
	src, ok := s.openCompareSource(w, r, req.Source)
	if !ok {
		return
	}
	if !migrateSource(src.engine) {
		if src.close {
			src.db.Close()
		}
		writeErr(w, 400, fmt.Errorf("%s: only SQL databases can be migrated", src.label))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
	job := &migrateJob{created: time.Now(), cancel: cancel, phase: "Starting", scriptOnly: req.ScriptOnly}
	id := newID()
	s.mu.Lock()
	if s.migrations == nil {
		s.migrations = map[string]*migrateJob{}
	}
	for k, j := range s.migrations { // reports are kept for a day
		j.mu.Lock()
		old := j.finished && time.Since(j.created) > 24*time.Hour
		j.mu.Unlock()
		if old {
			delete(s.migrations, k)
		}
	}
	s.migrations[id] = job
	s.mu.Unlock()
	what := "migrate"
	if req.ScriptOnly {
		what = "migration script of"
	}
	logAction(r, "%s %q (%s) to a new %s database %q", what, src.label, engineName(src.engine), engineName(te), req.NewDatabase)
	s.event(r, "export", false, "%s %q (%s) to a new %s database %q", what, src.label, engineName(src.engine), engineName(te), req.NewDatabase)
	m := &migrator{s: s, job: job, req: req, src: src, srcEngine: src.engine, tp: tp}
	go func() {
		defer cancel()
		defer func() {
			if src.close {
				src.db.Close()
			}
		}()
		start := time.Now()
		// A crash on an odd source (a parser bug, a damaged file) fails this
		// migration, not the whole server.
		err := func() (err error) {
			defer func() {
				if p := recover(); p != nil {
					log.Printf("migration %s panicked: %v\n%s", id, p, debug.Stack())
					err = fmt.Errorf("internal error: %v", p)
				}
			}()
			return m.run(ctx)
		}()
		job.mu.Lock()
		defer job.mu.Unlock()
		job.finished = true
		job.seconds = time.Since(start).Seconds()
		job.phase = "Finished"
		job.current = ""
		if err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				err = errors.New("stopped" + strings.TrimPrefix(err.Error(), context.Canceled.Error()))
			}
			job.err = err.Error()
			log.Printf("migration of %q FAILED: %v", src.label, err)
			return
		}
		log.Printf("migration of %q to %s %q finished in %.1fs", src.label, engineName(te), req.NewDatabase, job.seconds)
	}()
	writeJSON(w, 200, map[string]any{"job": id})
}

func cmp0(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *server) migrateJob(w http.ResponseWriter, r *http.Request) *migrateJob {
	s.mu.Lock()
	job := s.migrations[r.PathValue("job")]
	s.mu.Unlock()
	if job == nil {
		writeErr(w, 404, errors.New("this migration is no longer available"))
	}
	return job
}

func (s *server) handleMigrateStatus(w http.ResponseWriter, r *http.Request) {
	job := s.migrateJob(w, r)
	if job == nil {
		return
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	out := map[string]any{"phase": job.phase, "current": job.current, "done": job.done, "total": job.total, "rows": job.rows,
		"log": job.log, "finished": job.finished, "error": job.err, "seconds": job.seconds, "scriptOnly": job.scriptOnly}
	if job.finished {
		out["items"] = job.items
		out["target"] = job.target
		if job.script != "" {
			out["script"] = job.script
			out["scriptFile"] = filepath.Base(job.script)
			out["download"] = downloadURL(job.script)
		}
	}
	writeJSON(w, 200, out)
}

// handleMigrateScript downloads the migration's script. The job's id,
// known only to whoever started it (with the passwords it took), is the
// key: the file is in the source connection's folder, which may be protected.
func (s *server) handleMigrateScript(w http.ResponseWriter, r *http.Request) {
	job := s.migrateJob(w, r)
	if job == nil {
		return
	}
	job.mu.Lock()
	path := job.script
	job.mu.Unlock()
	if path == "" || !fileExists(path) {
		writeErr(w, 404, errors.New("the script isn't there (any more)"))
		return
	}
	logAction(r, "downloaded the migration script %s", filepath.Base(path))
	w.Header().Set("Content-Type", "application/sql; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(path)))
	http.ServeFile(w, r, path)
}

func (s *server) handleMigrateCancel(w http.ResponseWriter, r *http.Request) {
	job := s.migrateJob(w, r)
	if job == nil {
		return
	}
	job.cancel()
	logAction(r, "migration stopped")
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---- the migration -----------------------------------------------------------

type migrator struct {
	s         *server
	job       *migrateJob
	req       migrateReq
	src       *compareDB
	srcEngine string
	tp        ConnParams // the target server
	dst       string     // the target's dialect
	tdb       *sql.DB    // the new database (nil: script only)
	fbMajor   int        // the target Firebird's major version
	x         *xlate
	out       io.Writer // the script
	script    *bytes.Buffer
	tables    []*mTable
	byName    map[string]*mTable // upper-case source name → table
	used      map[string]bool    // target names of constraints and indexes, upper-case
	groups    map[string][]string
	maxIdent  map[string]int64 // script only: the largest identity value copied, per table
}

func (m *migrator) setPhase(phase string, total int) {
	m.job.mu.Lock()
	m.job.phase, m.job.done, m.job.total, m.job.current = phase, 0, total, ""
	m.job.mu.Unlock()
	m.job.logf("%s", phase)
}

func (m *migrator) step(current string) {
	m.job.mu.Lock()
	m.job.current = current
	m.job.mu.Unlock()
}

func (m *migrator) stepDone() {
	m.job.mu.Lock()
	m.job.done++
	m.job.mu.Unlock()
}

func (m *migrator) item(typ, name, target string) *migItem {
	it := &migItem{Type: typ, Name: name, Target: target, Status: "done"}
	m.job.mu.Lock()
	m.job.items = append(m.job.items, it)
	m.job.mu.Unlock()
	return it
}

func (it *migItem) fail(err error) {
	it.Status = "failed"
	it.Error = strings.TrimSpace(err.Error())
}

// identLimit is the longest name the target takes.
func (m *migrator) identLimit() int {
	switch m.dst {
	case EngineMySQL, EngineMariaDB:
		return 64
	case EnginePostgres:
		return 63
	case EngineMSSQL:
		return 128
	case EngineFirebird:
		if m.fbMajor >= 4 {
			return 63
		}
		return 31
	}
	return 200
}

func (m *migrator) limit(n string) string {
	max := m.identLimit()
	if len(n) <= max {
		return n
	}
	h := fnv.New32a()
	h.Write([]byte(n))
	cut := max - 7
	for cut > 0 && !utf8.RuneStart(n[cut]) { // whole characters: a name must stay valid UTF-8
		cut--
	}
	return n[:cut] + "_" + hex.EncodeToString(h.Sum(nil))[:6]
}

// targetName is the target's name of a source object: the default schema
// (dbo, public) is left out, another becomes a prefix.
func (m *migrator) targetName(src string) string {
	n := src
	if i := strings.Index(n, "("); i > 0 { // PostgreSQL routines: name(args)
		n = n[:i]
	}
	if schema, obj, ok := splitObject(n); ok && (m.srcEngine == EnginePostgres || m.srcEngine == EngineMSSQL || m.srcEngine == EngineOracle) {
		if strings.EqualFold(schema, "public") || strings.EqualFold(schema, "dbo") {
			n = obj
		} else {
			n = schema + "_" + obj
		}
	}
	return m.limit(m.x.applyCase(n))
}

// uniqueName makes a constraint or index name the target doesn't have yet.
func (m *migrator) uniqueName(table, name string) string {
	n := m.limit(m.x.applyCase(name))
	if name == "" || m.used[strings.ToUpper(n)] {
		n = m.limit(m.x.applyCase(table + "_" + name))
	}
	base := n
	for i := 2; m.used[strings.ToUpper(n)]; i++ {
		n = m.limit(fmt.Sprintf("%s_%d", base, i))
	}
	m.used[strings.ToUpper(n)] = true
	return n
}

func (m *migrator) q(name string) string { return quoteIdent(m.dst, name) }

// exec runs a statement on the new database and adds it to the script;
// code (a routine or trigger) is written with its own terminator.
func (m *migrator) exec(ctx context.Context, it *migItem, stmt string, code bool) error {
	if it != nil {
		if it.SQL != "" {
			it.SQL += "\n\n"
		}
		it.SQL += stmt
	}
	if m.tdb != nil {
		if err := m.execOne(ctx, stmt); err != nil {
			return err
		}
	}
	switch {
	case !code:
		fmt.Fprintf(m.out, "%s;\n", stmt)
		if m.dst == EngineMSSQL {
			fmt.Fprint(m.out, "GO\n")
		}
	case mysqlFamily(m.dst):
		fmt.Fprintf(m.out, "DELIMITER $$\n%s$$\nDELIMITER ;\n", stmt)
	case m.dst == EngineFirebird:
		fmt.Fprintf(m.out, "SET TERM ^ ;\n%s^\nSET TERM ; ^\n", stmt)
	case m.dst == EngineMSSQL:
		fmt.Fprintf(m.out, "%s\nGO\n", stmt)
	default:
		fmt.Fprintf(m.out, "%s;\n", stmt)
	}
	return nil
}

// execOne runs a statement; on Firebird in a transaction of its own, so a
// statement that fails leaves nothing behind for the next.
func (m *migrator) execOne(ctx context.Context, stmt string) error {
	if m.dst != EngineFirebird {
		_, err := m.tdb.ExecContext(ctx, stmt)
		return err
	}
	tx, err := m.tdb.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, stmt); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (m *migrator) comment(format string, args ...any) {
	for _, l := range strings.Split(fmt.Sprintf(format, args...), "\n") {
		fmt.Fprintf(m.out, "-- %s\n", inComment(l))
	}
}

func (m *migrator) run(ctx context.Context) error {
	req := m.req
	m.dst = dialect(req.TargetEngine)
	if req.TargetEngine == EngineMariaDB {
		m.dst = EngineMariaDB
	}
	m.used = map[string]bool{}
	m.byName = map[string]*mTable{}
	m.maxIdent = map[string]int64{}
	m.fbMajor = 4

	// The new database first: nothing else is worth doing if it can't be made.
	var tparams ConnParams
	if !req.ScriptOnly {
		m.setPhase("Creating the database "+req.NewDatabase, 1)
		db, p, info, err := createTarget(ctx, m.tp, req.TargetEngine, req.NewDatabase, !mysqlFamily(m.srcEngine))
		if err != nil {
			return err
		}
		m.tdb, tparams = db, p
		defer m.tdb.Close()
		if info.fbMajor > 0 {
			m.fbMajor = info.fbMajor
		}
		if info.mariadb {
			m.dst = EngineMariaDB
		}
		m.job.logf("created %s %s (%s)", engineName(m.dst), req.NewDatabase, info.version)
		m.job.mu.Lock()
		m.job.target = map[string]any{"engine": req.TargetEngine, "host": p.Host, "port": p.Port, "username": p.Username,
			"database": p.Database, "options": p.Options}
		m.job.mu.Unlock()
	}
	m.x = &xlate{src: m.srcEngine, dst: m.dst, names: map[string]string{}, columns: map[string]bool{}, bools: map[string]bool{}, seqs: map[string]bool{},
		procs: map[string]bool{}, nameCase: req.Names, dropQual: map[string]bool{"DBO": true, "PUBLIC": true}}
	m.x.seqOK = m.dst != EngineSQLite && m.dst != EngineMySQL
	if m.dst == EngineFirebird && m.fbMajor < 3 {
		m.x.seqOK = true
	}

	run := func(w io.Writer) (int, error) {
		m.out = w
		return 0, m.migrate(ctx)
	}
	dir := filepath.Join(m.s.exportFolder(), connFolderName(m.src.label))
	name := connFolderName(m.src.label) + "_to_" + m.dst + "_" + req.NewDatabase
	if req.ScriptOnly {
		path, _, err := writeExportFile(dir, filepath.Base(name), "sql", run)
		if err != nil {
			return err
		}
		m.job.mu.Lock()
		m.job.script = path
		m.job.mu.Unlock()
		return nil
	}
	m.script = &bytes.Buffer{}
	err := func() error { _, err := run(m.script); return err }()
	// The script of what was done is kept, even when it stopped.
	if path, _, werr := writeExportFile(dir, filepath.Base(name), "sql", func(w io.Writer) (int, error) {
		return w.Write(m.script.Bytes())
	}); werr == nil {
		m.job.mu.Lock()
		m.job.script = path
		m.job.mu.Unlock()
	}
	if err != nil {
		return fmt.Errorf("%w (the new database %s was left as it is: delete it before migrating again)", err, tparams.Database)
	}
	return nil
}

// migrate does the work, writing the script to m.out.
func (m *migrator) migrate(ctx context.Context) error {
	req, x := m.req, m.x
	m.comment("Migration of %s (%s) to a new %s database %s\nby cactusdb %s on %s", m.src.label, engineName(m.srcEngine),
		engineName(m.dst), req.NewDatabase, version, time.Now().Format("2006-01-02 15:04:05"))
	switch {
	case req.ScriptOnly && mysqlFamily(m.dst) && !mysqlFamily(m.srcEngine):
		fmt.Fprint(m.out, "SET SESSION sql_mode = CONCAT(@@sql_mode, ',PIPES_AS_CONCAT,NO_AUTO_VALUE_ON_ZERO');\n")
	case req.ScriptOnly && mysqlFamily(m.dst):
		fmt.Fprint(m.out, "SET SESSION sql_mode = CONCAT(@@sql_mode, ',NO_AUTO_VALUE_ON_ZERO');\n")
	case m.dst == EngineMSSQL: // sqlcmd's defaults are off; filtered indexes need them
		fmt.Fprint(m.out, "SET ANSI_NULLS ON;\nSET QUOTED_IDENTIFIER ON;\nGO\n")
	}

	// 1. Read the source.
	m.setPhase("Reading the source's structure", 0)
	groups, err := listObjects(ctx, m.srcEngine, m.src.db)
	if err != nil {
		return err
	}
	m.groups = map[string][]string{}
	for _, g := range groups {
		m.groups[g.Type] = g.Items
	}
	m.setPhase("Reading the tables", len(m.groups[ObjTable]))
	for _, name := range m.groups[ObjTable] {
		if err := ctx.Err(); err != nil {
			return err
		}
		m.step(name)
		t, err := readTable(ctx, m.srcEngine, m.src.db, name)
		m.stepDone()
		if err != nil {
			m.item(ObjTable, name, "").fail(fmt.Errorf("reading it: %w", err))
			continue
		}
		m.tables = append(m.tables, t)
	}
	// Names in the target.
	for _, t := range m.tables {
		t.Name = m.targetName(t.Src)
		m.byName[strings.ToUpper(t.Src)] = t
		x.names[t.Src] = t.Name
		x.names[strings.ToUpper(t.Src)] = t.Name
		if _, obj, ok := splitObject(t.Src); ok {
			if _, dup := x.names[strings.ToUpper(obj)]; !dup {
				x.names[strings.ToUpper(obj)] = t.Name
			}
		}
		m.used[strings.ToUpper(t.Name)] = true
		for i := range t.Cols {
			c := &t.Cols[i]
			c.Name = m.limit(x.applyCase(c.Src))
			x.names[strings.ToUpper(c.Src)] = c.Name
			x.columns[strings.ToUpper(c.Src)] = true
			if c.Kind == kBool {
				x.bools[strings.ToUpper(c.Src)] = true
			}
		}
	}
	for _, typ := range []string{ObjView, ObjMatView, ObjProcedure, ObjFunction, ObjSequence} {
		for _, n := range m.groups[typ] {
			tn := m.targetName(n)
			x.names[n] = tn
			if _, dup := x.names[strings.ToUpper(n)]; !dup {
				x.names[strings.ToUpper(n)] = tn
			}
			if _, obj, ok := splitObject(strings.SplitN(n, "(", 2)[0]); ok {
				if _, dup := x.names[strings.ToUpper(obj)]; !dup {
					x.names[strings.ToUpper(obj)] = tn
				}
			}
			if typ == ObjSequence {
				x.seqs[strings.ToUpper(n)] = true
			}
		}
	}

	// Firebird's code is parsed now: a trigger that numbers a column from a
	// generator becomes an auto-increment column where there are no sequences.
	var fb *fbCode
	if m.srcEngine == EngineFirebird && (req.Routines || req.Triggers || !x.seqOK) {
		fb = m.readFirebirdCode(ctx)
	}
	if fb != nil && !x.seqOK {
		for _, r := range fb.triggers {
			if r.body == nil {
				continue
			}
			if col, gen, ok := autoIncrementTrigger(r.psqlRoutine); ok {
				if t := m.byName[strings.ToUpper(r.table)]; t != nil {
					if c := t.col(col); c != nil && (c.Kind == kInt16 || c.Kind == kInt32 || c.Kind == kInt64) && len(t.PK) > 0 && strings.EqualFold(t.PK[0], col) {
						c.Identity = true
						r.replaced = fmt.Sprintf("the column %s.%s numbers itself (AUTO_INCREMENT) instead of this trigger and the generator %s", t.Name, c.Name, gen)
					}
				}
			}
		}
	}
	for _, t := range m.tables {
		m.fitTable(t)
	}

	// 2. Sequences.
	if req.Sequences && len(m.groups[ObjSequence]) > 0 {
		m.setPhase("Creating the sequences", len(m.groups[ObjSequence]))
		for _, s := range readSequences(ctx, m.srcEngine, m.src.db, m.groups[ObjSequence]) {
			m.createSequence(ctx, s)
			m.stepDone()
		}
	}

	// 3. Tables.
	m.setPhase("Creating the tables", len(m.tables))
	created := map[*mTable]bool{}
	tableItems := map[*mTable]*migItem{}
	for _, t := range m.tables {
		if err := ctx.Err(); err != nil {
			return err
		}
		m.step(t.Src)
		it := m.item(ObjTable, t.Src, t.Name)
		tableItems[t] = it
		it.Notes = append(it.Notes, t.Notes...)
		if err := m.exec(ctx, it, m.createTable(t), false); err != nil {
			it.fail(err)
		} else {
			created[t] = true
		}
		m.stepDone()
	}

	// 4. Rows.
	if req.Data {
		m.setPhase("Copying the rows", len(m.tables))
		for _, t := range m.tables {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !created[t] {
				m.stepDone()
				continue
			}
			m.step(t.Src)
			it := tableItems[t]
			n, err := m.copyRows(ctx, t)
			it.Rows = n
			if err != nil {
				if ctx.Err() != nil {
					it.fail(fmt.Errorf("stopped after %d rows", n))
					return ctx.Err()
				}
				it.fail(fmt.Errorf("copying the rows (%d copied): %w", n, err))
			} else if m.tdb != nil {
				var got int64
				if err := m.tdb.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+m.q(t.Name)).Scan(&got); err == nil && got != n {
					it.Status = "check"
					it.Notes = append(it.Notes, fmt.Sprintf("%d rows copied, but the new table has %d", n, got))
				}
			}
			m.job.logf("%s: %d rows", t.Name, n)
			m.stepDone()
		}
	}

	// 5. Unique constraints, indexes, foreign keys, checks; identity counters.
	m.setPhase("Creating the keys and indexes", len(m.tables))
	for _, t := range m.tables {
		if !created[t] {
			m.stepDone()
			continue
		}
		m.step(t.Src)
		m.createIndexes(ctx, t)
		m.stepDone()
	}
	if m.dst != EngineSQLite {
		m.setPhase("Creating the foreign keys and checks", len(m.tables))
		for _, t := range m.tables {
			if !created[t] {
				m.stepDone()
				continue
			}
			m.step(t.Src)
			m.createForeignKeys(ctx, t)
			m.createChecks(ctx, t)
			m.stepDone()
		}
	}
	for _, t := range m.tables {
		if created[t] {
			m.resetIdentity(ctx, t, tableItems[t])
		}
	}

	// 6. Views and code.
	// A view that reads a function fails until the function is there: those
	// that fail are tried again after the code.
	code := req.Routines || req.Triggers
	var later []*mView
	if req.Views {
		later = m.migrateViews(ctx, !code)
	}
	if code {
		m.migrateCode(ctx, fb)
		if len(later) > 0 {
			m.setPhase("Creating the views that read functions", len(later))
			m.createViews(ctx, later, true)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// The script ends with what must be done by hand.
	m.job.mu.Lock()
	items := slices.Clone(m.job.items)
	m.job.mu.Unlock()
	for _, it := range items {
		if it.Status == "manual" || it.Status == "failed" {
			m.comment("\n%s %s: %s %s", it.Type, it.Name, it.Status, strings.Join(append([]string{it.Error}, it.Notes...), "; "))
			if it.Source != "" {
				m.comment("%s", it.Source)
			}
		}
	}
	return nil
}

// fitTable adjusts columns to the target's limits: key columns can't be
// unlimited text on MySQL and SQL Server, and a MySQL row has room for
// 65,535 bytes of VARCHAR.
func (m *migrator) fitTable(t *mTable) {
	// A decimal without a precision (PostgreSQL numeric, Oracle NUMBER) gets
	// a fixed one on most targets: say so, as it rounds longer values.
	for i := range t.Cols {
		c := &t.Cols[i]
		if c.Kind == kDecimal && c.Prec <= 0 {
			if typ := colType(m.dst, c, m.fbMajor); strings.Contains(typ, "(") {
				t.Notes = append(t.Notes, fmt.Sprintf("%s (%s, any precision) becomes %s: values with more digits are rounded or don't fit", c.Name, c.SrcType, typ))
			}
		}
	}
	if mysqlFamily(m.dst) || m.dst == EngineMSSQL {
		keyLen := int64(255)
		if m.dst == EngineMSSQL {
			keyLen = 450
		}
		keys := map[string]bool{}
		for _, k := range t.PK {
			keys[strings.ToUpper(k)] = true
		}
		for _, ix := range t.Indexes {
			for _, k := range ix.Cols {
				keys[strings.ToUpper(k)] = true
			}
		}
		for _, fk := range t.FKs {
			for _, k := range fk.Cols {
				keys[strings.ToUpper(k)] = true
			}
		}
		for i := range t.Cols {
			c := &t.Cols[i]
			if !keys[strings.ToUpper(c.Src)] {
				continue
			}
			switch {
			case c.Kind == kText || c.Kind == kJSON || (c.Kind == kVarchar || c.Kind == kChar) && c.Len > keyLen:
				t.Notes = append(t.Notes, fmt.Sprintf("%s is a key column: VARCHAR(%d) instead of unlimited text", c.Name, keyLen))
				c.Kind, c.Len = kVarchar, keyLen
			case c.Kind == kBlob:
				t.Notes = append(t.Notes, fmt.Sprintf("%s is a key column: VARBINARY(%d) instead of a BLOB", c.Name, keyLen))
				c.Kind, c.Len = kVarbinary, keyLen
			}
		}
	}
	if mysqlFamily(m.dst) {
		size := func() (total int64, big int) {
			big = -1
			for i, c := range t.Cols {
				switch c.Kind {
				case kVarchar, kChar:
					if c.Len <= 16000 {
						total += c.Len*4 + 2
						if big < 0 || c.Len > t.Cols[big].Len {
							big = i
						}
					}
				case kVarbinary, kBinary:
					if c.Len <= 16000 {
						total += c.Len + 2
					}
				default:
					total += 12
				}
			}
			return
		}
		for total, big := size(); total > 65000 && big >= 0; total, big = size() {
			c := &t.Cols[big]
			t.Notes = append(t.Notes, fmt.Sprintf("%s is LONGTEXT: a MySQL row has no room for all its VARCHARs", c.Name))
			c.Kind = kText
		}
	}
}

func (m *migrator) createTable(t *mTable) string {
	var lines []string
	pkInline := false
	inPK := map[string]bool{}
	for _, k := range t.PK {
		inPK[strings.ToUpper(k)] = true
	}
	identities := 0
	for i := range t.Cols {
		c := &t.Cols[i]
		line := m.q(c.Name) + " " + colType(m.dst, c, m.fbMajor)
		isInt := c.Kind == kInt16 || c.Kind == kInt32 || c.Kind == kInt64
		if c.Identity && (!isInt || identities > 0) {
			t.Notes = append(t.Notes, fmt.Sprintf("%s was numbered automatically in the source: here it isn't", c.Name))
			c.Identity = false
		}
		if c.Computed {
			t.Notes = append(t.Notes, fmt.Sprintf("%s is computed in the source: here it holds the values copied", c.Name))
		}
		if c.Identity {
			identities++
			switch m.dst {
			case EngineMySQL, EngineMariaDB:
				line += " AUTO_INCREMENT"
			case EnginePostgres, EngineFirebird:
				line += " GENERATED BY DEFAULT AS IDENTITY"
			case EngineMSSQL:
				line += " IDENTITY(1,1)"
			case EngineSQLite:
				if len(t.PK) == 1 && inPK[strings.ToUpper(c.Src)] {
					line = m.q(c.Name) + " INTEGER PRIMARY KEY AUTOINCREMENT"
					pkInline = true
				}
			}
		} else if d := renderDefault(m.dst, c); d != "" {
			line += " DEFAULT " + d
		}
		if (!c.Nullable || inPK[strings.ToUpper(c.Src)]) && !(pkInline && inPK[strings.ToUpper(c.Src)]) {
			line += " NOT NULL"
		}
		lines = append(lines, line)
	}
	cols := func(names []string) string {
		var out []string
		for _, n := range names {
			if c := t.col(n); c != nil {
				out = append(out, m.q(c.Name))
			} else {
				out = append(out, m.q(m.x.applyCase(n)))
			}
		}
		return strings.Join(out, ", ")
	}
	if len(t.PK) > 0 && !pkInline {
		pk := "PRIMARY KEY (" + cols(t.PK) + ")"
		if m.dst != EngineMySQL && m.dst != EngineMariaDB {
			pk = "CONSTRAINT " + m.q(m.uniqueName(t.Name, cmp0(t.PKName, "pk_"+t.Src))) + " " + pk
		}
		lines = append(lines, pk)
	}
	if mysqlFamily(m.dst) {
		for _, c := range t.Cols { // MySQL: an AUTO_INCREMENT column must lead a key
			if c.Identity && (len(t.PK) == 0 || !strings.EqualFold(t.PK[0], c.Src)) {
				lines = append(lines, "KEY ("+m.q(c.Name)+")")
			}
		}
	}
	if m.dst == EngineSQLite { // SQLite can't add these later
		for _, fk := range t.FKs {
			if rt := m.byName[strings.ToUpper(fk.RefTable)]; rt != nil {
				lines = append(lines, "FOREIGN KEY ("+cols(fk.Cols)+") REFERENCES "+m.q(rt.Name)+" ("+refCols(m, rt, fk.RefCols)+")"+fkActions(fk))
			}
		}
		for _, ck := range t.Checks {
			lines = append(lines, "CHECK ("+m.x.render(lexSQL(ck.Expr, m.srcEngine), true)+")")
		}
	}
	out := "CREATE TABLE " + m.q(t.Name) + " (\n  " + strings.Join(lines, ",\n  ") + "\n)"
	if mysqlFamily(m.dst) {
		out += " ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci"
	}
	return out
}

func refCols(m *migrator, rt *mTable, names []string) string {
	var out []string
	for _, n := range names {
		if c := rt.col(n); c != nil {
			out = append(out, m.q(c.Name))
		} else {
			out = append(out, m.q(m.x.applyCase(n)))
		}
	}
	return strings.Join(out, ", ")
}

func fkActions(fk mFK) string {
	s := ""
	if fk.OnDelete != "" {
		s += " ON DELETE " + fk.OnDelete
	}
	if fk.OnUpdate != "" {
		s += " ON UPDATE " + fk.OnUpdate
	}
	return s
}

func (m *migrator) colNames(t *mTable, names []string) string {
	return refCols(m, t, names)
}

func (m *migrator) createIndexes(ctx context.Context, t *mTable) {
	for _, ix := range t.Indexes {
		if ix.Name == "" {
			ix.Name = map[bool]string{true: "uq_", false: "ix_"}[ix.Unique] + t.Src + "_" + strings.Join(ix.Cols, "_")
		}
		name := m.uniqueName(t.Name, ix.Name)
		it := m.item(map[bool]string{true: "unique", false: "index"}[ix.Cons], t.Src+"."+ix.Name, t.Name+"."+name)
		cols := m.colNames(t, ix.Cols)
		if ix.Desc && m.dst != EngineFirebird {
			var d []string
			for _, c := range strings.Split(cols, ", ") {
				d = append(d, c+" DESC")
			}
			cols = strings.Join(d, ", ")
		}
		var stmt string
		nullable := false
		for _, cn := range ix.Cols {
			if c := t.col(cn); c != nil && c.Nullable {
				nullable = true
			}
		}
		switch {
		case ix.Unique && m.dst == EngineMSSQL && nullable:
			// SQL Server lets a unique constraint hold one NULL; the others any number.
			var where []string
			for _, cn := range ix.Cols {
				if c := t.col(cn); c != nil {
					where = append(where, m.q(c.Name)+" IS NOT NULL")
				}
			}
			stmt = fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s (%s) WHERE %s", m.q(name), m.q(t.Name), cols, strings.Join(where, " AND "))
			it.Notes = append(it.Notes, "a unique index on the rows that aren't NULL: SQL Server's unique constraint allows only one NULL")
		case ix.Cons && m.dst != EngineSQLite:
			stmt = fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s UNIQUE (%s)", m.q(t.Name), m.q(name), m.colNames(t, ix.Cols))
		default:
			kind := "INDEX"
			if ix.Desc && m.dst == EngineFirebird {
				kind = "DESCENDING INDEX"
			}
			if ix.Unique {
				kind = "UNIQUE " + kind
			}
			stmt = fmt.Sprintf("CREATE %s %s ON %s (%s)", kind, m.q(name), m.q(t.Name), cols)
		}
		if err := m.exec(ctx, it, stmt, false); err != nil {
			it.fail(err)
		}
	}
}

func (m *migrator) createForeignKeys(ctx context.Context, t *mTable) {
	for i, fk := range t.FKs {
		if fk.Name == "" {
			fk.Name = fmt.Sprintf("fk_%s_%d", t.Src, i+1)
		}
		name := m.uniqueName(t.Name, fk.Name)
		it := m.item("foreign key", t.Src+"."+fk.Name, t.Name+"."+name)
		rt := m.byName[strings.ToUpper(fk.RefTable)]
		if rt == nil {
			it.fail(fmt.Errorf("the table it references, %s, wasn't migrated", fk.RefTable))
			continue
		}
		stmt := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s)%s", m.q(t.Name), m.q(name),
			m.colNames(t, fk.Cols), m.q(rt.Name), refCols(m, rt, fk.RefCols), fkActions(fk))
		if err := m.exec(ctx, it, stmt, false); err != nil {
			it.fail(err)
		}
	}
}

func (m *migrator) createChecks(ctx context.Context, t *mTable) {
	for i, ck := range t.Checks {
		if ck.Name == "" {
			ck.Name = fmt.Sprintf("ck_%s_%d", t.Src, i+1)
		}
		name := m.uniqueName(t.Name, ck.Name)
		it := m.item("check", t.Src+"."+ck.Name, t.Name+"."+name)
		it.Source = ck.Expr
		m.x.notes, m.x.todo = nil, false
		m.x.vars, m.x.consts = nil, nil
		expr := m.x.render(lexSQL(ck.Expr, m.srcEngine), true)
		it.Notes = append(it.Notes, m.x.notes...)
		stmt := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s CHECK (%s)", m.q(t.Name), m.q(name), expr)
		if err := m.exec(ctx, it, stmt, false); err != nil {
			it.fail(err)
		}
	}
}

// resetIdentity makes an identity column go on from the largest value copied.
func (m *migrator) resetIdentity(ctx context.Context, t *mTable, it *migItem) {
	for _, c := range t.Cols {
		if !c.Identity {
			continue
		}
		var stmt string
		switch m.dst {
		case EnginePostgres:
			stmt = fmt.Sprintf("SELECT setval(pg_get_serial_sequence('%s', '%s'), COALESCE((SELECT MAX(%s) FROM %s), 0) + 1, false)",
				strings.ReplaceAll(m.q(t.Name), "'", "''"), strings.ReplaceAll(c.Name, "'", "''"), m.q(c.Name), m.q(t.Name))
		case EngineFirebird:
			var max int64
			if m.tdb != nil {
				m.tdb.QueryRowContext(ctx, fmt.Sprintf("SELECT COALESCE(MAX(%s), 0) FROM %s", m.q(c.Name), m.q(t.Name))).Scan(&max)
			} else {
				max = m.maxIdent[t.Src]
			}
			if m.fbMajor >= 4 {
				max++
			}
			stmt = fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s RESTART WITH %d", m.q(t.Name), m.q(c.Name), max)
		default: // MySQL, SQL Server and SQLite go on from the largest value themselves
			continue
		}
		if err := m.exec(ctx, nil, stmt, false); err != nil {
			it.Status = "check"
			it.Notes = append(it.Notes, fmt.Sprintf("the counter of %s couldn't be set: %v", c.Name, err))
		}
	}
}

func (m *migrator) createSequence(ctx context.Context, s mSeq) {
	s.Name = m.targetName(s.Src)
	it := m.item(ObjSequence, s.Src, s.Name)
	if !m.x.seqOK {
		it.Status = "manual"
		it.Notes = append(it.Notes, fmt.Sprintf("%s has no sequences: its next value would be %d", engineName(m.dst), s.Next))
		return
	}
	if !s.SourceKnown {
		it.Notes = append(it.Notes, "its current value couldn't be read: it starts at 1")
	}
	start := s.Next
	if m.dst == EngineFirebird && m.fbMajor < 4 {
		start -= s.Incr
	}
	stmt := fmt.Sprintf("CREATE SEQUENCE %s START WITH %d INCREMENT BY %d", m.q(s.Name), start, s.Incr)
	switch m.dst {
	case EngineMSSQL:
		stmt = fmt.Sprintf("CREATE SEQUENCE %s AS BIGINT START WITH %d INCREMENT BY %d", m.q(s.Name), start, s.Incr)
	case EnginePostgres, EngineMariaDB:
		if start < 1 {
			stmt += fmt.Sprintf(" MINVALUE %d", start)
		}
	}
	if err := m.exec(ctx, it, stmt, false); err != nil {
		it.fail(err)
	}
}

// ---- rows --------------------------------------------------------------------------

func (m *migrator) placeholder(n int) string {
	switch m.dst {
	case EnginePostgres:
		return "$" + strconv.Itoa(n)
	case EngineMSSQL:
		return "@p" + strconv.Itoa(n)
	}
	return "?"
}

// copyRows copies a table's rows, in batches of multi-row INSERTs, each
// few thousand rows committed.
func (m *migrator) copyRows(ctx context.Context, t *mTable) (int64, error) {
	var q string
	if backendOf(m.src.db) != nil {
		var err error
		if q, err = buildSelect(ctx, m.srcEngine, m.src.db, t.Src); err != nil {
			return 0, err
		}
	} else {
		cols := make([]string, len(t.Cols))
		for i, c := range t.Cols {
			cols[i] = quoteIdent(m.srcEngine, c.Src)
		}
		q = "SELECT " + strings.Join(cols, ", ") + " FROM " + fromOnly(m.srcEngine, m.src.db) + quoteObject(m.srcEngine, t.Src)
	}
	tcols := make([]string, len(t.Cols))
	identity := -1
	for i, c := range t.Cols {
		tcols[i] = m.q(c.Name)
		if c.Identity {
			identity = i
		}
	}
	head := "INSERT INTO " + m.q(t.Name) + " (" + strings.Join(tcols, ", ") + ") VALUES "
	maxParams := map[string]int{EngineMSSQL: 2000, EnginePostgres: 60000, EngineSQLite: 30000, EngineMySQL: 30000, EngineMariaDB: 30000, EngineFirebird: len(t.Cols)}[m.dst]
	perBatch := max(1, min(500, maxParams/len(t.Cols)))
	if m.dst == EngineFirebird {
		perBatch = 1
	}

	var conn *sql.Conn
	var tx *sql.Tx
	var err error
	if m.tdb != nil {
		if conn, err = m.tdb.Conn(ctx); err != nil {
			return 0, err
		}
		defer conn.Close()
		if m.dst == EngineMSSQL && identity >= 0 {
			if _, err := conn.ExecContext(ctx, "SET IDENTITY_INSERT "+m.q(t.Name)+" ON"); err != nil {
				return 0, err
			}
			defer conn.ExecContext(context.Background(), "SET IDENTITY_INSERT "+m.q(t.Name)+" OFF")
		}
	} else if m.dst == EngineMSSQL && identity >= 0 {
		fmt.Fprintf(m.out, "SET IDENTITY_INSERT %s ON;\n", m.q(t.Name))
		defer fmt.Fprintf(m.out, "SET IDENTITY_INSERT %s OFF;\nGO\n", m.q(t.Name))
	}
	var stmts = map[int]*sql.Stmt{}
	defer func() {
		for _, s := range stmts {
			s.Close()
		}
	}()
	var batch [][]any
	var bytesInBatch int
	var copied, inTx, committed int64
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		n := int64(len(batch))
		defer func() {
			batch, bytesInBatch = batch[:0], 0
			m.job.mu.Lock()
			m.job.rows += n
			m.job.mu.Unlock()
		}()
		if m.tdb == nil { // the script: literals
			var b strings.Builder
			b.WriteString(head)
			for i, row := range batch {
				if i > 0 {
					b.WriteString(",\n  ")
				}
				b.WriteString("(")
				for k, v := range row {
					if k > 0 {
						b.WriteString(", ")
					}
					b.WriteString(m.literal(v))
				}
				b.WriteString(")")
			}
			b.WriteString(";\n")
			_, err := io.WriteString(m.out, b.String())
			return err
		}
		if tx == nil {
			if tx, err = conn.BeginTx(ctx, nil); err != nil {
				return err
			}
		}
		st := stmts[len(batch)]
		if st == nil {
			var b strings.Builder
			b.WriteString(head)
			n := 1
			for i := range batch {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString("(")
				for k := range t.Cols {
					if k > 0 {
						b.WriteString(", ")
					}
					b.WriteString(m.placeholder(n))
					n++
				}
				b.WriteString(")")
			}
			if st, err = conn.PrepareContext(ctx, b.String()); err != nil {
				return err
			}
			stmts[len(batch)] = st
		}
		args := make([]any, 0, len(batch)*len(t.Cols))
		for _, row := range batch {
			args = append(args, row...)
		}
		if _, err := tx.StmtContext(ctx, st).ExecContext(ctx, args...); err != nil {
			return err
		}
		inTx += int64(len(batch))
		if inTx >= 5000 {
			if err := tx.Commit(); err != nil {
				return err
			}
			tx, committed, inTx = nil, committed+inTx, 0
		}
		return nil
	}
	err = streamRows(ctx, m.srcEngine, m.src.db, q, func([]string, []string) error { return nil }, func(raw []any) (bool, error) {
		row := make([]any, len(t.Cols))
		for i := range t.Cols {
			if i < len(raw) {
				row[i] = convertValue(raw[i], &t.Cols[i], m.dst)
				// MySQL's zero date (0000-00-00) arrives as year 1: it means
				// "no date", which is NULL where the column allows one.
				if tm, ok := raw[i].(time.Time); ok && tm.IsZero() && mysqlFamily(m.srcEngine) && t.Cols[i].Nullable {
					row[i] = nil
				}
				if s, ok := row[i].(string); ok && m.dst == EngineFirebird && (t.Cols[i].Kind == kTime || t.Cols[i].Kind == kTimestamp || t.Cols[i].Kind == kTimestampTZ) {
					row[i] = reSubSecond.ReplaceAllString(s, "$1") // Firebird keeps 1/10000 of a second
				}
				bytesInBatch += valueSize(row[i])
			}
		}
		if identity >= 0 {
			if n, ok := row[identity].(int64); ok && n > m.maxIdent[t.Src] {
				m.maxIdent[t.Src] = n
			}
		}
		batch = append(batch, row)
		copied++
		if len(batch) >= perBatch || bytesInBatch > 4<<20 {
			if err := flush(); err != nil {
				return false, err
			}
			m.job.mu.Lock()
			m.job.current = fmt.Sprintf("%s: %d rows", t.Src, copied)
			m.job.mu.Unlock()
		}
		return true, nil
	})
	if err == nil {
		err = flush()
	}
	if tx != nil {
		if err == nil {
			if err = tx.Commit(); err == nil {
				committed += inTx
			}
		} else {
			tx.Rollback()
		}
	}
	if err != nil {
		if m.tdb == nil {
			return copied, err
		}
		return committed, err
	}
	return copied, nil
}

func valueSize(v any) int {
	switch x := v.(type) {
	case string:
		return len(x)
	case []byte:
		return len(x)
	}
	return 8
}

// convertValue turns a value as the source's driver returns it into one
// the target's driver writes into a column of kind c.Kind. Dates and times
// are passed as text, so no time zone moves them.
// bitString packs a PostgreSQL bit(n) value ("0101…", as the driver gives
// it) into bytes, the first bit the highest.
func bitString(s, srcType string) ([]byte, bool) {
	if !strings.HasPrefix(strings.ToUpper(srcType), "BIT") || s == "" || strings.Trim(s, "01") != "" {
		return nil, false
	}
	b := make([]byte, (len(s)+7)/8)
	for i := 0; i < len(s); i++ {
		if s[i] == '1' {
			b[i/8] |= 0x80 >> (i % 8)
		}
	}
	return b, true
}

// moneyNumber is a PostgreSQL money value ("$1,234.56", "-$5.00",
// "($5.00)") as a plain number. The locale's decimal mark is the last . or ,
// followed by up to two digits.
func moneyNumber(s string) string {
	neg := strings.Contains(s, "-") || strings.HasPrefix(strings.TrimSpace(s), "(")
	var digits strings.Builder
	dec := -1
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case r == '.' || r == ',':
			if rest := strings.TrimRightFunc(s[i+1:], func(r rune) bool { return r < '0' || r > '9' }); len(rest) <= 2 && !strings.ContainsAny(rest, ".,") {
				dec = digits.Len()
			}
		}
	}
	d := digits.String()
	if d == "" {
		return s
	}
	out := d
	if dec >= 0 {
		out = cmp.Or(d[:dec], "0") + "." + d[dec:]
	}
	if neg {
		out = "-" + out
	}
	return out
}

func convertValue(v any, c *mCol, dst string) any {
	if vl, ok := v.(driver.Valuer); ok {
		if x, err := vl.Value(); err == nil {
			v = x
		}
	}
	switch x := v.(type) {
	case nil:
		if dst == EngineMSSQL && (c.Kind == kBinary || c.Kind == kVarbinary || c.Kind == kBlob) {
			return []byte(nil) // SQL Server: a NULL of a binary type, not of NVARCHAR
		}
		return nil
	case []byte:
		switch c.Kind {
		case kBinary, kVarbinary, kBlob:
			return slices.Clone(x)
		case kUUID:
			if len(x) == 16 {
				if strings.Contains(strings.ToUpper(c.SrcType), "UNIQUEIDENTIFIER") {
					return formatGUID(x)
				}
				return fmt.Sprintf("%x-%x-%x-%x-%x", x[0:4], x[4:6], x[6:8], x[8:10], x[10:16])
			}
		case kBool:
			return parseBool(string(x))
		}
		return string(x)
	case string:
		switch c.Kind {
		case kBinary, kVarbinary, kBlob:
			if b, ok := bitString(x, c.SrcType); ok {
				return b
			}
			return []byte(x)
		case kBool:
			return parseBool(x)
		case kDecimal:
			if strings.Contains(strings.ToUpper(c.SrcType), "MONEY") {
				return moneyNumber(x)
			}
		}
		return x
	case bool:
		return x
	case time.Time:
		switch c.Kind {
		case kDate:
			return x.Format("2006-01-02")
		case kTime:
			return x.Format("15:04:05.999999")
		case kTimestampTZ:
			if dst == EnginePostgres || dst == EngineMSSQL || dst == EngineFirebird {
				return x
			}
			return x.UTC().Format("2006-01-02 15:04:05.999999")
		case kText, kVarchar, kChar:
			return x.Format("2006-01-02 15:04:05.999999")
		}
		return x.Format("2006-01-02 15:04:05.999999")
	case int64, int32, int16, int8, int, uint8, uint16, uint32, uint64, uint:
		n := toInt64(x)
		if c.Kind == kBool {
			return n != 0
		}
		if u, ok := x.(uint64); ok && u > math.MaxInt64 {
			return strconv.FormatUint(u, 10)
		}
		return n
	case float32:
		if c.Kind == kBool {
			return x != 0
		}
		if c.Kind == kFloat32 {
			return x
		}
		return strconv.FormatFloat(float64(x), 'g', -1, 32)
	case float64:
		if c.Kind == kBool {
			return x != 0
		}
		return x
	case [16]byte:
		return fmt.Sprintf("%x-%x-%x-%x-%x", x[0:4], x[4:6], x[6:8], x[8:10], x[10:16])
	case map[string]any, []any:
		b, _ := json.Marshal(x)
		return string(b)
	case fmt.Stringer:
		return x.String()
	}
	return fmt.Sprint(v)
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int32:
		return int64(x)
	case int16:
		return int64(x)
	case int8:
		return int64(x)
	case int:
		return int64(x)
	case uint8:
		return int64(x)
	case uint16:
		return int64(x)
	case uint32:
		return int64(x)
	case uint64:
		return int64(x)
	case uint:
		return int64(x)
	}
	return 0
}

func parseBool(s string) any {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "t", "true", "y", "yes", "on", "\x01":
		return true
	case "0", "f", "false", "n", "no", "off", "", "\x00":
		return false
	}
	return s
}

// literal writes a converted value as an SQL literal of the target.
func (m *migrator) literal(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case bool:
		if m.dst == EnginePostgres || m.dst == EngineFirebird {
			return strings.ToUpper(strconv.FormatBool(x))
		}
		if x {
			return "1"
		}
		return "0"
	case int64:
		return strconv.FormatInt(x, 10)
	case float32:
		return floatLiteral(m.dst, float64(x))
	case float64:
		return floatLiteral(m.dst, x)
	case []byte:
		if m.dst == EngineFirebird {
			return "x'" + hex.EncodeToString(x) + "'"
		}
		return hexLiteral(m.dst, x)
	case time.Time:
		return quoteString(m.dst, x.Format("2006-01-02 15:04:05.999999 -07:00"))
	case string:
		return quoteString(m.dst, x)
	}
	return quoteString(m.dst, fmt.Sprint(v))
}

// ---- views ------------------------------------------------------------------------

// viewQuery reads a view's query (without CREATE VIEW … AS) and its columns.
func (m *migrator) viewQuery(ctx context.Context, name string) (string, []string, error) {
	db, e := m.src.db, m.srcEngine
	var src string
	var err error
	switch e {
	case EngineFirebird:
		src, err = fbString(ctx, db, `SELECT RDB$VIEW_SOURCE FROM RDB$RELATIONS WHERE RDB$RELATION_NAME = ?`, name)
	case EngineMySQL, EngineMariaDB:
		err = db.QueryRowContext(ctx, `SELECT VIEW_DEFINITION FROM information_schema.VIEWS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`, name).Scan(&src)
		var dbName string
		db.QueryRowContext(ctx, `SELECT DATABASE()`).Scan(&dbName)
		m.x.dropQual[strings.ToUpper(dbName)] = true
	case EnginePostgres:
		err = db.QueryRowContext(ctx, `SELECT pg_get_viewdef($1::regclass, true)`, quoteObject(e, name)).Scan(&src)
		src = strings.TrimSuffix(strings.TrimSpace(src), ";")
	default: // SQL Server, SQLite…: the CREATE VIEW statement
		if src, err = objectDefinition(ctx, e, db, ObjView, name); err == nil {
			t := lexSQL(src, e)
			if as := findWord(t, 0, "AS"); as >= 0 {
				src = joinRaw(t[as+1:], e)
			}
			src = strings.TrimSuffix(strings.TrimSpace(src), ";")
		}
	}
	if err != nil {
		return "", nil, err
	}
	cols, _ := listColumns(ctx, e, db, name)
	return strings.TrimSpace(src), cols, nil
}

// mView is a view to create on the target.
type mView struct {
	it   *migItem
	stmt string
	deps []string
}

// migrateViews creates the views. Those that fail are returned, not failed,
// when final is false: they may read functions that are created next.
func (m *migrator) migrateViews(ctx context.Context, final bool) []*mView {
	names := append(slices.Clone(m.groups[ObjView]), m.groups[ObjMatView]...)
	if len(names) == 0 {
		return nil
	}
	m.setPhase("Creating the views", len(names))
	var views []*mView
	for _, n := range names {
		if ctx.Err() != nil {
			return nil
		}
		m.step(n)
		it := m.item(ObjView, n, m.targetName(n))
		query, cols, err := m.viewQuery(ctx, n)
		if err != nil {
			it.fail(fmt.Errorf("reading it: %w", err))
			continue
		}
		it.Source = query
		m.x.notes, m.x.todo, m.x.vars, m.x.consts = nil, false, nil, nil
		toks := lexSQL(query, m.srcEngine)
		body := m.x.render(toks, true)
		var qc []string
		for _, c := range cols {
			qc = append(qc, m.q(m.limit(m.x.applyCase(c))))
		}
		stmt := "CREATE VIEW " + m.q(it.Target)
		if len(qc) > 0 {
			stmt += " (" + strings.Join(qc, ", ") + ")"
		}
		stmt += " AS\n" + body
		it.Notes = append(it.Notes, m.x.notes...)
		if m.x.todo {
			it.Status = "check"
		}
		v := &mView{it: it, stmt: stmt}
		for _, t := range toks { // the other views it reads
			if t.k == tWord || t.k == tIdent {
				for _, o := range names {
					if o != n && (strings.EqualFold(t.s, o) || strings.HasSuffix(strings.ToUpper(o), "."+strings.ToUpper(t.s))) {
						v.deps = append(v.deps, o)
					}
				}
			}
		}
		views = append(views, v)
	}
	return m.createViews(ctx, viewOrder(views), final)
}

// viewOrder puts every view after the views it reads (a cycle keeps the
// order it had).
func viewOrder(views []*mView) []*mView {
	byName := map[string]*mView{}
	for _, v := range views {
		byName[v.it.Name] = v
	}
	var out []*mView
	state := map[*mView]int{} // 1 visiting, 2 done
	var visit func(v *mView)
	visit = func(v *mView) {
		if state[v] != 0 {
			return
		}
		state[v] = 1
		for _, d := range v.deps {
			if dv := byName[d]; dv != nil {
				visit(dv)
			}
		}
		state[v] = 2
		out = append(out, v)
	}
	for _, v := range views {
		visit(v)
	}
	return out
}

// createViews runs the views' statements; on the database, a view that
// fails is tried again after the others. With final, those that still fail
// are failed; otherwise they are returned to be tried later.
func (m *migrator) createViews(ctx context.Context, pending []*mView, final bool) []*mView {
	for pass := 0; len(pending) > 0 && pass < 10; pass++ {
		var again []*mView
		for _, v := range pending {
			if ctx.Err() != nil {
				return nil
			}
			err := m.exec(ctx, nil, v.stmt, false)
			if err != nil && m.tdb != nil {
				v.it.Error = err.Error()
				again = append(again, v)
				continue
			}
			v.it.SQL = v.stmt
			v.it.Error = ""
			m.stepDone()
		}
		if len(again) == len(pending) {
			pending = again
			break
		}
		pending = again
	}
	if !final {
		return pending
	}
	for _, v := range pending {
		v.it.SQL = v.stmt
		v.it.fail(errors.New(v.it.Error))
		m.stepDone()
	}
	return nil
}

// ---- functions, procedures and triggers ------------------------------------------------

// fbCode is the code of a Firebird database, parsed.
type fbCode struct {
	procs, funcs, triggers []*psqlRoutineItem
}

type psqlRoutineItem struct {
	*psqlRoutine
	err      error
	inactive bool
	replaced string // the trigger is replaced by an auto-increment column
	def      string // the definition, as cactusdb shows it
}

func (c *fbCode) trigger(name string) *psqlRoutineItem {
	if c == nil {
		return nil
	}
	for _, t := range c.triggers {
		if t.name == name {
			return t
		}
	}
	return nil
}

// readFirebirdCode reads and parses the procedures, functions and triggers.
func (m *migrator) readFirebirdCode(ctx context.Context) *fbCode {
	db := m.src.db
	code := &fbCode{}
	exc := map[string]string{}
	scanRows(ctx, db, `SELECT TRIM(RDB$EXCEPTION_NAME), RDB$MESSAGE FROM RDB$EXCEPTIONS`, nil, func(scan func(...any) error) error {
		var n string
		var msg sql.NullString
		if scan(&n, &msg) == nil {
			exc[strings.ToUpper(n)] = msg.String
		}
		return nil
	})
	domains := map[string]string{}
	scanRows(ctx, db, `SELECT TRIM(f.RDB$FIELD_NAME), `+fbFieldCols+` FROM RDB$FIELDS f
		WHERE f.RDB$FIELD_NAME NOT STARTING WITH 'RDB$' AND COALESCE(f.RDB$SYSTEM_FLAG, 0) = 0`, nil, func(scan func(...any) error) error {
		var f fbField
		var n string
		if scan(append([]any{&n}, f.typePtrs()...)...) == nil {
			domains[strings.ToUpper(n)] = f.typeName(false)
		}
		return nil
	})
	params := func(q, name string) (ins, outs []mCol, ret *mCol) {
		ps, _ := firebirdParams(ctx, db, q, name)
		for _, p := range ps {
			c := mCol{Name: p.name, SrcType: p.typeName(false)}
			c.Kind, c.Len, c.Prec, c.Scale = classifyType(EngineFirebird, c.SrcType)
			switch {
			case strings.Contains(q, "RDB$FUNCTION_ARGUMENTS") && p.paramType.Int64 == 0:
				cc := c
				ret = &cc
			case strings.Contains(q, "RDB$FUNCTION_ARGUMENTS") || p.paramType.Int64 == 0:
				ins = append(ins, c)
			default:
				outs = append(outs, c)
			}
		}
		return
	}
	parse := func(r *psqlRoutine) *psqlRoutineItem {
		r.excMessages, r.domains = exc, domains
		it := &psqlRoutineItem{psqlRoutine: r}
		r.decls, r.body, it.err = parseBody(r.source)
		if it.err == nil && r.kind == "procedure" {
			r.selectable = hasKind(r.body, "suspend")
		}
		return it
	}
	if m.req.Routines {
		for _, n := range m.groups[ObjProcedure] {
			src, err := fbString(ctx, db, `SELECT RDB$PROCEDURE_SOURCE FROM RDB$PROCEDURES WHERE RDB$PROCEDURE_NAME = ?`, n)
			r := &psqlRoutine{kind: "procedure", name: n, source: src}
			r.ins, r.outs, _ = params(`SELECT TRIM(p.RDB$PARAMETER_NAME), TRIM(p.RDB$FIELD_SOURCE), p.RDB$PARAMETER_TYPE, `+fbFieldCols+`
				FROM RDB$PROCEDURE_PARAMETERS p JOIN RDB$FIELDS f ON f.RDB$FIELD_NAME = p.RDB$FIELD_SOURCE
				WHERE p.RDB$PROCEDURE_NAME = ? ORDER BY p.RDB$PARAMETER_TYPE, p.RDB$PARAMETER_NUMBER`, n)
			it := parse(r)
			if err != nil {
				it.err = err
			}
			it.def, _ = objectDefinition(ctx, EngineFirebird, db, ObjProcedure, n)
			if it.err == nil && r.selectable {
				m.x.procs[strings.ToUpper(n)] = true
			}
			code.procs = append(code.procs, it)
		}
		for _, n := range m.groups[ObjFunction] {
			def, _ := objectDefinition(ctx, EngineFirebird, db, ObjFunction, n)
			var src sql.NullString
			err := db.QueryRowContext(ctx, `SELECT RDB$FUNCTION_SOURCE FROM RDB$FUNCTIONS WHERE RDB$FUNCTION_NAME = ?`, n).Scan(&src)
			r := &psqlRoutine{kind: "function", name: n, source: src.String}
			r.ins, _, r.returns = params(`SELECT TRIM(a.RDB$ARGUMENT_NAME), TRIM(a.RDB$FIELD_SOURCE), a.RDB$ARGUMENT_POSITION, `+fbFieldCols+`
				FROM RDB$FUNCTION_ARGUMENTS a JOIN RDB$FIELDS f ON f.RDB$FIELD_NAME = a.RDB$FIELD_SOURCE
				WHERE a.RDB$FUNCTION_NAME = ? AND a.RDB$PACKAGE_NAME IS NULL ORDER BY a.RDB$ARGUMENT_POSITION`, n)
			it := &psqlRoutineItem{psqlRoutine: r, def: def}
			switch {
			case err != nil || !src.Valid:
				it.err = errors.New("an external function (UDF): there is no source to translate")
			case r.returns == nil:
				it.err = errors.New("its result type couldn't be read")
			default:
				it = parse(r)
				it.def = def
			}
			code.funcs = append(code.funcs, it)
		}
	}
	for _, n := range m.groups[ObjTrigger] {
		var rel, src sql.NullString
		var typ, inactive sql.NullInt64
		err := db.QueryRowContext(ctx, `SELECT TRIM(RDB$RELATION_NAME), RDB$TRIGGER_TYPE, RDB$TRIGGER_INACTIVE, RDB$TRIGGER_SOURCE
			FROM RDB$TRIGGERS WHERE RDB$TRIGGER_NAME = ?`, n).Scan(&rel, &typ, &inactive, &src)
		r := &psqlRoutine{kind: "trigger", name: n, table: rel.String, source: src.String}
		it := &psqlRoutineItem{psqlRoutine: r, inactive: inactive.Int64 == 1}
		if err == nil {
			when := firebirdTriggerType(typ.Int64)
			if w, ev, ok := strings.Cut(when, " "); ok && (w == "BEFORE" || w == "AFTER") && rel.String != "" {
				r.when, r.events = w, strings.Split(ev, " OR ")
				it = parse(r)
				it.inactive = inactive.Int64 == 1
			} else {
				err = fmt.Errorf("a database trigger (%s): only table triggers are translated", when)
			}
		}
		if err != nil {
			it.err = err
		}
		it.def, _ = objectDefinition(ctx, EngineFirebird, db, ObjTrigger, n)
		code.triggers = append(code.triggers, it)
	}
	return code
}

// migrateCode creates the functions, procedures and triggers: translated
// from Firebird's PSQL, or copied between databases of the same kind.
func (m *migrator) migrateCode(ctx context.Context, fb *fbCode) {
	req := m.req
	type unit struct {
		it    *migItem
		stmts []string
	}
	var units []*unit
	add := func(typ, name, def string) *migItem {
		it := m.item(typ, name, m.targetName(name))
		it.Source = def
		return it
	}
	translate := fb != nil && (mysqlFamily(m.dst) || m.dst == EnginePostgres)
	same := m.srcEngine == m.dst || sameKind(m.srcEngine, m.dst)
	switch {
	case fb != nil && translate:
		var all []*psqlRoutineItem
		if req.Routines {
			all = append(append(all, fb.funcs...), fb.procs...)
		}
		if req.Triggers {
			all = append(all, fb.triggers...)
		}
		m.setPhase("Translating the procedures and triggers", len(all))
		for _, r := range all {
			it := add(r.kind, r.name, r.def)
			switch {
			case r.replaced != "":
				it.Status = "skipped"
				it.Notes = append(it.Notes, r.replaced)
				continue
			case r.inactive:
				it.Status = "skipped"
				it.Notes = append(it.Notes, "inactive in the source: not created")
				continue
			case r.err != nil:
				it.Status = "manual"
				it.Error = r.err.Error()
				continue
			}
			if r.kind == "trigger" {
				if t := m.byName[strings.ToUpper(r.table)]; t == nil {
					it.fail(fmt.Errorf("its table %s wasn't migrated", r.table))
					continue
				}
			}
			m.x.notes, m.x.todo = nil, false
			stmts := writeRoutine(m.x, r.psqlRoutine)
			it.Notes = append(it.Notes, m.x.notes...)
			if m.x.todo {
				it.Status = "check"
			}
			units = append(units, &unit{it, stmts})
		}
	case same:
		var types []string
		if req.Routines {
			types = append(types, ObjFunction, ObjProcedure)
		}
		if req.Triggers {
			types = append(types, ObjTrigger)
		}
		total := 0
		for _, t := range types {
			total += len(m.groups[t])
		}
		m.setPhase("Copying the procedures and triggers", total)
		for _, typ := range types {
			for _, n := range m.groups[typ] {
				def, err := objectDefinition(ctx, m.srcEngine, m.src.db, typ, n)
				it := add(typ, n, def)
				if err != nil {
					it.fail(fmt.Errorf("reading it: %w", err))
					continue
				}
				def = cleanDef(m.srcEngine, "", def)
				if m.x.nameCase != "" {
					it.Notes = append(it.Notes, "copied as it is: its names aren't changed to "+m.x.nameCase+" case")
				}
				units = append(units, &unit{it, splitCopiedDef(m.srcEngine, def)})
			}
		}
	default:
		total := 0
		var types []string
		if req.Routines {
			types = append(types, ObjFunction, ObjProcedure, ObjPackage)
		}
		if req.Triggers {
			types = append(types, ObjTrigger)
		}
		for _, t := range types {
			total += len(m.groups[t])
		}
		if total == 0 {
			return
		}
		m.setPhase("Listing the procedures and triggers to translate by hand", total)
		for _, typ := range types {
			for _, n := range m.groups[typ] {
				def, _ := objectDefinition(ctx, m.srcEngine, m.src.db, typ, n)
				it := add(typ, n, def)
				if r := fb.trigger(n); r != nil && r.replaced != "" {
					it.Status = "skipped"
					it.Notes = append(it.Notes, r.replaced)
					m.stepDone()
					continue
				}
				it.Status = "manual"
				it.Notes = append(it.Notes, fmt.Sprintf("cactusdb translates Firebird's PSQL to MySQL, MariaDB and PostgreSQL; %s code to %s is rewritten by hand",
					engineName(m.srcEngine), engineName(m.dst)))
				m.stepDone()
			}
		}
		return
	}
	pending := units
	for pass := 0; len(pending) > 0 && pass < 5; pass++ {
		var again []*unit
		for _, u := range pending {
			if ctx.Err() != nil {
				return
			}
			m.step(u.it.Name)
			u.it.SQL, u.it.Error = "", ""
			var err error
			for _, s := range u.stmts {
				if err = m.exec(ctx, u.it, s, true); err != nil {
					break
				}
			}
			if err != nil && m.tdb != nil {
				u.it.Error = err.Error()
				again = append(again, u)
				continue
			}
			m.stepDone()
		}
		if len(again) == len(pending) {
			pending = again
			break
		}
		pending = again
	}
	for _, u := range pending {
		u.it.fail(errors.New(u.it.Error))
		m.stepDone()
	}
}

// splitCopiedDef turns a copied definition into the statements to run:
// PostgreSQL's trigger definition is a function and a CREATE TRIGGER.
func splitCopiedDef(engine, def string) []string {
	def = strings.TrimSpace(def)
	if engine == EngineMSSQL {
		var out []string
		for _, p := range regexp.MustCompile(`(?im)^\s*GO\s*$`).Split(def, -1) {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	if engine == EngineFirebird {
		return []string{strings.TrimSuffix(def, ";")}
	}
	return []string{strings.TrimSuffix(def, ";")}
}

// ---- the new database ------------------------------------------------------------------

type targetInfo struct {
	version string
	fbMajor int
	mariadb bool
}

// createTarget creates the new database on the target server and opens it
// for writing. It refuses a database that already exists.
func createTarget(ctx context.Context, p ConnParams, engine, name string, pipesConcat bool) (*sql.DB, ConnParams, targetInfo, error) {
	var info targetInfo
	opts := parseOptions(p.Options)
	np := p
	np.Engine = engine
	np.Database = name
	switch dialect(engine) {
	case EngineMySQL, EngineMariaDB:
		cfg := mysql.NewConfig()
		cfg.User, cfg.Passwd, cfg.Net, cfg.Addr = p.Username, p.Password, "tcp", hostPort(p.Host, p.Port, 3306)
		cfg.ParseTime = true
		cfg.Timeout = 10 * time.Second
		// NO_AUTO_VALUE_ON_ZERO: a row with id 0 keeps it (it would get a new number).
		mode := "STRICT_TRANS_TABLES,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION,NO_AUTO_VALUE_ON_ZERO"
		if pipesConcat {
			mode += ",PIPES_AS_CONCAT"
		}
		cfg.Params = map[string]string{"sql_mode": "'" + mode + "'"}
		db, err := openAndPing(ctx, "mysql", cfg.FormatDSN(), 0)
		if err != nil {
			return nil, np, info, fmt.Errorf("connecting to the target server: %w", err)
		}
		var n int
		db.QueryRowContext(ctx, `SELECT VERSION()`).Scan(&info.version)
		info.mariadb = strings.Contains(strings.ToLower(info.version), "mariadb")
		err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?`, name).Scan(&n)
		if err == nil && n > 0 {
			err = fmt.Errorf("the database %s already exists: cactusdb only migrates into a new database", name)
		}
		if err == nil {
			_, err = db.ExecContext(ctx, "CREATE DATABASE "+quoteIdent(EngineMySQL, name)+" CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
		}
		db.Close()
		if err != nil {
			return nil, np, info, err
		}
		cfg.DBName = name
		db, err = openAndPing(ctx, "mysql", cfg.FormatDSN(), 0)
		return db, np, info, err
	case EnginePostgres:
		// cactusdb's sessions are read-only; this one writes the new database.
		opts["default_transaction_read_only"] = "off"
		admin := p
		if admin.Database == "" {
			admin.Database = "postgres"
		}
		db, err := openAndPing(ctx, "pgx", postgresDSN(admin, opts), 0)
		if err != nil {
			return nil, np, info, fmt.Errorf("connecting to the target server: %w", err)
		}
		db.QueryRowContext(ctx, `SHOW server_version`).Scan(&info.version)
		var exists bool
		err = db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists)
		if err == nil && exists {
			err = fmt.Errorf("the database %s already exists: cactusdb only migrates into a new database", name)
		}
		if err == nil {
			_, err = db.ExecContext(ctx, "CREATE DATABASE "+quoteIdent(EnginePostgres, name)+" ENCODING 'UTF8' TEMPLATE template0")
		}
		db.Close()
		if err != nil {
			return nil, np, info, err
		}
		info.version = "PostgreSQL " + info.version
		db, err = openAndPing(ctx, "pgx", postgresDSN(np, opts), 0)
		return db, np, info, err
	case EngineMSSQL:
		admin := p
		admin.Engine = EngineMSSQL
		db, err := openAndPing(ctx, "sqlserver", mssqlDSN(admin, opts), 0)
		if err != nil {
			return nil, np, info, fmt.Errorf("connecting to the target server: %w", err)
		}
		db.QueryRowContext(ctx, `SELECT CAST(SERVERPROPERTY('ProductVersion') AS NVARCHAR(40))`).Scan(&info.version)
		var id sql.NullInt64
		err = db.QueryRowContext(ctx, `SELECT DB_ID(@p1)`, name).Scan(&id)
		if err == nil && id.Valid {
			err = fmt.Errorf("the database %s already exists: cactusdb only migrates into a new database", name)
		}
		if err == nil {
			_, err = db.ExecContext(ctx, "CREATE DATABASE "+quoteIdent(EngineMSSQL, name))
		}
		db.Close()
		if err != nil {
			return nil, np, info, err
		}
		info.version = "SQL Server " + info.version
		np.Engine = EngineMSSQL
		db, err = openAndPing(ctx, "sqlserver", mssqlDSN(np, opts), 0)
		return db, np, info, err
	case EngineSQLite:
		np = ConnParams{Engine: EngineSQLite, Database: name}
		// Created here, only if it doesn't exist (O_EXCL): a file that
		// appeared since the check is never written into.
		f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			if errors.Is(err, fs.ErrExist) {
				err = fmt.Errorf("%s already exists: cactusdb only migrates into a new database", name)
			}
			return nil, np, info, err
		}
		f.Close()
		db, err := openAndPing(ctx, "sqlite", sqliteURI(name)+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)", 1)
		if err != nil {
			os.Remove(name) // the empty file just made, so trying again isn't refused
			return nil, np, info, err
		}
		db.QueryRowContext(ctx, `SELECT 'SQLite ' || sqlite_version()`).Scan(&info.version)
		return db, np, info, nil
	case EngineFirebird:
		path := name
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		u := url.URL{Scheme: "firebird", User: url.UserPassword(p.Username, p.Password), Host: hostPort(p.Host, p.Port, 3050), Path: path}
		if db, err := openAndPing(ctx, "firebirdsql", u.String(), 1); err == nil {
			db.Close()
			return nil, np, info, fmt.Errorf("the database %s already exists: cactusdb only migrates into a new database", name)
		}
		cdb, err := openAndPing(ctx, "firebirdsql_createdb", u.String(), 1)
		if err != nil {
			return nil, np, info, fmt.Errorf("creating %s on the Firebird server: %w", name, err)
		}
		cdb.Close()
		db, err := openAndPing(ctx, "firebirdsql", u.String(), 0)
		if err != nil {
			return nil, np, info, err
		}
		var v string
		db.QueryRowContext(ctx, `SELECT rdb$get_context('SYSTEM', 'ENGINE_VERSION') FROM rdb$database`).Scan(&v)
		info.version = "Firebird " + v
		if maj, _, ok := strings.Cut(v, "."); ok {
			info.fbMajor, _ = strconv.Atoi(maj)
		}
		return db, np, info, nil
	}
	return nil, np, info, fmt.Errorf("cactusdb can't create a %s database", engineName(engine))
}
