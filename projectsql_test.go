package main

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func testProjectData() *projectData {
	return &projectData{
		Links: []projectLink{{1, "fb"}, {2, "maria"}, {3, "ms"}},
		Tables: []projectTable{
			{Alias: "fb", Source: "CUSTOMER", Name: "customer"},
			{Alias: "maria", Source: "orders", Name: "orders"},
			{Alias: "ms", Source: "dbo.invoices", Name: "invoices"},
		},
	}
}

var testKinds = map[string]map[string]string{
	"customer": {"id": kInt32, "name": kVarchar, "city": kVarchar},
	"orders":   {"id": kInt64, "customer_id": kInt32, "order_date": kDate, "total": kDecimal, "note": kText},
	"invoices": {"id": kInt32, "order_id": kInt32, "amount": kDecimal},
}

func pushFor(t *testing.T, q string) map[string][]pushCond {
	t.Helper()
	d := testProjectData()
	pq, err := analyzeProjectQuery(q, d)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	got := pq.pushdown(func(pt *projectTable, col string) (string, bool) {
		k, ok := testKinds[pt.Name][strings.ToLower(col)]
		return k, ok
	}, func(*projectTable) bool { return false })
	out := map[string][]pushCond{}
	for pt, cs := range got {
		out[pt.Alias+"."+pt.Name] = cs
	}
	return out
}

const q1 = `SELECT c.name, o.order_date, i.amount
FROM   fb.customer c
JOIN   maria.orders o   ON o.customer_id = c.id
LEFT JOIN ms.invoices i ON i.order_id = o.id
WHERE  o.order_date >= '2026-01-01' AND i.amount > 10 AND c.name = 'Ann'
ORDER BY c.name`

func TestAnalyzeProjectQuery(t *testing.T) {
	d := testProjectData()
	pq, err := analyzeProjectQuery(q1, d)
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, r := range pq.refs {
		refs = append(refs, r.table.Alias+"."+r.table.Name+" "+r.alias)
	}
	if !slices.Equal(refs, []string{"fb.customer c", "maria.orders o", "ms.invoices i"}) {
		t.Errorf("refs: %v", refs)
	}
	need := func(name string, all ...string) []string {
		for i := range d.Tables {
			if d.Tables[i].Name == name {
				return pq.needed(&d.Tables[i], all)
			}
		}
		return nil
	}
	if got := need("customer", "ID", "NAME", "CITY", "PHONE"); !slices.Equal(got, []string{"ID", "NAME"}) {
		t.Errorf("customer columns: %v", got)
	}
	if got := need("orders", "id", "customer_id", "order_date", "total"); !slices.Equal(got, []string{"id", "customer_id", "order_date"}) {
		t.Errorf("orders columns: %v", got)
	}
	// SELECT *, alias.*, three-part names, COUNT(*) without columns.
	pq, _ = analyzeProjectQuery(`SELECT o.*, fb.customer.city FROM maria.orders o JOIN fb.customer ON customer.id = o.customer_id`, d)
	if got := pq.needed(&d.Tables[1], []string{"id", "total"}); len(got) != 2 {
		t.Errorf("o.*: %v", got)
	}
	if got := pq.needed(&d.Tables[0], []string{"ID", "CITY", "NAME"}); !slices.Equal(got, []string{"ID", "CITY"}) {
		t.Errorf("three-part and default alias: %v", got)
	}
	pq, _ = analyzeProjectQuery(`SELECT COUNT(*) FROM maria.orders`, d)
	if got := pq.needed(&d.Tables[1], []string{"id", "total"}); !slices.Equal(got, []string{"id"}) {
		t.Errorf("count(*): %v", got)
	}

	// Mistakes are named.
	var nip *errNotInProject
	if _, err := analyzeProjectQuery(`SELECT * FROM fb.orders`, d); !errors.As(err, &nip) || nip.Alias != "fb" || nip.Table != "orders" {
		t.Errorf("table not in the project: %v", err)
	}
	for _, q := range []string{`SELECT * FROM orders`, `SELECT 1`, `SELECT * FROM maria.orders fb`} {
		if _, err := analyzeProjectQuery(q, d); err == nil {
			t.Errorf("%s accepted", q)
		}
	}
	if _, err := analyzeProjectQuery(`WITH big AS (SELECT * FROM maria.orders WHERE total > 5) SELECT * FROM big`, d); err != nil {
		t.Errorf("a CTE name: %v", err)
	}
}

func TestProjectPushdown(t *testing.T) {
	got := pushFor(t, q1)
	o := got["maria.orders"]
	if len(o) != 1 || o[0].Col != "order_date" || o[0].Op != ">=" {
		t.Fatalf("orders: %+v", o)
	}
	if want := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC); !o[0].Args[0].(time.Time).Equal(want) {
		t.Errorf("date widened by a day: %v", o[0].Args[0])
	}
	if len(got["ms.invoices"]) != 0 {
		t.Errorf("the nullable side of a LEFT JOIN got %+v", got["ms.invoices"])
	}
	if c := got["fb.customer"]; len(c) != 1 || c[0].Op != "=" || c[0].Args[0] != "Ann" {
		t.Errorf("text equality: %+v", c)
	}

	for name, q := range map[string]string{
		"OR at the top":       `SELECT * FROM maria.orders o WHERE o.total > 5 OR o.id = 1`,
		"text range":          `SELECT * FROM fb.customer c WHERE c.name > 'B'`,
		"LIKE":                `SELECT * FROM fb.customer c WHERE c.name LIKE 'A%'`,
		"function":            `SELECT * FROM maria.orders o WHERE abs(o.total) > 5`,
		"used twice":          `SELECT * FROM maria.orders a JOIN maria.orders b ON a.id = b.id WHERE a.id = 1`,
		"compound":            `SELECT id FROM maria.orders o WHERE o.id = 1 UNION SELECT id FROM fb.customer`,
		"RIGHT JOIN left":     `SELECT * FROM maria.orders o RIGHT JOIN fb.customer c ON c.id = o.customer_id WHERE o.id = 1`,
		"FULL JOIN":           `SELECT * FROM maria.orders o FULL JOIN fb.customer c ON c.id = o.customer_id WHERE c.id = 1`,
		"date not ISO":        `SELECT * FROM maria.orders o WHERE o.order_date > '01/02/2026'`,
		"number vs text":      `SELECT * FROM fb.customer c WHERE c.id = 'x'`,
		"column against col":  `SELECT * FROM maria.orders o WHERE o.id = o.customer_id`,
		"subquery's WHERE":    `SELECT * FROM (SELECT * FROM maria.orders o WHERE o.id = 1)`,
		"unknown column type": `SELECT * FROM maria.orders o WHERE o.nope = 1`,
	} {
		if g := pushFor(t, q); len(g) != 0 {
			t.Errorf("%s: pushed %+v", name, g)
		}
	}

	// Numbers: integers exact, others widened; literal first is flipped; BETWEEN; IS NULL.
	g := pushFor(t, `SELECT * FROM maria.orders o WHERE 5 < o.id AND o.total >= 10.5 AND o.customer_id BETWEEN 1 AND 3 AND o.note IS NULL AND o.total <= -2`)["maria.orders"]
	if len(g) != 5 {
		t.Fatalf("got %+v", g)
	}
	if g[0].Op != ">" || g[0].Args[0] != int64(5) {
		t.Errorf("flipped: %+v", g[0])
	}
	if g[1].Op != ">=" || g[1].Args[0].(float64) >= 10.5 {
		t.Errorf("widened down: %+v", g[1])
	}
	if g[2].Op != "BETWEEN" || g[2].Args[0] != int64(1) || g[2].Args[1] != int64(3) {
		t.Errorf("between: %+v", g[2])
	}
	if g[3].Op != "IS NULL" || g[4].Op != "<=" || g[4].Args[0] != int64(-2) {
		t.Errorf("is null / negative: %+v %+v", g[3], g[4])
	}
	// RIGHT JOIN: the table after it is not nullable; LEFT side of WITH queries.
	if g := pushFor(t, `SELECT * FROM maria.orders o RIGHT JOIN fb.customer c ON c.id = o.customer_id WHERE c.id = 1`); len(g["fb.customer"]) != 1 {
		t.Errorf("RIGHT JOIN's right table: %+v", g)
	}
	if g := pushFor(t, `WITH x AS (SELECT 1 AS a) SELECT * FROM maria.orders o, x WHERE o.id = 2`); len(g["maria.orders"]) != 1 {
		t.Errorf("after WITH: %+v", g)
	}
}

func TestCheckProjectQuery(t *testing.T) {
	for _, q := range []string{"SELECT 1", "select * from fb.customer;", "WITH a AS (SELECT 1) SELECT * FROM a", "SELECT 'pragma; attach'"} {
		if err := checkProjectQuery(q); err != nil {
			t.Errorf("%q refused: %v", q, err)
		}
	}
	for _, q := range []string{
		"INSERT INTO fb.customer VALUES (1)", "UPDATE maria.orders SET total = 0", "DELETE FROM maria.orders",
		"ATTACH DATABASE 'x.db' AS x", "DETACH fb", "PRAGMA query_only = 0",
		"SELECT 1; SELECT 2", "VACUUM", "SELECT load_extension('x')", "SELECT readfile('/etc/passwd')", "SELECT writefile('x', 'y')",
		"VALUES (1)", "EXPLAIN SELECT 1", "CREATE TABLE t (a)", "DROP TABLE fb.customer", "REPLACE INTO t VALUES (1)", "",
	} {
		if checkProjectQuery(q) == nil {
			t.Errorf("%q accepted", q)
		}
	}
}

// LEFT OUTER JOIN is LEFT JOIN: the table after it is on the nullable side and
// gets nothing pushed down; the one before it still does.
func TestProjectPushdownLeftOuter(t *testing.T) {
	got := pushFor(t, `SELECT o.id FROM maria.orders o LEFT OUTER JOIN fb.customer c ON c.id = o.customer_id WHERE o.id = 7 AND c.id = 3`)
	if len(got["fb.customer"]) != 0 {
		t.Errorf("the nullable side of a LEFT OUTER JOIN got %+v", got["fb.customer"])
	}
	if len(got["maria.orders"]) != 1 {
		t.Errorf("orders: %+v", got["maria.orders"])
	}
}
