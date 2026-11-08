package main

// The connection form's list of databases: a new connection without a
// database lists the server's, so one can be picked instead of typed.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// databaseLister is a backend that can list its databases (keyspaces,
// buckets, datasets…), whatever database its connection names.
type databaseLister interface {
	Databases(ctx context.Context) ([]string, error)
}

// listerBackends are the backends that implement databaseLister.
var listerBackends = map[string]bool{EngineMongoDB: true, EngineCosmosDB: true, EngineCouchbase: true, EngineCassandra: true,
	EngineNeo4j: true, EngineRedis: true, EngineAthena: true, EngineBigQuery: true}

func canListDatabases(engine string) bool {
	switch dialect(engine) {
	case EngineMySQL, EngineMariaDB, EnginePostgres, EngineMSSQL:
		return true
	}
	return listerBackends[engine]
}

var errNoDBList = errors.New("this database type can't list its databases")

// listDatabases lists the databases the connection's user can see.
func listDatabases(ctx context.Context, engine string, db *sql.DB) ([]string, error) {
	if b := backendOf(db); b != nil {
		if l, ok := b.(databaseLister); ok {
			return l.Databases(ctx)
		}
		return nil, errNoDBList
	}
	var q string
	switch dialect(engine) {
	case EngineMySQL, EngineMariaDB:
		q = "SHOW DATABASES"
	case EnginePostgres:
		q = "SELECT datname FROM pg_database WHERE NOT datistemplate ORDER BY 1"
	case EngineMSSQL:
		q = "SELECT name FROM sys.databases WHERE HAS_DBACCESS(name) = 1 ORDER BY name"
	default:
		return nil, errNoDBList
	}
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// needsDatabase refuses a connection without the database its type needs.
func needsDatabase(p ConnParams) error {
	e := engineByID(p.Engine)
	if e == nil || !e.NeedsDB || strings.TrimSpace(p.Database) != "" {
		return nil
	}
	if e.File {
		return fmt.Errorf("%s: enter the database file", e.Name)
	}
	return fmt.Errorf("%s: enter the %s", e.Name, strings.ToLower(e.DBLabel))
}

// handleDatabases lists the databases of the connection the form describes.
func (s *server) handleDatabases(w http.ResponseWriter, r *http.Request) {
	var req connectReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	p := req.ConnParams
	if !canListDatabases(p.Engine) {
		writeErr(w, 400, fmt.Errorf("%s can't list its databases", engineName(p.Engine)))
		return
	}
	if req.KeepPasswordOf != 0 && p.Password == "" {
		c, _, ok := s.unlockConnAs(w, r, req.KeepPasswordOf, req.LockPassword, req.SuperPassword)
		if !ok {
			return
		}
		p.Password = c.Password
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	db, err := openDB(ctx, p)
	if err != nil {
		writeErr(w, 400, explainConnErr(p, err))
		return
	}
	defer db.Close()
	dbs, err := listDatabases(ctx, p.Engine, db)
	if err != nil {
		writeErr(w, 400, fmt.Errorf("connected, but listing the databases failed: %w", err))
		return
	}
	logAction(r, "listed %d databases of %s", len(dbs), defaultConnName(p))
	writeJSON(w, 200, map[string]any{"databases": dbs})
}

// ---- the backends ----------------------------------------------------------------------
// Each lists every database, even when the connection names one: the
// connections they're called on are the form's own, opened for this.

func (m *mongoBackend) Databases(ctx context.Context) ([]string, error) {
	names, err := m.c.ListDatabaseNames(ctx, map[string]any{})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

func (c *cosmosBackend) Databases(ctx context.Context) ([]string, error) {
	keep := c.db
	c.db = ""
	defer func() { c.db = keep }()
	return c.databases(ctx)
}

func (a *athenaBackend) Databases(ctx context.Context) ([]string, error) {
	keep := a.database
	a.database = ""
	defer func() { a.database = keep }()
	return a.databases(ctx)
}

func (b *bigqueryBackend) Databases(ctx context.Context) ([]string, error) {
	keep := b.dataset
	b.dataset = ""
	defer func() { b.dataset = keep }()
	return b.datasets(ctx)
}

func (c *cassandraBackend) Databases(ctx context.Context) ([]string, error) {
	keep := c.keyspace
	c.keyspace = ""
	defer func() { c.keyspace = keep }()
	return c.keyspaces(ctx)
}

// Couchbase: the buckets (system:buckets since 7.0, else the keyspaces of 6.x).
func (b *couchbaseBackend) Databases(ctx context.Context) ([]string, error) {
	out, err := b.strings(ctx, "SELECT RAW name FROM system:buckets ORDER BY name")
	if err != nil {
		out, err = b.strings(ctx, "SELECT RAW name FROM system:keyspaces ORDER BY name")
	}
	return out, err
}

// Neo4j: SHOW DATABASES runs on the system database.
func (n *neo4jBackend) Databases(ctx context.Context) ([]string, error) {
	res, err := neo4j.ExecuteQuery(ctx, n.d, "SHOW DATABASES YIELD name RETURN DISTINCT name ORDER BY name", nil,
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase("system"), neo4j.ExecuteQueryWithReadersRouting())
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, r := range res.Records {
		if len(r.Values) > 0 && fmt.Sprint(r.Values[0]) != "system" {
			out = append(out, fmt.Sprint(r.Values[0]))
		}
	}
	return out, nil
}

// Redis: the numbered databases (16 unless the server says otherwise).
func (r *redisBackend) Databases(ctx context.Context) ([]string, error) {
	n := 16
	if v, err := r.c.ConfigGet(ctx, "databases").Result(); err == nil {
		if c, err := strconv.Atoi(v["databases"]); err == nil && c > 0 {
			n = c
		}
	}
	out := make([]string, n)
	for i := range out {
		out[i] = strconv.Itoa(i)
	}
	return out, nil
}
