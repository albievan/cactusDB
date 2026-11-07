package main

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Exports of saved queries go to <connection>/queries/<dd.mm.yyyy>/
// <name>-<dd.mm.yyyy.hh.mm>.<ext>, are listed, previewed and downloaded.
func TestSavedQueryExport(t *testing.T) {
	ctx := context.Background()
	src := filepath.Join(t.TempDir(), "shop.db")
	w, err := sql.Open("sqlite", sqliteURI(src))
	if err != nil {
		t.Fatal(err)
	}
	w.Exec(`CREATE TABLE codes (code TEXT, name TEXT); INSERT INTO codes VALUES ('A','x'), ('','y'), ('B','z')`)
	w.Close()
	db, err := openDB(ctx, ConnParams{Engine: EngineSQLite, Database: src})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	s := newTestServer(t)
	connDir := filepath.Join(s.exportFolder(), "shop-prod")
	var paths []string
	for _, f := range []string{"csv", "xlsx", "csv"} { // the second csv in the same minute gets _2
		p, rows, err := queryExportToDir(ctx, connDir, EngineSQLite, db, "SELECT * FROM codes WHERE code <> ''", f, "cutter_subdiv_code <> ''")
		if err != nil || rows != 2 {
			t.Fatalf("export %s: %d rows, %v", f, rows, err)
		}
		paths = append(paths, p)
	}
	rel, _ := filepath.Rel(s.exportFolder(), paths[0])
	if !regexp.MustCompile(`^shop-prod/queries/\d\d\.\d\d\.\d{4}/cutter_subdiv_code-\d\d\.\d\d\.\d{4}\.\d\d\.\d\d\.csv$`).MatchString(filepath.ToSlash(rel)) {
		t.Fatalf("exported to %s", rel)
	}
	if !strings.HasSuffix(paths[2], "_2.csv") || !strings.HasSuffix(paths[1], ".xlsx") {
		t.Fatalf("paths %v", paths)
	}

	// Listed as kind queries, name <day>/<file>; previewed by its extension.
	var found *exportedFile
	for _, f := range s.listExportedFiles() {
		if f.path == paths[0] {
			found = &f
		}
	}
	if found == nil || found.Kind != "queries" || found.Conn != "shop-prod" || found.format() != "csv" {
		t.Fatalf("listed as %+v", found)
	}

	// The download link of the export serves it.
	u := downloadURL(paths[1])
	if !strings.HasPrefix(u, "/api/exports/shop-prod/queries/") {
		t.Fatalf("download url %s", u)
	}
	req := httptest.NewRequest("GET", u, nil)
	req.SetPathValue("path", strings.TrimPrefix(u, "/api/exports/"))
	rec := httptest.NewRecorder()
	s.handleDownload(rec, req)
	if b, _ := os.ReadFile(paths[1]); rec.Code != 200 || rec.Body.Len() != len(b) {
		t.Fatalf("download: %d, %d bytes", rec.Code, rec.Body.Len())
	}
	for _, bad := range []string{"shop-prod/queries/../csv/x.csv", "shop-prod/queries/.x/a.csv", "queries/x/a.csv"} {
		req := httptest.NewRequest("GET", "/api/exports/x", nil)
		req.SetPathValue("path", bad)
		rec := httptest.NewRecorder()
		s.handleDownload(rec, req)
		if rec.Code == 200 {
			t.Errorf("%s was served", bad)
		}
	}
}
