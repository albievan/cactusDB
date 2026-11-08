package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAccessFiles reads every table of the Access files in
// $CACTUSDB_ACCESS_DIR (e.g. the samples of github.com/mdbtools/mdbtestdata)
// and checks that each has as many rows as its definition says.
func TestAccessFiles(t *testing.T) {
	dir := os.Getenv("CACTUSDB_ACCESS_DIR")
	if dir == "" {
		t.Skip("set CACTUSDB_ACCESS_DIR to a folder of .mdb/.accdb files")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	ctx := context.Background()
	for _, f := range files {
		db, err := openDB(ctx, ConnParams{Engine: EngineAccess, Database: f})
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		groups, err := listObjects(ctx, EngineAccess, db)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			db.Close()
			continue
		}
		for _, tab := range groups[0].Items {
			want, err := countRows(ctx, EngineAccess, db, tab)
			if err != nil {
				t.Errorf("%s %s: %v", f, tab, err)
				continue
			}
			q, err := buildSelect(ctx, EngineAccess, db, tab)
			if err != nil {
				t.Errorf("%s %s: %v", f, tab, err)
				continue
			}
			var n int64
			err = streamQuery(ctx, EngineAccess, db, q, func([]string) error { return nil }, func([]any) (bool, error) { n++; return true, nil })
			if err != nil || n != want {
				t.Errorf("%s %s: %d rows of %d, %v", filepath.Base(f), tab, n, want, err)
			}
		}
		for _, qn := range groups[1].Items {
			if def, err := objectDefinition(ctx, EngineAccess, db, ObjView, qn); err != nil || !strings.Contains(def, "SELECT") && !strings.HasPrefix(def, "--") {
				t.Errorf("%s query %s: %q %v", filepath.Base(f), qn, def, err)
			}
		}
		db.Close()
	}
}

func TestAccessQuery(t *testing.T) {
	aq, err := parseAccessQuery("SELECT TOP 5 [Company Name], City FROM [My Customers] WHERE Country = 'Germany' AND Since >= #2020-01-02# AND Notes IS NOT NULL ORDER BY City DESC, [Company Name];")
	if err != nil {
		t.Fatal(err)
	}
	if aq.top != 5 || aq.table != "My Customers" || len(aq.cols) != 2 || aq.cols[0] != "Company Name" || len(aq.where) != 3 ||
		aq.where[2].op != "IS NOT NULL" || len(aq.order) != 2 || !aq.order[0].desc {
		t.Errorf("%+v", aq)
	}
	for _, q := range []string{"SELECT * FROM a JOIN b", "SELECT a FROM t WHERE a = b", "DELETE FROM t"} {
		if _, err := parseAccessQuery(q); err == nil {
			t.Errorf("%s accepted", q)
		}
	}
	if !likeRegexp("Al*").MatchString("alfreds") || likeRegexp("A?").MatchString("abc") {
		t.Error("like")
	}
}
