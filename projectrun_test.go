package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A project over two databases: one SELECT joins them, with a LEFT JOIN that
// finds nothing and a NULL key; filters go to the source; limits stop the
// run; exports hold every row; write statements never run.
func TestProjectRun(t *testing.T) {
	s := newTestServer(t)
	fb := newSQLiteDB(t, "fb.db",
		`CREATE TABLE customer (id INTEGER PRIMARY KEY, name TEXT NOT NULL, city TEXT)`,
		`INSERT INTO customer VALUES (1, 'Ann', 'Cape Town'), (2, 'Bob', 'Durban'), (3, 'Cleo', NULL)`)
	maria := newSQLiteDB(t, "maria.db",
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER, order_date DATE, total NUMERIC(10,2))`,
		`INSERT INTO orders VALUES (10, 1, '2026-02-01', 100.50), (11, 1, '2026-03-05', 20), (12, 2, '2025-12-30', 7.25), (13, NULL, '2026-01-15', 1)`,
		`CREATE TABLE invoices (id INTEGER PRIMARY KEY, order_id INTEGER, amount NUMERIC(12,2))`,
		`INSERT INTO invoices VALUES (100, 10, 100.50)`)
	idA, boxA, _ := s.store.SaveLocked(SavedConn{Name: "fb", ConnParams: ConnParams{Engine: EngineSQLite, Database: fb}}, "password A", nil)
	idB, boxB, _ := s.store.SaveLocked(SavedConn{Name: "maria", ConnParams: ConnParams{Engine: EngineSQLite, Database: maria}}, "password B", nil)
	d := projectData{Links: []projectLink{{idA, "fb"}, {idB, "maria"}}, Tables: []projectTable{
		{Alias: "fb", Source: "customer", Name: "customer"}, {Alias: "maria", Source: "orders", Name: "orders"},
		{Alias: "maria", Source: "invoices", Name: "invoices"}}}
	checkProjectData(&d)
	pid, _, err := s.store.CreateProject("Sales", d, map[int64]*secretBox{idA: boxA, idB: boxB})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.projectRoutes(mux)
	call := func(method, path string, body any, want int) map[string]any {
		t.Helper()
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, jsonReq(method, path, bytes.NewReader(b)))
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != want {
			t.Fatalf("%s %s: status %d (want %d): %s", method, path, rec.Code, want, rec.Body.String())
		}
		return out
	}
	open := call("POST", "/api/projects/"+strconv.FormatInt(pid, 10)+"/open", map[string]any{"lockPasswords": map[string]string{
		strconv.FormatInt(idA, 10): "password A", strconv.FormatInt(idB, 10): "password B"}}, 200)
	ps := open["project"].(string)
	run := func(q string) map[string]any {
		t.Helper()
		call("POST", "/api/project/run", map[string]any{"project": ps, "sql": q}, 200)
		for i := 0; i < 200; i++ {
			st := call("GET", "/api/project/run?project="+ps, nil, 200)
			if st["state"] != "loading" && st["state"] != "querying" {
				return st
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("the run didn't finish")
		return nil
	}

	q := `SELECT c.name, o.id, o.order_date, i.amount
FROM fb.customer c
JOIN maria.orders o ON o.customer_id = c.id
LEFT JOIN maria.invoices i ON i.order_id = o.id
WHERE o.order_date >= '2026-01-01'
ORDER BY o.id`
	st := run(q)
	if st["state"] != "done" {
		t.Fatalf("run: %v", st)
	}
	res := st["result"].(map[string]any)
	if got := toJSON(res["rows"]); got != `[["Ann",10,"2026-02-01",100.5],["Ann",11,"2026-03-05",null]]` {
		t.Errorf("rows: %s", got)
	}
	tables := toJSON(st["tables"])
	// 3 of the 4 orders are read: the filter went to the source.
	if !strings.Contains(tables, `"name":"orders","pushed":["o.order_date \u003e= '2026-01-01'"],"rows":3`) {
		t.Errorf("pushdown: %s", tables)
	}
	// The order without a customer and the customer without orders: NULL keys
	// don't join, and a LEFT JOIN keeps the customer.
	st = run(`SELECT c.name, COUNT(o.id) AS n FROM fb.customer c LEFT JOIN maria.orders o ON o.customer_id = c.id GROUP BY c.name ORDER BY c.name`)
	if got := toJSON(st["result"].(map[string]any)["rows"]); got != `[["Ann",2],["Bob",1],["Cleo",0]]` {
		t.Errorf("left join: %s", got)
	}

	// Exports: all four formats, every row, from the staged copy.
	call("POST", "/api/project/export", map[string]any{"project": ps, "sql": q + " ", "format": "csv"}, 409) // changed text
	st = run(`SELECT * FROM maria.orders ORDER BY id`)
	ex := call("POST", "/api/project/export", map[string]any{"project": ps, "sql": `SELECT * FROM maria.orders ORDER BY id`, "format": "all", "name": "orders"}, 200)
	files := ex["files"].([]any)
	if len(files) != 4 {
		t.Fatalf("all formats: %v", ex)
	}
	for _, f := range files {
		m := f.(map[string]any)
		if m["rows"].(float64) != 4 || !strings.Contains(m["path"].(string), "/projects/Sales/") {
			t.Errorf("export: %v", m)
		}
		if fi, err := os.Stat(m["path"].(string)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("export file: %v %v", fi, err)
		}
	}

	// Downloading needs the open project (it links password-protected
	// connections); the files are listed under Exported files, protected.
	link := files[0].(map[string]any)["download"].(string)
	if !strings.HasPrefix(link, "/api/exports/projects/Sales/") {
		t.Fatalf("download link: %s", link)
	}
	dl := http.NewServeMux()
	dl.HandleFunc("GET /api/exports/{path...}", s.handleDownload)
	rec := httptest.NewRecorder()
	dl.ServeHTTP(rec, httptest.NewRequest("GET", link, nil))
	if rec.Code != 403 {
		t.Errorf("download without the project: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	dl.ServeHTTP(rec, httptest.NewRequest("GET", link+"?project="+ps, nil))
	if rec.Code != 200 || rec.Body.Len() == 0 {
		t.Errorf("download with the project: %d", rec.Code)
	}
	listed := 0
	for _, f := range s.listExportedFiles() {
		if f.Conn == "projects/Sales" {
			listed++
		}
	}
	if listed != 4 {
		t.Errorf("listed under Exported files: %d", listed)
	}
	if _, ok := s.protectedFolders()["projects/Sales"]; !ok {
		t.Error("the project's folder isn't protected")
	}

	// Refused before anything is read.
	for _, bad := range []string{"DELETE FROM maria.orders", "SELECT 1; DROP TABLE x", "ATTACH 'x' AS y", "PRAGMA query_only = 0",
		"SELECT * FROM maria.nope", "SELECT * FROM orders"} {
		call("POST", "/api/project/run", map[string]any{"project": ps, "sql": bad}, 400)
	}
	add := call("POST", "/api/project/run", map[string]any{"project": ps, "sql": "SELECT * FROM fb.notes"}, 400)
	if toJSON(add["addTable"]) != `{"alias":"fb","table":"notes"}` {
		t.Errorf("add-to-project hint: %v", add)
	}
	// The source is unchanged.
	if n := rowsIn(t, maria, "orders"); n != 4 {
		t.Errorf("orders changed: %d", n)
	}

	// The row limit stops the run, naming the table.
	d.Limits.MaxRows = 2
	ps2 := open["project"].(string)
	call("POST", "/api/project/save", map[string]any{"project": ps2, "name": "Sales", "links": d.Links, "tables": d.Tables, "limits": d.Limits}, 200)
	st = run(`SELECT * FROM maria.orders`)
	if st["state"] != "failed" || st["errorKind"] != "limit" || !strings.Contains(st["error"].(string), "maria.orders has more than 2 rows") {
		t.Errorf("row limit: %v", st)
	}

	// Closing removes the staging.
	s.mu.Lock()
	dir := s.projects[ps].run.dir
	s.mu.Unlock()
	call("POST", "/api/project/close", map[string]any{"project": ps}, 200)
	if _, err := os.Stat(dir); dir == "" || !os.IsNotExist(err) {
		t.Errorf("staging %q left behind: %v", dir, err)
	}
}

func rowsIn(t *testing.T, path, table string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestStageValues(t *testing.T) {
	dec := &mCol{Kind: kDecimal}
	for in, want := range map[string]any{"100.50": 100.5, "7": int64(7), "123456789012345678.12": "123456789012345678.12", "-0.001": -0.001} {
		if got := stageValue(in, dec, false); got != want {
			t.Errorf("%s: %#v, want %#v", in, got, want)
		}
	}
	if got := stageValue("ab  ", &mCol{Kind: kChar}, true); got != "ab" {
		t.Errorf("Firebird CHAR: %q", got)
	}
	if got := stageValue(true, &mCol{Kind: kBool}, false); got != int64(1) {
		t.Errorf("bool: %#v", got)
	}
	if got := stageValue(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), &mCol{Kind: kTimestamp}, false); got != "2026-01-02 03:04:05" {
		t.Errorf("timestamp: %#v", got)
	}
}
