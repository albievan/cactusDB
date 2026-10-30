package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

// A panic in an export worker fails that table instead of stopping cactusdb.
func TestExportJobPanicFailsTable(t *testing.T) {
	var db *sql.DB // nil: the first query panics
	b := startExportBatch(context.Background(), EngineMariaDB, db, t.TempDir(), "csv", []tableRef{{ObjTable, "t1"}, {ObjTable, "t2"}}, 2, nil)
	select {
	case <-b.done:
	case <-time.After(10 * time.Second):
		t.Fatal("export batch did not finish")
	}
	for _, j := range b.jobs {
		if j.state.Load() != jobFailed || j.err == nil || !strings.Contains(j.err.Error(), "internal error in cactusdb while exporting "+j.table.name) {
			t.Errorf("%s: state %d err %v", j.table.name, j.state.Load(), j.err)
		}
	}
}

// The workbook is written straight to the file, and still opens.
func TestExportXLSXStreamsValidWorkbook(t *testing.T) {
	ctx := context.Background()
	db, err := openDB(ctx, ConnParams{Engine: EngineSQLite, Database: newShopDB(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	path, n, err := exportToDir(ctx, t.TempDir(), EngineSQLite, db, "SELECT * FROM customer", "xlsx", "customer", nil)
	if err != nil || n != 3 {
		t.Fatalf("export: %d rows, %v", n, err)
	}
	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := f.GetRows("customer")
	if err != nil || len(rows) != 4 || rows[0][1] != "name" || rows[3][1] != "Cy" {
		t.Fatalf("rows %v, %v", rows, err)
	}
}

func TestTrackRunReportsPreviousRun(t *testing.T) {
	dir := t.TempDir()
	if prev, stopped := trackRun(dir, 1); prev != "" {
		t.Fatalf("first run: %q", prev)
	} else {
		stopped()
	}
	if prev, _ := trackRun(dir, 1); prev != "" { // the previous run stopped normally
		t.Fatalf("after a normal stop: %q", prev)
	}
	// This run "dies": its marker stays.
	prev, stopped := trackRun(dir, 1)
	if !strings.Contains(prev, "stopped without shutting down") || !strings.Contains(prev, "out of memory") {
		t.Fatalf("after a kill: %q", prev)
	}
	stopped()

	// A crash logged after the previous run started is reported with its error.
	marker := "pid=42\nstarted=" + time.Now().Add(-time.Minute).Format(time.RFC3339) + "\nversion=1.0\n"
	crash := filepath.Join(dir, crashLogName)
	os.WriteFile(crash, []byte("panic: old one\n\ngoroutine 1\nfatal error: concurrent map writes\n\ngoroutine 7 [running]:\n"), 0o600)
	got := previousRun(marker, crash)
	if !strings.Contains(got, "process 42") || !strings.Contains(got, "crashed: fatal error: concurrent map writes") {
		t.Fatalf("crash: %q", got)
	}
	// An old crash log doesn't count.
	old := time.Now().Add(-time.Hour)
	os.Chtimes(crash, old, old)
	if got := previousRun(marker, crash); !strings.Contains(got, "stopped without shutting down") {
		t.Fatalf("old crash log: %q", got)
	}
}
