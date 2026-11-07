package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A file deleted after the list was shown is left out of the .zip, which
// still downloads, instead of failing it.
func TestZipSkipsMissingFile(t *testing.T) {
	s := newTestServer(t)
	dir := filepath.Join(s.exportFolder(), "shop", "json")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "a.json"), []byte(`[1]`), 0o644)
	os.WriteFile(filepath.Join(dir, "b.json"), []byte(`[2]`), 0o644)
	// Gone between choosing and compressing: still in the listing, not on disk.
	orig := s.listExportedFiles()
	os.Remove(filepath.Join(dir, "b.json"))
	f, _ := os.CreateTemp(t.TempDir(), "z")
	defer f.Close()
	missing, err := writeZip(httptest.NewRequest("POST", "/", nil), f, orig)
	if err != nil || len(missing) != 1 || !strings.HasSuffix(missing[0], "b.json") {
		t.Fatalf("missing %v, %v", missing, err)
	}
	os.Remove(filepath.Join(dir, "a.json"))
	if _, err := writeZip(httptest.NewRequest("POST", "/", nil), f, orig); err == nil {
		t.Fatal("a .zip of files that are all gone must fail")
	}
}
