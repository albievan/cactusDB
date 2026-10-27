package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Database object types shown in the sidebar.
const (
	ObjTable     = "table"
	ObjView      = "view"
	ObjMatView   = "matview"
	ObjProcedure = "procedure"
	ObjFunction  = "function"
	ObjPackage   = "package"
	ObjTrigger   = "trigger"
	ObjSequence  = "sequence"
	ObjDomain    = "domain"
	ObjException = "exception"
	ObjEvent     = "event"
	ObjIndex     = "index"
	ObjSynonym   = "synonym"
)

// ObjectGroup is one sidebar section, e.g. all stored procedures.
type ObjectGroup struct {
	Type      string   `json:"type"`
	Label     string   `json:"label"`
	Queryable bool     `json:"queryable"` // rows can be selected (tables, views)
	Items     []string `json:"items"`
}

type objectQuery struct {
	typ, label string
	queries    []string // tried in order; later ones are fallbacks for older servers
}

func objectQueries(engine string) []objectQuery {
	switch engine {
	case EngineSQLite:
		master := func(t string) []string {
			return []string{`SELECT name FROM sqlite_master WHERE type = '` + t + `' AND name NOT LIKE 'sqlite_%' AND sql IS NOT NULL ORDER BY name`}
		}
		return []objectQuery{
			{ObjTable, "Tables", master("table")},
			{ObjView, "Views", master("view")},
			{ObjTrigger, "Triggers", master("trigger")},
			{ObjIndex, "Indexes", master("index")},
		}
	case EngineMySQL, EngineMariaDB:
		tables := func(types string) []string {
			return []string{`SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() AND table_type IN (` + types + `) ORDER BY table_name`}
		}
		routines := func(t string) []string {
			return []string{`SELECT routine_name FROM information_schema.routines WHERE routine_schema = DATABASE() AND routine_type = '` + t + `' ORDER BY routine_name`}
		}
		return []objectQuery{
			{ObjTable, "Tables", tables(`'BASE TABLE', 'SYSTEM VERSIONED'`)},
			{ObjView, "Views", tables(`'VIEW'`)},
			{ObjProcedure, "Procedures", routines("PROCEDURE")},
			{ObjFunction, "Functions", routines("FUNCTION")},
			{ObjTrigger, "Triggers", []string{`SELECT trigger_name FROM information_schema.triggers WHERE trigger_schema = DATABASE() ORDER BY trigger_name`}},
			{ObjEvent, "Events", []string{`SELECT event_name FROM information_schema.events WHERE event_schema = DATABASE() ORDER BY event_name`}},
			{ObjSequence, "Sequences", tables(`'SEQUENCE'`)}, // MariaDB 10.3+
		}
	case EngineFirebird:
		user := `COALESCE(RDB$SYSTEM_FLAG, 0) = 0`
		return []objectQuery{
			{ObjTable, "Tables", []string{`SELECT TRIM(RDB$RELATION_NAME) FROM RDB$RELATIONS WHERE RDB$VIEW_BLR IS NULL AND ` + user + ` ORDER BY 1`}},
			{ObjView, "Views", []string{`SELECT TRIM(RDB$RELATION_NAME) FROM RDB$RELATIONS WHERE RDB$VIEW_BLR IS NOT NULL AND ` + user + ` ORDER BY 1`}},
			{ObjProcedure, "Procedures", []string{
				`SELECT TRIM(RDB$PROCEDURE_NAME) FROM RDB$PROCEDURES WHERE RDB$PACKAGE_NAME IS NULL AND ` + user + ` ORDER BY 1`,
				`SELECT TRIM(RDB$PROCEDURE_NAME) FROM RDB$PROCEDURES WHERE ` + user + ` ORDER BY 1`}},
			{ObjFunction, "Functions", []string{
				`SELECT TRIM(RDB$FUNCTION_NAME) FROM RDB$FUNCTIONS WHERE RDB$PACKAGE_NAME IS NULL AND ` + user + ` ORDER BY 1`,
				`SELECT TRIM(RDB$FUNCTION_NAME) FROM RDB$FUNCTIONS WHERE ` + user + ` ORDER BY 1`}},
			{ObjPackage, "Packages", []string{`SELECT TRIM(RDB$PACKAGE_NAME) FROM RDB$PACKAGES WHERE ` + user + ` ORDER BY 1`}},
			{ObjTrigger, "Triggers", []string{`SELECT TRIM(RDB$TRIGGER_NAME) FROM RDB$TRIGGERS WHERE ` + user + ` ORDER BY 1`}},
			{ObjSequence, "Generators", []string{`SELECT TRIM(RDB$GENERATOR_NAME) FROM RDB$GENERATORS WHERE ` + user + ` ORDER BY 1`}},
			{ObjDomain, "Domains", []string{`SELECT TRIM(RDB$FIELD_NAME) FROM RDB$FIELDS WHERE RDB$FIELD_NAME NOT STARTING WITH 'RDB$' AND ` + user + ` ORDER BY 1`}},
			{ObjException, "Exceptions", []string{`SELECT TRIM(RDB$EXCEPTION_NAME) FROM RDB$EXCEPTIONS WHERE ` + user + ` ORDER BY 1`}},
		}
	case EnginePostgres:
		// Objects are named schema.name; system schemas and extension members are skipped.
		const userNS = `n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg\_%'`
		const notExt = ` AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = o.oid AND d.deptype = 'e')`
		rels := func(kinds string) []string {
			return []string{`SELECT n.nspname || '.' || o.relname FROM pg_class o JOIN pg_namespace n ON n.oid = o.relnamespace
				WHERE o.relkind IN (` + kinds + `) AND ` + userNS + notExt + ` ORDER BY 1`}
		}
		procs := func(kind string) []string {
			return []string{`SELECT n.nspname || '.' || o.proname || '(' || pg_get_function_identity_arguments(o.oid) || ')'
				FROM pg_proc o JOIN pg_namespace n ON n.oid = o.pronamespace
				WHERE o.prokind = '` + kind + `' AND ` + userNS + notExt + ` ORDER BY 1`}
		}
		return []objectQuery{
			{ObjTable, "Tables", rels(`'r', 'p'`)},
			{ObjView, "Views", rels(`'v'`)},
			{ObjMatView, "Materialized views", rels(`'m'`)},
			{ObjFunction, "Functions", procs("f")},
			{ObjProcedure, "Procedures", procs("p")},
			{ObjTrigger, "Triggers", []string{`SELECT n.nspname || '.' || c.relname || '.' || o.tgname FROM pg_trigger o
				JOIN pg_class c ON c.oid = o.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace
				WHERE NOT o.tgisinternal AND ` + userNS + ` ORDER BY 1`}},
			{ObjSequence, "Sequences", rels(`'S'`)},
			{ObjDomain, "Domains", []string{`SELECT n.nspname || '.' || o.typname FROM pg_type o JOIN pg_namespace n ON n.oid = o.typnamespace
				WHERE o.typtype = 'd' AND ` + userNS + notExt + ` ORDER BY 1`}},
		}
	case EngineMSSQL:
		objs := func(from string) []string {
			return []string{`SELECT s.name + '.' + o.name FROM ` + from + ` o JOIN sys.schemas s ON s.schema_id = o.schema_id WHERE o.is_ms_shipped = 0 ORDER BY 1`}
		}
		return []objectQuery{
			{ObjTable, "Tables", objs("sys.tables")},
			{ObjView, "Views", objs("sys.views")},
			{ObjProcedure, "Procedures", objs("sys.procedures")},
			{ObjFunction, "Functions", []string{`SELECT s.name + '.' + o.name FROM sys.objects o JOIN sys.schemas s ON s.schema_id = o.schema_id
				WHERE o.type IN ('FN', 'IF', 'TF', 'FS', 'FT') AND o.is_ms_shipped = 0 ORDER BY 1`}},
			{ObjTrigger, "Triggers", []string{`SELECT COALESCE(OBJECT_SCHEMA_NAME(object_id) + '.', '') + name FROM sys.triggers WHERE is_ms_shipped = 0 ORDER BY 1`}},
			{ObjSequence, "Sequences", objs("sys.sequences")},
			{ObjSynonym, "Synonyms", objs("sys.synonyms")},
		}
	}
	return nil
}

// listObjects returns every user object of the connected database, grouped
// by type. Groups the server doesn't support (e.g. packages before
// Firebird 3) come back empty.
func listObjects(ctx context.Context, engine string, db *sql.DB) ([]ObjectGroup, error) {
	qs := objectQueries(engine)
	if qs == nil {
		return nil, fmt.Errorf("unsupported engine %q", engine)
	}
	var groups []ObjectGroup
	for i, oq := range qs {
		g := ObjectGroup{Type: oq.typ, Label: oq.label, Queryable: oq.typ == ObjTable || oq.typ == ObjView || oq.typ == ObjMatView, Items: []string{}}
		var err error
		for _, q := range oq.queries {
			var items []string
			if items, err = queryStrings(ctx, db, q); err == nil {
				g.Items = items
				break
			}
		}
		if err != nil && i == 0 { // tables are mandatory
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, nil
}

// objectDefinition returns the source / DDL of one object.
func objectDefinition(ctx context.Context, engine string, db *sql.DB, typ, name string) (string, error) {
	var src string
	var err error
	switch engine {
	case EngineSQLite:
		err = db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = ? AND name = ?`, typ, name).Scan(&src)
	case EngineMySQL, EngineMariaDB:
		src, err = mysqlDefinition(ctx, db, typ, name)
	case EngineFirebird:
		src, err = firebirdDefinition(ctx, db, typ, name)
	case EngineMSSQL:
		src, err = mssqlDefinition(ctx, db, typ, name)
	case EnginePostgres:
		src, err = postgresDefinition(ctx, db, typ, name)
	default:
		err = fmt.Errorf("unsupported engine %q", engine)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%s %s not found", typ, name)
	}
	return src, err
}

// ---- MySQL / MariaDB -------------------------------------------------------

func mysqlDefinition(ctx context.Context, db *sql.DB, typ, name string) (string, error) {
	kind := map[string]string{
		ObjTable: "TABLE", ObjView: "VIEW", ObjProcedure: "PROCEDURE", ObjFunction: "FUNCTION",
		ObjTrigger: "TRIGGER", ObjEvent: "EVENT", ObjSequence: "SEQUENCE",
	}[typ]
	if kind == "" {
		return "", fmt.Errorf("unsupported object type %q", typ)
	}
	rows, err := db.QueryContext(ctx, "SHOW CREATE "+kind+" "+quoteIdent(EngineMySQL, name))
	if err != nil {
		return "", err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	if !rows.Next() {
		return "", sql.ErrNoRows
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return "", err
	}
	for i, c := range cols {
		if strings.HasPrefix(c, "Create ") || c == "SQL Original Statement" {
			if !vals[i].Valid {
				return "", errors.New("the definition is hidden: your user lacks the privilege to view it")
			}
			return vals[i].String, nil
		}
	}
	return "", errors.New("server returned no definition")
}

// ---- Firebird --------------------------------------------------------------

// fbField describes a column, parameter or domain type from RDB$FIELDS.
type fbField struct {
	name, source                      string // column/parameter name, domain (RDB$FIELD_SOURCE)
	typ, subType, length, prec, scale sql.NullInt64
	charLen                           sql.NullInt64
	nullFlag                          sql.NullInt64
	defaultSrc, computedSrc, checkSrc sql.NullString
	paramType                         sql.NullInt64 // 0 = input, 1 = output
}

// typeName renders a Firebird data type, or the domain name for columns and
// parameters based on a user-defined domain.
func (f fbField) typeName(useDomain bool) string {
	if useDomain && f.source != "" && !strings.HasPrefix(f.source, "RDB$") {
		return f.source
	}
	n := func(v sql.NullInt64) int64 { return v.Int64 }
	length := n(f.length)
	if f.charLen.Valid {
		length = f.charLen.Int64
	}
	exact := func(base string) string {
		switch n(f.subType) {
		case 1:
			return fmt.Sprintf("NUMERIC(%d,%d)", n(f.prec), -n(f.scale))
		case 2:
			return fmt.Sprintf("DECIMAL(%d,%d)", n(f.prec), -n(f.scale))
		}
		if n(f.scale) < 0 {
			return fmt.Sprintf("NUMERIC(%d,%d)", n(f.prec), -n(f.scale))
		}
		return base
	}
	switch n(f.typ) {
	case 7:
		return exact("SMALLINT")
	case 8:
		return exact("INTEGER")
	case 16:
		return exact("BIGINT")
	case 26:
		return exact("INT128")
	case 10:
		return "FLOAT"
	case 27:
		return "DOUBLE PRECISION"
	case 12:
		return "DATE"
	case 13:
		return "TIME"
	case 35:
		return "TIMESTAMP"
	case 28:
		return "TIME WITH TIME ZONE"
	case 29:
		return "TIMESTAMP WITH TIME ZONE"
	case 23:
		return "BOOLEAN"
	case 24:
		return "DECFLOAT(16)"
	case 25:
		return "DECFLOAT(34)"
	case 14:
		return fmt.Sprintf("CHAR(%d)", length)
	case 37:
		return fmt.Sprintf("VARCHAR(%d)", length)
	case 40:
		return fmt.Sprintf("CSTRING(%d)", length)
	case 261:
		switch n(f.subType) {
		case 0:
			return "BLOB SUB_TYPE BINARY"
		case 1:
			return "BLOB SUB_TYPE TEXT"
		}
		return fmt.Sprintf("BLOB SUB_TYPE %d", n(f.subType))
	}
	return fmt.Sprintf("UNKNOWN(%d)", n(f.typ))
}

const fbFieldCols = `f.RDB$FIELD_TYPE, f.RDB$FIELD_SUB_TYPE, f.RDB$FIELD_LENGTH, f.RDB$FIELD_PRECISION,
	f.RDB$FIELD_SCALE, f.RDB$CHARACTER_LENGTH`

func (f *fbField) typePtrs() []any {
	return []any{&f.typ, &f.subType, &f.length, &f.prec, &f.scale, &f.charLen}
}

func fbString(ctx context.Context, db *sql.DB, q string, args ...any) (string, error) {
	var s sql.NullString
	err := db.QueryRowContext(ctx, q, args...).Scan(&s)
	return strings.TrimSpace(s.String), err
}

func firebirdDefinition(ctx context.Context, db *sql.DB, typ, name string) (string, error) {
	q := func(s string) string { return quoteIdent(EngineFirebird, s) }
	switch typ {
	case ObjTable:
		return tableDefinition(ctx, EngineFirebird, db, name, true)

	case ObjView:
		src, err := fbString(ctx, db, `SELECT RDB$VIEW_SOURCE FROM RDB$RELATIONS WHERE RDB$RELATION_NAME = ?`, name)
		if err != nil {
			return "", err
		}
		cols, err := listColumns(ctx, EngineFirebird, db, name)
		if err != nil {
			return "", err
		}
		for i, c := range cols {
			cols[i] = q(c)
		}
		return fmt.Sprintf("CREATE OR ALTER VIEW %s (%s)\nAS\n%s;\n", q(name), strings.Join(cols, ", "), src), nil

	case ObjProcedure:
		src, err := fbString(ctx, db, `SELECT RDB$PROCEDURE_SOURCE FROM RDB$PROCEDURES WHERE RDB$PROCEDURE_NAME = ?`, name)
		if err != nil {
			return "", err
		}
		params, _ := firebirdParams(ctx, db, `SELECT TRIM(p.RDB$PARAMETER_NAME), TRIM(p.RDB$FIELD_SOURCE), p.RDB$PARAMETER_TYPE, `+fbFieldCols+`
			FROM RDB$PROCEDURE_PARAMETERS p JOIN RDB$FIELDS f ON f.RDB$FIELD_NAME = p.RDB$FIELD_SOURCE
			WHERE p.RDB$PROCEDURE_NAME = ? ORDER BY p.RDB$PARAMETER_TYPE, p.RDB$PARAMETER_NUMBER`, name)
		var in, out []string
		for _, p := range params {
			decl := "  " + q(p.name) + " " + p.typeName(true)
			if p.paramType.Int64 == 0 {
				in = append(in, decl)
			} else {
				out = append(out, decl)
			}
		}
		var b strings.Builder
		b.WriteString("CREATE OR ALTER PROCEDURE " + q(name))
		if len(in) > 0 {
			b.WriteString(" (\n" + strings.Join(in, ",\n") + ")")
		}
		if len(out) > 0 {
			b.WriteString("\nRETURNS (\n" + strings.Join(out, ",\n") + ")")
		}
		b.WriteString("\nAS\n" + src + "\n")
		return b.String(), nil

	case ObjFunction:
		var src, entry, module sql.NullString
		err := db.QueryRowContext(ctx, `SELECT RDB$FUNCTION_SOURCE, RDB$ENTRYPOINT, RDB$MODULE_NAME FROM RDB$FUNCTIONS WHERE RDB$FUNCTION_NAME = ?`, name).
			Scan(&src, &entry, &module)
		if err != nil { // Firebird 2.5: only external UDFs, no source column
			err = db.QueryRowContext(ctx, `SELECT RDB$ENTRYPOINT, RDB$MODULE_NAME FROM RDB$FUNCTIONS WHERE RDB$FUNCTION_NAME = ?`, name).
				Scan(&entry, &module)
			if err != nil {
				return "", err
			}
		}
		if !src.Valid {
			return fmt.Sprintf("DECLARE EXTERNAL FUNCTION %s\n  ENTRY_POINT '%s' MODULE_NAME '%s';\n", q(name),
				strings.TrimSpace(entry.String), strings.TrimSpace(module.String)), nil
		}
		params, _ := firebirdParams(ctx, db, `SELECT TRIM(a.RDB$ARGUMENT_NAME), TRIM(a.RDB$FIELD_SOURCE), a.RDB$ARGUMENT_POSITION, `+fbFieldCols+`
			FROM RDB$FUNCTION_ARGUMENTS a JOIN RDB$FIELDS f ON f.RDB$FIELD_NAME = a.RDB$FIELD_SOURCE
			WHERE a.RDB$FUNCTION_NAME = ? AND a.RDB$PACKAGE_NAME IS NULL ORDER BY a.RDB$ARGUMENT_POSITION`, name)
		var args []string
		returns := ""
		for _, p := range params {
			if p.paramType.Int64 == 0 { // position 0 is the return value
				returns = p.typeName(true)
				continue
			}
			args = append(args, q(p.name)+" "+p.typeName(true))
		}
		return fmt.Sprintf("CREATE OR ALTER FUNCTION %s (%s)\nRETURNS %s\nAS\n%s\n", q(name), strings.Join(args, ", "), returns, strings.TrimSpace(src.String)), nil

	case ObjPackage:
		var head, body sql.NullString
		err := db.QueryRowContext(ctx, `SELECT RDB$PACKAGE_HEADER_SOURCE, RDB$PACKAGE_BODY_SOURCE FROM RDB$PACKAGES WHERE RDB$PACKAGE_NAME = ?`, name).
			Scan(&head, &body)
		if err != nil {
			return "", err
		}
		out := fmt.Sprintf("CREATE OR ALTER PACKAGE %s\nAS\n%s\n", q(name), strings.TrimSpace(head.String))
		if body.Valid {
			out += fmt.Sprintf("\nRECREATE PACKAGE BODY %s\nAS\n%s\n", q(name), strings.TrimSpace(body.String))
		}
		return out, nil

	case ObjTrigger:
		var rel, src sql.NullString
		var ttype, seq, inactive sql.NullInt64
		err := db.QueryRowContext(ctx, `SELECT TRIM(RDB$RELATION_NAME), RDB$TRIGGER_TYPE, RDB$TRIGGER_SEQUENCE, RDB$TRIGGER_INACTIVE, RDB$TRIGGER_SOURCE
			FROM RDB$TRIGGERS WHERE RDB$TRIGGER_NAME = ?`, name).Scan(&rel, &ttype, &seq, &inactive, &src)
		if err != nil {
			return "", err
		}
		state := "ACTIVE"
		if inactive.Int64 == 1 {
			state = "INACTIVE"
		}
		head := "CREATE OR ALTER TRIGGER " + q(name)
		if rel.Valid && rel.String != "" {
			head += " FOR " + q(rel.String)
		}
		return fmt.Sprintf("%s\n%s %s POSITION %d\n%s\n", head, state, firebirdTriggerType(ttype.Int64), seq.Int64, strings.TrimSpace(src.String)), nil

	case ObjSequence:
		var start, incr sql.NullInt64
		err := db.QueryRowContext(ctx, `SELECT RDB$INITIAL_VALUE, RDB$GENERATOR_INCREMENT FROM RDB$GENERATORS WHERE RDB$GENERATOR_NAME = ?`, name).
			Scan(&start, &incr)
		if err != nil { // Firebird 2.5 has neither column
			if err := db.QueryRowContext(ctx, `SELECT 1 FROM RDB$GENERATORS WHERE RDB$GENERATOR_NAME = ?`, name).Scan(new(int)); err != nil {
				return "", err
			}
		}
		// GEN_ID(g, 0) reads the current value without incrementing it.
		var cur sql.NullInt64
		db.QueryRowContext(ctx, `SELECT GEN_ID(`+q(name)+`, 0) FROM RDB$DATABASE`).Scan(&cur)
		out := "CREATE SEQUENCE " + q(name)
		if start.Valid {
			out += fmt.Sprintf(" START WITH %d INCREMENT BY %d", start.Int64, incr.Int64)
		}
		out += ";\n"
		if cur.Valid {
			out += fmt.Sprintf("\n-- current value: %d\n", cur.Int64)
		}
		return out, nil

	case ObjDomain:
		var f fbField
		err := db.QueryRowContext(ctx, `SELECT `+fbFieldCols+`, f.RDB$NULL_FLAG, f.RDB$DEFAULT_SOURCE, f.RDB$VALIDATION_SOURCE, f.RDB$COMPUTED_SOURCE
			FROM RDB$FIELDS f WHERE f.RDB$FIELD_NAME = ?`, name).
			Scan(append(f.typePtrs(), &f.nullFlag, &f.defaultSrc, &f.checkSrc, &f.computedSrc)...)
		if err != nil {
			return "", err
		}
		out := "CREATE DOMAIN " + q(name) + " AS " + f.typeName(false)
		if f.defaultSrc.Valid {
			out += "\n  " + strings.TrimSpace(f.defaultSrc.String)
		}
		if f.nullFlag.Int64 == 1 {
			out += "\n  NOT NULL"
		}
		if f.checkSrc.Valid {
			out += "\n  " + strings.TrimSpace(f.checkSrc.String)
		}
		return out + ";\n", nil

	case ObjException:
		msg, err := fbString(ctx, db, `SELECT RDB$MESSAGE FROM RDB$EXCEPTIONS WHERE RDB$EXCEPTION_NAME = ?`, name)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("CREATE EXCEPTION %s '%s';\n", q(name), strings.ReplaceAll(msg, "'", "''")), nil
	}
	return "", fmt.Errorf("unsupported object type %q", typ)
}

// firebirdParams runs a parameter query whose columns are name, field
// source, kind (input/output or argument position) and the fbFieldCols.
func firebirdParams(ctx context.Context, db *sql.DB, query, name string) ([]fbField, error) {
	rows, err := db.QueryContext(ctx, query, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []fbField
	for rows.Next() {
		var f fbField
		var pname, source sql.NullString
		if err := rows.Scan(append([]any{&pname, &source, &f.paramType}, f.typePtrs()...)...); err != nil {
			return nil, err
		}
		f.name, f.source = pname.String, source.String
		out = append(out, f)
	}
	return out, rows.Err()
}

func firebirdTableDDL(ctx context.Context, db *sql.DB, table string) (string, error) {
	q := func(s string) string { return quoteIdent(EngineFirebird, s) }
	// Identity columns (Firebird 3+; 0 = ALWAYS, 1 = BY DEFAULT). Older
	// versions lack the column, so an error just means there are none.
	identity := map[string]string{}
	if rows, err := db.QueryContext(ctx, `SELECT TRIM(RDB$FIELD_NAME), RDB$IDENTITY_TYPE FROM RDB$RELATION_FIELDS
		WHERE RDB$RELATION_NAME = ? AND RDB$IDENTITY_TYPE IS NOT NULL`, table); err == nil {
		for rows.Next() {
			var name string
			var typ int64
			if rows.Scan(&name, &typ) == nil {
				identity[name] = map[int64]string{0: "ALWAYS"}[typ]
				if identity[name] == "" {
					identity[name] = "BY DEFAULT"
				}
			}
		}
		rows.Close()
	}
	rows, err := db.QueryContext(ctx, `SELECT TRIM(rf.RDB$FIELD_NAME), TRIM(rf.RDB$FIELD_SOURCE), `+fbFieldCols+`,
			rf.RDB$NULL_FLAG, rf.RDB$DEFAULT_SOURCE, f.RDB$COMPUTED_SOURCE
		FROM RDB$RELATION_FIELDS rf JOIN RDB$FIELDS f ON f.RDB$FIELD_NAME = rf.RDB$FIELD_SOURCE
		WHERE rf.RDB$RELATION_NAME = ? ORDER BY rf.RDB$FIELD_POSITION`, table)
	if err != nil {
		return "", err
	}
	var lines []string
	for rows.Next() {
		var f fbField
		var cname, source sql.NullString
		if err := rows.Scan(append(append([]any{&cname, &source}, f.typePtrs()...), &f.nullFlag, &f.defaultSrc, &f.computedSrc)...); err != nil {
			rows.Close()
			return "", err
		}
		f.source = source.String
		line := "  " + q(cname.String) + " "
		if f.computedSrc.Valid {
			line += "COMPUTED BY " + strings.TrimSpace(f.computedSrc.String)
		} else {
			line += f.typeName(true)
			if kind, ok := identity[cname.String]; ok {
				line += " GENERATED " + kind + " AS IDENTITY"
			} else if f.defaultSrc.Valid {
				line += " " + strings.TrimSpace(f.defaultSrc.String)
			}
			if f.nullFlag.Int64 == 1 {
				line += " NOT NULL"
			}
		}
		lines = append(lines, line)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", sql.ErrNoRows
	}
	pk, _ := queryStringsArgs(ctx, db, `SELECT TRIM(s.RDB$FIELD_NAME) FROM RDB$RELATION_CONSTRAINTS c
		JOIN RDB$INDEX_SEGMENTS s ON s.RDB$INDEX_NAME = c.RDB$INDEX_NAME
		WHERE c.RDB$RELATION_NAME = ? AND c.RDB$CONSTRAINT_TYPE = 'PRIMARY KEY' ORDER BY s.RDB$FIELD_POSITION`, table)
	if len(pk) > 0 {
		for i, c := range pk {
			pk[i] = q(c)
		}
		lines = append(lines, "  PRIMARY KEY ("+strings.Join(pk, ", ")+")")
	}
	cons, err := firebirdConstraints(ctx, db, table)
	if err != nil {
		return "", err
	}
	lines = append(lines, cons...)
	return "CREATE TABLE " + q(table) + " (\n" + strings.Join(lines, ",\n") + "\n);\n", nil
}

func firebirdTriggerType(t int64) string {
	switch t {
	case 8192:
		return "ON CONNECT"
	case 8193:
		return "ON DISCONNECT"
	case 8194:
		return "ON TRANSACTION START"
	case 8195:
		return "ON TRANSACTION COMMIT"
	case 8196:
		return "ON TRANSACTION ROLLBACK"
	}
	if t >= 16384 {
		return "ON DDL STATEMENT"
	}
	// DML triggers: bit 0 of (type+1) is BEFORE/AFTER, then up to three
	// 2-bit action codes (1 = INSERT, 2 = UPDATE, 3 = DELETE).
	when := "BEFORE"
	if (t+1)&1 == 1 {
		when = "AFTER"
	}
	actions := []string{}
	for code := (t + 1) >> 1; code > 0; code >>= 2 {
		switch code & 3 {
		case 1:
			actions = append(actions, "INSERT")
		case 2:
			actions = append(actions, "UPDATE")
		case 3:
			actions = append(actions, "DELETE")
		}
	}
	return when + " " + strings.Join(actions, " OR ")
}

// ---- SQL Server ------------------------------------------------------------

func mssqlDefinition(ctx context.Context, db *sql.DB, typ, name string) (string, error) {
	const byName = `JOIN sys.schemas s ON s.schema_id = o.schema_id WHERE s.name + '.' + o.name = @p1`
	switch typ {
	case ObjTable:
		return tableDefinition(ctx, EngineMSSQL, db, name, true)
	case ObjView, ObjProcedure, ObjFunction:
		var def sql.NullString
		err := db.QueryRowContext(ctx, `SELECT m.definition FROM sys.objects o JOIN sys.sql_modules m ON m.object_id = o.object_id `+byName, name).Scan(&def)
		if err != nil {
			return "", err
		}
		if !def.Valid {
			return "", errors.New("the definition is encrypted or hidden: your user lacks VIEW DEFINITION permission")
		}
		return strings.TrimSpace(def.String) + "\n", nil
	case ObjTrigger:
		var def sql.NullString
		err := db.QueryRowContext(ctx, `SELECT m.definition FROM sys.triggers t JOIN sys.sql_modules m ON m.object_id = t.object_id
			WHERE COALESCE(OBJECT_SCHEMA_NAME(t.object_id) + '.', '') + t.name = @p1`, name).Scan(&def)
		if err != nil {
			return "", err
		}
		if !def.Valid {
			return "", errors.New("the definition is encrypted or hidden: your user lacks VIEW DEFINITION permission")
		}
		return strings.TrimSpace(def.String) + "\n", nil
	case ObjSequence:
		var typName, start, incr, min, max, cur sql.NullString
		var cycle bool
		err := db.QueryRowContext(ctx, `SELECT TYPE_NAME(o.user_type_id), CONVERT(varchar(40), o.start_value), CONVERT(varchar(40), o.increment),
				CONVERT(varchar(40), o.minimum_value), CONVERT(varchar(40), o.maximum_value), o.is_cycling, CONVERT(varchar(40), o.current_value)
			FROM sys.sequences o `+byName, name).Scan(&typName, &start, &incr, &min, &max, &cycle, &cur)
		if err != nil {
			return "", err
		}
		out := fmt.Sprintf("CREATE SEQUENCE %s AS %s\n  START WITH %s\n  INCREMENT BY %s\n  MINVALUE %s\n  MAXVALUE %s\n",
			quoteObject(EngineMSSQL, name), typName.String, start.String, incr.String, min.String, max.String)
		if cycle {
			out += "  CYCLE"
		} else {
			out += "  NO CYCLE"
		}
		return out + ";\n\n-- current value: " + cur.String + "\n", nil
	case ObjSynonym:
		var base string
		err := db.QueryRowContext(ctx, `SELECT o.base_object_name FROM sys.synonyms o `+byName, name).Scan(&base)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("CREATE SYNONYM %s FOR %s;\n", quoteObject(EngineMSSQL, name), base), nil
	}
	return "", fmt.Errorf("unsupported object type %q", typ)
}

func mssqlTableDDL(ctx context.Context, db *sql.DB, table string) (string, error) {
	rows, err := db.QueryContext(ctx, `SELECT c.name, TYPE_NAME(c.user_type_id), c.max_length, c.precision, c.scale, c.is_nullable,
			c.is_identity, CONVERT(varchar(40), ic.seed_value), CONVERT(varchar(40), ic.increment_value), dc.definition, cc.definition
		FROM sys.columns c
		JOIN sys.tables o ON o.object_id = c.object_id
		JOIN sys.schemas s ON s.schema_id = o.schema_id
		LEFT JOIN sys.identity_columns ic ON ic.object_id = c.object_id AND ic.column_id = c.column_id
		LEFT JOIN sys.default_constraints dc ON dc.object_id = c.default_object_id
		LEFT JOIN sys.computed_columns cc ON cc.object_id = c.object_id AND cc.column_id = c.column_id
		WHERE s.name + '.' + o.name = @p1 ORDER BY c.column_id`, table)
	if err != nil {
		return "", err
	}
	var lines []string
	for rows.Next() {
		var name, typ string
		var maxLen, prec, scale int64
		var nullable, identity bool
		var seed, incr, def, computed sql.NullString
		if err := rows.Scan(&name, &typ, &maxLen, &prec, &scale, &nullable, &identity, &seed, &incr, &def, &computed); err != nil {
			rows.Close()
			return "", err
		}
		line := "  " + quoteIdent(EngineMSSQL, name) + " "
		if computed.Valid {
			line += "AS " + computed.String
		} else {
			line += mssqlTypeName(typ, maxLen, prec, scale)
			if identity {
				line += fmt.Sprintf(" IDENTITY(%s,%s)", seed.String, incr.String)
			}
			if def.Valid {
				line += " DEFAULT " + def.String
			}
			if nullable {
				line += " NULL"
			} else {
				line += " NOT NULL"
			}
		}
		lines = append(lines, line)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", sql.ErrNoRows
	}
	pk, _ := queryStringsArgs(ctx, db, `SELECT c.name FROM sys.indexes i
		JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
		JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
		JOIN sys.tables o ON o.object_id = i.object_id
		JOIN sys.schemas s ON s.schema_id = o.schema_id
		WHERE i.is_primary_key = 1 AND s.name + '.' + o.name = @p1 ORDER BY ic.key_ordinal`, table)
	if len(pk) > 0 {
		for i, c := range pk {
			pk[i] = quoteIdent(EngineMSSQL, c)
		}
		lines = append(lines, "  PRIMARY KEY ("+strings.Join(pk, ", ")+")")
	}
	cons, err := mssqlConstraints(ctx, db, table)
	if err != nil {
		return "", err
	}
	lines = append(lines, cons...)
	return "CREATE TABLE " + quoteObject(EngineMSSQL, table) + " (\n" + strings.Join(lines, ",\n") + "\n);\n", nil
}

func mssqlTypeName(typ string, maxLen, prec, scale int64) string {
	size := func(n int64) string {
		if n == -1 {
			return "MAX"
		}
		return strconv.FormatInt(n, 10)
	}
	switch strings.ToLower(typ) {
	case "varchar", "char", "varbinary", "binary":
		return typ + "(" + size(maxLen) + ")"
	case "nvarchar", "nchar":
		if maxLen == -1 {
			return typ + "(MAX)"
		}
		return typ + "(" + strconv.FormatInt(maxLen/2, 10) + ")"
	case "decimal", "numeric":
		return fmt.Sprintf("%s(%d,%d)", typ, prec, scale)
	case "datetime2", "time", "datetimeoffset":
		return fmt.Sprintf("%s(%d)", typ, scale)
	}
	return typ
}

func queryStringsArgs(ctx context.Context, db *sql.DB, q string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return scanStrings(rows)
}

// ---- PostgreSQL ------------------------------------------------------------

// pgRel is the WHERE clause matching a relation by its "schema.name".
const pgRel = `FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname || '.' || c.relname = $1`

func postgresDefinition(ctx context.Context, db *sql.DB, typ, name string) (string, error) {
	q := func(s string) string { return quoteObject(EnginePostgres, s) }
	one := func(query string) (string, error) {
		var s sql.NullString
		err := db.QueryRowContext(ctx, query, name).Scan(&s)
		return s.String, err
	}
	switch typ {
	case ObjTable:
		return tableDefinition(ctx, EnginePostgres, db, name, true)
	case ObjView:
		def, err := one(`SELECT pg_get_viewdef(c.oid, true) ` + pgRel)
		return "CREATE OR REPLACE VIEW " + q(name) + " AS\n" + strings.TrimSpace(def) + "\n", err
	case ObjMatView:
		def, err := one(`SELECT pg_get_viewdef(c.oid, true) ` + pgRel)
		return "CREATE MATERIALIZED VIEW " + q(name) + " AS\n" + strings.TrimSpace(def) + "\n", err
	case ObjFunction, ObjProcedure:
		def, err := one(`SELECT pg_get_functiondef(p.oid) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
			WHERE n.nspname || '.' || p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')' = $1`)
		return strings.TrimSpace(def) + "\n", err
	case ObjTrigger:
		def, err := one(`SELECT pg_get_triggerdef(t.oid, true) FROM pg_trigger t
			JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname || '.' || c.relname || '.' || t.tgname = $1`)
		return strings.TrimSpace(def) + ";\n", err
	case ObjSequence:
		var typName, start, incr, min, max, cache, last sql.NullString
		var cycle bool
		// pg_sequences.last_value is read without advancing the sequence.
		err := db.QueryRowContext(ctx, `SELECT data_type::text, start_value::text, increment_by::text, min_value::text,
				max_value::text, cache_size::text, cycle, last_value::text
			FROM pg_sequences WHERE schemaname || '.' || sequencename = $1`, name).
			Scan(&typName, &start, &incr, &min, &max, &cache, &cycle, &last)
		if err != nil {
			return "", err
		}
		out := fmt.Sprintf("CREATE SEQUENCE %s AS %s\n  START WITH %s\n  INCREMENT BY %s\n  MINVALUE %s\n  MAXVALUE %s\n  CACHE %s\n",
			q(name), typName.String, start.String, incr.String, min.String, max.String, cache.String)
		if cycle {
			out += "  CYCLE;\n"
		} else {
			out += "  NO CYCLE;\n"
		}
		if last.Valid {
			return out + "\n-- last value: " + last.String + "\n", nil
		}
		return out + "\n-- not used yet\n", nil
	case ObjDomain:
		var base string
		var notNull bool
		var def sql.NullString
		err := db.QueryRowContext(ctx, `SELECT format_type(t.typbasetype, t.typtypmod), t.typnotnull, t.typdefault
			FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace WHERE n.nspname || '.' || t.typname = $1`, name).
			Scan(&base, &notNull, &def)
		if err != nil {
			return "", err
		}
		out := "CREATE DOMAIN " + q(name) + " AS " + base
		if def.Valid {
			out += "\n  DEFAULT " + def.String
		}
		if notNull {
			out += "\n  NOT NULL"
		}
		checks, _ := queryStringsArgs(ctx, db, `SELECT 'CONSTRAINT ' || quote_ident(con.conname) || ' ' || pg_get_constraintdef(con.oid)
			FROM pg_constraint con JOIN pg_type t ON t.oid = con.contypid JOIN pg_namespace n ON n.oid = t.typnamespace
			WHERE n.nspname || '.' || t.typname = $1 AND con.contype = 'c' ORDER BY con.conname`, name)
		for _, c := range checks {
			out += "\n  " + c
		}
		return out + ";\n", nil
	}
	return "", fmt.Errorf("unsupported object type %q", typ)
}

// postgresTableDDL builds CREATE TABLE with its constraints; foreign keys
// only when fks is set.
func postgresTableDDL(ctx context.Context, db *sql.DB, table string, fks bool) (string, error) {
	rows, err := db.QueryContext(ctx, `SELECT a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull,
			pg_get_expr(d.adbin, d.adrelid), a.attidentity, a.attgenerated
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE n.nspname || '.' || c.relname = $1 AND a.attnum > 0 AND NOT a.attisdropped ORDER BY a.attnum`, table)
	if err != nil {
		return "", err
	}
	var lines []string
	for rows.Next() {
		var col, typ, identity, generated string
		var notNull bool
		var def sql.NullString
		if err := rows.Scan(&col, &typ, &notNull, &def, &identity, &generated); err != nil {
			rows.Close()
			return "", err
		}
		line := "  " + quoteIdent(EnginePostgres, col) + " " + typ
		switch {
		case generated == "s":
			line += " GENERATED ALWAYS AS (" + def.String + ") STORED"
		case identity == "a":
			line += " GENERATED ALWAYS AS IDENTITY"
		case identity == "d":
			line += " GENERATED BY DEFAULT AS IDENTITY"
		case def.Valid:
			line += " DEFAULT " + def.String
		}
		if notNull {
			line += " NOT NULL"
		}
		lines = append(lines, line)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", sql.ErrNoRows
	}
	cons, _ := queryStringsArgs(ctx, db, `SELECT 'CONSTRAINT ' || quote_ident(con.conname) || ' ' || pg_get_constraintdef(con.oid)
		FROM pg_constraint con JOIN pg_class c ON c.oid = con.conrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname || '.' || c.relname = $1 AND (con.contype IN ('p', 'u', 'c') OR (con.contype = 'f' AND $2))
		ORDER BY CASE con.contype WHEN 'p' THEN 0 WHEN 'u' THEN 1 WHEN 'f' THEN 2 ELSE 3 END, con.conname`, table, fks)
	for _, c := range cons {
		lines = append(lines, "  "+c)
	}
	return "CREATE TABLE " + quoteObject(EnginePostgres, table) + " (\n" + strings.Join(lines, ",\n") + "\n);\n", nil
}
