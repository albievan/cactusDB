package main

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestLogHasSession(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)
	h := logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			noteSession(r, "0123456789abcdef0123456789abcdef") // as s.session does with the id from the body
		}
		if r.URL.Path != "/api/engines" {
			logAction(r, "[shop] open table customers")
		}
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/query", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/select?session=fedcba9876543210&table=x", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/engines", nil))
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("want 5 log lines, got %q", lines)
	}
	for i, want := range []string{"session=01234567 [shop] open table", "session=01234567 POST /api/query -> 200",
		"session=fedcba98 [shop] open table", "session=fedcba98 GET /api/select", "GET /api/engines"} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("line %d = %q, want %q in it", i, lines[i], want)
		}
	}
	if strings.Contains(buf.String(), "0123456789abcdef") || strings.Contains(lines[4], "session=") {
		t.Errorf("a whole session id, or a session for a request without one, was logged:\n%s", buf.String())
	}
}
