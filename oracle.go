package main

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"weak"

	go_ora "github.com/sijms/go-ora/v2"
)

// Oracle: objects are listed as OWNER.NAME, of the schema in the schema
// option, else of the user's own (current) schema.

// oracleSchemas remembers the schema option of an Oracle *sql.DB.
var oracleSchemas sync.Map

// oracleDSN builds a go-ora URL: Database is the service name (or a SID with
// the sid option); the other options are passed on, e.g. ssl=true.
func oracleDSN(p ConnParams, opts map[string]string) string {
	urlOpts := map[string]string{}
	for k, v := range opts {
		if k != "schema" {
			urlOpts[strings.ToUpper(k)] = v
		}
	}
	port := p.Port
	if port == 0 {
		port = 1521
	}
	host := p.Host
	if host == "" {
		host = "localhost"
	}
	return go_ora.BuildUrl(host, port, p.Database, p.Username, p.Password, urlOpts)
}

// oracleSplit splits OWNER.NAME; owner "" means the current schema.
func oracleSplit(name string) (owner, obj string) {
	if o, n, ok := splitObject(name); ok {
		return o, n
	}
	return "", name
}

// oracleOwner returns the schema of db's objects.
func oracleOwner(ctx context.Context, db *sql.DB) (string, error) {
	if v, ok := oracleSchemas.Load(weak.Make(db)); ok {
		return v.(string), nil
	}
	var s string
	err := db.QueryRowContext(ctx, `SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') FROM dual`).Scan(&s)
	return s, err
}

func oracleObjects(ctx context.Context, db *sql.DB) ([]ObjectGroup, error) {
	owner, err := oracleOwner(ctx, db)
	if err != nil {
		return nil, err
	}
	objs := func(types string) string {
		return `SELECT owner || '.' || object_name FROM all_objects WHERE owner = :1 AND object_type IN (` + types + `)
			AND object_name NOT LIKE 'BIN$%' AND object_name NOT LIKE 'SYS\_%' ESCAPE '\' AND generated = 'N' ORDER BY object_name`
	}
	groups := []struct {
		typ, label, q string
	}{
		// Tables, without the containers of materialized views and nested tables.
		{ObjTable, "Tables", `SELECT owner || '.' || table_name FROM all_tables t WHERE owner = :1 AND nested = 'NO'
			AND table_name NOT LIKE 'BIN$%' AND (iot_type IS NULL OR iot_type = 'IOT')
			AND NOT EXISTS (SELECT 1 FROM all_mviews m WHERE m.owner = t.owner AND m.mview_name = t.table_name) ORDER BY table_name`},
		{ObjView, "Views", objs(`'VIEW'`)},
		{ObjMatView, "Materialized views", objs(`'MATERIALIZED VIEW'`)},
		{ObjProcedure, "Procedures", objs(`'PROCEDURE'`)},
		{ObjFunction, "Functions", objs(`'FUNCTION'`)},
		{ObjPackage, "Packages", objs(`'PACKAGE'`)},
		{ObjTrigger, "Triggers", objs(`'TRIGGER'`)},
		{ObjSequence, "Sequences", objs(`'SEQUENCE'`)},
		{ObjSynonym, "Synonyms", objs(`'SYNONYM'`)},
		{ObjDomain, "Types", objs(`'TYPE'`)},
	}
	var out []ObjectGroup
	for i, g := range groups {
		items, err := queryStringsArgs(ctx, db, g.q, owner)
		if err != nil {
			if i == 0 {
				return nil, err
			}
			items = []string{}
		}
		out = append(out, ObjectGroup{Type: g.typ, Label: g.label, Queryable: g.typ == ObjTable || g.typ == ObjView || g.typ == ObjMatView, Items: items})
	}
	return out, nil
}

var oracleMetaTypes = map[string]string{
	ObjTable: "TABLE", ObjView: "VIEW", ObjMatView: "MATERIALIZED_VIEW", ObjProcedure: "PROCEDURE",
	ObjFunction: "FUNCTION", ObjPackage: "PACKAGE", ObjTrigger: "TRIGGER", ObjSequence: "SEQUENCE",
	ObjSynonym: "SYNONYM", ObjDomain: "TYPE",
}

// oracleDefinition returns DBMS_METADATA's DDL without storage clauses, or
// builds it from the dictionary when the user may not call it.
func oracleDefinition(ctx context.Context, db *sql.DB, typ, name string) (string, error) {
	owner, obj := oracleSplit(name)
	if owner == "" {
		var err error
		if owner, err = oracleOwner(ctx, db); err != nil {
			return "", err
		}
	}
	mt, ok := oracleMetaTypes[typ]
	if !ok {
		return "", fmt.Errorf("unsupported object type %q", typ)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	// Session settings of DBMS_METADATA only: nothing is written.
	conn.ExecContext(ctx, `BEGIN
		DBMS_METADATA.SET_TRANSFORM_PARAM(DBMS_METADATA.SESSION_TRANSFORM, 'STORAGE', FALSE);
		DBMS_METADATA.SET_TRANSFORM_PARAM(DBMS_METADATA.SESSION_TRANSFORM, 'SEGMENT_ATTRIBUTES', FALSE);
		DBMS_METADATA.SET_TRANSFORM_PARAM(DBMS_METADATA.SESSION_TRANSFORM, 'SQLTERMINATOR', TRUE);
		DBMS_METADATA.SET_TRANSFORM_PARAM(DBMS_METADATA.SESSION_TRANSFORM, 'PRETTY', TRUE);
	END;`)
	var ddl sql.NullString
	err = conn.QueryRowContext(ctx, `SELECT DBMS_METADATA.GET_DDL(:1, :2, :3) FROM dual`, mt, obj, owner).Scan(&ddl)
	if err == nil && ddl.Valid {
		out := strings.TrimSpace(ddl.String) + "\n"
		if typ == ObjTable { // its indexes, which GET_DDL leaves out
			var idx sql.NullString
			if conn.QueryRowContext(ctx, `SELECT DBMS_METADATA.GET_DEPENDENT_DDL('INDEX', :1, :2) FROM dual`, obj, owner).Scan(&idx) == nil && idx.Valid {
				out += "\n" + strings.TrimSpace(idx.String) + "\n"
			}
		}
		return out, nil
	}
	// No access to DBMS_METADATA for this object: from the dictionary.
	switch typ {
	case ObjTable:
		return oracleTableDDL(ctx, db, owner, obj)
	case ObjView:
		var text sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT text FROM all_views WHERE owner = :1 AND view_name = :2`, owner, obj).Scan(&text); err != nil {
			return "", err
		}
		return "CREATE OR REPLACE VIEW " + quoteObject(EngineOracle, owner+"."+obj) + " AS\n" + strings.TrimSpace(text.String) + ";\n", nil
	case ObjProcedure, ObjFunction, ObjPackage, ObjTrigger, ObjDomain:
		var b strings.Builder
		for _, t := range []string{mt, mt + " BODY"} {
			lines, err := queryStringsArgs(ctx, db, `SELECT text FROM all_source WHERE owner = :1 AND name = :2 AND type = :3 ORDER BY line`,
				owner, obj, strings.ReplaceAll(t, "_", " "))
			if err != nil || len(lines) == 0 {
				continue
			}
			b.WriteString("CREATE OR REPLACE " + strings.Join(lines, "\n") + "\n/\n\n")
		}
		if b.Len() == 0 {
			return "", fmt.Errorf("%s %s not found (or no permission to read it)", typ, name)
		}
		return b.String(), nil
	}
	return "", err
}

// oracleTableDDL builds CREATE TABLE from ALL_TAB_COLUMNS and ALL_CONSTRAINTS.
func oracleTableDDL(ctx context.Context, db *sql.DB, owner, table string) (string, error) {
	rows, err := db.QueryContext(ctx, `SELECT column_name, data_type, data_length, data_precision, data_scale, nullable, char_used, char_length
		FROM all_tab_columns WHERE owner = :1 AND table_name = :2 ORDER BY column_id`, owner, table)
	if err != nil {
		return "", err
	}
	var lines []string
	for rows.Next() {
		var col, typ, nullable string
		var length, charLen sql.NullInt64
		var prec, scale sql.NullInt64
		var charUsed sql.NullString
		if err := rows.Scan(&col, &typ, &length, &prec, &scale, &nullable, &charUsed, &charLen); err != nil {
			rows.Close()
			return "", err
		}
		t := typ
		switch {
		case typ == "VARCHAR2" || typ == "NVARCHAR2" || typ == "CHAR" || typ == "NCHAR":
			n := length.Int64
			unit := ""
			if charUsed.String == "C" {
				n, unit = charLen.Int64, " CHAR"
			}
			if strings.HasPrefix(typ, "N") {
				unit = ""
			}
			t += "(" + strconv.FormatInt(n, 10) + unit + ")"
		case typ == "NUMBER" && prec.Valid:
			t += "(" + strconv.FormatInt(prec.Int64, 10)
			if scale.Valid && scale.Int64 != 0 {
				t += "," + strconv.FormatInt(scale.Int64, 10)
			}
			t += ")"
		case typ == "RAW":
			t += "(" + strconv.FormatInt(length.Int64, 10) + ")"
		}
		line := "  " + quoteIdent(EngineOracle, col) + " " + t
		if nullable == "N" {
			line += " NOT NULL"
		}
		lines = append(lines, line)
	}
	rows.Close()
	if len(lines) == 0 {
		return "", sql.ErrNoRows
	}
	cons, _ := queryStringsArgs(ctx, db, `SELECT 'CONSTRAINT "' || c.constraint_name || '" ' ||
			CASE c.constraint_type WHEN 'P' THEN 'PRIMARY KEY' ELSE 'UNIQUE' END || ' (' ||
			(SELECT LISTAGG('"' || cc.column_name || '"', ', ') WITHIN GROUP (ORDER BY cc.position)
			 FROM all_cons_columns cc WHERE cc.owner = c.owner AND cc.constraint_name = c.constraint_name) || ')'
		FROM all_constraints c WHERE c.owner = :1 AND c.table_name = :2 AND c.constraint_type IN ('P', 'U')
		ORDER BY c.constraint_type, c.constraint_name`, owner, table)
	for _, c := range cons {
		lines = append(lines, "  "+c)
	}
	return "CREATE TABLE " + quoteObject(EngineOracle, owner+"."+table) + " (\n" + strings.Join(lines, ",\n") + "\n);\n", nil
}

// oracleSizeQuery returns name, estimated rows and bytes per table.
// (sizes are known for the user's own tables only).
const oracleSizeQuery = `SELECT USER || '.' || t.table_name, NVL(t.num_rows, -1),
	NVL((SELECT SUM(s.bytes) FROM user_segments s WHERE s.segment_name = t.table_name), 0) FROM user_tables t`
