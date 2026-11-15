package main

import (
	"database/sql"
	"testing"
	"time"
)

func TestProjectFormatFunctions(t *testing.T) {
	for in, want := range map[string]string{"FRANK o'BRIEN": "Frank O'brien", "city deep auto-spares": "City Deep Auto-Spares", "": ""} {
		if got := titleCase(in); got != want {
			t.Errorf("titleCase(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"Frank Ostoja-Smith": "frankOstojaSmith", "STK_NO": "stkNo", "  a b  ": "aB"} {
		if got := camelCase(in); got != want {
			t.Errorf("camelCase(%q) = %q, want %q", in, got, want)
		}
	}
	d := time.Date(2026, 11, 5, 14, 7, 9, 0, time.UTC)
	for p, want := range map[string]string{"yyyy-MMM-dd": "2026-Nov-05", "dd/MM/yyyy HH:mm": "05/11/2026 14:07", "d MMMM yyyy": "5 November 2026",
		"ddd, d MMM yy h:mm tt": "Thu, 5 Nov 26 2:07 PM", "'Week of' yyyy-MM-dd": "Week of 2026-11-05"} {
		if got := formatDate(d, p); got != want {
			t.Errorf("formatDate %q = %q, want %q", p, got, want)
		}
	}
	for _, c := range []struct {
		f    float64
		p    string
		want string
	}{
		{1234567.891, "#,##0.00", "1,234,567.89"}, {1234567.891, "R #,##0.00", "R 1,234,567.89"}, {-1234.5, "$#,##0.00", "-$1,234.50"},
		{1234.5, "#,##0 €", "1,235 €"}, {0.1234, "0.0%", "12.3%"}, {1234567.5, "# ##0,00", "1 234 567,50"}, {1234567.5, "#.##0,00", "1.234.567,50"},
		{42, "0", "42"}, {-0.001, "0.00", "0.00"},
	} {
		if got := formatNumber(c.f, c.p); got != c.want {
			t.Errorf("formatNumber(%v, %q) = %q, want %q", c.f, c.p, got, c.want)
		}
	}
	// In SQLite, as a project query uses them; a value that isn't a date is left as it is.
	db, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var a, b, c, e, n string
	var z any
	err = db.QueryRow(`SELECT cdb_title('frank OSTOJA'), cdb_camel('stk no'), cdb_date('2026-11-05 14:07:09', 'yyyy-MMM-dd'), cdb_date('not a date', 'yyyy'), cdb_number(1234.5, 'R #,##0.00'), cdb_number(NULL, '0')`).Scan(&a, &b, &c, &e, &n, &z)
	if err != nil || a != "Frank Ostoja" || b != "stkNo" || c != "2026-Nov-05" || e != "not a date" || n != "R 1,234.50" || z != nil {
		t.Errorf("in SQLite: %q %q %q %q %q %v (%v)", a, b, c, e, n, z, err)
	}
}
