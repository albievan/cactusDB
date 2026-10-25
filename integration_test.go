package main

import (
	"database/sql"
	"os"
	"testing"
)

// TestServerReadOnlyTransactions checks the database-level layer on its own:
// writes inside the transaction used by streamQuery must fail, or on SQL
// Server be rolled back, even when checkReadOnly is bypassed. Set CACTUSDB_IT=1 and run the docker test
// databases described in README.md to enable it.
func TestServerReadOnlyTransactions(t *testing.T) {
	if os.Getenv("CACTUSDB_IT") == "" {
		t.Skip("set CACTUSDB_IT=1 to run against live test databases")
	}
	cases := []struct {
		p     ConnParams
		table string
	}{
		{ConnParams{Engine: EngineMySQL, Host: "127.0.0.1", Port: 13306, Username: "root", Password: "p@ss:w/rd", Database: "shop"}, "customers"},
		{ConnParams{Engine: EngineMariaDB, Host: "127.0.0.1", Port: 13307, Username: "root", Password: "secret", Database: "shop"}, "customers"},
		{ConnParams{Engine: EngineFirebird, Host: "127.0.0.1", Port: 13050, Username: "SYSDBA", Password: "masterkey", Database: "/firebird/data/test.fdb"}, "CUSTOMER"},
		{ConnParams{Engine: EnginePostgres, Host: "127.0.0.1", Port: 15432, Username: "postgres", Password: "pg p@ss:/w", Database: "shop"}, "public.customers"},
		{ConnParams{Engine: EngineMSSQL, Host: "127.0.0.1", Port: 11433, Username: "sa", Password: "Cactus#Pass123", Database: "shop"}, "dbo.customers"},
	}
	for _, c := range cases {
		t.Run(c.p.Engine, func(t *testing.T) {
			db, err := openDB(t.Context(), c.p)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, q := range []string{
				"DELETE FROM " + c.table,
				"UPDATE " + c.table + " SET " + map[bool]string{true: "NAME", false: "name"}[c.p.Engine == EngineFirebird] + " = 'x'",
				"INSERT INTO " + c.table + " (" + map[bool]string{true: "ID", false: "id"}[c.p.Engine == EngineFirebird] + ") VALUES (999)",
				"CREATE TABLE cactus_probe (a INTEGER)",
				"DROP TABLE " + c.table,
			} {
				// The same transaction options streamQuery uses: READ ONLY, except on
				// SQL Server, which relies on the unconditional rollback.
				tx, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: c.p.Engine != EngineMSSQL})
				if err != nil {
					t.Fatal(err)
				}
				_, err = tx.ExecContext(t.Context(), q)
				tx.Rollback()
				if c.p.Engine == EngineMSSQL {
					continue // writes succeed inside the transaction; the row count check below proves the rollback
				}
				if err == nil {
					t.Errorf("%q succeeded inside a READ ONLY transaction", q)
				} else {
					t.Logf("blocked %-45q %v", q, err)
				}
			}
			// MySQL/MariaDB/PostgreSQL sessions are read-only themselves, so even a
			// statement outside any transaction must fail.
			if c.p.Engine == EngineMySQL || c.p.Engine == EngineMariaDB || c.p.Engine == EnginePostgres {
				for _, q := range []string{"DELETE FROM " + c.table, "CREATE TABLE cactus_probe2 (a INTEGER)"} {
					if _, err := db.ExecContext(t.Context(), q); err == nil {
						t.Errorf("%q succeeded outside a transaction on a read-only session", q)
					}
				}
			}
			var n int
			if err := db.QueryRow("SELECT COUNT(*) FROM " + c.table).Scan(&n); err != nil || n != 2 {
				t.Errorf("table changed or missing: n=%d err=%v", n, err)
			}
		})
	}
}
