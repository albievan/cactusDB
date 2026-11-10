package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestCheckQuery(t *testing.T) {
	ctx := context.Background()
	db, err := openDB(ctx, ConnParams{Engine: EngineSQLite, Database: newShopDB(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, c := range []struct{ q, err string }{
		{"SELECT c.name, o.total FROM customer c JOIN orders o ON o.customer_id = c.id ORDER BY 2", ""},
		{"SELEC * FROM customer", "starts with SELEC"},
		{"SELECT * FORM customer", "syntax error"},
		{"SELECT * FROM customers", "no such table"},
		{"SELECT nam FROM customer", "no such column"},
		{"DELETE FROM customer", "only read-only"},
	} {
		how, err := checkQuery(ctx, EngineSQLite, db, c.q)
		switch {
		case c.err == "" && err != nil:
			t.Errorf("%q: %v", c.q, err)
		case c.err == "" && !strings.Contains(how, "not run"):
			t.Errorf("%q: checked by %q, want prepared", c.q, how)
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%q: %v, want %q", c.q, err, c.err)
		}
	}
	var n int
	db.QueryRow("SELECT count(*) FROM customer").Scan(&n)
	if n != 3 {
		t.Errorf("customer has %d rows after the checks", n)
	}
}

// Each save of a query is a version; an unchanged save is none; a query saved
// before versions were kept has its old contents as version 1; versions are
// sealed like the query, re-sealed with a new connection password, and go
// with the query.
func TestQueryVersions(t *testing.T) {
	s := newTestServer(t)
	st := s.store
	id, box, err := st.SaveLocked(SavedConn{Name: "shop", ConnParams: ConnParams{Engine: EngineSQLite, Database: "/x.db"}}, "correct horse", nil)
	if err != nil {
		t.Fatal(err)
	}
	qid, v, err := st.SaveQuery(id, box, SavedQuery{Name: "q", SQL: "SELECT 1"})
	if err != nil || v != 1 {
		t.Fatalf("add: %d %v", v, err)
	}
	save := func(sql string) int {
		t.Helper()
		_, v, err := st.SaveQuery(id, box, SavedQuery{ID: qid, Name: "q", SQL: sql})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := save("SELECT 2"); v != 2 {
		t.Errorf("change: version %d", v)
	}
	if v := save("SELECT 2"); v != 2 {
		t.Errorf("an unchanged save made version %d", v)
	}
	save("SELECT 3")
	vs, err := st.QueryVersions(id, box, qid)
	if err != nil || len(vs) != 3 || vs[0].Version != 3 || vs[0].SQL != "SELECT 3" || vs[2].SQL != "SELECT 1" {
		t.Fatalf("versions: %+v %v", vs, err)
	}
	if qs, _ := st.Queries(id, box); qs[0].Version != 3 {
		t.Errorf("listed as version %d", qs[0].Version)
	}
	if _, err := st.QueryVersions(id, nil, qid); err == nil {
		t.Error("versions opened without the connection key")
	}
	if b, _ := os.ReadFile(st.path); strings.Contains(string(b), "SELECT 2") {
		t.Error("a version is readable in the database file")
	}

	// A query saved before 1.46.0: no versions until it is changed.
	st.DB().Exec(`DELETE FROM query_versions WHERE query_id = ?`, qid)
	if vs, _ := st.QueryVersions(id, box, qid); len(vs) != 1 || vs[0].SQL != "SELECT 3" {
		t.Errorf("an old query's versions: %+v", vs)
	}
	if v := save("SELECT 4"); v != 2 {
		t.Errorf("an old query's first change is version %d, want 2", v)
	}

	// A new connection password: the versions open with the new key only.
	c, _, _ := st.Unlock(id, "correct horse")
	_, box2, err := st.SaveLocked(SavedConn{ID: id, Name: "shop", ConnParams: c.ConnParams}, "new password!", box)
	if err != nil {
		t.Fatal(err)
	}
	if vs, err := st.QueryVersions(id, box2, qid); err != nil || len(vs) != 2 || vs[1].SQL != "SELECT 3" {
		t.Errorf("after a new password: %+v %v", vs, err)
	}
	if err := st.DeleteQuery(id, qid); err != nil {
		t.Fatal(err)
	}
	var n int
	st.DB().QueryRow(`SELECT COUNT(*) FROM query_versions`).Scan(&n)
	if n != 0 {
		t.Errorf("%d versions kept after the query was deleted", n)
	}
}

// To the page, a database's error doesn't appear in the answer as plain text,
// but reads the same once decoded; other clients get it plain.
func TestErrorsAreOpaque(t *testing.T) {
	msg := "Error 1064 (42000): You have an error in your SQL syntax near 'FORM x' — é 😀"
	answer := func(fromPage bool, write func(w http.ResponseWriter)) (string, map[string]any) {
		t.Helper()
		h := statusInHeader(logRequests(recoverPanics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { write(w) }))))
		r := httptest.NewRequest("POST", "/api/query", nil)
		if fromPage {
			r.Header.Set(statusInHeaderReq, "in-header")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: %v", w.Body, err)
		}
		return w.Body.String(), got
	}
	body, got := answer(true, func(w http.ResponseWriter) { writeErr(w, 400, errors.New(msg)) })
	if strings.Contains(body, "SQL syntax") || strings.Contains(body, "error in") || got["error"] != msg {
		t.Errorf("to the page: %s -> %v", body, got)
	}
	body, got = answer(true, func(w http.ResponseWriter) { writeOpaqueErr(w, 200, map[string]string{"valid": "false"}, msg) })
	if strings.Contains(body, "SQL syntax") || got["valid"] != false || got["error"] != msg {
		t.Errorf("check answer: %s -> %v", body, got)
	}
	if body, got = answer(false, func(w http.ResponseWriter) { writeErr(w, 400, errors.New(msg)) }); !strings.Contains(body, "SQL syntax") || got["error"] != msg {
		t.Errorf("to a script: %s", body)
	}
}
