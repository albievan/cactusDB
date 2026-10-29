package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
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

func TestExportedFilesPreviewAndDelete(t *testing.T) {
	s := newTestServer(t)
	dir := s.exportFolder()
	put := func(kind, name string, data []byte) string {
		p := filepath.Join(dir, kind, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	var csvData strings.Builder
	csvData.WriteString("id,name\n")
	for i := range 250 {
		fmt.Fprintf(&csvData, "%d,\"name, %d\"\n", i, i)
	}
	put("csv", "big.csv", []byte(csvData.String()))
	put("json", "rows.json", []byte(`[
  {"id": 1, "name": "Ann", "z": null},
  {"id": 2, "name": "Bob"}
]`))
	put("sql", "ddl.sql", []byte("CREATE TABLE t (a int);\n"))
	// An Excel file and a backup .zip, made the way cactusdb makes them.
	xf := excelize.NewFile()
	xf.SetSheetRow("Sheet1", "A1", &[]any{"id", "name"})
	xf.SetSheetRow("Sheet1", "A2", &[]any{1, "Ann"})
	var xb bytes.Buffer
	xf.Write(&xb)
	put("xlsx", "t.xlsx", xb.Bytes())
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	w, _ := zw.Create("shop.sql")
	io.WriteString(w, "-- Backup of shop\nINSERT INTO t VALUES (1);\n")
	zw.Close()
	put("backup", "shop_backup.zip", zb.Bytes())

	preview := func(kind, name string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		b, _ := json.Marshal(map[string]string{"kind": kind, "name": name})
		s.handlePreviewFile(rec, httptest.NewRequest("POST", "/api/files/preview", bytes.NewReader(b)))
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if _, p := preview("csv", "big.csv"); p["mode"] != "table" || len(p["rows"].([]any)) != previewRows || p["truncated"] != true ||
		p["rows"].([]any)[0].([]any)[1] != "name, 0" {
		t.Errorf("csv: %v %v", p["mode"], p["truncated"])
	}
	if _, p := preview("xlsx", "t.xlsx"); p["mode"] != "table" || len(p["rows"].([]any)) != 1 || p["truncated"] != false {
		t.Errorf("xlsx: %v", p)
	}
	if _, p := preview("json", "rows.json"); p["mode"] != "json" || len(p["items"].([]any)) != 2 || p["truncated"] != false {
		t.Errorf("json: %v", p)
	}
	if _, p := preview("sql", "ddl.sql"); p["mode"] != "text" || p["text"] != "CREATE TABLE t (a int);\n" {
		t.Errorf("sql: %v", p)
	}
	if _, p := preview("backup", "shop_backup.zip"); p["mode"] != "zip" || len(p["entries"].([]any)) != 1 || !strings.Contains(p["text"].(string), "INSERT INTO") {
		t.Errorf("zip: %v", p)
	}
	for _, bad := range [][2]string{{"csv", "../csv/big.csv"}, {"..", "c.db"}, {"csv", "missing.csv"}} {
		if code, _ := preview(bad[0], bad[1]); code != 404 {
			t.Errorf("preview %v: %d", bad, code)
		}
	}

	del := func(contentType, body string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/files/delete", strings.NewReader(body))
		req.Header.Set("Content-Type", contentType)
		s.handleDeleteFiles(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, _ := del("text/plain", `{"files":[{"kind":"sql","name":"ddl.sql"}]}`); code != 415 {
		t.Errorf("non-JSON delete: %d", code)
	}
	outside := filepath.Join(t.TempDir(), "keep.txt")
	os.WriteFile(outside, []byte("x"), 0o644)
	code, res := del("application/json", `{"files":[{"kind":"sql","name":"ddl.sql"},{"kind":"csv","name":"big.csv"},{"kind":"csv","name":"../../`+filepath.Base(outside)+`"}]}`)
	if code != 200 || res["deleted"].(float64) != 2 {
		t.Fatalf("delete: %d %v", code, res)
	}
	for _, p := range []string{filepath.Join(dir, "sql", "ddl.sql"), filepath.Join(dir, "csv", "big.csv")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists", p)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Error("a file outside the export folder was deleted")
	}
	if len(s.listExportedFiles()) != 3 {
		t.Errorf("left: %v", s.listExportedFiles())
	}
}

func TestConnFolderName(t *testing.T) {
	for in, want := range map[string]string{
		"optimo-prod":              "optimo-prod",
		"Optima Prod (EU)":         "Optima Prod _EU",
		"../../etc":                "etc",
		"a/b\\c:d*e?":              "a_b_c_d_e",
		"  .hidden  ":              "hidden",
		"":                         "connection",
		"///":                      "connection",
		"CON":                      "_CON",
		"csv":                      "csv_db",
		"Zoë's DB":                 "Zoë_s DB",
		"root@10.66.1.136+replica": "root@10.66.1.136+replica",
	} {
		if got := connFolderName(in); got != want {
			t.Errorf("connFolderName(%q) = %q, want %q", in, got, want)
		}
		if got := connFolderName(in); !validFileName(got) {
			t.Errorf("connFolderName(%q) = %q is not a plain folder name", in, got)
		}
	}
	for p, want := range map[ConnParams]string{
		{Engine: EngineMariaDB, Host: "10.66.1.136", Database: "optima"}: "10.66.1.136-optima",
		{Engine: EngineSQLite, Database: "/data/shop.db"}:                "shop",
		{Engine: EngineFirebird, Host: "fb1", Database: "/db/ERP.FDB"}:   "fb1-ERP",
	} {
		if got := unnamedConnFolder(p); got != want {
			t.Errorf("unnamedConnFolder(%+v) = %q, want %q", p, got, want)
		}
	}
}

// Files are listed per connection folder, next to exports made before files
// were sorted by connection; the same name in two connections is two files.
func TestExportedFilesByConnection(t *testing.T) {
	s := newTestServer(t)
	dir := s.exportFolder()
	for _, p := range []string{"optimo-prod/csv/orders.csv", "optimo-test/csv/orders.csv", "optimo-prod/backup/b.zip", "csv/old.csv"} {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte("id\n1\n"), 0o644)
	}
	got := map[string]bool{}
	for _, f := range s.listExportedFiles() {
		got[f.key()] = true
	}
	for _, k := range []string{"optimo-prod/csv/orders.csv", "optimo-test/csv/orders.csv", "optimo-prod/backup/b.zip", "/csv/old.csv"} {
		if !got[k] {
			t.Errorf("%s not listed (%v)", k, got)
		}
	}
	if len(got) != 4 {
		t.Errorf("listed %v", got)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/files/zip", strings.NewReader(`{"files":[{"conn":"optimo-prod","kind":"csv","name":"orders.csv"},{"conn":"optimo-prod","kind":"backup","name":"b.zip"}]}`))
	req.Header.Set("Content-Type", "application/json")
	s.handlePrepareZip(rec, req)
	var res map[string]any
	json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != 200 || !strings.HasPrefix(res["name"].(string), "optimo-prod-exports_") {
		t.Errorf("zip of one connection: %d %v", rec.Code, res)
	}
	// Deleting one connection's file leaves the other connection's alone.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/files/delete", strings.NewReader(`{"files":[{"conn":"optimo-test","kind":"csv","name":"orders.csv"}]}`))
	req.Header.Set("Content-Type", "application/json")
	s.handleDeleteFiles(rec, req)
	if _, err := os.Stat(filepath.Join(dir, "optimo-prod/csv/orders.csv")); err != nil {
		t.Error("the other connection's file was deleted")
	}
	if _, err := os.Stat(filepath.Join(dir, "optimo-test/csv/orders.csv")); !os.IsNotExist(err) {
		t.Error("the file was not deleted")
	}
}
