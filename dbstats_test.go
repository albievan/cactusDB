package main

import (
	"context"
	"database/sql"
	"testing"
)

// Every section of the SQLite dashboard reads, and the database's numbers
// come out right.
func TestSQLiteDashboard(t *testing.T) {
	db, err := sql.Open("sqlite", newShopDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range dashboards["sqlite"] {
		sec := runStatQuery(context.Background(), db, q)
		if sec.Error != "" || len(sec.Rows) == 0 {
			t.Fatalf("%s: error %q, %d rows", q.title, sec.Error, len(sec.Rows))
		}
		if q.title == "Database" {
			got := map[string]any{}
			for i, c := range sec.Columns {
				got[c] = sec.Rows[0][i]
			}
			if got["Tables"] != int64(2) || got["Size (bytes)"].(int64) <= 0 {
				t.Errorf("database cards %v", got)
			}
		}
	}
	if !hasDashboard("sqlite") || !hasDashboard(EngineMariaDB) || hasDashboard("redis") {
		t.Error("hasDashboard")
	}
}
