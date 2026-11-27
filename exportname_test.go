package main

import "testing"

// TestExportFileBase: a query's name as an export's file name, and whether
// the query limits its rows itself.
func TestExportFileBase(t *testing.T) {
	for in, want := range map[string]string{
		"034, 043 - Coded Name":         "034.043.Coded.Name",
		"034, 043 – Coded cartographic": "034.043.Coded.Cartographic",
		"022 - ISSN numbers":            "022.ISSN.Numbers",
		"  ":                            "Query",
		"orders":                        "Orders",
	} {
		if got := exportFileBase(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	for q, want := range map[string]bool{
		`SELECT * FROM a.t LIMIT 10`:                     true,
		`SELECT * FROM a.t ORDER BY id limit 5 OFFSET 2`: true,
		`SELECT * FROM (SELECT * FROM a.t LIMIT 3) x`:    false,
		`SELECT * FROM a.t WHERE name = 'LIMIT 3'`:       false,
		"SELECT * FROM a.t -- LIMIT 3\nWHERE id > 1":     false,
		`SELECT "limit" FROM a.t`:                        false,
		`SELECT * FROM a.t`:                              false,
	} {
		if got := queryHasLimit(q); got != want {
			t.Errorf("%q: %v, want %v", q, got, want)
		}
	}
}
