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

// Text that a spreadsheet would run as a formula is marked as text in CSV.
func TestCSVText(t *testing.T) {
	for in, want := range map[string]string{
		`=HYPERLINK("http://x")`: `'=HYPERLINK("http://x")`,
		"+cmd|' /c calc'!A1":     "'+cmd|' /c calc'!A1",
		"@SUM(A1)":               "'@SUM(A1)",
		"-12.5":                  "-12.5",
		"-x":                     "'-x",
		"plain":                  "plain",
		"":                       "",
	} {
		if got := csvText(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
