package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSQLNames(t *testing.T) {
	src := `-- orders_old is a comment
		SELECT o.status, c."Name" FROM shop.orders o JOIN [customer] c ON c.id = o.cust /* audit */
		WHERE :status = 1 AND @limit > 2 AND x = nextval('seq_id') AND y = ` + "`items`" + `.qty`
	var got []string
	for _, n := range sqlNames(src) {
		got = append(got, strings.Join(n, "."))
	}
	for _, want := range []string{"select", "shop.orders", "customer", "seq_id", "items.qty", "o.status", "c.name"} {
		if !slices.Contains(got, want) {
			t.Errorf("%q not found in %v", want, got)
		}
	}
	for _, not := range []string{"orders_old", "audit", "status", "limit", "name", "qty", "orders"} {
		if slices.Contains(got, not) {
			t.Errorf("%q found in %v", not, got)
		}
	}
}

// The relationships of an SQLite database: a foreign key with its columns,
// the table a trigger fires on, and what a view and a trigger use.
func TestRelationshipsSQLite(t *testing.T) {
	ctx := context.Background()
	src := filepath.Join(t.TempDir(), "shop.db")
	w, err := sql.Open("sqlite", sqliteURI(src))
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.Exec(`CREATE TABLE customer (id INTEGER PRIMARY KEY, name TEXT);
		CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INT REFERENCES customer(id), note TEXT);
		CREATE TABLE audit (at TEXT, what TEXT);
		CREATE TABLE lonely (x INT);
		CREATE INDEX ix_cust ON orders(customer_id);
		CREATE VIEW v_orders AS SELECT o.id, c.name FROM orders o JOIN customer c ON c.id = o.customer_id;
		CREATE TRIGGER trg_log AFTER INSERT ON orders BEGIN INSERT INTO audit VALUES (datetime(), 'order'); END;`)
	w.Close()
	if err != nil {
		t.Fatal(err)
	}
	db, err := openDB(ctx, ConnParams{Engine: EngineSQLite, Database: src})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	g, err := relationships(ctx, &session{engine: EngineSQLite, db: db})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, k := range g.order {
		e := g.edges[k]
		s := e.From + " " + e.Kind + " " + e.To
		if e.Label != "" {
			s += " (" + e.Label + ")"
		}
		if e.Guess {
			s += " ?"
		}
		got = append(got, s)
	}
	slices.Sort(got)
	want := []string{
		"table:orders fk table:customer (customer_id → id)",
		"trigger:trg_log trigger table:orders",
		"trigger:trg_log uses table:audit ?",
		"view:v_orders uses table:customer ?",
		"view:v_orders uses table:orders ?",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("edges:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, n := range g.nodes {
		if n.Type == ObjIndex {
			t.Errorf("index %s drawn", n.Name)
		}
	}
	if len(g.nodes) != 6 || len(g.notes) != 0 {
		t.Errorf("%d nodes, notes %v", len(g.nodes), g.notes)
	}
}

// Without foreign keys, relationships are assumed from columns of the same
// name: a key column in other tables, a column starting with a table's name,
// customer_id → customers.id; generic names (name, status) link nothing.
func TestRelationshipsAssumed(t *testing.T) {
	ctx := context.Background()
	src := filepath.Join(t.TempDir(), "legacy.db")
	w, err := sql.Open("sqlite", sqliteURI(src))
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.Exec(`CREATE TABLE customers (id INTEGER PRIMARY KEY, name TEXT, status TEXT);
		CREATE TABLE orders (id INTEGER PRIMARY KEY, CustomerID INT, name TEXT, status TEXT);
		CREATE TABLE products (product_code TEXT PRIMARY KEY, name TEXT);
		CREATE TABLE order_lines (order_id INT, product_code TEXT, qty INT);
		CREATE TABLE CLIENT (CLIENT_NO INT, NAME TEXT);
		CREATE TABLE INVOICE (INV_NO INT, CLIENT_NO INT, STATUS TEXT);
		CREATE TABLE REMINDER (REM_NO INT, CLIENT_NO INT);
		CREATE TABLE notes (note_id INTEGER PRIMARY KEY, name TEXT, status TEXT);
		CREATE TABLE INVOICE_LINE (INV_NO INT, LINE INT);
		CREATE TABLE PAYMENT (PAY_NO INT, INV_NO INT);
		CREATE TABLE CLIENT_DETAIL (CLIENT_NO INT PRIMARY KEY, NOTES TEXT);
		CREATE TABLE ACCT (ACCT_KEY INT PRIMARY KEY, TITLE TEXT);
		CREATE TABLE LEDGER (LINE INT, ACCOUNT_REF INT, AMOUNT REAL);
		CREATE VIEW V_LEDGER AS SELECT l.LINE, a.TITLE FROM LEDGER AS l JOIN ACCT a ON a.ACCT_KEY = l.ACCOUNT_REF;
		CREATE VIEW V_INV AS SELECT * FROM INVOICE, CLIENT WHERE CLIENT.CLIENT_NO = INVOICE.CLIENT_NO;`)
	w.Close()
	if err != nil {
		t.Fatal(err)
	}
	db, err := openDB(ctx, ConnParams{Engine: EngineSQLite, Database: src})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	g, err := relationships(ctx, &session{engine: EngineSQLite, db: db})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, k := range g.order {
		e := g.edges[k]
		if e.Kind != "assumed" {
			continue
		}
		got = append(got, e.From+" "+e.Kind+" "+e.To+" ("+e.Label+")"+strings.Join(e.Via, ","))
	}
	slices.Sort(got)
	want := []string{
		"table:CLIENT_DETAIL assumed table:CLIENT (CLIENT_NO = CLIENT_NO)",
		"table:INVOICE assumed table:CLIENT (CLIENT_NO = CLIENT_NO)V_INV",
		"table:INVOICE_LINE assumed table:INVOICE (INV_NO = INV_NO)",
		"table:LEDGER assumed table:ACCT (ACCOUNT_REF = ACCT_KEY)V_LEDGER",
		"table:PAYMENT assumed table:INVOICE (INV_NO = INV_NO)",
		"table:REMINDER assumed table:CLIENT (CLIENT_NO = CLIENT_NO)",
		"table:order_lines assumed table:orders (order_id → id)",
		"table:order_lines assumed table:products (product_code = product_code)",
		"table:orders assumed table:customers (CustomerID → id)",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("edges:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Joins are found as MariaDB writes views (qualified, in parentheses), with
// AS aliases, and in a WHERE.
func TestSQLJoins(t *testing.T) {
	g := newRelGraph([]ObjectGroup{{Type: ObjTable, Items: []string{"ledger", "acct", "visit", "person"}}})
	g.schemas["shop"] = true
	var got []string
	for _, src := range []string{
		"select `l`.`line` AS `line` from (`shop`.`ledger` `l` join `shop`.`acct` `a` on(`a`.`acct_key` = `l`.`account_ref`))",
		"SELECT * FROM visit AS v, person p WHERE p.pid = v.who AND v.x = 1 -- p.a = v.b",
	} {
		for _, j := range g.sqlJoins(src) {
			got = append(got, j.a+"."+j.ac+"="+j.b+"."+j.bc)
		}
	}
	want := "table:acct.acct_key=table:ledger.account_ref table:person.pid=table:visit.who"
	if strings.Join(got, " ") != want {
		t.Errorf("joins %v, want %s", got, want)
	}
}
