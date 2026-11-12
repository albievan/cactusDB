package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Google BigQuery through its REST API, signed in with a service account
// key (or a gcloud "authorized_user" file). Tables, views, materialized
// views and routines are named dataset.name. Every query is first checked
// with a dry run, which reports the statement type: only SELECT runs.

func init() { backendOpeners[EngineBigQuery] = openBigQuery }

type bigqueryBackend struct {
	c        *http.Client
	api      string // …/bigquery/v2
	project  string
	dataset  string // "" = every dataset
	location string
	maxBytes string
	auth     *googleAuth // nil: no sign-in (an emulator)
}

func openBigQuery(ctx context.Context, p ConnParams, opts map[string]string) (backend, error) {
	project := strings.TrimSpace(p.Host)
	if project == "" {
		return nil, errors.New("enter the project ID")
	}
	b := &bigqueryBackend{c: apiClient(opts), api: "https://bigquery.googleapis.com/bigquery/v2", project: project,
		dataset: strings.TrimSpace(p.Database), location: opts["location"], maxBytes: opts["maxbytes"]}
	if e := opts["endpoint"]; e != "" {
		b.api = strings.TrimRight(e, "/") + "/bigquery/v2"
	}
	key := strings.TrimSpace(p.Password)
	if key == "" && opts["endpoint"] == "" {
		return nil, errors.New("paste the service account key (JSON) into the key field, or give the path of the key file on the server")
	}
	if key != "" {
		// A Google sign-in is only ever sent to Google: with the key of a
		// file on the server, another endpoint would receive a token made
		// from the server's own credentials.
		if e := opts["endpoint"]; e != "" {
			u, err := url.Parse(strings.TrimSpace(e))
			if err != nil || u.Scheme != "https" || !strings.HasSuffix(strings.ToLower(u.Hostname()), ".googleapis.com") {
				return nil, errors.New("a key is only sent to Google (https://….googleapis.com): leave the key empty for an emulator endpoint")
			}
		}
		if !strings.HasPrefix(key, "{") {
			f, err := os.Open(key)
			if err != nil {
				return nil, fmt.Errorf("the key isn't JSON and can't be read as a file: %w", err)
			}
			b2, err := io.ReadAll(io.LimitReader(f, 1<<20)) // a key file is a few kB
			f.Close()
			if err != nil {
				return nil, fmt.Errorf("the key isn't JSON and can't be read as a file: %w", err)
			}
			key = string(b2)
		}
		a, err := newGoogleAuth([]byte(key), "https://www.googleapis.com/auth/bigquery")
		if err != nil {
			return nil, err
		}
		a.c = b.c
		b.auth = a
	}
	return b, nil
}

// ---- Google sign-in ------------------------------------------------------------------

// googleAuth gets OAuth access tokens for a service account (a signed JWT)
// or a user's refresh token (gcloud auth application-default login).
type googleAuth struct {
	c        *http.Client
	email    string
	key      *rsa.PrivateKey
	tokenURL string
	scope    string
	// authorized_user
	clientID, clientSecret, refresh string

	mu      sync.Mutex
	token   string
	expires time.Time
}

func newGoogleAuth(j []byte, scope string) (*googleAuth, error) {
	var k struct {
		Type         string `json:"type"`
		ClientEmail  string `json:"client_email"`
		PrivateKey   string `json:"private_key"`
		TokenURI     string `json:"token_uri"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(j, &k); err != nil {
		return nil, fmt.Errorf("the key isn't valid JSON: %w", err)
	}
	a := &googleAuth{tokenURL: k.TokenURI, scope: scope}
	if a.tokenURL == "" {
		a.tokenURL = "https://oauth2.googleapis.com/token"
	}
	switch k.Type {
	case "service_account":
		block, _ := pem.Decode([]byte(k.PrivateKey))
		if block == nil {
			return nil, errors.New("the key's private_key isn't a PEM key")
		}
		pk, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			if pk1, err1 := x509.ParsePKCS1PrivateKey(block.Bytes); err1 == nil {
				pk, err = pk1, nil
			} else {
				return nil, fmt.Errorf("the key's private_key can't be read: %w", err)
			}
		}
		rk, ok := pk.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("the key's private_key isn't an RSA key")
		}
		a.email, a.key = k.ClientEmail, rk
	case "authorized_user":
		a.clientID, a.clientSecret, a.refresh = k.ClientID, k.ClientSecret, k.RefreshToken
	default:
		return nil, fmt.Errorf("the key is of type %q: use a service account key (or a gcloud application-default credentials file)", k.Type)
	}
	return a, nil
}

func (a *googleAuth) header(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && time.Until(a.expires) > time.Minute {
		return "Bearer " + a.token, nil
	}
	form := url.Values{}
	if a.key != nil {
		jwt, err := a.jwt()
		if err != nil {
			return "", err
		}
		form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
		form.Set("assertion", jwt)
	} else {
		form.Set("grant_type", "refresh_token")
		form.Set("client_id", a.clientID)
		form.Set("client_secret", a.clientSecret)
		form.Set("refresh_token", a.refresh)
	}
	h := http.Header{}
	h.Set("Content-Type", "application/x-www-form-urlencoded")
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if _, err := doJSON(ctx, a.c, "POST", a.tokenURL, h, form.Encode(), &tok); err != nil {
		return "", fmt.Errorf("signing in to Google failed: %w", err)
	}
	a.token, a.expires = tok.AccessToken, time.Now().Add(time.Duration(tok.ExpiresIn)*time.Second)
	return "Bearer " + a.token, nil
}

// jwt is the service account's signed assertion (RS256).
func (a *googleAuth) jwt() (string, error) {
	enc := base64.RawURLEncoding
	head := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	now := time.Now().Unix()
	claims, _ := json.Marshal(map[string]any{"iss": a.email, "scope": a.scope, "aud": a.tokenURL, "iat": now, "exp": now + 3600})
	body := head + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(body))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return body + "." + enc.EncodeToString(sig), nil
}

// ---- the API -------------------------------------------------------------------------

func (b *bigqueryBackend) do(ctx context.Context, method, path string, body, out any) error {
	h := http.Header{}
	if b.auth != nil {
		v, err := b.auth.header(ctx)
		if err != nil {
			return err
		}
		h.Set("Authorization", v)
	}
	_, err := doJSON(ctx, b.c, method, b.api+path, h, body, out)
	return err
}

func (b *bigqueryBackend) projectPath() string { return "/projects/" + url.PathEscape(b.project) }

func (b *bigqueryBackend) Ping(ctx context.Context) error {
	if b.dataset != "" {
		return b.do(ctx, "GET", b.projectPath()+"/datasets/"+url.PathEscape(b.dataset), nil, nil)
	}
	return b.do(ctx, "GET", b.projectPath()+"/datasets?maxResults=1", nil, nil)
}
func (b *bigqueryBackend) Close() error { return nil }

func (b *bigqueryBackend) Version(context.Context) string {
	return "Google BigQuery, project " + b.project
}

func (b *bigqueryBackend) datasets(ctx context.Context) ([]string, error) {
	if b.dataset != "" {
		return []string{b.dataset}, nil
	}
	var out []string
	token := ""
	for {
		var res struct {
			Datasets []struct {
				Ref struct {
					DatasetID string `json:"datasetId"`
				} `json:"datasetReference"`
			} `json:"datasets"`
			Next string `json:"nextPageToken"`
		}
		path := b.projectPath() + "/datasets?maxResults=1000"
		if token != "" {
			path += "&pageToken=" + url.QueryEscape(token)
		}
		if err := b.do(ctx, "GET", path, nil, &res); err != nil {
			return nil, err
		}
		for _, d := range res.Datasets {
			out = append(out, d.Ref.DatasetID)
		}
		if token = res.Next; token == "" {
			break
		}
	}
	sort.Strings(out)
	return out, nil
}

func (b *bigqueryBackend) Objects(ctx context.Context) ([]ObjectGroup, error) {
	dss, err := b.datasets(ctx)
	if err != nil {
		return nil, err
	}
	g := []ObjectGroup{
		{Type: ObjTable, Label: "Tables", Queryable: true, Items: []string{}},
		{Type: ObjView, Label: "Views", Queryable: true, Items: []string{}},
		{Type: ObjMatView, Label: "Materialized views", Queryable: true, Items: []string{}},
		{Type: ObjFunction, Label: "Functions", Items: []string{}},
		{Type: ObjProcedure, Label: "Procedures", Items: []string{}},
	}
	for _, ds := range dss {
		token := ""
		for {
			var res struct {
				Tables []struct {
					Ref struct {
						TableID string `json:"tableId"`
					} `json:"tableReference"`
					Type string `json:"type"`
				} `json:"tables"`
				Next string `json:"nextPageToken"`
			}
			path := b.projectPath() + "/datasets/" + url.PathEscape(ds) + "/tables?maxResults=1000"
			if token != "" {
				path += "&pageToken=" + url.QueryEscape(token)
			}
			if err := b.do(ctx, "GET", path, nil, &res); err != nil {
				return nil, err
			}
			for _, t := range res.Tables {
				i := 0
				switch t.Type {
				case "VIEW":
					i = 1
				case "MATERIALIZED_VIEW":
					i = 2
				}
				g[i].Items = append(g[i].Items, ds+"."+t.Ref.TableID)
			}
			if token = res.Next; token == "" {
				break
			}
		}
		var rr struct {
			Routines []struct {
				Ref struct {
					RoutineID string `json:"routineId"`
				} `json:"routineReference"`
				Type string `json:"routineType"`
			} `json:"routines"`
		}
		if b.do(ctx, "GET", b.projectPath()+"/datasets/"+url.PathEscape(ds)+"/routines?maxResults=1000", nil, &rr) == nil {
			for _, r := range rr.Routines {
				i := 3
				if r.Type == "PROCEDURE" {
					i = 4
				}
				g[i].Items = append(g[i].Items, ds+"."+r.Ref.RoutineID)
			}
		}
	}
	for i := range g {
		sort.Strings(g[i].Items)
	}
	return g, nil
}

// bqName quotes project.dataset.name with backticks.
func (b *bigqueryBackend) bqName(name string) string {
	return "`" + b.project + "." + name + "`"
}

func (b *bigqueryBackend) SelectFor(_ context.Context, name string) (string, error) {
	return "SELECT *\nFROM " + b.bqName(name), nil
}

type bqField struct {
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	Mode        string    `json:"mode"`
	Description string    `json:"description"`
	Fields      []bqField `json:"fields"`
}

type bqTable struct {
	Type    string `json:"type"`
	NumRows string `json:"numRows"`
	Schema  struct {
		Fields []bqField `json:"fields"`
	} `json:"schema"`
	View struct {
		Query string `json:"query"`
	} `json:"view"`
	MatView struct {
		Query string `json:"query"`
	} `json:"materializedView"`
	Partitioning *struct {
		Type  string `json:"type"`
		Field string `json:"field"`
	} `json:"timePartitioning"`
	Clustering *struct {
		Fields []string `json:"fields"`
	} `json:"clustering"`
	Description string `json:"description"`
}

func (b *bigqueryBackend) table(ctx context.Context, name string) (*bqTable, error) {
	ds, t, _ := strings.Cut(name, ".")
	var tab bqTable
	err := b.do(ctx, "GET", b.projectPath()+"/datasets/"+url.PathEscape(ds)+"/tables/"+url.PathEscape(t), nil, &tab)
	return &tab, err
}

func (b *bigqueryBackend) Count(ctx context.Context, name string) (int64, error) {
	t, err := b.table(ctx, name)
	if err != nil || t.Type != "TABLE" {
		return -1, err
	}
	return strconv.ParseInt(t.NumRows, 10, 64)
}

// bqType writes a column type: STRUCT<…> and ARRAY<…> included.
func bqType(f bqField) string {
	t := f.Type
	switch t {
	case "RECORD", "STRUCT":
		parts := make([]string, len(f.Fields))
		for i, sub := range f.Fields {
			parts[i] = "`" + sub.Name + "` " + bqType(sub)
		}
		t = "STRUCT<" + strings.Join(parts, ", ") + ">"
	case "INTEGER":
		t = "INT64"
	case "FLOAT":
		t = "FLOAT64"
	case "BOOLEAN":
		t = "BOOL"
	}
	if f.Mode == "REPEATED" {
		return "ARRAY<" + t + ">"
	}
	if f.Mode == "REQUIRED" {
		return t + " NOT NULL"
	}
	return t
}

func (b *bigqueryBackend) Definition(ctx context.Context, typ, name string) (string, error) {
	if typ == ObjFunction || typ == ObjProcedure {
		ds, r, _ := strings.Cut(name, ".")
		var rt struct {
			Type      string `json:"routineType"`
			Language  string `json:"language"`
			Body      string `json:"definitionBody"`
			Arguments []struct {
				Name     string          `json:"name"`
				DataType json.RawMessage `json:"dataType"`
			} `json:"arguments"`
			ReturnType json.RawMessage `json:"returnType"`
		}
		if err := b.do(ctx, "GET", b.projectPath()+"/datasets/"+url.PathEscape(ds)+"/routines/"+url.PathEscape(r), nil, &rt); err != nil {
			return "", err
		}
		var args []string
		for _, a := range rt.Arguments {
			var dt struct {
				TypeKind string `json:"typeKind"`
			}
			json.Unmarshal(a.DataType, &dt)
			args = append(args, a.Name+" "+cmpOr(dt.TypeKind, "ANY TYPE"))
		}
		kind := "FUNCTION"
		if rt.Type == "PROCEDURE" {
			kind = "PROCEDURE"
		} else if rt.Type == "TABLE_VALUED_FUNCTION" {
			kind = "TABLE FUNCTION"
		}
		out := fmt.Sprintf("CREATE OR REPLACE %s %s(%s)", kind, b.bqName(name), strings.Join(args, ", "))
		if len(rt.ReturnType) > 0 {
			var dt struct {
				TypeKind string `json:"typeKind"`
			}
			json.Unmarshal(rt.ReturnType, &dt)
			if dt.TypeKind != "" {
				out += " RETURNS " + dt.TypeKind
			}
		}
		if rt.Language != "" && rt.Language != "SQL" {
			out += " LANGUAGE " + strings.ToLower(rt.Language) + " AS r\"\"\"" + rt.Body + "\"\"\";\n"
		} else if kind == "PROCEDURE" {
			out += "\n" + rt.Body + ";\n"
		} else {
			out += " AS (\n" + rt.Body + "\n);\n"
		}
		return out, nil
	}
	t, err := b.table(ctx, name)
	if err != nil {
		return "", err
	}
	switch t.Type {
	case "VIEW":
		return "CREATE OR REPLACE VIEW " + b.bqName(name) + " AS\n" + strings.TrimSpace(t.View.Query) + ";\n", nil
	case "MATERIALIZED_VIEW":
		return "CREATE MATERIALIZED VIEW " + b.bqName(name) + " AS\n" + strings.TrimSpace(t.MatView.Query) + ";\n", nil
	}
	var lines []string
	for _, f := range t.Schema.Fields {
		line := "  `" + f.Name + "` " + bqType(f)
		if f.Description != "" {
			line += " OPTIONS(description=" + strconv.Quote(f.Description) + ")"
		}
		lines = append(lines, line)
	}
	kind := "TABLE"
	if t.Type == "EXTERNAL" {
		kind = "EXTERNAL TABLE"
	}
	out := "CREATE " + kind + " " + b.bqName(name) + " (\n" + strings.Join(lines, ",\n") + "\n)"
	if p := t.Partitioning; p != nil {
		if p.Field != "" {
			out += "\nPARTITION BY " + map[string]string{"DAY": "DATE", "HOUR": "TIMESTAMP_TRUNC", "MONTH": "DATE_TRUNC", "YEAR": "DATE_TRUNC"}[p.Type]
			if p.Type == "DAY" {
				out += "(`" + p.Field + "`)"
			} else {
				out += "(`" + p.Field + "`, " + p.Type + ")"
			}
		} else {
			out += "\nPARTITION BY _PARTITIONDATE"
		}
	}
	if c := t.Clustering; c != nil && len(c.Fields) > 0 {
		out += "\nCLUSTER BY " + strings.Join(c.Fields, ", ")
	}
	if t.Description != "" {
		out += "\nOPTIONS(description=" + strconv.Quote(t.Description) + ")"
	}
	out += ";\n"
	if t.NumRows != "" {
		out += "\n-- " + t.NumRows + " row(s)\n"
	}
	return out, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (b *bigqueryBackend) CheckReadOnly(q string) error { return checkSQL(EngineMySQL, q) }

// queryRequest is the configuration of a query job.
func (b *bigqueryBackend) queryConfig(q string) map[string]any {
	cfg := map[string]any{"query": q, "useLegacySql": false}
	if b.dataset != "" {
		cfg["defaultDataset"] = map[string]string{"projectId": b.project, "datasetId": b.dataset}
	}
	if b.maxBytes != "" {
		cfg["maximumBytesBilled"] = b.maxBytes
	}
	return cfg
}

func (b *bigqueryBackend) Query(ctx context.Context, q string) (resultSet, error) {
	q = strings.TrimRight(strings.TrimSpace(q), ";")
	// The dry run: what kind of statement, without running it.
	job := map[string]any{"configuration": map[string]any{"query": b.queryConfig(q), "dryRun": true}}
	if b.location != "" {
		job["jobReference"] = map[string]string{"location": b.location}
	}
	var dry struct {
		Statistics struct {
			Query struct {
				StatementType string `json:"statementType"`
			} `json:"query"`
		} `json:"statistics"`
	}
	if err := b.do(ctx, "POST", b.projectPath()+"/jobs", job, &dry); err != nil {
		return nil, err
	}
	if st := dry.Statistics.Query.StatementType; st != "" && st != "SELECT" {
		return nil, fmt.Errorf("cactusdb is read-only: BigQuery reports this is a %s statement; only SELECT runs", st)
	}
	req := b.queryConfig(q)
	req["maxResults"] = 1000
	req["timeoutMs"] = 10000
	req["formatOptions"] = map[string]bool{"useInt64Timestamp": true}
	if b.location != "" {
		req["location"] = b.location
	}
	var res bqResults
	if err := b.do(ctx, "POST", b.projectPath()+"/queries", req, &res); err != nil {
		return nil, err
	}
	r := &bqRows{b: b, ctx: ctx, res: res}
	for !r.res.JobComplete {
		if err := r.fetch(); err != nil {
			return nil, err
		}
	}
	r.setColumns()
	return r, nil
}

type bqResults struct {
	JobReference struct {
		JobID    string `json:"jobId"`
		Location string `json:"location"`
	} `json:"jobReference"`
	JobComplete bool `json:"jobComplete"`
	Schema      struct {
		Fields []bqField `json:"fields"`
	} `json:"schema"`
	Rows      []json.RawMessage `json:"rows"`
	PageToken string            `json:"pageToken"`
}

type bqRows struct {
	b     *bigqueryBackend
	ctx   context.Context
	res   bqResults
	cols  []string
	types []string
}

// fetch reads the next page of results (or waits for the job).
func (r *bqRows) fetch() error {
	v := url.Values{}
	v.Set("maxResults", "1000")
	v.Set("timeoutMs", "10000")
	v.Set("formatOptions.useInt64Timestamp", "true")
	if r.res.JobReference.Location != "" {
		v.Set("location", r.res.JobReference.Location)
	}
	if r.res.PageToken != "" {
		v.Set("pageToken", r.res.PageToken)
	}
	var next bqResults
	if err := r.b.do(r.ctx, "GET", r.b.projectPath()+"/queries/"+url.PathEscape(r.res.JobReference.JobID)+"?"+v.Encode(), nil, &next); err != nil {
		return err
	}
	if next.JobReference.JobID == "" {
		next.JobReference = r.res.JobReference
	}
	r.res = next
	return nil
}

func (r *bqRows) setColumns() {
	for _, f := range r.res.Schema.Fields {
		r.cols = append(r.cols, f.Name)
		t := f.Type
		switch {
		case f.Mode == "REPEATED", t == "RECORD", t == "STRUCT":
			t = "JSON"
		case t == "NUMERIC", t == "BIGNUMERIC":
			t = "DECIMAL"
		}
		r.types = append(r.types, t)
	}
}

func (r *bqRows) Columns() []string { return r.cols }
func (r *bqRows) Types() []string   { return r.types }
func (r *bqRows) Close() error      { return nil }
func (r *bqRows) Next() ([]any, error) {
	for len(r.res.Rows) == 0 {
		if r.res.PageToken == "" {
			return nil, io.EOF
		}
		if err := r.fetch(); err != nil {
			return nil, err
		}
	}
	raw := r.res.Rows[0]
	r.res.Rows = r.res.Rows[1:]
	var row struct {
		F []struct {
			V json.RawMessage `json:"v"`
		} `json:"f"`
	}
	if err := json.Unmarshal(raw, &row); err != nil {
		return nil, err
	}
	out := make([]any, len(r.cols))
	for i, f := range r.res.Schema.Fields {
		if i < len(row.F) {
			out[i] = docCell(bqValue(f, row.F[i].V))
		}
	}
	return out, nil
}

// bqValue converts a value of the REST format ({"v": …}) by its field.
func bqValue(f bqField, raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if f.Mode == "REPEATED" {
		var items []struct {
			V json.RawMessage `json:"v"`
		}
		json.Unmarshal(raw, &items)
		out := make([]any, len(items))
		one := f
		one.Mode = ""
		for i, it := range items {
			out[i] = bqValue(one, it.V)
		}
		return out
	}
	if f.Type == "RECORD" || f.Type == "STRUCT" {
		var rec struct {
			F []struct {
				V json.RawMessage `json:"v"`
			} `json:"f"`
		}
		json.Unmarshal(raw, &rec)
		d := doc{}
		for i, sub := range f.Fields {
			if i < len(rec.F) {
				d = append(d, field{sub.Name, bqValue(sub, rec.F[i].V)})
			}
		}
		return d
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return string(raw)
	}
	switch f.Type {
	case "INTEGER", "INT64":
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
	case "FLOAT", "FLOAT64":
		if x, err := strconv.ParseFloat(s, 64); err == nil {
			return x
		}
	case "NUMERIC", "BIGNUMERIC":
		return json.Number(s)
	case "BOOLEAN", "BOOL":
		return s == "true"
	case "TIMESTAMP":
		if us, err := strconv.ParseInt(s, 10, 64); err == nil {
			return time.UnixMicro(us).UTC()
		}
	case "JSON":
		if v, err := parseOrdered([]byte(s)); err == nil {
			return v
		}
	}
	return s
}
