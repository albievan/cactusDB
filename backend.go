package main

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// backend is a database reached through its own client instead of a
// database/sql driver: document, key-value, graph and vector stores, and
// cloud query services. openBackend wraps one in a *sql.DB, so browsing,
// queries, exports and the DDL export work the same as for SQL databases.
type backend interface {
	Ping(ctx context.Context) error
	Close() error
	// Version describes the server, e.g. "MongoDB 7.0.14" ("" when unknown).
	Version(ctx context.Context) string
	// Objects lists what the sidebar shows; the first group is the main one.
	Objects(ctx context.Context) ([]ObjectGroup, error)
	// Definition describes one object: its schema, indexes, options…
	Definition(ctx context.Context, typ, name string) (string, error)
	// SelectFor is the query that shows the rows of a queryable object.
	SelectFor(ctx context.Context, name string) (string, error)
	// CheckReadOnly refuses anything but a read.
	CheckReadOnly(q string) error
	// Query runs a read: CheckReadOnly has passed.
	Query(ctx context.Context, q string) (resultSet, error)
	// Count returns the number of rows of a queryable object (-1: unknown).
	Count(ctx context.Context, name string) (int64, error)
}

// resultSet is the rows of one query.
type resultSet interface {
	Columns() []string
	Types() []string      // the database type of each column ("" when unknown)
	Next() ([]any, error) // io.EOF after the last row
	Close() error
}

// backendOf returns the backend behind db, or nil for a database/sql driver.
func backendOf(db *sql.DB) backend {
	if db == nil {
		return nil
	}
	if d, ok := db.Driver().(*backendDriver); ok {
		return d.b
	}
	return nil
}

// isBackend reports whether engine is reached through a backend.
func isBackend(engine string) bool {
	e := engineByID(engine)
	return e != nil && e.Backend
}

// openBackend pings b and wraps it in a *sql.DB.
func openBackend(ctx context.Context, b backend) (*sql.DB, error) {
	pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := b.Ping(pctx); err != nil {
		b.Close()
		return nil, err
	}
	db := sql.OpenDB(&backendConnector{drv: &backendDriver{b: b}})
	db.SetConnMaxIdleTime(time.Minute)
	return db, nil
}

// ---- database/sql glue ---------------------------------------------------------

type backendDriver struct{ b backend }

func (d *backendDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("cactusdb: open a backend with openBackend")
}

type backendConnector struct{ drv *backendDriver }

func (c *backendConnector) Connect(context.Context) (driver.Conn, error) {
	return &backendConn{b: c.drv.b}, nil
}
func (c *backendConnector) Driver() driver.Driver { return c.drv }

// Close is called by sql.DB.Close: the client is shared by every connection.
func (c *backendConnector) Close() error { return c.drv.b.Close() }

type backendConn struct{ b backend }

func (c *backendConn) Prepare(q string) (driver.Stmt, error) { return &backendStmt{c: c, q: q}, nil }
func (c *backendConn) Close() error                          { return nil }
func (c *backendConn) Begin() (driver.Tx, error)             { return backendTx{}, nil }
func (c *backendConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return backendTx{}, nil
}
func (c *backendConn) Ping(ctx context.Context) error { return c.b.Ping(ctx) }

func (c *backendConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if len(args) > 0 {
		return nil, errors.New("query parameters are not supported for this database")
	}
	if err := c.b.CheckReadOnly(q); err != nil {
		return nil, err
	}
	rs, err := c.b.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	return &backendRows{rs: rs}, nil
}

// The transaction only exists for database/sql: every backend query is a read.
type backendTx struct{}

func (backendTx) Commit() error   { return nil }
func (backendTx) Rollback() error { return nil }

type backendStmt struct {
	c *backendConn
	q string
}

func (s *backendStmt) Close() error  { return nil }
func (s *backendStmt) NumInput() int { return -1 }
func (s *backendStmt) Exec([]driver.Value) (driver.Result, error) {
	return nil, errReadOnly
}
func (s *backendStmt) Query(args []driver.Value) (driver.Rows, error) {
	if len(args) > 0 {
		return nil, errors.New("query parameters are not supported for this database")
	}
	return s.c.QueryContext(context.Background(), s.q, nil)
}

type backendRows struct{ rs resultSet }

func (r *backendRows) Columns() []string { return r.rs.Columns() }
func (r *backendRows) Close() error      { return r.rs.Close() }
func (r *backendRows) ColumnTypeDatabaseTypeName(i int) string {
	if t := r.rs.Types(); i < len(t) {
		return t[i]
	}
	return ""
}
func (r *backendRows) Next(dest []driver.Value) error {
	row, err := r.rs.Next()
	if err != nil {
		return err
	}
	for i := range dest {
		if i < len(row) {
			dest[i] = cellValue(row[i])
		} else {
			dest[i] = nil
		}
	}
	return nil
}

// cellValue turns a value of a backend into one database/sql accepts:
// documents and arrays become JSON, exact numbers text.
func cellValue(v any) driver.Value {
	switch t := v.(type) {
	case nil, int64, float64, bool, string, time.Time:
		return t
	case []byte:
		return t
	case int:
		return int64(t)
	case int8:
		return int64(t)
	case int16:
		return int64(t)
	case int32:
		return int64(t)
	case uint8:
		return int64(t)
	case uint16:
		return int64(t)
	case uint32:
		return int64(t)
	case uint64:
		if t <= math.MaxInt64 {
			return int64(t)
		}
		return strconv.FormatUint(t, 10)
	case float32:
		return float64(t)
	case json.Number:
		return t.String()
	case fmt.Stringer:
		return t.String()
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return string(b)
	}
}

// ---- documents as rows -----------------------------------------------------------

// field is one field of a document, which keeps the order of its fields.
type field struct {
	Key   string
	Value any
}

// doc is a document with its fields in order: JSON from document stores
// decodes to doc (objects), []any (arrays) and json.Number (numbers).
type doc []field

func (d doc) get(key string) (any, bool) {
	for _, f := range d {
		if f.Key == key {
			return f.Value, true
		}
	}
	return nil, false
}

func (d doc) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range d {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(f.Key)
		b.Write(k)
		b.WriteByte(':')
		v, err := json.Marshal(f.Value)
		if err != nil {
			return nil, err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// decodeOrdered reads one JSON value, keeping the order of object fields.
func decodeOrdered(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return decodeOrderedFrom(dec, tok)
}

func decodeOrderedFrom(dec *json.Decoder, tok json.Token) (any, error) {
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			d := doc{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, _ := kt.(string)
				v, err := decodeOrdered(dec)
				if err != nil {
					return nil, err
				}
				d = append(d, field{k, v})
			}
			_, err := dec.Token() // }
			return d, err
		case '[':
			a := []any{}
			for dec.More() {
				v, err := decodeOrdered(dec)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			_, err := dec.Token() // ]
			return a, err
		}
		return nil, fmt.Errorf("unexpected %v in JSON", t)
	default:
		return t, nil // string, json.Number, bool or nil
	}
}

// parseOrdered decodes a whole JSON text, keeping the order of fields.
func parseOrdered(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	return decodeOrdered(dec)
}

// docSample is how many documents decide the columns of a document result.
const docSample = 1000

// docRows shows documents as rows: the columns are the top-level fields of
// the first docSample documents, in the order they first appear. Nested
// documents and arrays are shown as JSON.
type docRows struct {
	next    func() (doc, error) // io.EOF after the last
	closeFn func() error
	cols    []string
	types   []string
	idx     map[string]int
	buf     []doc
	err     error // of reading the sample
}

// newDocRows reads the sample from next. first names fields that come
// first (e.g. "_id"), whether or not the sample has them.
func newDocRows(next func() (doc, error), closeFn func() error, first ...string) (*docRows, error) {
	r := &docRows{next: next, closeFn: closeFn, idx: map[string]int{}}
	for _, f := range first {
		r.add(f)
	}
	for len(r.buf) < docSample {
		d, err := next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if len(r.buf) == 0 {
				r.Close()
				return nil, err
			}
			r.err = err
			break
		}
		for _, f := range d {
			r.add(f.Key)
		}
		r.buf = append(r.buf, d)
	}
	r.types = make([]string, len(r.cols))
	return r, nil
}

func (r *docRows) add(key string) {
	if _, ok := r.idx[key]; !ok {
		r.idx[key] = len(r.cols)
		r.cols = append(r.cols, key)
	}
}

func (r *docRows) Columns() []string { return r.cols }
func (r *docRows) Types() []string   { return r.types }
func (r *docRows) Close() error {
	if r.closeFn != nil {
		return r.closeFn()
	}
	return nil
}

func (r *docRows) Next() ([]any, error) {
	var d doc
	switch {
	case len(r.buf) > 0:
		d, r.buf = r.buf[0], r.buf[1:]
	case r.err != nil:
		return nil, r.err
	default:
		var err error
		if d, err = r.next(); err != nil {
			return nil, err
		}
	}
	row := make([]any, len(r.cols))
	for _, f := range d {
		if i, ok := r.idx[f.Key]; ok {
			row[i] = docCell(f.Value)
		}
	}
	return row, nil
}

// docCell shows a document value in one cell.
func docCell(v any) any {
	switch t := v.(type) {
	case doc, []any, map[string]any:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return string(b)
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n
		}
		if jsonNumber.MatchString(string(t)) {
			return t // exact
		}
		return string(t) // NaN, Infinity
	}
	return v
}

// jsonNumber is a number as JSON writes it: no leading zeros, NaN or Inf.
var jsonNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][-+]?[0-9]+)?$`)

// sliceRows is a result that is already in memory.
type sliceRows struct {
	cols, types []string
	rows        [][]any
}

func newSliceRows(cols []string, rows [][]any) *sliceRows {
	return &sliceRows{cols: cols, types: make([]string, len(cols)), rows: rows}
}

func (r *sliceRows) Columns() []string { return r.cols }
func (r *sliceRows) Types() []string   { return r.types }
func (r *sliceRows) Close() error      { return nil }
func (r *sliceRows) Next() ([]any, error) {
	if len(r.rows) == 0 {
		return nil, io.EOF
	}
	row := r.rows[0]
	r.rows = r.rows[1:]
	for i, v := range row {
		row[i] = docCell(v)
	}
	return row, nil
}

// docsOf is a docRows over documents already in memory.
func docsOf(docs []doc, first ...string) (*docRows, error) {
	return newDocRows(func() (doc, error) {
		if len(docs) == 0 {
			return nil, io.EOF
		}
		d := docs[0]
		docs = docs[1:]
		return d, nil
	}, nil, first...)
}

// ---- describing a schema -----------------------------------------------------------

// inferFields describes the fields of sample documents: their JSON types and
// in how many documents they occur, nested fields as a.b.
func inferFields(docs []doc) string {
	type info struct {
		n     int
		types map[string]bool
	}
	fields := map[string]*info{}
	var order []string
	var walk func(prefix string, d doc)
	walk = func(prefix string, d doc) {
		for _, f := range d {
			name := prefix + f.Key
			in, ok := fields[name]
			if !ok {
				in = &info{types: map[string]bool{}}
				fields[name] = in
				order = append(order, name)
			}
			in.n++
			in.types[jsonType(f.Value)] = true
			if sub, ok := f.Value.(doc); ok && strings.Count(prefix, ".") < 3 {
				walk(name+".", sub)
			}
		}
	}
	for _, d := range docs {
		walk("", d)
	}
	if len(docs) == 0 {
		return "-- no documents to describe\n"
	}
	w := 0
	for _, n := range order {
		w = max(w, len(n))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "-- fields of %d sampled document(s): type, and in how many it occurs\n", len(docs))
	for _, n := range order {
		in := fields[n]
		var ts []string
		for t := range in.types {
			ts = append(ts, t)
		}
		sort.Strings(ts)
		fmt.Fprintf(&b, "%-*s  %s", w, n, strings.Join(ts, " | "))
		if in.n < len(docs) {
			fmt.Fprintf(&b, "  (%d of %d)", in.n, len(docs))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func jsonType(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case string:
		return "string"
	case json.Number:
		if _, err := t.Int64(); err == nil {
			return "int"
		}
		return "number"
	case int, int8, int16, int32, int64, uint8, uint16, uint32, uint64:
		return "int"
	case float32, float64:
		return "double"
	case time.Time:
		return "date"
	case doc, map[string]any:
		return "object"
	case []any:
		return "array"
	case []byte:
		return "binary"
	}
	return fmt.Sprintf("%T", v)
}

// prettyJSON indents JSON for a definition.
func prettyJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// ---- connection options --------------------------------------------------------------

// parseOptions reads the Options of a connection: key=value pairs separated
// by &, ; or new lines. Keys are lower case.
func parseOptions(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == '&' || r == ';' || r == '\n' || r == '\r' }) {
		k, v, _ := strings.Cut(part, "=")
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			continue
		}
		if uv, err := url.QueryUnescape(strings.TrimSpace(v)); err == nil {
			v = uv
		}
		out[k] = strings.TrimSpace(v)
	}
	return out
}

// optBool reads a yes/no option; def when it isn't given.
func optBool(o map[string]string, key string, def bool) bool {
	v, ok := o[key]
	if !ok || v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on", "require", "required":
		return true
	}
	return false
}

// ---- reading results and checking queries ---------------------------------------------------

// firstWord returns the first word of q in upper case, after comments.
func firstWord(q string) string {
	q = stripLineComments(q)
	f := strings.Fields(q)
	if len(f) == 0 {
		return ""
	}
	w := strings.ToUpper(f[0])
	if i := strings.IndexAny(w, "({;"); i > 0 {
		w = w[:i]
	}
	return w
}

// stripLineComments removes -- and // comment lines.
func stripLineComments(q string) string {
	var b strings.Builder
	for _, line := range strings.Split(q, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "--") || strings.HasPrefix(t, "//") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

// decodeB64 decodes standard or URL base64.
func decodeB64(s string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// httpError reads an error answer of a REST API into a short message.
func httpError(status string, body []byte) error {
	msg := strings.TrimSpace(string(body))
	var e struct {
		Message string `json:"message"`
		Error   any    `json:"error"`
		Errors  []struct {
			Msg     string `json:"msg"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &e) == nil {
		switch {
		case e.Message != "":
			msg = e.Message
		case len(e.Errors) > 0 && e.Errors[0].Msg != "":
			msg = e.Errors[0].Msg
		case len(e.Errors) > 0 && e.Errors[0].Message != "":
			msg = e.Errors[0].Message
		case e.Error != nil:
			if m, ok := e.Error.(map[string]any); ok && m["message"] != nil {
				msg = fmt.Sprint(m["message"])
			} else if s, ok := e.Error.(string); ok {
				msg = s
			}
		}
	}
	if len(msg) > 600 {
		msg = msg[:600] + "…"
	}
	if msg == "" {
		return errors.New(status)
	}
	return fmt.Errorf("%s: %s", status, msg)
}

// blankQuoted replaces what is between the given quote characters, and
// // and /* */ comments, by spaces, so that words in them aren't mistaken
// for keywords. A quote is escaped by doubling it or with a backslash.
func blankQuoted(q, quotes string) string {
	b := []byte(q)
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case strings.IndexByte(quotes, c) >= 0:
			j := i + 1
			for ; j < len(b); j++ {
				if b[j] == '\\' {
					b[j] = ' '
					if j+1 < len(b) {
						j++
						b[j] = ' '
					}
					continue
				}
				if b[j] == c {
					if j+1 < len(b) && b[j+1] == c { // doubled
						b[j], b[j+1] = ' ', ' '
						j++
						continue
					}
					break
				}
				b[j] = ' '
			}
			i = j
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for ; i < len(b) && b[i] != '\n'; i++ {
				b[i] = ' '
			}
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			for ; i < len(b) && !(b[i] == '*' && i+1 < len(b) && b[i+1] == '/'); i++ {
				b[i] = ' '
			}
			if i+1 < len(b) {
				b[i], b[i+1] = ' ', ' '
				i++
			}
		}
	}
	return string(b)
}
