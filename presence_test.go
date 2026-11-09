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

func TestDirectMessages(t *testing.T) {
	s := newTestServer(t)
	type answer struct {
		Clients  []presenceView `json:"clients"`
		Messages []message      `json:"messages"`
	}
	checkIn := func(client, ip, name string) answer {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/presence", strings.NewReader(`{"client":"`+client+`","name":"`+name+`"}`))
		r.RemoteAddr = ip + ":50000"
		w := httptest.NewRecorder()
		s.handlePresence(w, r)
		var a answer
		json.Unmarshal(w.Body.Bytes(), &a)
		return a
	}
	send := func(client, to, text string) (int, message) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"client": client, "to": to, "text": text})
		w := httptest.NewRecorder()
		s.handleMessage(w, httptest.NewRequest("POST", "/api/messages", strings.NewReader(string(body))))
		var m message
		json.Unmarshal(w.Body.Bytes(), &m)
		return w.Code, m
	}
	handle := func(a answer) string {
		for _, c := range a.Clients {
			if c.You {
				return c.Handle
			}
		}
		return ""
	}
	checkIn("page-aaaa", "10.0.0.1", "Ann")
	checkIn("page-bbbb", "10.0.0.2", "")
	checkIn("page-cccc", "10.0.0.3", "")
	a, b := handle(checkIn("page-aaaa", "10.0.0.1", "Ann")), handle(checkIn("page-bbbb", "10.0.0.2", ""))
	if a == "" || b == "" || a == "page-aaaa" {
		t.Fatalf("handles %q %q: a page's handle must not be its secret id", a, b)
	}

	if code, m := send("page-aaaa", b, "  hello B  "); code != 200 || m.Text != "hello B" || m.From != a || m.FromName != "Ann" || m.FromIP != "10.0.0.1" {
		t.Fatalf("send: %d %+v", code, m)
	}
	if got := checkIn("page-cccc", "10.0.0.3", "").Messages; len(got) != 0 {
		t.Errorf("a third page was given the message: %+v", got)
	}
	got := checkIn("page-bbbb", "10.0.0.2", "").Messages
	if len(got) != 1 || got[0].Text != "hello B" {
		t.Fatalf("B got %+v", got)
	}
	if again := checkIn("page-bbbb", "10.0.0.2", "").Messages; len(again) != 0 {
		t.Errorf("given twice: %+v", again)
	}
	// B replies to the handle it was written from.
	if code, _ := send("page-bbbb", got[0].From, "hi Ann"); code != 200 {
		t.Fatalf("reply: %d", code)
	}
	if m := checkIn("page-aaaa", "10.0.0.1", "Ann").Messages; len(m) != 2 || m[1].Text != "hi Ann" || m[1].To != a {
		t.Errorf("A got %+v, want its own message and the reply", m)
	}
	if m := checkIn("page-cccc", "10.0.0.3", "").Messages; len(m) != 0 {
		t.Errorf("a third page was given the reply: %+v", m)
	}

	// Not from an unknown page, not to itself, not to a closed one, not empty.
	if code, _ := send("page-zzzz", b, "x"); code != 409 {
		t.Errorf("from an unknown page: %d", code)
	}
	if code, _ := send("page-aaaa", a, "x"); code != 400 {
		t.Errorf("to itself: %d", code)
	}
	if code, _ := send("page-aaaa", b, "   "); code != 400 {
		t.Errorf("empty: %d", code)
	}
	s.handlePresenceLeave(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/presence/leave", strings.NewReader(`{"client":"page-bbbb"}`)))
	if code, _ := send("page-aaaa", b, "still there?"); code != 404 {
		t.Errorf("to a closed page: %d", code)
	}
	s.handlePresenceLeave(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/presence/leave", strings.NewReader(`{"client":"page-aaaa"}`)))
	if len(s.presence.messages) != 0 {
		t.Errorf("messages kept after both pages closed: %d", len(s.presence.messages))
	}
}
