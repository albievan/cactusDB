package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A project's key needs every password-protected linked connection: one
// password alone opens nothing, all of them (or none needed) open it.
func TestProjectKeyNeedsEveryLockedConnection(t *testing.T) {
	s := newTestServer(t)
	st := s.store
	a, boxA, err := st.SaveLocked(SavedConn{Name: "fb", ConnParams: ConnParams{Engine: EngineFirebird, Host: "h"}}, "password A", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, boxB, err := st.SaveLocked(SavedConn{Name: "maria", ConnParams: ConnParams{Engine: EngineMariaDB, Host: "h"}}, "password B", nil)
	if err != nil {
		t.Fatal(err)
	}
	d := projectData{Links: []projectLink{{a, "fb"}, {b, "maria"}}, Tables: []projectTable{{Alias: "fb", Source: "CUSTOMER", Name: "customer"}}}
	if err := checkProjectData(&d); err != nil {
		t.Fatal(err)
	}
	id, key, err := st.CreateProject("Sales", d, map[int64]*secretBox{a: boxA, b: boxB})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ProjectKey(id, map[int64]*secretBox{a: boxA}); err == nil {
		t.Error("one connection's password opened the project")
	}
	if _, err := st.ProjectKey(id, nil); !errors.Is(err, errLockRequired) {
		t.Errorf("no passwords: %v", err)
	}
	other, _, _ := st.SaveLocked(SavedConn{Name: "x", ConnParams: ConnParams{Engine: EngineMySQL}}, "password X", nil)
	_, boxX, _ := st.Unlock(other, "password X")
	if _, err := st.ProjectKey(id, map[int64]*secretBox{a: boxA, b: boxX}); err == nil {
		t.Error("another connection's key opened the project")
	}
	got, err := st.ProjectKey(id, map[int64]*secretBox{a: boxA, b: boxB})
	if err != nil || !bytes.Equal(got, key) {
		t.Fatalf("both passwords: %v", err)
	}
	p, err := st.GetProject(id, got)
	if err != nil || p.Name != "Sales" || len(p.Links) != 2 || p.Tables[0].Name != "customer" || p.Limits != defaultProjectLimits() {
		t.Fatalf("project: %+v %v", p, err)
	}

	// Nothing of it is readable in the database file.
	raw, _ := os.ReadFile(st.Path())
	for _, secret := range []string{"Sales", "CUSTOMER", "customer", `"maria"`} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Errorf("%q is readable in the database file", secret)
		}
	}

	// A new password for one connection: its share follows, the old one is useless.
	_, boxA2, err := st.SaveLocked(SavedConn{ID: a, Name: "fb", ConnParams: ConnParams{Engine: EngineFirebird, Host: "h"}}, "password A2", boxA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ProjectKey(id, map[int64]*secretBox{a: boxA, b: boxB}); err == nil {
		t.Error("the old password still opens the project")
	}
	if got, err := st.ProjectKey(id, map[int64]*secretBox{a: boxA2, b: boxB}); err != nil || !bytes.Equal(got, key) {
		t.Errorf("after the password change: %v", err)
	}

	// Deleting a linked connection keeps the project readable with the others.
	if names, _ := st.ProjectsUsing(b); len(names) != 1 || names[0] != "Sales" {
		t.Errorf("projects using maria: %v", names)
	}
	if err := st.Delete(b, boxB); err != nil {
		t.Fatal(err)
	}
	if got, err := st.ProjectKey(id, map[int64]*secretBox{a: boxA2}); err != nil || !bytes.Equal(got, key) {
		t.Errorf("after deleting a linked connection: %v", err)
	}
	if ids, _ := st.projectLinks(id); len(ids) != 1 || ids[0] != a {
		t.Errorf("links after the delete: %v", ids)
	}

	// Saving with other links splits the key again over them.
	d.Links = []projectLink{{a, "fb"}, {other, "x"}}
	d.Tables = nil
	if err := st.SaveProject(id, key, "Sales 2026", d, map[int64]*secretBox{a: boxA2, other: boxX}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ProjectKey(id, map[int64]*secretBox{a: boxA2}); err == nil {
		t.Error("the newly linked connection's password wasn't needed")
	}
	if got, err := st.ProjectKey(id, map[int64]*secretBox{a: boxA2, other: boxX}); err != nil || !bytes.Equal(got, key) {
		t.Errorf("after re-linking: %v", err)
	}
	if list, _ := st.Projects(); len(list) != 1 || list[0].Name != "Sales 2026" || len(list[0].ConnIDs) != 2 {
		t.Errorf("list: %+v", list)
	}
	if _, _, err := st.CreateProject("sales 2026", projectData{}, nil); !errors.Is(err, errProjectName) {
		t.Errorf("a duplicate name (other case): %v", err)
	}
	if err := st.DeleteProject(id); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.Projects(); len(list) != 0 {
		t.Errorf("after delete: %+v", list)
	}
}

// A project without password-protected connections needs no password.
func TestProjectWithoutLocks(t *testing.T) {
	st := newTestServer(t).store
	id, key, err := st.CreateProject("Open", projectData{Links: []projectLink{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.ProjectKey(id, nil); err != nil || !bytes.Equal(got, key) {
		t.Fatalf("no links: %v", err)
	}
}

func TestProjectAliasesAndTables(t *testing.T) {
	for _, a := range []string{"fb", "maria", "ms_2", "_x", "Sales"} {
		if err := checkAlias(a); err != nil {
			t.Errorf("%q refused: %v", a, err)
		}
	}
	for _, a := range []string{"", "2fb", "my db", "fb-1", "select", "MAIN", "temp", "Order", strings.Repeat("a", 31)} {
		if checkAlias(a) == nil {
			t.Errorf("%q accepted", a)
		}
	}
	for name, want := range map[string]string{"Firebird prod": "firebird_pro", "2nd MariaDB": "nd_mariadb", "SQL Server": "sql_server", "": "db"} {
		if got := defaultAlias(name, nil); got != want {
			t.Errorf("defaultAlias(%q) = %q, want %q", name, got, want)
		}
	}
	if got := defaultAlias("fb", []string{"FB"}); got != "fb2" {
		t.Errorf("taken alias: %q", got)
	}
	bad := []projectData{
		{Links: []projectLink{{1, "fb"}, {2, "FB"}}},
		{Links: []projectLink{{1, "fb"}, {1, "fb2"}}},
		{Links: []projectLink{{1, "fb"}}, Tables: []projectTable{{Alias: "ms", Source: "t", Name: "t"}}},
		{Links: []projectLink{{1, "fb"}}, Tables: []projectTable{{Alias: "fb", Source: "a.t", Name: "t"}, {Alias: "fb", Source: "b.t", Name: "T"}}},
		{Links: []projectLink{{1, "fb"}}, Tables: []projectTable{{Alias: "fb", Source: "t", Name: "a.b"}}},
	}
	many := projectData{}
	for i := 0; i <= maxProjectLinks; i++ {
		many.Links = append(many.Links, projectLink{int64(i), "db" + strconv.Itoa(i)})
	}
	bad = append(bad, many)
	for i, d := range bad {
		if err := checkProjectData(&d); err == nil {
			t.Errorf("case %d accepted: %+v", i, d)
		}
	}
	for _, e := range []string{EngineSQLite, EngineMySQL, EngineMariaDB, EngineFirebird, EngineMSSQL, EnginePostgres, EngineOracle,
		EngineInformix, EngineAccess, EngineCockroach, EngineAzureSQL, EngineAzurePG, EngineHorizonDB, EngineRedshift, EngineAthena,
		EngineBigQuery, EnginePgvector} {
		if !linkable(e) {
			t.Errorf("%s can't be linked", e)
		}
	}
	for _, e := range []string{EngineMongoDB, EngineCosmosDB, EngineCouchbase, EngineCassandra, EngineRedis, EngineNeo4j, EngineWeaviate} {
		if linkable(e) {
			t.Errorf("%s can be linked", e)
		}
	}
}

// An existing cactus.conn.db (from before projects) gains the project tables
// and keeps its connections.
func TestStoreMigratesToProjects(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cactus.conn.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE connections (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE,
		engine TEXT NOT NULL, host TEXT NOT NULL DEFAULT '', port INTEGER NOT NULL DEFAULT 0, username TEXT NOT NULL DEFAULT '',
		password TEXT NOT NULL DEFAULT '', database TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
		INSERT INTO connections (name, engine, created_at) VALUES ('old one', 'sqlite', '2025-01-01')`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	st, err := openStore(path, filepath.Join(dir, "cactus.key"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB().Close()
	list, err := st.List()
	if err != nil || len(list) != 1 || list[0].Name != "old one" {
		t.Fatalf("connections after the upgrade: %+v %v", list, err)
	}
	for _, table := range []string{"projects", "project_links", "project_queries", "project_query_versions"} {
		var n int
		if err := st.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s missing", table)
		}
	}
	if list, err := st.Projects(); err != nil || len(list) != 0 {
		t.Errorf("projects: %v %v", list, err)
	}
}

// Through the API: create, open (asking for the password of a locked link),
// link a database, save tables, list objects, close; deleting a linked
// connection asks first.
func TestProjectAPI(t *testing.T) {
	s := newTestServer(t)
	shop := newShopDB(t)
	cid, _, err := s.store.SaveLocked(SavedConn{Name: "shop", ConnParams: ConnParams{Engine: EngineSQLite, Database: shop}}, "shop password", nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(h func(w *httptest.ResponseRecorder), want int) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		h(rec)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != want {
			t.Fatalf("status %d (want %d): %s", rec.Code, want, rec.Body.String())
		}
		return out
	}
	mux := http.NewServeMux()
	s.projectRoutes(mux)
	mux.HandleFunc("POST /api/connections/{id}/delete", s.handleDeleteConn)
	req := func(method, path string, body any) func(*httptest.ResponseRecorder) {
		b, _ := json.Marshal(body)
		return func(w *httptest.ResponseRecorder) { mux.ServeHTTP(w, jsonReq(method, path, bytes.NewReader(b))) }
	}
	out := call(req("POST", "/api/projects", map[string]any{"name": "Shop project"}), 200)
	pid := int64(out["id"].(float64))
	open := call(req("POST", "/api/projects/"+strconv.FormatInt(pid, 10)+"/open", map[string]any{}), 200)
	ps := open["project"].(string)
	// Linking the locked connection needs its password.
	link := map[string]any{"project": ps, "name": "Shop project", "links": []map[string]any{{"connId": cid, "alias": "shop"}}}
	locked := call(req("POST", "/api/project/save", link), 403)
	if locked["locked"] != true || locked["name"] != "shop" {
		t.Fatalf("missing password: %v", locked)
	}
	link["lockPasswords"] = map[string]string{strconv.FormatInt(cid, 10): "shop password"}
	saved := call(req("POST", "/api/project/save", link), 200)
	if l := saved["links"].([]any)[0].(map[string]any); l["state"] != "connected" {
		t.Fatalf("link: %v", l)
	}
	objs := call(req("GET", "/api/project/objects?project="+ps+"&alias=shop", nil), 200)
	if !strings.Contains(toJSON(objs), "customer") {
		t.Errorf("objects: %v", objs)
	}
	link["tables"] = []map[string]any{{"alias": "shop", "source": "customer", "name": "customer"}}
	delete(link, "lockPasswords")
	call(req("POST", "/api/project/save", link), 200)
	call(req("POST", "/api/project/close", map[string]any{"project": ps}), 200)

	// Opening again needs the password; with it, the tables are back.
	call(req("POST", "/api/projects/"+strconv.FormatInt(pid, 10)+"/open", map[string]any{}), 403)
	again := call(req("POST", "/api/projects/"+strconv.FormatInt(pid, 10)+"/open",
		map[string]any{"lockPasswords": map[string]string{strconv.FormatInt(cid, 10): "shop password"}}), 200)
	if !strings.Contains(toJSON(again["tables"]), `"customer"`) {
		t.Errorf("tables after reopening: %v", again["tables"])
	}

	// Deleting the linked connection names the project first.
	del := call(req("POST", "/api/connections/"+strconv.FormatInt(cid, 10)+"/delete", map[string]any{"lockPassword": "shop password"}), 409)
	if !strings.Contains(toJSON(del), "Shop project") {
		t.Errorf("delete warning: %v", del)
	}
	call(req("POST", "/api/connections/"+strconv.FormatInt(cid, 10)+"/delete", map[string]any{"lockPassword": "shop password", "projectsOk": true}), 200)
	broken := call(req("POST", "/api/projects/"+strconv.FormatInt(pid, 10)+"/open", map[string]any{}), 200)
	if l := broken["links"].([]any); len(l) != 1 || l[0].(map[string]any)["state"] != "broken" {
		t.Errorf("after deleting the connection: %v", broken["links"])
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
