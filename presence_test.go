package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPresenceAndNotices(t *testing.T) {
	s := newTestServer(t)
	checkIn := func(client, ip string) (clients []presenceView, notices []notice) {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/presence", strings.NewReader(`{"client":"`+client+`"}`))
		r.RemoteAddr = ip + ":50000"
		w := httptest.NewRecorder()
		s.handlePresence(w, r)
		if w.Code != 200 {
			t.Fatalf("check-in of %s: %d %s", client, w.Code, w.Body)
		}
		var res struct {
			Clients []presenceView `json:"clients"`
			Notices []notice       `json:"notices"`
		}
		json.Unmarshal(w.Body.Bytes(), &res)
		return res.Clients, res.Notices
	}
	s.notify(nil, "upgrade", 0, "before anyone was here")
	if _, n := checkIn("page-aaaa", "10.0.0.1"); len(n) != 0 {
		t.Errorf("a page's first check-in got old notices: %v", n)
	}
	clients, _ := checkIn("page-bbbb", "10.0.0.2")
	if len(clients) != 2 || clients[0].IP != "10.0.0.1" || clients[1].IP != "10.0.0.2" || clients[0].You || !clients[1].You {
		t.Errorf("clients = %+v", clients)
	}

	// A change made by page b: page a is told, page b isn't.
	r := httptest.NewRequest("POST", "/api/connections", nil)
	r.RemoteAddr = "10.0.0.2:50001"
	r.Header.Set("X-Cactusdb-Client", "page-bbbb")
	s.notify(r, "connections", 0, "added the saved connection “%s”", "shop")
	s.notify(nil, "containers", 0, "deployed the container pg1")
	if _, n := checkIn("page-aaaa", "10.0.0.1"); len(n) != 2 || n[0].IP != "10.0.0.2" || n[0].Kind != "connections" || n[1].IP != "" {
		t.Errorf("page a got %+v", n)
	}
	if _, n := checkIn("page-aaaa", "10.0.0.1"); len(n) != 0 {
		t.Errorf("notices given twice: %+v", n)
	}
	if _, n := checkIn("page-bbbb", "10.0.0.2"); len(n) != 1 || n[0].Kind != "containers" {
		t.Errorf("page b got %+v, want only the container notice", n)
	}

	// A page that stops checking in is gone; one that leaves, straight away.
	s.presence.clients["page-aaaa"].seen = time.Now().Add(-2 * presenceGone)
	if clients, _ := checkIn("page-bbbb", "10.0.0.2"); len(clients) != 1 {
		t.Errorf("a quiet page is still listed: %+v", clients)
	}
	w := httptest.NewRecorder()
	s.handlePresenceLeave(w, httptest.NewRequest("POST", "/api/presence/leave", strings.NewReader(`{"client":"page-bbbb"}`)))
	if len(s.presence.clients) != 0 {
		t.Errorf("a page that left is still listed")
	}

	w = httptest.NewRecorder()
	s.handlePresence(w, httptest.NewRequest("POST", "/api/presence", strings.NewReader(`{"client":"x"}`)))
	if w.Code != 400 {
		t.Errorf("a bad client id: %d", w.Code)
	}
}
