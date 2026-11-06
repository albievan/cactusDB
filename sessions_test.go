package main

import (
	"context"
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
