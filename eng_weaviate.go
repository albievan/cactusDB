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

// Weaviate: collections (classes) through the REST API; queries are GraphQL
// Get or Aggregate. A Get without arguments on one collection pages through
// every object (with its cursor), so exports have them all.

func init() { backendOpeners[EngineWeaviate] = openWeaviate }

type weaviateBackend struct {
	c   *http.Client
	url string
	key string
}

func openWeaviate(ctx context.Context, p ConnParams, opts map[string]string) (backend, error) {
	useTLS := optBool(opts, "tls", false) || optBool(opts, "ssl", false) || strings.HasSuffix(strings.TrimSpace(p.Host), ".weaviate.cloud")
	port := p.Port
	if useTLS && (port == 0 || port == 8080) {
		port = 443
	}
	return &weaviateBackend{c: apiClient(opts), url: baseURL(p.Host, port, 8080, useTLS), key: strings.TrimSpace(p.Password)}, nil
}

func (w *weaviateBackend) do(ctx context.Context, method, path string, body, out any) error {
	h := http.Header{}
	if w.key != "" {
		h.Set("Authorization", "Bearer "+w.key)
	}
	_, err := doJSON(ctx, w.c, method, w.url+path, h, body, out)
	return err
}

func (w *weaviateBackend) Ping(ctx context.Context) error {
	var s struct{}
	return w.do(ctx, "GET", "/v1/schema", nil, &s)
}
func (w *weaviateBackend) Close() error { return nil }

func (w *weaviateBackend) Version(ctx context.Context) string {
	var m struct {
		Version string `json:"version"`
	}
	if w.do(ctx, "GET", "/v1/meta", nil, &m) != nil {
		return "Weaviate"
	}
	return "Weaviate " + m.Version
}

type weaviateClass struct {
	Class      string `json:"class"`
	Properties []struct {
		Name     string   `json:"name"`
		DataType []string `json:"dataType"`
	} `json:"properties"`
}

func (w *weaviateBackend) schema(ctx context.Context) ([]weaviateClass, json.RawMessage, error) {
	var raw json.RawMessage
	if err := w.do(ctx, "GET", "/v1/schema", nil, &raw); err != nil {
		return nil, nil, err
	}
	var s struct {
		Classes []weaviateClass `json:"classes"`
	}
	err := json.Unmarshal(raw, &s)
	return s.Classes, raw, err
}

func (w *weaviateBackend) Objects(ctx context.Context) ([]ObjectGroup, error) {
	classes, _, err := w.schema(ctx)
	if err != nil {
		return nil, err
	}
	items := []string{}
	for _, c := range classes {
		items = append(items, c.Class)
	}
	sort.Strings(items)
	return []ObjectGroup{{Type: ObjTable, Label: "Collections", Queryable: true, Items: items}}, nil
}

// weaviateScalar: properties that can be selected without a sub-selection.
func weaviateScalar(dataType []string) bool {
	if len(dataType) == 0 {
		return false
	}
	switch strings.TrimSuffix(dataType[0], "[]") {
	case "text", "string", "int", "number", "boolean", "date", "uuid":
		return true
	}
	return false
}

func (w *weaviateBackend) SelectFor(ctx context.Context, name string) (string, error) {
	classes, _, err := w.schema(ctx)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("{\n  Get {\n    " + name + " {\n")
	for _, c := range classes {
		if c.Class == name {
			for _, p := range c.Properties {
				if weaviateScalar(p.DataType) {
					b.WriteString("      " + p.Name + "\n")
				}
			}
		}
	}
	b.WriteString("      _additional { id }\n    }\n  }\n}")
	return b.String(), nil
}

func (w *weaviateBackend) Count(ctx context.Context, name string) (int64, error) {
	var res struct {
		Data struct {
			Aggregate map[string][]struct {
				Meta struct {
					Count int64 `json:"count"`
				} `json:"meta"`
			} `json:"Aggregate"`
		} `json:"data"`
	}
	if err := w.do(ctx, "POST", "/v1/graphql", map[string]string{"query": "{ Aggregate { " + name + " { meta { count } } } }"}, &res); err != nil {
		return -1, err
	}
	if a := res.Data.Aggregate[name]; len(a) > 0 {
		return a[0].Meta.Count, nil
	}
	return -1, nil
}

func (w *weaviateBackend) Definition(ctx context.Context, typ, name string) (string, error) {
	var raw json.RawMessage
	if err := w.do(ctx, "GET", "/v1/schema/"+name, nil, &raw); err != nil {
		return "", err
	}
	v, _ := parseOrdered(raw)
	out := "// collection " + name + "\n"
	if n, err := w.Count(ctx, name); err == nil && n >= 0 {
		out += fmt.Sprintf("// %d object(s)\n", n)
	}
	return out + "\n" + prettyJSON(v) + "\n", nil
}

var errGraphQLReadOnly = errors.New("cactusdb is read-only: only GraphQL queries ({ Get { … } } or { Aggregate { … } }) are allowed")

func (w *weaviateBackend) CheckReadOnly(q string) error {
	code := strings.TrimSpace(blankQuoted(stripLineComments(q), `"`))
	if !strings.HasPrefix(code, "{") && !strings.HasPrefix(strings.ToLower(code), "query") {
		return errGraphQLReadOnly
	}
	if regexp.MustCompile(`(?i)\bmutation\b`).MatchString(code) {
		return errGraphQLReadOnly
	}
	return nil
}

// A Get of one collection without arguments: cactusdb pages through it.
var weaviatePlainGet = regexp.MustCompile(`^\s*(?:query\s*)?\{\s*Get\s*\{\s*([A-Za-z_]\w*)\s*\{`)

func (w *weaviateBackend) Query(ctx context.Context, q string) (resultSet, error) {
	q = stripLineComments(q)
	m := weaviatePlainGet.FindStringSubmatchIndex(q)
	if m == nil {
		return w.once(ctx, q)
	}
	// Ask for the id, which is the cursor, and page with after:.
	body := q[m[1]:]
	if !regexp.MustCompile(`_additional\s*\{[^}]*\bid\b`).MatchString(body) {
		q = q[:m[1]] + " _additional { id } " + body
	}
	after := ""
	var page []doc
	done := false
	return newDocRows(func() (doc, error) {
		for len(page) == 0 {
			if done {
				return nil, io.EOF
			}
			args := "(limit: 1000"
			if after != "" {
				args += `, after: "` + after + `"`
			}
			paged := q[:m[3]] + args + ")" + q[m[3]:]
			docs, err := w.get(ctx, paged)
			if err != nil {
				return nil, err
			}
			page = docs
			if len(docs) < 1000 {
				done = true
			}
			if len(docs) > 0 {
				id, _ := docs[len(docs)-1].get("_id")
				after = fmt.Sprint(id)
			}
			if len(docs) == 0 {
				return nil, io.EOF
			}
		}
		d := page[0]
		page = page[1:]
		return d, nil
	}, nil, "_id")
}

// once runs a query and returns the objects of its first list.
func (w *weaviateBackend) once(ctx context.Context, q string) (resultSet, error) {
	docs, err := w.get(ctx, q)
	if err != nil {
		return nil, err
	}
	return docsOf(docs, "_id")
}

// get runs a GraphQL query and returns the objects of its first list (Get
// or Aggregate of one collection), with _additional fields as _name.
func (w *weaviateBackend) get(ctx context.Context, q string) ([]doc, error) {
	var raw json.RawMessage
	if err := w.do(ctx, "POST", "/v1/graphql", map[string]string{"query": q}, &raw); err != nil {
		return nil, err
	}
	v, err := parseOrdered(raw)
	if err != nil {
		return nil, err
	}
	res, _ := v.(doc)
	if errs, ok := res.get("errors"); ok && errs != nil {
		var msgs []string
		for _, e := range toAnySlice(errs) {
			if d, ok := e.(doc); ok {
				m, _ := d.get("message")
				msgs = append(msgs, fmt.Sprint(m))
			}
		}
		if len(msgs) > 0 {
			return nil, errors.New(strings.Join(msgs, "; "))
		}
	}
	data, _ := res.get("data")
	list := firstList(data)
	out := make([]doc, 0, len(list))
	for _, e := range list {
		d, ok := e.(doc)
		if !ok {
			out = append(out, doc{{"value", e}})
			continue
		}
		var row doc
		for _, f := range d {
			if f.Key == "_additional" {
				if add, ok := f.Value.(doc); ok {
					for _, a := range add {
						row = append(row, field{"_" + a.Key, a.Value})
					}
					continue
				}
			}
			row = append(row, f)
		}
		out = append(out, row)
	}
	return out, nil
}

// firstList finds the first array in data (data.Get.<Class>).
func firstList(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	case doc:
		for _, f := range t {
			if l := firstList(f.Value); l != nil {
				return l
			}
		}
	}
	return nil
}
