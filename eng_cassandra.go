package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	gocql "github.com/apache/cassandra-gocql-driver/v2"
)

// Apache Cassandra (also ScyllaDB, DataStax Astra through a proxy, and Azure
// Cosmos DB's Cassandra API): tables, materialized views, user types and
// functions from system_schema, named keyspace.name. Queries are CQL
// SELECTs.

func init() { backendOpeners[EngineCassandra] = openCassandra }

type cassandraBackend struct {
	s        *gocql.Session
	keyspace string // "" = every keyspace
}

var cassandraSystemKeyspaces = map[string]bool{
	"system": true, "system_auth": true, "system_schema": true, "system_distributed": true, "system_traces": true,
	"system_views": true, "system_virtual_schema": true, "dse_system": true, "dse_security": true, "dse_auth": true,
	"dse_insights": true, "dse_insights_local": true, "dse_leases": true, "dse_perf": true, "dse_system_local": true,
	"solr_admin": true, "system_backups": true, "OpsCenter": true,
}

func openCassandra(ctx context.Context, p ConnParams, opts map[string]string) (backend, error) {
	var hosts []string
	for _, h := range strings.Split(p.Host, ",") {
		if h = strings.TrimSpace(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 {
		hosts = []string{"localhost"}
	}
	c := gocql.NewCluster(hosts...)
	if p.Port != 0 {
		c.Port = p.Port
	}
	c.Keyspace = strings.TrimSpace(p.Database)
	c.ConnectTimeout = 10 * time.Second
	c.Timeout = 60 * time.Second
	c.Consistency = gocql.LocalOne
	if s := opts["consistency"]; s != "" {
		cons, err := gocql.ParseConsistencyWrapper(strings.ToUpper(s))
		if err != nil {
			return nil, err
		}
		c.Consistency = cons
	}
	if dc := opts["dc"]; dc != "" {
		c.PoolConfig.HostSelectionPolicy = gocql.TokenAwareHostPolicy(gocql.DCAwareRoundRobinPolicy(dc))
	}
	if p.Username != "" {
		c.Authenticator = gocql.PasswordAuthenticator{Username: p.Username, Password: p.Password}
	}
	if optBool(opts, "tls", false) || optBool(opts, "ssl", false) {
		c.SslOpts = &gocql.SslOptions{Config: &tls.Config{InsecureSkipVerify: optBool(opts, "insecure", false)},
			EnableHostVerification: !optBool(opts, "insecure", false)}
	}
	s, err := c.CreateSession()
	if err != nil {
		return nil, err
	}
	return &cassandraBackend{s: s, keyspace: c.Keyspace}, nil
}

func (c *cassandraBackend) Ping(ctx context.Context) error {
	return c.s.Query(`SELECT release_version FROM system.local`).WithContext(ctx).Exec()
}
func (c *cassandraBackend) Close() error { c.s.Close(); return nil }

func (c *cassandraBackend) Version(ctx context.Context) string {
	var v string
	if c.s.Query(`SELECT release_version FROM system.local`).WithContext(ctx).Scan(&v) != nil {
		return "Cassandra"
	}
	return "Cassandra " + v
}

// rows reads every row of a catalog query as strings.
func (c *cassandraBackend) rows(ctx context.Context, q string, args ...any) ([]map[string]any, error) {
	iter := c.s.Query(q, args...).WithContext(ctx).Iter()
	var out []map[string]any
	for {
		m := map[string]any{}
		if !iter.MapScan(m) {
			break
		}
		out = append(out, m)
	}
	return out, iter.Close()
}

func (c *cassandraBackend) keyspaces(ctx context.Context) ([]string, error) {
	if c.keyspace != "" {
		return []string{c.keyspace}, nil
	}
	rows, err := c.rows(ctx, `SELECT keyspace_name FROM system_schema.keyspaces`)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rows {
		if ks := fmt.Sprint(r["keyspace_name"]); !cassandraSystemKeyspaces[ks] {
			out = append(out, ks)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (c *cassandraBackend) Objects(ctx context.Context) ([]ObjectGroup, error) {
	kss, err := c.keyspaces(ctx)
	if err != nil {
		return nil, err
	}
	g := []ObjectGroup{
		{Type: ObjTable, Label: "Tables", Queryable: true, Items: []string{}},
		{Type: ObjMatView, Label: "Materialized views", Queryable: true, Items: []string{}},
		{Type: ObjDomain, Label: "Types", Items: []string{}},
		{Type: ObjFunction, Label: "Functions", Items: []string{}},
		{Type: ObjIndex, Label: "Indexes", Items: []string{}},
	}
	for _, ks := range kss {
		list := func(i int, q, col string) error {
			rows, err := c.rows(ctx, q, ks)
			for _, r := range rows {
				g[i].Items = append(g[i].Items, ks+"."+fmt.Sprint(r[col]))
			}
			return err
		}
		if err := list(0, `SELECT table_name FROM system_schema.tables WHERE keyspace_name = ?`, "table_name"); err != nil {
			return nil, err
		}
		list(1, `SELECT view_name FROM system_schema.views WHERE keyspace_name = ?`, "view_name")
		list(2, `SELECT type_name FROM system_schema.types WHERE keyspace_name = ?`, "type_name")
		list(4, `SELECT index_name FROM system_schema.indexes WHERE keyspace_name = ?`, "index_name")
		if rows, err := c.rows(ctx, `SELECT function_name, argument_types FROM system_schema.functions WHERE keyspace_name = ?`, ks); err == nil {
			for _, r := range rows {
				g[3].Items = append(g[3].Items, fmt.Sprintf("%s.%v(%s)", ks, r["function_name"], strings.Join(toStrings(r["argument_types"]), ", ")))
			}
		}
	}
	for i := range g {
		sort.Strings(g[i].Items)
	}
	return g, nil
}

func toStrings(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, len(t))
		for i, x := range t {
			out[i] = fmt.Sprint(x)
		}
		return out
	}
	return nil
}

// cqlName quotes a name when it needs it.
func cqlName(s string) string {
	simple := s != ""
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c == '_' || i > 0 && c >= '0' && c <= '9') {
			simple = false
		}
	}
	if simple {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func cqlObject(name string) (ks, obj, quoted string) {
	ks, obj, _ = strings.Cut(name, ".")
	return ks, obj, cqlName(ks) + "." + cqlName(obj)
}

func (c *cassandraBackend) SelectFor(_ context.Context, name string) (string, error) {
	_, _, q := cqlObject(name)
	return "SELECT * FROM " + q, nil
}

// Count is unknown: COUNT(*) reads the whole table on every node.
func (c *cassandraBackend) Count(context.Context, string) (int64, error) { return -1, nil }

type cqlColumn struct {
	name, kind, typ, order string
	pos                    int
}

func (c *cassandraBackend) columns(ctx context.Context, ks, table string) ([]cqlColumn, error) {
	rows, err := c.rows(ctx, `SELECT column_name, kind, position, type, clustering_order FROM system_schema.columns
		WHERE keyspace_name = ? AND table_name = ?`, ks, table)
	if err != nil {
		return nil, err
	}
	var cols []cqlColumn
	for _, r := range rows {
		pos, _ := strconv.Atoi(fmt.Sprint(r["position"]))
		cols = append(cols, cqlColumn{name: fmt.Sprint(r["column_name"]), kind: fmt.Sprint(r["kind"]), typ: fmt.Sprint(r["type"]),
			order: fmt.Sprint(r["clustering_order"]), pos: pos})
	}
	rank := map[string]int{"partition_key": 0, "clustering": 1, "static": 2, "regular": 3}
	sort.SliceStable(cols, func(i, j int) bool {
		a, b := cols[i], cols[j]
		if rank[a.kind] != rank[b.kind] {
			return rank[a.kind] < rank[b.kind]
		}
		if a.pos != b.pos {
			return a.pos < b.pos
		}
		return a.name < b.name
	})
	return cols, nil
}

// primaryKey writes PRIMARY KEY ((pk…), ck…) and the clustering order.
func primaryKey(cols []cqlColumn) (key, order string) {
	var pk, ck, ord []string
	for _, col := range cols {
		switch col.kind {
		case "partition_key":
			pk = append(pk, cqlName(col.name))
		case "clustering":
			ck = append(ck, cqlName(col.name))
			ord = append(ord, cqlName(col.name)+" "+strings.ToUpper(col.order))
		}
	}
	part := strings.Join(pk, ", ")
	if len(pk) > 1 {
		part = "(" + part + ")"
	}
	key = "PRIMARY KEY (" + strings.Join(append([]string{part}, ck...), ", ") + ")"
	if len(ord) > 0 {
		order = "CLUSTERING ORDER BY (" + strings.Join(ord, ", ") + ")"
	}
	return key, order
}

func (c *cassandraBackend) Definition(ctx context.Context, typ, name string) (string, error) {
	ks, obj, quoted := cqlObject(name)
	switch typ {
	case ObjTable:
		cols, err := c.columns(ctx, ks, obj)
		if err != nil {
			return "", err
		}
		if len(cols) == 0 {
			return "", fmt.Errorf("table %s not found", name)
		}
		var lines []string
		for _, col := range cols {
			line := "  " + cqlName(col.name) + " " + col.typ
			if col.kind == "static" {
				line += " STATIC"
			}
			lines = append(lines, line)
		}
		key, order := primaryKey(cols)
		lines = append(lines, "  "+key)
		out := "CREATE TABLE " + quoted + " (\n" + strings.Join(lines, ",\n") + "\n)"
		var with []string
		if order != "" {
			with = append(with, order)
		}
		if rows, err := c.rows(ctx, `SELECT comment, default_time_to_live FROM system_schema.tables WHERE keyspace_name = ? AND table_name = ?`, ks, obj); err == nil && len(rows) == 1 {
			if cm := fmt.Sprint(rows[0]["comment"]); cm != "" {
				with = append(with, "comment = '"+strings.ReplaceAll(cm, "'", "''")+"'")
			}
			if ttl := fmt.Sprint(rows[0]["default_time_to_live"]); ttl != "0" && ttl != "<nil>" {
				with = append(with, "default_time_to_live = "+ttl)
			}
		}
		if len(with) > 0 {
			out += " WITH " + strings.Join(with, "\n  AND ")
		}
		out += ";\n"
		if idx, err := c.rows(ctx, `SELECT index_name FROM system_schema.indexes WHERE keyspace_name = ? AND table_name = ?`, ks, obj); err == nil {
			for _, r := range idx {
				if d, err := c.indexDDL(ctx, ks, fmt.Sprint(r["index_name"])); err == nil {
					out += "\n" + d
				}
			}
		}
		return out, nil
	case ObjMatView:
		rows, err := c.rows(ctx, `SELECT base_table_name, where_clause, include_all_columns FROM system_schema.views WHERE keyspace_name = ? AND view_name = ?`, ks, obj)
		if err != nil {
			return "", err
		}
		if len(rows) == 0 {
			return "", fmt.Errorf("view %s not found", name)
		}
		cols, err := c.columns(ctx, ks, obj)
		if err != nil {
			return "", err
		}
		sel := "*"
		if all, _ := rows[0]["include_all_columns"].(bool); !all {
			names := make([]string, len(cols))
			for i, col := range cols {
				names[i] = cqlName(col.name)
			}
			sel = strings.Join(names, ", ")
		}
		key, order := primaryKey(cols)
		out := fmt.Sprintf("CREATE MATERIALIZED VIEW %s AS\n  SELECT %s\n  FROM %s.%s\n  WHERE %v\n  %s", quoted, sel, cqlName(ks),
			cqlName(fmt.Sprint(rows[0]["base_table_name"])), rows[0]["where_clause"], key)
		if order != "" {
			out += "\n  WITH " + order
		}
		return out + ";\n", nil
	case ObjDomain:
		rows, err := c.rows(ctx, `SELECT field_names, field_types FROM system_schema.types WHERE keyspace_name = ? AND type_name = ?`, ks, obj)
		if err != nil {
			return "", err
		}
		if len(rows) == 0 {
			return "", fmt.Errorf("type %s not found", name)
		}
		names, types := toStrings(rows[0]["field_names"]), toStrings(rows[0]["field_types"])
		var lines []string
		for i := range names {
			if i < len(types) {
				lines = append(lines, "  "+cqlName(names[i])+" "+types[i])
			}
		}
		return "CREATE TYPE " + quoted + " (\n" + strings.Join(lines, ",\n") + "\n);\n", nil
	case ObjFunction:
		fname, _, _ := strings.Cut(obj, "(")
		rows, err := c.rows(ctx, `SELECT argument_names, argument_types, body, language, return_type, called_on_null_input
			FROM system_schema.functions WHERE keyspace_name = ? AND function_name = ?`, ks, fname)
		if err != nil {
			return "", err
		}
		var out []string
		for _, r := range rows {
			an, at := toStrings(r["argument_names"]), toStrings(r["argument_types"])
			if fmt.Sprintf("%s.%s(%s)", ks, fname, strings.Join(at, ", ")) != name {
				continue
			}
			args := make([]string, len(an))
			for i := range an {
				args[i] = cqlName(an[i]) + " " + at[i]
			}
			null := "RETURNS NULL ON NULL INPUT"
			if b, _ := r["called_on_null_input"].(bool); b {
				null = "CALLED ON NULL INPUT"
			}
			out = append(out, fmt.Sprintf("CREATE FUNCTION %s.%s(%s)\n  %s\n  RETURNS %v\n  LANGUAGE %v\n  AS $$%v$$;\n",
				cqlName(ks), cqlName(fname), strings.Join(args, ", "), null, r["return_type"], r["language"], r["body"]))
		}
		if len(out) == 0 {
			return "", fmt.Errorf("function %s not found", name)
		}
		return strings.Join(out, "\n"), nil
	case ObjIndex:
		return c.indexDDL(ctx, ks, obj)
	}
	return "", fmt.Errorf("unsupported object type %q", typ)
}

func (c *cassandraBackend) indexDDL(ctx context.Context, ks, index string) (string, error) {
	rows, err := c.rows(ctx, `SELECT table_name, kind, options FROM system_schema.indexes WHERE keyspace_name = ? AND index_name = ?`, ks, index)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("index %s not found", index)
	}
	opts, _ := rows[0]["options"].(map[string]string)
	target := opts["target"]
	table := cqlName(ks) + "." + cqlName(fmt.Sprint(rows[0]["table_name"]))
	if fmt.Sprint(rows[0]["kind"]) == "CUSTOM" {
		return fmt.Sprintf("CREATE CUSTOM INDEX %s ON %s (%s) USING '%s';\n", cqlName(index), table, target, opts["class_name"]), nil
	}
	return fmt.Sprintf("CREATE INDEX %s ON %s (%s);\n", cqlName(index), table, target), nil
}

func (c *cassandraBackend) CheckReadOnly(q string) error { return checkSQL("", q) }

func (c *cassandraBackend) Query(ctx context.Context, q string) (resultSet, error) {
	iter := c.s.Query(strings.TrimRight(strings.TrimSpace(q), ";")).WithContext(ctx).PageSize(1000).Iter()
	rd, err := iter.RowData()
	if err != nil {
		iter.Close()
		return nil, err
	}
	types := []string{}
	for _, col := range iter.Columns() {
		t := strings.ToUpper(fmt.Sprint(col.TypeInfo.Type()))
		if tt, ok := col.TypeInfo.(gocql.TupleTypeInfo); ok {
			for range tt.Elems {
				types = append(types, "TUPLE")
			}
			continue
		}
		types = append(types, t)
	}
	return &cqlRows{iter: iter, sc: iter.Scanner(), rd: rd, types: types}, nil
}

type cqlRows struct {
	iter  *gocql.Iter
	sc    gocql.Scanner
	rd    gocql.RowData
	types []string
}

func (r *cqlRows) Columns() []string { return r.rd.Columns }
func (r *cqlRows) Types() []string   { return r.types }
func (r *cqlRows) Close() error      { return r.iter.Close() }
func (r *cqlRows) Next() ([]any, error) {
	if !r.sc.Next() {
		if err := r.sc.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	if err := r.sc.Scan(r.rd.Values...); err != nil {
		return nil, err
	}
	row := make([]any, len(r.rd.Values))
	for i, v := range r.rd.Values {
		row[i] = docCell(cqlValue(reflect.Indirect(reflect.ValueOf(v)).Interface()))
	}
	return row, nil
}

// cqlValue turns a scanned CQL value into a cell value: collections and
// user types as JSON, UUIDs and decimals as text.
func cqlValue(v any) any {
	switch t := v.(type) {
	case nil, string, bool, int64, float64, []byte, time.Time:
		return t
	case int:
		return int64(t)
	case int8:
		return int64(t)
	case int16:
		return int64(t)
	case int32:
		return int64(t)
	case float32:
		return float64(t)
	case gocql.UUID:
		return t.String()
	case json.Marshaler:
		return t
	case fmt.Stringer: // varint, decimal (exact), inet, duration…
		if s := t.String(); plainNumber.MatchString(s) {
			return json.Number(s)
		}
		return t.String()
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer:
		if rv.IsNil() {
			return nil
		}
		return cqlValue(rv.Elem().Interface())
	case reflect.Slice, reflect.Array:
		out := make([]any, rv.Len())
		for i := range out {
			out[i] = cqlValue(rv.Index(i).Interface())
		}
		return out
	case reflect.Map:
		d := doc{}
		for _, k := range rv.MapKeys() {
			d = append(d, field{fmt.Sprint(cqlValue(k.Interface())), cqlValue(rv.MapIndex(k).Interface())})
		}
		sort.Slice(d, func(i, j int) bool { return d[i].Key < d[j].Key })
		return d
	}
	return fmt.Sprint(v)
}
