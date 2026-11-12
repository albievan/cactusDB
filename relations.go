package main

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
)

// DB Relationships: every object of the connected database and how they hang
// together, for the tab that draws them. Foreign keys join tables, triggers
// fire on tables and views, and views, procedures, functions, triggers and
// packages use tables, views, other routines, generators / sequences and
// exceptions. Each database's own catalog of dependencies is read where it
// keeps one (Firebird, PostgreSQL, SQL Server, Oracle); otherwise, and for
// what a catalog doesn't record (PostgreSQL function bodies), the names of
// the other objects are looked for in the object's source: those lines are
// marked as found in the source. Only the catalog is read, never any rows.

type relNode struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
}

type relEdge struct {
	From  string `json:"from"` // the object that depends on…
	To    string `json:"to"`   // …this one
	Kind  string `json:"kind"` // fk, trigger or uses
	Label string `json:"label,omitempty"`
	Guess bool   `json:"guess,omitempty"` // found by its name in the source
}

// relObj is an object as the catalog queries name it: its sidebar name and
// the types it may be (a Firebird relation is a table or a view).
type relObj struct {
	types []string
	name  string
}

func relOf(name string, types ...string) relObj { return relObj{types, name} }

// relSource is the source of an object, to look for the names of others in.
type relSource struct {
	obj relObj
	src string
}

type relGraph struct {
	nodes   []relNode
	byKey   map[string]string   // type + "\x00" + lower-case name → id
	byName  map[string][]string // lower-case name, its last part, and without arguments → ids
	edges   map[string]*relEdge
	order   []string
	notes   []string
	labels  map[string]string // type → the sidebar's label, e.g. Generators
	schemas map[string]bool   // lower-case schemas (and MySQL's database) that qualify names
}

// relTypes are the objects drawn: all but indexes.
func relDrawn(typ string) bool { return typ != ObjIndex }

func newRelGraph(groups []ObjectGroup) *relGraph {
	g := &relGraph{byKey: map[string]string{}, byName: map[string][]string{}, edges: map[string]*relEdge{}, labels: map[string]string{}, schemas: map[string]bool{}}
	for _, gr := range groups {
		if !relDrawn(gr.Type) {
			continue
		}
		g.labels[gr.Type] = gr.Label
		for _, name := range gr.Items {
			id := gr.Type + ":" + name
			g.nodes = append(g.nodes, relNode{ID: id, Type: gr.Type, Name: name})
			lower := strings.ToLower(name)
			g.byKey[gr.Type+"\x00"+lower] = id
			bare := lower
			if i := strings.IndexByte(bare, '('); i >= 0 { // PostgreSQL routines: schema.name(args)
				bare = bare[:i]
			}
			keys := []string{lower, bare}
			if gr.Type != ObjTrigger { // schema.table.trigger: only its full name
				if i := strings.LastIndexByte(bare, '.'); i >= 0 {
					keys = append(keys, bare[i+1:])
					g.schemas[bare[:i]] = true
				}
			}
			for _, k := range slices.Compact(keys) {
				if !slices.Contains(g.byName[k], id) {
					g.byName[k] = append(g.byName[k], id)
				}
			}
		}
	}
	return g
}

// find is the id of the object, of the first of its types that has it.
func (g *relGraph) find(o relObj) string {
	lower := strings.ToLower(o.name)
	for _, t := range o.types {
		if id, ok := g.byKey[t+"\x00"+lower]; ok {
			return id
		}
	}
	if len(o.types) == 0 { // any type: names are unique (SQL Server)
		if ids := g.byName[lower]; len(ids) == 1 {
			return ids[0]
		}
	}
	return ""
}

func (g *relGraph) add(from, to relObj, kind, label string) {
	f, t := g.find(from), g.find(to)
	g.link(f, t, kind, label, false)
}

func (g *relGraph) link(from, to, kind, label string, guess bool) {
	if from == "" || to == "" || from == to && kind != "fk" { // a table may refer to itself (a tree)
		return
	}
	key := from + "\x00" + to + "\x00" + kind
	if e := g.edges[key]; e != nil {
		if label != "" && !strings.Contains(e.Label, label) {
			e.Label = strings.TrimPrefix(e.Label+"; "+label, "; ")
		}
		e.Guess = e.Guess && guess
		return
	}
	// A trigger that fires on a table doesn't "use" it as well.
	if kind == "uses" && g.edges[from+"\x00"+to+"\x00trigger"] != nil {
		return
	}
	if uses := from + "\x00" + to + "\x00uses"; kind == "trigger" && g.edges[uses] != nil {
		delete(g.edges, uses)
		g.order = slices.DeleteFunc(g.order, func(k string) bool { return k == uses })
	}
	g.edges[key] = &relEdge{From: from, To: to, Kind: kind, Label: label, Guess: guess}
	g.order = append(g.order, key)
}

// relScanTo are the objects whose names are looked for in sources.
var relScanTo = map[string]bool{ObjTable: true, ObjView: true, ObjMatView: true, ObjProcedure: true, ObjFunction: true,
	ObjPackage: true, ObjSequence: true, ObjException: true, ObjSynonym: true}

// scan links the object of a source with every object it names: a name,
// or its first part (a table before .column), or after a schema (schema.name,
// schema.table.column).
func (g *relGraph) scan(s relSource) {
	from := g.find(s.obj)
	if from == "" || s.src == "" {
		return
	}
	for _, parts := range sqlNames(s.src) {
		names := []string{strings.Join(parts, "."), parts[0]}
		if len(parts) > 1 && g.schemas[parts[0]] {
			names = append(names, parts[1])
		}
		for _, name := range names {
			for _, id := range g.byName[name] {
				if t, _, _ := strings.Cut(id, ":"); relScanTo[t] {
					g.link(from, id, "uses", "", true)
				}
			}
		}
	}
}

// sqlNames are the names in SQL source, lower case and split at their dots:
// plain or quoted ("x", [x], `x`), also in strings (nextval('seq')), not in
// comments. Variables (:x, @x) are skipped.
func sqlNames(src string) [][]string {
	seen := map[string]bool{}
	var out [][]string
	var parts []string // the dotted name being read
	flush := func() {
		if key := strings.ToLower(strings.Join(parts, ".")); len(parts) > 0 && parts[0] != "" && !seen[key] {
			seen[key] = true
			out = append(out, strings.Split(key, "."))
		}
		parts = parts[:0]
	}
	rs := []rune(src)
	ident := func(r rune) bool {
		return r == '_' || r == '$' || r == '#' || unicode.IsLetter(r) || unicode.IsDigit(r)
	}
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case r == '-' && i+1 < len(rs) && rs[i+1] == '-':
			flush()
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
		case r == '/' && i+1 < len(rs) && rs[i+1] == '*':
			flush()
			i += 2
			for i+1 < len(rs) && !(rs[i] == '*' && rs[i+1] == '/') {
				i++
			}
			i += 2
		case r == '"' || r == '`' || r == '[':
			end := map[rune]rune{'"': '"', '`': '`', '[': ']'}[r]
			j := i + 1
			for j < len(rs) && rs[j] != end {
				j++
			}
			parts = append(parts, strings.ReplaceAll(string(rs[i+1:min(j, len(rs))]), ".", "\x00"))
			i = j + 1
			if i >= len(rs) || rs[i] != '.' {
				flush()
			}
		case r == '\'':
			flush()
			j := i + 1
			for j < len(rs) && rs[j] != '\'' {
				j++
			}
			for _, n := range sqlNames(string(rs[i+1 : min(j, len(rs))])) {
				if key := strings.Join(n, "."); !seen[key] {
					seen[key] = true
					out = append(out, n)
				}
			}
			i = j + 1
		case r == ':' || r == '@':
			flush()
			i++
			for i < len(rs) && ident(rs[i]) {
				i++
			}
		case ident(r) && !unicode.IsDigit(r):
			j := i
			for j < len(rs) && ident(rs[j]) {
				j++
			}
			parts = append(parts, string(rs[i:j]))
			i = j
			if i >= len(rs) || rs[i] != '.' {
				flush()
			}
		case r == '.' && len(parts) > 0:
			i++
		default:
			flush()
			for i++; unicode.IsDigit(r) && i < len(rs) && ident(rs[i]); i++ {
			}
		}
	}
	flush()
	return out
}

// relRows runs a catalog query; a failure becomes a note (an older server or
// missing privileges), the rest of the relationships are still drawn.
func (g *relGraph) relRows(ctx context.Context, db *sql.DB, what, q string, each func(cols []sql.NullString), args ...any) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		g.notes = append(g.notes, fmt.Sprintf("The %s couldn't be read: %v", what, firstLine(err.Error())))
		return
	}
	defer rows.Close()
	cs, _ := rows.Columns()
	for rows.Next() {
		vals := make([]sql.NullString, len(cs))
		ptrs := make([]any, len(cs))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			g.notes = append(g.notes, fmt.Sprintf("The %s couldn't be read: %v", what, firstLine(err.Error())))
			return
		}
		for i := range vals {
			vals[i].String = strings.TrimSpace(vals[i].String)
		}
		each(vals)
	}
	if err := rows.Err(); err != nil {
		g.notes = append(g.notes, fmt.Sprintf("The %s couldn't be read: %v", what, firstLine(err.Error())))
	}
}

// foreignKeys reads rows of child table, parent table, constraint, child
// column and parent column, in column order.
func (g *relGraph) foreignKeys(ctx context.Context, db *sql.DB, q string, args ...any) {
	type fk struct {
		from, to     string
		fcols, tcols []string
	}
	var order []string
	fks := map[string]*fk{}
	g.relRows(ctx, db, "foreign keys", q, func(c []sql.NullString) {
		key := c[0].String + "\x00" + c[2].String
		f := fks[key]
		if f == nil {
			f = &fk{from: c[0].String, to: c[1].String}
			fks[key] = f
			order = append(order, key)
		}
		f.fcols, f.tcols = append(f.fcols, c[3].String), append(f.tcols, c[4].String)
	}, args...)
	for _, k := range order {
		f := fks[k]
		label := strings.Join(f.fcols, ", ")
		if t := strings.Join(slices.DeleteFunc(f.tcols, func(s string) bool { return s == "" }), ", "); t != "" {
			label += " → " + t
		}
		g.add(relOf(f.from, ObjTable), relOf(f.to, ObjTable), "fk", label)
	}
}

func (g *relGraph) sources(ctx context.Context, db *sql.DB, what, q string, obj func(c []sql.NullString) relSource, args ...any) {
	g.relRows(ctx, db, what, q, func(c []sql.NullString) { g.scan(obj(c)) }, args...)
}

// Firebird's RDB$DEPENDENCIES types, as object types.
var fbDepTypes = map[string][]string{
	"0": {ObjTable, ObjView}, "1": {ObjView}, "2": {ObjTrigger}, "3": {ObjTable}, "5": {ObjProcedure}, "7": {ObjException},
	"9": {ObjDomain}, "14": {ObjSequence}, "15": {ObjFunction}, "18": {ObjPackage}, "19": {ObjPackage},
}

// Oracle's object types, as cactusdb's.
var oraDepTypes = map[string][]string{
	"TABLE": {ObjTable}, "VIEW": {ObjView}, "MATERIALIZED VIEW": {ObjMatView, ObjTable}, "PROCEDURE": {ObjProcedure},
	"FUNCTION": {ObjFunction}, "PACKAGE": {ObjPackage}, "PACKAGE BODY": {ObjPackage}, "TRIGGER": {ObjTrigger},
	"SEQUENCE": {ObjSequence}, "SYNONYM": {ObjSynonym}, "TYPE": {ObjDomain}, "TYPE BODY": {ObjDomain},
}

var pgRelKinds = map[string][]string{"r": {ObjTable}, "p": {ObjTable}, "v": {ObjView}, "m": {ObjMatView}, "S": {ObjSequence}}

func relationships(ctx context.Context, ss *session) (*relGraph, error) {
	groups, err := listObjects(ctx, ss.engine, ss.db)
	if err != nil {
		return nil, err
	}
	g := newRelGraph(groups)
	db := ss.db
	if backendOf(db) != nil {
		g.notes = append(g.notes, fmt.Sprintf("%s keeps no relationships between its objects: they are drawn on their own.", engineName(ss.kind())))
		return g, nil
	}
	switch ss.engine {
	case EngineFirebird:
		g.foreignKeys(ctx, db, `SELECT TRIM(rc.RDB$RELATION_NAME), TRIM(pk.RDB$RELATION_NAME), TRIM(rc.RDB$CONSTRAINT_NAME),
			TRIM(s1.RDB$FIELD_NAME), TRIM(s2.RDB$FIELD_NAME)
			FROM RDB$RELATION_CONSTRAINTS rc
			JOIN RDB$REF_CONSTRAINTS r ON r.RDB$CONSTRAINT_NAME = rc.RDB$CONSTRAINT_NAME
			JOIN RDB$RELATION_CONSTRAINTS pk ON pk.RDB$CONSTRAINT_NAME = r.RDB$CONST_NAME_UQ
			JOIN RDB$INDEX_SEGMENTS s1 ON s1.RDB$INDEX_NAME = rc.RDB$INDEX_NAME
			LEFT JOIN RDB$INDEX_SEGMENTS s2 ON s2.RDB$INDEX_NAME = pk.RDB$INDEX_NAME AND s2.RDB$FIELD_POSITION = s1.RDB$FIELD_POSITION
			WHERE rc.RDB$CONSTRAINT_TYPE = 'FOREIGN KEY' ORDER BY rc.RDB$CONSTRAINT_NAME, s1.RDB$FIELD_POSITION`)
		g.relRows(ctx, db, "triggers", `SELECT TRIM(RDB$TRIGGER_NAME), TRIM(RDB$RELATION_NAME) FROM RDB$TRIGGERS
			WHERE COALESCE(RDB$SYSTEM_FLAG, 0) = 0 AND RDB$RELATION_NAME IS NOT NULL`, func(c []sql.NullString) {
			g.add(relOf(c[0].String, ObjTrigger), relOf(c[1].String, ObjTable, ObjView), "trigger", "")
		})
		g.relRows(ctx, db, "dependencies", `SELECT DISTINCT TRIM(RDB$DEPENDENT_NAME), RDB$DEPENDENT_TYPE,
			TRIM(RDB$DEPENDED_ON_NAME), RDB$DEPENDED_ON_TYPE FROM RDB$DEPENDENCIES`, func(c []sql.NullString) {
			ft, tt := fbDepTypes[c[1].String], fbDepTypes[c[3].String]
			if ft != nil && tt != nil && c[3].String != "9" {
				g.add(relOf(c[0].String, ft...), relOf(c[2].String, tt...), "uses", "")
			}
		})
		g.relRows(ctx, db, "domains of the columns", `SELECT DISTINCT TRIM(RDB$RELATION_NAME), TRIM(RDB$FIELD_SOURCE)
			FROM RDB$RELATION_FIELDS WHERE RDB$FIELD_SOURCE NOT STARTING WITH 'RDB$' AND COALESCE(RDB$SYSTEM_FLAG, 0) = 0`, func(c []sql.NullString) {
			g.add(relOf(c[0].String, ObjTable, ObjView), relOf(c[1].String, ObjDomain), "uses", "")
		})
	case EngineMySQL, EngineMariaDB:
		var dbName sql.NullString
		db.QueryRowContext(ctx, `SELECT DATABASE()`).Scan(&dbName)
		g.schemas[strings.ToLower(dbName.String)] = true
		g.foreignKeys(ctx, db, `SELECT table_name, referenced_table_name, constraint_name, column_name, referenced_column_name
			FROM information_schema.key_column_usage WHERE table_schema = DATABASE() AND referenced_table_schema = DATABASE()
			AND referenced_table_name IS NOT NULL ORDER BY table_name, constraint_name, ordinal_position`)
		g.relRows(ctx, db, "triggers", `SELECT trigger_name, event_object_table, action_statement FROM information_schema.triggers
			WHERE trigger_schema = DATABASE()`, func(c []sql.NullString) {
			g.add(relOf(c[0].String, ObjTrigger), relOf(c[1].String, ObjTable), "trigger", "")
			g.scan(relSource{relOf(c[0].String, ObjTrigger), c[2].String})
		})
		g.sources(ctx, db, "views", `SELECT table_name, view_definition FROM information_schema.views WHERE table_schema = DATABASE()`,
			func(c []sql.NullString) relSource { return relSource{relOf(c[0].String, ObjView), c[1].String} })
		g.sources(ctx, db, "procedures and functions", `SELECT routine_name, routine_type, routine_definition FROM information_schema.routines
			WHERE routine_schema = DATABASE()`, func(c []sql.NullString) relSource {
			return relSource{relOf(c[0].String, cmp.Or(map[string]string{"FUNCTION": ObjFunction}[c[1].String], ObjProcedure)), c[2].String}
		})
		g.sources(ctx, db, "events", `SELECT event_name, event_definition FROM information_schema.events WHERE event_schema = DATABASE()`,
			func(c []sql.NullString) relSource { return relSource{relOf(c[0].String, ObjEvent), c[1].String} })
		if ss.engine == EngineMariaDB || hasSequences(groups) {
			g.sources(ctx, db, "column defaults", `SELECT table_name, column_default FROM information_schema.columns
				WHERE table_schema = DATABASE() AND column_default LIKE '%nextval%'`,
				func(c []sql.NullString) relSource { return relSource{relOf(c[0].String, ObjTable), c[1].String} })
		}
	case EngineSQLite:
		g.foreignKeys(ctx, db, `SELECT m.name, p."table", p.id, p."from", COALESCE(p."to", '') FROM sqlite_master m
			JOIN pragma_foreign_key_list(m.name) p WHERE m.type = 'table' ORDER BY m.name, p.id, p.seq`)
		g.relRows(ctx, db, "triggers", `SELECT name, tbl_name, sql FROM sqlite_master WHERE type = 'trigger'`, func(c []sql.NullString) {
			g.add(relOf(c[0].String, ObjTrigger), relOf(c[1].String, ObjTable, ObjView), "trigger", "")
			g.scan(relSource{relOf(c[0].String, ObjTrigger), sqliteBody(c[2].String, "ON")})
		})
		g.sources(ctx, db, "views", `SELECT name, sql FROM sqlite_master WHERE type = 'view'`,
			func(c []sql.NullString) relSource {
				return relSource{relOf(c[0].String, ObjView), sqliteBody(c[1].String, "AS")}
			})
	case EnginePostgres:
		pgRelationships(ctx, db, g)
	case EngineMSSQL:
		g.foreignKeys(ctx, db, `SELECT OBJECT_SCHEMA_NAME(fk.parent_object_id) + '.' + OBJECT_NAME(fk.parent_object_id),
			OBJECT_SCHEMA_NAME(fk.referenced_object_id) + '.' + OBJECT_NAME(fk.referenced_object_id), fk.name,
			COL_NAME(c.parent_object_id, c.parent_column_id), COL_NAME(c.referenced_object_id, c.referenced_column_id)
			FROM sys.foreign_keys fk JOIN sys.foreign_key_columns c ON c.constraint_object_id = fk.object_id
			ORDER BY fk.name, c.constraint_column_id`)
		g.relRows(ctx, db, "triggers", `SELECT COALESCE(OBJECT_SCHEMA_NAME(object_id) + '.', '') + name,
			OBJECT_SCHEMA_NAME(parent_id) + '.' + OBJECT_NAME(parent_id) FROM sys.triggers WHERE parent_class = 1 AND is_ms_shipped = 0`,
			func(c []sql.NullString) {
				g.add(relOf(c[0].String, ObjTrigger), relOf(c[1].String, ObjTable, ObjView), "trigger", "")
			})
		// Defaults and checks count for their table; names are unique in a schema.
		g.relRows(ctx, db, "dependencies", `SELECT COALESCE(OBJECT_SCHEMA_NAME(o.object_id) + '.', '') + o.name,
			COALESCE(d.referenced_schema_name, OBJECT_SCHEMA_NAME(d.referenced_id), OBJECT_SCHEMA_NAME(o.object_id), 'dbo') + '.' + d.referenced_entity_name
			FROM sys.sql_expression_dependencies d JOIN sys.objects x ON x.object_id = d.referencing_id
			JOIN sys.objects o ON o.object_id = CASE WHEN x.type IN ('D', 'C') THEN x.parent_object_id ELSE x.object_id END
			WHERE d.referencing_class = 1 AND d.referenced_class = 1 AND d.referenced_database_name IS NULL AND d.referenced_server_name IS NULL`,
			func(c []sql.NullString) { g.add(relOf(c[0].String), relOf(c[1].String), "uses", "") })
	case EngineOracle:
		owner, err := oracleOwner(ctx, db)
		if err != nil {
			return nil, err
		}
		g.foreignKeys(ctx, db, `SELECT c.owner || '.' || c.table_name, r.owner || '.' || r.table_name, c.constraint_name, cc.column_name, rc.column_name
			FROM all_constraints c JOIN all_constraints r ON r.owner = c.r_owner AND r.constraint_name = c.r_constraint_name
			JOIN all_cons_columns cc ON cc.owner = c.owner AND cc.constraint_name = c.constraint_name
			LEFT JOIN all_cons_columns rc ON rc.owner = r.owner AND rc.constraint_name = r.constraint_name AND rc.position = cc.position
			WHERE c.owner = :1 AND c.constraint_type = 'R' ORDER BY c.constraint_name, cc.position`, owner)
		g.relRows(ctx, db, "triggers", `SELECT owner || '.' || trigger_name, table_owner || '.' || table_name FROM all_triggers
			WHERE owner = :1 AND base_object_type IN ('TABLE', 'VIEW')`, func(c []sql.NullString) {
			g.add(relOf(c[0].String, ObjTrigger), relOf(c[1].String, ObjTable, ObjView), "trigger", "")
		}, owner)
		g.relRows(ctx, db, "dependencies", `SELECT owner || '.' || name, type, referenced_owner || '.' || referenced_name, referenced_type
			FROM all_dependencies WHERE owner = :1 AND referenced_owner = :1`, func(c []sql.NullString) {
			ft, tt := oraDepTypes[c[1].String], oraDepTypes[c[3].String]
			if ft != nil && tt != nil {
				g.add(relOf(c[0].String, ft...), relOf(c[2].String, tt...), "uses", "")
			}
		}, owner)
	default:
		g.notes = append(g.notes, fmt.Sprintf("The relationships of %s databases can't be read yet: the objects are drawn on their own.", engineName(ss.kind())))
	}
	return g, nil
}

func hasSequences(groups []ObjectGroup) bool {
	for _, gr := range groups {
		if gr.Type == ObjSequence && len(gr.Items) > 0 {
			return true
		}
	}
	return false
}

// sqliteBody is a CREATE statement from its first AS (a view) or ON (a
// trigger) on, so an object isn't found to use one of its own name.
func sqliteBody(sql, kw string) string {
	if loc := regexp.MustCompile(`(?i)\b` + kw + `\b`).FindStringIndex(sql); loc != nil {
		return sql[loc[0]:]
	}
	return sql
}

func pgRelationships(ctx context.Context, db *sql.DB, g *relGraph) {
	const fn = `n.nspname || '.' || p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')'`
	g.foreignKeys(ctx, db, `SELECT fn.nspname || '.' || fc.relname, tn.nspname || '.' || tc.relname, c.conname, a.attname, b.attname
		FROM pg_constraint c
		JOIN pg_class fc ON fc.oid = c.conrelid JOIN pg_namespace fn ON fn.oid = fc.relnamespace
		JOIN pg_class tc ON tc.oid = c.confrelid JOIN pg_namespace tn ON tn.oid = tc.relnamespace
		CROSS JOIN LATERAL unnest(c.conkey, c.confkey) WITH ORDINALITY AS k(f, t, i)
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.f
		JOIN pg_attribute b ON b.attrelid = c.confrelid AND b.attnum = k.t
		WHERE c.contype = 'f' ORDER BY 1, c.conname, k.i`)
	// Views and materialized views: the tables, views and sequences their query reads…
	g.relRows(ctx, db, "views", `SELECT DISTINCT vn.nspname || '.' || v.relname, v.relkind, tn.nspname || '.' || t.relname, t.relkind
		FROM pg_depend d JOIN pg_rewrite r ON r.oid = d.objid
		JOIN pg_class v ON v.oid = r.ev_class JOIN pg_namespace vn ON vn.oid = v.relnamespace
		JOIN pg_class t ON t.oid = d.refobjid JOIN pg_namespace tn ON tn.oid = t.relnamespace
		WHERE d.classid = 'pg_rewrite'::regclass AND d.refclassid = 'pg_class'::regclass AND v.oid <> t.oid`, func(c []sql.NullString) {
		g.add(relOf(c[0].String, pgRelKinds[c[1].String]...), relOf(c[2].String, pgRelKinds[c[3].String]...), "uses", "")
	})
	// …and the functions it calls.
	g.relRows(ctx, db, "functions of the views", `SELECT DISTINCT vn.nspname || '.' || v.relname, v.relkind, `+fn+`
		FROM pg_depend d JOIN pg_rewrite r ON r.oid = d.objid
		JOIN pg_class v ON v.oid = r.ev_class JOIN pg_namespace vn ON vn.oid = v.relnamespace
		JOIN pg_proc p ON p.oid = d.refobjid JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE d.classid = 'pg_rewrite'::regclass AND d.refclassid = 'pg_proc'::regclass`, func(c []sql.NullString) {
		g.add(relOf(c[0].String, pgRelKinds[c[1].String]...), relOf(c[2].String, ObjFunction, ObjProcedure), "uses", "")
	})
	g.relRows(ctx, db, "triggers", `SELECT cn.nspname || '.' || c.relname || '.' || t.tgname, cn.nspname || '.' || c.relname, `+fn+`
		FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_namespace cn ON cn.oid = c.relnamespace
		JOIN pg_proc p ON p.oid = t.tgfoid JOIN pg_namespace n ON n.oid = p.pronamespace WHERE NOT t.tgisinternal`, func(c []sql.NullString) {
		g.add(relOf(c[0].String, ObjTrigger), relOf(c[1].String, ObjTable, ObjView, ObjMatView), "trigger", "")
		g.add(relOf(c[0].String, ObjTrigger), relOf(c[2].String, ObjFunction, ObjProcedure), "uses", "")
	})
	// Sequences: owned by a column, or in a column's default.
	g.relRows(ctx, db, "sequences", `SELECT DISTINCT tn.nspname || '.' || t.relname, sn.nspname || '.' || s.relname
		FROM pg_depend d
		JOIN pg_class s ON s.oid = d.objid AND s.relkind = 'S' JOIN pg_namespace sn ON sn.oid = s.relnamespace
		JOIN pg_class t ON t.oid = d.refobjid AND t.relkind IN ('r', 'p') JOIN pg_namespace tn ON tn.oid = t.relnamespace
		WHERE d.classid = 'pg_class'::regclass AND d.refclassid = 'pg_class'::regclass
		UNION
		SELECT DISTINCT tn.nspname || '.' || t.relname, sn.nspname || '.' || s.relname
		FROM pg_depend d JOIN pg_attrdef ad ON ad.oid = d.objid
		JOIN pg_class t ON t.oid = ad.adrelid JOIN pg_namespace tn ON tn.oid = t.relnamespace
		JOIN pg_class s ON s.oid = d.refobjid AND s.relkind = 'S' JOIN pg_namespace sn ON sn.oid = s.relnamespace
		WHERE d.classid = 'pg_attrdef'::regclass AND d.refclassid = 'pg_class'::regclass`, func(c []sql.NullString) {
		g.add(relOf(c[0].String, ObjTable), relOf(c[1].String, ObjSequence), "uses", "")
	})
	// Function bodies aren't tracked by PostgreSQL: their source is read.
	const userNS = `n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg\_%'`
	before := len(g.notes)
	g.sources(ctx, db, "functions", `SELECT `+fn+`, p.prokind, p.prosrc FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE p.prokind IN ('f', 'p') AND `+userNS, func(c []sql.NullString) relSource {
		return relSource{relOf(c[0].String, map[string]string{"p": ObjProcedure}[c[1].String], ObjFunction), c[2].String}
	})
	if len(g.notes) > before { // before PostgreSQL 11: no prokind
		g.notes = g.notes[:before]
		g.sources(ctx, db, "functions", `SELECT `+fn+`, p.prosrc FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
			WHERE `+userNS, func(c []sql.NullString) relSource { return relSource{relOf(c[0].String, ObjFunction), c[1].String} })
	}
}

func (s *server) handleRelationships(w http.ResponseWriter, r *http.Request) {
	ss, err := s.session(r, r.URL.Query().Get("session"))
	if err != nil {
		writeErr(w, 401, err)
		return
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	g, err := relationships(ctx, ss)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	edges := make([]*relEdge, 0, len(g.order))
	for _, k := range g.order {
		edges = append(edges, g.edges[k])
	}
	if g.nodes == nil {
		g.nodes = []relNode{}
	}
	writeJSON(w, 200, map[string]any{"label": ss.label, "engine": ss.kind(), "nodes": g.nodes,
		"edges": edges, "labels": g.labels, "notes": g.notes, "ms": time.Since(start).Milliseconds()})
}
