package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStatusInHeader(t *testing.T) {
	h := statusInHeader(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, 403, errors.New("this connection is protected"))
	}))
	for _, c := range []struct {
		name   string
		req    func() *http.Request
		status int
		inHdr  string
	}{
		{"web UI", func() *http.Request {
			r := httptest.NewRequest("POST", "/api/connect", nil)
			r.Header.Set(statusInHeaderReq, "in-header")
			return r
		}, 200, "403"},
		{"download link", func() *http.Request { return httptest.NewRequest("GET", "/api/exports/a.csv?errors=in-header", nil) }, 200, "403"},
		{"script", func() *http.Request { return httptest.NewRequest("POST", "/api/connect", nil) }, 403, ""},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, c.req())
		if w.Code != c.status || w.Header().Get(statusInHeaderResp) != c.inHdr {
			t.Errorf("%s: status %d, %s %q; want %d, %q", c.name, w.Code, statusInHeaderResp, w.Header().Get(statusInHeaderResp), c.status, c.inHdr)
		}
	}
}

// Requests that change something are refused when the browser says another
// site sent them (CSRF), and readJSON wants a JSON body.
func TestSameOriginOnly(t *testing.T) {
	h := sameOriginOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	cases := []struct {
		method string
		hdr    map[string]string
		want   int
	}{
		{"POST", nil, 204}, // curl, scripts
		{"POST", map[string]string{"Origin": "http://cdb.local:8080"}, 204},
		{"POST", map[string]string{"Origin": "http://CDB.local:8080", "Sec-Fetch-Site": "same-origin"}, 204},
		{"POST", map[string]string{"Origin": "https://evil.example"}, 400},
		{"POST", map[string]string{"Origin": "null"}, 400},
		{"POST", map[string]string{"Sec-Fetch-Site": "cross-site"}, 400},
		{"POST", map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "http://cdb.local:8080"}, 400},
		{"GET", map[string]string{"Origin": "https://evil.example"}, 204},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "http://cdb.local:8080/api/testconn", strings.NewReader("{}"))
		for k, v := range c.hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s %v: %d, want %d", c.method, c.hdr, rec.Code, c.want)
		}
	}
	var v map[string]any
	req := httptest.NewRequest("POST", "/api/testconn", strings.NewReader(`{"a":1}`))
	req.Header.Set("Content-Type", "text/plain")
	if readJSON(req, &v) == nil {
		t.Error("readJSON took a text/plain body")
	}
}

// jsonReq is a request with a JSON body, as the page sends it.
func jsonReq(method, target string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	r.Header.Set("Content-Type", "application/json")
	return r
}
