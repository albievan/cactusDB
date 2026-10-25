package main

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	testUser = "SYSDBA-unique-user"
	testPass = "s3cret-Pa55-unique"
)

// assertNoPlaintext fails if the raw database file contains the credentials.
func assertNoPlaintext(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{testUser, testPass} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Errorf("%s contains %q in plain text", filepath.Base(path), secret)
		}
	}
}

func TestStoreEncryptsCredentials(t *testing.T) {
	dir := t.TempDir()
	path, keyFile := filepath.Join(dir, "cactus.conn.db"), filepath.Join(dir, "cactus.key")
	st, err := openStore(path, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.Save(SavedConn{Name: "fb", ConnParams: ConnParams{Engine: EngineFirebird, Host: "h", Username: testUser, Password: testPass, Database: "/d.fdb"}})
	if err != nil {
		t.Fatal(err)
	}
	var user, pass string
	st.db.QueryRow(`SELECT username, password FROM connections WHERE id = ?`, id).Scan(&user, &pass)
	if !isSealed(user) || !isSealed(pass) || strings.Contains(user+pass, "unique") {
		t.Errorf("stored columns are not encrypted: %q %q", user, pass)
	}
	c, err := st.Get(id)
	if err != nil || c.Username != testUser || c.Password != testPass {
		t.Errorf("round trip failed: %+v %v", c, err)
	}
	st.db.Close()
	assertNoPlaintext(t, path)

	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(keyFile); fi == nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("key file should be mode 600, got %v", fi.Mode())
		}
	}

	// The same key file opens it again...
	st, err = openStore(path, keyFile)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if list, err := st.List(); err != nil || len(list) != 1 || list[0].Password != testPass {
		t.Errorf("list after reopen: %+v %v", list, err)
	}
	st.db.Close()

	// A missing key file is reported, not silently replaced by a new key.
	if _, err := openStore(path, filepath.Join(dir, "missing.key")); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("expected missing-key error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "missing.key")); !os.IsNotExist(err) {
		t.Errorf("a new key file must not be created for a store that already has encrypted data")
	}

	// ...a different key does not.
	os.WriteFile(filepath.Join(dir, "other.key"), []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))), 0o600)
	if _, err := openStore(path, filepath.Join(dir, "other.key")); err == nil || !strings.Contains(err.Error(), "cannot decrypt") {
		t.Errorf("expected wrong-key error, got %v", err)
	}
}

func TestStoreMigratesPlaintext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cactus.conn.db")
	// A connection file as written by cactusdb 1.0, with plain-text credentials.
	old, err := sql.Open("sqlite", sqliteURI(path))
	if err != nil {
		t.Fatal(err)
	}
	_, err = old.Exec(`CREATE TABLE connections (
		id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, engine TEXT NOT NULL,
		host TEXT NOT NULL DEFAULT '', port INTEGER NOT NULL DEFAULT 0, username TEXT NOT NULL DEFAULT '',
		password TEXT NOT NULL DEFAULT '', database TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
		INSERT INTO connections (name, engine, host, username, password, database, created_at)
		VALUES ('Citydeep', 'firebird', '127.0.0.1', '` + testUser + `', '` + testPass + `', '/x.fdb', '2026-10-25');`)
	old.Close()
	if err != nil {
		t.Fatal(err)
	}

	st, err := openStore(path, filepath.Join(dir, "cactus.key"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetByName("Citydeep")
	if err != nil || c.Username != testUser || c.Password != testPass || c.Host != "127.0.0.1" {
		t.Errorf("migrated connection: %+v %v", c, err)
	}
	st.db.Close()
	assertNoPlaintext(t, path) // VACUUM removed the old plaintext pages too
}

func TestStorePassphrase(t *testing.T) {
	dir := t.TempDir()
	path, keyFile := filepath.Join(dir, "cactus.conn.db"), filepath.Join(dir, "cactus.key")
	t.Setenv(passphraseEnv, "correct horse battery staple")
	st, err := openStore(path, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	st.Save(SavedConn{Name: "pg", ConnParams: ConnParams{Engine: EnginePostgres, Username: testUser, Password: testPass}})
	st.db.Close()
	if _, err := os.Stat(keyFile); !os.IsNotExist(err) {
		t.Errorf("passphrase mode must not write a key file")
	}
	assertNoPlaintext(t, path)

	t.Setenv(passphraseEnv, "wrong passphrase")
	if _, err := openStore(path, keyFile); err == nil {
		t.Errorf("wrong passphrase accepted")
	}
	t.Setenv(passphraseEnv, "correct horse battery staple")
	st, err = openStore(path, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := st.GetByName("pg"); err != nil || c.Password != testPass {
		t.Errorf("passphrase round trip: %+v %v", c, err)
	}
	st.db.Close()
}

func TestSealedValuesAreBoundToTheirField(t *testing.T) {
	box, _ := newSecretBox(bytes.Repeat([]byte{7}, 32))
	sealed := box.seal("password", testPass)
	if _, err := box.open("username", sealed); err == nil {
		t.Error("a password ciphertext must not decrypt as a username")
	}
	if a, b := box.seal("password", "x"), box.seal("password", "x"); a == b {
		t.Error("equal plaintexts must encrypt differently (random nonce)")
	}
}

// The HTTP API never returns saved passwords, and saving with an empty
// password plus keepPassword keeps the stored one.
func TestAPIKeepsPasswordsServerSide(t *testing.T) {
	dir := t.TempDir()
	st, err := openStore(filepath.Join(dir, "c.db"), filepath.Join(dir, "c.key"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.db.Close()
	s := &server{store: st, sessions: map[string]*session{}}
	post := func(body string) {
		rec := httptest.NewRecorder()
		s.handleSaveConn(rec, httptest.NewRequest("POST", "/api/connections", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("save: %d %s", rec.Code, rec.Body)
		}
	}
	post(`{"name":"x","engine":"postgres","host":"a","username":"u","password":"` + testPass + `"}`)
	post(`{"name":"x","engine":"postgres","host":"b","username":"u","password":"","keepPassword":true}`)

	rec := httptest.NewRecorder()
	s.handleListConns(rec, httptest.NewRequest("GET", "/api/connections", nil))
	if strings.Contains(rec.Body.String(), testPass) || !strings.Contains(rec.Body.String(), `"hasPassword":true`) {
		t.Errorf("list must hide the password but flag it: %s", rec.Body)
	}
	if c, _ := st.GetByName("x"); c.Password != testPass || c.Host != "b" {
		t.Errorf("password not kept on save: %+v", c)
	}
}
