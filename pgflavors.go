package main

import (
	"context"
	"database/sql"
	"strings"
)

// The PostgreSQL-compatible engines use PostgreSQL's code, with these
// differences in their catalogs.

// cockroachObjectQueries are PostgreSQL's, without CockroachDB's own schemas.
func cockroachObjectQueries() []objectQuery {
	qs := objectQueries(EnginePostgres)
	for i := range qs {
		for j, q := range qs[i].queries {
			qs[i].queries[j] = strings.ReplaceAll(q, `n.nspname NOT LIKE 'pg\_%'`,
				`n.nspname NOT LIKE 'pg\_%' AND n.nspname NOT IN ('crdb_internal')`)
		}
	}
	return qs
}

// cockroachDefinition uses SHOW CREATE, which has CockroachDB's own syntax
// (e.g. its index and family clauses). "" with no error: use PostgreSQL's.
func cockroachDefinition(ctx context.Context, db *sql.DB, typ, name string) (string, error) {
	var what, obj string
	switch typ {
	case ObjTable, ObjView, ObjMatView, ObjSequence:
		what, obj = "", quoteObject(EnginePostgres, name)
	case ObjFunction, ObjProcedure:
		what = map[string]string{ObjFunction: "FUNCTION ", ObjProcedure: "PROCEDURE "}[typ]
		n, _, _ := strings.Cut(name, "(")
		obj = quoteObject(EnginePostgres, n)
	default:
		return "", nil
	}
	rows, err := db.QueryContext(ctx, "SHOW CREATE "+what+obj)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n, stmt sql.NullString
		if err := rows.Scan(&n, &stmt); err != nil {
			return "", err
		}
		out = append(out, strings.TrimSpace(stmt.String))
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(out) == 0 {
		return "", sql.ErrNoRows
	}
	return strings.Join(out, ";\n\n") + ";\n", nil
}

// Redshift is based on PostgreSQL 8.0: it has its own system views and
// SHOW TABLE / VIEW / PROCEDURE for the DDL.
func redshiftNS(col string) string {
	return col + ` NOT IN ('pg_catalog', 'information_schema') AND ` + col + ` NOT LIKE 'pg\_%'`
}

func redshiftObjectQueries() []objectQuery {
	procs := func(kind string) []string {
		return []string{`SELECT n.nspname || '.' || p.proname || '(' || oidvectortypes(p.proargtypes) || ')'
			FROM pg_proc_info p JOIN pg_namespace n ON n.oid = p.pronamespace
			WHERE p.prokind = '` + kind + `' AND ` + redshiftNS("n.nspname") + ` ORDER BY 1`}
	}
	return []objectQuery{
		{ObjTable, "Tables", []string{
			`SELECT schemaname || '.' || tablename FROM pg_tables WHERE ` + redshiftNS("schemaname") + `
			 UNION SELECT schemaname || '.' || tablename FROM svv_external_tables ORDER BY 1`,
			`SELECT schemaname || '.' || tablename FROM pg_tables WHERE ` + redshiftNS("schemaname") + ` ORDER BY 1`}},
		{ObjView, "Views", []string{`SELECT schemaname || '.' || viewname FROM pg_views WHERE ` + redshiftNS("schemaname") + `
			AND schemaname || '.' || viewname NOT IN (SELECT schema || '.' || name FROM stv_mv_info) ORDER BY 1`,
			`SELECT schemaname || '.' || viewname FROM pg_views WHERE ` + redshiftNS("schemaname") + ` ORDER BY 1`}},
		{ObjMatView, "Materialized views", []string{`SELECT TRIM(schema) || '.' || TRIM(name) FROM stv_mv_info ORDER BY 1`}},
		{ObjProcedure, "Procedures", procs("p")},
		{ObjFunction, "Functions", procs("f")},
	}
}

func redshiftDefinition(ctx context.Context, db *sql.DB, typ, name string) (string, error) {
	var stmt string
	switch typ {
	case ObjTable:
		stmt = "SHOW TABLE " + quoteObject(EnginePostgres, name)
	case ObjView, ObjMatView:
		stmt = "SHOW VIEW " + quoteObject(EnginePostgres, name)
	case ObjProcedure:
		n, args, _ := strings.Cut(name, "(")
		stmt = "SHOW PROCEDURE " + quoteObject(EnginePostgres, n) + "(" + args
	case ObjFunction:
		var lang, src sql.NullString
		err := db.QueryRowContext(ctx, `SELECT l.lanname, p.prosrc FROM pg_proc_info p
			JOIN pg_namespace n ON n.oid = p.pronamespace JOIN pg_language l ON l.oid = p.prolang
			WHERE n.nspname || '.' || p.proname || '(' || oidvectortypes(p.proargtypes) || ')' = $1`, name).Scan(&lang, &src)
		if err != nil {
			return "", err
		}
		return "-- function " + name + ", language " + lang.String + "\n" + strings.TrimSpace(src.String) + "\n", nil
	default:
		return "", nil
	}
	rows, err := db.QueryContext(ctx, stmt)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var line sql.NullString
		if err := rows.Scan(&line); err != nil {
			return "", err
		}
		b.WriteString(line.String)
		b.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if b.Len() == 0 {
		return "", sql.ErrNoRows
	}
	return b.String(), nil
}

// pgvectorNote is added to the server version of a pgvector connection: the
// extension's version, or that it isn't installed in this database.
func pgvectorNote(ctx context.Context, db *sql.DB) string {
	var v sql.NullString
	if db.QueryRowContext(ctx, `SELECT extversion FROM pg_extension WHERE extname = 'vector'`).Scan(&v) != nil || !v.Valid {
		return " · pgvector isn't installed in this database (CREATE EXTENSION vector)"
	}
	return " · pgvector " + v.String
}
