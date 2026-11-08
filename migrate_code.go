package main

// ☰ → Migrate: views, procedures, functions and triggers in the target's
// SQL. A view's query is rewritten token by token (quoting, names, the
// functions and clauses that differ). Firebird's PSQL is parsed into
// statements and written as MySQL/MariaDB or PostgreSQL (PL/pgSQL) code;
// between databases of the same kind the definition is copied. What can't
// be translated is listed with its definition, to finish by hand.

import (
	"fmt"
	"strings"
	"unicode"
)

type tkind int

const (
	tWord    tkind = iota
	tIdent         // a quoted identifier: s is the name, unquoted
	tStr           // a string literal, as single-quoted SQL
	tNum           // a number
	tOp            // punctuation and operators
	tSpace         // white space
	tComment       // -- or /* */
	tParam         // :name (PSQL) or @name (T-SQL): s is the name
	tDollar        // a PostgreSQL $tag$…$tag$ string
)

type tok struct {
	k tkind
	s string
}

func (t tok) is(w string) bool { return t.k == tWord && strings.EqualFold(t.s, w) }
func (t tok) op(o string) bool { return t.k == tOp && t.s == o }

// lexSQL splits SQL into tokens, by the quoting rules of engine.
func lexSQL(src, engine string) []tok {
	var out []tok
	r := []rune(src)
	n := len(r)
	isWord := func(c rune) bool { return c == '_' || c == '$' || unicode.IsLetter(c) || unicode.IsDigit(c) }
	for i := 0; i < n; {
		c := r[i]
		start := i
		switch {
		case unicode.IsSpace(c):
			for i < n && unicode.IsSpace(r[i]) {
				i++
			}
			out = append(out, tok{tSpace, string(r[start:i])})
		case c == '-' && i+1 < n && r[i+1] == '-', c == '#' && mysqlFamily(engine):
			for i < n && r[i] != '\n' {
				i++
			}
			out = append(out, tok{tComment, string(r[start:i])})
		case c == '/' && i+1 < n && r[i+1] == '*':
			i += 2
			for i < n && !(r[i] == '*' && i+1 < n && r[i+1] == '/') {
				i++
			}
			i = min(i+2, n)
			out = append(out, tok{tComment, string(r[start:i])})
		case c == '\'' || c == '"' && mysqlFamily(engine):
			q := c
			var b strings.Builder
			i++
			for i < n {
				if r[i] == '\\' && mysqlFamily(engine) && i+1 < n {
					b.WriteRune(r[i+1])
					i += 2
					continue
				}
				if r[i] == q {
					if i+1 < n && r[i+1] == q {
						b.WriteRune(q)
						i += 2
						continue
					}
					i++
					break
				}
				b.WriteRune(r[i])
				i++
			}
			out = append(out, tok{tStr, "'" + strings.ReplaceAll(b.String(), "'", "''") + "'"})
		case c == '"' || c == '`' || c == '[' && engine == EngineMSSQL:
			q := c
			if c == '[' {
				q = ']'
			}
			var b strings.Builder
			i++
			for i < n {
				if r[i] == q {
					if i+1 < n && r[i+1] == q {
						b.WriteRune(q)
						i += 2
						continue
					}
					i++
					break
				}
				b.WriteRune(r[i])
				i++
			}
			out = append(out, tok{tIdent, b.String()})
		case c == '$' && engine == EnginePostgres && i+1 < n && (r[i+1] == '$' || unicode.IsLetter(r[i+1])):
			j := i + 1
			for j < n && r[j] != '$' && isWord(r[j]) {
				j++
			}
			if j < n && r[j] == '$' {
				tag := string(r[i : j+1])
				if end := strings.Index(string(r[j+1:]), tag); end >= 0 {
					i = j + 1 + len([]rune(string(r[j+1:])[:end])) + len([]rune(tag))
					out = append(out, tok{tDollar, string(r[start:i])})
					continue
				}
			}
			i++
			out = append(out, tok{tOp, "$"})
		case unicode.IsDigit(c) || c == '.' && i+1 < n && unicode.IsDigit(r[i+1]):
			for i < n && (unicode.IsDigit(r[i]) || r[i] == '.') {
				i++
			}
			if i < n && (r[i] == 'e' || r[i] == 'E') && i+1 < n && (unicode.IsDigit(r[i+1]) || (r[i+1] == '-' || r[i+1] == '+') && i+2 < n && unicode.IsDigit(r[i+2])) {
				i += 2
				for i < n && unicode.IsDigit(r[i]) {
					i++
				}
			}
			out = append(out, tok{tNum, string(r[start:i])})
		case c == ':' && engine == EngineFirebird && i+1 < n && (unicode.IsLetter(r[i+1]) || r[i+1] == '_' || r[i+1] == '"'):
			i++
			if r[i] == '"' {
				j := i + 1
				for j < n && r[j] != '"' {
					j++
				}
				out = append(out, tok{tParam, string(r[i+1 : j])})
				i = min(j+1, n)
				continue
			}
			for i < n && isWord(r[i]) {
				i++
			}
			out = append(out, tok{tParam, string(r[start+1 : i])})
		case c == '@' && engine == EngineMSSQL && i+1 < n && (unicode.IsLetter(r[i+1]) || r[i+1] == '_'):
			i++
			for i < n && isWord(r[i]) {
				i++
			}
			out = append(out, tok{tParam, string(r[start+1 : i])})
		case isWord(c) || c == '#' && engine == EngineMSSQL || c == '@':
			i++
			for i < n && (isWord(r[i]) || r[i] == '@' || r[i] == '#') {
				i++
			}
			out = append(out, tok{tWord, string(r[start:i])})
		default:
			two := ""
			if i+1 < n {
				two = string(r[i : i+2])
			}
			switch two {
			case "||", "::", "<=", ">=", "<>", "!=", ":=", "->", "=>":
				i += 2
				if two == "->" && i < n && r[i] == '>' {
					i++
				}
				out = append(out, tok{tOp, string(r[start:i])})
			default:
				i++
				out = append(out, tok{tOp, string(c)})
			}
		}
	}
	return out
}

// ---- helpers on token lists ------------------------------------------------------

// skipSp returns the index of the first token from i that isn't space or a comment.
func skipSp(t []tok, i int) int {
	for i < len(t) && (t[i].k == tSpace || t[i].k == tComment) {
		i++
	}
	return i
}

// prevSp returns the index of the last token before i that isn't space or a comment (-1: none).
func prevSp(t []tok, i int) int {
	for i--; i >= 0 && (t[i].k == tSpace || t[i].k == tComment); i-- {
	}
	return i
}

// closeParen returns the index of the ) that closes the ( at i.
func closeParen(t []tok, i int) int {
	depth := 0
	for j := i; j < len(t); j++ {
		if t[j].op("(") {
			depth++
		} else if t[j].op(")") {
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return len(t) - 1
}

// splitArgs splits the tokens between parentheses at the top-level commas.
func splitArgs(t []tok) [][]tok {
	var out [][]tok
	depth, from := 0, 0
	for i, x := range t {
		switch {
		case x.op("("):
			depth++
		case x.op(")"):
			depth--
		case x.op(",") && depth == 0:
			out = append(out, t[from:i])
			from = i + 1
		}
	}
	return append(out, t[from:])
}

func trimToks(t []tok) []tok {
	for len(t) > 0 && (t[0].k == tSpace || t[0].k == tComment) {
		t = t[1:]
	}
	for len(t) > 0 && (t[len(t)-1].k == tSpace || t[len(t)-1].k == tComment) {
		t = t[:len(t)-1]
	}
	return t
}

func joinRaw(t []tok, engine string) string {
	var b strings.Builder
	for _, x := range t {
		switch x.k {
		case tIdent:
			b.WriteString(quoteIdent(engine, x.s))
		case tParam:
			b.WriteString(":" + x.s)
		default:
			b.WriteString(x.s)
		}
	}
	return b.String()
}

// findWord returns the index of the first top-level word w from i (-1: none).
func findWord(t []tok, i int, w string) int {
	depth := 0
	for ; i < len(t); i++ {
		switch {
		case t[i].op("("):
			depth++
		case t[i].op(")"):
			depth--
		case depth == 0 && t[i].is(w):
			return i
		}
	}
	return -1
}

// ---- the translator ---------------------------------------------------------------

// xlate rewrites SQL of the source engine for the target.
type xlate struct {
	src, dst string
	names    map[string]string // upper-case source name (tables, views, columns, procedures, sequences…) → target name
	columns  map[string]bool   // upper-case column names: a variable named like one is renamed or qualified
	bools    map[string]bool   // upper-case names of BOOLEAN columns (SQL Server: WHERE active is WHERE active = 1)
	seqs     map[string]bool   // upper-case names of the source's sequences
	procs    map[string]bool   // upper-case names of selectable procedures (they return rows)
	nameCase string            // "lower", "upper" or ""
	seqOK    bool              // the target has sequences (MariaDB 10.3+, PostgreSQL, SQL Server, Firebird)
	dropQual map[string]bool   // qualifiers to drop: the source database's name, dbo, public

	// While translating code:
	vars    map[string]string // upper-case variable → its name in the target
	sqlVars map[string]string // the same inside an SQL statement (PostgreSQL: qualified when named like a column)
	consts  map[string]string // INSERTING, UPDATING, DELETING in a trigger
	notes   []string
	todo    bool // something couldn't be translated: the result needs checking
}

// sqlKeywords are left as they are when names change case.
var sqlKeywords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`SELECT FROM WHERE AND OR NOT NULL IS IN AS ON JOIN LEFT RIGHT INNER OUTER FULL CROSS GROUP BY ORDER HAVING
		UNION ALL DISTINCT CASE WHEN THEN ELSE END EXISTS BETWEEN LIKE ASC DESC INSERT INTO VALUES UPDATE SET DELETE CAST COALESCE
		COUNT SUM MIN MAX AVG UPPER LOWER TRIM SUBSTRING POSITION EXTRACT CURRENT_DATE CURRENT_TIME CURRENT_TIMESTAMP TRUE FALSE
		LIMIT OFFSET FIRST SKIP ROWS TOP WITH RECURSIVE NEW OLD IF BEGIN DECLARE RETURN RETURNS FOR DO WHILE LOOP LEAVE EXIT
		ROUND ABS MOD CHAR VARCHAR INTEGER INT BIGINT SMALLINT NUMERIC DECIMAL DATE TIME TIMESTAMP BOOLEAN DOUBLE PRECISION FLOAT
		REAL BLOB TEXT SUB_TYPE CHARACTER VARYING NULLIF IIF ANY SOME USING NATURAL MATCHED MERGE RETURNING CONCAT LENGTH
		CHAR_LENGTH CHARACTER_LENGTH REPLACE DATEADD DATEDIFF YEAR MONTH DAY HOUR MINUTE SECOND LEADING TRAILING BOTH
		ESCAPE COLLATE OVER PARTITION ROW_NUMBER RANK DENSE_RANK LAG LEAD LIST GEN_ID NEXT VALUE CURRENT_USER USER INTERVAL
		FETCH ONLY DUAL`) {
		sqlKeywords[w] = true
	}
}

// sqlFuncs are function names: a word followed by ( that is one isn't an object name.
var sqlFuncs = map[string]bool{"COUNT": true, "SUM": true, "MIN": true, "MAX": true, "AVG": true, "UPPER": true, "LOWER": true,
	"TRIM": true, "SUBSTRING": true, "COALESCE": true, "CAST": true, "ROUND": true, "ABS": true, "MOD": true, "NULLIF": true,
	"EXTRACT": true, "POSITION": true, "REPLACE": true, "CONCAT": true, "LENGTH": true, "CHAR_LENGTH": true, "LEFT": true, "RIGHT": true}

func (x *xlate) applyCase(s string) string {
	switch x.nameCase {
	case "lower":
		return strings.ToLower(s)
	case "upper":
		return strings.ToUpper(s)
	}
	return s
}

func (x *xlate) q(name string) string { return quoteIdent(x.dst, name) }

func (x *xlate) note(format string, args ...any) {
	n := fmt.Sprintf(format, args...)
	for _, o := range x.notes {
		if o == n {
			return
		}
	}
	x.notes = append(x.notes, n)
}

// nameOf maps an identifier of the source (quoted or not) to the target's.
func (x *xlate) nameOf(s string, quoted bool) (string, bool) {
	if n, ok := x.names[s]; ok {
		return n, true
	}
	if !quoted || x.src == EngineMySQL || x.src == EngineMariaDB || x.src == EngineMSSQL || x.src == EngineSQLite {
		if n, ok := x.names[strings.ToUpper(s)]; ok {
			return n, true
		}
	}
	return "", false
}

// seqRef renders the next value of a sequence.
func (x *xlate) seqNext(name string) string {
	n, ok := x.nameOf(name, false)
	if !ok {
		n = x.applyCase(name)
	}
	switch x.dst {
	case EnginePostgres:
		return "nextval('" + strings.ReplaceAll(x.q(n), "'", "''") + "')"
	case EngineMySQL, EngineMariaDB:
		if !x.seqOK {
			x.note("MySQL has no sequences: %s is used here (MariaDB 10.3+ has them)", name)
			x.todo = true
		}
		return "NEXTVAL(" + x.q(n) + ")"
	case EngineSQLite:
		x.note("SQLite has no sequences: %s is used here", name)
		x.todo = true
	}
	return "NEXT VALUE FOR " + x.q(n)
}

// render writes tokens in the target's SQL. inSQL: inside an SQL statement
// (where a bare name is a column), else a PSQL expression (a variable).
func (x *xlate) render(t []tok, inSQL bool) string {
	t = x.limitPass(t)
	if x.src == EnginePostgres && x.dst != EnginePostgres {
		t = anyArrayPass(castPass(t))
	}
	if (x.dst == EngineFirebird || x.dst == EngineMSSQL) && (mysqlFamily(x.src) || x.src == EnginePostgres || x.src == EngineSQLite) {
		t = limitToFetch(t, x.dst == EngineMSSQL)
	}
	var b strings.Builder
	for i := 0; i < len(t); i++ {
		c := t[i]
		switch c.k {
		case tComment:
			continue
		case tSpace:
			if strings.Contains(c.s, "\n") {
				b.WriteString("\n" + strings.TrimLeft(c.s[strings.LastIndex(c.s, "\n")+1:], "\n"))
			} else {
				b.WriteString(" ")
			}
			continue
		case tStr:
			if x.dst == EngineMSSQL {
				b.WriteString("N")
			}
			if mysqlFamily(x.dst) && strings.Contains(c.s, `\`) {
				b.WriteString(strings.ReplaceAll(c.s, `\`, `\\`))
			} else {
				b.WriteString(c.s)
			}
			continue
		case tNum, tDollar:
			b.WriteString(c.s)
			continue
		case tParam:
			b.WriteString(x.varRef(c.s, true))
			continue
		case tOp:
			switch {
			case c.s == "||" && x.dst == EngineMSSQL:
				b.WriteString("+")
			case c.s == "!=" && x.dst == EngineFirebird:
				b.WriteString("<>")
			default:
				b.WriteString(c.s)
			}
			continue
		}
		// A word or a quoted identifier.
		W := strings.ToUpper(c.s)
		j := skipSp(t, i+1)
		nextOpen := j < len(t) && t[j].op("(")
		nextDot := j < len(t) && t[j].op(".")
		prev := prevSp(t, i)
		afterDot := prev >= 0 && t[prev].op(".")

		// schema.table and db.table: a qualifier of the source.
		if nextDot && !afterDot && c.k != tStr {
			k := skipSp(t, j+1)
			if k < len(t) && (t[k].k == tWord || t[k].k == tIdent) {
				if n, ok := x.nameOf(c.s+"."+t[k].s, c.k == tIdent); ok {
					b.WriteString(x.q(n))
					i = k
					continue
				}
				if x.dropQual[strings.ToUpper(c.s)] {
					i = j // drop the qualifier and its dot
					continue
				}
			}
		}
		if c.k == tIdent {
			if afterDot && prev >= 1 && (t[prevSp(t, prev)].is("NEW") || t[prevSp(t, prev)].is("OLD")) {
				if n, ok := x.nameOf(c.s, true); ok {
					b.WriteString(x.q(n))
					continue
				}
			}
			if n, ok := x.nameOf(c.s, true); ok {
				b.WriteString(x.q(n))
			} else if v, ok := x.vars[strings.ToUpper(c.s)]; ok && !afterDot {
				b.WriteString(x.pickVar(c.s, v, inSQL))
			} else {
				b.WriteString(x.q(x.applyCase(c.s)))
			}
			continue
		}
		if !afterDot {
			if v, ok := x.consts[W]; ok && !nextDot {
				b.WriteString(v)
				continue
			}
			if done, skip := x.rewriteCall(&b, t, i, W, inSQL); done {
				i = skip
				continue
			}
			if W == "FROM" {
				if k := skipSp(t, i+1); k < len(t) && t[k].is("RDB$DATABASE") {
					switch {
					case mysqlFamily(x.dst):
						b.WriteString("FROM DUAL")
					case x.dst == EngineFirebird:
						b.WriteString("FROM RDB$DATABASE")
					}
					i = k
					continue
				}
			}
			if W == "IS" && x.dst == EngineMSSQL {
				if k := skipSp(t, i+1); k < len(t) && (t[k].is("TRUE") || t[k].is("FALSE")) {
					b.WriteString(map[bool]string{true: "= 1", false: "= 0"}[t[k].is("TRUE")])
					i = k
					continue
				}
			}
			if (W == "TRUE" || W == "FALSE") && x.dst == EngineMSSQL {
				b.WriteString(map[string]string{"TRUE": "1", "FALSE": "0"}[W])
				continue
			}
			if W == "N" && j == i+1 && j < len(t) && t[j].k == tStr { // N'…'
				continue
			}
			if W == "NEXT" {
				if k := skipSp(t, i+1); k < len(t) && t[k].is("VALUE") {
					if f := skipSp(t, k+1); f < len(t) && t[f].is("FOR") {
						if s := skipSp(t, f+1); s < len(t) && (t[s].k == tWord || t[s].k == tIdent) {
							b.WriteString(x.seqNext(t[s].s))
							i = s
							continue
						}
					}
				}
			}
		}
		if W == "NEW" || W == "OLD" {
			if nextDot {
				b.WriteString(W)
				continue
			}
		}
		if afterDot {
			if n, ok := x.nameOf(c.s, false); ok {
				b.WriteString(x.q(n))
			} else {
				b.WriteString(x.applyCase(c.s))
			}
			continue
		}
		if nextOpen && sqlFuncs[W] {
			b.WriteString(c.s)
			continue
		}
		if v, ok := x.vars[W]; ok && (!inSQL || !x.columns[W]) {
			b.WriteString(x.pickVar(c.s, v, inSQL))
			continue
		}
		if n, ok := x.nameOf(c.s, false); ok {
			b.WriteString(x.q(n))
			if x.dst == EngineMSSQL && x.bools[W] && boolPredicate(t, i) {
				b.WriteString(" = 1")
			}
			continue
		}
		if sqlKeywords[W] {
			b.WriteString(c.s)
		} else {
			b.WriteString(x.applyCase(c.s))
		}
	}
	return strings.TrimSpace(b.String())
}

// boolPredicate reports whether the name at i stands alone as a condition
// (WHERE active AND …), which SQL Server's BIT can't.
func boolPredicate(t []tok, i int) bool {
	p := prevSp(t, i)
	if p >= 0 && t[p].op(".") { // c.active
		p = prevSp(t, prevSp(t, p))
	}
	n := skipSp(t, i+1)
	before := p < 0 || t[p].op("(") || t[p].k == tWord && map[string]bool{"WHERE": true, "AND": true, "OR": true, "NOT": true, "ON": true,
		"WHEN": true, "HAVING": true}[strings.ToUpper(t[p].s)]
	after := n >= len(t) || t[n].op(")") || t[n].op(";") || t[n].k == tWord && map[string]bool{"AND": true, "OR": true, "THEN": true,
		"ORDER": true, "GROUP": true, "UNION": true, "HAVING": true, "LIMIT": true}[strings.ToUpper(t[n].s)]
	return before && after
}

func (x *xlate) varRef(name string, inSQL bool) string {
	if v, ok := x.vars[strings.ToUpper(name)]; ok {
		return x.pickVar(name, v, inSQL)
	}
	x.note("the variable %s is unknown", name)
	return x.applyCase(name)
}

func (x *xlate) pickVar(name, v string, inSQL bool) string {
	if inSQL {
		if s, ok := x.sqlVars[strings.ToUpper(name)]; ok {
			return s
		}
	}
	return v
}

// rewriteCall rewrites a function or clause that differs between engines.
// It returns whether it wrote something and the last token it used.
func (x *xlate) rewriteCall(b *strings.Builder, t []tok, i int, W string, inSQL bool) (bool, int) {
	j := skipSp(t, i+1)
	if j >= len(t) || !t[j].op("(") {
		switch W {
		case "GETDATE", "SYSDATETIME":
			b.WriteString("CURRENT_TIMESTAMP")
			return true, i
		}
		return false, i
	}
	end := closeParen(t, j)
	args := splitArgs(t[j+1 : end])
	r := func(k int) string { return x.render(args[k], inSQL) }
	switch W {
	case "IIF":
		if len(args) != 3 || x.dst == EngineMSSQL || x.dst == EngineFirebird {
			return false, i
		}
		if mysqlFamily(x.dst) {
			fmt.Fprintf(b, "IF(%s, %s, %s)", r(0), r(1), r(2))
		} else {
			fmt.Fprintf(b, "CASE WHEN %s THEN %s ELSE %s END", r(0), r(1), r(2))
		}
		return true, end
	case "IF":
		if !mysqlFamily(x.src) || mysqlFamily(x.dst) || len(args) != 3 {
			return false, i
		}
		if x.dst == EngineMSSQL {
			fmt.Fprintf(b, "IIF(%s, %s, %s)", r(0), r(1), r(2))
		} else {
			fmt.Fprintf(b, "CASE WHEN %s THEN %s ELSE %s END", r(0), r(1), r(2))
		}
		return true, end
	case "IFNULL", "NVL", "ISNULL":
		if len(args) != 2 || W == "ISNULL" && x.src != EngineMSSQL || x.src == x.dst {
			return false, i
		}
		fmt.Fprintf(b, "COALESCE(%s, %s)", r(0), r(1))
		return true, end
	case "GEN_ID":
		if len(args) != 2 || x.dst == EngineFirebird {
			return false, i
		}
		name := strings.TrimSpace(joinRaw(trimToks(args[0]), EngineFirebird))
		if tt := trimToks(args[0]); len(tt) == 1 {
			name = tt[0].s
		}
		if strings.TrimSpace(r(1)) == "1" {
			b.WriteString(x.seqNext(name))
		} else {
			n, _ := x.nameOf(name, false)
			switch x.dst {
			case EnginePostgres:
				fmt.Fprintf(b, "(SELECT last_value FROM %s)", x.q(n))
			case EngineMySQL, EngineMariaDB:
				fmt.Fprintf(b, "(SELECT next_not_cached_value - 1 FROM %s)", x.q(n))
			default:
				b.WriteString(x.seqNext(name))
				x.note("GEN_ID(%s, %s) is translated as the next value", name, r(1))
				x.todo = true
			}
		}
		return true, end
	case "LIST":
		if x.src != EngineFirebird || x.dst == EngineFirebird {
			return false, i
		}
		sep := "','"
		if len(args) > 1 {
			sep = r(1)
		}
		switch x.dst {
		case EngineMySQL, EngineMariaDB:
			fmt.Fprintf(b, "GROUP_CONCAT(%s SEPARATOR %s)", r(0), sep)
		case EnginePostgres:
			fmt.Fprintf(b, "string_agg(CAST(%s AS TEXT), %s)", r(0), sep)
		case EngineMSSQL:
			fmt.Fprintf(b, "STRING_AGG(%s, %s)", r(0), sep)
		default:
			fmt.Fprintf(b, "group_concat(%s, %s)", r(0), sep)
		}
		return true, end
	case "LEN":
		if x.src != EngineMSSQL || x.dst == EngineMSSQL {
			return false, i
		}
		fmt.Fprintf(b, "CHAR_LENGTH(%s)", x.render(t[j+1:end], inSQL))
		return true, end
	case "CHAR_LENGTH", "CHARACTER_LENGTH":
		switch x.dst {
		case EngineMSSQL:
			fmt.Fprintf(b, "LEN(%s)", x.render(t[j+1:end], inSQL))
		case EngineSQLite:
			fmt.Fprintf(b, "length(%s)", x.render(t[j+1:end], inSQL))
		default:
			return false, i
		}
		return true, end
	case "CONCAT":
		if x.dst != EngineFirebird && x.dst != EngineSQLite || len(args) < 2 {
			return false, i
		}
		parts := make([]string, len(args)) // MySQL's CONCAT is NULL when one part is, like ||
		for k := range args {
			parts[k] = r(k)
		}
		b.WriteString("(" + strings.Join(parts, " || ") + ")")
		return true, end
	case "NOW":
		if x.dst == EnginePostgres || mysqlFamily(x.dst) {
			return false, i
		}
		b.WriteString("CURRENT_TIMESTAMP")
		return true, end
	case "SUBSTRING":
		inner := t[j+1 : end]
		from := findWord(inner, 0, "FROM")
		if from < 0 || x.dst != EngineMSSQL && x.dst != EngineSQLite {
			return false, i
		}
		s := x.render(inner[:from], inSQL)
		rest := inner[from+1:]
		pos, ln := rest, []tok(nil)
		if f := findWord(rest, 0, "FOR"); f >= 0 {
			pos, ln = rest[:f], rest[f+1:]
		}
		fn := "SUBSTRING"
		if x.dst == EngineSQLite {
			fn = "substr"
		}
		if ln == nil {
			if x.dst == EngineMSSQL {
				fmt.Fprintf(b, "SUBSTRING(%s, %s, 2147483647)", s, x.render(pos, inSQL))
			} else {
				fmt.Fprintf(b, "substr(%s, %s)", s, x.render(pos, inSQL))
			}
		} else {
			fmt.Fprintf(b, "%s(%s, %s, %s)", fn, s, x.render(pos, inSQL), x.render(ln, inSQL))
		}
		return true, end
	case "CAST":
		inner := t[j+1 : end]
		as := findWord(inner, 0, "AS")
		if as < 0 {
			return false, i
		}
		val := trimToks(inner[:as])
		typ := strings.TrimSpace(joinRaw(trimToks(inner[as+1:]), x.src))
		if len(val) == 1 && val[0].k == tStr {
			switch strings.ToUpper(val[0].s) {
			case "'NOW'":
				b.WriteString("CURRENT_TIMESTAMP")
				return true, end
			case "'TODAY'":
				b.WriteString("CURRENT_DATE")
				return true, end
			}
		}
		fmt.Fprintf(b, "CAST(%s AS %s)", x.render(val, inSQL), x.castType(typ))
		return true, end
	}
	return false, i
}

// castType renders a type for CAST (MySQL's CAST takes only some types).
func (x *xlate) castType(typ string) string {
	if x.src == x.dst || sameKind(x.src, x.dst) {
		return typ
	}
	c := mCol{}
	c.Kind, c.Len, c.Prec, c.Scale = classifyType(x.src, typ)
	if mysqlFamily(x.dst) {
		switch c.Kind {
		case kInt16, kInt32, kInt64, kBool:
			return "SIGNED"
		case kDecimal:
			return decimalType("DECIMAL", &c, 65, 30)
		case kFloat32, kFloat64:
			return "DOUBLE"
		case kChar, kVarchar:
			return fmt.Sprintf("CHAR(%d)", c.Len)
		case kText, kJSON, kUUID:
			return "CHAR"
		case kBinary, kVarbinary, kBlob:
			return "BINARY"
		case kDate:
			return "DATE"
		case kTime:
			return "TIME(6)"
		case kTimestamp, kTimestampTZ:
			return "DATETIME(6)"
		}
	}
	if c.Kind == kText && x.dst == EngineFirebird {
		return "VARCHAR(8191)"
	}
	return colType(x.dst, &c, 4)
}

// limitPass moves Firebird's FIRST/SKIP and SQL Server's TOP to the end of
// their SELECT as LIMIT/OFFSET (or the other way round).
func (x *xlate) limitPass(t []tok) []tok {
	for i := 0; i < len(t); i++ {
		if !t[i].is("SELECT") {
			continue
		}
		j := skipSp(t, i+1)
		if j < len(t) && t[j].is("DISTINCT") {
			j = skipSp(t, j+1)
		}
		if j >= len(t) {
			continue
		}
		arg := func(k int) ([]tok, int) { // a number, :param or (expression)
			k = skipSp(t, k)
			if k < len(t) && t[k].op("(") {
				e := closeParen(t, k)
				return t[k : e+1], e + 1
			}
			if k < len(t) {
				return t[k : k+1], k + 1
			}
			return nil, k
		}
		var first, skip []tok
		end := j
		switch {
		case t[j].is("FIRST") && x.src == EngineFirebird && x.dst != EngineFirebird:
			first, end = arg(j + 1)
			if k := skipSp(t, end); k < len(t) && t[k].is("SKIP") {
				skip, end = arg(k + 1)
			}
		case t[j].is("SKIP") && x.src == EngineFirebird && x.dst != EngineFirebird:
			skip, end = arg(j + 1)
		case t[j].is("TOP") && x.src == EngineMSSQL && x.dst != EngineMSSQL:
			first, end = arg(j + 1)
			if k := skipSp(t, end); k < len(t) && (t[k].is("PERCENT") || t[k].is("WITH")) {
				x.note("TOP … PERCENT / WITH TIES isn't translated")
				x.todo = true
				continue
			}
		default:
			continue
		}
		// The end of this SELECT: a ) closing its level, a ; or the end.
		depth, stop := 0, len(t)
		for k := end; k < len(t); k++ {
			if t[k].op("(") {
				depth++
			} else if t[k].op(")") {
				if depth == 0 {
					stop = k
					break
				}
				depth--
			} else if t[k].op(";") && depth == 0 {
				stop = k
				break
			}
		}
		var tail []tok
		switch x.dst {
		case EngineMSSQL:
			if skip != nil {
				x.note("FIRST … SKIP: only FIRST is translated (TOP)")
				x.todo = true
			}
		case EngineFirebird:
			tail = nil
		default:
			if first != nil {
				tail = append(tail, tok{tSpace, " "}, tok{tWord, "LIMIT"}, tok{tSpace, " "})
				tail = append(tail, first...)
			} else if x.dst != EnginePostgres {
				tail = append(tail, tok{tSpace, " "}, tok{tWord, "LIMIT"}, tok{tSpace, " "}, tok{tNum, "18446744073709551615"})
				if x.dst == EngineSQLite {
					tail[len(tail)-1] = tok{tNum, "-1"}
				}
			}
			if skip != nil {
				tail = append(tail, tok{tSpace, " "}, tok{tWord, "OFFSET"}, tok{tSpace, " "})
				tail = append(tail, skip...)
			}
		}
		var head []tok
		switch {
		case x.dst == EngineMSSQL && first != nil:
			head = append([]tok{{tWord, "TOP"}, {tSpace, " "}, {tOp, "("}}, append(append([]tok{}, first...), tok{tOp, ")"}, tok{tSpace, " "})...)
		case x.dst == EngineFirebird && first != nil:
			head = append([]tok{{tWord, "FIRST"}, {tSpace, " "}}, append(append([]tok{}, first...), tok{tSpace, " "})...)
		}
		out := append([]tok{}, t[:j]...)
		out = append(out, head...)
		out = append(out, t[end:stop]...)
		out = append(out, tail...)
		out = append(out, t[stop:]...)
		t = out
	}
	return t
}

// anyArrayPass turns PostgreSQL's x = ANY (ARRAY[a, b]) into x IN (a, b).
func anyArrayPass(t []tok) []tok {
	for i := 0; i < len(t); i++ {
		if !t[i].op("=") {
			continue
		}
		a := skipSp(t, i+1)
		if a >= len(t) || !t[a].is("ANY") {
			continue
		}
		o := skipSp(t, a+1)
		if o >= len(t) || !t[o].op("(") {
			continue
		}
		r := skipSp(t, o+1)
		if r+1 >= len(t) || !t[r].is("ARRAY") || !t[skipSp(t, r+1)].op("[") {
			continue
		}
		lb := skipSp(t, r+1)
		depth, rb := 0, -1
		for k := lb; k < len(t); k++ {
			if t[k].op("[") {
				depth++
			} else if t[k].op("]") {
				if depth--; depth == 0 {
					rb = k
					break
				}
			}
		}
		c := skipSp(t, rb+1)
		if rb < 0 || c >= len(t) || !t[c].op(")") {
			continue
		}
		out := append([]tok{}, t[:i]...)
		out = append(out, tok{tWord, "IN"}, tok{tSpace, " "}, tok{tOp, "("})
		out = append(out, t[lb+1:rb]...)
		out = append(out, tok{tOp, ")"})
		t = append(out, t[c+1:]...)
	}
	return t
}

// notFuncs are words that a ( follows without being a function call.
var notFuncs = map[string]bool{"AND": true, "OR": true, "NOT": true, "IN": true, "WHEN": true, "THEN": true, "ELSE": true,
	"SELECT": true, "WHERE": true, "ON": true, "AS": true, "BY": true, "EXISTS": true, "ANY": true, "ALL": true, "SOME": true,
	"VALUES": true, "IS": true, "LIKE": true, "BETWEEN": true, "CASE": true, "RETURN": true, "FROM": true, "JOIN": true, "HAVING": true}

// limitToFetch turns LIMIT n [OFFSET m] (and MySQL's LIMIT m, n) into
// OFFSET m ROWS FETCH NEXT n ROWS ONLY, for Firebird and SQL Server.
func limitToFetch(t []tok, mssql bool) []tok {
	for i := 0; i < len(t); i++ {
		if !t[i].is("LIMIT") {
			continue
		}
		var n, off []tok
		k := skipSp(t, i+1)
		if k >= len(t) || t[k].k != tNum && t[k].k != tParam {
			continue
		}
		n, k = t[k:k+1], k+1
		if c := skipSp(t, k); c < len(t) && t[c].op(",") { // LIMIT offset, count
			if d := skipSp(t, c+1); d < len(t) {
				off, n, k = n, t[d:d+1], d+1
			}
		} else if c < len(t) && t[c].is("OFFSET") {
			if d := skipSp(t, c+1); d < len(t) {
				off, k = t[d:d+1], d+1
			}
		}
		if off == nil {
			off = []tok{{tNum, "0"}}
		}
		repl := []tok{{tWord, "OFFSET"}, {tSpace, " "}}
		repl = append(repl, off...)
		repl = append(repl, tok{tSpace, " "}, tok{tWord, "ROWS"}, tok{tSpace, " "}, tok{tWord, "FETCH"}, tok{tSpace, " "}, tok{tWord, "NEXT"}, tok{tSpace, " "})
		repl = append(repl, n...)
		repl = append(repl, tok{tSpace, " "}, tok{tWord, "ROWS"}, tok{tSpace, " "}, tok{tWord, "ONLY"})
		if mssql && findWord(t[:i], 0, "ORDER") < 0 {
			repl = append(append(lexSQL("ORDER BY (SELECT NULL)", EngineMSSQL), tok{tSpace, " "}), repl...)
		}
		t = append(append(append([]tok{}, t[:i]...), repl...), t[k:]...)
	}
	return t
}

// castPass turns PostgreSQL's value::type into CAST(value AS type).
func castPass(t []tok) []tok {
	for i := 0; i < len(t); i++ {
		if !t[i].op("::") {
			continue
		}
		// The value: a (group), or a name, literal or number (with its qualifiers).
		p := prevSp(t, i)
		if p < 0 {
			continue
		}
		from := p
		if t[p].op(")") {
			depth := 0
			for k := p; k >= 0; k-- {
				if t[k].op(")") {
					depth++
				} else if t[k].op("(") {
					if depth--; depth == 0 {
						from = k
						break
					}
				}
			}
			if f := prevSp(t, from); f >= 0 && f == from-1 && (t[f].k == tWord || t[f].k == tIdent) && !notFuncs[strings.ToUpper(t[f].s)] {
				from = f // a function call
			}
		} else {
			for from >= 2 && t[from-1].op(".") {
				from -= 2
			}
		}
		// The type: words, with (args) and [].
		k := skipSp(t, i+1)
		end := k
		for end < len(t) && (t[end].k == tWord || t[end].k == tIdent || t[end].k == tSpace && end+1 < len(t) && t[end+1].k == tWord &&
			(strings.EqualFold(t[end+1].s, "varying") || strings.EqualFold(t[end+1].s, "precision") || strings.EqualFold(t[end+1].s, "without") ||
				strings.EqualFold(t[end+1].s, "with") || strings.EqualFold(t[end+1].s, "time") || strings.EqualFold(t[end+1].s, "zone"))) {
			end++
		}
		if end < len(t) && t[end].op("(") {
			end = closeParen(t, end) + 1
		}
		typ := strings.TrimSpace(joinRaw(t[k:end], EnginePostgres))
		kind, _, _, _ := classifyType(EnginePostgres, typ)
		literal := from == p && (t[from].k == tStr && (kind == kText || kind == kVarchar || kind == kChar) || t[from].k == tNum && kind != kText)
		if strings.EqualFold(typ, "regclass") || literal { // 'x'::text is just 'x'; 100::numeric just 100
			out := append(append([]tok{}, t[:i]...), t[end:]...) // 'x'::text is just 'x'
			t = out
			i = from
			continue
		}
		out := append([]tok{}, t[:from]...)
		out = append(out, tok{tWord, "CAST"}, tok{tOp, "("})
		out = append(out, t[from:i]...)
		out = append(out, tok{tSpace, " "}, tok{tWord, "AS"}, tok{tSpace, " "}, tok{tWord, typ}, tok{tOp, ")"})
		out = append(out, t[end:]...)
		t = out
	}
	return t
}

// ---- Firebird PSQL ------------------------------------------------------------------

type pst struct {
	kind      string // block, if, while, for, assign, sql, selinto, suspend, exit, leave, continue, raise, call, return, empty, todo
	label     string
	cond      []tok
	then, els *pst
	body      *pst
	stmts     []*pst
	handlers  []*pst // a block's WHEN … DO
	lhs, expr []tok
	into      []tok
	name      string
	args      []tok
	raw       string
}

type pdecl struct {
	name, typ string
	def       []tok
}

type psqlParser struct {
	t   []tok
	i   int
	err error
}

func (p *psqlParser) peek() tok {
	p.i = skipSp(p.t, p.i)
	if p.i < len(p.t) {
		return p.t[p.i]
	}
	return tok{tOp, ""}
}

func (p *psqlParser) eat(w string) bool {
	if p.peek().is(w) || p.peek().op(w) {
		p.i++
		return true
	}
	return false
}

func (p *psqlParser) fail(format string, args ...any) {
	if p.err == nil {
		at := p.i
		ctx := joinRaw(p.t[at:min(at+12, len(p.t))], EngineFirebird)
		p.err = fmt.Errorf(format+" near: %s", append(args, strings.TrimSpace(ctx))...)
	}
	p.i = len(p.t)
}

// untilSemi returns the tokens up to the next top-level ; (which it skips).
func (p *psqlParser) untilSemi() []tok {
	from, depth := p.i, 0
	for ; p.i < len(p.t); p.i++ {
		c := p.t[p.i]
		if c.op("(") {
			depth++
		} else if c.op(")") {
			depth--
		} else if c.op(";") && depth == 0 {
			p.i++
			return trimToks(p.t[from : p.i-1])
		}
	}
	return trimToks(p.t[from:])
}

// parseBody parses a procedure's, function's or trigger's source: the
// declarations and the main block.
func parseBody(src string) ([]pdecl, *pst, error) {
	p := &psqlParser{t: lexSQL(src, EngineFirebird)}
	p.eat("AS")
	var decls []pdecl
	for p.peek().is("DECLARE") {
		p.i++
		p.eat("VARIABLE")
		name := p.peek()
		if name.k != tWord && name.k != tIdent {
			p.fail("a variable name was expected")
			break
		}
		p.i++
		if p.peek().is("CURSOR") || p.peek().is("PROCEDURE") || p.peek().is("FUNCTION") {
			p.fail("DECLARE … %s isn't translated", strings.ToUpper(p.peek().s))
			break
		}
		from := p.i
		for p.i < len(p.t) {
			c := p.t[p.i]
			if c.op(";") || c.op("=") || c.is("DEFAULT") || c.is("NOT") {
				break
			}
			if c.op("(") {
				p.i = closeParen(p.t, p.i)
			}
			p.i++
		}
		d := pdecl{name: name.s, typ: strings.TrimSpace(joinRaw(p.t[from:p.i], EngineFirebird))}
		if p.eat("NOT") {
			p.eat("NULL")
		}
		if p.eat("=") || p.eat("DEFAULT") {
			d.def = p.untilSemi()
		} else {
			p.eat(";")
		}
		decls = append(decls, d)
	}
	if !p.peek().is("BEGIN") {
		p.fail("BEGIN was expected")
	}
	body := p.stmt()
	if p.err == nil && skipSp(p.t, p.i) < len(p.t) {
		p.fail("unexpected text after the last END")
	}
	return decls, body, p.err
}

func (p *psqlParser) stmt() *pst {
	c := p.peek()
	if p.i >= len(p.t) {
		p.fail("the source ended early")
		return &pst{kind: "empty"}
	}
	// A label: name: FOR / WHILE
	if c.k == tWord {
		if k := skipSp(p.t, p.i+1); k < len(p.t) && p.t[k].op(":") {
			p.i = k + 1
			s := p.stmt()
			s.label = c.s
			return s
		}
	}
	W := strings.ToUpper(c.s)
	switch {
	case c.op(";"):
		p.i++
		return &pst{kind: "empty"}
	case W == "BEGIN" && c.k == tWord:
		p.i++
		b := &pst{kind: "block"}
		for p.err == nil && !p.peek().is("END") && !p.peek().is("WHEN") && p.i < len(p.t) {
			b.stmts = append(b.stmts, p.stmt())
		}
		for p.err == nil && p.peek().is("WHEN") {
			p.i++
			from := p.i
			for p.i < len(p.t) && !p.t[p.i].is("DO") {
				p.i++
			}
			h := &pst{kind: "when", cond: trimToks(p.t[from:p.i])}
			p.i++
			h.body = p.stmt()
			b.handlers = append(b.handlers, h)
		}
		if !p.eat("END") {
			p.fail("END was expected")
		}
		p.eat(";")
		return b
	case W == "IF" && c.k == tWord:
		p.i++
		s := &pst{kind: "if", cond: p.parens()}
		if !p.eat("THEN") {
			p.fail("THEN was expected")
		}
		s.then = p.stmt()
		if p.eat("ELSE") {
			s.els = p.stmt()
		}
		return s
	case W == "WHILE" && c.k == tWord:
		p.i++
		s := &pst{kind: "while", cond: p.parens()}
		if !p.eat("DO") {
			p.fail("DO was expected")
		}
		s.body = p.stmt()
		return s
	case W == "FOR" && c.k == tWord:
		p.i++
		if !p.peek().is("SELECT") && !p.peek().is("WITH") {
			p.fail("FOR %s isn't translated", strings.ToUpper(p.peek().s))
			return &pst{kind: "todo"}
		}
		from := p.i
		do := findWord(p.t, p.i, "DO")
		if do < 0 {
			p.fail("DO was expected")
			return &pst{kind: "todo"}
		}
		q := p.t[from:do]
		s := &pst{kind: "for"}
		if as := findWord(q, 0, "AS"); as >= 0 && skipSp(q, as+1) < len(q) && q[skipSp(q, as+1)].is("CURSOR") {
			if k := findWord(q, 0, "INTO"); k < 0 || k > as {
				p.fail("FOR SELECT … AS CURSOR isn't translated")
			}
			q = q[:as]
		}
		s.expr, s.into = splitInto(q)
		if s.into == nil {
			p.fail("FOR SELECT without INTO")
		}
		p.i = do + 1
		s.body = p.stmt()
		return s
	case W == "SUSPEND":
		p.i++
		p.eat(";")
		return &pst{kind: "suspend"}
	case W == "EXIT":
		p.i++
		p.eat(";")
		return &pst{kind: "exit"}
	case W == "LEAVE" || W == "BREAK" || W == "CONTINUE":
		p.i++
		s := &pst{kind: map[bool]string{true: "continue", false: "leave"}[W == "CONTINUE"]}
		if n := p.peek(); n.k == tWord {
			s.name = n.s
			p.i++
		}
		p.eat(";")
		return s
	case W == "EXCEPTION":
		p.i++
		s := &pst{kind: "raise"}
		if n := p.peek(); n.k == tWord || n.k == tIdent {
			s.name = n.s
			p.i++
			s.expr = p.untilSemi()
		} else {
			p.eat(";")
		}
		return s
	case W == "RETURN":
		p.i++
		return &pst{kind: "return", expr: p.untilSemi()}
	case W == "EXECUTE":
		p.i++
		if !p.eat("PROCEDURE") {
			s := &pst{kind: "todo", raw: "EXECUTE " + joinRaw(p.untilSemi(), EngineFirebird)}
			return s
		}
		n := p.peek()
		p.i++
		s := &pst{kind: "call", name: n.s}
		rest := p.untilSemi()
		if k := findWord(rest, 0, "RETURNING_VALUES"); k >= 0 {
			s.into = trimToks(rest[k+1:])
			rest = rest[:k]
		}
		rest = trimToks(rest)
		if len(rest) > 0 && rest[0].op("(") && closeParen(rest, 0) == len(rest)-1 {
			rest = rest[1 : len(rest)-1]
		}
		s.args = rest
		return s
	case W == "POST_EVENT" || W == "IN" && c.k == tWord:
		p.i++
		return &pst{kind: "todo", raw: c.s + " " + joinRaw(p.untilSemi(), EngineFirebird)}
	case W == "SELECT" || W == "WITH":
		q := p.untilSemi()
		s := &pst{kind: "sql"}
		s.expr, s.into = splitInto(q)
		if s.into != nil {
			s.kind = "selinto"
		}
		return s
	case W == "INSERT" || W == "UPDATE" || W == "DELETE" || W == "MERGE":
		q := p.untilSemi()
		if findWord(q, 0, "RETURNING") >= 0 {
			return &pst{kind: "todo", raw: joinRaw(q, EngineFirebird)}
		}
		return &pst{kind: "sql", expr: q}
	}
	// An assignment: target = expression;
	q := p.untilSemi()
	for k, x := range q {
		if x.op("=") {
			return &pst{kind: "assign", lhs: trimToks(q[:k]), expr: trimToks(q[k+1:])}
		}
	}
	return &pst{kind: "todo", raw: joinRaw(q, EngineFirebird)}
}

// parens returns the tokens inside the next ( … ).
func (p *psqlParser) parens() []tok {
	if !p.peek().op("(") {
		p.fail("( was expected")
		return nil
	}
	e := closeParen(p.t, p.i)
	in := p.t[p.i+1 : e]
	p.i = e + 1
	return trimToks(in)
}

// splitInto splits SELECT … INTO :a, :b (INTO last, or before FROM).
func splitInto(q []tok) (query, into []tok) {
	k := findWord(q, 0, "INTO")
	if k < 0 {
		return q, nil
	}
	if f := findWord(q, k, "FROM"); f > k {
		return append(append([]tok{}, q[:k]...), q[f:]...), trimToks(q[k+1 : f])
	}
	return trimToks(q[:k]), trimToks(q[k+1:])
}

// intoVars lists the variables of an INTO / RETURNING_VALUES list.
func intoVars(t []tok) []string {
	var out []string
	for _, a := range splitArgs(t) {
		a = trimToks(a)
		if len(a) == 1 {
			out = append(out, a[0].s)
		} else if len(a) > 0 {
			out = append(out, joinRaw(a, EngineFirebird))
		}
	}
	return out
}

// ---- writing PSQL for MySQL / MariaDB and PostgreSQL ------------------------------------

// codeWriter writes the statements of one routine or trigger.
type codeWriter struct {
	x        *xlate
	b        strings.Builder
	pg       bool
	routine  string   // the routine's target name (PostgreSQL qualifies parameters with it)
	tmp      string   // MySQL, selectable procedure: the temporary table of its rows
	outs     []string // its output variables, in order
	exitStmt string   // what EXIT becomes
	loops    []string // the labels of the enclosing loops, innermost last
	cursors  int      // MySQL: FOR SELECT loops in scope (a SELECT INTO then needs its own NOT FOUND handler)
	n        int      // for unique labels
	excMsg   map[string]string
}

func (w *codeWriter) line(depth int, s string) {
	w.b.WriteString(strings.Repeat("  ", depth) + s + "\n")
}

func (w *codeWriter) expr(t []tok) string { return w.x.render(t, false) }
func (w *codeWriter) sql(t []tok) string  { return w.x.render(t, true) }

// fold evaluates a condition made only of TRUE, FALSE, NOT, AND, OR and
// parentheses (INSERTING… in a trigger split by event): ok is false otherwise.
func (w *codeWriter) fold(cond []tok) (val, ok bool) {
	s := strings.ToUpper(strings.Join(strings.Fields(w.expr(cond)), " "))
	s = strings.NewReplacer("(", " ( ", ")", " ) ").Replace(s)
	f := strings.Fields(s)
	for _, x := range f {
		switch x {
		case "TRUE", "FALSE", "NOT", "AND", "OR", "(", ")", "1", "0":
		default:
			return false, false
		}
	}
	pos := 0
	var or func() bool
	atom := func() bool {
		if pos >= len(f) {
			return false
		}
		x := f[pos]
		pos++
		switch x {
		case "NOT":
			return !atomNot(&pos, f, or)
		case "(":
			v := or()
			pos++ // )
			return v
		}
		return x == "TRUE" || x == "1"
	}
	and := func() bool {
		v := atom()
		for pos < len(f) && f[pos] == "AND" {
			pos++
			v = atom() && v
		}
		return v
	}
	or = func() bool {
		v := and()
		for pos < len(f) && f[pos] == "OR" {
			pos++
			v = and() || v
		}
		return v
	}
	return or(), true
}

func atomNot(pos *int, f []string, or func() bool) bool {
	if *pos >= len(f) {
		return false
	}
	x := f[*pos]
	*pos++
	switch x {
	case "NOT":
		return !atomNot(pos, f, or)
	case "(":
		v := or()
		*pos++
		return v
	}
	return x == "TRUE" || x == "1"
}

func (w *codeWriter) label(s *pst) string {
	if s.label != "" {
		return w.x.applyCase(s.label)
	}
	w.n++
	return fmt.Sprintf("loop_%d", w.n)
}

// stmts writes a statement; a block's statements are written in place.
func (w *codeWriter) stmts(s *pst, depth int) {
	if s.kind == "block" && len(s.handlers) == 0 && s.label == "" {
		for _, c := range s.stmts {
			w.stmt(c, depth)
		}
		return
	}
	w.stmt(s, depth)
}

func (w *codeWriter) stmt(s *pst, depth int) {
	x := w.x
	switch s.kind {
	case "empty":
	case "block":
		if len(s.handlers) == 0 && s.label == "" {
			w.stmts(s, depth)
			return
		}
		w.line(depth, "BEGIN")
		if !w.pg {
			for _, h := range s.handlers {
				w.line(depth+1, "DECLARE EXIT HANDLER FOR SQLEXCEPTION")
				w.line(depth+1, "BEGIN")
				w.stmts(h.body, depth+2)
				w.line(depth+1, "END;")
				w.handlerNote(h)
			}
		}
		for _, c := range s.stmts {
			w.stmt(c, depth+1)
		}
		if w.pg && len(s.handlers) > 0 {
			w.line(depth, "EXCEPTION WHEN OTHERS THEN")
			for _, h := range s.handlers {
				w.stmts(h.body, depth+1)
				w.handlerNote(h)
			}
		}
		w.line(depth, "END;")
	case "if":
		if v, ok := w.fold(s.cond); ok {
			if v {
				w.stmts(s.then, depth)
			} else if s.els != nil {
				w.stmts(s.els, depth)
			}
			return
		}
		w.line(depth, "IF "+w.expr(s.cond)+" THEN")
		w.stmts(s.then, depth+1)
		for s.els != nil && s.els.kind == "if" {
			if _, ok := w.fold(s.els.cond); ok {
				break
			}
			s = s.els
			kw := "ELSEIF "
			if w.pg {
				kw = "ELSIF "
			}
			w.line(depth, kw+w.expr(s.cond)+" THEN")
			w.stmts(s.then, depth+1)
		}
		if s.els != nil {
			w.line(depth, "ELSE")
			w.stmts(s.els, depth+1)
		}
		w.line(depth, "END IF;")
	case "while":
		lbl := w.label(s)
		w.loops = append(w.loops, lbl)
		if w.pg {
			w.line(depth, "<<"+lbl+">>")
			w.line(depth, "WHILE "+w.expr(s.cond)+" LOOP")
			w.stmts(s.body, depth+1)
			w.line(depth, "END LOOP "+lbl+";")
		} else {
			w.line(depth, lbl+": WHILE "+w.expr(s.cond)+" DO")
			w.stmts(s.body, depth+1)
			w.line(depth, "END WHILE "+lbl+";")
		}
		w.loops = w.loops[:len(w.loops)-1]
	case "for":
		lbl := w.label(s)
		vars := w.varList(s.into)
		w.loops = append(w.loops, lbl)
		if w.pg {
			w.line(depth, "<<"+lbl+">>")
			w.line(depth, "FOR "+vars+" IN "+w.sql(s.expr))
			w.line(depth, "LOOP")
			w.stmts(s.body, depth+1)
			w.line(depth, "END LOOP "+lbl+";")
		} else {
			w.n++
			cur, done := fmt.Sprintf("cur_%d", w.n), fmt.Sprintf("done_%d", w.n)
			w.line(depth, "BEGIN")
			w.line(depth+1, "DECLARE "+done+" INT DEFAULT 0;")
			w.line(depth+1, "DECLARE "+cur+" CURSOR FOR "+w.sql(s.expr)+";")
			w.line(depth+1, "DECLARE CONTINUE HANDLER FOR NOT FOUND SET "+done+" = 1;")
			w.line(depth+1, "OPEN "+cur+";")
			w.line(depth+1, lbl+": LOOP")
			w.line(depth+2, "FETCH "+cur+" INTO "+vars+";")
			w.line(depth+2, "IF "+done+" THEN LEAVE "+lbl+"; END IF;")
			w.cursors++
			w.stmts(s.body, depth+2)
			w.cursors--
			w.line(depth+1, "END LOOP "+lbl+";")
			w.line(depth+1, "CLOSE "+cur+";")
			w.line(depth, "END;")
		}
		w.loops = w.loops[:len(w.loops)-1]
	case "assign":
		lhs := w.expr(s.lhs)
		if w.pg {
			w.line(depth, lhs+" := "+w.expr(s.expr)+";")
		} else {
			w.line(depth, "SET "+lhs+" = "+w.expr(s.expr)+";")
		}
	case "sql":
		w.line(depth, w.sql(s.expr)+";")
	case "selinto":
		q := s.expr
		f := findWord(q, 0, "FROM")
		var stmt string
		if f >= 0 {
			stmt = w.sql(q[:f]) + " INTO " + w.varList(s.into) + " " + w.sql(q[f:])
		} else {
			stmt = w.sql(q) + " INTO " + w.varList(s.into)
		}
		if !w.pg && w.cursors > 0 { // no rows: leave the variables, and the loop going
			w.line(depth, "BEGIN")
			w.line(depth+1, "DECLARE CONTINUE HANDLER FOR NOT FOUND BEGIN END;")
			w.line(depth+1, stmt+";")
			w.line(depth, "END;")
		} else {
			w.line(depth, stmt+";")
		}
	case "suspend":
		if w.pg {
			w.line(depth, "RETURN NEXT;")
		} else if w.tmp != "" {
			w.line(depth, "INSERT INTO "+x.q(w.tmp)+" VALUES ("+strings.Join(w.outVars(), ", ")+");")
		} else {
			x.note("SUSPEND outside a selectable procedure")
			x.todo = true
		}
	case "exit":
		w.line(depth, w.exitStmt)
	case "leave", "continue":
		lbl := ""
		if s.name != "" {
			lbl = x.applyCase(s.name)
		} else if len(w.loops) > 0 {
			lbl = w.loops[len(w.loops)-1]
		}
		switch {
		case w.pg && s.kind == "leave":
			w.line(depth, strings.TrimSpace("EXIT "+lbl)+";")
		case w.pg:
			w.line(depth, strings.TrimSpace("CONTINUE "+lbl)+";")
		case s.kind == "leave":
			w.line(depth, "LEAVE "+lbl+";")
		default:
			w.line(depth, "ITERATE "+lbl+";")
		}
	case "raise":
		msg := ""
		if len(s.expr) > 0 {
			msg = w.expr(s.expr)
		} else if m, ok := w.excMsg[strings.ToUpper(s.name)]; ok {
			msg = quoteString(EngineFirebird, m)
		} else if s.name != "" {
			msg = quoteString(EngineFirebird, s.name)
		}
		switch {
		case s.name == "" && w.pg:
			w.line(depth, "RAISE;")
		case s.name == "":
			w.line(depth, "RESIGNAL;")
		case w.pg:
			w.line(depth, "RAISE EXCEPTION '%', "+msg+";")
		default:
			w.line(depth, "SET @cactusdb_message = SUBSTRING("+msg+", 1, 128);")
			w.line(depth, "SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = @cactusdb_message;")
		}
	case "call":
		name, ok := x.nameOf(s.name, false)
		if !ok {
			name = x.applyCase(s.name)
		}
		args := []string{}
		if len(trimToks(s.args)) > 0 {
			for _, a := range splitArgs(s.args) {
				args = append(args, w.expr(a))
			}
		}
		if len(s.into) > 0 {
			args = append(args, w.varList(s.into))
		}
		w.line(depth, "CALL "+x.q(name)+"("+strings.Join(args, ", ")+");")
	case "return":
		w.line(depth, "RETURN "+w.expr(s.expr)+";")
	case "when":
	default: // todo
		x.todo = true
		x.note("not translated: %s", strings.TrimSpace(s.raw))
		w.line(depth, "-- TODO (cactusdb couldn't translate this): "+strings.ReplaceAll(strings.TrimSpace(s.raw), "\n", " "))
	}
}

func (w *codeWriter) handlerNote(h *pst) {
	if c := strings.ToUpper(joinRaw(h.cond, EngineFirebird)); c != "ANY" {
		w.x.note("WHEN %s DO: handled as any error", joinRaw(h.cond, EngineFirebird))
	}
}

func (w *codeWriter) varList(t []tok) string {
	var out []string
	for _, v := range intoVars(t) {
		out = append(out, w.x.varRef(strings.TrimPrefix(v, ":"), false))
	}
	return strings.Join(out, ", ")
}

func (w *codeWriter) outVars() []string {
	var out []string
	for _, o := range w.outs {
		out = append(out, w.x.varRef(o, false))
	}
	return out
}

// psqlRoutine describes a Firebird procedure, function or trigger to write.
type psqlRoutine struct {
	kind        string // procedure, function, trigger
	name        string // the source name
	ins, outs   []mCol // parameters (Name = source name; the kind and size)
	returns     *mCol  // a function's result
	selectable  bool   // a procedure with SUSPEND: it returns rows
	table       string // a trigger's table (source name)
	when        string // BEFORE / AFTER
	events      []string
	source      string
	decls       []pdecl
	body        *pst
	excMessages map[string]string
	domains     map[string]string // domain → its type, for DECLARE … D_MONEY
}

// hasKind reports whether a statement (or one inside it) is of kind.
func hasKind(s *pst, kind string) bool {
	if s == nil {
		return false
	}
	if s.kind == kind {
		return true
	}
	for _, c := range s.stmts {
		if hasKind(c, kind) {
			return true
		}
	}
	for _, h := range s.handlers {
		if hasKind(h.body, kind) {
			return true
		}
	}
	return hasKind(s.then, kind) || hasKind(s.els, kind) || hasKind(s.body, kind)
}

// declType resolves a declared type: a domain, TYPE OF … or a type.
func (r *psqlRoutine) declType(x *xlate, typ string) string {
	t := strings.TrimSpace(typ)
	up := strings.ToUpper(t)
	if s, ok := strings.CutPrefix(up, "TYPE OF COLUMN "); ok {
		x.note("TYPE OF COLUMN %s is declared as text", s)
		return colType(x.dst, &mCol{Kind: kText}, 4)
	}
	if s, ok := strings.CutPrefix(up, "TYPE OF "); ok {
		t, up = s, s
	}
	if d, ok := r.domains[strings.Trim(up, `"`)]; ok {
		t = d
	}
	c := mCol{}
	c.Kind, c.Len, c.Prec, c.Scale = classifyType(EngineFirebird, t)
	return colType(x.dst, &c, 4)
}

// writeRoutine translates a parsed Firebird routine for the target. It
// returns the statements to run.
func writeRoutine(x *xlate, r *psqlRoutine) []string {
	pg := x.dst == EnginePostgres
	tname, _ := x.nameOf(r.name, false)
	if tname == "" {
		tname = x.applyCase(r.name)
	}
	w := &codeWriter{x: x, pg: pg, routine: tname, excMsg: r.excMessages}
	x.vars, x.sqlVars = map[string]string{}, map[string]string{}
	// Variables: named like a column, they are renamed (MySQL) or
	// qualified inside SQL statements (PostgreSQL).
	addVar := func(name, prefix, qualifier string) string {
		v := x.applyCase(name)
		if x.columns[strings.ToUpper(name)] {
			if pg {
				x.sqlVars[strings.ToUpper(name)] = qualifier + "." + v
			} else {
				v = prefix + v
				x.note("the variable %s is called %s: MySQL would take it for the column of that name", name, v)
			}
		}
		x.vars[strings.ToUpper(name)] = v
		return v
	}
	var head strings.Builder
	var decls []string
	typ := func(c *mCol) string { return colType(x.dst, c, 4) }
	switch r.kind {
	case "trigger":
		w.exitStmt = "LEAVE main;"
		if pg {
			w.exitStmt = "RETURN " + pgTriggerReturn(r) + ";"
		}
	default:
		w.exitStmt = "LEAVE main;"
		if pg {
			w.exitStmt = "RETURN;"
		}
	}
	var params []string
	for _, p := range r.ins {
		v := addVar(p.Name, "p_", tname)
		if pg {
			params = append(params, v+" "+typ(&p))
		} else if r.kind == "function" {
			params = append(params, v+" "+typ(&p))
		} else {
			params = append(params, "IN "+v+" "+typ(&p))
		}
	}
	for _, o := range r.outs {
		w.outs = append(w.outs, o.Name)
		switch {
		case r.selectable && pg:
			addVar(o.Name, "v_", tname)
		case r.selectable:
			decls = append(decls, "DECLARE "+addVar(o.Name, "v_", "main")+" "+typ(&o)+";")
		case pg:
			params = append(params, "INOUT "+addVar(o.Name, "o_", tname)+" "+typ(&o)+" DEFAULT NULL")
		default:
			params = append(params, "OUT "+addVar(o.Name, "o_", "main")+" "+typ(&o))
		}
	}
	for _, d := range r.decls {
		v := addVar(d.name, "v_", "main")
		line := v + " " + r.declType(x, d.typ)
		if d.def != nil {
			if pg {
				line += " := " + w.expr(d.def)
			} else {
				line += " DEFAULT " + w.expr(d.def)
			}
		}
		if pg {
			decls = append(decls, line+";")
		} else {
			decls = append(decls, "DECLARE "+line+";")
		}
	}
	var out []string
	switch {
	case pg && r.kind == "trigger":
		x.consts = map[string]string{"INSERTING": "(TG_OP = 'INSERT')", "UPDATING": "(TG_OP = 'UPDATE')", "DELETING": "(TG_OP = 'DELETE')"}
		fn := x.applyCase("trg_" + r.name)
		tbl, _ := x.nameOf(r.table, false)
		fmt.Fprintf(&head, "CREATE OR REPLACE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $cactusdb$\n", x.q(fn))
		body := w.pgBody(r, decls)
		out = append(out, head.String()+body+"$cactusdb$",
			fmt.Sprintf("CREATE TRIGGER %s %s %s ON %s FOR EACH ROW EXECUTE FUNCTION %s()", x.q(tname), r.when,
				strings.Join(r.events, " OR "), x.q(tbl), x.q(fn)))
	case pg:
		x.consts = nil
		switch {
		case r.kind == "function":
			fmt.Fprintf(&head, "CREATE OR REPLACE FUNCTION %s(%s) RETURNS %s LANGUAGE plpgsql AS $cactusdb$\n", x.q(tname), strings.Join(params, ", "), typ(r.returns))
		case r.selectable:
			var cols []string
			for _, o := range r.outs {
				cols = append(cols, x.applyCase(o.Name)+" "+typ(&o))
			}
			fmt.Fprintf(&head, "CREATE OR REPLACE FUNCTION %s(%s) RETURNS TABLE (%s) LANGUAGE plpgsql AS $cactusdb$\n", x.q(tname), strings.Join(params, ", "), strings.Join(cols, ", "))
		default:
			fmt.Fprintf(&head, "CREATE OR REPLACE PROCEDURE %s(%s) LANGUAGE plpgsql AS $cactusdb$\n", x.q(tname), strings.Join(params, ", "))
		}
		out = append(out, head.String()+w.pgBody(r, decls)+"$cactusdb$")
	case r.kind == "trigger": // MySQL: a trigger per event
		tbl, _ := x.nameOf(r.table, false)
		for _, ev := range r.events {
			x.consts = map[string]string{"INSERTING": "FALSE", "UPDATING": "FALSE", "DELETING": "FALSE"}
			x.consts[map[string]string{"INSERT": "INSERTING", "UPDATE": "UPDATING", "DELETE": "DELETING"}[ev]] = "TRUE"
			name := tname
			if len(r.events) > 1 {
				name = x.applyCase(r.name + "_" + strings.ToLower(ev[:3]))
			}
			w.b.Reset()
			w.n = 0
			fmt.Fprintf(&w.b, "CREATE TRIGGER %s %s %s ON %s FOR EACH ROW\nmain: BEGIN\n", x.q(name), r.when, ev, x.q(tbl))
			for _, d := range decls {
				w.line(1, d)
			}
			w.stmts(r.body, 1)
			w.b.WriteString("END")
			out = append(out, w.b.String())
		}
	default: // MySQL procedure or function
		x.consts = nil
		if r.kind == "function" {
			fmt.Fprintf(&w.b, "CREATE FUNCTION %s(%s) RETURNS %s READS SQL DATA\nmain: BEGIN\n", x.q(tname), strings.Join(params, ", "), typ(r.returns))
		} else {
			fmt.Fprintf(&w.b, "CREATE PROCEDURE %s(%s)\nBEGIN\n", x.q(tname), strings.Join(params, ", "))
		}
		for _, d := range decls {
			w.line(1, d)
		}
		depth := 1
		if r.kind != "function" {
			if r.selectable {
				w.tmp = x.applyCase("tmp_" + r.name)
				var cols []string
				for _, o := range r.outs {
					cols = append(cols, x.q(x.applyCase(o.Name))+" "+typ(&o))
				}
				w.line(1, "DROP TEMPORARY TABLE IF EXISTS "+x.q(w.tmp)+";")
				w.line(1, "CREATE TEMPORARY TABLE "+x.q(w.tmp)+" ("+strings.Join(cols, ", ")+");")
			}
			w.line(1, "main: BEGIN")
			depth = 2
		}
		w.stmts(r.body, depth)
		if r.kind != "function" {
			w.line(1, "END main;")
			if r.selectable {
				w.line(1, "SELECT * FROM "+x.q(w.tmp)+";")
				w.line(1, "DROP TEMPORARY TABLE "+x.q(w.tmp)+";")
			}
		}
		w.b.WriteString("END")
		out = append(out, w.b.String())
		if r.selectable {
			x.note("a selectable procedure: CALL %s(…) returns its rows (MySQL can't SELECT FROM a procedure)", tname)
		}
	}
	return out
}

func pgTriggerReturn(r *psqlRoutine) string {
	if r.when == "AFTER" {
		return "NULL"
	}
	if len(r.events) == 1 && r.events[0] == "DELETE" {
		return "OLD"
	}
	for _, e := range r.events {
		if e == "DELETE" {
			return "CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END"
		}
	}
	return "NEW"
}

func (w *codeWriter) pgBody(r *psqlRoutine, decls []string) string {
	w.b.Reset()
	if len(w.x.sqlVars) > 0 {
		// A variable named like a column: in SQL the name is the column's
		// (the variable is written qualified there).
		w.b.WriteString("#variable_conflict use_column\n")
	}
	w.b.WriteString("<<main>>\n")
	if len(decls) > 0 {
		w.b.WriteString("DECLARE\n")
		for _, d := range decls {
			w.line(1, d)
		}
	}
	w.b.WriteString("BEGIN\n")
	body := r.body
	if len(body.handlers) > 0 {
		w.stmt(body, 1)
	} else {
		w.stmts(body, 1)
	}
	if r.kind == "trigger" {
		w.line(1, "RETURN "+pgTriggerReturn(r)+";")
	}
	w.b.WriteString("END main;\n")
	return w.b.String()
}

// autoIncrementTrigger recognises the trigger that numbers a column from a
// generator: IF (NEW.ID IS NULL) THEN NEW.ID = GEN_ID(G, 1); It returns the
// column and the generator.
func autoIncrementTrigger(r *psqlRoutine) (col, gen string, ok bool) {
	if r.kind != "trigger" || r.when != "BEFORE" || len(r.events) != 1 || r.events[0] != "INSERT" || len(r.decls) > 0 {
		return "", "", false
	}
	s := r.body
	for s.kind == "block" && len(s.stmts) == 1 && len(s.handlers) == 0 {
		s = s.stmts[0]
	}
	if s.kind == "if" && s.els == nil {
		s = s.then
		for s.kind == "block" && len(s.stmts) == 1 {
			s = s.stmts[0]
		}
	}
	if s.kind != "assign" {
		return "", "", false
	}
	l := trimToks(s.lhs)
	if len(l) != 3 || !l[0].is("NEW") || !l[1].op(".") {
		return "", "", false
	}
	e := trimToks(s.expr)
	switch {
	case len(e) >= 4 && e[0].is("GEN_ID"):
		args := splitArgs(e[skipSp(e, 1)+1 : closeParen(e, skipSp(e, 1))])
		if len(args) == 2 && strings.TrimSpace(joinRaw(trimToks(args[1]), EngineFirebird)) == "1" {
			if a := trimToks(args[0]); len(a) == 1 {
				return l[2].s, a[0].s, true
			}
		}
	case len(e) >= 7 && e[0].is("NEXT") && e[2].is("VALUE") && e[4].is("FOR"):
		return l[2].s, e[6].s, true
	}
	return "", "", false
}
