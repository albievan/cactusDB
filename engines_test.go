package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// AWS's get-vanilla test of Signature Version 4.
func TestSignV4(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://example.amazonaws.com/", nil)
	signV4(req, nil, "AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "us-east-1", "service",
		time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC))
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestEngineRegistry(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range engines {
		if seen[e.ID] {
			t.Errorf("engine %s twice", e.ID)
		}
		seen[e.ID] = true
		if e.Backend && backendOpeners[e.ID] == nil {
			t.Errorf("backend engine %s has no opener", e.ID)
		}
		if e.Dialect != "" && engineByID(e.Dialect) == nil {
			t.Errorf("engine %s: unknown dialect %s", e.ID, e.Dialect)
		}
		if e.Language == "" || e.QueryHint == "" {
			t.Errorf("engine %s: no query language", e.ID)
		}
	}
	for id := range backendOpeners {
		if e := engineByID(id); e == nil || !e.Backend {
			t.Errorf("opener for %s, which isn't a backend engine", id)
		}
	}
	for _, id := range []string{"redis", "mongodb", "neo4j", "access", "informix", "bigquery", "cockroachdb", "couchbase",
		"cassandra", "redshift", "athena", "oracle", "cosmosdb", "horizondb", "azurepg", "azuresql", "weaviate", "pgvector"} {
		if !seen[id] {
			t.Errorf("engine %s missing", id)
		}
		if e, err := parseEngine(id); err != nil || e != id {
			t.Errorf("--dbtype %s: %q, %v", id, e, err)
		}
	}
	if dialect(EngineCockroach) != EnginePostgres || dialect(EngineAzureSQL) != EngineMSSQL || dialect(EngineMongoDB) != EngineMongoDB {
		t.Error("dialects")
	}
}

func TestParseOptions(t *testing.T) {
	o := parseOptions("workgroup=primary&output=s3://b/x/; TLS = true\nauthSource=admin")
	if o["workgroup"] != "primary" || o["output"] != "s3://b/x/" || o["tls"] != "true" || o["authsource"] != "admin" {
		t.Errorf("%v", o)
	}
	if !optBool(o, "tls", false) || optBool(o, "missing", false) || !optBool(o, "missing", true) {
		t.Error("optBool")
	}
}

func TestShellQuery(t *testing.T) {
	sq, err := parseShellQuery(`db.orders.find({ status: 'open', "total": { $gt: 10.5 }, _id: ObjectId("64b7f0c2a1b2c3d4e5f60718"),
		at: { $gte: ISODate("2024-01-02") }, name: /^ab/i }, { _id: 0 }).sort({ total: -1 }).limit(5)`)
	if err != nil {
		t.Fatal(err)
	}
	if sq.collection != "orders" || len(sq.calls) != 3 || sq.calls[0].name != "find" || sq.calls[2].name != "limit" {
		t.Fatalf("%+v", sq)
	}
	filter := sq.calls[0].args[0].(bson.D)
	if filter[0].Key != "status" || filter[0].Value != "open" || filter[2].Value.(bson.ObjectID).Hex() != "64b7f0c2a1b2c3d4e5f60718" {
		t.Errorf("%v", filter)
	}
	if r, ok := filter[4].Value.(bson.Regex); !ok || r.Pattern != "^ab" || r.Options != "i" {
		t.Errorf("regex %v", filter[4].Value)
	}
	if err := checkShellReadOnly(sq); err != nil {
		t.Error(err)
	}
	sq, err = parseShellQuery(`db.getSiblingDB("shop").getCollection("my orders").aggregate([{ $match: {} }, { $out: "x" }])`)
	if err != nil {
		t.Fatal(err)
	}
	if sq.db != "shop" || sq.collection != "my orders" {
		t.Errorf("%+v", sq)
	}
	if err := checkShellReadOnly(sq); err == nil {
		t.Error("$out accepted")
	}
	for _, q := range []string{`db.orders.insertOne({a: 1})`, `db.orders.deleteMany({})`, `db.orders.drop()`,
		`db.orders.find({}).forEach(x => x)`, `db.orders.updateOne({}, {$set: {a: 1}})`} {
		sq, err := parseShellQuery(q)
		if err == nil {
			err = checkShellReadOnly(sq)
		}
		if err == nil {
			t.Errorf("%s accepted", q)
		}
	}
	if got := mongoCollExpr(false, "", "orders"); got != "db.orders" {
		t.Error(got)
	}
	if got := mongoCollExpr(true, "shop", "a b"); got != `db.getSiblingDB("shop").getCollection("a b")` {
		t.Error(got)
	}
}

func TestRedisReadOnly(t *testing.T) {
	r := &redisBackend{}
	for _, q := range []string{"GET a", "hgetall user:1", `SCAN MATCH "user:*"`, "MEMORY USAGE k", "XINFO STREAM s", "INFO"} {
		if err := r.CheckReadOnly(q); err != nil {
			t.Errorf("%s: %v", q, err)
		}
	}
	for _, q := range []string{"SET a 1", "DEL a", "FLUSHALL", "CONFIG SET x y", "MEMORY PURGE", "EVAL 'x' 0", "GET a\nDEL a"} {
		if err := r.CheckReadOnly(q); err == nil {
			t.Errorf("%s accepted", q)
		}
	}
	args, _ := redisArgs(`SCAN MATCH "a b*" COUNT 10`)
	if s, ok := parseFullScan(args[1:]); !ok || s.pattern != "a b*" {
		t.Errorf("%v %v", s, ok)
	}
	if _, ok := parseFullScan([]string{"1234"}); ok {
		t.Error("a cursor is Redis's own page")
	}
}

func TestCypherReadOnly(t *testing.T) {
	n := &neo4jBackend{}
	for _, q := range []string{"MATCH (n:Person) RETURN n LIMIT 10", "MATCH (a)--(b) WHERE a.name = 'CREATE' RETURN b",
		"CALL db.labels() YIELD label RETURN label", "SHOW INDEXES", "MATCH (n) RETURN n.`set` AS s"} {
		if err := n.CheckReadOnly(q); err != nil {
			t.Errorf("%s: %v", q, err)
		}
	}
	for _, q := range []string{"CREATE (n:X)", "MATCH (n) DETACH DELETE n", "MATCH (n) SET n.a = 1", "MERGE (n:A)",
		"CALL apoc.create.node(['A'], {})", "LOAD CSV FROM 'x' AS r RETURN r", "MATCH (n) RETURN n; MATCH (m) DELETE m"} {
		if err := n.CheckReadOnly(q); err == nil {
			t.Errorf("%s accepted", q)
		}
	}
}

func TestSQLBackendsReadOnly(t *testing.T) {
	cb := &couchbaseBackend{}
	if err := cb.CheckReadOnly("SELECT META(c).id, c.* FROM `b`.`s`.`c` AS c WHERE c.`delete` = 'UPDATE'"); err != nil {
		t.Error(err)
	}
	for _, q := range []string{"UPSERT INTO b VALUES ('k', {})", "SELECT 1; DELETE FROM b", "CREATE INDEX i ON b(x)"} {
		if cb.CheckReadOnly(q) == nil {
			t.Errorf("%s accepted", q)
		}
	}
	cs := &cosmosBackend{db: "shop"}
	if err := cs.CheckReadOnly("-- container: shop.orders\nSELECT * FROM c WHERE c.status = 'DELETE'"); err != nil {
		t.Error(err)
	}
	if db, coll, err := cs.target("-- container: shop.orders\nSELECT * FROM c"); err != nil || db != "shop" || coll != "orders" {
		t.Errorf("%s %s %v", db, coll, err)
	}
	if db, coll, err := cs.target("SELECT * FROM orders o"); err != nil || db != "shop" || coll != "orders" {
		t.Errorf("%s %s %v", db, coll, err)
	}
	w := &weaviateBackend{}
	if w.CheckReadOnly("{ Get { Article { title } } }") != nil || w.CheckReadOnly("mutation { x }") == nil {
		t.Error("weaviate read-only")
	}
	for _, b := range []backend{&cassandraBackend{}, &athenaBackend{}, &informixBackend{}, &bigqueryBackend{}} {
		if b.CheckReadOnly("SELECT * FROM t") != nil || b.CheckReadOnly("DROP TABLE t") == nil || b.CheckReadOnly("INSERT INTO t VALUES (1)") == nil {
			t.Errorf("%T read-only", b)
		}
	}
}

func TestDocRows(t *testing.T) {
	v, err := parseOrdered([]byte(`[{"b": 1, "a": {"x": [1, 2]}}, {"c": 1.5, "b": "two"}]`))
	if err != nil {
		t.Fatal(err)
	}
	var docs []doc
	for _, d := range v.([]any) {
		docs = append(docs, d.(doc))
	}
	r, err := docsOf(docs, "_id")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.Columns(), ","); got != "_id,b,a,c" {
		t.Errorf("columns %s", got)
	}
	row, _ := r.Next()
	if row[1] != int64(1) || row[2] != `{"x":[1,2]}` || row[0] != nil {
		t.Errorf("row 1 %#v", row)
	}
	row, _ = r.Next()
	if row[1] != "two" || row[3] != json.Number("1.5") {
		t.Errorf("row 2 %#v", row)
	}
	if _, err := r.Next(); err != io.EOF {
		t.Error("no EOF")
	}
	if !strings.Contains(inferFields(docs), "a.x") {
		t.Error(inferFields(docs))
	}
}

// A fake Weaviate: the backend is browsed through database/sql like the
// other engines (objects, select, paging through every object).
func TestWeaviateFake(t *testing.T) {
	objs := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/schema":
			io.WriteString(w, `{"classes":[{"class":"Article","properties":[{"name":"title","dataType":["text"]},{"name":"author","dataType":["Person"]}]}]}`)
		case "/v1/meta":
			io.WriteString(w, `{"version":"1.30.0"}`)
		case "/v1/graphql":
			var body struct{ Query string }
			json.NewDecoder(r.Body).Decode(&body)
			if strings.Contains(body.Query, "Aggregate") {
				io.WriteString(w, `{"data":{"Aggregate":{"Article":[{"meta":{"count":1500}}]}}}`)
				return
			}
			if !strings.Contains(body.Query, "limit: 1000") {
				t.Errorf("not paged: %s", body.Query)
			}
			n := 1000
			if strings.Contains(body.Query, "after:") {
				n = 500
			}
			var items []string
			for i := 0; i < n; i++ {
				objs++
				items = append(items, `{"title":"t`+itoa(objs)+`","_additional":{"id":"id-`+itoa(objs)+`"}}`)
			}
			io.WriteString(w, `{"data":{"Get":{"Article":[`+strings.Join(items, ",")+`]}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	db, err := openDB(context.Background(), ConnParams{Engine: EngineWeaviate, Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	groups, err := listObjects(ctx, EngineWeaviate, db)
	if err != nil || len(groups) != 1 || groups[0].Items[0] != "Article" {
		t.Fatalf("%v %v", groups, err)
	}
	q, err := buildSelect(ctx, EngineWeaviate, db, "Article")
	if err != nil || !strings.Contains(q, "title") || strings.Contains(q, "author") {
		t.Fatalf("select %q %v", q, err)
	}
	n := 0
	var cols []string
	err = streamQuery(ctx, EngineWeaviate, db, q, func(c []string) error { cols = c; return nil }, func(row []any) (bool, error) {
		n++
		return true, nil
	})
	if err != nil || n != 1500 || strings.Join(cols, ",") != "_id,title" {
		t.Errorf("%d rows, columns %v, %v", n, cols, err)
	}
	if c, err := countRows(ctx, EngineWeaviate, db, "Article"); err != nil || c != 1500 {
		t.Errorf("count %d %v", c, err)
	}
	if err := streamQuery(ctx, EngineWeaviate, db, "mutation { x }", func([]string) error { return nil },
		func([]any) (bool, error) { return true, nil }); err == nil {
		t.Error("mutation accepted")
	}
	var _ *sql.DB = db
}

func itoa(i int) string { return strconv.Itoa(i) }

// collect runs q through database/sql like the web UI does.
func collect(t *testing.T, engine string, db *sql.DB, q string) ([]string, [][]any) {
	t.Helper()
	var cols []string
	var rows [][]any
	err := streamQuery(context.Background(), engine, db, q, func(c []string) error { cols = c; return nil }, func(r []any) (bool, error) {
		rows = append(rows, append([]any(nil), r...))
		return true, nil
	})
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return cols, rows
}

func TestCosmosFake(t *testing.T) {
	key := "C2y6yDjf5/R+ob0N8A7Cgv30VRDJIWEHLM+4QDU5DE2nQ9nDuVTqobD4b8mGGyPMbIZnqyMsEcaGQy67XIw/Jw=="
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "type%3Dmaster%26ver%3D1.0%26sig%3D") || r.Header.Get("x-ms-date") == "" {
			w.WriteHeader(401)
			io.WriteString(w, `{"message":"bad auth"}`)
			return
		}
		switch {
		case r.URL.Path == "/":
			io.WriteString(w, `{"id":"acct"}`)
		case r.URL.Path == "/dbs":
			io.WriteString(w, `{"Databases":[{"id":"shop"}]}`)
		case r.URL.Path == "/dbs/shop/colls":
			io.WriteString(w, `{"DocumentCollections":[{"id":"orders","partitionKey":{"paths":["/cust"]}}]}`)
		case strings.HasSuffix(r.URL.Path, "/sprocs"):
			io.WriteString(w, `{"StoredProcedures":[{"id":"bulk"}]}`)
		case r.URL.Path == "/dbs/shop/colls/orders/docs":
			if r.Header.Get("x-ms-documentdb-isquery") != "True" || r.Header.Get("Content-Type") != "application/query+json" {
				t.Errorf("query headers %v", r.Header)
			}
			pages++
			if r.Header.Get("x-ms-continuation") == "" {
				w.Header().Set("x-ms-continuation", "next")
				io.WriteString(w, `{"Documents":[{"id":"1","cust":"a","_rid":"x","_self":"y","_etag":"z","_attachments":"q","_ts":1}]}`)
			} else {
				io.WriteString(w, `{"Documents":[{"id":"2","cust":"b","total":12.5}]}`)
			}
		case r.URL.Path == "/dbs/shop/colls/orders":
			w.Header().Set("x-ms-resource-usage", "documentsSize=1;documentsCount=42;collectionSize=3")
			io.WriteString(w, `{"id":"orders"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	db, err := openDB(context.Background(), ConnParams{Engine: EngineCosmosDB, Host: srv.URL, Password: key})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	groups, err := listObjects(context.Background(), EngineCosmosDB, db)
	if err != nil || groups[0].Items[0] != "shop.orders" || groups[1].Items[0] != "shop.orders.bulk" {
		t.Fatalf("%v %v", groups, err)
	}
	q, _ := buildSelect(context.Background(), EngineCosmosDB, db, "shop.orders")
	cols, rows := collect(t, EngineCosmosDB, db, q)
	if strings.Join(cols, ",") != "id,cust,_ts,total" || len(rows) != 2 || pages != 2 {
		t.Errorf("%v %v pages=%d", cols, rows, pages)
	}
	if n, _ := countRows(context.Background(), EngineCosmosDB, db, "shop.orders"); n != 42 {
		t.Errorf("count %d", n)
	}
}

func TestCouchbaseFake(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["readonly"] != true {
			t.Errorf("not readonly: %v", body)
		}
		st := body["statement"].(string)
		switch {
		case strings.Contains(st, "system:keyspaces"):
			io.WriteString(w, `{"requestID":"1","results":["travel.inventory.airline"],"status":"success"}`)
		case strings.Contains(st, "system:indexes"):
			io.WriteString(w, `{"results":[],"status":"success"}`)
		case strings.Contains(st, "bad"):
			io.WriteString(w, `{"requestID":"1","errors":[{"code":3000,"msg":"syntax error - at bad"}],"status":"fatal"}`)
		default:
			io.WriteString(w, `{"requestID":"1","signature":{"*":"*"},"results":[{"_id":"a1","name":"Air","country":"FR"},{"_id":"a2","name":"Jet","iata":"JT"}],"status":"success","metrics":{"resultCount":2}}`)
		}
	}))
	defer srv.Close()
	db, err := openDB(context.Background(), ConnParams{Engine: EngineCouchbase, Host: srv.URL, Username: "u", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	groups, err := listObjects(context.Background(), EngineCouchbase, db)
	if err != nil || groups[0].Items[0] != "travel.inventory.airline" {
		t.Fatalf("%v %v", groups, err)
	}
	q, _ := buildSelect(context.Background(), EngineCouchbase, db, "travel.inventory.airline")
	if !strings.Contains(q, "`travel`.`inventory`.`airline`") {
		t.Error(q)
	}
	cols, rows := collect(t, EngineCouchbase, db, q)
	if strings.Join(cols, ",") != "_id,name,country,iata" || len(rows) != 2 {
		t.Errorf("%v %v", cols, rows)
	}
	err = streamQuery(context.Background(), EngineCouchbase, db, "SELECT bad", func([]string) error { return nil }, func([]any) (bool, error) { return true, nil })
	if err == nil || !strings.Contains(err.Error(), "syntax error") {
		t.Errorf("error not reported: %v", err)
	}
}

func TestBigQueryFake(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/bigquery/v2/projects/proj/datasets" || p == "/bigquery/v2/projects/proj/datasets/sales":
			io.WriteString(w, `{"datasets":[{"datasetReference":{"datasetId":"sales"}}]}`)
		case strings.HasSuffix(p, "/datasets/sales/tables"):
			io.WriteString(w, `{"tables":[{"tableReference":{"tableId":"orders"},"type":"TABLE"},{"tableReference":{"tableId":"v1"},"type":"VIEW"}]}`)
		case strings.HasSuffix(p, "/routines"):
			io.WriteString(w, `{}`)
		case strings.HasSuffix(p, "/tables/orders"):
			io.WriteString(w, `{"type":"TABLE","numRows":"3","schema":{"fields":[{"name":"id","type":"INTEGER","mode":"REQUIRED"},{"name":"tags","type":"STRING","mode":"REPEATED"},{"name":"addr","type":"RECORD","fields":[{"name":"city","type":"STRING"}]}]}}`)
		case p == "/bigquery/v2/projects/proj/jobs":
			var job map[string]any
			json.NewDecoder(r.Body).Decode(&job)
			q := job["configuration"].(map[string]any)["query"].(map[string]any)["query"].(string)
			st := "SELECT"
			if strings.HasPrefix(q, "DELETE") {
				st = "DELETE"
			}
			io.WriteString(w, `{"statistics":{"query":{"statementType":"`+st+`"}}}`)
		case p == "/bigquery/v2/projects/proj/queries":
			io.WriteString(w, `{"jobReference":{"jobId":"j1"},"jobComplete":true,"pageToken":"p2","schema":{"fields":[{"name":"id","type":"INTEGER"},{"name":"amount","type":"NUMERIC"},{"name":"at","type":"TIMESTAMP"},{"name":"tags","type":"STRING","mode":"REPEATED"},{"name":"addr","type":"RECORD","fields":[{"name":"city","type":"STRING"}]}]},
				"rows":[{"f":[{"v":"1"},{"v":"10.25"},{"v":"1700000000000000"},{"v":[{"v":"a"},{"v":"b"}]},{"v":{"f":[{"v":"Paris"}]}}]}]}`)
		case p == "/bigquery/v2/projects/proj/queries/j1":
			io.WriteString(w, `{"jobReference":{"jobId":"j1"},"jobComplete":true,"schema":{"fields":[{"name":"id","type":"INTEGER"},{"name":"amount","type":"NUMERIC"},{"name":"at","type":"TIMESTAMP"},{"name":"tags","type":"STRING","mode":"REPEATED"},{"name":"addr","type":"RECORD","fields":[{"name":"city","type":"STRING"}]}]},
				"rows":[{"f":[{"v":"2"},{"v":null},{"v":null},{"v":[]},{"v":null}]}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	db, err := openDB(context.Background(), ConnParams{Engine: EngineBigQuery, Host: "proj", Options: "endpoint=" + srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	groups, err := listObjects(context.Background(), EngineBigQuery, db)
	if err != nil || groups[0].Items[0] != "sales.orders" || groups[1].Items[0] != "sales.v1" {
		t.Fatalf("%v %v", groups, err)
	}
	def, _ := objectDefinition(context.Background(), EngineBigQuery, db, ObjTable, "sales.orders")
	if !strings.Contains(def, "`id` INT64 NOT NULL") || !strings.Contains(def, "ARRAY<STRING>") || !strings.Contains(def, "STRUCT<`city` STRING>") {
		t.Error(def)
	}
	_, rows := collect(t, EngineBigQuery, db, "SELECT * FROM `proj.sales.orders`")
	if len(rows) != 2 || rows[0][0] != int64(1) || rows[0][1] != json.Number("10.25") || rows[0][2] != "2023-11-14 22:13:20" ||
		rows[0][3] != `["a","b"]` || rows[0][4] != `{"city":"Paris"}` || rows[1][0] != int64(2) {
		t.Errorf("%#v", rows)
	}
	err = streamQuery(context.Background(), EngineBigQuery, db, "DELETE FROM x WHERE true", func([]string) error { return nil }, func([]any) (bool, error) { return true, nil })
	if err == nil {
		t.Error("DELETE accepted")
	}
}

func TestAthenaFake(t *testing.T) {
	var actions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKID/") {
			w.WriteHeader(403)
			io.WriteString(w, `{"__type":"UnrecognizedClientException","message":"bad signature"}`)
			return
		}
		a := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "AmazonAthena.")
		actions = append(actions, a)
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		switch a {
		case "GetWorkGroup":
			io.WriteString(w, `{"WorkGroup":{"Configuration":{"EngineVersion":{"EffectiveEngineVersion":"Athena engine version 3"}}}}`)
		case "ListDatabases":
			io.WriteString(w, `{"DatabaseList":[{"Name":"logs"}]}`)
		case "ListTableMetadata":
			io.WriteString(w, `{"TableMetadataList":[{"Name":"hits","TableType":"EXTERNAL_TABLE"},{"Name":"daily","TableType":"VIRTUAL_VIEW"}]}`)
		case "StartQueryExecution":
			if in["WorkGroup"] != "wg1" {
				t.Errorf("workgroup %v", in["WorkGroup"])
			}
			io.WriteString(w, `{"QueryExecutionId":"q1"}`)
		case "GetQueryExecution":
			io.WriteString(w, `{"QueryExecution":{"StatementType":"DML","Status":{"State":"SUCCEEDED"}}}`)
		case "GetQueryResults":
			meta := `"ResultSetMetadata":{"ColumnInfo":[{"Name":"n","Type":"bigint"},{"Name":"s","Type":"varchar"}]}`
			if in["NextToken"] == nil {
				io.WriteString(w, `{"NextToken":"t2","ResultSet":{`+meta+`,"Rows":[{"Data":[{"VarCharValue":"n"},{"VarCharValue":"s"}]},{"Data":[{"VarCharValue":"1"},{"VarCharValue":"a"}]}]}}`)
			} else {
				io.WriteString(w, `{"ResultSet":{`+meta+`,"Rows":[{"Data":[{"VarCharValue":"2"},{}]}]}}`)
			}
		}
	}))
	defer srv.Close()
	db, err := openDB(context.Background(), ConnParams{Engine: EngineAthena, Host: "eu-west-1", Username: "AKID", Password: "secret",
		Options: "workgroup=wg1&endpoint=" + srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	groups, err := listObjects(context.Background(), EngineAthena, db)
	if err != nil || groups[0].Items[0] != "logs.hits" || groups[1].Items[0] != "logs.daily" {
		t.Fatalf("%v %v", groups, err)
	}
	cols, rows := collect(t, EngineAthena, db, `SELECT * FROM "logs"."hits"`)
	if strings.Join(cols, ",") != "n,s" || len(rows) != 2 || rows[0][0] != int64(1) || rows[1][1] != nil {
		t.Errorf("%v %#v", cols, rows)
	}
	if ver := serverVersion(context.Background(), EngineAthena, db); !strings.Contains(ver, "engine version 3") {
		t.Error(ver)
	}
}

func TestPgvectorDSN(t *testing.T) {
	e := engineByID(EnginePgvector)
	if e == nil || e.Group != "Vector" || dialect(EnginePgvector) != EnginePostgres {
		t.Fatal("pgvector engine")
	}
	dsn := postgresDSN(ConnParams{Engine: EnginePgvector, Host: "db", Username: "u", Database: "emb"}, parseOptions("sslmode=require"))
	if !strings.Contains(dsn, "db:5432/emb") || !strings.Contains(dsn, "default_transaction_read_only=on") || !strings.Contains(dsn, "sslmode=require") {
		t.Error(dsn)
	}
	rs := postgresDSN(ConnParams{Engine: EngineRedshift, Host: "c"}, nil)
	if !strings.Contains(rs, "c:5439") || strings.Contains(rs, "default_transaction_read_only") {
		t.Error(rs)
	}
}

func TestContainerDeployChecks(t *testing.T) {
	sp, _ := specByID("mssql")
	ok := deployReq{Runtime: "docker", Spec: "mssql", Name: "cdb-mssql", Port: 1433, Password: "Str0ng!pass"}
	if err := ok.check(sp); err != nil {
		t.Fatalf("valid request refused: %v", err)
	}
	if ok.Image != sp.Image || ok.Username != "sa" {
		t.Errorf("defaults not filled: %+v", ok)
	}
	bad := []deployReq{
		{Runtime: "docker", Name: "Bad_Name", Port: 1433, Password: "Str0ng!pass"},
		{Runtime: "docker", Name: "cdb", Port: 1433, Password: "weakpass"},
		{Runtime: "docker", Name: "cdb", Port: 0, Password: "Str0ng!pass"},
		{Runtime: "docker", Name: "cdb", Port: 1433, Password: "Str0ng!pass", Image: "--privileged"},
		{Runtime: "docker", Name: "cdb", Port: 1433, Password: "Str0ng!pass", DataDir: "relative/dir"},
		{Runtime: "docker", Name: "cdb", Port: 1433, Password: "Str0ng!pass", DataDir: "/"},
		{Runtime: "kubernetes", Name: "cdb", Port: 1433, Password: "Str0ng!pass", ServiceType: "NodePort"},
		{Runtime: "kubernetes", Name: "cdb", Port: 1433, Password: "Str0ng!pass", Storage: "lots"},
	}
	for i, d := range bad {
		if err := d.check(sp); err == nil {
			t.Errorf("bad request %d accepted: %+v", i, d)
		}
	}
	pg, _ := specByID("postgres")
	d := deployReq{Runtime: "docker", Name: "pg", Port: 5432, Password: "x", Username: "app; drop", Database: "db"}
	if d.check(pg) == nil {
		t.Error("a username with ; was accepted")
	}
	cr, _ := specByID("cockroachdb")
	d = deployReq{Runtime: "docker", Name: "crdb", Port: 26257, Password: "ignored"}
	if err := d.check(cr); err != nil || d.Password != "" || d.Database != "defaultdb" {
		t.Errorf("cockroachdb: %v %+v", err, d)
	}
	for _, s := range containerCatalog {
		if s.env == nil || s.Image == "" || s.Port == 0 || s.DataPath == "" || engineByID(s.Engine) == nil {
			t.Errorf("catalogue entry %s is incomplete", s.ID)
		}
	}
}

func TestDockerRunArgsKeepPasswordsOut(t *testing.T) {
	sp, _ := specByID("mssql")
	d := deployReq{Runtime: "docker", Spec: "mssql", Name: "cdb-mssql", Port: 14330, Password: "Str0ng!secret", DataDir: "/srv/data/mssql"}
	if err := d.check(sp); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(dockerRunArgs(sp, d), " ")
	if strings.Contains(args, "Str0ng!secret") {
		t.Fatalf("the password is on the command line: %s", args)
	}
	for _, want := range []string{"-p 127.0.0.1:14330:1433", "-v /srv/data/mssql:/var/opt/mssql", "-e MSSQL_SA_PASSWORD ", "-e ACCEPT_EULA=Y", "--label " + ctrLabel + "=true"} {
		if !strings.Contains(args+" ", want) {
			t.Errorf("missing %q in %s", want, args)
		}
	}
	if env := secretEnv(sp, d); len(env) != 1 || env[0] != "MSSQL_SA_PASSWORD=Str0ng!secret" {
		t.Errorf("secret env = %v", env)
	}
	d.Public, d.DataDir = true, ""
	args = strings.Join(dockerRunArgs(sp, d), " ")
	if !strings.Contains(args, "-p 14330:1433") || !strings.Contains(args, "-v cdb-mssql-data:/var/opt/mssql") {
		t.Errorf("public/volume args wrong: %s", args)
	}
}

func TestKubeManifest(t *testing.T) {
	sp, _ := specByID("mssql")
	d := deployReq{Runtime: "kubernetes", Spec: "mssql", Name: "cdb-mssql", Port: 31433, Password: "Str0ng!secret", ServiceType: "NodePort", DataDir: "/srv/mssql"}
	if err := d.check(sp); err != nil {
		t.Fatal(err)
	}
	var m struct {
		Items []map[string]any
	}
	if err := json.Unmarshal(kubeManifest(sp, d), &m); err != nil {
		t.Fatal(err)
	}
	kinds := []string{}
	for _, it := range m.Items {
		kinds = append(kinds, it["kind"].(string))
	}
	if got := strings.Join(kinds, ","); got != "Namespace,Secret,Deployment,Service" {
		t.Errorf("kinds = %s", got)
	}
	raw := string(kubeManifest(sp, d))
	// The password is only in the Secret, which the deployment refers to.
	if strings.Count(raw, "Str0ng!secret") != 1 || !strings.Contains(raw, `"secretKeyRef"`) {
		t.Errorf("password not kept in the secret only:\n%s", raw)
	}
	for _, want := range []string{`"nodePort": 31433`, `"type": "DirectoryOrCreate"`, `"fsGroup": 10001`, `"chown"`, `"namespace": "cactusdb"`} {
		if !strings.Contains(raw, want) {
			t.Errorf("manifest lacks %s", want)
		}
	}
	d = deployReq{Runtime: "kubernetes", Spec: "redis", Name: "r", Port: 6379, Password: "pw"}
	rs, _ := specByID("redis")
	if err := d.check(rs); err != nil {
		t.Fatal(err)
	}
	raw = string(kubeManifest(rs, d))
	if !strings.Contains(raw, `"PersistentVolumeClaim"`) || !strings.Contains(raw, `"storage": "10Gi"`) || !strings.Contains(raw, `"LoadBalancer"`) {
		t.Errorf("redis manifest:\n%s", raw)
	}
}

func TestPortFree(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	if portFree(port, false) || portFree(port, true) {
		t.Errorf("port %d is in use but looks free", port)
	}
}

// Cypher: a CALL must name a read-only procedure in plain letters, and
// properties or labels named like clauses are not clauses.
func TestCypherReadOnlyEdges(t *testing.T) {
	n := &neo4jBackend{}
	for _, q := range []string{
		"CALL `apoc`.`load`.`json`('http://169.254.169.254/')",
		"CALL apoc.`export`.csv.all('x', {})",
		"CALL `dbms`.setConfigValue('a','b')",
		"MATCH (n) CALL apoc.create.node(['X'], {}) YIELD node RETURN node",
		"MATCH (n) SET n.x = 1",
	} {
		if n.CheckReadOnly(q) == nil {
			t.Errorf("%q allowed", q)
		}
	}
	for _, q := range []string{
		"MATCH (e:Event) RETURN e.start, e.set, e.load",
		"MATCH (n:Start) RETURN n {load: 1}",
		"CALL db.labels() YIELD label RETURN label",
		"CALL db.labels",
		"CALL { MATCH (n) RETURN n } RETURN n",
	} {
		if err := n.CheckReadOnly(q); err != nil {
			t.Errorf("%q: %v", q, err)
		}
	}
}

// Redis command words keep their bytes: UTF-8 keys and \xHH escapes.
func TestRedisArgsBytes(t *testing.T) {
	for in, want := range map[string][]string{
		`GET café`:                       {"GET", "café"},
		`GET "naïve key"`:                {"GET", "naïve key"},
		`GET "\xff\x00k"`:                {"GET", "\xff\x00k"},
		`GET ` + strconv.Quote("a\xffb"): {"GET", "a\xffb"},
	} {
		got, err := redisArgs(in)
		if err != nil || strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s: %q %v", in, got, err)
		}
	}
}

// A lone quote in an Access query is an error, not a crash.
func TestAccessLoneQuote(t *testing.T) {
	for _, q := range []string{"SELECT ` FROM t", `SELECT a FROM t WHERE a = '`, `SELECT a FROM t WHERE a = "`, "SELECT [ FROM t"} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%q panics: %v", q, r)
				}
			}()
			parseAccessQuery(q)
		}()
	}
}

// Values JSON can't hold are shown by name; a DATE keeps a time of day.
func TestNormalizeEdges(t *testing.T) {
	for _, c := range []struct {
		v    any
		typ  string
		want any
	}{
		{math.NaN(), "FLOAT8", "NaN"},
		{math.Inf(1), "DOUBLE", "Infinity"},
		{"NaN", "NUMERIC", "NaN"},
		{"Infinity", "NUMERIC", "Infinity"},
		{"12.50", "NUMERIC", json.Number("12.50")},
		{"007", "NUMERIC", "007"},
		{time.Date(2024, 5, 1, 13, 45, 0, 0, time.UTC), "DATE", "2024-05-01 13:45:00"},
		{time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC), "DATE", "2024-05-01"},
	} {
		if got := normalize(c.v, c.typ); got != c.want {
			t.Errorf("%v %s: %#v, want %#v", c.v, c.typ, got, c.want)
		}
	}
}

// Shell values: a / inside a regex class, an emoji as a surrogate pair,
// new Date(0), and a collation that is applied.
func TestShellQueryEdges(t *testing.T) {
	sq, err := parseShellQuery(`db.t.find({ p: /^a[/]b$/i, e: "😀", d: new Date(0) }).collation({ locale: "en", strength: 2 })`)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := sq.calls[0].args[0].(bson.D)
	m := map[string]any{}
	for _, e := range d {
		m[e.Key] = e.Value
	}
	if re, _ := m["p"].(bson.Regex); re.Pattern != "^a[/]b$" || re.Options != "i" {
		t.Errorf("regex = %#v", m["p"])
	}
	if m["e"] != "😀" {
		t.Errorf("emoji = %q", m["e"])
	}
	if tm, _ := m["d"].(time.Time); !tm.Equal(time.UnixMilli(0)) {
		t.Errorf("date = %v", m["d"])
	}
	c, err := mongoCollation(sq.calls[1].args[0])
	if err != nil || c.Locale != "en" || c.Strength != 2 {
		t.Errorf("collation = %+v %v", c, err)
	}
}
