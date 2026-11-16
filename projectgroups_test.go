package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// TestProjectGroupedExport: a query that groups its rows ("-- group rows by")
// exports them grouped: Excel as an outline, JSON and XML nested, CSV sorted
// with the group columns first.
func TestProjectGroupedExport(t *testing.T) {
	q := "SELECT name, loc AS Location, qty FROM t\n-- group rows by: [\"Location\"]"
	if g := projectGroupRows(q); len(g) != 1 || g[0] != "Location" {
		t.Fatalf("projectGroupRows: %v", g)
	}
	if g := projectGroupRows("SELECT 1 -- group rows by: nothing"); g != nil {
		t.Errorf("a comment that isn't a grouping: %v", g)
	}
	db, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, st := range []string{`CREATE TABLE t (name TEXT, loc TEXT, qty INTEGER)`,
		`INSERT INTO t VALUES ('a', 'STORE Q', 1), ('b', 'BIN', 2), ('c', 'STORE Q', 3), ('d', NULL, 4), ('e', 'BIN', 5)`} {
		if _, err := db.Exec(st); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	read := func(f string) string {
		t.Helper()
		path, n, err := exportGroupedToDir(t.Context(), dir, db, q, f, "grouped", projectGroupRows(q))
		if err != nil || n != 5 {
			t.Fatalf("%s: %d rows, %v", f, n, err)
		}
		b, _ := os.ReadFile(path)
		return path + "\n" + string(b)
	}
	if csv := read("csv"); !strings.Contains(csv, "Location,name,qty\n,d,4\nBIN,b,2\nBIN,e,5\nSTORE Q,a,1\nSTORE Q,c,3\n") {
		t.Errorf("csv: %q", csv)
	}
	var js []map[string]any
	j := read("json")
	if err := json.Unmarshal([]byte(j[strings.Index(j, "\n")+1:]), &js); err != nil || len(js) != 3 || js[1]["Location"] != "BIN" || js[1]["rows"] != 2.0 ||
		len(js[1]["items"].([]any)) != 2 || js[1]["items"].([]any)[0].(map[string]any)["name"] != "b" {
		t.Errorf("json: %v (%v)", js, err)
	}
	if x := read("xml"); !strings.Contains(x, `<group column="Location" value="BIN" rows="2">`) || !strings.Contains(x, `<group column="Location" null="true" rows="1">`) {
		t.Errorf("xml: %s", x)
	}
	x := read("xlsx")
	f, err := excelize.OpenFile(strings.SplitN(x, "\n", 2)[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, _ := f.GetRows(f.GetSheetName(0))
	// The empty (NULL) group first, then BIN, then STORE Q.
	if len(rows) != 1+3+5 || rows[1][0] != "Location: (empty) (1 row)" || rows[3][0] != "Location: BIN (2 rows)" || rows[4][0] != "b" {
		t.Errorf("xlsx rows: %v", rows)
	}
	if lv, _ := f.GetRowOutlineLevel(f.GetSheetName(0), 5); lv != 1 {
		t.Errorf("a row of a group: outline level %d", lv)
	}
	if lv, _ := f.GetRowOutlineLevel(f.GetSheetName(0), 4); lv != 0 {
		t.Errorf("a group's row: outline level %d", lv)
	}
}
