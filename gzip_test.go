package main

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// TestGzipResponses: big text answers are compressed for a browser that
// accepts gzip and decode to the same bytes; small ones, binary downloads and
// clients without gzip get them as they are.
func TestGzipResponses(t *testing.T) {
	big := `{"rows":[` + strings.Repeat(`["ann","bob",1234],`, 2000) + `[]]}`
	h := gzipResponses(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big":
			writeJSON(w, 200, map[string]string{"x": big})
		case "/small":
			writeJSON(w, 200, map[string]string{"x": "y"})
		case "/xlsx":
			w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
			w.Write([]byte(big))
		case "/err":
			writeErr(w, 400, io.ErrUnexpectedEOF)
		case "/stream":
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("first"))
			w.(http.Flusher).Flush()
			w.Write([]byte(strings.Repeat(" more", 500)))
		}
	}))
	get := func(path string, gz bool) *http.Response {
		r := httptest.NewRequest("GET", path, nil)
		if gz {
			r.Header.Set("Accept-Encoding", "gzip, deflate, br")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Result()
	}
	res := get("/big", true)
	if res.Header.Get("Content-Encoding") != "gzip" || res.Header.Get("Content-Length") != "" {
		t.Fatalf("big JSON: %v", res.Header)
	}
	zr, err := gzip.NewReader(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(zr)
	if strings.Count(string(b), "ann") != 2000 {
		t.Errorf("big JSON decoded: %d bytes", len(b))
	}
	if res := get("/big", false); res.Header.Get("Content-Encoding") != "" {
		t.Errorf("no gzip accepted: %v", res.Header)
	}
	if res := get("/small", true); res.Header.Get("Content-Encoding") != "" {
		t.Errorf("small JSON compressed: %v", res.Header)
	} else if b, _ := io.ReadAll(res.Body); !strings.Contains(string(b), `"y"`) {
		t.Errorf("small JSON: %q", b)
	}
	if res := get("/xlsx", true); res.Header.Get("Content-Encoding") != "" {
		t.Errorf("xlsx compressed: %v", res.Header)
	}
	if res := get("/err", true); res.StatusCode != 400 {
		t.Errorf("error status: %d", res.StatusCode)
	}
	res = get("/stream", true)
	zr, err = gzip.NewReader(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(zr); !strings.HasPrefix(string(b), "first more") {
		t.Errorf("stream: %q", b[:20])
	}
}

// TestStaticETag: an unchanged page answers 304, compressed or not.
func TestStaticETag(t *testing.T) {
	fsys := fstest.MapFS{"index.html": {Data: []byte("<html>" + strings.Repeat("x", 5000))}}
	files := http.FileServerFS(fsys)
	h := gzipResponses(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Etag", staticETag(fsys, r.URL.Path))
		files.ServeHTTP(w, r)
	}))
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	et := w.Result().Header.Get("Etag")
	if w.Code != 200 || !strings.HasPrefix(et, `W/"`) || w.Result().Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("first load: %d %v", w.Code, w.Result().Header)
	}
	r = httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	r.Header.Set("If-None-Match", et)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotModified || w.Body.Len() != 0 {
		t.Errorf("reload: %d, %d bytes", w.Code, w.Body.Len())
	}
}
