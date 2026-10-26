package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Table definitions beyond the column list: unique and check constraints,
// plain indexes and foreign keys, for the engines whose CREATE TABLE text
// cactusdb builds itself (Firebird, SQL Server, PostgreSQL). SQLite and
// MySQL/MariaDB keep their own complete CREATE TABLE text.

// tableDefinition returns a table's CREATE TABLE statement followed by its
// indexes. With fks the foreign keys are included too; without, the caller
// adds foreignKeyDDL later, once every referenced table exists.
func tableDefinition(ctx context.Context, engine string, db *sql.DB, table string, fks bool) (string, error) {
	var src string
	var err error
	switch engine {
	case EngineFirebird:
		src, err = firebirdTableDDL(ctx, db, table)
	case EngineMSSQL:
		src, err = mssqlTableDDL(ctx, db, table)
	case EnginePostgres:
		src, err = postgresTableDDL(ctx, db, table, fks)
	default:
		return objectDefinition(ctx, engine, db, ObjTable, table)
	}
	if err != nil {
		return "", err
	}
	idx, err := indexDDL(ctx, engine, db, table)
	if err != nil {
		return "", err
	}
	if len(idx) > 0 {
		src += "\n" + strings.Join(idx, "\n") + "\n"
	}
	if fks && engine != EnginePostgres { // PostgreSQL has them inline
		fk, err := foreignKeyDDL(ctx, engine, db, table)
		if err != nil {
			return "", err
		}
		if len(fk) > 0 {
			src += "\n" + strings.Join(fk, "\n") + "\n"
		}
	}
	return src, nil
}

// deferredFKs reports whether a DDL script should add an engine's foreign
// keys after all tables rather than inside CREATE TABLE.
func deferredFKs(engine string) bool {
	return engine == EngineFirebird || engine == EngineMSSQL || engine == EnginePostgres
}

// indexDDL returns CREATE INDEX statements for a table's indexes that don't
// belong to a primary key, unique or foreign key constraint.
func indexDDL(ctx context.Context, engine string, db *sql.DB, table string) ([]string, error) {
	switch engine {
	case EnginePostgres:
		defs, err := queryStringsArgs(ctx, db, `SELECT pg_get_indexdef(i.indexrelid) || ';' FROM pg_index i
			JOIN pg_class c ON c.oid = i.indrelid JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname || '.' || c.relname = $1
			  AND NOT EXISTS (SELECT 1 FROM pg_constraint con WHERE con.conindid = i.indexrelid AND con.conrelid = i.indrelid)
			ORDER BY 1`, table)
		return defs, err
	case EngineFirebird:
		return firebirdIndexes(ctx, db, table)
	case EngineMSSQL:
		return mssqlIndexes(ctx, db, table)
	}
	return nil, nil
}

// foreignKeyDDL returns ALTER TABLE … ADD … FOREIGN KEY statements for a table.
func foreignKeyDDL(ctx context.Context, engine string, db *sql.DB, table string) ([]string, error) {
	switch engine {
	case EnginePostgres:
		return queryStringsArgs(ctx, db, `SELECT 'ALTER TABLE ' || $2 || ' ADD CONSTRAINT ' || quote_ident(con.conname) || ' ' || pg_get_constraintdef(con.oid) || ';'
			FROM pg_constraint con JOIN pg_class c ON c.oid = con.conrelid JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname || '.' || c.relname = $1 AND con.contype = 'f' ORDER BY con.conname`, table, quoteObject(EnginePostgres, table))
	case EngineFirebird:
		return firebirdForeignKeys(ctx, db, table)
	case EngineMSSQL:
		return mssqlForeignKeys(ctx, db, table)
	}
	return nil, nil
}

// referentialAction returns " ON <event> <action>" unless the action is the
// default (NO ACTION / RESTRICT).
func referentialAction(event, action string) string {
	action = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(action), "_", " "))
	if action == "" || action == "RESTRICT" || action == "NO ACTION" {
		return ""
	}
	return " ON " + event + " " + action
}

// ---- Firebird ---------------------------------------------------------------

// fbConstraintName returns "CONSTRAINT <name> " for a named constraint, or
// nothing for the INTEG_n names Firebird generates.
func fbConstraintName(name string) string {
	if name == "" || strings.HasPrefix(name, "INTEG_") {
		return ""
	}
	return "CONSTRAINT " + quoteIdent(EngineFirebird, name) + " "
}

func firebirdIndexColumns(ctx context.Context, db *sql.DB, index string) (string, error) {
	cols, err := queryStringsArgs(ctx, db, `SELECT TRIM(RDB$FIELD_NAME) FROM RDB$INDEX_SEGMENTS
		WHERE RDB$INDEX_NAME = ? ORDER BY RDB$FIELD_POSITION`, index)
	for i, c := range cols {
		cols[i] = quoteIdent(EngineFirebird, c)
	}
	return strings.Join(cols, ", "), err
}

// firebirdConstraints returns a table's UNIQUE and CHECK constraint lines.
func firebirdConstraints(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT TRIM(RDB$CONSTRAINT_NAME), TRIM(RDB$INDEX_NAME) FROM RDB$RELATION_CONSTRAINTS
		WHERE RDB$RELATION_NAME = ? AND RDB$CONSTRAINT_TYPE = 'UNIQUE' ORDER BY 1`, table)
	if err != nil {
		return nil, err
	}
	type uq struct{ name, index string }
	var uqs []uq
	for rows.Next() {
		var u uq
		if err := rows.Scan(&u.name, &u.index); err != nil {
			rows.Close()
			return nil, err
		}
		uqs = append(uqs, u)
	}
	rows.Close()
	var lines []string
	for _, u := range uqs {
		cols, err := firebirdIndexColumns(ctx, db, u.index)
		if err != nil {
			return nil, err
		}
		lines = append(lines, "  "+fbConstraintName(u.name)+"UNIQUE ("+cols+")")
	}
	// Each CHECK constraint is kept as a pair of triggers; type 1 is the
	// before-insert one, whose source is the CHECK clause.
	rows, err = db.QueryContext(ctx, `SELECT TRIM(rc.RDB$CONSTRAINT_NAME), t.RDB$TRIGGER_SOURCE
		FROM RDB$RELATION_CONSTRAINTS rc
		JOIN RDB$CHECK_CONSTRAINTS cc ON cc.RDB$CONSTRAINT_NAME = rc.RDB$CONSTRAINT_NAME
		JOIN RDB$TRIGGERS t ON t.RDB$TRIGGER_NAME = cc.RDB$TRIGGER_NAME
		WHERE rc.RDB$RELATION_NAME = ? AND rc.RDB$CONSTRAINT_TYPE = 'CHECK' AND t.RDB$TRIGGER_TYPE = 1
		ORDER BY 1`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var src sql.NullString
		if err := rows.Scan(&name, &src); err != nil {
			return nil, err
		}
		lines = append(lines, "  "+fbConstraintName(name)+strings.TrimSpace(src.String))
	}
	return lines, rows.Err()
}

func firebirdIndexes(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT TRIM(i.RDB$INDEX_NAME), COALESCE(i.RDB$UNIQUE_FLAG, 0), COALESCE(i.RDB$INDEX_TYPE, 0),
			i.RDB$EXPRESSION_SOURCE, COALESCE(i.RDB$INDEX_INACTIVE, 0)
		FROM RDB$INDICES i
		WHERE i.RDB$RELATION_NAME = ? AND COALESCE(i.RDB$SYSTEM_FLAG, 0) = 0
		  AND NOT EXISTS (SELECT 1 FROM RDB$RELATION_CONSTRAINTS rc WHERE rc.RDB$INDEX_NAME = i.RDB$INDEX_NAME)
		ORDER BY 1`, table)
	if err != nil {
		return nil, err
	}
	type ix struct {
		name                   string
		unique, desc, inactive int64
		expr                   sql.NullString
	}
	var list []ix
	for rows.Next() {
		var x ix
		if err := rows.Scan(&x.name, &x.unique, &x.desc, &x.expr, &x.inactive); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, x)
	}
	rows.Close()
	var out []string
	for _, x := range list {
		s := "CREATE "
		if x.unique == 1 {
			s += "UNIQUE "
		}
		if x.desc == 1 {
			s += "DESCENDING "
		}
		s += "INDEX " + quoteIdent(EngineFirebird, x.name) + " ON " + quoteIdent(EngineFirebird, table)
		if x.expr.Valid {
			e := strings.TrimSpace(x.expr.String)
			if !strings.HasPrefix(e, "(") {
				e = "(" + e + ")"
			}
			s += " COMPUTED BY " + e + ";"
		} else {
			cols, err := firebirdIndexColumns(ctx, db, x.name)
			if err != nil {
				return nil, err
			}
			s += " (" + cols + ");"
		}
		if x.inactive == 1 {
			s += "\nALTER INDEX " + quoteIdent(EngineFirebird, x.name) + " INACTIVE;"
		}
		out = append(out, s)
	}
	return out, nil
}

func firebirdForeignKeys(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT TRIM(rc.RDB$CONSTRAINT_NAME), TRIM(rc.RDB$INDEX_NAME),
			TRIM(uq.RDB$RELATION_NAME), TRIM(uq.RDB$INDEX_NAME), TRIM(ref.RDB$UPDATE_RULE), TRIM(ref.RDB$DELETE_RULE)
		FROM RDB$RELATION_CONSTRAINTS rc
		JOIN RDB$REF_CONSTRAINTS ref ON ref.RDB$CONSTRAINT_NAME = rc.RDB$CONSTRAINT_NAME
		JOIN RDB$RELATION_CONSTRAINTS uq ON uq.RDB$CONSTRAINT_NAME = ref.RDB$CONST_NAME_UQ
		WHERE rc.RDB$RELATION_NAME = ? AND rc.RDB$CONSTRAINT_TYPE = 'FOREIGN KEY'
		ORDER BY 1`, table)
	if err != nil {
		return nil, err
	}
	type fk struct{ name, index, refTable, refIndex, onUpdate, onDelete string }
	var list []fk
	for rows.Next() {
		var f fk
		var upd, del sql.NullString
		if err := rows.Scan(&f.name, &f.index, &f.refTable, &f.refIndex, &upd, &del); err != nil {
			rows.Close()
			return nil, err
		}
		f.onUpdate, f.onDelete = upd.String, del.String
		list = append(list, f)
	}
	rows.Close()
	var out []string
	for _, f := range list {
		cols, err := firebirdIndexColumns(ctx, db, f.index)
		if err != nil {
			return nil, err
		}
		refCols, err := firebirdIndexColumns(ctx, db, f.refIndex)
		if err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("ALTER TABLE %s ADD %sFOREIGN KEY (%s) REFERENCES %s (%s)%s%s;",
			quoteIdent(EngineFirebird, table), fbConstraintName(f.name), cols, quoteIdent(EngineFirebird, f.refTable), refCols,
			referentialAction("UPDATE", f.onUpdate), referentialAction("DELETE", f.onDelete)))
	}
	return out, nil
}

// ---- SQL Server -------------------------------------------------------------

// mssqlTableID finds a table's object_id by its "schema.name".
const mssqlTableID = `(SELECT o.object_id FROM sys.tables o JOIN sys.schemas s ON s.schema_id = o.schema_id WHERE s.name + '.' + o.name = @p1)`

// mssqlIndexColumns returns the key columns (with DESC) and included columns of an index.
func mssqlIndexColumns(ctx context.Context, db *sql.DB, table string, indexID int64) (keys, include string, err error) {
	rows, err := db.QueryContext(ctx, `SELECT c.name, ic.is_descending_key, ic.is_included_column
		FROM sys.index_columns ic JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
		WHERE ic.object_id = `+mssqlTableID+` AND ic.index_id = @p2
		ORDER BY ic.is_included_column, ic.key_ordinal, ic.index_column_id`, table, indexID)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	var k, inc []string
	for rows.Next() {
		var name string
		var desc, included bool
		if err := rows.Scan(&name, &desc, &included); err != nil {
			return "", "", err
		}
		name = quoteIdent(EngineMSSQL, name)
		switch {
		case included:
			inc = append(inc, name)
		case desc:
			k = append(k, name+" DESC")
		default:
			k = append(k, name)
		}
	}
	return strings.Join(k, ", "), strings.Join(inc, ", "), rows.Err()
}

// mssqlConstraints returns a table's UNIQUE and CHECK constraint lines.
func mssqlConstraints(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT kc.name, kc.unique_index_id FROM sys.key_constraints kc
		WHERE kc.type = 'UQ' AND kc.parent_object_id = `+mssqlTableID+` ORDER BY kc.name`, table)
	if err != nil {
		return nil, err
	}
	type uq struct {
		name  string
		index int64
	}
	var uqs []uq
	for rows.Next() {
		var u uq
		if err := rows.Scan(&u.name, &u.index); err != nil {
			rows.Close()
			return nil, err
		}
		uqs = append(uqs, u)
	}
	rows.Close()
	var lines []string
	for _, u := range uqs {
		cols, _, err := mssqlIndexColumns(ctx, db, table, u.index)
		if err != nil {
			return nil, err
		}
		lines = append(lines, "  CONSTRAINT "+quoteIdent(EngineMSSQL, u.name)+" UNIQUE ("+cols+")")
	}
	checks, err := queryStringsArgs(ctx, db, `SELECT 'CONSTRAINT ' + QUOTENAME(name) + ' CHECK ' + definition
		FROM sys.check_constraints WHERE parent_object_id = `+mssqlTableID+` ORDER BY name`, table)
	if err != nil {
		return nil, err
	}
	for _, c := range checks {
		lines = append(lines, "  "+c)
	}
	return lines, nil
}

func mssqlIndexes(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT i.name, i.index_id, i.is_unique, i.type_desc, i.filter_definition
		FROM sys.indexes i
		WHERE i.object_id = `+mssqlTableID+` AND i.type IN (1, 2)
		  AND i.is_primary_key = 0 AND i.is_unique_constraint = 0 AND i.is_hypothetical = 0
		ORDER BY i.name`, table)
	if err != nil {
		return nil, err
	}
	type ix struct {
		name, kind string
		id         int64
		unique     bool
		filter     sql.NullString
	}
	var list []ix
	for rows.Next() {
		var x ix
		if err := rows.Scan(&x.name, &x.id, &x.unique, &x.kind, &x.filter); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, x)
	}
	rows.Close()
	var out []string
	for _, x := range list {
		keys, include, err := mssqlIndexColumns(ctx, db, table, x.id)
		if err != nil {
			return nil, err
		}
		s := "CREATE "
		if x.unique {
			s += "UNIQUE "
		}
		s += x.kind + " INDEX " + quoteIdent(EngineMSSQL, x.name) + " ON " + quoteObject(EngineMSSQL, table) + " (" + keys + ")"
		if include != "" {
			s += " INCLUDE (" + include + ")"
		}
		if x.filter.Valid {
			s += " WHERE " + x.filter.String
		}
		out = append(out, s+";")
	}
	return out, nil
}

func mssqlForeignKeys(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT fk.object_id, fk.name, rs.name + '.' + ro.name,
			fk.update_referential_action_desc, fk.delete_referential_action_desc
		FROM sys.foreign_keys fk
		JOIN sys.objects ro ON ro.object_id = fk.referenced_object_id
		JOIN sys.schemas rs ON rs.schema_id = ro.schema_id
		WHERE fk.parent_object_id = `+mssqlTableID+` ORDER BY fk.name`, table)
	if err != nil {
		return nil, err
	}
	type fk struct {
		id                  int64
		name, ref, upd, del string
	}
	var list []fk
	for rows.Next() {
		var f fk
		if err := rows.Scan(&f.id, &f.name, &f.ref, &f.upd, &f.del); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, f)
	}
	rows.Close()
	var out []string
	for _, f := range list {
		pairs, err := db.QueryContext(ctx, `SELECT pc.name, rc.name FROM sys.foreign_key_columns k
			JOIN sys.columns pc ON pc.object_id = k.parent_object_id AND pc.column_id = k.parent_column_id
			JOIN sys.columns rc ON rc.object_id = k.referenced_object_id AND rc.column_id = k.referenced_column_id
			WHERE k.constraint_object_id = @p1 ORDER BY k.constraint_column_id`, f.id)
		if err != nil {
			return nil, err
		}
		var cols, refCols []string
		for pairs.Next() {
			var c, r string
			if err := pairs.Scan(&c, &r); err != nil {
				pairs.Close()
				return nil, err
			}
			cols = append(cols, quoteIdent(EngineMSSQL, c))
			refCols = append(refCols, quoteIdent(EngineMSSQL, r))
		}
		pairs.Close()
		out = append(out, fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s)%s%s;",
			quoteObject(EngineMSSQL, table), quoteIdent(EngineMSSQL, f.name), strings.Join(cols, ", "),
			quoteObject(EngineMSSQL, f.ref), strings.Join(refCols, ", "),
			referentialAction("UPDATE", f.upd), referentialAction("DELETE", f.del)))
	}
	return out, nil
}
