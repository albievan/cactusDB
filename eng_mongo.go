package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

// MongoDB: collections and views; queries in the MongoDB shell's syntax
// (find, aggregate, countDocuments, distinct). With no database set, every
// database is listed and objects are named database.collection.

func init() { backendOpeners[EngineMongoDB] = openMongo }

type mongoBackend struct {
	c  *mongo.Client
	db string // "" = every database
}

// mongoClient connects to MongoDB, or to a server that speaks its protocol
// (the Informix wire listener). Host may be a whole mongodb:// URI.
func mongoClient(p ConnParams, opts map[string]string) (*mongo.Client, error) {
	var uri string
	host := strings.TrimSpace(p.Host)
	if strings.HasPrefix(host, "mongodb://") || strings.HasPrefix(host, "mongodb+srv://") {
		uri = host
	} else {
		uri = "mongodb://" + hostPort(host, p.Port, 27017) + "/"
	}
	q := url.Values{}
	for k, v := range opts {
		q.Set(k, v)
	}
	if len(q) > 0 {
		sep := "?"
		if strings.Contains(uri, "?") {
			sep = "&"
		}
		uri += sep + q.Encode()
	}
	co := options.Client().ApplyURI(uri).
		SetAppName("cactusdb").
		SetConnectTimeout(10 * time.Second).
		SetServerSelectionTimeout(15 * time.Second).
		// Reads may go to a secondary: nothing is written.
		SetReadPreference(readpref.PrimaryPreferred())
	if p.Username != "" {
		cred := options.Credential{Username: p.Username, Password: p.Password, AuthSource: "admin"}
		if s := opts["authsource"]; s != "" {
			cred.AuthSource = s
		}
		if m := opts["authmechanism"]; m != "" {
			cred.AuthMechanism = m
		}
		co.SetAuth(cred)
	}
	return mongo.Connect(co)
}

func openMongo(ctx context.Context, p ConnParams, opts map[string]string) (backend, error) {
	c, err := mongoClient(p, opts)
	if err != nil {
		return nil, err
	}
	return &mongoBackend{c: c, db: strings.TrimSpace(p.Database)}, nil
}

func (m *mongoBackend) Ping(ctx context.Context) error { return m.c.Ping(ctx, nil) }
func (m *mongoBackend) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return m.c.Disconnect(ctx)
}

func (m *mongoBackend) Version(ctx context.Context) string {
	var info struct {
		Version string `bson:"version"`
	}
	if m.c.Database("admin").RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&info) != nil {
		return "MongoDB"
	}
	return "MongoDB " + info.Version
}

// split turns an object name into its database and collection.
func (m *mongoBackend) split(name string) (db, coll string) {
	if m.db != "" {
		return m.db, name
	}
	db, coll, _ = strings.Cut(name, ".")
	return db, coll
}

func (m *mongoBackend) databases(ctx context.Context) ([]string, error) {
	if m.db != "" {
		return []string{m.db}, nil
	}
	names, err := m.c.ListDatabaseNames(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	out := names[:0]
	for _, n := range names {
		if n != "admin" && n != "local" && n != "config" {
			out = append(out, n)
		}
	}
	return out, nil
}

func (m *mongoBackend) Objects(ctx context.Context) ([]ObjectGroup, error) {
	dbs, err := m.databases(ctx)
	if err != nil {
		return nil, err
	}
	colls := ObjectGroup{Type: ObjTable, Label: "Collections", Queryable: true, Items: []string{}}
	views := ObjectGroup{Type: ObjView, Label: "Views", Queryable: true, Items: []string{}}
	for _, dbName := range dbs {
		specs, err := m.c.Database(dbName).ListCollectionSpecifications(ctx, bson.D{})
		if err != nil {
			if m.db != "" {
				return nil, err
			}
			continue // no rights on that database
		}
		for _, s := range specs {
			if strings.HasPrefix(s.Name, "system.") {
				continue
			}
			name := s.Name
			if m.db == "" {
				name = dbName + "." + s.Name
			}
			if s.Type == "view" {
				views.Items = append(views.Items, name)
			} else {
				colls.Items = append(colls.Items, name)
			}
		}
	}
	sortStrings(colls.Items)
	sortStrings(views.Items)
	return []ObjectGroup{colls, views}, nil
}

func (m *mongoBackend) SelectFor(_ context.Context, name string) (string, error) {
	db, coll := m.split(name)
	return mongoCollExpr(m.db == "", db, coll) + ".find({})", nil
}

// mongoCollExpr is db.<collection> in the shell's syntax.
func mongoCollExpr(otherDB bool, db, coll string) string {
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	prefix := "db"
	if otherDB {
		prefix = "db.getSiblingDB(" + q(db) + ")"
	}
	simple := coll != ""
	for i := 0; i < len(coll); i++ {
		if !isIdentByte(coll[i], i == 0) {
			simple = false
		}
	}
	if simple && !otherDB && coll != "getSiblingDB" && coll != "getCollection" {
		return prefix + "." + coll
	}
	return prefix + ".getCollection(" + q(coll) + ")"
}

func (m *mongoBackend) Count(ctx context.Context, name string) (int64, error) {
	db, coll := m.split(name)
	return m.c.Database(db).Collection(coll).EstimatedDocumentCount(ctx)
}

func (m *mongoBackend) Definition(ctx context.Context, typ, name string) (string, error) {
	dbName, coll := m.split(name)
	db := m.c.Database(dbName)
	specs, err := db.ListCollectionSpecifications(ctx, bson.D{{Key: "name", Value: coll}})
	if err != nil {
		return "", err
	}
	if len(specs) == 0 {
		return "", fmt.Errorf("%s %s not found", typ, name)
	}
	s := specs[0]
	var b strings.Builder
	fmt.Fprintf(&b, "// %s %s.%s\n", s.Type, dbName, coll)
	if len(s.Options) > 0 {
		if j, err := bson.MarshalExtJSONIndent(s.Options, false, false, "", "  "); err == nil && string(j) != "{}" {
			b.WriteString("\n// options (validator, time series, view pipeline…)\n" + string(j) + "\n")
		}
	}
	c := db.Collection(coll)
	if s.Type != "view" {
		if n, err := c.EstimatedDocumentCount(ctx); err == nil {
			fmt.Fprintf(&b, "\n// about %d document(s)\n", n)
		}
		if cur, err := c.Indexes().List(ctx); err == nil {
			b.WriteString("\n// indexes\n")
			for cur.Next(ctx) {
				if j, err := bson.MarshalExtJSON(cur.Current, false, false); err == nil {
					b.WriteString(string(j) + "\n")
				}
			}
			cur.Close(ctx)
		}
	}
	// The fields, from a sample.
	var sample []doc
	if cur, err := c.Aggregate(ctx, mongo.Pipeline{{{Key: "$sample", Value: bson.D{{Key: "size", Value: 200}}}}}); err == nil {
		for cur.Next(ctx) {
			var d bson.D
			if bson.Unmarshal(cur.Current, &d) == nil {
				sample = append(sample, mongoDoc(d))
			}
		}
		cur.Close(ctx)
	}
	b.WriteString("\n" + strings.ReplaceAll(inferFields(sample), "-- ", "// "))
	return b.String(), nil
}

// mongoReadMethods are the shell methods that may run.
var mongoReadMethods = map[string]bool{
	"find": true, "findOne": true, "aggregate": true, "countDocuments": true, "count": true,
	"estimatedDocumentCount": true, "distinct": true, "getIndexes": true, "getCollectionNames": true,
}

// Cursor methods that may follow find().
var mongoCursorMethods = map[string]bool{"sort": true, "limit": true, "skip": true, "projection": true, "hint": true,
	"maxTimeMS": true, "collation": true, "toArray": true, "pretty": true, "batchSize": true, "comment": true}

func (m *mongoBackend) CheckReadOnly(q string) error {
	sq, err := parseShellQuery(q)
	if err != nil {
		return err
	}
	return checkShellReadOnly(sq)
}

var errMongoReadOnly = errors.New("cactusdb is read-only: only find, findOne, aggregate (without $out or $merge), countDocuments, distinct and getIndexes are allowed")

func checkShellReadOnly(sq *shellQuery) error {
	first := sq.calls[0]
	if !mongoReadMethods[first.name] {
		return fmt.Errorf("%w (%s)", errMongoReadOnly, first.name)
	}
	for _, c := range sq.calls[1:] {
		if first.name != "find" || !mongoCursorMethods[c.name] {
			return fmt.Errorf("%s() can't follow %s()", c.name, first.name)
		}
	}
	if first.name == "aggregate" && len(first.args) > 0 {
		stages, _ := first.args[0].(bson.A)
		for _, st := range stages {
			if d, ok := st.(bson.D); ok {
				for _, e := range d {
					if e.Key == "$out" || e.Key == "$merge" {
						return fmt.Errorf("%w (%s writes)", errMongoReadOnly, e.Key)
					}
				}
			}
		}
	}
	if first.name == "getCollectionNames" && sq.collection != "" || first.name != "getCollectionNames" && sq.collection == "" {
		return errors.New("name a collection: db.<collection>." + first.name + "(…)")
	}
	return nil
}

func argDoc(args []any, i int) (any, error) {
	if i >= len(args) || args[i] == nil {
		return bson.D{}, nil
	}
	switch args[i].(type) {
	case bson.D:
		return args[i], nil
	}
	return nil, fmt.Errorf("argument %d must be an object { … }", i+1)
}

func argInt(args []any) (int64, error) {
	if len(args) == 0 {
		return 0, errors.New("a number is missing")
	}
	switch t := args[0].(type) {
	case int32:
		return int64(t), nil
	case int64:
		return t, nil
	case float64:
		return int64(t), nil
	}
	return 0, fmt.Errorf("%v is not a number", args[0])
}

func (m *mongoBackend) Query(ctx context.Context, q string) (resultSet, error) {
	sq, err := parseShellQuery(q)
	if err != nil {
		return nil, err
	}
	dbName := m.db
	if sq.db != "" {
		dbName = sq.db
	}
	if dbName == "" {
		if d, c, ok := strings.Cut(sq.collection, "."); ok && m.db == "" {
			dbName, sq.collection = d, c
		} else {
			return nil, errors.New(`no database: set one in the connection, or write db.getSiblingDB("name")…`)
		}
	}
	return runShellQuery(ctx, m.c.Database(dbName), sq)
}

// runShellQuery runs a checked shell query on db.
func runShellQuery(ctx context.Context, db *mongo.Database, sq *shellQuery) (resultSet, error) {
	first := sq.calls[0]
	if first.name == "getCollectionNames" {
		names, err := db.ListCollectionNames(ctx, bson.D{})
		if err != nil {
			return nil, err
		}
		sortStrings(names)
		rows := make([][]any, len(names))
		for i, n := range names {
			rows[i] = []any{n}
		}
		return newSliceRows([]string{"name"}, rows), nil
	}
	c := db.Collection(sq.collection)
	switch first.name {
	case "find", "findOne":
		filter, err := argDoc(first.args, 0)
		if err != nil {
			return nil, err
		}
		fo := options.Find()
		if len(first.args) > 1 {
			proj, err := argDoc(first.args, 1)
			if err != nil {
				return nil, err
			}
			fo.SetProjection(proj)
		}
		if first.name == "findOne" {
			fo.SetLimit(1)
		}
		for _, cm := range sq.calls[1:] {
			switch cm.name {
			case "sort", "projection", "hint", "collation":
				d, err := argDoc(cm.args, 0)
				if err != nil {
					return nil, err
				}
				switch cm.name {
				case "sort":
					fo.SetSort(d)
				case "projection":
					fo.SetProjection(d)
				case "hint":
					fo.SetHint(d)
				}
			case "limit", "skip", "batchSize":
				n, err := argInt(cm.args)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", cm.name, err)
				}
				switch cm.name {
				case "limit":
					fo.SetLimit(n)
				case "skip":
					fo.SetSkip(n)
				case "batchSize":
					fo.SetBatchSize(int32(n))
				}
			}
		}
		cur, err := c.Find(ctx, filter, fo)
		if err != nil {
			return nil, err
		}
		return cursorRows(ctx, cur)
	case "aggregate":
		pipe := bson.A{}
		if len(first.args) > 0 {
			p, ok := first.args[0].(bson.A)
			if !ok {
				return nil, errors.New("aggregate takes an array of stages: aggregate([ { $match: {} } ])")
			}
			pipe = p
		}
		cur, err := c.Aggregate(ctx, pipe, options.Aggregate().SetAllowDiskUse(true))
		if err != nil {
			return nil, err
		}
		return cursorRows(ctx, cur)
	case "countDocuments", "count":
		filter, err := argDoc(first.args, 0)
		if err != nil {
			return nil, err
		}
		n, err := c.CountDocuments(ctx, filter)
		if err != nil {
			return nil, err
		}
		return newSliceRows([]string{"count"}, [][]any{{n}}), nil
	case "estimatedDocumentCount":
		n, err := c.EstimatedDocumentCount(ctx)
		if err != nil {
			return nil, err
		}
		return newSliceRows([]string{"count"}, [][]any{{n}}), nil
	case "distinct":
		f, ok := firstString(first.args)
		if !ok {
			return nil, errors.New(`distinct needs a field name: distinct("status")`)
		}
		filter, err := argDoc(first.args, 1)
		if err != nil {
			return nil, err
		}
		res := c.Distinct(ctx, f, filter)
		if err := res.Err(); err != nil {
			return nil, err
		}
		var vals []any
		if err := res.Decode(&vals); err != nil {
			return nil, err
		}
		rows := make([][]any, len(vals))
		for i, v := range vals {
			rows[i] = []any{mongoValue(v)}
		}
		return newSliceRows([]string{f}, rows), nil
	case "getIndexes":
		cur, err := c.Indexes().List(ctx)
		if err != nil {
			return nil, err
		}
		return cursorRows(ctx, cur)
	}
	return nil, fmt.Errorf("%s isn't supported", first.name)
}

// cursorRows shows the documents of a cursor as rows.
func cursorRows(ctx context.Context, cur *mongo.Cursor) (resultSet, error) {
	return newDocRows(func() (doc, error) {
		if !cur.Next(ctx) {
			if err := cur.Err(); err != nil {
				return nil, err
			}
			return nil, io.EOF
		}
		var d bson.D
		if err := bson.Unmarshal(cur.Current, &d); err != nil {
			return nil, err
		}
		return mongoDoc(d), nil
	}, func() error { return cur.Close(context.Background()) }, "_id")
}

func mongoDoc(d bson.D) doc {
	out := make(doc, len(d))
	for i, e := range d {
		out[i] = field{e.Key, mongoValue(e.Value)}
	}
	return out
}

// mongoValue turns a BSON value into a cell value: documents stay documents
// (shown as JSON), ObjectIds hex, dates time.Time.
func mongoValue(v any) any {
	switch t := v.(type) {
	case bson.D:
		return mongoDoc(t)
	case bson.M:
		d := doc{}
		for k, val := range t {
			d = append(d, field{k, mongoValue(val)})
		}
		return d
	case bson.A:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = mongoValue(e)
		}
		return out
	case []any:
		return mongoValue(bson.A(t))
	case bson.ObjectID:
		return t.Hex()
	case bson.DateTime:
		return t.Time().UTC()
	case bson.Decimal128:
		return json.Number(t.String())
	case bson.Binary:
		if t.Subtype == 4 && len(t.Data) == 16 {
			h := hex.EncodeToString(t.Data)
			return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
		}
		return "0x" + hex.EncodeToString(t.Data)
	case bson.Timestamp:
		return doc{{"t", int64(t.T)}, {"i", int64(t.I)}}
	case bson.Regex:
		return "/" + t.Pattern + "/" + t.Options
	case bson.Null, bson.Undefined:
		return nil
	case bson.JavaScript:
		return string(t)
	case bson.Symbol:
		return string(t)
	case bson.MinKey:
		return "MinKey"
	case bson.MaxKey:
		return "MaxKey"
	case int32:
		return int64(t)
	case float64:
		if t == float64(int64(t)) && t < 1e15 && t > -1e15 {
			return json.Number(strconv.FormatFloat(t, 'f', -1, 64))
		}
		return t
	}
	return v
}

// sortStrings sorts names ignoring case.
func sortStrings(s []string) {
	sort.SliceStable(s, func(i, j int) bool { return strings.ToLower(s[i]) < strings.ToLower(s[j]) })
}
