package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ☰ → Compare databases: the structure (DDL) of a master database against
// one or more others. Every object's definition is read the same way the DDL
// export reads it, the differences are listed per object, and for each other
// database a script is generated that would make its structure match the
// master's. cactusdb never runs that script: it only shows it.

// compareSource is one database to compare: a saved connection (with its
// connection or superuser password) or the browser's open connection.
type compareSource struct {
	Session       string `json:"session"`
	SavedID       int64  `json:"savedId"`
	LockPassword  string `json:"lockPassword"`
	SuperPassword string `json:"superPassword"`
}

type compareReq struct {
	Master compareSource   `json:"master"`
	Others []compareSource `json:"others"`
	Drops  bool            `json:"drops"` // the scripts drop what the master doesn't have
}

// compareDB is an opened database to read.
type compareDB struct {
	label, engine string
	db            *sql.DB
	close         bool // opened for the comparison (not the browser's connection)
}

// schemaObj is one object's definition.
type schemaObj struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Def  string `json:"def,omitempty"`
	Err  string `json:"error,omitempty"`
	key  string // its definition with comments and spacing removed, for comparing
}

// schemaSnap is every object of one database, by type and lower-case name.
type schemaSnap struct {
	Label, Engine string
	objs          map[string]*schemaObj
	order         []string // keys in DDL order
}

func objKey(typ, name string) string { return typ + "\x00" + strings.ToLower(name) }

// compareJob is a comparison running in the background, polled by the page.
type compareJob struct {
	mu      sync.Mutex
	created time.Time
	total   int // objects to read, all databases together (0 until listed)
	done    int
	reading []string // the databases still being read
	result  *compareResult
	err     string
}

type compareCell struct {
	Status string   `json:"status"` // same, diff, missing (only in the master), extra (not in the master), absent, present (other kind of database)
	Def    string   `json:"def,omitempty"`
	Err    string   `json:"error,omitempty"`
	Notes  []string `json:"notes,omitempty"`
}

type compareObject struct {
	Type   string        `json:"type"`
	Name   string        `json:"name"`
	Master *schemaObj    `json:"master,omitempty"`
	Cells  []compareCell `json:"cells"`
}

type compareTarget struct {
	Label      string         `json:"label"`
	Engine     string         `json:"engine"`
	Comparable bool           `json:"comparable"` // the same kind of database as the master: definitions compared, script made
	Counts     map[string]int `json:"counts"`
	Script     string         `json:"script"`
}

type compareResult struct {
	Master  compareTarget   `json:"master"`
	Targets []compareTarget `json:"targets"`
	Objects []compareObject `json:"objects"`
	Seconds float64         `json:"seconds"`
}

// openCompareSource opens one database of a comparison. On failure it has
// written the error and returns ok false.
func (s *server) openCompareSource(w http.ResponseWriter, r *http.Request, src compareSource) (*compareDB, bool) {
	if src.Session != "" {
		ss, err := s.session(r, src.Session)
		if err != nil {
			writeErr(w, 401, err)
			return nil, false
		}
		if e := engineByID(ss.kind()); e != nil && e.NoCompare {
			writeErr(w, 400, fmt.Errorf("%s: %s databases can't be compared", ss.label, e.Name))
			return nil, false
		}
		return &compareDB{label: ss.label, engine: ss.engine, db: ss.db}, true
	}
	c, _, ok := s.unlockConnAs(w, r, src.SavedID, src.LockPassword, src.SuperPassword)
	if !ok {
		return nil, false
	}
	if e := engineByID(c.Engine); e != nil && e.NoCompare {
		writeErr(w, 400, fmt.Errorf("%s: %s databases can't be compared", c.Name, e.Name))
		return nil, false
	}
	db, err := openDB(r.Context(), c.ConnParams)
	if err != nil {
		writeErr(w, 400, fmt.Errorf("%s: connection failed: %w", c.Name, explainConnErr(c.ConnParams, err)))
		return nil, false
	}
	return &compareDB{label: c.Name, engine: dialect(c.Engine), db: db, close: true}, true
}

func (s *server) handleCompare(w http.ResponseWriter, r *http.Request) {
	var req compareReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if len(req.Others) == 0 {
		writeErr(w, 400, errors.New("choose at least one database to compare with the master"))
		return
	}
	var dbs []*compareDB
	closeAll := func() {
		for _, d := range dbs {
			if d.close {
				d.db.Close()
			}
		}
	}
	for _, src := range append([]compareSource{req.Master}, req.Others...) {
		d, ok := s.openCompareSource(w, r, src)
		if !ok {
			closeAll()
			return
		}
		dbs = append(dbs, d)
	}
	job := &compareJob{created: time.Now()}
	for _, d := range dbs {
		job.reading = append(job.reading, d.label)
	}
	id := newID()
	s.mu.Lock()
	if s.compares == nil {
		s.compares = map[string]*compareJob{}
	}
	for k, j := range s.compares { // results are kept for an hour
		if time.Since(j.created) > time.Hour {
			delete(s.compares, k)
		}
	}
	s.compares[id] = job
	s.mu.Unlock()
	names := make([]string, len(dbs)-1)
	for i, d := range dbs[1:] {
		names[i] = fmt.Sprintf("%q", d.label)
	}
	logAction(r, "compare structure of %q with %s", dbs[0].label, strings.Join(names, ", "))
	go func() {
		defer closeAll()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		res, err := runCompare(ctx, job, dbs, req.Drops)
		job.mu.Lock()
		defer job.mu.Unlock()
		job.reading = nil
		if err != nil {
			job.err = err.Error()
			log.Printf("compare of %q FAILED: %v", dbs[0].label, err)
			return
		}
		job.result = res
		log.Printf("compare of %q finished in %.1fs, %d objects", dbs[0].label, res.Seconds, len(res.Objects))
	}()
	writeJSON(w, 200, map[string]any{"job": id})
}

func (s *server) handleCompareStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	job := s.compares[r.PathValue("job")]
	s.mu.Unlock()
	if job == nil {
		writeErr(w, 404, errors.New("this comparison is no longer available: compare again"))
		return
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	writeJSON(w, 200, map[string]any{"total": job.total, "done": job.done, "reading": job.reading,
		"error": job.err, "finished": job.result != nil || job.err != "", "result": job.result})
}

// runCompare reads every database's objects (the databases at the same
// time) and compares them with the master's, dbs[0].
func runCompare(ctx context.Context, job *compareJob, dbs []*compareDB, drops bool) (*compareResult, error) {
	start := time.Now()
	snaps := make([]*schemaSnap, len(dbs))
	errs := make([]error, len(dbs))
	var wg sync.WaitGroup
	for i, d := range dbs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snaps[i], errs[i] = readSchema(ctx, d, job)
			job.mu.Lock()
			for k, l := range job.reading {
				if l == d.label {
					job.reading = append(job.reading[:k:k], job.reading[k+1:]...)
					break
				}
			}
			job.mu.Unlock()
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("%s: %w", dbs[i].label, err)
		}
	}
	res := compareSnaps(snaps, drops)
	res.Seconds = time.Since(start).Seconds()
	return res, nil
}

// readSchema reads the definition of every object of a database.
func readSchema(ctx context.Context, d *compareDB, job *compareJob) (*schemaSnap, error) {
	groups, err := listObjects(ctx, d.engine, d.db)
	if err != nil {
		return nil, err
	}
	targets := ddlTargets(groups)
	job.mu.Lock()
	job.total += len(targets)
	job.mu.Unlock()
	dbName := ""
	if mysqlFamily(d.engine) {
		d.db.QueryRowContext(ctx, `SELECT DATABASE()`).Scan(&dbName)
	}
	snap := &schemaSnap{Label: d.label, Engine: d.engine, objs: map[string]*schemaObj{}}
	for _, t := range targets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var src string
		if t.typ == ObjTable {
			src, err = tableDefinition(ctx, d.engine, d.db, t.name, true)
		} else {
			src, err = objectDefinition(ctx, d.engine, d.db, t.typ, t.name)
		}
		o := &schemaObj{Type: t.typ, Name: t.name, Def: cleanDef(d.engine, dbName, src)}
		if err != nil {
			o.Err = strings.ReplaceAll(err.Error(), "\n", " ")
		}
		o.key = defKey(o.Def)
		k := objKey(t.typ, t.name)
		if _, dup := snap.objs[k]; !dup {
			snap.order = append(snap.order, k)
		}
		snap.objs[k] = o
		job.mu.Lock()
		job.done++
		job.mu.Unlock()
	}
	return snap, nil
}

func mysqlFamily(engine string) bool { return engine == EngineMySQL || engine == EngineMariaDB }

// sameKind reports whether two databases' definitions can be compared.
func sameKind(a, b string) bool { return a == b || mysqlFamily(a) && mysqlFamily(b) }

var (
	reAutoIncrement = regexp.MustCompile(` AUTO_INCREMENT=\d+`)
	reDefiner       = regexp.MustCompile("DEFINER=(`[^`]*`|'[^']*'|[^@\\s]+)@(`[^`]*`|'[^']*'|\\S+)\\s*")
)

// cleanDef removes what differs between two copies of the same object but
// isn't its structure: MySQL's next AUTO_INCREMENT value, the DEFINER user
// and the database's own name in front of object names; and line endings
// and trailing spaces.
func cleanDef(engine, dbName, def string) string {
	def = strings.ReplaceAll(def, "\r\n", "\n")
	if mysqlFamily(engine) {
		def = reAutoIncrement.ReplaceAllString(def, "")
		def = reDefiner.ReplaceAllString(def, "")
		if dbName != "" {
			def = strings.ReplaceAll(def, quoteIdent(engine, dbName)+".", "")
		}
	}
	lines := strings.Split(strings.TrimSpace(def), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.Join(lines, "\n")
}

// defKey is a definition with comment lines removed and all spacing
// reduced to single spaces: two definitions with the same key are the same.
func defKey(def string) string {
	var b strings.Builder
	for _, l := range strings.Split(def, "\n") {
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "--") {
			b.WriteString(t)
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// compareSnaps compares snaps[1:] with the master, snaps[0].
func compareSnaps(snaps []*schemaSnap, drops bool) *compareResult {
	master := snaps[0]
	res := &compareResult{Master: compareTarget{Label: master.Label, Engine: master.Engine, Comparable: true}}
	// Every object of any database, the master's first.
	var keys []string
	seen := map[string]bool{}
	for _, s := range snaps {
		for _, k := range s.order {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	rank := map[string]int{}
	for i, t := range ddlOrder {
		rank[t] = i
	}
	sort.SliceStable(keys, func(i, j int) bool {
		ti, _, _ := strings.Cut(keys[i], "\x00")
		tj, _, _ := strings.Cut(keys[j], "\x00")
		if rank[ti] != rank[tj] {
			return rank[ti] < rank[tj]
		}
		return keys[i] < keys[j]
	})
	tdiffs := make([]map[string]*tableDiff, len(snaps)) // per target: the tables that differ
	for _, k := range keys {
		mo := master.objs[k]
		co := compareObject{Master: mo}
		for _, s := range snaps {
			if o := s.objs[k]; o != nil {
				co.Type, co.Name = o.Type, o.Name
				break
			}
		}
		if mo != nil {
			co.Name = mo.Name
		}
		for ti, s := range snaps[1:] {
			o := s.objs[k]
			cell := compareCell{}
			switch {
			case mo == nil && o == nil:
				cell.Status = "absent"
			case o == nil:
				cell.Status = "missing"
			case mo == nil:
				cell.Status = "extra"
				cell.Def, cell.Err = o.Def, o.Err
			case !sameKind(master.Engine, s.Engine):
				cell.Status = "present"
			case mo.key == o.key && mo.Err == "" && o.Err == "":
				cell.Status = "same"
			default:
				cell.Status = "diff"
				cell.Def, cell.Err = o.Def, o.Err
				if mo.Type == ObjTable && mo.Err == "" && o.Err == "" {
					mt, ok1 := parseTable(mo.Def)
					tt, ok2 := parseTable(o.Def)
					if ok1 && ok2 {
						td := diffTables(mt, tt)
						cell.Notes = td.notes()
						if td.empty() {
							// Only the text differs (spacing, or the order
							// of columns, keys or indexes).
							cell.Status = "same"
							break
						}
						if tdiffs[ti+1] == nil {
							tdiffs[ti+1] = map[string]*tableDiff{}
						}
						tdiffs[ti+1][k] = td
					}
				}
			}
			co.Cells = append(co.Cells, cell)
		}
		res.Objects = append(res.Objects, co)
	}
	for ti, s := range snaps[1:] {
		t := compareTarget{Label: s.Label, Engine: s.Engine, Comparable: sameKind(master.Engine, s.Engine), Counts: map[string]int{}}
		for _, o := range res.Objects {
			t.Counts[o.Cells[ti].Status]++
		}
		if t.Comparable {
			t.Script = syncScript(master, s, res.Objects, ti, tdiffs[ti+1], drops)
		} else {
			t.Script = fmt.Sprintf("-- %s is a %s database and the master %s is %s:\n-- only databases of the same kind can be compared object by object, so no script was made.\n",
				s.Label, s.Engine, master.Label, master.Engine)
		}
		res.Targets = append(res.Targets, t)
	}
	return res
}

// ---- tables, column by column ----------------------------------------------

type tableItem struct {
	name, key, text string // key: the lower-case name
}

// tableShape is a CREATE TABLE statement taken apart.
type tableShape struct {
	head  string      // CREATE TABLE name
	cols  []tableItem // column definitions
	cons  []string    // constraints and keys inside the parentheses
	tail  string      // table options after them (MySQL: ENGINE=…)
	extra []string    // the statements after it: indexes, foreign keys
}

// scanSQL calls fn for every byte of s outside quotes and identifiers in
// quotes, with the parenthesis depth; fn returns false to stop.
func scanSQL(s string, fn func(i, depth int) bool) {
	depth := 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
			continue
		case '[':
			quote = ']'
			continue
		case '(':
			depth++
		case ')':
			depth--
		}
		if !fn(i, depth) {
			return
		}
	}
}

// splitTop splits s at the sep bytes outside parentheses and quotes.
func splitTop(s string, sep byte) []string {
	var out []string
	last := 0
	scanSQL(s, func(i, depth int) bool {
		if s[i] == sep && depth == 0 {
			out = append(out, s[last:i])
			last = i + 1
		}
		return true
	})
	return append(out, s[last:])
}

// firstIdent returns the identifier s starts with, without its quotes.
func firstIdent(s string) string {
	if s == "" {
		return ""
	}
	end := map[byte]byte{'`': '`', '"': '"', '[': ']'}[s[0]]
	if end != 0 {
		if j := strings.IndexByte(s[1:], end); j >= 0 {
			return s[1 : j+1]
		}
		return s[1:]
	}
	if j := strings.IndexAny(s, " \t\n("); j >= 0 {
		return s[:j]
	}
	return s
}

var constraintWords = map[string]bool{"PRIMARY": true, "KEY": true, "INDEX": true, "UNIQUE": true, "CONSTRAINT": true,
	"FOREIGN": true, "CHECK": true, "FULLTEXT": true, "SPATIAL": true, "EXCLUDE": true, "PERIOD": true}

// stripComments removes whole comment lines.
func stripComments(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "--") {
			out = append(out, l)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// parseTable takes a CREATE TABLE statement (with its indexes and foreign
// keys after it, as tableDefinition returns it) apart.
func parseTable(def string) (*tableShape, bool) {
	open, close := -1, -1
	scanSQL(def, func(i, depth int) bool {
		if def[i] == '(' && depth == 1 && open < 0 {
			open = i
		}
		if def[i] == ')' && depth == 0 && open >= 0 {
			close = i
			return false
		}
		return true
	})
	if open < 0 || close < 0 {
		return nil, false
	}
	t := &tableShape{head: strings.TrimSpace(def[:open])}
	if !strings.Contains(strings.ToUpper(t.head), "TABLE") {
		return nil, false
	}
	for _, it := range splitTop(def[open+1:close], ',') {
		it = strings.TrimSpace(stripComments(it))
		if it == "" {
			continue
		}
		word := strings.ToUpper(firstIdent(it))
		if it[0] != '`' && it[0] != '"' && it[0] != '[' && constraintWords[word] {
			t.cons = append(t.cons, it)
			continue
		}
		name := firstIdent(it)
		t.cols = append(t.cols, tableItem{name: name, key: strings.ToLower(name), text: it})
	}
	rest := strings.Split(def[close+1:], "\n")
	// The table options run to the first ';'; then come the other statements.
	stmts := splitTop(strings.Join(rest, "\n"), ';')
	t.tail = strings.TrimSpace(stripComments(stmts[0]))
	for _, st := range stmts[1:] {
		if st = stripComments(st); st != "" && !strings.EqualFold(st, "GO") {
			t.extra = append(t.extra, st)
		}
	}
	return t, true
}

// tableDiff is what the master's table has that the other's doesn't (add)
// and the other way round (drop).
type tableDiff struct {
	mcols               []tableItem // the master's columns, in order
	order               bool        // the same columns, in another order
	addCols, dropCols   []tableItem
	changed             [][2]tableItem // master's, other's
	addCons, dropCons   []string
	addExtra, dropExtra []string
	tail                [2]string // the table options, when they differ
}

func diffTables(m, t *tableShape) *tableDiff {
	d := &tableDiff{mcols: m.cols}
	tc := map[string]tableItem{}
	for _, c := range t.cols {
		tc[c.key] = c
	}
	mc := map[string]bool{}
	for _, c := range m.cols {
		mc[c.key] = true
		o, ok := tc[c.key]
		switch {
		case !ok:
			d.addCols = append(d.addCols, c)
		case defKey(o.text) != defKey(c.text):
			d.changed = append(d.changed, [2]tableItem{c, o})
		}
	}
	for _, c := range t.cols {
		if !mc[c.key] {
			d.dropCols = append(d.dropCols, c)
		}
	}
	if len(d.addCols)+len(d.dropCols) == 0 && len(m.cols) == len(t.cols) {
		for i := range m.cols {
			if m.cols[i].key != t.cols[i].key {
				d.order = true
			}
		}
	}
	d.addCons, d.dropCons = setDiff(m.cons, t.cons)
	d.addExtra, d.dropExtra = setDiff(m.extra, t.extra)
	if defKey(m.tail) != defKey(t.tail) {
		d.tail = [2]string{m.tail, t.tail}
	}
	return d
}

// setDiff returns the items only in a, and those only in b.
func setDiff(a, b []string) (onlyA, onlyB []string) {
	in := func(list []string) map[string]bool {
		m := map[string]bool{}
		for _, s := range list {
			m[defKey(s)] = true
		}
		return m
	}
	ia, ib := in(a), in(b)
	for _, s := range a {
		if !ib[defKey(s)] {
			onlyA = append(onlyA, s)
		}
	}
	for _, s := range b {
		if !ia[defKey(s)] {
			onlyB = append(onlyB, s)
		}
	}
	return
}

// empty reports whether the tables have the same columns, keys and indexes
// (perhaps in another order).
func (d *tableDiff) empty() bool {
	return len(d.addCols)+len(d.dropCols)+len(d.changed)+len(d.addCons)+len(d.dropCons)+len(d.addExtra)+len(d.dropExtra) == 0 &&
		d.tail[0] == "" && d.tail[1] == ""
}

// notes describes the differences for the page.
func (d *tableDiff) notes() []string {
	var n []string
	if d.order {
		n = append(n, "the same columns, in another order")
	}
	for _, c := range d.addCols {
		n = append(n, "column "+c.name+" missing: "+oneLine(c.text))
	}
	for _, c := range d.dropCols {
		n = append(n, "column "+c.name+" not in the master: "+oneLine(c.text))
	}
	for _, c := range d.changed {
		n = append(n, "column "+c[0].name+" differs: master "+oneLine(c[0].text)+" / here "+oneLine(c[1].text))
	}
	for _, c := range d.addCons {
		n = append(n, "missing: "+oneLine(c))
	}
	for _, c := range d.dropCons {
		n = append(n, "not in the master: "+oneLine(c))
	}
	for _, c := range d.addExtra {
		n = append(n, "missing: "+oneLine(c))
	}
	for _, c := range d.dropExtra {
		n = append(n, "not in the master: "+oneLine(c))
	}
	if d.tail[0] != "" || d.tail[1] != "" {
		n = append(n, "table options differ: master "+oneLine(d.tail[0])+" / here "+oneLine(d.tail[1]))
	}
	return n
}

// ---- the script ------------------------------------------------------------

// scriptWriter collects the statements of a sync script.
type scriptWriter struct {
	engine string
	b      strings.Builder
}

func (w *scriptWriter) comment(format string, args ...any) {
	for _, l := range strings.Split(fmt.Sprintf(format, args...), "\n") {
		w.b.WriteString("-- " + l + "\n")
	}
}

// stmt writes one statement with the terminator the engine's tools need.
func (w *scriptWriter) stmt(s string) {
	s = strings.TrimSuffix(strings.TrimSpace(s), ";")
	if w.engine == EngineMSSQL {
		w.b.WriteString(s + "\nGO\n")
		return
	}
	w.b.WriteString(s + ";\n")
}

// section starts a part of the script.
func (w *scriptWriter) section(title string) { w.b.WriteString("\n-- ==== " + title + "\n") }

// commentOut writes src as comment lines, for what must be done by hand.
func (w *scriptWriter) commentOut(src string) {
	for _, l := range strings.Split(strings.TrimSpace(src), "\n") {
		w.b.WriteString("--   " + l + "\n")
	}
}

// qualify quotes an object name the way the engine lists it.
func qualify(engine, name string) string { return quoteObject(engine, name) }

// routineName quotes a PostgreSQL function name "schema.name(args)".
func routineName(engine, name string) string {
	if engine != EnginePostgres {
		return qualify(engine, name)
	}
	base, args, ok := strings.Cut(name, "(")
	if !ok {
		return qualify(engine, name)
	}
	return qualify(engine, base) + "(" + args
}

// dropObject returns the statement that drops an object.
func dropObject(engine, typ, name string) string {
	q := qualify(engine, name)
	switch typ {
	case ObjTable:
		return "DROP TABLE " + q
	case ObjView:
		return "DROP VIEW " + q
	case ObjMatView:
		return "DROP MATERIALIZED VIEW " + q
	case ObjProcedure:
		return "DROP PROCEDURE " + routineName(engine, name)
	case ObjFunction:
		return "DROP FUNCTION " + routineName(engine, name)
	case ObjTrigger:
		if engine == EnginePostgres {
			// Listed as schema.table.trigger.
			if i := strings.LastIndex(name, "."); i > 0 {
				return "DROP TRIGGER " + quoteIdent(engine, name[i+1:]) + " ON " + qualify(engine, name[:i])
			}
		}
		return "DROP TRIGGER " + q
	case ObjSequence:
		return "DROP SEQUENCE " + q
	case ObjDomain:
		return "DROP DOMAIN " + q
	case ObjException:
		return "DROP EXCEPTION " + q
	case ObjPackage:
		return "DROP PACKAGE " + q
	case ObjEvent:
		return "DROP EVENT " + q
	case ObjSynonym:
		return "DROP SYNONYM " + q
	case ObjIndex:
		return "DROP INDEX " + q
	}
	return ""
}

var (
	identRE      = "(`[^`]+`|\"(?:[^\"]|\"\")+\"|\\[[^\\]]+\\]|[^\\s(]+)"
	reCreateIdx  = regexp.MustCompile(`(?is)^\s*CREATE\s+(?:UNIQUE\s+)?(?:(?:ASC|ASCENDING|DESC|DESCENDING|CLUSTERED|NONCLUSTERED)\s+)*INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?` + identRE + `\s+ON\s+(?:ONLY\s+)?` + identRE)
	reAddConstr  = regexp.MustCompile(`(?is)^\s*(ALTER\s+TABLE\s+.+?)\s+ADD\s+CONSTRAINT\s+` + identRE)
	reInlineName = regexp.MustCompile(`(?is)^\s*CONSTRAINT\s+` + identRE + `\s+(\w+)`)
	reMySQLKey   = regexp.MustCompile("(?is)^\\s*(?:UNIQUE\\s+|FULLTEXT\\s+|SPATIAL\\s+)?(?:KEY|INDEX)\\s+" + identRE)
)

// dropExtra returns the statement that undoes one of the statements after a
// CREATE TABLE (CREATE INDEX, ALTER TABLE … ADD CONSTRAINT), or "".
func dropExtra(engine, table, st string) string {
	if m := reCreateIdx.FindStringSubmatch(st); m != nil {
		switch engine {
		case EngineMySQL, EngineMariaDB, EngineMSSQL:
			return "DROP INDEX " + m[1] + " ON " + m[2]
		case EnginePostgres:
			// The index is in its table's schema.
			if schema, _, ok := strings.Cut(table, "."); ok && !strings.Contains(m[1], ".") {
				return "DROP INDEX " + quoteIdent(engine, schema) + "." + m[1]
			}
		}
		return "DROP INDEX " + m[1]
	}
	if m := reAddConstr.FindStringSubmatch(st); m != nil {
		return m[1] + " DROP CONSTRAINT " + m[2]
	}
	return ""
}

// dropInline returns the statement that drops a constraint written inside
// CREATE TABLE, or "" when it has no name to drop it by.
func dropInline(engine, q, item string) string {
	if mysqlFamily(engine) {
		up := strings.ToUpper(item)
		if strings.HasPrefix(up, "PRIMARY KEY") {
			return "ALTER TABLE " + q + " DROP PRIMARY KEY"
		}
		if m := reMySQLKey.FindStringSubmatch(item); m != nil {
			return "ALTER TABLE " + q + " DROP INDEX " + m[1]
		}
		if m := reInlineName.FindStringSubmatch(item); m != nil {
			switch strings.ToUpper(m[2]) {
			case "FOREIGN":
				return "ALTER TABLE " + q + " DROP FOREIGN KEY " + m[1]
			case "UNIQUE":
				return "ALTER TABLE " + q + " DROP INDEX " + m[1]
			}
			return "ALTER TABLE " + q + " DROP CONSTRAINT " + m[1]
		}
		return ""
	}
	if m := reInlineName.FindStringSubmatch(item); m != nil {
		return "ALTER TABLE " + q + " DROP CONSTRAINT " + m[1]
	}
	return ""
}

// columnParts splits a column definition built by cactusdb (Firebird, SQL
// Server, PostgreSQL) into its type, default, and whether it is NOT NULL.
// special is set for computed, generated and identity columns.
func columnParts(text string) (typ, def string, notNull, special bool) {
	rest := strings.TrimSpace(text[len(quoteIdentLen(text)):])
	up := strings.ToUpper(rest)
	if strings.HasPrefix(up, "AS ") || strings.HasPrefix(up, "COMPUTED ") || strings.Contains(up, " GENERATED ") || strings.Contains(up, " IDENTITY(") {
		special = true
	}
	if strings.HasSuffix(up, " NOT NULL") {
		notNull = true
		rest, up = rest[:len(rest)-9], up[:len(up)-9]
	} else if strings.HasSuffix(up, " NULL") {
		rest, up = rest[:len(rest)-5], up[:len(up)-5]
	}
	typ = rest
	if i := strings.Index(up, " DEFAULT "); i >= 0 {
		typ, def = rest[:i], strings.TrimSpace(rest[i+9:])
	}
	return strings.TrimSpace(typ), def, notNull, special
}

// quoteIdentLen returns the column name at the start of a column
// definition, with its quotes.
func quoteIdentLen(text string) string {
	name := firstIdent(text)
	if text != "" && (text[0] == '`' || text[0] == '"' || text[0] == '[') {
		return text[:len(name)+2]
	}
	return name
}

// alterColumn writes the statements that change column o to c (the
// master's definition).
func alterColumn(w *scriptWriter, q string, c, o tableItem) {
	col := quoteIdentLen(c.text)
	switch w.engine {
	case EngineMySQL, EngineMariaDB:
		w.stmt("ALTER TABLE " + q + " MODIFY COLUMN " + c.text)
		return
	case EngineSQLite:
		w.comment("SQLite can't change a column: rebuild the table to change %s to:", c.name)
		w.commentOut(c.text)
		return
	}
	mt, md, mn, ms := columnParts(c.text)
	ot, od, on, os := columnParts(o.text)
	if ms || os {
		w.comment("%s is a computed, generated or identity column: change it by hand to:", c.name)
		w.commentOut(c.text)
		return
	}
	switch w.engine {
	case EnginePostgres:
		if mt != ot {
			w.stmt("ALTER TABLE " + q + " ALTER COLUMN " + col + " TYPE " + mt)
		}
		if md != od {
			if md == "" {
				w.stmt("ALTER TABLE " + q + " ALTER COLUMN " + col + " DROP DEFAULT")
			} else {
				w.stmt("ALTER TABLE " + q + " ALTER COLUMN " + col + " SET DEFAULT " + md)
			}
		}
		if mn != on {
			w.stmt("ALTER TABLE " + q + " ALTER COLUMN " + col + map[bool]string{true: " SET NOT NULL", false: " DROP NOT NULL"}[mn])
		}
	case EngineMSSQL:
		if mt != ot || mn != on {
			w.stmt("ALTER TABLE " + q + " ALTER COLUMN " + col + " " + mt + map[bool]string{true: " NOT NULL", false: " NULL"}[mn])
		}
		if md != od {
			w.comment("the default of %s differs (master: %s, here: %s): SQL Server keeps defaults as constraints, change it by hand", c.name, orNone(md), orNone(od))
		}
	case EngineFirebird:
		if mt != ot {
			w.stmt("ALTER TABLE " + q + " ALTER COLUMN " + col + " TYPE " + mt)
		}
		if md != od {
			if md == "" {
				w.stmt("ALTER TABLE " + q + " ALTER COLUMN " + col + " DROP DEFAULT")
			} else {
				// Firebird keeps the word DEFAULT in the default's source.
				w.stmt("ALTER TABLE " + q + " ALTER COLUMN " + col + " SET DEFAULT " + md)
			}
		}
		if mn != on {
			w.stmt("ALTER TABLE " + q + " ALTER " + col + map[bool]string{true: " SET NOT NULL", false: " DROP NOT NULL"}[mn])
		}
	}
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// isFK reports whether a constraint or statement is a foreign key.
func isFK(s string) bool { return strings.Contains(strings.ToUpper(s), "FOREIGN KEY") }

// syncScript writes the script that makes database t's structure (the
// target ti of objs) match the master's.
func syncScript(master, t *schemaSnap, objs []compareObject, ti int, tdiffs map[string]*tableDiff, drops bool) string {
	engine := t.Engine
	w := &scriptWriter{engine: engine}
	fmt.Fprintf(&w.b, "-- Structure changes that make %s match the master %s (%s)\n", t.Label, master.Label, engine)
	fmt.Fprintf(&w.b, "-- made by cactusdb %s on %s. cactusdb doesn't run it: read it, take a\n-- backup, then run it with your own tool.\n",
		version, time.Now().Format("2006-01-02 15:04:05"))
	if !drops {
		w.comment("objects the master doesn't have are kept (dropping them was not chosen)")
	}
	if mysqlFamily(engine) {
		w.b.WriteString("\nSET FOREIGN_KEY_CHECKS = 0;\n")
	}
	changes := 0

	// 1. Drop what the master doesn't have, and the views, routines and
	// triggers that differ (they are created again below); dependents first.
	var dropped []string
	for i := len(ddlOrder) - 1; i >= 0; i-- {
		typ := ddlOrder[i]
		for _, o := range objs {
			if o.Type != typ {
				continue
			}
			st := o.Cells[ti].Status
			_, inPlace := replaceInPlace(engine, typ, defOf(o.Master))
			if st == "extra" && drops || st == "diff" && typ != ObjTable && !inPlace && !byHand(typ) {
				if st == "extra" && typ == ObjTable {
					dropped = append(dropped, "-- WARNING: drops table "+o.Name+" and every row in it\n")
				} else {
					dropped = append(dropped, "")
				}
				dropped[len(dropped)-1] += dropTerm(w, dropObject(engine, o.Type, o.Name))
			}
		}
	}
	if len(dropped) > 0 {
		w.section("drop what differs or isn't in the master")
		for _, d := range dropped {
			w.b.WriteString(d)
		}
		changes += len(dropped)
	}

	// 2. Tables: create the missing ones, change the ones that differ.
	var fks []string
	tables := false
	for _, o := range objs {
		if o.Type != ObjTable {
			continue
		}
		st := o.Cells[ti].Status
		if st != "missing" && st != "diff" {
			continue
		}
		if !tables {
			w.section("tables")
			tables = true
		}
		changes++
		q := qualify(engine, o.Name)
		if st == "missing" {
			w.b.WriteString("\n-- new table " + o.Name + "\n")
			shape, ok := parseTable(o.Master.Def)
			if !ok || mysqlFamily(engine) || engine == EngineSQLite {
				w.b.WriteString(terminate(engine, ObjTable, o.Master.Def))
				continue
			}
			// Foreign keys are added once every table exists.
			var items []string
			for _, c := range shape.cols {
				items = append(items, c.text)
			}
			for _, c := range shape.cons {
				if isFK(c) {
					fks = append(fks, "ALTER TABLE "+q+" ADD "+c)
				} else {
					items = append(items, c)
				}
			}
			w.stmt(shape.head + " (\n  " + strings.Join(items, ",\n  ") + "\n)" + spaced(shape.tail))
			for _, x := range shape.extra {
				if isFK(x) {
					fks = append(fks, x)
				} else {
					w.stmt(x)
				}
			}
			continue
		}
		w.b.WriteString("\n-- table " + o.Name + "\n")
		d := tdiffs[objKey(o.Type, o.Name)]
		if d == nil {
			w.comment("the definitions differ but couldn't be compared column by column; the master's is:")
			w.commentOut(o.Master.Def)
			continue
		}
		alterTable(w, q, o.Name, d, drops)
	}
	if len(fks) > 0 {
		w.section("foreign keys of the new tables")
		for _, fk := range fks {
			w.stmt(fk)
		}
	}

	// 3. Create the other missing objects, and those that differ again.
	for _, typ := range ddlOrder {
		if typ == ObjTable {
			continue
		}
		first := true
		for _, o := range objs {
			if o.Type != typ {
				continue
			}
			st := o.Cells[ti].Status
			if st != "missing" && st != "diff" {
				continue
			}
			if first {
				w.section(typePlural[typ])
				first = false
			}
			changes++
			w.b.WriteString("\n-- " + map[string]string{"missing": "new", "diff": "changed"}[st] + " " + typ + " " + o.Name + "\n")
			if o.Master.Err != "" || o.Master.Def == "" {
				w.comment("the master's definition couldn't be read: %s", orNone(o.Master.Err))
				continue
			}
			def := o.Master.Def
			if st == "diff" {
				if byHand(typ) {
					// Dropping it would lose its value, or fail while columns use it.
					w.comment("change it by hand to the master's definition:")
					w.commentOut(def)
					continue
				}
				if d, ok := replaceInPlace(engine, typ, def); ok {
					def = d
				}
			}
			w.b.WriteString(terminate(engine, typ, def))
		}
	}
	if mysqlFamily(engine) {
		w.b.WriteString("\nSET FOREIGN_KEY_CHECKS = 1;\n")
	}
	if changes == 0 {
		w.b.WriteString("\n-- nothing to change: the structure is the same as the master's\n")
	}
	return w.b.String()
}

func spaced(s string) string {
	if s == "" {
		return ""
	}
	return " " + s
}

// dropTerm returns a drop statement with its terminator.
func dropTerm(w *scriptWriter, st string) string {
	if st == "" {
		return ""
	}
	if w.engine == EngineMSSQL {
		return st + "\nGO\n"
	}
	return st + ";\n"
}

// alterTable writes the statements that change a table to the master's.
func alterTable(w *scriptWriter, q, name string, d *tableDiff, drops bool) {
	engine := w.engine
	add := " ADD COLUMN "
	drop := " DROP COLUMN "
	switch engine {
	case EngineMSSQL:
		add = " ADD "
	case EngineFirebird:
		add, drop = " ADD ", " DROP "
	}
	// Constraints and indexes go first, so a column they use can be dropped.
	for _, x := range d.dropExtra {
		if st := dropExtra(engine, name, x); st != "" {
			w.stmt(st)
		} else if !strings.HasPrefix(strings.ToUpper(x), "ALTER INDEX") {
			w.comment("not in the master, remove by hand: %s", oneLine(x))
		}
	}
	for _, c := range d.dropCons {
		if engine == EngineSQLite {
			w.comment("not in the master (SQLite can't drop it without rebuilding the table): %s", oneLine(c))
		} else if st := dropInline(engine, q, c); st != "" {
			w.stmt(st)
		} else {
			w.comment("not in the master, drop it by its constraint name: %s", oneLine(c))
		}
	}
	if drops {
		for _, c := range d.dropCols {
			w.comment("WARNING: drops column %s and its data", c.name)
			w.stmt("ALTER TABLE " + q + drop + quoteIdentLen(c.text))
		}
	} else {
		for _, c := range d.dropCols {
			w.comment("column %s isn't in the master (kept)", c.name)
		}
	}
	for _, c := range d.addCols {
		where := ""
		if mysqlFamily(engine) {
			// Where the master has it.
			where = " FIRST"
			for i, mc := range d.mcols {
				if mc.key == c.key && i > 0 {
					where = " AFTER " + quoteIdentLen(d.mcols[i-1].text)
				}
			}
		}
		w.stmt("ALTER TABLE " + q + add + c.text + where)
	}
	for _, c := range d.changed {
		alterColumn(w, q, c[0], c[1])
	}
	for _, c := range d.addCons {
		if engine == EngineSQLite {
			w.comment("missing (SQLite can't add it without rebuilding the table): %s", oneLine(c))
		} else {
			w.stmt("ALTER TABLE " + q + " ADD " + c)
		}
	}
	for _, x := range d.addExtra {
		w.stmt(x)
	}
	if d.tail[0] != "" || d.tail[1] != "" {
		w.comment("the table options differ, change them by hand: master %s / here %s", orNone(oneLine(d.tail[0])), orNone(oneLine(d.tail[1])))
	}
}

// byHand reports whether a changed object of this type is left for the user
// to change: a sequence keeps its current value, a domain is used by columns.
func byHand(typ string) bool { return typ == ObjSequence || typ == ObjDomain }

func defOf(o *schemaObj) string {
	if o == nil {
		return ""
	}
	return o.Def
}

var reCreate = regexp.MustCompile(`(?is)^((?:\s*(?:--[^\n]*\n|/\*.*?\*/))*\s*)CREATE\s+(OR\s+(?:ALTER|REPLACE)\s+)?`)

// replaceInPlace returns a definition that replaces an existing object of
// the same name, so it needn't be dropped first (which fails while other
// objects use it): CREATE OR ALTER (Firebird, SQL Server) or CREATE OR
// REPLACE (PostgreSQL views and routines).
func replaceInPlace(engine, typ, def string) (string, bool) {
	m := reCreate.FindStringSubmatchIndex(def)
	if m == nil {
		return def, false
	}
	orAlter := m[4] >= 0
	switch engine {
	case EngineFirebird:
		switch typ {
		case ObjView, ObjProcedure, ObjFunction, ObjPackage, ObjTrigger, ObjException:
			if orAlter {
				return def, true
			}
			return def[:m[3]] + "CREATE OR ALTER " + def[m[1]:], true
		}
	case EngineMSSQL:
		switch typ {
		case ObjView, ObjProcedure, ObjFunction, ObjTrigger:
			if orAlter {
				return def, true
			}
			return def[:m[3]] + "CREATE OR ALTER " + def[m[1]:], true
		}
	case EnginePostgres:
		switch typ {
		case ObjView, ObjProcedure, ObjFunction:
			return def, orAlter
		}
	}
	return def, false
}

var typePlural = map[string]string{ObjDomain: "domains", ObjSequence: "sequences", ObjException: "exceptions",
	ObjTable: "tables", ObjIndex: "indexes", ObjView: "views", ObjMatView: "materialized views", ObjFunction: "functions",
	ObjProcedure: "procedures", ObjPackage: "packages", ObjTrigger: "triggers", ObjEvent: "events", ObjSynonym: "synonyms"}
