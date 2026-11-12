package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGASettingsAndCLI(t *testing.T) {
	t.Setenv(gaIDEnv, "")
	t.Setenv(gaSecretEnv, "")
	s := newTestServer(t)
	post := func(body string) *httptest.ResponseRecorder {
		req := jsonReq("POST", "/api/settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.handleSaveSettings(rec, req)
		return rec
	}
	if rec := post(`{"gaId":"UA-123"}`); rec.Code != 400 {
		t.Errorf("bad id accepted: %d", rec.Code)
	}
	rec := post(`{"gaId":"g-abc123xyz","gaSecret":"s3cret","gaCli":true}`)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || out["gaId"] != "G-ABC123XYZ" || out["gaSecretSet"] != true || out["gaCli"] != true || strings.Contains(rec.Body.String(), "s3cret") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if v, _ := s.store.meta(gaSecretKey); v == "" || strings.Contains(v, "s3cret") {
		t.Errorf("secret stored as %q", v)
	}

	// A command-line run: in the usage log, and sent with the Measurement Protocol.
	got := make(chan string, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- r.URL.RawQuery + " " + string(b)
		w.WriteHeader(204)
	}))
	defer ts.Close()
	old := gaEndpoint
	gaEndpoint = ts.URL
	defer func() { gaEndpoint = old }()
	o := cliOptions{ConnParams: ConnParams{Engine: EngineMariaDB, Host: "db1", Username: "me", Password: "pw", Database: "shop"}, Tables: "a,b", Format: "csv", ExportDir: "/x"}
	recordCLI(s.store, cliRun{o: o, start: time.Now(), err: errors.New("boom"), function: cliFunction(o)})
	select {
	case q := <-got:
		for _, w := range []string{"measurement_id=G-ABC123XYZ", "api_secret=s3cret", `"name":"cli_export"`, `"engine":"mariadb"`, `"format":"csv"`, `"result":"failed"`, `"tables_bucket":"2-10"`} {
			if !strings.Contains(q, w) {
				t.Errorf("sent %s, missing %s", q, w)
			}
		}
		if strings.Contains(q, "db1") || strings.Contains(q, "shop") || strings.Contains(q, "boom") {
			t.Errorf("internal details sent: %s", q)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("nothing sent")
	}
	var fn, detail, ip string
	var status int
	if err := s.store.DB().QueryRow(`SELECT function, detail, client_ip, status FROM usage_events WHERE source = 'cli'`).Scan(&fn, &detail, &ip, &status); err != nil {
		t.Fatal(err)
	}
	if fn != "cli: export" || !strings.Contains(detail, "FAILED: boom") || strings.Contains(detail, "pw") || ip != "local" || status != 500 {
		t.Errorf("%q %q %q %d", fn, detail, ip, status)
	}

	// Google not reachable: the run isn't held up for long.
	gaEndpoint = "http://10.255.255.1:9/mp/collect" // never answers
	start := time.Now()
	recordCLI(s.store, cliRun{o: o, start: time.Now(), function: "export"})
	if d := time.Since(start); d > gaTimeout+time.Second {
		t.Errorf("waited %s", d)
	}

	// Switched off: nothing sent.
	gaEndpoint = ts.URL
	post(`{"gaCli":false}`)
	recordCLI(s.store, cliRun{o: o, start: time.Now(), function: "export"})
	select {
	case q := <-got:
		t.Errorf("sent while off: %s", q)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestUsageHousekeeping(t *testing.T) {
	s := newTestServer(t)
	if _, err := s.store.DB().Exec(usageSchema); err != nil {
		t.Fatal(err)
	}
	for _, at := range []string{"2026-10-30 23:59:59.999", "2026-11-01 00:00:00.000", "2026-11-02 12:00:00.000", "2026-11-03 00:00:00.000"} {
		s.store.DB().Exec(`INSERT INTO usage_events (at, source, function) VALUES (?, 'ui', 'x')`, at)
	}
	del := func(body string) (int, map[string]any) {
		req := jsonReq("POST", "/api/usage/delete", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.handleUsageDelete(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, out := del(`{"from":"2026-11-01T00:00:00Z","to":"2026-11-03T00:00:00Z"}`); code != 200 || out["deleted"] != float64(2) || out["count"] != float64(2) {
		t.Fatalf("%d %v", code, out)
	}
	if code, _ := del(`{"from":"2026-11-03T00:00:00Z","to":"2026-11-01T00:00:00Z"}`); code != 400 {
		t.Errorf("reversed range: %d", code)
	}
	if code, out := del(`{"from":"","to":""}`); code != 200 || out["deleted"] != float64(2) || out["count"] != float64(0) {
		t.Errorf("all: %d %v", code, out)
	}
}

func TestRefresh(t *testing.T) {
	s := newTestServer(t)
	path := newShopDB(t)
	db, err := openDB(context.Background(), ConnParams{Engine: EngineSQLite, Database: path})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s.sessions["s1"] = &session{engine: EngineSQLite, label: "shop", db: db}
	rw, err := sql.Open("sqlite", sqliteURI(path)) // cactusdb's own connection is read-only
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rw.Exec(`CREATE TABLE added_later (id INT)`); err != nil {
		t.Fatal(err)
	}
	rw.Close()
	rec := httptest.NewRecorder()
	s.handleRefresh(rec, jsonReq("POST", "/api/refresh", strings.NewReader(`{"session":"s1"}`)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "added_later") || !strings.Contains(rec.Body.String(), "trg_neg") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	s.handleRefresh(rec, jsonReq("POST", "/api/refresh", strings.NewReader(`{"session":"gone"}`)))
	if rec.Code != 401 {
		t.Errorf("unknown session: %d", rec.Code)
	}
}
