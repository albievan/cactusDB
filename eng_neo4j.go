package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j/config"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j/dbtype"
)

// Neo4j: node labels (:Person) and relationship types [:KNOWS] are the
// "tables"; opening one returns its nodes or relationships with one column
// per property. Queries are Cypher, run in READ transactions, which the
// server refuses to write in.

func init() { backendOpeners[EngineNeo4j] = openNeo4j }

type neo4jBackend struct {
	d  neo4j.DriverWithContext
	db string
}

func openNeo4j(ctx context.Context, p ConnParams, opts map[string]string) (backend, error) {
	uri := strings.TrimSpace(p.Host)
	if !strings.Contains(uri, "://") {
		scheme := "neo4j"
		if optBool(opts, "tls", false) || optBool(opts, "ssl", false) {
			scheme = "neo4j+s"
		}
		uri = scheme + "://" + hostPort(uri, p.Port, 7687)
	}
	d, err := neo4j.NewDriverWithContext(uri, neo4j.BasicAuth(p.Username, p.Password, ""), func(c *config.Config) {
		c.SocketConnectTimeout = 10 * time.Second
		c.ConnectionAcquisitionTimeout = 30 * time.Second
		c.UserAgent = "cactusdb/" + version
	})
	if err != nil {
		return nil, err
	}
	return &neo4jBackend{d: d, db: strings.TrimSpace(p.Database)}, nil
}

func (n *neo4jBackend) Ping(ctx context.Context) error { return n.d.VerifyConnectivity(ctx) }
func (n *neo4jBackend) Close() error                   { return n.d.Close(context.Background()) }

// read runs cypher in a read transaction and returns every record.
func (n *neo4jBackend) read(ctx context.Context, cypher string, params map[string]any) ([]*neo4j.Record, error) {
	res, err := neo4j.ExecuteQuery(ctx, n.d, cypher, params, neo4j.EagerResultTransformer,
		neo4j.ExecuteQueryWithDatabase(n.db), neo4j.ExecuteQueryWithReadersRouting())
	if err != nil {
		return nil, err
	}
	return res.Records, nil
}

func (n *neo4jBackend) strings(ctx context.Context, cypher string) ([]string, error) {
	recs, err := n.read(ctx, cypher, nil)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, r := range recs {
		if len(r.Values) > 0 {
			out = append(out, fmt.Sprint(r.Values[0]))
		}
	}
	return out, nil
}

func (n *neo4jBackend) Version(ctx context.Context) string {
	recs, err := n.read(ctx, `CALL dbms.components() YIELD name, versions, edition RETURN name, versions[0], edition`, nil)
	if err != nil || len(recs) == 0 {
		return "Neo4j"
	}
	v := recs[0].Values
	return fmt.Sprintf("%v %v (%v)", v[0], v[1], v[2])
}

// Object names: (:Label) and [:TYPE].
func labelItem(l string) string   { return "(:" + l + ")" }
func relTypeItem(t string) string { return "[:" + t + "]" }

const relTypeObj = "reltype"

func (n *neo4jBackend) Objects(ctx context.Context) ([]ObjectGroup, error) {
	labels, err := n.strings(ctx, `CALL db.labels() YIELD label RETURN label ORDER BY label`)
	if err != nil {
		return nil, err
	}
	for i, l := range labels {
		labels[i] = labelItem(l)
	}
	types, _ := n.strings(ctx, `CALL db.relationshipTypes() YIELD relationshipType RETURN relationshipType ORDER BY relationshipType`)
	for i, t := range types {
		types[i] = relTypeItem(t)
	}
	if types == nil {
		types = []string{}
	}
	indexes, _ := n.strings(ctx, `SHOW INDEXES YIELD name, type WHERE type <> 'LOOKUP' RETURN name ORDER BY name`)
	constraints, _ := n.strings(ctx, `SHOW CONSTRAINTS YIELD name RETURN name ORDER BY name`)
	if indexes == nil {
		indexes = []string{}
	}
	if constraints == nil {
		constraints = []string{}
	}
	return []ObjectGroup{
		{Type: ObjTable, Label: "Node labels", Queryable: true, Items: labels},
		{Type: relTypeObj, Label: "Relationship types", Queryable: true, Items: types},
		{Type: ObjIndex, Label: "Indexes", Items: indexes},
		{Type: "constraint", Label: "Constraints", Items: constraints},
	}, nil
}

// cypherName quotes a label, type or property name with backticks.
func cypherName(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

// entity reads (:Label) or [:TYPE].
func entity(name string) (label, relType string, err error) {
	switch {
	case strings.HasPrefix(name, "(:") && strings.HasSuffix(name, ")"):
		return name[2 : len(name)-1], "", nil
	case strings.HasPrefix(name, "[:") && strings.HasSuffix(name, "]"):
		return "", name[2 : len(name)-1], nil
	}
	return "", "", fmt.Errorf("%s is not a (:Label) or [:TYPE]", name)
}

func (n *neo4jBackend) SelectFor(_ context.Context, name string) (string, error) {
	l, t, err := entity(name)
	if err != nil {
		return "", err
	}
	if l != "" {
		return "MATCH (n:" + cypherName(l) + ")\nRETURN n", nil
	}
	return "MATCH ()-[r:" + cypherName(t) + "]->()\nRETURN r", nil
}

func (n *neo4jBackend) Count(ctx context.Context, name string) (int64, error) {
	l, t, err := entity(name)
	if err != nil {
		return -1, err
	}
	q := "MATCH (n:" + cypherName(l) + ") RETURN count(n)"
	if t != "" {
		q = "MATCH ()-[r:" + cypherName(t) + "]->() RETURN count(r)"
	}
	recs, err := n.read(ctx, q, nil)
	if err != nil || len(recs) == 0 {
		return -1, err
	}
	c, _ := recs[0].Values[0].(int64)
	return c, nil
}

func (n *neo4jBackend) Definition(ctx context.Context, typ, name string) (string, error) {
	switch typ {
	case ObjIndex, "constraint":
		what := "INDEXES"
		if typ == "constraint" {
			what = "CONSTRAINTS"
		}
		recs, err := n.read(ctx, "SHOW "+what+" YIELD name, createStatement WHERE name = $name RETURN createStatement", map[string]any{"name": name})
		if err != nil {
			return "", err
		}
		if len(recs) == 0 {
			return "", fmt.Errorf("%s %s not found", typ, name)
		}
		return fmt.Sprint(recs[0].Values[0]) + ";\n", nil
	}
	l, t, err := entity(name)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	var props []*neo4j.Record
	if l != "" {
		fmt.Fprintf(&b, "// nodes with label %s\n", l)
		props, err = n.read(ctx, `CALL db.schema.nodeTypeProperties() YIELD nodeLabels, propertyName, propertyTypes, mandatory
			WHERE $label IN nodeLabels AND propertyName IS NOT NULL
			RETURN DISTINCT propertyName, propertyTypes, mandatory ORDER BY propertyName`, map[string]any{"label": l})
	} else {
		fmt.Fprintf(&b, "// relationships of type %s\n", t)
		props, err = n.read(ctx, `CALL db.schema.relTypeProperties() YIELD relType, propertyName, propertyTypes, mandatory
			WHERE relType = $type AND propertyName IS NOT NULL
			RETURN DISTINCT propertyName, propertyTypes, mandatory ORDER BY propertyName`, map[string]any{"type": ":`" + t + "`"})
		if ends, err := n.read(ctx, "MATCH (a)-[r:"+cypherName(t)+"]->(b) WITH labels(a) AS from, labels(b) AS to, count(*) AS n RETURN from, to, n ORDER BY n DESC LIMIT 20", nil); err == nil && len(ends) > 0 {
			b.WriteString("\n// between\n")
			for _, e := range ends {
				fmt.Fprintf(&b, "(%s)-[:%s]->(%s)  %v\n", labelList(e.Values[0]), t, labelList(e.Values[1]), e.Values[2])
			}
		}
	}
	if c, err := n.Count(ctx, name); err == nil {
		fmt.Fprintf(&b, "\n// %d in all\n", c)
	}
	if err == nil && len(props) > 0 {
		b.WriteString("\n// properties: types, and whether every one has it\n")
		w := 0
		for _, p := range props {
			w = max(w, len(fmt.Sprint(p.Values[0])))
		}
		for _, p := range props {
			must := ""
			if m, _ := p.Values[2].(bool); m {
				must = "  (always)"
			}
			fmt.Fprintf(&b, "%-*s  %s%s\n", w, fmt.Sprint(p.Values[0]), labelList(p.Values[1]), must)
		}
	}
	key := l
	if key == "" {
		key = t
	}
	for _, what := range []string{"INDEXES", "CONSTRAINTS"} {
		recs, err := n.read(ctx, "SHOW "+what+" YIELD labelsOrTypes, createStatement WHERE $key IN labelsOrTypes RETURN createStatement", map[string]any{"key": key})
		if err == nil && len(recs) > 0 {
			b.WriteString("\n// " + strings.ToLower(what) + "\n")
			for _, r := range recs {
				b.WriteString(fmt.Sprint(r.Values[0]) + ";\n")
			}
		}
	}
	return b.String(), nil
}

func labelList(v any) string {
	if l, ok := v.([]any); ok {
		s := make([]string, len(l))
		for i, x := range l {
			s[i] = fmt.Sprint(x)
		}
		return strings.Join(s, ", ")
	}
	return fmt.Sprint(v)
}

// Cypher clauses and procedures that write or administer.
var cypherWrites = regexp.MustCompile(`(?i)\b(CREATE|MERGE|DELETE|DETACH|SET|REMOVE|DROP|FOREACH|ALTER|GRANT|DENY|REVOKE|RENAME|START|STOP|TERMINATE|ENABLE|DEALLOCATE|REALLOCATE|LOAD|USE)\b`)

// cypherProcs are the procedures a query may CALL: they only read.
var cypherProcs = map[string]bool{
	"db.labels": true, "db.relationshiptypes": true, "db.propertykeys": true, "db.info": true, "db.ping": true,
	"db.schema.visualization": true, "db.schema.nodetypeproperties": true, "db.schema.reltypeproperties": true,
	"db.index.fulltext.querynodes": true, "db.index.fulltext.queryrelationships": true,
	"db.index.vector.querynodes": true, "db.index.vector.queryrelationships": true,
	"dbms.components": true, "dbms.procedures": true, "dbms.functions": true,
}

var (
	cypherCall     = regexp.MustCompile(`(?i)^CALL\s*([A-Za-z_][\w.]*\b|\{)(?:[^.\w]|$)`)
	cypherCallWord = regexp.MustCompile(`(?i)\bCALL\b`)
)

// cypherIsName says whether the word at loc is a property (n.set), a label or
// type (n:Start) or a map key ({load: 1}) rather than a clause.
func cypherIsName(code string, loc []int) bool {
	before := strings.TrimRight(code[:loc[0]], " \t\r\n")
	after := strings.TrimLeft(code[loc[1]:], " \t\r\n")
	return strings.HasSuffix(before, ".") || strings.HasSuffix(before, ":") || strings.HasPrefix(after, ":")
}

var errCypherReadOnly = errors.New("cactusdb is read-only: only Cypher that reads is allowed (MATCH, RETURN, WITH, UNWIND, SHOW…), no CREATE, MERGE, SET or DELETE")

func (n *neo4jBackend) CheckReadOnly(q string) error {
	code := blankQuoted(q, "'\"`")
	code = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(code), ";"))
	if code == "" {
		return errors.New("the query is empty")
	}
	if strings.Contains(code, ";") {
		return fmt.Errorf("%w; run one query at a time", errCypherReadOnly)
	}
	for _, loc := range cypherWrites.FindAllStringIndex(code, -1) {
		if cypherIsName(code, loc) {
			continue // n.set, (n:Start), {load: 1}: a property, label or key
		}
		return fmt.Errorf("%w (found %s)", errCypherReadOnly, strings.ToUpper(code[loc[0]:loc[1]]))
	}
	for _, loc := range cypherCallWord.FindAllStringIndex(code, -1) {
		if cypherIsName(code, loc) {
			continue
		}
		// Every CALL must name its procedure in plain letters: a quoted
		// name (`apoc`.`load`.`json`) is blanked above and can't be checked.
		m := cypherCall.FindStringSubmatch(code[loc[0]:])
		if m == nil {
			return fmt.Errorf("%w (CALL with a quoted procedure name)", errCypherReadOnly)
		}
		if m[1] == "{" {
			continue // a subquery: its clauses are checked above
		}
		if !cypherProcs[strings.ToLower(m[1])] {
			return fmt.Errorf("%w (CALL %s isn't one of the procedures that only read)", errCypherReadOnly, m[1])
		}
	}
	return nil
}

func (n *neo4jBackend) Query(ctx context.Context, q string) (resultSet, error) {
	sess := n.d.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead, DatabaseName: n.db})
	tx, err := sess.BeginTransaction(ctx)
	if err != nil {
		sess.Close(ctx)
		return nil, err
	}
	closeAll := func() error {
		tx.Rollback(context.Background())
		return sess.Close(context.Background())
	}
	res, err := tx.Run(ctx, strings.TrimRight(strings.TrimSpace(q), ";"), nil)
	if err != nil {
		closeAll()
		return nil, err
	}
	keys, err := res.Keys()
	if err != nil {
		closeAll()
		return nil, err
	}
	// One column of nodes or relationships: a column per property.
	expand := len(keys) == 1
	return newDocRows(func() (doc, error) {
		if !res.Next(ctx) {
			if err := res.Err(); err != nil {
				return nil, err
			}
			return nil, io.EOF
		}
		rec := res.Record()
		if expand {
			switch rec.Values[0].(type) {
			case dbtype.Node, dbtype.Relationship:
				return neo4jEntity(rec.Values[0]), nil
			}
		}
		d := make(doc, len(keys))
		for i, k := range keys {
			d[i] = field{k, neo4jValue(rec.Values[i])}
		}
		return d, nil
	}, closeAll)
}

// neo4jEntity is a node or relationship with its properties as fields.
func neo4jEntity(v any) doc {
	var d doc
	var props map[string]any
	switch t := v.(type) {
	case dbtype.Node:
		d = doc{{"_id", t.ElementId}, {"_labels", strings.Join(t.Labels, ":")}}
		props = t.Props
	case dbtype.Relationship:
		d = doc{{"_id", t.ElementId}, {"_type", t.Type}, {"_start", t.StartElementId}, {"_end", t.EndElementId}}
		props = t.Props
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		d = append(d, field{k, neo4jValue(props[k])})
	}
	return d
}

// neo4jValue turns a Neo4j value into a cell value.
func neo4jValue(v any) any {
	switch t := v.(type) {
	case dbtype.Node, dbtype.Relationship:
		return neo4jEntity(t)
	case dbtype.Path:
		out := []any{}
		for i, node := range t.Nodes {
			out = append(out, neo4jEntity(node))
			if i < len(t.Relationships) {
				out = append(out, neo4jEntity(t.Relationships[i]))
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = neo4jValue(e)
		}
		return out
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		d := make(doc, len(keys))
		for i, k := range keys {
			d[i] = field{k, neo4jValue(t[k])}
		}
		return d
	case dbtype.Date:
		return t.Time().Format("2006-01-02")
	case dbtype.LocalTime:
		return t.Time().Format("15:04:05.999999999")
	case dbtype.Time:
		return t.Time().Format("15:04:05.999999999Z07:00")
	case dbtype.LocalDateTime:
		return t.Time().Format("2006-01-02T15:04:05.999999999")
	case time.Time:
		return t.Format("2006-01-02T15:04:05.999999999Z07:00")
	case dbtype.Duration, dbtype.Point2D, dbtype.Point3D:
		return fmt.Sprint(t)
	}
	return v
}
