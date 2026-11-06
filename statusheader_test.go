package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
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
