package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// newShopDB creates an SQLite database with tables, an index, a view and a trigger.
func newShopDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shop.db")
	db, err := sql.Open("sqlite", sqliteURI(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE customer (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
		CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INT REFERENCES customer(id), total REAL);
		CREATE INDEX ix_cust ON orders(customer_id);
		CREATE VIEW v_big AS SELECT * FROM orders WHERE total > 100;
		CREATE TRIGGER trg_neg BEFORE INSERT ON orders BEGIN SELECT RAISE(ABORT, 'negative') WHERE NEW.total < 0; END;
		INSERT INTO customer VALUES (1, 'Ann'), (2, 'Bob'), (3, 'Cy');
		INSERT INTO orders VALUES (1, 1, 50), (2, 2, 150);`)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWithoutFlag(t *testing.T) {
	in := []string{"--port", "8087", "--runas", "background", "-runas=background", "--logfile", "x.log", "--runas=bg"}
	want := []string{"--port", "8087", "--logfile", "x.log"}
	if got := withoutFlag(in, "runas"); !reflect.DeepEqual(got, want) {
		t.Errorf("withoutFlag = %q, want %q", got, want)
	}
}

func TestGroupDigits(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -4500: "-4,500"} {
		if got := groupDigits(n); got != want {
			t.Errorf("groupDigits(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestCLIConnName(t *testing.T) {
	for _, c := range []struct {
		p    ConnParams
		want string
	}{
		{ConnParams{Engine: EngineMariaDB, Host: "10.66.1.136"}, "cli-10.66.1.136"},
		{ConnParams{Engine: EnginePostgres}, "cli-localhost"},
		{ConnParams{Engine: EngineSQLite, Database: "/data/shop.db"}, "cli-shop.db"},
	} {
		if got := cliConnName(c.p); got != c.want {
			t.Errorf("cliConnName(%+v) = %q, want %q", c.p, got, c.want)
		}
	}
}

func TestTerminate(t *testing.T) {
	for _, c := range []struct{ engine, typ, src, want string }{
		{EngineSQLite, ObjTable, "CREATE TABLE t (a)", "CREATE TABLE t (a);\n"},
		{EnginePostgres, ObjTable, "CREATE TABLE t (a);", "CREATE TABLE t (a);\n"},
		{EngineMSSQL, ObjProcedure, "CREATE PROCEDURE p AS SELECT 1;", "CREATE PROCEDURE p AS SELECT 1\nGO\n"},
		{EngineFirebird, ObjProcedure, "CREATE PROCEDURE P AS BEGIN END", "SET TERM ^ ;\nCREATE PROCEDURE P AS BEGIN END^\nSET TERM ; ^\n"},
		{EngineMariaDB, ObjTrigger, "CREATE TRIGGER x BEGIN SET @a = 1; END;", "DELIMITER $$\nCREATE TRIGGER x BEGIN SET @a = 1; END$$\nDELIMITER ;\n"},
		{EngineFirebird, ObjSequence, "CREATE SEQUENCE G;\n-- current value: 5", "CREATE SEQUENCE G;\n-- current value: 5\n"},
	} {
		if got := terminate(c.engine, c.typ, c.src); got != c.want {
			t.Errorf("terminate(%s, %s, %q) =\n%q\nwant\n%q", c.engine, c.typ, c.src, got, c.want)
		}
	}
}

// The DDL script of an SQLite database recreates the same schema.
func TestWriteDDLRoundTrip(t *testing.T) {
	src := newShopDB(t)
	ctx := context.Background()
	db, err := openDB(ctx, ConnParams{Engine: EngineSQLite, Database: src})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	groups, err := listObjects(ctx, EngineSQLite, db)
	if err != nil {
		t.Fatal(err)
	}
	targets := ddlTargets(groups)
	var order []string
	for _, t := range targets {
		order = append(order, t.typ)
	}
	if want := []string{ObjTable, ObjTable, ObjIndex, ObjView, ObjTrigger}; !reflect.DeepEqual(order, want) {
		t.Errorf("DDL order = %v, want %v", order, want)
	}
	var script bytes.Buffer
	n, err := writeDDL(ctx, &script, EngineSQLite, db, "shop", targets, nil)
	if err != nil || n != 5 {
		t.Fatalf("writeDDL: %d objects, %v", n, err)
	}

	copyPath := filepath.Join(t.TempDir(), "copy.db")
	cp, _ := sql.Open("sqlite", sqliteURI(copyPath))
	defer cp.Close()
	if _, err := cp.Exec(script.String()); err != nil {
		t.Fatalf("running the DDL script: %v\n%s", err, script.String())
	}
	schema := func(d *sql.DB) []string {
		s, err := queryStrings(ctx, d, `SELECT type || ' ' || name || ' ' || sql FROM sqlite_master ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if a, b := schema(db), schema(cp); !reflect.DeepEqual(a, b) {
		t.Errorf("schema differs after round trip:\n%v\n%v", a, b)
	}
}

func TestExportBatchParallel(t *testing.T) {
	ctx := context.Background()
	db, err := openDB(ctx, ConnParams{Engine: EngineSQLite, Database: newShopDB(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := t.TempDir()
	targets := []tableRef{{ObjTable, "customer"}, {ObjTable, "orders"}, {ObjView, "v_big"}}
	var mu sync.Mutex
	var finished []string
	b := startExportBatch(ctx, EngineSQLite, db, dir, "csv", targets, 3, func(j *exportJob) {
		mu.Lock() // called from the worker goroutines
		finished = append(finished, j.table.name)
		mu.Unlock()
	})
	select {
	case <-b.done:
	case <-time.After(30 * time.Second):
		t.Fatal("export batch did not finish")
	}
	want := map[string]int64{"customer": 3, "orders": 2, "v_big": 1}
	for _, j := range b.jobs {
		if j.err != nil || j.state.Load() != jobDone || j.rows.Load() != want[j.table.name] || j.total.Load() != want[j.table.name] {
			t.Errorf("%s: state %d rows %d total %d err %v", j.table.name, j.state.Load(), j.rows.Load(), j.total.Load(), j.err)
		}
		if _, err := os.Stat(j.path); err != nil {
			t.Errorf("%s: %v", j.table.name, err)
		}
	}
	if len(finished) != 3 {
		t.Errorf("finished callbacks: %v", finished)
	}
}

func TestRunCLISavesConnection(t *testing.T) {
	dir := t.TempDir()
	st, err := openStore(filepath.Join(dir, "c.db"), filepath.Join(dir, "c.key"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.db.Close()
	path := newShopDB(t)
	var out bytes.Buffer
	err = runCLI(context.Background(), cliOptions{ConnParams: ConnParams{Engine: EngineSQLite, Database: path},
		Show: "all", Store: st}, &out)
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetByName("cli-shop.db")
	if err != nil || c.Database != path || !strings.Contains(out.String(), `saved as "cli-shop.db"`) {
		t.Errorf("saved connection: %+v %v\n%s", c, err, out.String())
	}
}

func newTestServer(t *testing.T) *server {
	t.Helper()
	dir := t.TempDir()
	st, err := openStore(filepath.Join(dir, "c.db"), filepath.Join(dir, "c.key"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.db.Close() })
	return &server{store: st, exportDir: filepath.Join(dir, "export"), sessions: map[string]*session{}}
}

// Editing a saved connection with an id can rename it and keep its password.
func TestAPISaveRenameKeepsPassword(t *testing.T) {
	s := newTestServer(t)
	id, _ := s.store.Save(SavedConn{Name: "old", ConnParams: ConnParams{Engine: EngineMySQL, Host: "h", Password: testPass}})
	rec := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]any{"id": id, "name": "new", "engine": "mysql", "host": "h2", "keepPassword": true})
	s.handleSaveConn(rec, httptest.NewRequest("POST", "/api/connections", bytes.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	list, _ := s.store.List()
	if len(list) != 1 || list[0].Name != "new" || list[0].Password != testPass || list[0].Host != "h2" {
		t.Errorf("after rename: %+v", list)
	}
}

func TestAPITestConn(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]any{"engine": "sqlite", "database": newShopDB(t)})
	s.handleTestConn(rec, httptest.NewRequest("POST", "/api/testconn", bytes.NewReader(body)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"tables":2`) || !strings.Contains(rec.Body.String(), `"version":"SQLite 3.`) {
		t.Errorf("test connection: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	body, _ = json.Marshal(map[string]any{"engine": "sqlite", "database": "/no/such.db"})
	s.handleTestConn(rec, httptest.NewRequest("POST", "/api/testconn", bytes.NewReader(body)))
	if rec.Code != 400 {
		t.Errorf("missing file should fail: %d %s", rec.Code, rec.Body)
	}
	if list, _ := s.store.List(); len(list) != 0 {
		t.Errorf("testing must not save: %+v", list)
	}
}

func TestAPIExportAll(t *testing.T) {
	s := newTestServer(t)
	db, err := openDB(context.Background(), ConnParams{Engine: EngineSQLite, Database: newShopDB(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s.sessions["s1"] = &session{engine: EngineSQLite, label: "shop", db: db}

	rec := httptest.NewRecorder()
	s.handleExportAll(rec, httptest.NewRequest("POST", "/api/exportall", strings.NewReader(`{"session":"s1","format":"excel"}`)))
	var start struct {
		Job    string
		Tables int
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &start) != nil || start.Tables != 2 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	var st struct {
		Done   bool
		Tables []struct{ Name, State, File string }
	}
	for deadline := time.Now().Add(30 * time.Second); !st.Done; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("export all did not finish")
		}
		rec = httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/exportall/"+start.Job, nil)
		req.SetPathValue("job", start.Job)
		s.handleExportAllStatus(rec, req)
		json.Unmarshal(rec.Body.Bytes(), &st)
	}
	for _, tb := range st.Tables {
		if tb.State != "done" || !strings.HasSuffix(tb.File, ".xlsx") {
			t.Errorf("%+v", tb)
		}
	}
}
