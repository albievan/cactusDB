package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCLIValues(t *testing.T) {
	for in, want := range map[string]string{"MariaDB": EngineMariaDB, "postgresql": EnginePostgres, "sqlserver": EngineMSSQL, "sqlite": EngineSQLite} {
		if got, err := parseEngine(in); err != nil || got != want {
			t.Errorf("parseEngine(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := parseEngine("oracle"); err == nil {
		t.Error("oracle accepted")
	}
	for in, want := range map[string]string{"excel": "xlsx", "XLSX": "xlsx", "csv": "csv", "json": "json", "xml": "xml"} {
		if got, err := parseExportFormat(in); err != nil || got != want {
			t.Errorf("parseExportFormat(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := parseExportFormat("pdf"); err == nil {
		t.Error("pdf accepted")
	}
	if on, given, err := parseYesNo("yes"); !on || !given || err != nil {
		t.Error("--web yes")
	}
	if on, given, _ := parseYesNo(""); on || given {
		t.Error("--web omitted")
	}
	if _, _, err := parseYesNo("maybe"); err == nil {
		t.Error("--web maybe accepted")
	}
}

func TestResolveTable(t *testing.T) {
	groups := []ObjectGroup{
		{Type: ObjTable, Queryable: true, Items: []string{"public.orders", "sales.orders", "sales.invoices", "Stock"}},
		{Type: ObjView, Queryable: true, Items: []string{"public.v_totals"}},
		{Type: ObjProcedure, Items: []string{"public.not_a_table"}},
	}
	for in, want := range map[string]tableRef{
		"sales.orders": {ObjTable, "sales.orders"},
		"invoices":     {ObjTable, "sales.invoices"},
		"stock":        {ObjTable, "Stock"},
		"v_totals":     {ObjView, "public.v_totals"},
	} {
		if got, err := resolveTable(groups, in); err != nil || got != want {
			t.Errorf("resolveTable(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := resolveTable(groups, "orders"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("orders should be ambiguous, got %v", err)
	}
	for _, in := range []string{"missing", "not_a_table"} {
		if _, err := resolveTable(groups, in); err == nil {
			t.Errorf("%q should not resolve", in)
		}
	}
}

func TestRunCLIExportsSQLite(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "shop.db")
	db, err := sql.Open("sqlite", sqliteURI(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE customers (id INTEGER, name TEXT); INSERT INTO customers VALUES (1, 'Ann'), (2, 'Bob');
		CREATE TABLE orders (id INTEGER); INSERT INTO orders VALUES (10);`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	opts := cliOptions{ConnParams: ConnParams{Engine: EngineSQLite, Database: dbPath}, Format: "json", ExportDir: filepath.Join(dir, "export")}

	var out bytes.Buffer
	opts.Tables = "all"
	if err := runCLI(context.Background(), opts, &out); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "export", "*.json"))
	if len(files) != 2 || !strings.Contains(out.String(), "2 rows") {
		t.Fatalf("expected 2 exports, got %v\n%s", files, out.String())
	}
	data, _ := os.ReadFile(files[0])
	if !strings.Contains(string(data), `"name": "Ann"`) {
		t.Errorf("customers export: %s", data)
	}

	out.Reset()
	opts.Tables, opts.Show = "", "all"
	if err := runCLI(context.Background(), opts, &out); err != nil || !strings.Contains(out.String(), "  orders\n") {
		t.Errorf("--showtable all: %v\n%s", err, out.String())
	}
	out.Reset()
	opts.Show = "customers"
	if err := runCLI(context.Background(), opts, &out); err != nil || !strings.Contains(out.String(), "CREATE TABLE customers") {
		t.Errorf("--showtable customers: %v\n%s", err, out.String())
	}
	opts.Show, opts.Tables = "", "nope"
	if err := runCLI(context.Background(), opts, &out); err == nil {
		t.Error("exporting a missing table should fail")
	}
}
