package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"
)

// TestProjectCrossDatabase joins the test databases of integration_test.go
// (MariaDB, PostgreSQL, SQL Server, Firebird) in one project query, and
// shows that write attempts through a project change nothing. Set
// CACTUSDB_IT=1 and start them as README.md describes.
func TestProjectCrossDatabase(t *testing.T) {
	if os.Getenv("CACTUSDB_IT") == "" {
		t.Skip("set CACTUSDB_IT=1 to run against live test databases")
	}
	s := newTestServer(t)
	conns := []struct {
		alias, table, name string
		p                  ConnParams
	}{
		{"maria", "customers", "customers", ConnParams{Engine: EngineMariaDB, Host: "127.0.0.1", Port: 13307, Username: "root", Password: "secret", Database: "shop"}},
		{"pg", "public.customers", "customers", ConnParams{Engine: EnginePostgres, Host: "127.0.0.1", Port: 15432, Username: "postgres", Password: "pg p@ss:/w", Database: "shop"}},
		{"ms", "dbo.customers", "customers", ConnParams{Engine: EngineMSSQL, Host: "127.0.0.1", Port: 11433, Username: "sa", Password: "Cactus#Pass123", Database: "shop"}},
		{"fb", "CUSTOMER", "customer", ConnParams{Engine: EngineFirebird, Host: "127.0.0.1", Port: 13050, Username: "SYSDBA", Password: "masterkey", Database: "/firebird/data/test.fdb"}},
	}
	d := projectData{}
	boxes := map[int64]*secretBox{}
	pw := map[string]string{}
	for i, c := range conns {
		id, box, err := s.store.SaveLocked(SavedConn{Name: c.alias + "-it", ConnParams: c.p}, "password "+strconv.Itoa(i), nil)
		if err != nil {
			t.Fatal(err)
		}
		boxes[id], pw[strconv.FormatInt(id, 10)] = box, "password "+strconv.Itoa(i)
		d.Links = append(d.Links, projectLink{id, c.alias})
		d.Tables = append(d.Tables, projectTable{Alias: c.alias, Source: c.table, Name: c.name})
	}
	if err := checkProjectData(&d); err != nil {
		t.Fatal(err)
	}
	pid, _, err := s.store.CreateProject("IT", d, boxes)
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
			t.Fatalf("%s %s: %d (want %d): %s", method, path, rec.Code, want, rec.Body.String())
		}
		return out
	}
	open := call("POST", "/api/projects/"+strconv.FormatInt(pid, 10)+"/open", map[string]any{"lockPasswords": pw}, 200)
	for _, l := range open["links"].([]any) {
		if m := l.(map[string]any); m["state"] != "connected" {
			t.Fatalf("%s: %v", m["alias"], m["error"])
		}
	}
	ps := open["project"].(string)
	run := func(q string) map[string]any {
		t.Helper()
		call("POST", "/api/project/run", map[string]any{"project": ps, "sql": q}, 200)
		for i := 0; i < 600; i++ {
			st := call("GET", "/api/project/run?project="+ps, nil, 200)
			if st["state"] != "loading" && st["state"] != "querying" {
				return st
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("the run didn't finish")
		return nil
	}
	st := run(`SELECT m.id, m.name, p.name AS pg_name, s.name AS ms_name, f.NAME AS fb_name
FROM maria.customers m
JOIN pg.customers p ON p.id = m.id
JOIN ms.customers s ON s.id = m.id
JOIN fb.customer f ON f.ID = m.id
ORDER BY m.id`)
	if st["state"] != "done" {
		t.Fatalf("cross-database join: %v", st)
	}
	if rows := st["result"].(map[string]any)["rows"].([]any); len(rows) != 2 {
		t.Errorf("rows: %v", rows)
	}
	st = run(`SELECT m.id FROM maria.customers m JOIN pg.customers p ON p.id = m.id WHERE m.id = 1 AND p.id >= 1`)
	if st["state"] != "done" || len(st["result"].(map[string]any)["rows"].([]any)) != 1 {
		t.Errorf("with pushdown: %v", st)
	}
	t.Logf("tables read: %s", toJSON(st["tables"]))

	// Write attempts through the project are refused, and the data is unchanged.
	for _, q := range []string{"DELETE FROM maria.customers", "UPDATE pg.customers SET name = 'x'", "INSERT INTO ms.customers (id) VALUES (9)",
		"DROP TABLE fb.customer", "SELECT 1; DELETE FROM maria.customers", "ATTACH 'x' AS y", "PRAGMA query_only = 0"} {
		call("POST", "/api/project/run", map[string]any{"project": ps, "sql": q}, 400)
	}
	for _, c := range conns {
		db, err := openDB(t.Context(), c.p)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		err = db.QueryRow("SELECT COUNT(*) FROM " + quoteObject(dialect(c.p.Engine), c.table)).Scan(&n)
		db.Close()
		if err != nil || n != 2 {
			t.Errorf("%s: %d rows (%v)", c.alias, n, err)
		}
	}
}
