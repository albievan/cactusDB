package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUsageLog(t *testing.T) {
	s := newTestServer(t)
	if err := startUsageLog(s.store.db); err != nil {
		t.Fatal(err)
	}
	defer func() { usageEvents = nil }()
	db, err := openDB(context.Background(), ConnParams{Engine: EngineSQLite, Database: newShopDB(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s.sessions["abcdef1234567890"] = &session{engine: EngineSQLite, label: "shop", db: db, lastUsed: time.Now()}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/definition", s.handleDefinition)
	mux.HandleFunc("GET /api/exportall/{job}", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("POST /api/usage", s.handleUsage)
	h := logRequests(mux)
	do := func(method, url, body string) {
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		req.RemoteAddr = "10.1.2.3:5555"
		req.Header.Set("X-Cactusdb-Page", "page1")
		req.Header.Set("User-Agent", "test-browser")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	do("GET", "/api/definition?session=abcdef1234567890&type=table&name=customer", "")
	do("GET", "/api/exportall/job1", "") // a progress poll: not recorded
	do("POST", "/api/usage", `{"page":"page1","session":"abcdef1234567890","events":[{"fn":"tab: logs","detail":"Logs","ago":1500},{"fn":"set: cmpDrops","detail":"on"}]}`)

	type row struct{ source, fn, detail, ip, session, page, conn, engine, agent string }
	var rows []row
	for deadline := time.Now().Add(5 * time.Second); len(rows) < 3 && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		rows = rows[:0]
		res, err := s.store.db.Query(`SELECT source, function, detail, client_ip, session_id, page_id, connection, engine, user_agent FROM usage_events ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		for res.Next() {
			var r row
			res.Scan(&r.source, &r.fn, &r.detail, &r.ip, &r.session, &r.page, &r.conn, &r.engine, &r.agent)
			rows = append(rows, r)
		}
		res.Close()
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows: %+v", len(rows), rows)
	}
	want := []row{
		{"server", "show definition", "[shop] definition of table customer", "10.1.2.3", "abcdef12", "page1", "shop", EngineSQLite, "test-browser"},
		{"ui", "tab: logs", "Logs", "10.1.2.3", "abcdef12", "page1", "shop", EngineSQLite, "test-browser"},
		{"ui", "set: cmpDrops", "on", "10.1.2.3", "abcdef12", "page1", "shop", EngineSQLite, "test-browser"},
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], want[i])
		}
	}
	var status, ms int
	if err := s.store.db.QueryRow(`SELECT status, duration_ms FROM usage_events WHERE source = 'server'`).Scan(&status, &ms); err != nil || status != 200 {
		t.Errorf("status %d, %v", status, err)
	}
}
