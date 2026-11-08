package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Azure Cosmos DB, NoSQL API, through its REST API with the account key:
// containers (database.container), their stored procedures, functions and
// triggers. Queries are Cosmos DB SQL, sent to one container: the one named
// in a first line "-- container: database.container", else the one after
// FROM. The query API only reads; a read-only key also works.

func init() { backendOpeners[EngineCosmosDB] = openCosmos }

type cosmosBackend struct {
	c   *http.Client
	url string
	key []byte
	db  string // "" = every database
}

func openCosmos(ctx context.Context, p ConnParams, opts map[string]string) (backend, error) {
	host := strings.TrimSpace(p.Host)
	if host == "" {
		return nil, errors.New("enter the account endpoint, e.g. https://myaccount.documents.azure.com:443/")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(p.Password))
	if err != nil || len(key) == 0 {
		return nil, errors.New("the account key isn't valid: copy a primary or read-only key from the account's Keys page")
	}
	port := p.Port
	if port == 0 {
		port = 443
	}
	u := baseURL(host, port, 443, true)
	return &cosmosBackend{c: apiClient(opts), url: u, key: key, db: strings.TrimSpace(p.Database)}, nil
}

// do sends a request for resource link (e.g. dbs/shop/colls/orders) of
// resource type (dbs, colls, docs, sprocs…), signed with the key.
func (c *cosmosBackend) do(ctx context.Context, method, resType, resLink, path string, header http.Header, body, out any) (http.Header, error) {
	date := strings.ToLower(time.Now().UTC().Format(http.TimeFormat))
	text := strings.ToLower(method) + "\n" + strings.ToLower(resType) + "\n" + resLink + "\n" + date + "\n\n"
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte(text))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if header == nil {
		header = http.Header{}
	}
	header.Set("Authorization", url.QueryEscape("type=master&ver=1.0&sig="+sig))
	header.Set("x-ms-date", date)
	header.Set("x-ms-version", "2018-12-31")
	return doJSON(ctx, c.c, method, c.url+"/"+path, header, body, out)
}

func (c *cosmosBackend) Ping(ctx context.Context) error {
	_, err := c.do(ctx, "GET", "", "", "", nil, nil, nil)
	return err
}
func (c *cosmosBackend) Close() error { return nil }

func (c *cosmosBackend) Version(ctx context.Context) string {
	var acct struct {
		ID string `json:"id"`
	}
	c.do(ctx, "GET", "", "", "", nil, nil, &acct)
	if acct.ID != "" {
		return "Azure Cosmos DB (NoSQL API), account " + acct.ID
	}
	return "Azure Cosmos DB (NoSQL API)"
}

func (c *cosmosBackend) databases(ctx context.Context) ([]string, error) {
	if c.db != "" {
		return []string{c.db}, nil
	}
	var res struct {
		Databases []struct {
			ID string `json:"id"`
		} `json:"Databases"`
	}
	if _, err := c.do(ctx, "GET", "dbs", "", "dbs", nil, nil, &res); err != nil {
		return nil, err
	}
	var out []string
	for _, d := range res.Databases {
		out = append(out, d.ID)
	}
	sort.Strings(out)
	return out, nil
}

type cosmosContainer struct {
	ID           string          `json:"id"`
	PartitionKey json.RawMessage `json:"partitionKey"`
	Indexing     json.RawMessage `json:"indexingPolicy"`
	UniqueKeys   json.RawMessage `json:"uniqueKeyPolicy"`
	DefaultTTL   *int64          `json:"defaultTtl"`
	Conflict     json.RawMessage `json:"conflictResolutionPolicy"`
}

func (c *cosmosBackend) containers(ctx context.Context, db string) ([]cosmosContainer, error) {
	var res struct {
		Colls []cosmosContainer `json:"DocumentCollections"`
	}
	_, err := c.do(ctx, "GET", "colls", "dbs/"+db, "dbs/"+url.PathEscape(db)+"/colls", nil, nil, &res)
	return res.Colls, err
}

// cosmosScripts are a container's server-side scripts: REST path and the
// field of the list.
var cosmosScripts = []struct{ typ, label, res, field string }{
	{ObjProcedure, "Stored procedures", "sprocs", "StoredProcedures"},
	{ObjFunction, "Functions", "udfs", "UserDefinedFunctions"},
	{ObjTrigger, "Triggers", "triggers", "Triggers"},
}

func (c *cosmosBackend) Objects(ctx context.Context) ([]ObjectGroup, error) {
	dbs, err := c.databases(ctx)
	if err != nil {
		return nil, err
	}
	groups := []ObjectGroup{{Type: ObjTable, Label: "Containers", Queryable: true, Items: []string{}}}
	for _, s := range cosmosScripts {
		groups = append(groups, ObjectGroup{Type: s.typ, Label: s.label, Items: []string{}})
	}
	for _, db := range dbs {
		colls, err := c.containers(ctx, db)
		if err != nil {
			return nil, err
		}
		for _, coll := range colls {
			name := db + "." + coll.ID
			groups[0].Items = append(groups[0].Items, name)
			for i, s := range cosmosScripts {
				var res map[string][]struct {
					ID string `json:"id"`
				}
				link := "dbs/" + db + "/colls/" + coll.ID
				if _, err := c.do(ctx, "GET", s.res, link, escapeLink(link)+"/"+s.res, nil, nil, &res); err == nil {
					for _, x := range res[s.field] {
						groups[i+1].Items = append(groups[i+1].Items, name+"."+x.ID)
					}
				}
			}
		}
	}
	for i := range groups {
		sort.Strings(groups[i].Items)
	}
	return groups, nil
}

// escapeLink escapes each part of a resource link for the URL path.
func escapeLink(link string) string {
	parts := strings.Split(link, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// splitContainer splits database.container[.script].
func (c *cosmosBackend) splitContainer(name string) (db, coll, rest string) {
	db, rest, _ = strings.Cut(name, ".")
	coll, rest, _ = strings.Cut(rest, ".")
	return db, coll, rest
}

func (c *cosmosBackend) SelectFor(_ context.Context, name string) (string, error) {
	return "-- container: " + name + "\nSELECT * FROM c", nil
}

func (c *cosmosBackend) Count(ctx context.Context, name string) (int64, error) {
	db, coll, _ := c.splitContainer(name)
	link := "dbs/" + db + "/colls/" + coll
	h := http.Header{}
	h.Set("x-ms-documentdb-populatequotainfo", "true")
	got, err := c.do(ctx, "GET", "colls", link, escapeLink(link), h, nil, nil)
	if err != nil {
		return -1, err
	}
	for _, part := range strings.Split(got.Get("x-ms-resource-usage"), ";") {
		if k, v, ok := strings.Cut(part, "="); ok && k == "documentsCount" {
			return strconv.ParseInt(v, 10, 64)
		}
	}
	return -1, nil
}

func (c *cosmosBackend) Definition(ctx context.Context, typ, name string) (string, error) {
	db, coll, script := c.splitContainer(name)
	link := "dbs/" + db + "/colls/" + coll
	if typ != ObjTable {
		for _, s := range cosmosScripts {
			if s.typ != typ {
				continue
			}
			var res map[string]any
			slink := link + "/" + s.res + "/" + script
			if _, err := c.do(ctx, "GET", s.res, slink, escapeLink(slink), nil, nil, &res); err != nil {
				return "", err
			}
			out := fmt.Sprintf("// %s %s of container %s.%s\n", strings.TrimSuffix(strings.ToLower(s.label), "s"), script, db, coll)
			if t := res["triggerType"]; t != nil {
				out += fmt.Sprintf("// runs %v %v\n", t, res["triggerOperation"])
			}
			return out + fmt.Sprint(res["body"]) + "\n", nil
		}
		return "", fmt.Errorf("unsupported object type %q", typ)
	}
	var cont cosmosContainer
	if _, err := c.do(ctx, "GET", "colls", link, escapeLink(link), nil, nil, &cont); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "// container %s.%s\n", db, coll)
	if n, err := c.Count(ctx, name); err == nil && n >= 0 {
		fmt.Fprintf(&b, "// %d document(s)\n", n)
	}
	pretty := func(title string, raw json.RawMessage) {
		if len(raw) > 0 && string(raw) != "null" {
			v, _ := parseOrdered(raw)
			fmt.Fprintf(&b, "\n// %s\n%s\n", title, prettyJSON(v))
		}
	}
	pretty("partition key", cont.PartitionKey)
	if cont.DefaultTTL != nil {
		fmt.Fprintf(&b, "\n// default time to live: %d s\n", *cont.DefaultTTL)
	}
	pretty("unique keys", cont.UniqueKeys)
	pretty("indexing policy", cont.Indexing)
	if rs, err := c.Query(ctx, "-- container: "+name+"\nSELECT TOP 200 * FROM c"); err == nil {
		var sample []doc
		dr := rs.(*docRows)
		for _, d := range dr.buf {
			sample = append(sample, d)
		}
		rs.Close()
		b.WriteString("\n" + strings.ReplaceAll(inferFields(sample), "-- ", "// "))
	}
	return b.String(), nil
}

var (
	cosmosContainerLine = regexp.MustCompile(`(?im)^\s*--\s*container\s*:\s*(\S+)\s*$`)
	cosmosFrom          = regexp.MustCompile(`(?i)\bFROM\s+([A-Za-z_][\w-]*)`)
	cosmosWrites        = regexp.MustCompile(`(?i)\b(INSERT|UPDATE|DELETE|UPSERT|REPLACE|CREATE|DROP|ALTER)\b`)
	errCosmosReadOnly   = errors.New("cactusdb is read-only: only SELECT queries are allowed")
)

func (c *cosmosBackend) CheckReadOnly(q string) error {
	// The query API can't write; this only makes a mistake clear early.
	return checkWords(q, "'\"", []string{"SELECT"}, cosmosWrites, errCosmosReadOnly)
}

// target finds the container a query runs on.
func (c *cosmosBackend) target(q string) (db, coll string, err error) {
	if m := cosmosContainerLine.FindStringSubmatch(q); m != nil {
		name := m[1]
		if d, cl, ok := strings.Cut(name, "."); ok {
			return d, cl, nil
		}
		if c.db != "" {
			return c.db, name, nil
		}
		return "", "", fmt.Errorf("write the container as database.container: -- container: mydb.%s", name)
	}
	if m := cosmosFrom.FindStringSubmatch(stripLineComments(q)); m != nil && c.db != "" && !strings.EqualFold(m[1], "c") && !strings.EqualFold(m[1], "root") {
		return c.db, m[1], nil
	}
	return "", "", errors.New("name the container in a first line: -- container: database.container")
}

// cosmosSystemFields are left out of the rows.
var cosmosSystemFields = map[string]bool{"_rid": true, "_self": true, "_etag": true, "_attachments": true}

func (c *cosmosBackend) Query(ctx context.Context, q string) (resultSet, error) {
	db, coll, err := c.target(q)
	if err != nil {
		return nil, err
	}
	link := "dbs/" + db + "/colls/" + coll
	sqlText := strings.TrimRight(strings.TrimSpace(stripLineComments(q)), ";")
	var page []any
	continuation := ""
	first := true
	fetch := func() error {
		h := http.Header{}
		h.Set("x-ms-documentdb-isquery", "True")
		h.Set("Content-Type", "application/query+json")
		h.Set("x-ms-documentdb-query-enablecrosspartition", "True")
		h.Set("x-ms-max-item-count", "1000")
		if continuation != "" {
			h.Set("x-ms-continuation", continuation)
		}
		var res struct {
			Documents []json.RawMessage `json:"Documents"`
		}
		got, err := c.do(ctx, "POST", "docs", link, escapeLink(link)+"/docs", h, map[string]any{"query": sqlText, "parameters": []any{}}, &res)
		if err != nil {
			if strings.Contains(err.Error(), "cross partition") || strings.Contains(err.Error(), "query plan") {
				err = fmt.Errorf("%w\n\nWithout a partition key filter, Cosmos DB's gateway can't run ORDER BY, GROUP BY, DISTINCT, TOP, OFFSET or aggregates across partitions: add WHERE c.<partition key> = …", err)
			}
			return err
		}
		continuation = got.Get("x-ms-continuation")
		page = page[:0]
		for _, raw := range res.Documents {
			v, err := parseOrdered(raw)
			if err != nil {
				return err
			}
			page = append(page, v)
		}
		return nil
	}
	return newDocRows(func() (doc, error) {
		for len(page) == 0 {
			if !first && continuation == "" {
				return nil, io.EOF
			}
			first = false
			if err := fetch(); err != nil {
				return nil, err
			}
			if len(page) == 0 && continuation == "" {
				return nil, io.EOF
			}
		}
		v := page[0]
		page = page[1:]
		d, ok := v.(doc)
		if !ok {
			return doc{{"value", v}}, nil // SELECT VALUE …
		}
		out := d[:0:0]
		for _, f := range d {
			if !cosmosSystemFields[f.Key] {
				out = append(out, f)
			}
		}
		return out, nil
	}, nil, "id")
}
