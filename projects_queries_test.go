package main

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"testing"
)

func TestProjectQueries(t *testing.T) {
	st := newTestServer(t).store
	pid, key, err := st.CreateProject("P", projectData{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	layout := json.RawMessage(`{"fb.customer":{"x":10,"y":20}}`)
	id, v, err := st.SaveProjectQuery(pid, key, ProjectQuery{projectQueryData: projectQueryData{Name: "Open orders", SQL: "SELECT * FROM maria.orders", Layout: layout}})
	if err != nil || v != 1 {
		t.Fatalf("save: %d %v", v, err)
	}
	// A layout-only change makes no version; a changed SQL does.
	if _, v, _ := st.SaveProjectQuery(pid, key, ProjectQuery{ID: id, projectQueryData: projectQueryData{Name: "Open orders", SQL: "SELECT * FROM maria.orders", Layout: json.RawMessage(`{}`)}}); v != 1 {
		t.Errorf("layout-only save made version %d", v)
	}
	if _, v, _ := st.SaveProjectQuery(pid, key, ProjectQuery{ID: id, projectQueryData: projectQueryData{Name: "Open orders", SQL: "SELECT id FROM maria.orders"}}); v != 2 {
		t.Errorf("changed SQL: version %d", v)
	}
	if _, _, err := st.SaveProjectQuery(pid, key, ProjectQuery{projectQueryData: projectQueryData{Name: "open ORDERS", SQL: "SELECT 1"}}); err == nil {
		t.Error("a duplicate name was saved")
	}
	list, _ := st.ProjectQueries(pid, key)
	if len(list) != 1 || list[0].Version != 2 || list[0].SQL != "SELECT id FROM maria.orders" {
		t.Errorf("list: %+v", list)
	}
	vs, _ := st.ProjectQueryVersions(pid, key, id)
	if len(vs) != 2 || vs[1].SQL != "SELECT * FROM maria.orders" || !bytes.Equal(vs[1].Layout, layout) {
		t.Errorf("versions: %+v", vs)
	}
	raw, _ := os.ReadFile(st.Path())
	if bytes.Contains(raw, []byte("maria.orders")) || bytes.Contains(raw, []byte("Open orders")) {
		t.Error("a saved project query is readable in the database file")
	}
	if _, err := st.ProjectQueries(pid, make([]byte, 32)); err == nil {
		t.Error("another key opened the queries")
	}
	if err := st.DeleteProject(pid); err != nil {
		t.Fatal(err)
	}
	var n int
	st.DB().QueryRow(`SELECT COUNT(*) FROM project_query_versions`).Scan(&n)
	if n != 0 {
		t.Errorf("%d versions left after deleting the project", n)
	}
}

func TestSuggestJoins(t *testing.T) {
	customer := &mTable{Src: "CUSTOMER", PK: []string{"ID"}, Cols: []mCol{{Src: "ID", Kind: kInt32}, {Src: "NAME", Kind: kVarchar}, {Src: "REGION_CODE", Kind: kVarchar}}}
	orders := &mTable{Src: "orders", PK: []string{"id"}, Cols: []mCol{{Src: "id", Kind: kInt64}, {Src: "customer_id", Kind: kInt32},
		{Src: "region_code", Kind: kVarchar}, {Src: "name", Kind: kText}}}
	got := suggestJoins(&projectTable{Alias: "fb", Name: "customer"}, customer, &projectTable{Alias: "maria", Name: "orders"}, orders)
	var whys []string
	for _, g := range got {
		whys = append(whys, g.Why)
	}
	if len(got) != 2 || got[0].Pairs[0] != [2]string{"ID", "customer_id"} || got[1].Pairs[0] != [2]string{"REGION_CODE", "region_code"} {
		t.Errorf("across databases: %+v", got)
	}
	// Foreign keys within one database.
	orders.FKs = []mFK{{Name: "fk_cust", Cols: []string{"customer_id"}, RefTable: "CUSTOMER", RefCols: []string{"ID"}}}
	got = suggestJoins(&projectTable{Alias: "db", Name: "orders"}, orders, &projectTable{Alias: "db", Name: "customer", Source: "CUSTOMER"}, customer)
	if len(got) == 0 || got[0].Why != "foreign key fk_cust" || got[0].Pairs[0] != [2]string{"customer_id", "ID"} {
		t.Errorf("foreign key: %+v", got)
	}
	// A text column never pairs with a number.
	customer.Cols = append(customer.Cols, mCol{Src: "ORDERS_ID", Kind: kVarchar})
	got = suggestJoins(&projectTable{Alias: "fb", Name: "customer"}, customer, &projectTable{Alias: "maria", Name: "orders"}, orders)
	if slices.ContainsFunc(got, func(g joinSuggestion) bool { return g.Pairs[0][0] == "ORDERS_ID" }) {
		t.Errorf("text with number offered: %+v", got)
	}
	_ = whys
}

// foreignKeysOf names the table a foreign key points at by its project name,
// and leaves it empty when that table isn't in the project.
func TestProjectForeignKeysOf(t *testing.T) {
	ps := &projectSession{data: projectData{Tables: []projectTable{
		{Alias: "fb", Source: "STOCK", Name: "stock"}, {Alias: "fb", Source: "CUST", Name: "cust"}, {Alias: "ms", Source: "dbo.cust", Name: "cust"}}}}
	tbl := &mTable{FKs: []mFK{
		{Name: "fk_cust", Cols: []string{"CUST_ID"}, RefTable: "CUST", RefCols: []string{"ID"}},
		{Name: "fk_x", Cols: []string{"X_ID"}, RefTable: "XTAB", RefCols: []string{"ID"}}}}
	got := ps.foreignKeysOf(&ps.data.Tables[0], tbl)
	if len(got) != 2 || got[0].Table != "cust" || got[0].Cols[0] != "CUST_ID" || got[0].RefCols[0] != "ID" {
		t.Errorf("fk_cust: %+v", got)
	}
	if got[1].Table != "" || got[1].RefSource != "XTAB" {
		t.Errorf("a table outside the project: %+v", got[1])
	}
}
