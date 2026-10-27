package main

import (
	"archive/zip"
	"context"
	"database/sql"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSQLLiteral(t *testing.T) {
	ts := time.Date(2026, 10, 27, 13, 4, 5, 120000000, time.UTC)
	for _, c := range []struct {
		engine, typ string
		v           any
		want        string
	}{
		{EngineSQLite, "TEXT", nil, "NULL"},
		{EngineSQLite, "TEXT", "it's", "'it''s'"},
		{EngineSQLite, "BLOB", []byte{0, 1, 0xff}, "X'0001ff'"},
		{EngineSQLite, "", []byte("abc"), "X'616263'"}, // SQLite bytes are always a BLOB
		{EngineSQLite, "REAL", 1.5, "1.5"},
		{EngineSQLite, "INTEGER", int64(-7), "-7"},
		{EngineMySQL, "VARCHAR", []byte(`a\b'c`), `'a\\b''c'`},
		{EngineMySQL, "INT", []byte("42"), "42"},
		{EngineMySQL, "DECIMAL", []byte("-1.50"), "-1.50"},
		{EngineMySQL, "DATETIME", []byte("2026-10-27 13:04:05"), "'2026-10-27 13:04:05'"},
		{EngineMariaDB, "BLOB", []byte("x"), "X'78'"},
		{EngineMariaDB, "BIT", []byte{1}, "X'01'"},
		{EnginePostgres, "BOOL", true, "TRUE"},
		{EnginePostgres, "BYTEA", []byte{0xde, 0xad}, `'\xdead'::bytea`},
		{EnginePostgres, "NUMERIC", "12.340", "12.340"},
		{EnginePostgres, "TEXT", `back\slash`, `'back\slash'`},
		{EnginePostgres, "TIMESTAMP", ts, "'2026-10-27 13:04:05.12'"},
		{EnginePostgres, "FLOAT8", math.NaN(), "'NaN'"},
		{EngineMSSQL, "NVARCHAR", "Zoë", "N'Zoë'"},
		{EngineMSSQL, "VARBINARY", []byte{1, 2}, "0x0102"},
		{EngineMSSQL, "BIT", false, "0"},
		{EngineMSSQL, "DECIMAL", []byte("3.14"), "3.14"},
		{EngineMSSQL, "UNIQUEIDENTIFIER", []byte{0x67, 0x45, 0x23, 0x01, 0xab, 0x89, 0xef, 0xcd, 1, 2, 3, 4, 5, 6, 7, 8}, "N'01234567-89AB-CDEF-0102-030405060708'"},
		{EngineFirebird, "BLOB", []byte("plain text"), "'plain text'"},
		{EngineFirebird, "BLOB", []byte{0xff, 0xfe}, "X'fffe'"},
		{EngineFirebird, "DATE", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), "'2026-01-02'"},
	} {
		if got := sqlLiteral(c.engine, c.typ, c.v); got != c.want {
			t.Errorf("sqlLiteral(%s, %s, %#v) = %s, want %s", c.engine, c.typ, c.v, got, c.want)
		}
	}
}

// A backup of an SQLite database restores to the same schema and rows, and
// the .zip also holds an exact copy of the database file.
func TestBackupRoundTripSQLite(t *testing.T) {
	ctx := context.Background()
	src := filepath.Join(t.TempDir(), "shop.db")
	w, err := sql.Open("sqlite", sqliteURI(src))
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.Exec(`CREATE TABLE customer (id INTEGER PRIMARY KEY, name TEXT NOT NULL, photo BLOB, score REAL,
			upper_name TEXT GENERATED ALWAYS AS (upper(name)) VIRTUAL);
		CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INT REFERENCES customer(id), note TEXT);
		CREATE INDEX ix_cust ON orders(customer_id);
		CREATE VIEW v_notes AS SELECT note FROM orders;
		CREATE TRIGGER trg_block BEFORE INSERT ON orders BEGIN SELECT RAISE(ABORT, 'trigger fired'); END;
		INSERT INTO customer (id, name, photo, score) VALUES
			(1, 'O''Brien', X'00ff10', 1.25), (2, 'Zoë 😀', NULL, -3), (3, 'back\slash
new line', X'', NULL);`)
	if err == nil {
		// The trigger blocks inserts, so the rows go in before it exists.
		_, err = w.Exec(`DROP TRIGGER trg_block; INSERT INTO orders VALUES (1, 1, 'first'), (2, 3, NULL);
			CREATE TRIGGER trg_block BEFORE INSERT ON orders BEGIN SELECT RAISE(ABORT, 'trigger fired'); END;`)
	}
	w.Close()
	if err != nil {
		t.Fatal(err)
	}

	db, err := openDB(ctx, ConnParams{Engine: EngineSQLite, Database: src})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	groups, err := listObjects(ctx, EngineSQLite, db)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	res, err := backupToDir(ctx, dir, EngineSQLite, db, "shop", "shop", groups, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Tables != 2 || res.Rows != 5 || len(res.Failed) != 0 || filepath.Dir(res.Path) != filepath.Join(dir, "backup") || !strings.HasSuffix(res.Path, ".zip") {
		t.Fatalf("result: %+v", res)
	}

	zr, err := zip.OpenReader(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	files := map[string]string{}
	for _, f := range zr.File {
		if f.Method != zip.Deflate {
			t.Errorf("%s is not compressed", f.Name)
		}
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(b)
	}
	script, ok := files["shop.sql"]
	if !ok || files["shop.db"] == "" {
		t.Fatalf("zip holds %v", reflect.ValueOf(files).MapKeys())
	}
	// Rows come before the trigger (else restoring them would fail).
	if strings.Index(script, "INSERT INTO") > strings.Index(script, "CREATE TRIGGER") {
		t.Error("rows must be inserted before triggers are created")
	}

	restored := filepath.Join(t.TempDir(), "restored.db")
	r, _ := sql.Open("sqlite", sqliteURI(restored))
	defer r.Close()
	if _, err := r.Exec(script); err != nil {
		t.Fatalf("restoring: %v\n%s", err, script)
	}
	dump := func(d *sql.DB) []string {
		out, err := queryStrings(ctx, d, `SELECT id || '|' || name || '|' || COALESCE(hex(photo), 'null') || '|' || COALESCE(score, 'null') || '|' || upper_name FROM customer
			UNION ALL SELECT id || '|' || customer_id || '|' || COALESCE(note, 'null') FROM orders
			UNION ALL SELECT type || ' ' || name || ' ' || sql FROM sqlite_master ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	orig := dump(db)
	if got := dump(r); !reflect.DeepEqual(orig, got) {
		t.Errorf("restored database differs:\n%q\n%q", orig, got)
	}

	// The file copy in the zip is the same database.
	copyPath := filepath.Join(t.TempDir(), "copy.db")
	if err := os.WriteFile(copyPath, []byte(files["shop.db"]), 0o600); err != nil {
		t.Fatal(err)
	}
	c, _ := sql.Open("sqlite", sqliteURI(copyPath))
	defer c.Close()
	if got := dump(c); !reflect.DeepEqual(orig, got) {
		t.Errorf("file copy differs:\n%q\n%q", orig, got)
	}
}
