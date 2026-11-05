package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestAPITableStats(t *testing.T) {
	s := newTestServer(t)
	db, err := openDB(context.Background(), ConnParams{Engine: EngineSQLite, Database: newShopDB(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s.sessions["s1"] = &session{engine: EngineSQLite, label: "shop", db: db}

	rec := httptest.NewRecorder()
	s.handleTableStats(rec, httptest.NewRequest("GET", "/api/tablestats?session=s1", nil))
	var res struct {
		Tables []tableStat
		Rows   int64
		Exact  bool
		Bytes  int64
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &res) != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	want := map[string]int64{"customer": 3, "orders": 2} // tables only, not the view
	if len(res.Tables) != len(want) || res.Rows != 5 || !res.Exact || res.Bytes <= 0 {
		t.Fatalf("got %+v", res)
	}
	for _, tb := range res.Tables {
		if tb.Rows != want[tb.Name] || !tb.Exact || tb.Bytes <= 0 {
			t.Errorf("%s: %+v", tb.Name, tb)
		}
	}

	rec = httptest.NewRecorder()
	s.handleTableStats(rec, httptest.NewRequest("GET", "/api/tablestats?session=nope", nil))
	if rec.Code != 401 {
		t.Errorf("unknown session: %d", rec.Code)
	}
}
