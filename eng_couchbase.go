package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// Couchbase: collections (bucket.scope.collection) through the query
// service's REST API, which runs SQL++ with readonly=true: the server
// refuses anything that writes.

func init() { backendOpeners[EngineCouchbase] = openCouchbase }

type couchbaseBackend struct {
	c      *http.Client
	url    string // …/query/service
	user   string
	pass   string
	bucket string // "" = every bucket
}

func openCouchbase(ctx context.Context, p ConnParams, opts map[string]string) (backend, error) {
	useTLS := optBool(opts, "tls", false) || optBool(opts, "ssl", false)
	def := 8093
	if useTLS {
		def = 18093
	}
	return &couchbaseBackend{c: apiClient(opts), url: baseURL(p.Host, p.Port, def, useTLS) + "/query/service",
		user: p.Username, pass: p.Password, bucket: strings.TrimSpace(p.Database)}, nil
}

func (b *couchbaseBackend) Ping(ctx context.Context) error {
	_, err := b.all(ctx, "SELECT RAW 1")
	return err
}
func (b *couchbaseBackend) Close() error { return nil }

// start sends a statement and returns the open answer.
func (b *couchbaseBackend) start(ctx context.Context, q string) (*http.Response, error) {
	h := http.Header{}
	h.Set("Authorization", "Basic "+basicAuth(b.user, b.pass))
	body := map[string]any{"statement": q, "readonly": true, "timeout": "600s", "client_context_id": "cactusdb"}
	resp, err := sendJSON(ctx, b.c, "POST", b.url, h, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 401 {
		resp.Body.Close()
		return nil, errors.New("401 Unauthorized: wrong username or password")
	}
	return resp, nil
}

// all runs a statement and returns every result.
func (b *couchbaseBackend) all(ctx context.Context, q string) ([]any, error) {
	cr, err := b.open(ctx, q)
	if err != nil {
		return nil, err
	}
	defer cr.body.Close()
	var out []any
	for {
		v, err := cr.nextValue()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
}

func (b *couchbaseBackend) strings(ctx context.Context, q string) ([]string, error) {
	vals, err := b.all(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		out = append(out, fmt.Sprint(v))
	}
	return out, nil
}

func (b *couchbaseBackend) Version(ctx context.Context) string {
	v, err := b.strings(ctx, "SELECT RAW ds_version()")
	if err != nil || len(v) == 0 {
		return "Couchbase"
	}
	return "Couchbase " + v[0]
}

// splitKeyspace splits bucket.scope.collection from the right: a bucket's
// name may hold dots, a scope's or collection's can't. Fewer than three
// parts are a bucket (Couchbase 6).
func splitKeyspace(name string) []string {
	parts := strings.Split(name, ".")
	if len(parts) < 3 {
		return []string{name}
	}
	n := len(parts)
	return []string{strings.Join(parts[:n-2], "."), parts[n-2], parts[n-1]}
}

// keyspaceExpr quotes bucket.scope.collection for SQL++.
func keyspaceExpr(name string) string {
	parts := splitKeyspace(name)
	for i, p := range parts {
		parts[i] = "`" + strings.ReplaceAll(p, "`", "``") + "`"
	}
	return strings.Join(parts, ".")
}

func (b *couchbaseBackend) Objects(ctx context.Context) ([]ObjectGroup, error) {
	filter := ""
	if b.bucket != "" {
		filter = " AND k.`bucket` = " + sqlppString(b.bucket)
	}
	colls, err := b.strings(ctx, "SELECT RAW k.`bucket` || '.' || k.`scope` || '.' || k.name FROM system:keyspaces AS k WHERE k.`bucket` IS VALUED AND k.`scope` != '_system'"+filter)
	if err != nil {
		// Couchbase 6: buckets only.
		f6 := ""
		if b.bucket != "" {
			f6 = " WHERE name = " + sqlppString(b.bucket)
		}
		if colls, err = b.strings(ctx, "SELECT RAW name FROM system:keyspaces"+f6); err != nil {
			return nil, err
		}
	}
	sort.Strings(colls)
	idxFilter := ""
	if b.bucket != "" {
		idxFilter = " WHERE IFMISSING(i.bucket_id, i.keyspace_id) = " + sqlppString(b.bucket)
	}
	indexes, _ := b.strings(ctx, "SELECT RAW i.name || ' on ' || IFMISSING(i.bucket_id || '.' || i.scope_id || '.' || i.keyspace_id, i.keyspace_id) FROM system:indexes AS i"+idxFilter)
	sort.Strings(indexes)
	if indexes == nil {
		indexes = []string{}
	}
	return []ObjectGroup{
		{Type: ObjTable, Label: "Collections", Queryable: true, Items: colls},
		{Type: ObjIndex, Label: "Indexes", Items: indexes},
	}, nil
}

func sqlppString(s string) string { b, _ := json.Marshal(s); return string(b) }

func (b *couchbaseBackend) SelectFor(_ context.Context, name string) (string, error) {
	return "SELECT META(c).id AS _id, c.*\nFROM " + keyspaceExpr(name) + " AS c", nil
}

func (b *couchbaseBackend) Count(ctx context.Context, name string) (int64, error) {
	v, err := b.all(ctx, "SELECT RAW COUNT(*) FROM "+keyspaceExpr(name))
	if err != nil || len(v) == 0 {
		return -1, err
	}
	n, _ := v[0].(json.Number).Int64()
	return n, nil
}

func (b *couchbaseBackend) Definition(ctx context.Context, typ, name string) (string, error) {
	if typ == ObjIndex {
		idx, ks, _ := strings.Cut(name, " on ")
		parts := splitKeyspace(ks)
		where := "i.name = " + sqlppString(idx) + " AND i.keyspace_id = " + sqlppString(parts[len(parts)-1])
		if len(parts) == 3 {
			where += " AND i.bucket_id = " + sqlppString(parts[0]) + " AND i.scope_id = " + sqlppString(parts[1])
		}
		v, err := b.all(ctx, "SELECT i.* FROM system:indexes AS i WHERE "+where)
		if err != nil {
			return "", err
		}
		if len(v) == 0 {
			return "", fmt.Errorf("index %s not found", name)
		}
		d, _ := v[0].(doc)
		keys, _ := d.get("index_key")
		cond, _ := d.get("condition")
		using, _ := d.get("using")
		primary, _ := d.get("is_primary")
		var stmt string
		if primary == true {
			stmt = fmt.Sprintf("CREATE PRIMARY INDEX `%s` ON %s", strings.ReplaceAll(idx, "`", "``"), keyspaceExpr(ks))
		} else {
			var ks2 []string
			for _, k := range toAnySlice(keys) {
				ks2 = append(ks2, fmt.Sprint(k))
			}
			stmt = fmt.Sprintf("CREATE INDEX `%s` ON %s(%s)", strings.ReplaceAll(idx, "`", "``"), keyspaceExpr(ks), strings.Join(ks2, ", "))
			if cond != nil {
				stmt += " WHERE " + fmt.Sprint(cond)
			}
		}
		if using != nil {
			stmt += " USING " + strings.ToUpper(fmt.Sprint(using))
		}
		return stmt + ";\n\n/* " + prettyJSON(d) + " */\n", nil
	}
	var out strings.Builder
	fmt.Fprintf(&out, "/* collection %s */\n", name)
	if n, err := b.Count(ctx, name); err == nil {
		fmt.Fprintf(&out, "/* %d document(s) */\n", n)
	}
	v, err := b.all(ctx, "INFER "+keyspaceExpr(name)+` WITH {"sample_size": 1000, "num_sample_values": 3}`)
	if err != nil {
		out.WriteString("\n/* the schema can't be inferred: " + err.Error() + " */\n")
	} else {
		out.WriteString("\n/* inferred schema (INFER of up to 1,000 documents) */\n" + prettyJSON(v) + "\n")
	}
	if idx, err := b.strings(ctx, "SELECT RAW i.name FROM system:indexes AS i WHERE IFMISSING(i.bucket_id || '.' || i.scope_id || '.' || i.keyspace_id, i.keyspace_id) = "+sqlppString(name)); err == nil && len(idx) > 0 {
		sort.Strings(idx)
		out.WriteString("\n/* indexes: " + strings.Join(idx, ", ") + " */\n")
	}
	return out.String(), nil
}

func toAnySlice(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	return nil
}

var (
	sqlppWrites      = regexp.MustCompile(`(?i)\b(INSERT|UPSERT|UPDATE|DELETE|MERGE|CREATE|DROP|ALTER|BUILD|GRANT|REVOKE|EXECUTE|PREPARE|TRUNCATE|START|COMMIT|ROLLBACK|SAVEPOINT)\b`)
	errSQLPPReadOnly = errors.New("cactusdb is read-only: only SELECT, WITH, INFER and EXPLAIN are allowed")
)

func (b *couchbaseBackend) CheckReadOnly(q string) error {
	return checkWords(q, "'\"`", []string{"SELECT", "WITH", "INFER", "EXPLAIN", "ADVISE"}, sqlppWrites, errSQLPPReadOnly)
}

func (b *couchbaseBackend) Query(ctx context.Context, q string) (resultSet, error) {
	cr, err := b.open(ctx, q)
	if err != nil {
		return nil, err
	}
	return newDocRows(func() (doc, error) {
		v, err := cr.nextValue()
		if err != nil {
			return nil, err
		}
		if d, ok := v.(doc); ok {
			return d, nil
		}
		return doc{{"value", v}}, nil // SELECT RAW …
	}, cr.body.Close, "_id")
}

// open sends a statement and reads its answer up to the first result.
func (b *couchbaseBackend) open(ctx context.Context, q string) (*couchRows, error) {
	resp, err := b.start(ctx, strings.TrimRight(strings.TrimSpace(q), ";"))
	if err != nil {
		return nil, err
	}
	cr := &couchRows{body: resp.Body, dec: json.NewDecoder(resp.Body), status: resp.Status}
	cr.dec.UseNumber()
	if err := cr.toResults(); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return cr, nil
}

// couchRows reads the results of an answer as they arrive:
// {"requestID":…, "signature":…, "results":[…], "status":…, "errors":[…]}.
type couchRows struct {
	body   io.ReadCloser
	dec    *json.Decoder
	status string
	in     bool // in the results array
	done   bool
	errs   []any
	state  string
}

// toResults reads up to the first result, or to the end when there is none.
func (c *couchRows) toResults() error {
	if _, err := c.dec.Token(); err != nil { // {
		return fmt.Errorf("%s: unexpected answer: %w", c.status, err)
	}
	return c.skipTo()
}

// skipTo reads fields until "results" opens, or the answer ends.
func (c *couchRows) skipTo() error {
	for c.dec.More() {
		kt, err := c.dec.Token()
		if err != nil {
			return err
		}
		k, _ := kt.(string)
		if k == "results" && !c.in && !c.done {
			if _, err := c.dec.Token(); err != nil { // [
				return err
			}
			c.in = true
			return nil
		}
		v, err := decodeOrdered(c.dec)
		if err != nil {
			return err
		}
		switch k {
		case "errors":
			c.errs = toAnySlice(v)
		case "status":
			c.state = fmt.Sprint(v)
		}
	}
	c.done = true
	return c.failure()
}

func (c *couchRows) failure() error {
	if len(c.errs) == 0 && (c.state == "" || c.state == "success") {
		return nil
	}
	var msgs []string
	for _, e := range c.errs {
		if d, ok := e.(doc); ok {
			m, _ := d.get("msg")
			code, _ := d.get("code")
			msgs = append(msgs, fmt.Sprintf("%v (%v)", m, code))
		}
	}
	if len(msgs) == 0 {
		msgs = append(msgs, "the query ended with status "+c.state)
	}
	return errors.New(strings.Join(msgs, "; "))
}

// nextValue returns the next result, io.EOF after the last.
func (c *couchRows) nextValue() (any, error) {
	if c.in && c.dec.More() {
		return decodeOrdered(c.dec)
	}
	if c.in {
		if _, err := c.dec.Token(); err != nil { // ]
			return nil, err
		}
		c.in, c.done = false, true
		for c.dec.More() {
			if err := c.skipRest(); err != nil {
				return nil, err
			}
		}
		if err := c.failure(); err != nil {
			return nil, err
		}
	}
	return nil, io.EOF
}

func (c *couchRows) skipRest() error {
	kt, err := c.dec.Token()
	if err != nil {
		return err
	}
	v, err := decodeOrdered(c.dec)
	if err != nil {
		return err
	}
	switch kt {
	case "errors":
		c.errs = toAnySlice(v)
	case "status":
		c.state = fmt.Sprint(v)
	}
	return nil
}
