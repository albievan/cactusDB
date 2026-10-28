package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The Exported files page lists the export subfolders and downloads a
// selection as one maximally compressed .zip, prepared before the download.
func TestExportedFilesZip(t *testing.T) {
	s := newTestServer(t)
	dir := s.exportFolder()
	write := func(kind, name, data string, age time.Duration) {
		p := filepath.Join(dir, kind, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		ts := time.Now().Add(-age)
		os.Chtimes(p, ts, ts)
	}
	big := strings.Repeat("customer,order,total\n", 5000)
	write("csv", "orders.csv", big, 2*time.Hour)
	write("json", "orders.json", `[{"a":1}]`, time.Hour)
	write("sql", "shop_ddl.sql", "CREATE TABLE t (a int);", 0)
	write("csv", ".cactusdb-export-123", "unfinished", 0) // a temp file: hidden

	rec := httptest.NewRecorder()
	s.handleListFiles(rec, httptest.NewRequest("GET", "/api/files", nil))
	var list struct{ Files []exportedFile }
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Files) != 3 || list.Files[0].Name != "shop_ddl.sql" || list.Files[2].Name != "orders.csv" || list.Files[2].Size != int64(len(big)) {
		t.Fatalf("list (newest first): %+v", list.Files)
	}

	prepare := func(contentType, body string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/files/zip", strings.NewReader(body))
		req.Header.Set("Content-Type", contentType)
		s.handlePrepareZip(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, _ := prepare("text/plain", `{"files":[{"kind":"csv","name":"orders.csv"}]}`); code != 415 {
		t.Errorf("non-JSON request: %d", code)
	}
	for _, bad := range []string{`{"files":[]}`, `{"files":[{"kind":"csv","name":"../../c.db"}]}`, `{"files":[{"kind":"..","name":"c.db"}]}`, `{"files":[{"kind":"csv","name":".cactusdb-export-123"}]}`} {
		if code, _ := prepare("application/json", bad); code == 200 {
			t.Errorf("%s was accepted", bad)
		}
	}

	code, res := prepare("application/json", `{"files":[{"kind":"csv","name":"orders.csv"},{"kind":"sql","name":"shop_ddl.sql"},{"kind":"csv","name":"orders.csv"}]}`)
	if code != 200 || res["files"].(float64) != 2 || !strings.HasPrefix(res["name"].(string), "cactusdb-exports_") {
		t.Fatalf("prepare: %d %v", code, res)
	}
	if res["zipSize"].(float64) >= res["size"].(float64)/10 {
		t.Errorf("not compressed much: %v of %v", res["zipSize"], res["size"])
	}
	id := strings.TrimPrefix(res["download"].(string), "/api/files/zip/")
	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", res["download"].(string), nil)
		req.SetPathValue("id", id)
		s.handleDownloadZip(rec, req)
		return rec
	}
	rec = get()
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Disposition"), res["name"].(string)) {
		t.Fatalf("download: %d %v", rec.Code, rec.Header())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range zr.File {
		if f.Method != zip.Deflate {
			t.Errorf("%s not deflated", f.Name)
		}
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = string(b)
	}
	if len(got) != 2 || got["csv/orders.csv"] != big || got["sql/shop_ddl.sql"] != "CREATE TABLE t (a int);" {
		t.Errorf("zip holds %v", len(got))
	}

	// One file is named after it; dropped downloads are gone.
	if _, one := prepare("application/json", `{"files":[{"kind":"json","name":"orders.json"}]}`); one["name"] != "orders.json.zip" {
		t.Errorf("single file zip name: %v", one["name"])
	}
	s.dropZips()
	if rec := get(); rec.Code != 404 {
		t.Errorf("after expiry: %d", rec.Code)
	}
	if len(s.zips) != 0 {
		t.Error("zips left behind")
	}
}
