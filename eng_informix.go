package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Informix is reached through its wire listener, which speaks MongoDB's
// protocol and runs SQL sent to the system.sql collection ("SQL
// passthrough", enabled with security.sql.passthrough=true). Rows come back
// as documents, one field per column.

func init() { backendOpeners[EngineInformix] = openInformix }

type informixBackend struct {
	c  *mongo.Client
	db *mongo.Database
}

func openInformix(ctx context.Context, p ConnParams, opts map[string]string) (backend, error) {
	if strings.TrimSpace(p.Database) == "" {
		return nil, errors.New("enter the Informix database name")
	}
	c, err := mongoClient(p, opts)
	if err != nil {
		return nil, err
	}
	return &informixBackend{c: c, db: c.Database(strings.TrimSpace(p.Database))}, nil
}

func (x *informixBackend) Ping(ctx context.Context) error {
	if err := x.c.Ping(ctx, nil); err != nil {
		return err
	}
	_, err := x.strings(ctx, `SELECT FIRST 1 tabname FROM systables`)
	if err != nil {
		return fmt.Errorf("%w (is SQL passthrough on? security.sql.passthrough=true in the wire listener's properties)", err)
	}
	return nil
}

func (x *informixBackend) Close() error { return (&mongoBackend{c: x.c}).Close() }

// sql runs one statement through the listener.
func (x *informixBackend) sql(ctx context.Context, q string) (*mongo.Cursor, error) {
	return x.db.Collection("system.sql").Find(ctx, bson.D{{Key: "$sql", Value: q}})
}

// strings returns the first column of every row of q.
func (x *informixBackend) strings(ctx context.Context, q string) ([]string, error) {
	cur, err := x.sql(ctx, q)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []string
	for cur.Next(ctx) {
		var d bson.D
		if err := bson.Unmarshal(cur.Current, &d); err != nil {
			return nil, err
		}
		if len(d) > 0 {
			out = append(out, strings.TrimRight(fmt.Sprint(mongoValue(d[0].Value)), " "))
		}
	}
	return out, cur.Err()
}

func (x *informixBackend) Version(ctx context.Context) string {
	v, err := x.strings(ctx, `SELECT DBINFO('version', 'full') FROM systables WHERE tabid = 1`)
	if err != nil || len(v) == 0 {
		return "Informix"
	}
	return v[0]
}

// ifxString quotes a value for SQL: passthrough has no parameters.
func ifxString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func (x *informixBackend) Objects(ctx context.Context) ([]ObjectGroup, error) {
	q := func(typ, label string, queryable bool, sql string, mandatory bool) (ObjectGroup, error) {
		items, err := x.strings(ctx, sql)
		if err != nil && mandatory {
			return ObjectGroup{}, err
		}
		if items == nil {
			items = []string{}
		}
		return ObjectGroup{Type: typ, Label: label, Queryable: queryable, Items: items}, nil
	}
	tables, err := q(ObjTable, "Tables", true, `SELECT TRIM(tabname) FROM systables WHERE tabid >= 100 AND tabtype = 'T' ORDER BY 1`, true)
	if err != nil {
		return nil, err
	}
	out := []ObjectGroup{tables}
	for _, g := range []struct{ typ, label, sql string }{
		{ObjView, "Views", `SELECT TRIM(tabname) FROM systables WHERE tabid >= 100 AND tabtype = 'V' ORDER BY 1`},
		{ObjProcedure, "Procedures", `SELECT DISTINCT TRIM(procname) FROM sysprocedures WHERE isproc = 't' AND mode IN ('D', 'O', 'R') AND internal = 'f' ORDER BY 1`},
		{ObjFunction, "Functions", `SELECT DISTINCT TRIM(procname) FROM sysprocedures WHERE isproc = 'f' AND mode IN ('D', 'O', 'R') AND internal = 'f' ORDER BY 1`},
		{ObjTrigger, "Triggers", `SELECT TRIM(trigname) FROM systriggers ORDER BY 1`},
		{ObjSequence, "Sequences", `SELECT TRIM(t.tabname) FROM syssequences s, systables t WHERE t.tabid = s.tabid ORDER BY 1`},
		{ObjSynonym, "Synonyms", `SELECT TRIM(tabname) FROM systables WHERE tabid >= 100 AND tabtype IN ('S', 'P') ORDER BY 1`},
	} {
		grp, _ := q(g.typ, g.label, g.typ == ObjView, g.sql, false)
		out = append(out, grp)
	}
	return out, nil
}

func (x *informixBackend) SelectFor(_ context.Context, name string) (string, error) {
	return "SELECT * FROM " + name, nil
}

func (x *informixBackend) Count(ctx context.Context, name string) (int64, error) {
	v, err := x.strings(ctx, "SELECT COUNT(*) FROM "+name)
	if err != nil || len(v) == 0 {
		return -1, err
	}
	return strconv.ParseInt(v[0], 10, 64)
}

func (x *informixBackend) Definition(ctx context.Context, typ, name string) (string, error) {
	n := ifxString(name)
	text := func(sql string) (string, error) {
		parts, err := x.strings(ctx, sql)
		if err != nil {
			return "", err
		}
		if len(parts) == 0 {
			return "", fmt.Errorf("%s %s not found", typ, name)
		}
		return strings.TrimSpace(strings.Join(parts, "")) + "\n", nil
	}
	switch typ {
	case ObjTable:
		return x.tableDDL(ctx, name)
	case ObjView:
		return text(`SELECT v.viewtext FROM sysviews v, systables t WHERE t.tabid = v.tabid AND t.tabname = ` + n + ` ORDER BY v.seqno`)
	case ObjProcedure, ObjFunction:
		return text(`SELECT b.data FROM sysprocbody b, sysprocedures p WHERE p.procid = b.procid AND p.procname = ` + n +
			` AND b.datakey = 'T' ORDER BY p.procid, b.seqno`)
	case ObjTrigger:
		return text(`SELECT b.data FROM systrigbody b, systriggers t WHERE t.trigid = b.trigid AND t.trigname = ` + n +
			` AND b.datakey IN ('D', 'A') ORDER BY b.datakey DESC, b.seqno`)
	case ObjSequence:
		cur, err := x.sql(ctx, `SELECT s.start_val, s.inc_val, s.min_val, s.max_val, s.cycle, s.cache FROM syssequences s, systables t
			WHERE t.tabid = s.tabid AND t.tabname = `+n)
		if err != nil {
			return "", err
		}
		defer cur.Close(ctx)
		if !cur.Next(ctx) {
			return "", fmt.Errorf("sequence %s not found", name)
		}
		var d bson.D
		bson.Unmarshal(cur.Current, &d)
		v := func(i int) string {
			if i < len(d) {
				return fmt.Sprint(mongoValue(d[i].Value))
			}
			return ""
		}
		cycle := "NOCYCLE"
		if v(4) == "1" {
			cycle = "CYCLE"
		}
		return fmt.Sprintf("CREATE SEQUENCE %s\n  START WITH %s\n  INCREMENT BY %s\n  MINVALUE %s\n  MAXVALUE %s\n  %s\n  CACHE %s;\n",
			name, v(0), v(1), v(2), v(3), cycle, v(5)), nil
	case ObjSynonym:
		return text(`SELECT 'CREATE SYNONYM ` + strings.ReplaceAll(name, "'", "''") + ` FOR ' || TRIM(r.tabname) || ';' FROM syssyntable s, systables t, systables r
			WHERE t.tabid = s.tabid AND r.tabid = s.btabid AND t.tabname = ` + n)
	}
	return "", fmt.Errorf("unsupported object type %q", typ)
}

// tableDDL builds CREATE TABLE from syscolumns.
func (x *informixBackend) tableDDL(ctx context.Context, table string) (string, error) {
	cur, err := x.sql(ctx, `SELECT TRIM(c.colname), c.coltype, c.collength, c.extended_id FROM syscolumns c, systables t
		WHERE t.tabid = c.tabid AND t.tabname = `+ifxString(table)+` ORDER BY c.colno`)
	if err != nil {
		return "", err
	}
	defer cur.Close(ctx)
	var lines []string
	for cur.Next(ctx) {
		var d bson.D
		if err := bson.Unmarshal(cur.Current, &d); err != nil || len(d) < 3 {
			continue
		}
		name := fmt.Sprint(mongoValue(d[0].Value))
		coltype, _ := strconv.Atoi(fmt.Sprint(mongoValue(d[1].Value)))
		collen, _ := strconv.Atoi(fmt.Sprint(mongoValue(d[2].Value)))
		line := "  " + name + " " + informixType(coltype, collen)
		if coltype&0x100 != 0 {
			line += " NOT NULL"
		}
		lines = append(lines, line)
	}
	if err := cur.Err(); err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", fmt.Errorf("table %s not found", table)
	}
	// Primary key and unique constraints.
	cons, _ := x.strings(ctx, `SELECT TRIM(c.constrname) || ' ' || c.constrtype || ' ' || TRIM(c.idxname) FROM sysconstraints c, systables t
		WHERE t.tabid = c.tabid AND t.tabname = `+ifxString(table)+` AND c.constrtype IN ('P', 'U')`)
	for _, c := range cons {
		f := strings.Fields(c)
		if len(f) == 3 {
			kind := "UNIQUE"
			if f[1] == "P" {
				kind = "PRIMARY KEY"
			}
			lines = append(lines, "  -- "+kind+" constraint "+f[0]+" (index "+f[2]+")")
		}
	}
	return "CREATE TABLE " + table + " (\n" + strings.Join(lines, ",\n") + "\n);\n", nil
}

// informixType decodes syscolumns.coltype and collength.
func informixType(coltype, collen int) string {
	units := []string{"YEAR", "", "MONTH", "", "DAY", "", "HOUR", "", "MINUTE", "", "SECOND", "FRACTION(1)", "FRACTION(2)", "FRACTION(3)", "FRACTION(4)", "FRACTION(5)"}
	unit := func(i int) string {
		if i >= 0 && i < len(units) && units[i] != "" {
			return units[i]
		}
		return "?"
	}
	qualifier := func() string {
		start, end := (collen%256)/16, collen%16
		if end > 10 {
			return unit(start) + " TO FRACTION(" + strconv.Itoa(end-10) + ")"
		}
		return unit(start) + " TO " + unit(end)
	}
	decimal := func(base string) string {
		p, s := collen/256, collen%256
		if s == 255 {
			return base + "(" + strconv.Itoa(p) + ")"
		}
		return base + "(" + strconv.Itoa(p) + "," + strconv.Itoa(s) + ")"
	}
	switch coltype & 0xFF {
	case 0:
		return "CHAR(" + strconv.Itoa(collen) + ")"
	case 1:
		return "SMALLINT"
	case 2:
		return "INTEGER"
	case 3:
		return "FLOAT"
	case 4:
		return "SMALLFLOAT"
	case 5:
		return decimal("DECIMAL")
	case 6:
		return "SERIAL"
	case 7:
		return "DATE"
	case 8:
		return decimal("MONEY")
	case 10:
		return "DATETIME " + qualifier()
	case 11:
		return "BYTE"
	case 12:
		return "TEXT"
	case 13, 16:
		t := "VARCHAR"
		if coltype&0xFF == 16 {
			t = "NVARCHAR"
		}
		if min := collen / 256; min > 0 {
			return t + "(" + strconv.Itoa(collen%256) + "," + strconv.Itoa(min) + ")"
		}
		return t + "(" + strconv.Itoa(collen%256) + ")"
	case 14:
		return "INTERVAL " + qualifier()
	case 15:
		return "NCHAR(" + strconv.Itoa(collen) + ")"
	case 17:
		return "INT8"
	case 18:
		return "SERIAL8"
	case 19:
		return "SET"
	case 20:
		return "MULTISET"
	case 21:
		return "LIST"
	case 22:
		return "ROW"
	case 40, 43:
		return "LVARCHAR(" + strconv.Itoa(collen) + ")"
	case 41:
		return "BLOB/CLOB/BOOLEAN"
	case 45:
		return "BOOLEAN"
	case 52:
		return "BIGINT"
	case 53:
		return "BIGSERIAL"
	}
	return "TYPE" + strconv.Itoa(coltype&0xFF)
}

func (x *informixBackend) CheckReadOnly(q string) error { return checkSQL("", q) }

func (x *informixBackend) Query(ctx context.Context, q string) (resultSet, error) {
	cur, err := x.sql(ctx, strings.TrimRight(strings.TrimSpace(q), ";"))
	if err != nil {
		return nil, err
	}
	return newDocRows(func() (doc, error) {
		if !cur.Next(ctx) {
			if err := cur.Err(); err != nil {
				return nil, err
			}
			return nil, io.EOF
		}
		var d bson.D
		if err := bson.Unmarshal(cur.Current, &d); err != nil {
			return nil, err
		}
		return mongoDoc(d), nil
	}, func() error { return cur.Close(context.Background()) })
}
