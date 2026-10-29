package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A connection's credentials and saved queries are sealed with its own
// password: the right password opens them, a wrong one doesn't, and the
// database file holds neither in readable form.
func TestConnectionPasswordProtectsCredentialsAndQueries(t *testing.T) {
	s := newTestServer(t)
	st := s.store
	id, box, err := st.SaveLocked(SavedConn{Name: "optimo-prod", ConnParams: ConnParams{Engine: EngineMariaDB, Host: "db1", Username: "admin", Password: testPass, Database: "optima"}}, "correct horse", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveQuery(id, box, SavedQuery{Name: "Open orders", Description: "orders not shipped", SQL: "SELECT * FROM orders WHERE shipped = 0"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SaveLocked(SavedConn{Name: "short", ConnParams: ConnParams{Engine: EngineMySQL}}, "abc", nil); err == nil {
		t.Error("a 3-character connection password was accepted")
	}
	if _, _, err := st.SaveLocked(SavedConn{Name: "optimo-prod", ConnParams: ConnParams{Engine: EngineMySQL}}, "another pw", nil); err == nil {
		t.Error("a new connection replaced an existing one")
	}

	// Wrong or missing password: nothing opens.
	if _, _, err := st.Unlock(id, "wrong horse"); !errors.Is(err, errLockWrong) {
		t.Errorf("wrong password: %v", err)
	}
	if _, _, err := st.Unlock(id, ""); !errors.Is(err, errLockRequired) {
		t.Errorf("no password: %v", err)
	}
	if c, _ := st.Get(id); c.Username != "" || c.Password != "" {
		t.Errorf("Get must not reveal a protected connection's credentials: %+v", c)
	}
	if _, err := st.Queries(id, nil); err == nil {
		t.Error("queries opened without the connection key")
	}
	// The right password opens both.
	c, box2, err := st.Unlock(id, "correct horse")
	if err != nil || c.Username != "admin" || c.Password != testPass {
		t.Fatalf("unlock: %+v %v", c, err)
	}
	qs, err := st.Queries(id, box2)
	if err != nil || len(qs) != 1 || qs[0].SQL != "SELECT * FROM orders WHERE shipped = 0" || qs[0].Description != "orders not shipped" {
		t.Fatalf("queries: %+v %v", qs, err)
	}

	// A new connection password re-seals the queries; the old one stops working.
	if _, _, err := st.SaveLocked(SavedConn{ID: id, Name: "optimo-prod", ConnParams: c.ConnParams}, "new password!", box2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Unlock(id, "correct horse"); !errors.Is(err, errLockWrong) {
		t.Errorf("old password still works: %v", err)
	}
	_, box3, err := st.Unlock(id, "new password!")
	if qs, err2 := st.Queries(id, box3); err != nil || err2 != nil || len(qs) != 1 {
		t.Errorf("after changing the password: %v %v %v", qs, err, err2)
	}

	// Nothing readable on disk.
	st.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(dbPathOf(t, st)), "*"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, secret := range []string{testPass, "admin", "shipped = 0", "Open orders", "orders not shipped"} {
			if bytes.Contains(b, []byte(secret)) {
				t.Errorf("%s holds %q in plain text", filepath.Base(f), secret)
			}
		}
	}

	// The command line never replaces a protected connection.
	if _, err := st.SaveUnlocked(SavedConn{Name: "optimo-prod", ConnParams: ConnParams{Engine: EngineMariaDB}}); !errors.Is(err, errLockedSave) {
		t.Errorf("SaveUnlocked over a protected connection: %v", err)
	}
	// Deleting the connection deletes its queries.
	st.Delete(id)
	var n int
	st.db.QueryRow(`SELECT COUNT(*) FROM queries`).Scan(&n)
	if n != 0 {
		t.Errorf("%d queries left after deleting the connection", n)
	}
}

func dbPathOf(t *testing.T, st *Store) string {
	var p string
	if err := st.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

// Through the API: connecting needs the connection password, wrong guesses
// are throttled, and only read-only queries can be saved.
func TestAPIConnectionPasswordAndSavedQueries(t *testing.T) {
	s := newTestServer(t)
	db := newShopDB(t)
	// Save with a connection password.
	rec := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]any{"name": "shop", "engine": "sqlite", "database": db})
	s.handleSaveConn(rec, httptest.NewRequest("POST", "/api/connections", bytes.NewReader(body)))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "connection password") {
		t.Errorf("saving without a connection password: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	body, _ = json.Marshal(map[string]any{"name": "shop", "engine": "sqlite", "database": db, "newLockPassword": "open sesame"})
	s.handleSaveConn(rec, httptest.NewRequest("POST", "/api/connections", bytes.NewReader(body)))
	var saved struct{ ID int64 }
	json.Unmarshal(rec.Body.Bytes(), &saved)
	if rec.Code != 200 || saved.ID == 0 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}

	connect := func(pw string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		body, _ := json.Marshal(map[string]any{"savedId": saved.ID, "lockPassword": pw})
		s.handleConnect(rec, httptest.NewRequest("POST", "/api/connect", bytes.NewReader(body)))
		return rec
	}
	if rec := connect(""); rec.Code != 403 || !strings.Contains(rec.Body.String(), `"locked":true`) {
		t.Errorf("connect without the password: %d %s", rec.Code, rec.Body)
	}
	for range 5 {
		if rec := connect("guess"); rec.Code != 403 {
			t.Fatalf("wrong password: %d %s", rec.Code, rec.Body)
		}
	}
	if rec := connect("open sesame"); rec.Code != 429 {
		t.Errorf("after 5 wrong passwords even the right one must wait: %d %s", rec.Code, rec.Body)
	}
	s.throttle.ok(saved.ID)
	rec = connect("open sesame")
	var sess struct {
		Session   string
		SavedConn int64
	}
	json.Unmarshal(rec.Body.Bytes(), &sess)
	if rec.Code != 200 || sess.SavedConn != saved.ID {
		t.Fatalf("connect: %d %s", rec.Code, rec.Body)
	}

	saveQuery := func(q map[string]any) *httptest.ResponseRecorder {
		q["session"] = sess.Session
		b, _ := json.Marshal(q)
		rec := httptest.NewRecorder()
		s.handleSaveQuery(rec, httptest.NewRequest("POST", "/api/queries", bytes.NewReader(b)))
		return rec
	}
	for _, bad := range []map[string]any{
		{"name": "drop it", "sql": "DROP TABLE orders"},
		{"name": "sneaky", "sql": "SELECT 1; DELETE FROM orders"},
		{"name": "update", "sql": "UPDATE orders SET total = 0"},
		{"name": "", "sql": "SELECT 1"},
		{"name": "empty", "sql": "  ;  "},
	} {
		if rec := saveQuery(bad); rec.Code != 400 {
			t.Errorf("%v was accepted: %d %s", bad, rec.Code, rec.Body)
		}
	}
	if rec := saveQuery(map[string]any{"name": "Big orders", "description": "over 100", "sql": "SELECT * FROM orders WHERE total > 100;"}); rec.Code != 200 {
		t.Fatalf("save query: %d %s", rec.Code, rec.Body)
	}
	if rec := saveQuery(map[string]any{"name": "big ORDERS", "sql": "SELECT 1"}); rec.Code != 400 {
		t.Errorf("duplicate name accepted: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.handleListQueries(rec, httptest.NewRequest("GET", "/api/queries?session="+sess.Session, nil))
	var list struct {
		Saved   bool
		Queries []SavedQuery
	}
	json.Unmarshal(rec.Body.Bytes(), &list)
	if !list.Saved || len(list.Queries) != 1 || list.Queries[0].SQL != "SELECT * FROM orders WHERE total > 100" {
		t.Fatalf("list: %s", rec.Body)
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/queries/x?session="+sess.Session, nil)
	req.SetPathValue("id", strconv.FormatInt(list.Queries[0].ID, 10))
	s.handleDeleteQuery(rec, req)
	if rec.Code != 200 {
		t.Errorf("delete query: %d %s", rec.Code, rec.Body)
	}

	// Deleting the connection needs its password too.
	del := func(pw string) int {
		b, _ := json.Marshal(map[string]any{"lockPassword": pw})
		req := httptest.NewRequest("DELETE", "/api/connections/x", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.SetPathValue("id", strconv.FormatInt(saved.ID, 10))
		rec := httptest.NewRecorder()
		s.handleDeleteConn(rec, req)
		return rec.Code
	}
	if code := del("nope!!"); code != 403 {
		t.Errorf("delete with a wrong password: %d", code)
	}
	if code := del("open sesame"); code != 200 {
		t.Errorf("delete: %d", code)
	}
}

// The superuser password opens every connection: those saved after it was
// set at once, older ones after they were opened once with their own password.
func TestSuperuserOpensEveryConnection(t *testing.T) {
	s := newTestServer(t)
	st := s.store
	const super = "super secret 123"
	old, _, err := st.SaveLocked(SavedConn{Name: "old", ConnParams: ConnParams{Engine: EngineMySQL, Username: "u1", Password: "p1"}}, "old-conn-pw", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UnlockAsSuperuser(old, super); !errors.Is(err, errNoSuperuser) {
		t.Errorf("before a superuser exists: %v", err)
	}
	if err := st.SetSuperuser("short", ""); err == nil {
		t.Error("a 5-character superuser password was accepted")
	}
	if err := st.SetSuperuser(super, ""); err != nil {
		t.Fatal(err)
	}
	newer, box, err := st.SaveLocked(SavedConn{Name: "new", ConnParams: ConnParams{Engine: EngineMySQL, Username: "u2", Password: "p2"}}, "new-conn-pw", nil)
	if err != nil {
		t.Fatal(err)
	}
	st.SaveQuery(newer, box, SavedQuery{Name: "q", SQL: "SELECT 1"})

	if _, _, err := st.UnlockAsSuperuser(newer, "wrong super pw"); !errors.Is(err, errLockWrong) {
		t.Errorf("wrong superuser password: %v", err)
	}
	c, sbox, err := st.UnlockAsSuperuser(newer, super)
	if err != nil || c.Username != "u2" || c.Password != "p2" {
		t.Fatalf("superuser unlock: %+v %v", c, err)
	}
	if qs, err := st.Queries(newer, sbox); err != nil || len(qs) != 1 {
		t.Errorf("superuser can't read the saved queries: %v %v", qs, err)
	}
	// Saved before the superuser: not until opened once with its own password.
	if _, _, err := st.UnlockAsSuperuser(old, super); !errors.Is(err, errNoEscrow) {
		t.Errorf("old connection: %v", err)
	}
	st.Unlock(old, "old-conn-pw")
	if c, _, err := st.UnlockAsSuperuser(old, super); err != nil || c.Password != "p1" {
		t.Errorf("old connection after being opened: %+v %v", c, err)
	}
	// Changing the superuser password keeps every connection openable.
	if err := st.SetSuperuser("another super pw", "nope"); err == nil {
		t.Error("changed without the current superuser password")
	}
	if err := st.SetSuperuser("another super pw", super); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UnlockAsSuperuser(newer, "another super pw"); err != nil {
		t.Errorf("after changing the superuser password: %v", err)
	}
	// The superuser password is not in the database file.
	b, _ := os.ReadFile(dbPathOf(t, st))
	if bytes.Contains(b, []byte(super)) || bytes.Contains(b, []byte("another super pw")) {
		t.Error("the superuser password is stored in plain text")
	}
}

// A protected connection's exported files need its password (or the
// superuser's) to be previewed, downloaded or deleted.
func TestExportedFilesOfProtectedConnection(t *testing.T) {
	s := newTestServer(t)
	s.store.SetSuperuser("super secret 123", "")
	id, _, err := s.store.SaveLocked(SavedConn{Name: "optimo-prod", ConnParams: ConnParams{Engine: EngineSQLite, Database: newShopDB(t)}}, "prod-password", nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := s.exportFolder()
	for _, p := range []string{"optimo-prod/csv/orders.csv", "other/csv/x.csv"} {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte("id\n1\n"), 0o644)
	}
	rec := httptest.NewRecorder()
	s.handleListFiles(rec, httptest.NewRequest("GET", "/api/files", nil))
	if !strings.Contains(rec.Body.String(), `"conn":"optimo-prod","kind":"csv","name":"orders.csv"`) || strings.Count(rec.Body.String(), `"protected":true`) != 1 {
		t.Errorf("list: %s", rec.Body)
	}
	zip := func(body map[string]any) (int, string) {
		body["files"] = []map[string]string{{"conn": "optimo-prod", "kind": "csv", "name": "orders.csv"}}
		b, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", "/api/files/zip", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.handlePrepareZip(rec, req)
		return rec.Code, rec.Body.String()
	}
	if code, body := zip(map[string]any{}); code != 403 || !strings.Contains(body, `"conn":"optimo-prod"`) {
		t.Errorf("without a password: %d %s", code, body)
	}
	if code, _ := zip(map[string]any{"unlock": map[string]string{"optimo-prod": "wrong"}}); code != 403 {
		t.Errorf("wrong password: %d", code)
	}
	if code, body := zip(map[string]any{"unlock": map[string]string{"optimo-prod": "prod-password"}}); code != 200 {
		t.Errorf("connection password: %d %s", code, body)
	}
	if code, body := zip(map[string]any{"superPassword": "super secret 123"}); code != 200 {
		t.Errorf("superuser password: %d %s", code, body)
	}
	// An unprotected folder needs nothing.
	b, _ := json.Marshal(map[string]any{"conn": "other", "kind": "csv", "name": "x.csv"})
	rec = httptest.NewRecorder()
	s.handlePreviewFile(rec, httptest.NewRequest("POST", "/api/files/preview", bytes.NewReader(b)))
	if rec.Code != 200 {
		t.Errorf("unprotected preview: %d %s", rec.Code, rec.Body)
	}
	// A direct download link only for the session that opened the connection.
	get := func(session string) int {
		req := httptest.NewRequest("GET", "/api/exports/x?session="+session, nil)
		req.SetPathValue("path", "optimo-prod/csv/orders.csv")
		rec := httptest.NewRecorder()
		s.handleDownload(rec, req)
		return rec.Code
	}
	if code := get(""); code != 403 {
		t.Errorf("download without a session: %d", code)
	}
	s.sessions["s1"] = &session{label: "optimo-prod", savedID: id}
	if code := get("s1"); code != 200 {
		t.Errorf("download with the session: %d", code)
	}
}
