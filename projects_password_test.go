package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// TestProjectPassword: a project password, set with the linked connections'
// passwords, opens the project and connects every connection without them;
// a wrong one opens nothing; a connection's changed password is asked once,
// then sealed; "" removes the project password.
func TestProjectPassword(t *testing.T) {
	s := newTestServer(t)
	a := newSQLiteDB(t, "a.db", `CREATE TABLE t (id INTEGER PRIMARY KEY)`, `INSERT INTO t VALUES (1)`)
	b := newSQLiteDB(t, "b.db", `CREATE TABLE u (id INTEGER PRIMARY KEY)`, `INSERT INTO u VALUES (1)`)
	idA, boxA, _ := s.store.SaveLocked(SavedConn{Name: "a", ConnParams: ConnParams{Engine: EngineSQLite, Database: a}}, "password A", nil)
	idB, boxB, _ := s.store.SaveLocked(SavedConn{Name: "b", ConnParams: ConnParams{Engine: EngineSQLite, Database: b}}, "password B", nil)
	d := projectData{Links: []projectLink{{idA, "a"}, {idB, "b"}}, Tables: []projectTable{{Alias: "a", Source: "t", Name: "t"}, {Alias: "b", Source: "u", Name: "u"}}}
	checkProjectData(&d)
	pid, _, err := s.store.CreateProject("P", d, map[int64]*secretBox{idA: boxA, idB: boxB})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.projectRoutes(mux)
	call := func(path string, body any, want int) map[string]any {
		t.Helper()
		j, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, jsonReq("POST", path, bytes.NewReader(j)))
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != want {
			t.Fatalf("%s: status %d (want %d): %s", path, rec.Code, want, rec.Body.String())
		}
		return out
	}
	openPath := "/api/projects/" + strconv.FormatInt(pid, 10) + "/open"
	ka, kb := strconv.FormatInt(idA, 10), strconv.FormatInt(idB, 10)
	connected := func(st map[string]any) bool {
		for _, l := range st["links"].([]any) {
			if l.(map[string]any)["state"] != "connected" {
				return false
			}
		}
		return true
	}

	// Set it: the connections' passwords are needed (asked one by one), and checked.
	st := call(openPath, map[string]any{"lockPasswords": map[string]string{ka: "password A", kb: "password B"}}, 200)
	ps := st["project"].(string)
	if out := call("/api/project/password", map[string]any{"project": ps, "password": "short"}, 400); out["error"] == nil {
		t.Error("a short project password was taken")
	}
	if out := call("/api/project/password", map[string]any{"project": ps, "password": "project pass", "lockPasswords": map[string]string{ka: "password A"}}, 403); out["savedId"] != float64(idB) {
		t.Errorf("b's password wasn't asked for: %v", out)
	}
	call("/api/project/password", map[string]any{"project": ps, "password": "project pass", "lockPasswords": map[string]string{ka: "password A", kb: "password B"}}, 200)
	if list, _ := s.store.Projects(); !list[0].HasPassword {
		t.Error("the list doesn't say the project has a password")
	}
	call("/api/project/close", map[string]any{"project": ps}, 200)

	// The project password alone opens it and connects both.
	if st := call(openPath, map[string]any{"projectPassword": "project pass"}, 200); !connected(st) {
		t.Errorf("not every connection opened: %v", st["links"])
	} else {
		call("/api/project/close", map[string]any{"project": st["project"]}, 200)
	}
	// A wrong one opens nothing.
	if st := call(openPath, map[string]any{"projectPassword": "wrong pass"}, 200); st["projectLocked"] != true || st["project"] != nil {
		t.Errorf("a wrong project password: %v", st)
	}

	// b's password changes: it is asked once, then the project password knows it again.
	if _, _, err := s.store.SaveLocked(SavedConn{ID: idB, Name: "b", ConnParams: ConnParams{Engine: EngineSQLite, Database: b}}, "password B2", boxB); err != nil {
		t.Fatal(err)
	}
	if out := call(openPath, map[string]any{"projectPassword": "project pass"}, 403); out["savedId"] != float64(idB) {
		t.Errorf("b's changed password wasn't asked for: %v", out)
	}
	st = call(openPath, map[string]any{"projectPassword": "project pass", "lockPasswords": map[string]string{kb: "password B2"}}, 200)
	call("/api/project/close", map[string]any{"project": st["project"]}, 200)
	st = call(openPath, map[string]any{"projectPassword": "project pass"}, 200)
	if !connected(st) {
		t.Errorf("after the change, the project password alone: %v", st["links"])
	}

	// Removed: the connections' passwords are needed again.
	if out := call("/api/project/password", map[string]any{"project": st["project"], "password": ""}, 200); out["hasPassword"] != false {
		t.Errorf("remove: %v", out)
	}
	call("/api/project/close", map[string]any{"project": st["project"]}, 200)
	if out := call(openPath, map[string]any{"projectPassword": "project pass"}, 403); out["locked"] != true {
		t.Errorf("without a project password, it still opened: %v", out)
	}
}
