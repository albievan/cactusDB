package main

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// The SQL of a project query (SQLite's dialect), read just enough to know
// which project tables and columns it uses, and which of its conditions can
// narrow what is read from a source database (pushdown). Anything not
// understood is left to SQLite, and reads more, never less.

type ptok struct {
	kind byte   // w: word, q: quoted name, s: string, n: number, p: punctuation or operator
	text string // as written (a quoted name or string without its quotes)
	pos  int
}

func (t ptok) is(word string) bool { return t.kind == 'w' && strings.EqualFold(t.text, word) }

// ptokens splits SQL into tokens, without comments.
func ptokens(src string) ([]ptok, error) {
	var out []ptok
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
			i++
		case c == '-' && i+1 < len(src) && src[i+1] == '-':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return nil, errors.New("a /* comment isn't closed")
			}
			i += end + 4
		case c == '\'' || c == '"' || c == '`' || c == '[':
			close := c
			if c == '[' {
				close = ']'
			}
			var b strings.Builder
			j := i + 1
			for ; j < len(src); j++ {
				if src[j] == close {
					if close != ']' && j+1 < len(src) && src[j+1] == close { // doubled: one of it
						b.WriteByte(close)
						j++
						continue
					}
					break
				}
				b.WriteByte(src[j])
			}
			if j >= len(src) {
				return nil, fmt.Errorf("a %c isn't closed", c)
			}
			kind := byte('q')
			if c == '\'' {
				kind = 's'
			}
			out = append(out, ptok{kind, b.String(), i})
			i = j + 1
		case c >= '0' && c <= '9' || c == '.' && i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '9':
			j := i
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.' || src[j] == 'e' || src[j] == 'E' ||
				(src[j] == '+' || src[j] == '-') && j > i && (src[j-1] == 'e' || src[j-1] == 'E') || src[j] == 'x' || src[j] == 'X' ||
				j > i+1 && (src[i+1] == 'x' || src[i+1] == 'X') && strings.ContainsRune("abcdefABCDEF", rune(src[j]))) {
				j++
			}
			out = append(out, ptok{'n', src[i:j], i})
			i = j
		case c == '_' || c == '$' || unicode.IsLetter(rune(c)) || c >= 0x80:
			j := i
			for j < len(src) && (src[j] == '_' || src[j] == '$' || src[j] >= '0' && src[j] <= '9' || unicode.IsLetter(rune(src[j])) || src[j] >= 0x80) {
				j++
			}
			out = append(out, ptok{'w', src[i:j], i})
			i = j
		default:
			j := i + 1
			if j < len(src) {
				if two := src[i : j+1]; slices.Contains([]string{"<=", ">=", "<>", "!=", "==", "||", "<<", ">>", "->"}, two) {
					j++
				}
			}
			out = append(out, ptok{'p', src[i:j], i})
			i = j
		}
	}
	return out, nil
}

// name reports whether t names something (a word or a quoted name).
func (t ptok) name() bool { return t.kind == 'w' || t.kind == 'q' }

// clauseWords end a FROM or WHERE clause (and can't be a table alias).
var clauseWords = []string{"WHERE", "GROUP", "ORDER", "LIMIT", "HAVING", "WINDOW", "UNION", "INTERSECT", "EXCEPT", "ON", "USING",
	"JOIN", "LEFT", "RIGHT", "FULL", "INNER", "CROSS", "NATURAL", "OUTER", "OFFSET", "SELECT", "FROM", "AS", "AND", "OR", "NOT",
	"INDEXED", "WITH", "VALUES", "RETURNING"}

func isClauseWord(t ptok) bool {
	return t.kind == 'w' && slices.ContainsFunc(clauseWords, func(w string) bool { return strings.EqualFold(w, t.text) })
}

// pref is one use of a project table in a query.
type pref struct {
	table   *projectTable
	alias   string // its table alias in the query (default: its name)
	depth   int    // parenthesis depth (0: the outer query)
	tokIdx  int
	nullabl bool // on the nullable side of an outer join of the outer query
}

// projectQuery is what a query uses.
type projectQuery struct {
	refs    []pref
	columns map[*projectTable]map[string]bool // columns used per table (lower case); nil: all
	words   map[string]bool                   // every unqualified name in the query (lower case)
	star    bool                              // SELECT *: every column of every table
	allOf   []*projectTable                   // alias.*: every column of these
	toks    []ptok
}

// errNotInProject names a table the query uses that isn't in the project, so
// the page can offer to add it.
type errNotInProject struct{ Alias, Table string }

func (e *errNotInProject) Error() string {
	return fmt.Sprintf("%s.%s isn't a table of the project: add it to the project (Tables) to use it", e.Alias, e.Table)
}

// analyzeProjectQuery finds the project tables a query uses, their aliases
// and the columns it may use.
func analyzeProjectQuery(q string, d *projectData) (*projectQuery, error) {
	toks, err := ptokens(q)
	if err != nil {
		return nil, err
	}
	aliases := map[string]bool{}
	for _, l := range d.Links {
		aliases[strings.ToLower(l.Alias)] = true
	}
	find := func(alias, name string) *projectTable {
		for i := range d.Tables {
			t := &d.Tables[i]
			if strings.EqualFold(t.Alias, alias) && strings.EqualFold(t.Name, name) {
				return t
			}
		}
		return nil
	}
	pq := &projectQuery{columns: map[*projectTable]map[string]bool{}, words: map[string]bool{}, toks: toks}
	// The names WITH defines: FROM may name them without an alias.
	ctes := map[string]bool{}
	for i := 0; i+1 < len(toks); i++ {
		if toks[i].name() && (toks[i+1].is("AS") || toks[i+1].text == "(" && i > 0 && (toks[i-1].is("WITH") || toks[i-1].text == "," || toks[i-1].is("RECURSIVE"))) {
			if i > 0 && (toks[i-1].is("WITH") || toks[i-1].is("RECURSIVE") || toks[i-1].text == ",") {
				ctes[strings.ToLower(toks[i].text)] = true
			}
		}
	}
	depth := 0
	tableAliases := map[string]*projectTable{} // lower-case table alias -> table
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		switch t.text {
		case "(":
			if t.kind == 'p' {
				depth++
			}
		case ")":
			if t.kind == 'p' {
				depth--
			}
		}
		// alias.table, not alias.table.column
		if t.name() && i+2 < len(toks) && toks[i+1].text == "." && toks[i+1].kind == 'p' && toks[i+2].name() && aliases[strings.ToLower(t.text)] &&
			(i == 0 || toks[i-1].text != ".") {
			name := toks[i+2].text
			if i+4 < len(toks) && toks[i+3].text == "." && toks[i+3].kind == 'p' && toks[i+4].name() { // a column: alias.table.column
				pt := find(t.text, name)
				if pt == nil {
					return nil, &errNotInProject{t.text, name}
				}
				pq.use(pt, toks[i+4].text)
				i += 4
				continue
			}
			pt := find(t.text, name)
			if pt == nil {
				return nil, &errNotInProject{t.text, name}
			}
			r := pref{table: pt, alias: pt.Name, depth: depth, tokIdx: i}
			j := i + 3
			if j < len(toks) && toks[j].is("AS") {
				j++
			}
			if j < len(toks) && toks[j].name() && !isClauseWord(toks[j]) && toks[j].kind != 's' {
				r.alias = toks[j].text
				if aliases[strings.ToLower(r.alias)] {
					return nil, fmt.Errorf("the table alias %q is also a database alias: choose another", r.alias)
				}
				i = j
			} else {
				i += 2
			}
			tableAliases[strings.ToLower(r.alias)] = pt
			pq.refs = append(pq.refs, r)
			continue
		}
		// FROM or JOIN followed by a single name: a table without its database alias.
		if (t.is("FROM") || t.is("JOIN")) && i+1 < len(toks) && toks[i+1].name() && !isClauseWord(toks[i+1]) &&
			(i+2 >= len(toks) || toks[i+2].text != "." && toks[i+2].text != "(") && !ctes[strings.ToLower(toks[i+1].text)] {
			return nil, fmt.Errorf("%s: name tables as <database alias>.<table>, e.g. %s.%s", toks[i+1].text,
				firstOr(d.Links, "db"), toks[i+1].text)
		}
	}
	if len(pq.refs) == 0 {
		return nil, errors.New("the query uses no table of the project: name them as <database alias>.<table>")
	}
	// Columns: q.c with q a table alias; q.* or * reads every column.
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.kind == 'p' && t.text == "*" && (i == 0 || toks[i-1].text != ".") && i > 0 && (toks[i-1].is("SELECT") || toks[i-1].text == "," || toks[i-1].is("DISTINCT") || toks[i-1].is("ALL")) {
			for _, r := range pq.refs {
				pq.columns[r.table] = nil
			}
			pq.all(true)
			continue
		}
		if t.name() && i+2 < len(toks) && toks[i+1].text == "." && toks[i+1].kind == 'p' {
			if pt := tableAliases[strings.ToLower(t.text)]; pt != nil && !aliases[strings.ToLower(t.text)] {
				if toks[i+2].text == "*" {
					pq.columns[pt] = nil
					pq.markAll(pt)
				} else if toks[i+2].name() {
					pq.use(pt, toks[i+2].text)
				}
				i += 2
				continue
			}
		}
		if t.name() && (i == 0 || toks[i-1].text != ".") {
			pq.words[strings.ToLower(t.text)] = true
		}
	}
	return pq, nil
}

func firstOr(links []projectLink, def string) string {
	if len(links) > 0 {
		return links[0].Alias
	}
	return def
}

// use notes column c of table pt as used.
func (pq *projectQuery) use(pt *projectTable, c string) {
	if m, ok := pq.columns[pt]; ok && m == nil {
		return // every column already
	}
	if pq.columns[pt] == nil {
		pq.columns[pt] = map[string]bool{}
	}
	pq.columns[pt][strings.ToLower(c)] = true
}

func (pq *projectQuery) markAll(pt *projectTable) { pq.columns[pt] = nil; pq.allOf = append(pq.allOf, pt) }

func (pq *projectQuery) all(b bool) { pq.star = b }

// needed lists the columns of table pt (of all its columns) the query may
// use: those named with its alias, any column named without an alias (a
// name of another table's column too: reading more is harmless), and all
// of them for * or alias.*.
func (pq *projectQuery) needed(pt *projectTable, all []string) []string {
	if pq.star || slices.Contains(pq.allOf, pt) {
		return all
	}
	var out []string
	for _, c := range all {
		lc := strings.ToLower(c)
		if pq.columns[pt][lc] || pq.words[lc] {
			out = append(out, c)
		}
	}
	if len(out) == 0 && len(all) > 0 { // e.g. SELECT COUNT(*) FROM t: one column keeps the rows
		out = all[:1]
	}
	return out
}

// ---- pushdown -----------------------------------------------------------------

// pushCond is a condition sent to the source with a table's read: the rows
// it returns are always a superset of the rows the query keeps, because the
// condition also stays in the query that runs on the staged copy.
type pushCond struct {
	Col  string // the source column
	Op   string // =, <, <=, >, >=, IS NULL, IS NOT NULL, BETWEEN
	Args []any  // 0, 1 or 2 values
	Text string // as written, for the log and the page
}

var isoDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}([ T]\d{2}:\d{2}(:\d{2}(\.\d+)?)?)?$`)

// pushdown works out the conditions of the outer query's WHERE that can be
// sent to the source of each table. kinds gives a column's kind (see
// classifyType) per table; sqlite: the table's source keeps dates as text.
func (pq *projectQuery) pushdown(kinds func(pt *projectTable, col string) (string, bool), textDates func(pt *projectTable) bool) map[*projectTable][]pushCond {
	out := map[*projectTable][]pushCond{}
	toks := pq.toks
	// A compound query (UNION…) at the top: nothing is pushed.
	depth := 0
	start := -1
	for i, t := range toks {
		if t.kind == 'p' && t.text == "(" {
			depth++
		} else if t.kind == 'p' && t.text == ")" {
			depth--
		} else if depth == 0 {
			if t.is("UNION") || t.is("INTERSECT") || t.is("EXCEPT") {
				return out
			}
			if t.is("SELECT") && start < 0 {
				start = i
			}
		}
	}
	if start < 0 {
		return out
	}
	// The outer query's FROM: which tables are on the nullable side of an
	// outer join; a table used more than once (anywhere) gets nothing.
	uses := map[*projectTable]int{}
	for _, r := range pq.refs {
		uses[r.table]++
	}
	outer := map[string]*pref{} // lower-case alias -> ref of the outer query
	for i := range pq.refs {
		r := &pq.refs[i]
		if r.depth == 0 && r.tokIdx > start {
			outer[strings.ToLower(r.alias)] = r
		}
	}
	depth = 0
	var seen []*pref
	where, end := -1, len(toks)
	for i := start; i < len(toks); i++ {
		t := toks[i]
		if t.kind == 'p' && t.text == "(" {
			depth++
			continue
		}
		if t.kind == 'p' && t.text == ")" {
			depth--
			continue
		}
		if depth != 0 {
			continue
		}
		if t.is("LEFT") || t.is("RIGHT") || t.is("FULL") {
			// The table after this JOIN (LEFT, FULL) and those before it (RIGHT, FULL) may be NULL.
			for j := i + 1; j < len(toks); j++ {
				if r := refAt(pq.refs, j); r != nil {
					if !t.is("RIGHT") {
						r.nullabl = true
					}
					break
				}
				if toks[j].is("SELECT") {
					break
				}
			}
			if !t.is("LEFT") {
				for _, r := range seen {
					r.nullabl = true
				}
			}
		}
		if r := refAt(pq.refs, i); r != nil {
			seen = append(seen, r)
		}
		if t.is("WHERE") && where < 0 {
			where = i + 1
		} else if where >= 0 && (t.is("GROUP") || t.is("ORDER") || t.is("LIMIT") || t.is("HAVING") || t.is("WINDOW")) {
			end = i
			break
		}
	}
	if where < 0 {
		return out
	}
	conds := toks[where:end]
	// OR at the top of WHERE: nothing can be split off.
	depth = 0
	for _, t := range conds {
		if t.kind == 'p' && t.text == "(" {
			depth++
		} else if t.kind == 'p' && t.text == ")" {
			depth--
		} else if depth == 0 && t.is("OR") {
			return out
		}
	}
	for _, c := range splitAnd(conds) {
		r, col, cond, ok := parseCond(c)
		if !ok {
			continue
		}
		ref := outer[strings.ToLower(r)]
		if ref == nil || ref.nullabl || uses[ref.table] != 1 {
			continue
		}
		kind, ok := kinds(ref.table, col)
		if !ok {
			continue
		}
		if pc, ok := typedCond(cond, col, kind, textDates(ref.table)); ok {
			pc.Text = condText(c)
			out[ref.table] = append(out[ref.table], pc)
		}
	}
	return out
}

func refAt(refs []pref, i int) *pref {
	for j := range refs {
		if refs[j].tokIdx == i {
			return &refs[j]
		}
	}
	return nil
}

func condText(c []ptok) string {
	var parts []string
	for _, t := range c {
		switch t.kind {
		case 's':
			parts = append(parts, "'"+strings.ReplaceAll(t.text, "'", "''")+"'")
		case 'q':
			parts = append(parts, `"`+t.text+`"`)
		default:
			parts = append(parts, t.text)
		}
	}
	return strings.ReplaceAll(strings.Join(parts, " "), " . ", ".")
}

// splitAnd splits the conditions at the top-level ANDs (not BETWEEN's AND).
func splitAnd(toks []ptok) [][]ptok {
	var out [][]ptok
	depth, from, between := 0, 0, false
	for i, t := range toks {
		switch {
		case t.kind == 'p' && t.text == "(":
			depth++
		case t.kind == 'p' && t.text == ")":
			depth--
		case depth == 0 && t.is("BETWEEN"):
			between = true
		case depth == 0 && t.is("AND"):
			if between {
				between = false
				continue
			}
			out = append(out, toks[from:i])
			from = i + 1
		}
	}
	return append(out, toks[from:])
}

// rawCond is one condition as written: the column (by table alias) and the
// operator with its literal values.
type rawCond struct {
	op   string
	vals []ptok
}

// parseCond reads alias.col OP literal (or literal OP alias.col), alias.col
// BETWEEN literal AND literal, or alias.col IS [NOT] NULL.
func parseCond(c []ptok) (alias, col string, rc rawCond, ok bool) {
	lit := func(ts []ptok) (ptok, int, bool) { // a literal, possibly signed
		if len(ts) >= 1 && (ts[0].kind == 's' || ts[0].kind == 'n') {
			return ts[0], 1, true
		}
		if len(ts) >= 2 && ts[0].kind == 'p' && (ts[0].text == "-" || ts[0].text == "+") && ts[1].kind == 'n' {
			t := ts[1]
			if ts[0].text == "-" {
				t.text = "-" + t.text
			}
			return t, 2, true
		}
		return ptok{}, 0, false
	}
	colAt := func(ts []ptok) (string, string, bool) {
		if len(ts) >= 3 && ts[0].name() && ts[1].kind == 'p' && ts[1].text == "." && ts[2].name() {
			return ts[0].text, ts[2].text, true
		}
		return "", "", false
	}
	ops := map[string]string{"=": "=", "==": "=", "<": "<", "<=": "<=", ">": ">", ">=": ">="}
	flip := map[string]string{"=": "=", "<": ">", "<=": ">=", ">": "<", ">=": "<="}
	if a, cl, okc := colAt(c); okc {
		rest := c[3:]
		switch {
		case len(rest) >= 2 && rest[0].kind == 'p' && ops[rest[0].text] != "":
			if v, n, okl := lit(rest[1:]); okl && 1+n == len(rest) {
				return a, cl, rawCond{ops[rest[0].text], []ptok{v}}, true
			}
		case len(rest) == 2 && rest[0].is("IS") && rest[1].is("NULL"):
			return a, cl, rawCond{"IS NULL", nil}, true
		case len(rest) == 3 && rest[0].is("IS") && rest[1].is("NOT") && rest[2].is("NULL"):
			return a, cl, rawCond{"IS NOT NULL", nil}, true
		case len(rest) >= 4 && rest[0].is("BETWEEN"):
			if v1, n1, ok1 := lit(rest[1:]); ok1 && 1+n1 < len(rest) && rest[1+n1].is("AND") {
				if v2, n2, ok2 := lit(rest[2+n1:]); ok2 && 2+n1+n2 == len(rest) {
					return a, cl, rawCond{"BETWEEN", []ptok{v1, v2}}, true
				}
			}
		}
		return
	}
	if v, n, okl := lit(c); okl && n < len(c) && c[n].kind == 'p' && ops[c[n].text] != "" {
		if a, cl, okc := colAt(c[n+1:]); okc && n+1+3 == len(c) {
			return a, cl, rawCond{flip[ops[c[n].text]], []ptok{v}}, true
		}
	}
	return
}

func isNumericKind(k string) bool {
	return k == kInt16 || k == kInt32 || k == kInt64 || k == kDecimal || k == kFloat32 || k == kFloat64
}

func isTextKind(k string) bool { return k == kChar || k == kVarchar || k == kText }

// typedCond turns a condition into one for the source, widened so that the
// source returns at least the rows SQLite keeps: non-integer numbers and
// dates get a margin (float rounding, time zones), and only = is sent for
// text (collations order text differently).
func typedCond(rc rawCond, col, kind string, textDates bool) (pushCond, bool) {
	if rc.op == "IS NULL" || rc.op == "IS NOT NULL" {
		return pushCond{Col: col, Op: rc.op}, true
	}
	switch {
	case isNumericKind(kind):
		lo, hi := math.Inf(-1), math.Inf(1)
		vals := make([]float64, len(rc.vals))
		exact := true
		for i, v := range rc.vals {
			if v.kind != 'n' {
				return pushCond{}, false
			}
			f, err := strconv.ParseFloat(v.text, 64)
			if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
				return pushCond{}, false
			}
			vals[i] = f
			if f != math.Trunc(f) || math.Abs(f) > 1<<53 {
				exact = false
			}
		}
		arg := func(f float64) any {
			if f == math.Trunc(f) && math.Abs(f) <= 1<<53 {
				return int64(f)
			}
			return f
		}
		if exact {
			pc := pushCond{Col: col, Op: rc.op}
			for _, f := range vals {
				pc.Args = append(pc.Args, arg(f))
			}
			return pc, true
		}
		margin := func(f float64) float64 { return math.Abs(f)*1e-9 + 1e-9 }
		switch rc.op {
		case "=":
			lo, hi = vals[0]-margin(vals[0]), vals[0]+margin(vals[0])
		case ">", ">=":
			lo = vals[0] - margin(vals[0])
		case "<", "<=":
			hi = vals[0] + margin(vals[0])
		case "BETWEEN":
			lo, hi = vals[0]-margin(vals[0]), vals[1]+margin(vals[1])
		}
		return widened(col, lo, hi, func(f float64) any { return f })
	case kind == kDate || kind == kTimestamp || kind == kTimestampTZ:
		vals := make([]time.Time, len(rc.vals))
		for i, v := range rc.vals {
			if v.kind != 's' || !isoDateRe.MatchString(v.text) {
				return pushCond{}, false
			}
			t, err := parseISO(v.text)
			if err != nil {
				return pushCond{}, false
			}
			vals[i] = t
		}
		if textDates { // the source keeps dates as text like the staged copy: compared as written
			pc := pushCond{Col: col, Op: rc.op}
			for _, v := range rc.vals {
				pc.Args = append(pc.Args, v.text)
			}
			return pc, true
		}
		day := 24 * time.Hour
		var lo, hi *time.Time
		switch rc.op {
		case "=":
			a, b := vals[0].Add(-day), vals[0].Add(day)
			lo, hi = &a, &b
		case ">", ">=":
			a := vals[0].Add(-day)
			lo = &a
		case "<", "<=":
			b := vals[0].Add(day)
			hi = &b
		case "BETWEEN":
			a, b := vals[0].Add(-day), vals[1].Add(day)
			lo, hi = &a, &b
		}
		pc := pushCond{Col: col}
		switch {
		case lo != nil && hi != nil:
			pc.Op, pc.Args = "BETWEEN", []any{*lo, *hi}
		case lo != nil:
			pc.Op, pc.Args = ">=", []any{*lo}
		default:
			pc.Op, pc.Args = "<=", []any{*hi}
		}
		return pc, true
	case isTextKind(kind) && rc.op == "=" && len(rc.vals) == 1 && rc.vals[0].kind == 's':
		return pushCond{Col: col, Op: "=", Args: []any{rc.vals[0].text}}, true
	}
	return pushCond{}, false
}

func widened(col string, lo, hi float64, arg func(float64) any) (pushCond, bool) {
	switch {
	case !math.IsInf(lo, 0) && !math.IsInf(hi, 0):
		return pushCond{Col: col, Op: "BETWEEN", Args: []any{arg(lo), arg(hi)}}, true
	case !math.IsInf(lo, 0):
		return pushCond{Col: col, Op: ">=", Args: []any{arg(lo)}}, true
	case !math.IsInf(hi, 0):
		return pushCond{Col: col, Op: "<=", Args: []any{arg(hi)}}, true
	}
	return pushCond{}, false
}

func parseISO(s string) (time.Time, error) {
	s = strings.Replace(s, "T", " ", 1)
	for _, f := range []string{"2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("not a date: %s", s)
}

// ---- the read-only check of a project query -------------------------------------

// projectForbidden are words a project query can't use: on top of
// checkReadOnly, nothing that reads files, loads code or changes settings.
var projectForbidden = []string{"ATTACH", "DETACH", "PRAGMA", "VACUUM", "LOAD_EXTENSION", "READFILE", "WRITEFILE", "EDIT",
	"FTS3_TOKENIZER", "SQLITE_COMPILEOPTION_USED", "SQLITE_COMPILEOPTION_GET", "SQLAR_COMPRESS", "SQLAR_UNCOMPRESS", "ZIPFILE", "FSDIR", "LSDIR"}

// checkProjectQuery allows one SELECT (or WITH … SELECT) statement only.
func checkProjectQuery(q string) error {
	q = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(q), ";"))
	if q == "" {
		return errors.New("the query is empty")
	}
	if err := checkReadOnly(EngineSQLite, q); err != nil {
		return err
	}
	toks, err := ptokens(q)
	if err != nil {
		return err
	}
	if len(toks) == 0 || !(toks[0].is("SELECT") || toks[0].is("WITH")) {
		return errors.New("a project query is one SELECT (or WITH … SELECT): nothing else runs")
	}
	for _, t := range toks {
		if t.kind == 'p' && t.text == ";" {
			return errors.New("a project query is one statement: remove the ;")
		}
		if t.kind == 'w' && slices.ContainsFunc(projectForbidden, func(w string) bool { return strings.EqualFold(w, t.text) }) {
			return fmt.Errorf("%s can't be used in a project query (read-only)", strings.ToUpper(t.text))
		}
	}
	return nil
}
