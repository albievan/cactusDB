package main

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
)

// Responses are gzip-compressed for a browser that accepts it: the page, its
// scripts and styles and the JSON of the API (query results above all) are
// text that shrinks to a fifth or less, so they arrive much sooner over a
// network. Downloads that are compressed already (.zip, .xlsx, .gz) and small
// answers are sent as they are.

// gzipMinSize: smaller answers aren't worth compressing.
const gzipMinSize = 1024

// JSON (query results, maybe many MB) at the fastest level, to keep up with the
// rows; the page, scripts and styles harder, as they are read once per upgrade.
var (
	gzipFast = sync.Pool{New: func() any { w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed); return w }}
	gzipBest = sync.Pool{New: func() any { w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestCompression); return w }}
)

// gzipCompressible tells whether a Content-Type is text worth compressing.
func gzipCompressible(ct string) bool {
	ct, _, _ = mime.ParseMediaType(ct)
	switch {
	case strings.HasPrefix(ct, "text/"):
		return ct != "text/event-stream"
	case ct == "application/json", ct == "application/javascript", ct == "application/xml", ct == "image/svg+xml", ct == "application/sql":
		return true
	}
	return false
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, q, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(name), "gzip") {
			q = strings.ReplaceAll(strings.TrimSpace(q), " ", "")
			return q != "q=0" && q != "q=0.0" && q != "q=0.00" && q != "q=0.000"
		}
	}
	return false
}

// gzipResponses compresses the responses of h for clients that accept gzip.
func gzipResponses(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		if r.Method == http.MethodHead || r.Header.Get("Range") != "" || !acceptsGzip(r) {
			h.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w}
		defer gw.close()
		h.ServeHTTP(gw, r)
	})
}

// gzipWriter decides at the response's first byte whether to compress: by its
// type, its declared length and whether it has an encoding already.
type gzipWriter struct {
	http.ResponseWriter
	zw      *gzip.Writer
	pool    *sync.Pool
	decided bool
	status  int
	buf     []byte // the first bytes, while an answer without a length may still be small
}

func (g *gzipWriter) WriteHeader(code int) {
	if g.status == 0 {
		g.status = code
	}
	h := g.Header()
	// No body (1xx, 204, 304) or not compressible: passed on as it is.
	if code < 200 || code == http.StatusNoContent || code == http.StatusNotModified || h.Get("Content-Encoding") != "" || !gzipCompressible(h.Get("Content-Type")) {
		g.pass()
		return
	}
	if n, err := strconv.Atoi(h.Get("Content-Length")); err == nil && n < gzipMinSize {
		g.pass()
	}
}

func (g *gzipWriter) pass() {
	if g.decided {
		return
	}
	g.decided = true
	g.ResponseWriter.WriteHeader(g.statusOr200())
}

func (g *gzipWriter) statusOr200() int {
	if g.status == 0 {
		return http.StatusOK
	}
	return g.status
}

func (g *gzipWriter) start() {
	g.decided = true
	h := g.Header()
	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	if et := h.Get("Etag"); et != "" && !strings.HasPrefix(et, "W/") {
		h.Set("Etag", "W/"+et)
	}
	g.pool = &gzipBest
	if ct, _, _ := mime.ParseMediaType(h.Get("Content-Type")); ct == "application/json" {
		g.pool = &gzipFast
	}
	g.zw = g.pool.Get().(*gzip.Writer)
	g.zw.Reset(g.ResponseWriter)
	g.ResponseWriter.WriteHeader(g.statusOr200())
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if !g.decided {
		h := g.Header()
		if h.Get("Content-Type") == "" {
			h.Set("Content-Type", http.DetectContentType(append(g.buf, b...)))
		}
		if g.status == 0 {
			g.WriteHeader(http.StatusOK)
		}
	}
	if g.decided {
		if g.zw != nil {
			return g.zw.Write(b)
		}
		return g.ResponseWriter.Write(b)
	}
	// Compressible, length unknown: wait until it is clearly big enough.
	g.buf = append(g.buf, b...)
	if len(g.buf) >= gzipMinSize {
		g.start()
		buf := g.buf
		g.buf = nil
		if _, err := g.zw.Write(buf); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

// Flush sends what is written so far (streaming answers keep working).
func (g *gzipWriter) Flush() {
	if !g.decided && len(g.buf) > 0 {
		g.start()
		g.zw.Write(g.buf)
		g.buf = nil
	}
	if g.zw != nil {
		g.zw.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *gzipWriter) close() {
	if g.zw != nil {
		g.zw.Close()
		g.pool.Put(g.zw)
		g.zw = nil
		return
	}
	if !g.decided {
		// A small answer: sent as it is, with its length.
		if len(g.buf) > 0 || g.status != 0 {
			if len(g.buf) > 0 && g.Header().Get("Content-Length") == "" {
				g.Header().Set("Content-Length", strconv.Itoa(len(g.buf)))
			}
			g.pass()
			g.ResponseWriter.Write(g.buf)
		}
	}
}

// staticETags fingerprints the embedded files, so the browser checks its copy
// ("no-cache") and gets a 304 instead of the whole page while it is unchanged.
var staticETags sync.Map

func staticETag(fsys fs.FS, urlPath string) string {
	p := strings.TrimPrefix(path.Clean(urlPath), "/")
	if p == "" || strings.HasSuffix(urlPath, "/") {
		p = path.Join(p, "index.html")
	}
	if v, ok := staticETags.Load(p); ok {
		return v.(string)
	}
	b, err := fs.ReadFile(fsys, p)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	et := `"` + hex.EncodeToString(sum[:8]) + `"`
	staticETags.Store(p, et)
	return et
}
