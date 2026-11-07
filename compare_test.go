package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// newSQLiteDB creates a SQLite file from statements.
func newSQLiteDB(t *testing.T, name string, stmts ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return path
}

func snapOf(t *testing.T, label, path string) *schemaSnap {
	t.Helper()
	db, err := openDB(context.Background(), ConnParams{Engine: EngineSQLite, Database: path})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := readSchema(context.Background(), &compareDB{label: label, engine: EngineSQLite, db: db}, &compareJob{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func statusOf(res *compareResult, typ, name string) string {
	for _, o := range res.Objects {
		if o.Type == typ && strings.EqualFold(o.Name, name) {
			return o.Cells[0].Status
		}
	}
	return ""
}

func TestCompareSQLite(t *testing.T) {
	master := newSQLiteDB(t, "master.db",
		`CREATE TABLE customers (id INTEGER PRIMARY KEY, name TEXT NOT NULL, email TEXT)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER REFERENCES customers(id), total REAL)`,
		`CREATE INDEX orders_customer ON orders (customer_id)`,
		`CREATE VIEW big_orders AS SELECT * FROM orders WHERE total > 100`,
		`CREATE TRIGGER orders_ins AFTER INSERT ON orders BEGIN SELECT 1; END`,
		`INSERT INTO customers VALUES (1, 'a', 'b')`)
	other := newSQLiteDB(t, "other.db",
		`CREATE TABLE customers (id INTEGER PRIMARY KEY, name TEXT NOT NULL, phone TEXT)`,
		`CREATE VIEW big_orders AS SELECT 1 AS x`,
		`CREATE TABLE old_stuff (a TEXT)`,
		`INSERT INTO customers VALUES (7, 'x', 'y'), (8, 'z', 'w')`)
	same := newSQLiteDB(t, "same.db",
		`CREATE TABLE customers (id INTEGER PRIMARY KEY, name TEXT NOT NULL, email TEXT)`,
		`CREATE TABLE   orders (id INTEGER PRIMARY KEY, customer_id INTEGER REFERENCES customers(id), total REAL)`,
		`CREATE INDEX orders_customer ON orders (customer_id)`,
		`CREATE VIEW big_orders AS SELECT * FROM orders WHERE total > 100`,
		`CREATE TRIGGER orders_ins AFTER INSERT ON orders BEGIN SELECT 1; END`)

	res := compareSnaps([]*schemaSnap{snapOf(t, "master", master), snapOf(t, "other", other)}, true)
	for _, c := range []struct{ typ, name, want string }{
		{ObjTable, "customers", "diff"},
		{ObjTable, "orders", "missing"},
		{ObjTable, "old_stuff", "extra"},
		{ObjView, "big_orders", "diff"},
		{ObjIndex, "orders_customer", "missing"},
		{ObjTrigger, "orders_ins", "missing"},
	} {
		if got := statusOf(res, c.typ, c.name); got != c.want {
			t.Errorf("%s %s: %q, want %q", c.typ, c.name, got, c.want)
		}
	}
	var notes []string
	for _, o := range res.Objects {
		if o.Name == "customers" {
			notes = o.Cells[0].Notes
		}
	}
	if j := strings.Join(notes, "\n"); !strings.Contains(j, "column email missing") || !strings.Contains(j, "column phone not in the master") {
		t.Errorf("notes: %q", notes)
	}
	script := res.Targets[0].Script
	for _, want := range []string{`ALTER TABLE "customers" ADD COLUMN email TEXT;`, `DROP TABLE "old_stuff";`,
		`DROP VIEW "big_orders";`, `ALTER TABLE "customers" DROP COLUMN phone;`, "CREATE TABLE orders", "CREATE INDEX orders_customer"} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q:\n%s", want, script)
		}
	}

	// Run the script on the other database (only this test writes to it:
	// cactusdb itself never runs a script): then the two must be the same.
	db, err := sql.Open("sqlite", other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(script); err != nil {
		t.Fatalf("script failed: %v\n%s", err, script)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM customers`).Scan(&n)
	db.Close()
	if n != 2 {
		t.Errorf("rows kept: %d, want 2", n)
	}
	res = compareSnaps([]*schemaSnap{snapOf(t, "master", master), snapOf(t, "other", other)}, true)
	for _, o := range res.Objects {
		// SQLite keeps ALTER TABLE ADD COLUMN in its own CREATE TABLE text:
		// the text differs, every column is the same.
		if st := o.Cells[0].Status; st != "same" {
			t.Errorf("after the script: %s %s is %s %v", o.Type, o.Name, st, o.Cells[0].Notes)
		}
	}

	// Spacing alone is no difference; without drops nothing is dropped.
	res = compareSnaps([]*schemaSnap{snapOf(t, "master", master), snapOf(t, "same", same), snapOf(t, "other", newSQLiteDB(t, "o2.db", `CREATE TABLE x (a)`))}, false)
	if c := res.Targets[0].Counts; c["same"] != len(res.Objects)-1 || c["absent"] != 1 {
		t.Errorf("same database: %v", c)
	}
	if !strings.Contains(res.Targets[0].Script, "nothing to change") {
		t.Errorf("same database script:\n%s", res.Targets[0].Script)
	}
	if s := res.Targets[1].Script; strings.Contains(s, "DROP TABLE") {
		t.Errorf("dropped without drops:\n%s", s)
	}
}

func TestParseTable(t *testing.T) {
	def := "CREATE TABLE `t` (\n  `id` int(11) NOT NULL AUTO_INCREMENT,\n  `name` varchar(50) DEFAULT 'a, b',\n  `d` decimal(10,2) DEFAULT NULL,\n" +
		"  PRIMARY KEY (`id`),\n  KEY `ix` (`name`),\n  CONSTRAINT `fk` FOREIGN KEY (`d`) REFERENCES `x` (`d`)\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4"
	p, ok := parseTable(def)
	if !ok || len(p.cols) != 3 || len(p.cons) != 3 || p.tail != "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4" || p.cols[1].name != "name" {
		t.Fatalf("%v %+v", ok, p)
	}
	pg := "CREATE TABLE \"public\".\"t\" (\n  \"id\" integer NOT NULL,\n  CONSTRAINT \"t_pkey\" PRIMARY KEY (id)\n);\nCREATE INDEX ix ON public.t USING btree (id);\n"
	p, ok = parseTable(pg)
	if !ok || len(p.cols) != 1 || len(p.cons) != 1 || len(p.extra) != 1 || p.tail != "" {
		t.Fatalf("%v %+v", ok, p)
	}
	if got := dropExtra(EnginePostgres, "public.t", p.extra[0]); got != `DROP INDEX "public".ix` {
		t.Errorf("drop index: %q", got)
	}
	if got := dropInline(EngineMySQL, "`t`", "CONSTRAINT `fk` FOREIGN KEY (`d`) REFERENCES `x` (`d`)"); got != "ALTER TABLE `t` DROP FOREIGN KEY `fk`" {
		t.Errorf("drop fk: %q", got)
	}
	if got := dropInline(EngineMySQL, "`t`", "UNIQUE KEY `u` (`a`)"); got != "ALTER TABLE `t` DROP INDEX `u`" {
		t.Errorf("drop key: %q", got)
	}
	if typ, def, nn, sp := columnParts(`"amount" numeric(10,2) DEFAULT 0 NOT NULL`); typ != "numeric(10,2)" || def != "0" || !nn || sp {
		t.Errorf("columnParts: %q %q %v %v", typ, def, nn, sp)
	}
	if d, ok := replaceInPlace(EngineMSSQL, ObjProcedure, "-- note\nCREATE PROCEDURE dbo.p AS SELECT 1"); !ok || d != "-- note\nCREATE OR ALTER PROCEDURE dbo.p AS SELECT 1" {
		t.Errorf("replaceInPlace: %q", d)
	}
	if got := cleanDef(EngineMySQL, "shop", "CREATE ALGORITHM=UNDEFINED DEFINER=`root`@`%` SQL SECURITY DEFINER VIEW `v` AS select `shop`.`t`.`a` AS `a` from `shop`.`t`"); got != "CREATE ALGORITHM=UNDEFINED SQL SECURITY DEFINER VIEW `v` AS select `t`.`a` AS `a` from `t`" {
		t.Errorf("cleanDef: %q", got)
	}
}

// snapFrom builds a schema from definitions, as readSchema would.
func snapFrom(label, engine string, objs ...*schemaObj) *schemaSnap {
	s := &schemaSnap{Label: label, Engine: engine, objs: map[string]*schemaObj{}}
	for _, o := range objs {
		o.Def = cleanDef(engine, "", o.Def)
		o.key = defKey(o.Def)
		k := objKey(o.Type, o.Name)
		s.objs[k] = o
		s.order = append(s.order, k)
	}
	return s
}

func TestCompareScriptsOtherEngines(t *testing.T) {
	cases := []struct {
		engine         string
		master, target string
		want           []string
	}{
		{EnginePostgres,
			"CREATE TABLE \"public\".\"t\" (\n  \"id\" integer NOT NULL,\n  \"name\" character varying(100) DEFAULT 'x'::character varying NOT NULL,\n  \"n\" numeric(10,2),\n  CONSTRAINT \"t_pkey\" PRIMARY KEY (id)\n);\nCREATE INDEX t_name ON public.t USING btree (name);\n",
			"CREATE TABLE \"public\".\"t\" (\n  \"id\" integer NOT NULL,\n  \"name\" character varying(50),\n  \"old\" text,\n  CONSTRAINT \"t_pkey\" PRIMARY KEY (id),\n  CONSTRAINT \"t_old_key\" UNIQUE (old)\n);\nCREATE INDEX t_old ON public.t USING btree (old);\n",
			[]string{`DROP INDEX "public".t_old;`, `ALTER TABLE "public"."t" DROP CONSTRAINT "t_old_key";`, `ALTER TABLE "public"."t" DROP COLUMN "old";`,
				`ALTER TABLE "public"."t" ADD COLUMN "n" numeric(10,2);`, `ALTER TABLE "public"."t" ALTER COLUMN "name" TYPE character varying(100);`,
				`ALTER TABLE "public"."t" ALTER COLUMN "name" SET DEFAULT 'x'::character varying;`, `ALTER TABLE "public"."t" ALTER COLUMN "name" SET NOT NULL;`,
				`CREATE INDEX t_name ON public.t USING btree (name);`}},
		{EngineMSSQL,
			"CREATE TABLE [dbo].[t] (\n  [id] int NOT NULL,\n  [name] nvarchar(100) NOT NULL,\n  [n] decimal(10,2) NULL,\n  PRIMARY KEY ([id])\n);\nCREATE INDEX [ix_name] ON [dbo].[t] ([name]);\n",
			"CREATE TABLE [dbo].[t] (\n  [id] int NOT NULL,\n  [name] nvarchar(50) NULL,\n  [old] int NULL,\n  PRIMARY KEY ([id])\n);\n",
			[]string{"ALTER TABLE [dbo].[t] DROP COLUMN [old]\nGO", "ALTER TABLE [dbo].[t] ADD [n] decimal(10,2) NULL\nGO",
				"ALTER TABLE [dbo].[t] ALTER COLUMN [name] nvarchar(100) NOT NULL\nGO", "CREATE INDEX [ix_name] ON [dbo].[t] ([name])\nGO"}},
		{EngineFirebird,
			"CREATE TABLE \"T\" (\n  \"ID\" INTEGER NOT NULL,\n  \"NAME\" VARCHAR(100) DEFAULT 'x' NOT NULL,\n  PRIMARY KEY (\"ID\")\n);\n",
			"CREATE TABLE \"T\" (\n  \"ID\" INTEGER NOT NULL,\n  \"NAME\" VARCHAR(50),\n  \"OLD\" INTEGER,\n  PRIMARY KEY (\"ID\")\n);\n",
			[]string{`ALTER TABLE "T" DROP "OLD";`, `ALTER TABLE "T" ALTER COLUMN "NAME" TYPE VARCHAR(100);`,
				`ALTER TABLE "T" ALTER COLUMN "NAME" SET DEFAULT 'x';`, `ALTER TABLE "T" ALTER "NAME" SET NOT NULL;`}},
	}
	for _, c := range cases {
		name := "public.t"
		switch c.engine {
		case EngineMSSQL:
			name = "dbo.t"
		case EngineFirebird:
			name = "T"
		}
		m := snapFrom("m", c.engine, &schemaObj{Type: ObjTable, Name: name, Def: c.master},
			&schemaObj{Type: ObjView, Name: name + "v", Def: "CREATE VIEW v AS SELECT 1"},
			&schemaObj{Type: ObjSequence, Name: name + "s", Def: "CREATE SEQUENCE s INCREMENT BY 2"})
		o := snapFrom("o", c.engine, &schemaObj{Type: ObjTable, Name: name, Def: c.target},
			&schemaObj{Type: ObjView, Name: name + "v", Def: "CREATE VIEW v AS SELECT 2"},
			&schemaObj{Type: ObjSequence, Name: name + "s", Def: "CREATE SEQUENCE s"})
		res := compareSnaps([]*schemaSnap{m, o}, true)
		script := res.Targets[0].Script
		for _, w := range c.want {
			if !strings.Contains(script, w) {
				t.Errorf("%s: script lacks %q:\n%s", c.engine, w, script)
			}
		}
		if strings.Contains(script, "DROP SEQUENCE") || !strings.Contains(script, "change it by hand") {
			t.Errorf("%s: a changed sequence must be left to the user:\n%s", c.engine, script)
		}
		// A changed view is replaced in place where the engine can.
		inPlace := c.engine != EnginePostgres // "CREATE VIEW" from the test isn't OR REPLACE
		if strings.Contains(script, "DROP VIEW") == inPlace {
			t.Errorf("%s: view replaced in place = %v:\n%s", c.engine, inPlace, script)
		}
	}
}
