package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Microsoft Access: the file is read directly (jet.go), so no Access, ACE
// or ODBC driver is needed and nothing can be written. Tables are browsed
// with a subset of SQL; saved queries show their SQL, rebuilt from
// MSysQueries.

func init() { backendOpeners[EngineAccess] = openAccess }

type accessBackend struct {
	j    *jetFile
	path string
}

func openAccess(ctx context.Context, p ConnParams, opts map[string]string) (backend, error) {
	if p.Database == "" {
		return nil, errors.New("database file path is required")
	}
	if _, err := os.Stat(p.Database); err != nil {
		return nil, fmt.Errorf("access file not found: %s", p.Database)
	}
	j, err := openJet(p.Database)
	if err != nil {
		return nil, err
	}
	return &accessBackend{j: j, path: p.Database}, nil
}

func (a *accessBackend) Ping(context.Context) error {
	_, err := a.j.objects()
	return err
}
func (a *accessBackend) Close() error { return a.j.Close() }

func (a *accessBackend) Version(context.Context) string {
	v := map[byte]string{0: "Jet 3 (Access 97)", 1: "Jet 4 (Access 2000-2003)", 2: "ACE 12 (Access 2007)", 3: "ACE 14 (Access 2010)",
		4: "ACE 15 (Access 2013)", 5: "ACE 16 (Access 2016 or later)"}[a.j.version]
	if v == "" {
		v = "ACE (version " + strconv.Itoa(int(a.j.version)) + ")"
	}
	return "Microsoft Access, " + v + ", " + filepath.Base(a.path)
}

func (a *accessBackend) Objects(context.Context) ([]ObjectGroup, error) {
	objs, err := a.j.objects()
	if err != nil {
		return nil, err
	}
	tables := ObjectGroup{Type: ObjTable, Label: "Tables", Queryable: true, Items: []string{}}
	queries := ObjectGroup{Type: ObjView, Label: "Queries", Items: []string{}}
	linked := ObjectGroup{Type: ObjSynonym, Label: "Linked tables", Items: []string{}}
	for _, o := range objs {
		switch {
		case o.userTable():
			tables.Items = append(tables.Items, o.name)
		case o.typ == 5 && !strings.HasPrefix(o.name, "~"):
			queries.Items = append(queries.Items, o.name)
		case (o.typ == 4 || o.typ == 6) && !strings.HasPrefix(o.name, "MSys"):
			linked.Items = append(linked.Items, o.name)
		}
	}
	return []ObjectGroup{tables, queries, linked}, nil
}

// table finds a user table by name (ignoring case).
func (a *accessBackend) table(name string) (*jetTable, error) {
	objs, err := a.j.objects()
	if err != nil {
		return nil, err
	}
	for _, o := range objs {
		if o.typ == 1 && strings.EqualFold(o.name, name) {
			return a.j.table(o.name, o.id)
		}
	}
	return nil, fmt.Errorf("table %s not found", name)
}

func accessName(s string) string { return "[" + strings.ReplaceAll(s, "]", "]]") + "]" }

func (a *accessBackend) SelectFor(_ context.Context, name string) (string, error) {
	t, err := a.table(name)
	if err != nil {
		return "", err
	}
	cols := make([]string, len(t.cols))
	for i, c := range t.cols {
		cols[i] = accessName(c.name)
	}
	return "SELECT " + strings.Join(cols, ", ") + "\nFROM " + accessName(name), nil
}

func (a *accessBackend) Count(_ context.Context, name string) (int64, error) {
	t, err := a.table(name)
	if err != nil {
		return -1, err
	}
	return t.rows, nil
}

// accessType is a column's type in Access SQL.
func accessType(c jetColumn, jet3 bool) string {
	switch c.typ {
	case jetBool:
		return "BIT"
	case jetByte:
		return "BYTE"
	case jetInt:
		return "SHORT"
	case jetLong:
		if c.autoNum {
			return "COUNTER"
		}
		return "LONG"
	case jetMoney:
		return "CURRENCY"
	case jetFloat:
		return "SINGLE"
	case jetDouble:
		return "DOUBLE"
	case jetDateTime:
		return "DATETIME"
	case jetBinary:
		return "BINARY(" + strconv.Itoa(c.size) + ")"
	case jetText:
		n := c.size
		if !jet3 {
			n /= 2
		}
		return "TEXT(" + strconv.Itoa(n) + ")"
	case jetOLE:
		return "LONGBINARY"
	case jetMemo:
		return "MEMO"
	case jetGUID:
		return "GUID"
	case jetNumeric:
		return fmt.Sprintf("DECIMAL(%d, %d)", c.prec, c.scale)
	case jetComplex:
		return "LONG -- attachment or multi-valued field"
	case jetBigInt:
		return "BIGINT"
	case jetExtDate:
		return "DATETIME2"
	}
	return fmt.Sprintf("BINARY -- type 0x%02X", c.typ)
}

func (a *accessBackend) Definition(_ context.Context, typ, name string) (string, error) {
	switch typ {
	case ObjTable:
		t, err := a.table(name)
		if err != nil {
			return "", err
		}
		lines := make([]string, len(t.cols))
		for i, c := range t.cols {
			lines[i] = "  " + accessName(c.name) + " " + accessType(c, a.j.jet3)
		}
		return fmt.Sprintf("CREATE TABLE %s (\n%s\n);\n\n-- %d row(s)\n", accessName(name), strings.Join(lines, ",\n"), t.rows), nil
	case ObjView:
		return a.querySQL(name)
	case ObjSynonym:
		return "-- " + inComment(name) + " is a linked table: its data is in another database or file.\n", nil
	}
	return "", fmt.Errorf("unsupported object type %q", typ)
}

// querySQL rebuilds a saved query's SQL from MSysQueries.
func (a *accessBackend) querySQL(name string) (string, error) {
	objs, err := a.j.objects()
	if err != nil {
		return "", err
	}
	var id int64 = -1
	var sysQueries uint32
	for _, o := range objs {
		if o.typ == 5 && o.name == name {
			id = int64(o.id)
		}
		if o.name == "MSysQueries" {
			sysQueries = o.id
		}
	}
	if id < 0 || sysQueries == 0 {
		return "", fmt.Errorf("query %s not found", name)
	}
	t, err := a.j.table("MSysQueries", sysQueries)
	if err != nil {
		return "", err
	}
	col := map[string]int{}
	for i, c := range t.cols {
		col[c.name] = i
	}
	type part struct {
		attr         int64
		expr, n1, n2 string
		flag         int64
	}
	var parts []part
	str := func(v any) string { s, _ := v.(string); return s }
	num := func(v any) int64 { n, _ := v.(int64); return n }
	err = a.j.rows(t, func(v []any) (bool, error) {
		oid := num(v[col["ObjectId"]]) & 0x00FFFFFF
		if oid == id {
			parts = append(parts, part{num(v[col["Attribute"]]), str(v[col["Expression"]]), str(v[col["Name1"]]),
				str(v[col["Name2"]]), num(v[col["Flag"]])})
		}
		return true, nil
	})
	if err != nil {
		return "", err
	}
	var kind int64
	type join struct{ kind, left, right, on string }
	var params, cols, from, where, group, having, order []string
	var joins []join
	tableText := map[string]string{} // a table's name or alias → how FROM writes it
	distinct := false
	top := ""
	for _, p := range parts {
		switch p.attr {
		case 1:
			kind = p.flag
		case 2:
			params = append(params, p.n1+" "+p.expr)
		case 3:
			distinct = p.flag&0x02 != 0
			if p.n1 != "" {
				top = "TOP " + p.n1 + " "
			}
		case 5:
			s := p.n1
			if p.expr != "" {
				s = p.expr
			}
			key := p.n1
			if strings.ContainsAny(s, " -") && !strings.HasPrefix(s, "[") {
				s = accessName(s)
			}
			if p.n2 != "" {
				s += " AS " + p.n2
				key = p.n2
			}
			tableText[key] = s
			from = append(from, key)
		case 6:
			s := p.expr
			if p.n1 != "" {
				s += " AS " + p.n1
			}
			cols = append(cols, s)
		case 7:
			j := map[int64]string{1: "INNER", 2: "LEFT", 3: "RIGHT"}[p.flag]
			joins = append(joins, join{cmpOr(j, "INNER"), p.n1, p.n2, p.expr})
		case 8:
			where = append(where, p.expr)
		case 9:
			group = append(group, p.expr)
		case 10:
			having = append(having, p.expr)
		case 11:
			if p.n1 == "D" {
				order = append(order, p.expr+" DESC")
			} else {
				order = append(order, p.expr)
			}
		}
	}
	if kind != 1 && kind != 0 {
		what := map[int64]string{2: "crosstab", 3: "delete", 4: "update", 5: "append", 6: "make-table", 7: "data-definition", 8: "pass-through", 9: "union"}[kind]
		return fmt.Sprintf("-- query %s is a %s query: its SQL can't be rebuilt here\n", inComment(name), cmpOr(what, "special")), nil
	}
	var b strings.Builder
	if len(params) > 0 {
		b.WriteString("PARAMETERS " + strings.Join(params, ", ") + ";\n")
	}
	b.WriteString("SELECT ")
	if distinct {
		b.WriteString("DISTINCT ")
	}
	b.WriteString(top)
	if len(cols) == 0 {
		cols = []string{"*"}
	}
	b.WriteString(strings.Join(cols, ", "))
	// FROM: the joined tables, nested as Access writes them, then the others.
	text := func(k string) string { return cmpOr(tableText[k], k) }
	used := map[string]bool{}
	var fromParts []string
	clause := ""
	for _, j := range joins {
		switch {
		case clause == "":
			clause = text(j.left) + " " + j.kind + " JOIN " + text(j.right) + " ON " + j.on
		case used[j.left] && !used[j.right]:
			clause = "(" + clause + ") " + j.kind + " JOIN " + text(j.right) + " ON " + j.on
		case used[j.right] && !used[j.left]:
			clause = "(" + clause + ") " + j.kind + " JOIN " + text(j.left) + " ON " + j.on
		default:
			clause = "(" + clause + ") " + j.kind + " JOIN " + text(j.right) + " ON " + j.on
		}
		used[j.left], used[j.right] = true, true
	}
	if clause != "" {
		fromParts = append(fromParts, clause)
	}
	for _, f := range from {
		if !used[f] {
			fromParts = append(fromParts, text(f))
			used[f] = true
		}
	}
	if len(fromParts) > 0 {
		b.WriteString("\nFROM " + strings.Join(fromParts, ", "))
	}
	if len(where) > 0 {
		b.WriteString("\nWHERE " + strings.Join(where, " AND "))
	}
	if len(group) > 0 {
		b.WriteString("\nGROUP BY " + strings.Join(group, ", "))
	}
	if len(having) > 0 {
		b.WriteString("\nHAVING " + strings.Join(having, " AND "))
	}
	if len(order) > 0 {
		b.WriteString("\nORDER BY " + strings.Join(order, ", "))
	}
	return b.String() + ";\n", nil
}

func (a *accessBackend) CheckReadOnly(q string) error {
	if firstWord(q) != "SELECT" {
		return errors.New("cactusdb reads Access files: write SELECT * | columns FROM table [WHERE …] [ORDER BY …]")
	}
	return nil
}

// ---- a small SELECT --------------------------------------------------------------------

// accessQuery is SELECT [TOP n] cols FROM table [WHERE c op v [AND …]] [ORDER BY c [DESC], …].
type accessQuery struct {
	top   int
	cols  []string // nil: every column
	table string
	where []accessCond
	order []accessOrder
}

type accessCond struct {
	col, op string
	val     any // nil with IS [NOT] NULL
}

type accessOrder struct {
	col  string
	desc bool
}

var accessToken = regexp.MustCompile(`(?s)\s*(\[(?:[^\]]|\]\])*\]|` + "`[^`]*`" + `|"(?:[^"]|"")*"|'(?:[^']|'')*'|#[^#]*#|<>|<=|>=|!=|[=<>,*();]|[^\s=<>,*();\[\]'"#]+)`)

func parseAccessQuery(q string) (*accessQuery, error) {
	var toks []string
	rest := strings.TrimSpace(stripLineComments(q))
	for rest != "" {
		m := accessToken.FindStringSubmatchIndex(rest)
		if m == nil || m[0] != 0 {
			return nil, fmt.Errorf("can't read the query at %q", rest)
		}
		toks = append(toks, rest[m[2]:m[3]])
		rest = strings.TrimSpace(rest[m[1]:])
	}
	for len(toks) > 0 && toks[len(toks)-1] == ";" {
		toks = toks[:len(toks)-1]
	}
	i := 0
	peek := func() string {
		if i < len(toks) {
			return toks[i]
		}
		return ""
	}
	kw := func(w string) bool {
		if strings.EqualFold(peek(), w) {
			i++
			return true
		}
		return false
	}
	name := func() (string, error) {
		t := peek()
		if t == "" || strings.ContainsAny(t[:1], "'#=<>,()*") {
			return "", fmt.Errorf("expected a name, found %q", t)
		}
		i++
		return unquoteAccess(t), nil
	}
	aq := &accessQuery{}
	if !kw("SELECT") {
		return nil, errors.New("the query must start with SELECT")
	}
	if kw("TOP") {
		n, err := strconv.Atoi(peek())
		if err != nil {
			return nil, errors.New("TOP needs a number")
		}
		aq.top = n
		i++
	}
	if kw("*") {
	} else {
		for {
			c, err := name()
			if err != nil {
				return nil, err
			}
			aq.cols = append(aq.cols, c)
			if !kw(",") {
				break
			}
		}
	}
	if !kw("FROM") {
		return nil, fmt.Errorf("expected FROM, found %q (only SELECT columns FROM one table)", peek())
	}
	t, err := name()
	if err != nil {
		return nil, err
	}
	aq.table = t
	if kw("WHERE") {
		for {
			c, err := name()
			if err != nil {
				return nil, err
			}
			cond := accessCond{col: c}
			switch {
			case kw("IS"):
				cond.op = "IS NULL"
				if kw("NOT") {
					cond.op = "IS NOT NULL"
				}
				if !kw("NULL") {
					return nil, errors.New("expected NULL after IS")
				}
			default:
				op := strings.ToUpper(peek())
				switch op {
				case "=", "<>", "!=", "<", "<=", ">", ">=", "LIKE":
				default:
					return nil, fmt.Errorf("expected a comparison after %s, found %q", c, peek())
				}
				i++
				cond.op = op
				v, err := accessLiteral(peek())
				if err != nil {
					return nil, err
				}
				i++
				cond.val = v
			}
			aq.where = append(aq.where, cond)
			if !kw("AND") {
				break
			}
		}
	}
	if kw("ORDER") {
		if !kw("BY") {
			return nil, errors.New("expected BY after ORDER")
		}
		for {
			c, err := name()
			if err != nil {
				return nil, err
			}
			o := accessOrder{col: c}
			if kw("DESC") {
				o.desc = true
			} else {
				kw("ASC")
			}
			aq.order = append(aq.order, o)
			if !kw(",") {
				break
			}
		}
	}
	if i < len(toks) {
		return nil, fmt.Errorf("can't read the query from %q: Access files take SELECT columns FROM table [WHERE column = value AND …] [ORDER BY column]", toks[i])
	}
	return aq, nil
}

func unquoteAccess(t string) string {
	if len(t) < 2 { // a lone quote: no name inside
		return t
	}
	switch {
	case strings.HasPrefix(t, "["):
		return strings.ReplaceAll(t[1:len(t)-1], "]]", "]")
	case strings.HasPrefix(t, "`"), strings.HasPrefix(t, `"`):
		return t[1 : len(t)-1]
	}
	return t
}

func accessLiteral(t string) (any, error) {
	switch {
	case t == "":
		return nil, errors.New("a value is missing")
	case len(t) < 2 && (t == "'" || t == `"`):
		return nil, errors.New("a quote isn't closed")
	case strings.HasPrefix(t, "'"):
		return strings.ReplaceAll(t[1:len(t)-1], "''", "'"), nil
	case strings.HasPrefix(t, `"`):
		return strings.ReplaceAll(t[1:len(t)-1], `""`, `"`), nil
	case strings.HasPrefix(t, "#"):
		return parseShellDate(strings.Trim(t, "#"))
	case strings.EqualFold(t, "true"), strings.EqualFold(t, "yes"):
		return true, nil
	case strings.EqualFold(t, "false"), strings.EqualFold(t, "no"):
		return false, nil
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil {
		return nil, fmt.Errorf("%s is not a value: put text in 'quotes'", t)
	}
	return f, nil
}

// accessCompare compares a cell with a literal: -2 when they can't be compared.
func accessCompare(v, lit any) int {
	num := func(x any) (float64, bool) {
		switch t := x.(type) {
		case int64:
			return float64(t), true
		case float64:
			return t, true
		case json.Number:
			f, err := t.Float64()
			return f, err == nil
		case bool:
			if t {
				return -1, true // Access's True
			}
			return 0, true
		}
		return 0, false
	}
	if a, ok := num(v); ok {
		if b, ok := num(lit); ok {
			switch {
			case a < b:
				return -1
			case a > b:
				return 1
			}
			return 0
		}
	}
	if a, ok := v.(time.Time); ok {
		if b, ok := lit.(time.Time); ok {
			return a.Compare(b)
		}
	}
	return strings.Compare(strings.ToLower(fmt.Sprint(v)), strings.ToLower(fmt.Sprint(lit)))
}

// likeRegexp turns an Access LIKE pattern (* ? # and %, _) into a regexp.
func likeRegexp(p string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("(?is)^")
	for _, r := range p {
		switch r {
		case '*', '%':
			b.WriteString(".*")
		case '?', '_':
			b.WriteString(".")
		case '#':
			b.WriteString("[0-9]")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

func (a *accessBackend) Query(ctx context.Context, q string) (resultSet, error) {
	aq, err := parseAccessQuery(q)
	if err != nil {
		return nil, err
	}
	t, err := a.table(aq.table)
	if err != nil {
		return nil, err
	}
	colIdx := func(n string) (int, error) {
		for i, c := range t.cols {
			if strings.EqualFold(c.name, n) {
				return i, nil
			}
		}
		return -1, fmt.Errorf("%s has no column %s", t.name, n)
	}
	var pick []int
	var names []string
	if aq.cols == nil {
		for i, c := range t.cols {
			pick = append(pick, i)
			names = append(names, c.name)
		}
	} else {
		for _, n := range aq.cols {
			i, err := colIdx(n)
			if err != nil {
				return nil, err
			}
			pick = append(pick, i)
			names = append(names, t.cols[i].name)
		}
	}
	type cond struct {
		i    int
		c    accessCond
		like *regexp.Regexp
	}
	var conds []cond
	for _, c := range aq.where {
		i, err := colIdx(c.col)
		if err != nil {
			return nil, err
		}
		cd := cond{i: i, c: c}
		if c.op == "LIKE" {
			cd.like = likeRegexp(fmt.Sprint(c.val))
		}
		conds = append(conds, cd)
	}
	match := func(v []any) bool {
		for _, cd := range conds {
			x := v[cd.i]
			switch cd.c.op {
			case "IS NULL":
				if x != nil {
					return false
				}
				continue
			case "IS NOT NULL":
				if x == nil {
					return false
				}
				continue
			}
			if x == nil {
				return false
			}
			if cd.like != nil {
				if !cd.like.MatchString(fmt.Sprint(x)) {
					return false
				}
				continue
			}
			r := accessCompare(x, cd.c.val)
			ok := map[string]bool{"=": r == 0, "<>": r != 0, "!=": r != 0, "<": r < 0, "<=": r <= 0, ">": r > 0, ">=": r >= 0}[cd.c.op]
			if !ok {
				return false
			}
		}
		return true
	}
	types := make([]string, len(pick))
	for k, i := range pick {
		switch t.cols[i].typ {
		case jetMoney, jetNumeric:
			types[k] = "DECIMAL"
		case jetDateTime:
			types[k] = "DATETIME"
		case jetGUID:
			types[k] = "GUID"
		}
	}
	project := func(v []any) []any {
		out := make([]any, len(pick))
		for k, i := range pick {
			out[k] = v[i]
		}
		return out
	}
	if len(aq.order) > 0 {
		// Sorted: read every matching row first.
		var rows [][]any
		err := a.j.rows(t, func(v []any) (bool, error) {
			if match(v) {
				rows = append(rows, v)
			}
			return ctx.Err() == nil, ctx.Err()
		})
		if err != nil {
			return nil, err
		}
		var keys []int
		for _, o := range aq.order {
			i, err := colIdx(o.col)
			if err != nil {
				return nil, err
			}
			keys = append(keys, i)
		}
		sort.SliceStable(rows, func(x, y int) bool {
			for k, i := range keys {
				a, b := rows[x][i], rows[y][i]
				var r int
				switch {
				case a == nil && b == nil:
					continue
				case a == nil:
					r = -1
				case b == nil:
					r = 1
				default:
					r = accessCompare(a, b)
				}
				if r == 0 {
					continue
				}
				if aq.order[k].desc {
					return r > 0
				}
				return r < 0
			}
			return false
		})
		if aq.top > 0 && len(rows) > aq.top {
			rows = rows[:aq.top]
		}
		out := make([][]any, len(rows))
		for k, r := range rows {
			out[k] = project(r)
		}
		rs := newSliceRows(names, out)
		rs.types = types
		return rs, nil
	}
	// Unsorted: rows are read as they are asked for.
	ch := make(chan []any, 256)
	errc := make(chan error, 1)
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		defer close(ch)
		n := 0
		err := a.j.rows(t, func(v []any) (bool, error) {
			if !match(v) {
				return true, nil
			}
			select {
			case ch <- project(v):
			case <-ctx.Done():
				return false, nil
			}
			n++
			return aq.top == 0 || n < aq.top, nil
		})
		if err != nil {
			errc <- err
		}
	}()
	return &typedChanRows{chanRows: chanRows{cols: names, ch: ch, errc: errc, cancel: cancel}, types: types}, nil
}

type typedChanRows struct {
	chanRows
	types []string
}

func (t *typedChanRows) Types() []string { return t.types }
