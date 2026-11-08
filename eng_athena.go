package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Amazon Athena through its API (JSON, signed with AWS Signature Version 4):
// databases and tables of a data catalog, named database.table. Queries run
// in a workgroup and write their results to its S3 output location.

func init() { backendOpeners[EngineAthena] = openAthena }

type athenaBackend struct {
	c         *http.Client
	endpoint  string
	region    string
	keyID     string
	secret    string
	token     string
	catalog   string
	database  string // "" = every database of the catalog
	workgroup string
	output    string
}

func openAthena(ctx context.Context, p ConnParams, opts map[string]string) (backend, error) {
	region := strings.TrimSpace(p.Host)
	if region == "" {
		return nil, errors.New("enter the AWS region, e.g. eu-west-1")
	}
	if p.Username == "" || p.Password == "" {
		return nil, errors.New("enter the access key ID and the secret access key")
	}
	secret, token, _ := strings.Cut(strings.TrimSpace(p.Password), " ")
	a := &athenaBackend{c: apiClient(opts), endpoint: "https://athena." + region + ".amazonaws.com/", region: region,
		keyID: strings.TrimSpace(p.Username), secret: secret, token: strings.TrimSpace(token),
		catalog: cmpOr(opts["catalog"], "AwsDataCatalog"), database: strings.TrimSpace(p.Database),
		workgroup: cmpOr(opts["workgroup"], "primary"), output: opts["output"]}
	if e := opts["endpoint"]; e != "" {
		a.endpoint = strings.TrimRight(e, "/") + "/"
	}
	return a, nil
}

// call runs one Athena action, e.g. ListDatabases.
func (a *athenaBackend) call(ctx context.Context, action string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", a.endpoint, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AmazonAthena."+action)
	if a.token != "" {
		req.Header.Set("X-Amz-Security-Token", a.token)
	}
	signV4(req, body, a.keyID, a.secret, a.region, "athena", time.Now())
	resp, err := a.c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Message  string `json:"Message"`
			Message2 string `json:"message"`
			Type     string `json:"__type"`
		}
		if json.Unmarshal(b, &e) == nil && (e.Message != "" || e.Message2 != "") {
			typ := e.Type
			if i := strings.LastIndex(typ, "#"); i >= 0 {
				typ = typ[i+1:]
			}
			return fmt.Errorf("%s: %s", cmpOr(typ, resp.Status), cmpOr(e.Message, e.Message2))
		}
		return httpError(resp.Status, b)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

// signV4 signs req with AWS Signature Version 4.
func signV4(req *http.Request, body []byte, keyID, secret, region, service string, now time.Time) {
	t := now.UTC()
	amzDate := t.Format("20060102T150405Z")
	day := t.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	hash := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	mac := func(key []byte, s string) []byte {
		m := hmac.New(sha256.New, key)
		m.Write([]byte(s))
		return m.Sum(nil)
	}

	headers := map[string]string{"host": req.URL.Host}
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		if lk == "content-type" || strings.HasPrefix(lk, "x-amz-") {
			headers[lk] = strings.TrimSpace(strings.Join(v, ","))
		}
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, k := range names {
		canonHeaders.WriteString(k + ":" + headers[k] + "\n")
	}
	signed := strings.Join(names, ";")
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonical := req.Method + "\n" + path + "\n" + req.URL.Query().Encode() + "\n" + canonHeaders.String() + "\n" + signed + "\n" + hash(body)
	scope := day + "/" + region + "/" + service + "/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hash([]byte(canonical))
	key := mac(mac(mac(mac([]byte("AWS4"+secret), day), region), service), "aws4_request")
	sig := hex.EncodeToString(mac(key, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+keyID+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

func (a *athenaBackend) Ping(ctx context.Context) error {
	return a.call(ctx, "GetWorkGroup", map[string]string{"WorkGroup": a.workgroup}, nil)
}
func (a *athenaBackend) Close() error { return nil }

func (a *athenaBackend) Version(ctx context.Context) string {
	var wg struct {
		WorkGroup struct {
			Configuration struct {
				EngineVersion struct {
					Effective string `json:"EffectiveEngineVersion"`
				} `json:"EngineVersion"`
			} `json:"Configuration"`
		} `json:"WorkGroup"`
	}
	a.call(ctx, "GetWorkGroup", map[string]string{"WorkGroup": a.workgroup}, &wg)
	v := "Amazon Athena, workgroup " + a.workgroup
	if e := wg.WorkGroup.Configuration.EngineVersion.Effective; e != "" {
		v += " (" + e + ")"
	}
	return v
}

func (a *athenaBackend) databases(ctx context.Context) ([]string, error) {
	if a.database != "" {
		return []string{a.database}, nil
	}
	var out []string
	next := ""
	for {
		in := map[string]any{"CatalogName": a.catalog, "MaxResults": 50}
		if next != "" {
			in["NextToken"] = next
		}
		var res struct {
			DatabaseList []struct{ Name string } `json:"DatabaseList"`
			NextToken    string                  `json:"NextToken"`
		}
		if err := a.call(ctx, "ListDatabases", in, &res); err != nil {
			return nil, err
		}
		for _, d := range res.DatabaseList {
			out = append(out, d.Name)
		}
		if next = res.NextToken; next == "" {
			break
		}
	}
	sort.Strings(out)
	return out, nil
}

type athenaColumn struct {
	Name    string `json:"Name"`
	Type    string `json:"Type"`
	Comment string `json:"Comment"`
}

type athenaTable struct {
	Name          string            `json:"Name"`
	TableType     string            `json:"TableType"`
	Columns       []athenaColumn    `json:"Columns"`
	PartitionKeys []athenaColumn    `json:"PartitionKeys"`
	Parameters    map[string]string `json:"Parameters"`
}

func (a *athenaBackend) Objects(ctx context.Context) ([]ObjectGroup, error) {
	dbs, err := a.databases(ctx)
	if err != nil {
		return nil, err
	}
	tables := ObjectGroup{Type: ObjTable, Label: "Tables", Queryable: true, Items: []string{}}
	views := ObjectGroup{Type: ObjView, Label: "Views", Queryable: true, Items: []string{}}
	for _, db := range dbs {
		next := ""
		for {
			in := map[string]any{"CatalogName": a.catalog, "DatabaseName": db, "MaxResults": 50}
			if next != "" {
				in["NextToken"] = next
			}
			var res struct {
				List []athenaTable `json:"TableMetadataList"`
				Next string        `json:"NextToken"`
			}
			if err := a.call(ctx, "ListTableMetadata", in, &res); err != nil {
				return nil, err
			}
			for _, t := range res.List {
				if t.TableType == "VIRTUAL_VIEW" {
					views.Items = append(views.Items, db+"."+t.Name)
				} else {
					tables.Items = append(tables.Items, db+"."+t.Name)
				}
			}
			if next = res.Next; next == "" {
				break
			}
		}
	}
	sort.Strings(tables.Items)
	sort.Strings(views.Items)
	return []ObjectGroup{tables, views}, nil
}

func athenaName(name string) string {
	db, t, _ := strings.Cut(name, ".")
	return `"` + strings.ReplaceAll(db, `"`, `""`) + `"."` + strings.ReplaceAll(t, `"`, `""`) + `"`
}

func (a *athenaBackend) SelectFor(_ context.Context, name string) (string, error) {
	return "SELECT *\nFROM " + athenaName(name), nil
}

// Count is unknown: Athena bills the data a COUNT(*) scans.
func (a *athenaBackend) Count(context.Context, string) (int64, error) { return -1, nil }

func (a *athenaBackend) Definition(ctx context.Context, typ, name string) (string, error) {
	db, t, _ := strings.Cut(name, ".")
	if typ == ObjView {
		rs, err := a.Query(ctx, "SHOW CREATE VIEW "+athenaName(name))
		if err != nil {
			return "", err
		}
		defer rs.Close()
		var b strings.Builder
		for {
			row, err := rs.Next()
			if err != nil {
				break
			}
			if len(row) > 0 {
				b.WriteString(fmt.Sprint(row[0]) + "\n")
			}
		}
		return b.String(), nil
	}
	var res struct {
		Table athenaTable `json:"TableMetadata"`
	}
	if err := a.call(ctx, "GetTableMetadata", map[string]string{"CatalogName": a.catalog, "DatabaseName": db, "TableName": t}, &res); err != nil {
		return "", err
	}
	tab := res.Table
	cols := func(list []athenaColumn) string {
		lines := make([]string, len(list))
		for i, c := range list {
			lines[i] = "  `" + c.Name + "` " + c.Type
			if c.Comment != "" {
				lines[i] += " COMMENT '" + strings.ReplaceAll(c.Comment, "'", "\\'") + "'"
			}
		}
		return strings.Join(lines, ",\n")
	}
	out := "CREATE EXTERNAL TABLE `" + db + "`.`" + t + "` (\n" + cols(tab.Columns) + "\n)"
	if len(tab.PartitionKeys) > 0 {
		out += "\nPARTITIONED BY (\n" + cols(tab.PartitionKeys) + "\n)"
	}
	p := tab.Parameters
	if s := p["serde.serialization.lib"]; s != "" {
		out += "\nROW FORMAT SERDE '" + s + "'"
	}
	if in := p["inputformat"]; in != "" {
		out += "\nSTORED AS INPUTFORMAT '" + in + "'\n  OUTPUTFORMAT '" + p["outputformat"] + "'"
	}
	if l := p["location"]; l != "" {
		out += "\nLOCATION '" + l + "'"
	}
	var props []string
	for k, v := range p {
		switch k {
		case "location", "inputformat", "outputformat", "serde.serialization.lib":
			continue
		}
		props = append(props, "  '"+k+"'='"+strings.ReplaceAll(v, "'", "\\'")+"'")
	}
	sort.Strings(props)
	if len(props) > 0 {
		out += "\nTBLPROPERTIES (\n" + strings.Join(props, ",\n") + "\n)"
	}
	return out + ";\n", nil
}

func (a *athenaBackend) CheckReadOnly(q string) error { return checkSQL("", q) }

func (a *athenaBackend) Query(ctx context.Context, q string) (resultSet, error) {
	in := map[string]any{
		"QueryString":           strings.TrimRight(strings.TrimSpace(q), ";"),
		"QueryExecutionContext": map[string]string{"Catalog": a.catalog},
		"WorkGroup":             a.workgroup,
	}
	if a.database != "" {
		in["QueryExecutionContext"].(map[string]string)["Database"] = a.database
	}
	if a.output != "" {
		in["ResultConfiguration"] = map[string]string{"OutputLocation": a.output}
	}
	var started struct {
		ID string `json:"QueryExecutionId"`
	}
	if err := a.call(ctx, "StartQueryExecution", in, &started); err != nil {
		return nil, err
	}
	stop := func() {
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		a.call(sctx, "StopQueryExecution", map[string]string{"QueryExecutionId": started.ID}, nil)
	}
	wait := 300 * time.Millisecond
	var stmtType string
	for {
		var ex struct {
			QueryExecution struct {
				StatementType string `json:"StatementType"`
				Status        struct {
					State  string `json:"State"`
					Reason string `json:"StateChangeReason"`
				} `json:"Status"`
			} `json:"QueryExecution"`
		}
		if err := a.call(ctx, "GetQueryExecution", map[string]string{"QueryExecutionId": started.ID}, &ex); err != nil {
			if ctx.Err() != nil {
				stop()
			}
			return nil, err
		}
		st := ex.QueryExecution.Status
		stmtType = ex.QueryExecution.StatementType
		if st.State == "SUCCEEDED" {
			break
		}
		if st.State == "FAILED" || st.State == "CANCELLED" {
			return nil, fmt.Errorf("the query %s: %s", strings.ToLower(st.State), st.Reason)
		}
		select {
		case <-ctx.Done():
			stop()
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		wait = min(wait*2, 3*time.Second)
	}
	r := &athenaRows{a: a, ctx: ctx, id: started.ID, header: stmtType == "DML"}
	if err := r.fetch(); err != nil {
		return nil, err
	}
	return r, nil
}

type athenaRows struct {
	a      *athenaBackend
	ctx    context.Context
	id     string
	next   string
	header bool // the first row of a SELECT repeats the column names
	first  bool
	rows   [][]*string
	cols   []string
	types  []string
	done   bool
}

func (r *athenaRows) fetch() error {
	in := map[string]any{"QueryExecutionId": r.id, "MaxResults": 1000}
	if r.next != "" {
		in["NextToken"] = r.next
	}
	var res struct {
		ResultSet struct {
			Rows []struct {
				Data []struct {
					V *string `json:"VarCharValue"`
				} `json:"Data"`
			} `json:"Rows"`
			Meta struct {
				Columns []struct {
					Name string `json:"Name"`
					Type string `json:"Type"`
				} `json:"ColumnInfo"`
			} `json:"ResultSetMetadata"`
		} `json:"ResultSet"`
		NextToken string `json:"NextToken"`
	}
	if err := r.a.call(r.ctx, "GetQueryResults", in, &res); err != nil {
		return err
	}
	if r.cols == nil {
		for _, c := range res.ResultSet.Meta.Columns {
			r.cols = append(r.cols, c.Name)
			r.types = append(r.types, strings.ToUpper(c.Type))
		}
		r.first = true
	}
	r.rows = r.rows[:0]
	for _, row := range res.ResultSet.Rows {
		vals := make([]*string, len(row.Data))
		for i, d := range row.Data {
			vals[i] = d.V
		}
		r.rows = append(r.rows, vals)
	}
	if r.first && r.header && len(r.rows) > 0 {
		r.rows = r.rows[1:]
	}
	r.first = false
	r.next = res.NextToken
	r.done = r.next == ""
	return nil
}

func (r *athenaRows) Columns() []string { return r.cols }
func (r *athenaRows) Types() []string   { return r.types }
func (r *athenaRows) Close() error      { return nil }
func (r *athenaRows) Next() ([]any, error) {
	for len(r.rows) == 0 {
		if r.done {
			return nil, io.EOF
		}
		if err := r.fetch(); err != nil {
			return nil, err
		}
	}
	vals := r.rows[0]
	r.rows = r.rows[1:]
	out := make([]any, len(r.cols))
	for i := range out {
		if i >= len(vals) || vals[i] == nil {
			continue
		}
		out[i] = athenaValue(r.types[i], *vals[i])
	}
	return out, nil
}

func athenaValue(typ, s string) any {
	switch typ {
	case "TINYINT", "SMALLINT", "INTEGER", "INT", "BIGINT":
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
	case "DOUBLE", "FLOAT", "REAL":
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
	case "BOOLEAN":
		return s == "true"
	}
	if strings.HasPrefix(typ, "DECIMAL") && plainNumber.MatchString(s) {
		return json.Number(s)
	}
	return s
}
