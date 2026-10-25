package main

import "testing"

func TestCheckReadOnlyAllows(t *testing.T) {
	ok := []struct{ engine, q string }{
		{EngineSQLite, `SELECT * FROM t`},
		{EngineSQLite, `select a, b from t where note = 'please delete me';`},
		{EngineSQLite, `SELECT "update", [insert] FROM "drop table"`},
		{EngineSQLite, `WITH x AS (SELECT 1 AS n) SELECT n FROM x`},
		{EngineSQLite, `SELECT REPLACE(name, 'a', 'b') FROM t`},
		{EngineSQLite, "SELECT 1 -- delete everything\n"},
		{EngineSQLite, "SELECT 1 /* drop table t */"},
		{EngineMySQL, "SELECT `delete` FROM `t`"},
		{EngineMySQL, `SELECT 'it\'s ; insert' FROM t`},
		{EngineMySQL, `SHOW TABLES`},
		{EngineMariaDB, `DESCRIBE t`},
		{EngineMySQL, `SELECT 1--1`},
		{EngineFirebird, `SELECT FIRST 10 * FROM "CUSTOMER"`},
		{EngineFirebird, `select 'O''Brien; delete' from rdb$database`},
	}
	for _, c := range ok {
		if err := checkReadOnly(c.engine, c.q); err != nil {
			t.Errorf("%s %q: unexpected error %v", c.engine, c.q, err)
		}
	}
}

func TestCheckReadOnlyRejects(t *testing.T) {
	bad := []struct{ engine, q string }{
		{EngineSQLite, `INSERT INTO t VALUES (1)`},
		{EngineSQLite, `update t set a = 1`},
		{EngineSQLite, `DELETE FROM t`},
		{EngineSQLite, `DROP TABLE t`},
		{EngineSQLite, `CREATE TABLE x (a)`},
		{EngineSQLite, `ALTER TABLE t ADD c`},
		{EngineSQLite, `PRAGMA query_only = 0`},
		{EngineSQLite, `ATTACH 'x.db' AS x`},
		{EngineSQLite, `SELECT 1; DELETE FROM t`},
		{EngineSQLite, `WITH x AS (SELECT 1) DELETE FROM t`},
		{EngineSQLite, `WITH x AS (SELECT 1) INSERT INTO t SELECT * FROM x`},
		{EngineSQLite, `/* hi */ DELETE FROM t`},
		{EngineSQLite, `SELECT 'a\'; DELETE FROM t; --'`}, // no backslash escapes in SQLite
		{EngineMySQL, `REPLACE INTO t VALUES (1)`},
		{EngineMySQL, `SELECT * FROM t INTO OUTFILE '/tmp/x'`},
		{EngineMySQL, `SELECT * FROM t FOR UPDATE`},
		{EngineMySQL, `SELECT 1 /*! ; DELETE FROM t */`},
		{EngineMySQL, `TRUNCATE t`},
		{EngineMySQL, `CALL p()`},
		{EngineMySQL, `SET autocommit = 1`},
		{EngineMariaDB, `LOCK TABLES t WRITE`},
		{EngineFirebird, `EXECUTE BLOCK AS BEGIN DELETE FROM t; END`},
		{EngineFirebird, `MERGE INTO t USING s ON 1=1 WHEN MATCHED THEN DELETE`},
		{EngineFirebird, `UPDATE OR INSERT INTO t VALUES (1)`},
		{EngineSQLite, ``},
	}
	for _, c := range bad {
		if err := checkReadOnly(c.engine, c.q); err == nil {
			t.Errorf("%s %q: expected rejection", c.engine, c.q)
		}
	}
}

func TestSQLiteURI(t *testing.T) {
	cases := map[string]string{
		"/home/me/data.db":      "file:/home/me/data.db",
		"rel/a b.db":            "file:rel/a%20b.db",
		`C:\Users\me\x?y#z%.db`: "file:///C:/Users/me/x%3fy%23z%25.db",
	}
	for in, want := range cases {
		if got := sqliteURI(in); got != want {
			t.Errorf("sqliteURI(%q) = %q, want %q", in, got, want)
		}
	}
}

// The SQLite connection must refuse writes even without checkReadOnly.
func TestSQLiteConnectionIsReadOnly(t *testing.T) {
	path := t.TempDir() + "/ro test.db"
	st, err := openStore(path, t.TempDir()+"/cactus.key") // creates the file with a table in it
	if err != nil {
		t.Fatal(err)
	}
	st.db.Close()

	db, err := openDB(t.Context(), ConnParams{Engine: EngineSQLite, Database: path})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`INSERT INTO connections (name, engine, created_at) VALUES ('x', 'sqlite', '')`,
		`DELETE FROM connections`,
		`CREATE TABLE z (a)`,
	} {
		if _, err := db.Exec(q); err == nil {
			t.Errorf("%q succeeded on a read-only connection", q)
		}
	}
	if _, err := db.Query(`SELECT * FROM connections`); err != nil {
		t.Errorf("select failed: %v", err)
	}
}
