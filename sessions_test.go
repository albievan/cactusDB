package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCloseIdleSessions(t *testing.T) {
	s := newTestServer(t)
	open := func() *session {
		db, err := openDB(context.Background(), ConnParams{Engine: EngineSQLite, Database: newShopDB(t)})
		if err != nil {
			t.Fatal(err)
		}
		return &session{engine: EngineSQLite, label: "shop", db: db}
	}
	idle, busy, fresh := open(), open(), open()
	idle.lastUsed = time.Now().Add(-time.Hour)
	busy.lastUsed = time.Now().Add(-time.Hour)
	fresh.lastUsed = time.Now()
	s.sessions["idle"], s.sessions["busy"], s.sessions["fresh"] = idle, busy, fresh
	s.batches = map[string]*webBatch{"j": {exportBatch: &exportBatch{done: make(chan struct{})}, session: "busy"}}

	s.closeIdleSessions(30 * time.Minute)
	if _, ok := s.sessions["idle"]; ok {
		t.Error("idle session kept")
	}
	if idle.db.Ping() == nil {
		t.Error("idle session's database still open")
	}
	if _, ok := s.sessions["busy"]; !ok {
		t.Error("session with a running export closed")
	}
	if _, ok := s.sessions["fresh"]; !ok {
		t.Error("recently used session closed")
	}
	busy.db.Close()
	fresh.db.Close()
}

func TestIdleMinutesSetting(t *testing.T) {
	s := newTestServer(t)
	if got := s.idleMinutes(); got != defaultIdleMinutes {
		t.Fatalf("default idle = %d", got)
	}
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := jsonReq("POST", "/api/settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		s.handleSaveSettings(rec, req)
		return rec
	}
	before := s.exportFolder()
	if rec := post(`{"idleMinutes": 5}`); rec.Code != 200 || s.idleMinutes() != 5 {
		t.Fatalf("set 5: %d %s, idle %d", rec.Code, rec.Body, s.idleMinutes())
	}
	if s.exportFolder() != before {
		t.Error("saving the idle time changed the export folder")
	}
	if rec := post(`{"idleMinutes": 0}`); rec.Code != 200 || s.idleMinutes() != 0 {
		t.Fatalf("set 0: %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{`{"idleMinutes": -1}`, `{"idleMinutes": 99999}`} {
		if rec := post(bad); rec.Code != 400 || s.idleMinutes() != 0 {
			t.Errorf("%s: %d, idle %d", bad, rec.Code, s.idleMinutes())
		}
	}
}
