package main

import "testing"

func TestSheetName(t *testing.T) {
	for in, want := range map[string]string{
		"customers":                        "customers",
		"cutter_subdiv_code <> ''":         "cutter_subdiv_code <>",
		"'quoted'":                         "quoted",
		"a/b:c":                            "a_b_c",
		"history":                          "Data",
		"":                                 "Data",
		"abcdefghijklmnopqrstuvwxyz01234'": "abcdefghijklmnopqrstuvwxyz01234",
	} {
		if got := sheetName(in); got != want {
			t.Errorf("sheetName(%q) = %q, want %q", in, got, want)
		}
	}
}
