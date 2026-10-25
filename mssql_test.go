package main

import (
	"net/url"
	"testing"
)

func TestMSSQLDSN(t *testing.T) {
	cases := []struct {
		p              ConnParams
		host, instance string
	}{
		{ConnParams{Host: "db1", Username: "sa", Password: "p@ss:/w?rd#", Database: "shop"}, "db1:1433", ""},
		{ConnParams{Host: "db1", Port: 2000}, "db1:2000", ""},
		{ConnParams{Host: `db1\SQLEXPRESS`}, "db1", "SQLEXPRESS"},
		{ConnParams{Host: `db1\SQLEXPRESS`, Port: 1500}, "db1:1500", "SQLEXPRESS"},
		{ConnParams{}, "localhost:1433", ""},
	}
	for _, c := range cases {
		u, err := url.Parse(mssqlDSN(c.p))
		if err != nil {
			t.Fatalf("%+v: %v", c.p, err)
		}
		if u.Host != c.host || (u.Path != "" && u.Path[1:] != c.instance) || (u.Path == "" && c.instance != "") {
			t.Errorf("%+v: host=%q path=%q, want %q / %q", c.p, u.Host, u.Path, c.host, c.instance)
		}
		if pw, _ := u.User.Password(); pw != c.p.Password {
			t.Errorf("password round-trip: got %q want %q", pw, c.p.Password)
		}
		if got := u.Query().Get("database"); got != c.p.Database {
			t.Errorf("database: got %q want %q", got, c.p.Database)
		}
	}
}

func TestFormatGUID(t *testing.T) {
	// 6F9619FF-8B86-D011-B42D-00C04FC964FF as stored by SQL Server.
	b := []byte{0xFF, 0x19, 0x96, 0x6F, 0x86, 0x8B, 0x11, 0xD0, 0xB4, 0x2D, 0x00, 0xC0, 0x4F, 0xC9, 0x64, 0xFF}
	if got, want := formatGUID(b), "6F9619FF-8B86-D011-B42D-00C04FC964FF"; got != want {
		t.Errorf("got %s want %s", got, want)
	}
}

func TestFirebirdTriggerType(t *testing.T) {
	cases := map[int64]string{
		1: "BEFORE INSERT", 2: "AFTER INSERT", 3: "BEFORE UPDATE", 4: "AFTER UPDATE",
		5: "BEFORE DELETE", 6: "AFTER DELETE", 17: "BEFORE INSERT OR UPDATE",
		26: "AFTER INSERT OR DELETE", 27: "BEFORE UPDATE OR DELETE",
		113: "BEFORE INSERT OR UPDATE OR DELETE", 114: "AFTER INSERT OR UPDATE OR DELETE",
		8192: "ON CONNECT", 8196: "ON TRANSACTION ROLLBACK",
	}
	for in, want := range cases {
		if got := firebirdTriggerType(in); got != want {
			t.Errorf("type %d: got %q want %q", in, got, want)
		}
	}
}

func TestMSSQLReadOnlyGuard(t *testing.T) {
	ok := []string{
		`SELECT TOP 10 * FROM [dbo].[customers]`,
		`SELECT [delete], [update] FROM [drop table]`,
		`SELECT N'it''s; DROP TABLE x' AS s`,
		`WITH x AS (SELECT 1 AS n) SELECT n FROM x`,
	}
	bad := []string{
		`SELECT 1 DELETE FROM dbo.customers`, // T-SQL needs no ';' between statements
		`EXEC sp_who`,
		`SELECT * INTO dbo.copy FROM dbo.customers`,
		`BACKUP DATABASE shop TO DISK = 'x.bak'`,
		`SELECT 1 BACKUP DATABASE shop TO DISK = 'x.bak'`,
		`SELECT * FROM OPENQUERY(srv, 'DELETE FROM t')`,
		`DBCC CHECKDB`,
		`SELECT 1; TRUNCATE TABLE x`,
		`MERGE INTO t USING s ON 1 = 1 WHEN MATCHED THEN DELETE;`,
		`SELECT [a]] DELETE FROM t]`, // stays rejected even if the escape confuses the scanner
	}
	for _, q := range ok {
		if err := checkReadOnly(EngineMSSQL, q); err != nil {
			t.Errorf("%q: unexpected error %v", q, err)
		}
	}
	for _, q := range bad {
		if err := checkReadOnly(EngineMSSQL, q); err == nil {
			t.Errorf("%q: expected rejection", q)
		}
	}
}

func TestPostgresReadOnlyGuard(t *testing.T) {
	ok := []string{
		`SELECT * FROM "public"."customers" LIMIT 10`,
		`SELECT $$it's ; DELETE$$ AS s, $tag$ DROP TABLE x $tag$ AS t`,
		`SELECT E'a\'b' AS s`,
		`WITH x AS (SELECT 1 AS n) SELECT n FROM x`,
		`SELECT last_value FROM public.seq_orders`,
		`SELECT $1::int`,
	}
	bad := []string{
		`SELECT $$'$$; DELETE FROM t; SELECT $$'$$`, // a ' inside $$ must not hide the DELETE
		`WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d`,
		`SELECT nextval('seq_orders')`,
		`SELECT setval('seq_orders', 1)`,
		`SELECT set_config('default_transaction_read_only', 'off', false)`,
		`COPY t TO '/tmp/x'`,
		`SELECT * FROM t FOR UPDATE`,
		`SELECT dblink_exec('dbname=x', 'DELETE FROM t')`,
		`SELECT pg_terminate_backend(123)`,
		`SELECT lo_export(1, '/tmp/x')`,
	}
	for _, q := range ok {
		if err := checkReadOnly(EnginePostgres, q); err != nil {
			t.Errorf("%q: unexpected error %v", q, err)
		}
	}
	for _, q := range bad {
		if err := checkReadOnly(EnginePostgres, q); err == nil {
			t.Errorf("%q: expected rejection", q)
		}
	}
}

func TestSequenceAdvancersRejected(t *testing.T) {
	for engine, q := range map[string]string{
		EngineFirebird: `SELECT GEN_ID(GEN_X, 1) FROM RDB$DATABASE`,
		EngineMSSQL:    `SELECT NEXT VALUE FOR sales.seq_orders`,
		EngineMariaDB:  `SELECT NEXTVAL(seq_orders)`,
	} {
		if err := checkReadOnly(engine, q); err == nil {
			t.Errorf("%s %q: expected rejection", engine, q)
		}
	}
	if err := checkReadOnly(EngineFirebird, `SELECT "NEXT" FROM t WHERE x = 'NEXT VALUE'`); err != nil {
		t.Errorf("quoted NEXT/VALUE should be allowed: %v", err)
	}
}
