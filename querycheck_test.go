package main

import (
	"context"
	"strings"
	"testing"
)

func TestCheckQuery(t *testing.T) {
	ctx := context.Background()
	db, err := openDB(ctx, ConnParams{Engine: EngineSQLite, Database: newShopDB(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, c := range []struct{ q, err string }{
		{"SELECT c.name, o.total FROM customer c JOIN orders o ON o.customer_id = c.id ORDER BY 2", ""},
		{"SELEC * FROM customer", "starts with SELEC"},
		{"SELECT * FORM customer", "syntax error"},
		{"SELECT * FROM customers", "no such table"},
		{"SELECT nam FROM customer", "no such column"},
		{"DELETE FROM customer", "only read-only"},
	} {
		how, err := checkQuery(ctx, EngineSQLite, db, c.q)
		switch {
		case c.err == "" && err != nil:
			t.Errorf("%q: %v", c.q, err)
		case c.err == "" && !strings.Contains(how, "not run"):
			t.Errorf("%q: checked by %q, want prepared", c.q, how)
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%q: %v, want %q", c.q, err, c.err)
		}
	}
	var n int
	db.QueryRow("SELECT count(*) FROM customer").Scan(&n)
	if n != 3 {
		t.Errorf("customer has %d rows after the checks", n)
	}
}
