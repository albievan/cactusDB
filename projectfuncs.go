package main

import (
	"database/sql/driver"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"modernc.org/sqlite"
)

// Functions the advanced query builder offers on top of SQLite's own, for the
// value a column shows: Title Case, camelCase, and dates and numbers in a
// format of the user's own (yyyy-MMM-dd, R #,##0.00). They only read their
// arguments, and a value they can't format comes back as it was.
func init() {
	sqlite.MustRegisterDeterministicScalarFunction("cdb_title", 1, func(_ *sqlite.FunctionContext, a []driver.Value) (driver.Value, error) {
		if s, ok := sqlText(a[0]); ok {
			return titleCase(s), nil
		}
		return a[0], nil
	})
	sqlite.MustRegisterDeterministicScalarFunction("cdb_camel", 1, func(_ *sqlite.FunctionContext, a []driver.Value) (driver.Value, error) {
		if s, ok := sqlText(a[0]); ok {
			return camelCase(s), nil
		}
		return a[0], nil
	})
	sqlite.MustRegisterDeterministicScalarFunction("cdb_date", 2, func(_ *sqlite.FunctionContext, a []driver.Value) (driver.Value, error) {
		s, ok := sqlText(a[0])
		p, pok := sqlText(a[1])
		if !ok || !pok {
			return a[0], nil
		}
		if t, ok := parseSQLTime(s); ok {
			return formatDate(t, p), nil
		}
		return a[0], nil
	})
	sqlite.MustRegisterDeterministicScalarFunction("cdb_number", 2, func(_ *sqlite.FunctionContext, a []driver.Value) (driver.Value, error) {
		p, pok := sqlText(a[1])
		if !pok || a[0] == nil {
			return a[0], nil
		}
		var f float64
		switch x := a[0].(type) {
		case int64:
			f = float64(x)
		case float64:
			f = x
		default:
			s, _ := sqlText(x)
			v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if err != nil {
				return a[0], nil
			}
			f = v
		}
		return formatNumber(f, p), nil
	})
}

// sqlText is a SQLite value as text; false for NULL.
func sqlText(v driver.Value) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", false
	case string:
		return x, true
	case []byte:
		return string(x), true
	case int64:
		return strconv.FormatInt(x, 10), true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case time.Time:
		return x.Format("2006-01-02 15:04:05.999999"), true
	}
	return "", false
}

// titleCase capitalises the first letter of each word and lowers the rest
// ("FRANK o'BRIEN" -> "Frank O'brien").
func titleCase(s string) string {
	var b strings.Builder
	prev := ' '
	for _, r := range s {
		if unicode.IsLetter(prev) || unicode.IsDigit(prev) || prev == '\'' {
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(unicode.ToUpper(r))
		}
		prev = r
	}
	return b.String()
}

// camelCase joins the words, the first in small letters and the others
// capitalised ("Frank Ostoja-Smith" -> "frankOstojaSmith").
func camelCase(s string) string {
	words := strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for i, w := range words {
		w = strings.ToLower(w)
		if i > 0 {
			r := []rune(w)
			r[0] = unicode.ToUpper(r[0])
			w = string(r)
		}
		words[i] = w
	}
	return strings.Join(words, "")
}

// parseSQLTime reads a date or time as the copies hold them (ISO text).
func parseSQLTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range []string{"2006-01-02 15:04:05.999999999", "2006-01-02T15:04:05.999999999Z07:00", "2006-01-02T15:04:05.999999999",
		"2006-01-02 15:04", "2006-01-02", "15:04:05.999999999", "15:04"} {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// formatDate writes t in a pattern: yyyy yy, MMMM (January) MMM (Jan) MM M,
// dddd (Monday) ddd (Mon) dd d, HH H (24-hour), hh h (12-hour), mm, ss, fff
// (milliseconds), tt (AM/PM); text in 'quotes' is kept as it is.
func formatDate(t time.Time, p string) string {
	tokens := []struct {
		tok string
		f   func() string
	}{
		{"yyyy", func() string { return t.Format("2006") }}, {"yy", func() string { return t.Format("06") }},
		{"MMMM", func() string { return t.Format("January") }}, {"MMM", func() string { return t.Format("Jan") }},
		{"MM", func() string { return t.Format("01") }}, {"M", func() string { return strconv.Itoa(int(t.Month())) }},
		{"dddd", func() string { return t.Format("Monday") }}, {"ddd", func() string { return t.Format("Mon") }},
		{"dd", func() string { return t.Format("02") }}, {"d", func() string { return strconv.Itoa(t.Day()) }},
		{"HH", func() string { return t.Format("15") }}, {"H", func() string { return strconv.Itoa(t.Hour()) }},
		{"hh", func() string { return t.Format("03") }}, {"h", func() string { return t.Format("3") }},
		{"mm", func() string { return t.Format("04") }}, {"ss", func() string { return t.Format("05") }},
		{"fff", func() string { return t.Format(".000")[1:] }}, {"tt", func() string { return t.Format("PM") }},
	}
	var b strings.Builder
	for i := 0; i < len(p); {
		if p[i] == '\'' {
			j := strings.IndexByte(p[i+1:], '\'')
			if j < 0 {
				b.WriteString(p[i+1:])
				break
			}
			b.WriteString(p[i+1 : i+1+j])
			i += j + 2
			continue
		}
		found := false
		for _, tk := range tokens {
			if strings.HasPrefix(p[i:], tk.tok) {
				b.WriteString(tk.f())
				i += len(tk.tok)
				found = true
				break
			}
		}
		if !found {
			b.WriteByte(p[i])
			i++
		}
	}
	return b.String()
}

// formatNumber writes f in a pattern such as "#,##0.00", "R #,##0.00",
// "#,##0 €", "0.0%", "# ##0,00" or "#.##0,00": the part made of # 0 and
// separators is the number (its decimals the 0s and #s after the decimal
// separator, grouped by threes when it has a grouping separator); what is
// around it is kept. A % after it shows the number times 100.
func formatNumber(f float64, p string) string {
	start := strings.IndexAny(p, "#0")
	if start < 0 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	end := start
	for end < len(p) && (strings.IndexByte("#0,.", p[end]) >= 0 || (p[end] == ' ' && end+1 < len(p) && strings.IndexByte("#0", p[end+1]) >= 0)) {
		end++
	}
	num := strings.TrimRight(p[start:end], ",.")
	prefix, suffix := p[:start], p[start+len(num):]
	if strings.Contains(suffix, "%") {
		f *= 100
	}
	// The decimal separator: the last of . and , when both are there; else a lone "." (or a
	// lone "," followed by other than three digits).
	dec, group := byte('.'), byte(0)
	li := strings.LastIndexAny(num, ".,")
	switch {
	case strings.Contains(num, ".") && strings.Contains(num, ","):
		dec = num[li]
		group = map[byte]byte{'.': ',', ',': '.'}[dec]
	case strings.Contains(num, ","):
		if len(num)-li-1 == 3 {
			group, li = ',', -1
		} else {
			dec = ','
		}
	case strings.Contains(num, "."):
		dec = '.'
	default:
		li = -1
	}
	if strings.Contains(num, " ") {
		group = ' '
	}
	decimals := 0
	if li >= 0 && num[li] == dec {
		decimals = len(num) - li - 1
	}
	neg := f < 0 || math.Signbit(f) && f != 0
	// Halves round away from zero (2.5 -> 3), as people expect, not to even.
	scale := math.Pow(10, float64(decimals))
	s := strconv.FormatFloat(math.Round(math.Abs(f)*scale)/scale, 'f', decimals, 64)
	whole, frac, _ := strings.Cut(s, ".")
	if group != 0 {
		var g strings.Builder
		for i, r := range whole {
			if i > 0 && (len(whole)-i)%3 == 0 {
				g.WriteByte(group)
			}
			g.WriteRune(r)
		}
		whole = g.String()
	}
	out := whole
	if decimals > 0 {
		out += string(dec) + frac
	}
	if neg && strings.Trim(out, "0.,  ") != "" {
		return "-" + prefix + out + suffix
	}
	return prefix + out + suffix
}
