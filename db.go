package main

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	_ "github.com/nakagami/firebirdsql"
	_ "github.com/sijms/go-ora/v2"
	_ "modernc.org/sqlite"
)

// ConnParams describes how to reach a database. For file databases (SQLite,
// Access) only Database (the file path) is used. For SQL Server, Host may
// name an instance (host\INSTANCE). Options holds extra settings as
// key=value pairs (e.g. an Athena workgroup); never secrets, which go in
// Password, the only field stored encrypted besides Username.
type ConnParams struct {
	Engine   string `json:"engine"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	Database string `json:"database"`
	Options  string `json:"options"`
}

const (
	EngineSQLite   = "sqlite"
	EngineMySQL    = "mysql"
	EngineMariaDB  = "mariadb"
	EngineFirebird = "firebird"
	EngineMSSQL    = "mssql"
	EnginePostgres = "postgres"
	EngineOracle   = "oracle"

	// PostgreSQL and SQL Server compatible: their SQL and catalog are those of
	// the dialect (see engineInfo.Dialect).
	EngineCockroach = "cockroachdb"
	EngineRedshift  = "redshift"
	EngineAzurePG   = "azurepg"
	EngineHorizonDB = "horizondb"
	EnginePgvector  = "pgvector"
	EngineAzureSQL  = "azuresql"

	// Reached through a backend (backend.go).
	EngineInformix  = "informix"
	EngineAccess    = "access"
	EngineBigQuery  = "bigquery"
	EngineAthena    = "athena"
	EngineMongoDB   = "mongodb"
	EngineCosmosDB  = "cosmosdb"
	EngineCouchbase = "couchbase"
	EngineCassandra = "cassandra"
	EngineRedis     = "redis"
	EngineNeo4j     = "neo4j"
	EngineWeaviate  = "weaviate"
)

// engineInfo describes a database type and its connection form.
type engineInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Group       string `json:"group"` // the heading in the list of database types
	DefaultPort int    `json:"defaultPort"`
	// Dialect is the engine whose SQL and catalog it shares ("": its own).
	Dialect string `json:"dialect,omitempty"`
	// Backend: reached through backend.go rather than a database/sql driver.
	Backend bool `json:"backend,omitempty"`
	File    bool `json:"file,omitempty"` // a database file instead of a server
	// NeedsDB: it can't connect without the database (a file, path or
	// service name), so it's never saved without one. ListsDBs: the
	// connection form can list the server's databases (databases.go).
	NeedsDB  bool `json:"needsDB,omitempty"`
	ListsDBs bool `json:"listsDBs,omitempty"`

	// The connection form: a label "" hides that field.
	FileLabel   string `json:"fileLabel,omitempty"`
	FileHint    string `json:"fileHint,omitempty"`
	HostLabel   string `json:"hostLabel,omitempty"`
	HostHint    string `json:"hostHint,omitempty"`
	PortLabel   string `json:"portLabel,omitempty"`
	UserLabel   string `json:"userLabel,omitempty"`
	DefaultUser string `json:"defaultUser,omitempty"`
	PassLabel   string `json:"passLabel,omitempty"`
	PassHint    string `json:"passHint,omitempty"`
	DBLabel     string `json:"dbLabel,omitempty"`
	DBHint      string `json:"dbHint,omitempty"`
	OptionsHint string `json:"optionsHint,omitempty"` // "" hides Options
	Help        string `json:"help,omitempty"`        // a line under the form

	Language  string `json:"language"`  // the query language, e.g. SQL, CQL, Cypher
	QueryHint string `json:"queryHint"` // the query box's placeholder
	// What it can't do: back up as SQL INSERTs, compare structures.
	NoBackup  bool `json:"noBackup,omitempty"`
	NoCompare bool `json:"noCompare,omitempty"`
}

const (
	sqlQueryHint = "Select a table, or write a SELECT query here and press Ctrl+Enter (read-only: no INSERT / UPDATE / DELETE)"
)

var engines = []engineInfo{
	{ID: EngineSQLite, Name: "SQLite", Group: "Relational", File: true, FileLabel: "SQLite database file", FileHint: "/path/to/database.db"},
	{ID: EngineMySQL, Name: "MySQL", Group: "Relational", DefaultPort: 3306},
	{ID: EngineMariaDB, Name: "MariaDB", Group: "Relational", DefaultPort: 3306},
	{ID: EngineFirebird, Name: "Firebird", Group: "Relational", DefaultPort: 3050, DefaultUser: "SYSDBA", NeedsDB: true,
		DBLabel: "Database path or alias", DBHint: "/var/lib/firebird/data/employee.fdb"},
	{ID: EngineMSSQL, Name: "SQL Server", Group: "Relational", DefaultPort: 1433, HostHint: "server or server\\INSTANCE",
		DBHint: "database name (blank = login default)", OptionsHint: "e.g. encrypt=true&ApplicationIntent=ReadOnly"},
	{ID: EnginePostgres, Name: "PostgreSQL", Group: "Relational", DefaultPort: 5432, DefaultUser: "postgres",
		DBHint: "database name (blank = same as username)", OptionsHint: "e.g. sslmode=require"},
	{ID: EngineOracle, Name: "Oracle", Group: "Relational", DefaultPort: 1521, NeedsDB: true, DBLabel: "Service name", DBHint: "e.g. FREEPDB1 or ORCL",
		OptionsHint: "e.g. schema=HR (default: the user's own), sid=ORCL, ssl=true", NoBackup: true, NoCompare: true},
	{ID: EngineInformix, Name: "Informix", Group: "Relational", DefaultPort: 27017, Backend: true, DefaultUser: "informix", NeedsDB: true,
		HostLabel: "Wire listener host", DBHint: "database name, e.g. stores_demo", OptionsHint: "e.g. authSource=admin&tls=true",
		Help:     "Through the Informix wire listener (MongoDB API) with SQL passthrough on: security.sql.passthrough=true in its properties file.",
		NoBackup: true, NoCompare: true},
	{ID: EngineAccess, Name: "Microsoft Access", Group: "Relational", File: true, Backend: true,
		FileLabel: "Access database file (.mdb or .accdb)", FileHint: "/path/to/database.accdb",
		Help:     "Read directly from the file: no Access or ODBC driver needed. Password-protected (encrypted) files can't be opened.",
		Language: "SQL", QueryHint: "Select a table, or write SELECT * | columns FROM table [WHERE column = value] [ORDER BY column]",
		NoBackup: true, NoCompare: true},
	{ID: EngineCockroach, Name: "CockroachDB", Group: "Relational", DefaultPort: 26257, Dialect: EnginePostgres, DefaultUser: "root",
		DBHint: "database name, e.g. defaultdb", OptionsHint: "e.g. sslmode=verify-full&options=--cluster=my-cluster-123", NoBackup: true},

	{ID: EngineAzureSQL, Name: "Azure SQL Database (Hyperscale)", Group: "Cloud", DefaultPort: 1433, Dialect: EngineMSSQL,
		HostHint: "myserver.database.windows.net", DBHint: "database name",
		OptionsHint: "connects with ApplicationIntent=ReadOnly (a read replica when there is one); e.g. ApplicationIntent=ReadWrite"},
	{ID: EngineAzurePG, Name: "Azure Database for PostgreSQL", Group: "Cloud", DefaultPort: 5432, Dialect: EnginePostgres,
		HostHint: "myserver.postgres.database.azure.com", DBHint: "database name, e.g. postgres", OptionsHint: "sslmode=require is the default"},
	{ID: EngineHorizonDB, Name: "Azure HorizonDB", Group: "Cloud", DefaultPort: 5432, Dialect: EnginePostgres,
		HostHint: "the cluster's endpoint", DBHint: "database name, e.g. postgres", OptionsHint: "sslmode=require is the default"},
	{ID: EngineRedshift, Name: "Amazon Redshift", Group: "Cloud", DefaultPort: 5439, Dialect: EnginePostgres,
		HostHint: "cluster.xxxx.region.redshift.amazonaws.com", DBHint: "database name, e.g. dev", OptionsHint: "sslmode=require is the default",
		NoBackup: true, NoCompare: true},
	{ID: EngineAthena, Name: "Amazon Athena", Group: "Cloud", Backend: true, HostLabel: "AWS region", HostHint: "e.g. eu-west-1",
		PortLabel: "-", UserLabel: "Access key ID", PassLabel: "Secret access key", PassHint: "secret key (a session token after a space)",
		DBLabel: "Database", DBHint: "blank = every database of the catalog",
		OptionsHint: "workgroup=primary&output=s3://bucket/athena-results/&catalog=AwsDataCatalog",
		Help:        "Queries run in the workgroup and write their results to its S3 output location (or the one in Options). Athena bills the data each query scans.",
		Language:    "SQL", QueryHint: "Select a table, or write a SELECT query (Athena SQL) and press Ctrl+Enter", NoBackup: true, NoCompare: true},
	{ID: EngineBigQuery, Name: "Google BigQuery", Group: "Cloud", Backend: true, HostLabel: "Project ID", HostHint: "my-project",
		PortLabel: "-", UserLabel: "-", PassLabel: "Service account key (JSON)", PassHint: "paste the key file's JSON, or its path on the server",
		DBLabel: "Dataset", DBHint: "blank = every dataset of the project", OptionsHint: "e.g. location=EU&maxbytes=10000000000",
		Help:     "BigQuery bills the data each query scans; set maxbytes in Options to cap a query. Every query is checked with a dry run first: only SELECT runs.",
		Language: "SQL", QueryHint: "Select a table, or write a SELECT query (GoogleSQL) and press Ctrl+Enter", NoBackup: true, NoCompare: true},

	{ID: EngineMongoDB, Name: "MongoDB", Group: "NoSQL", DefaultPort: 27017, Backend: true,
		HostHint: "host, or a mongodb+srv:// or mongodb:// URI", DBHint: "database name", OptionsHint: "e.g. authSource=admin&tls=true&replicaSet=rs0",
		Language: "MongoDB", QueryHint: "Select a collection, or write db.collection.find({ field: \"value\" }).sort({ _id: -1 }) or .aggregate([...]) and press Ctrl+Enter",
		NoBackup: true, NoCompare: true},
	{ID: EngineCosmosDB, Name: "Azure Cosmos DB", Group: "NoSQL", Backend: true, HostLabel: "Account endpoint",
		HostHint: "https://myaccount.documents.azure.com:443/", PortLabel: "-", UserLabel: "-", PassLabel: "Account key",
		PassHint: "a read-only key is best", DBHint: "blank = every database",
		Help:     "The NoSQL API. For Cosmos DB's MongoDB, Cassandra or PostgreSQL APIs use those database types.",
		Language: "SQL", QueryHint: "Select a container, or write -- container: db.container, then SELECT * FROM c WHERE c.field = 'x'",
		NoBackup: true, NoCompare: true},
	{ID: EngineCouchbase, Name: "Couchbase", Group: "NoSQL", DefaultPort: 8093, Backend: true, PortLabel: "Query port",
		HostHint: "host of a query node", DBLabel: "Bucket", DBHint: "blank = every bucket", OptionsHint: "e.g. tls=true (port 18093)",
		Language: "SQL++", QueryHint: "Select a collection, or write a SELECT (SQL++) query and press Ctrl+Enter", NoBackup: true, NoCompare: true},
	{ID: EngineCassandra, Name: "Apache Cassandra", Group: "NoSQL", DefaultPort: 9042, Backend: true, DefaultUser: "cassandra",
		HostHint: "host, or several: host1,host2", DBLabel: "Keyspace", DBHint: "blank = every keyspace",
		OptionsHint: "e.g. consistency=LOCAL_QUORUM&dc=dc1&tls=true",
		Language:    "CQL", QueryHint: "Select a table, or write a SELECT (CQL) query and press Ctrl+Enter", NoBackup: true, NoCompare: true},
	{ID: EngineRedis, Name: "Redis", Group: "NoSQL", DefaultPort: 6379, Backend: true, UserLabel: "Username (ACL, optional)",
		DBLabel: "Database number", DBHint: "0", OptionsHint: "e.g. tls=true",
		Language: "Redis", QueryHint: "Select a key group, or write a read command, e.g. SCAN MATCH user:* or HGETALL user:1, and press Ctrl+Enter",
		NoBackup: true, NoCompare: true},
	{ID: EngineNeo4j, Name: "Neo4j", Group: "NoSQL", DefaultPort: 7687, Backend: true, DefaultUser: "neo4j",
		HostHint: "host, or a neo4j+s:// URI", DBHint: "blank = the default database", OptionsHint: "e.g. tls=true",
		Language: "Cypher", QueryHint: "Select a label, or write a Cypher query, e.g. MATCH (n:Person) RETURN n LIMIT 100, and press Ctrl+Enter",
		NoBackup: true, NoCompare: true},

	{ID: EnginePgvector, Name: "PostgreSQL + pgvector", Group: "Vector", DefaultPort: 5432, Dialect: EnginePostgres, DefaultUser: "postgres",
		DBHint: "database name (blank = same as username)", OptionsHint: "e.g. sslmode=require"},
	{ID: EngineWeaviate, Name: "Weaviate", Group: "Vector", DefaultPort: 8080, Backend: true,
		HostHint: "host, or https://cluster.weaviate.cloud", UserLabel: "-", PassLabel: "API key (optional)", DBLabel: "-",
		Language: "GraphQL", QueryHint: "Select a collection, or write a GraphQL Get, e.g. { Get { Article(limit: 10) { title } } }, and press Ctrl+Enter",
		NoBackup: true, NoCompare: true},
}

func init() {
	for i := range engines {
		e := &engines[i]
		if !e.File {
			e.HostLabel = cmp.Or(e.HostLabel, "Host")
			e.PortLabel = cmp.Or(e.PortLabel, "Port")
			e.UserLabel = cmp.Or(e.UserLabel, "Username")
			e.PassLabel = cmp.Or(e.PassLabel, "Password")
			e.DBLabel = cmp.Or(e.DBLabel, "Database")
			e.DBHint = cmp.Or(e.DBHint, "database name")
		}
		e.Language = cmp.Or(e.Language, "SQL")
		e.QueryHint = cmp.Or(e.QueryHint, sqlQueryHint)
		e.NeedsDB = e.NeedsDB || e.File
		e.ListsDBs = canListDatabases(e.ID)
	}
}

func engineByID(id string) *engineInfo {
	for i := range engines {
		if engines[i].ID == id {
			return &engines[i]
		}
	}
	return nil
}

// dialect is the engine whose SQL and catalog id uses: PostgreSQL for
// CockroachDB, Redshift…, SQL Server for Azure SQL, else id itself.
func dialect(id string) string {
	if e := engineByID(id); e != nil && e.Dialect != "" {
		return e.Dialect
	}
	return id
}

// engineName is the display name of an engine.
func engineName(id string) string {
	if e := engineByID(id); e != nil {
		return e.Name
	}
	return id
}

// driverModules maps each engine to the Go module that implements it. All of
// them are pure Go and compiled into the binary, so unlike JDBC-based tools
// nothing has to be downloaded or installed on the host.
var driverModules = []struct{ Engines, Module string }{
	{"SQLite", "modernc.org/sqlite"},
	{"MySQL, MariaDB", "github.com/go-sql-driver/mysql"},
	{"Firebird", "github.com/nakagami/firebirdsql"},
	{"SQL Server, Azure SQL Database", "github.com/microsoft/go-mssqldb"},
	{"PostgreSQL, pgvector, CockroachDB, Amazon Redshift, Azure Database for PostgreSQL, Azure HorizonDB", "github.com/jackc/pgx/v5"},
	{"Oracle", "github.com/sijms/go-ora/v2"},
	{"MongoDB, Informix (wire listener)", "go.mongodb.org/mongo-driver/v2"},
	{"Apache Cassandra", "github.com/apache/cassandra-gocql-driver/v2"},
	{"Redis", "github.com/redis/go-redis/v9"},
	{"Neo4j", "github.com/neo4j/neo4j-go-driver/v5"},
	{"Microsoft Access", "built into cactusdb (reads .mdb/.accdb files)"},
	{"Google BigQuery, Amazon Athena, Azure Cosmos DB, Couchbase, Weaviate", "built into cactusdb (their HTTPS APIs)"},
}

func driverInfo() []map[string]string {
	versions := map[string]string{}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			versions[d.Path] = d.Version
		}
	}
	out := []map[string]string{}
	for _, d := range driverModules {
		v := versions[d.Module]
		if v == "" {
			v = "built in"
		}
		if strings.HasPrefix(d.Module, "built into") {
			v = "-"
		}
		out = append(out, map[string]string{"engines": d.Engines, "module": d.Module, "version": v, "status": "bundled"})
	}
	return out
}

// openDB opens and pings a database for the given parameters.
func openDB(ctx context.Context, p ConnParams) (*sql.DB, error) {
	var driver, dsn string
	opts := parseOptions(p.Options)
	switch p.Engine {
	case EngineSQLite:
		if p.Database == "" {
			return nil, errors.New("database file path is required")
		}
		if _, err := os.Stat(p.Database); err != nil {
			return nil, fmt.Errorf("sqlite file not found: %s", p.Database)
		}
		driver = "sqlite"
		// Read-only at the SQLite level: no writes even if a query slips past checkReadOnly.
		dsn = sqliteURI(p.Database) + "?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)"
	case EngineMySQL, EngineMariaDB:
		cfg := mysql.NewConfig()
		cfg.User = p.Username
		cfg.Passwd = p.Password
		cfg.Net = "tcp"
		cfg.Addr = hostPort(p.Host, p.Port, 3306)
		cfg.DBName = p.Database
		cfg.ParseTime = true
		cfg.Timeout = 10 * time.Second
		// Make every pooled session read-only. Unlike a READ ONLY transaction
		// this also blocks DDL, which would otherwise commit implicitly. The
		// variable is transaction_read_only on MySQL 8+/MariaDB 11.1+ and
		// tx_read_only on older servers.
		var lastErr error
		for _, v := range []string{"transaction_read_only", "tx_read_only"} {
			cfg.Params = map[string]string{v: "1"}
			db, err := openAndPing(ctx, "mysql", cfg.FormatDSN(), 0)
			if err == nil {
				return db, nil
			}
			lastErr = err
			var me *mysql.MySQLError
			if !errors.As(err, &me) || me.Number != 1193 { // 1193: unknown system variable
				break
			}
		}
		return nil, lastErr
	case EngineFirebird:
		if p.Database == "" {
			return nil, errors.New("database path or alias is required")
		}
		// firebird://user:password@host:port/<alias | /unix/path | C:\win\path>
		path := p.Database
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		u := url.URL{
			Scheme: "firebird",
			User:   url.UserPassword(p.Username, p.Password),
			Host:   hostPort(p.Host, p.Port, 3050),
			Path:   path,
		}
		driver = "firebirdsql"
		dsn = u.String()
	case EngineMSSQL, EngineAzureSQL:
		driver = "sqlserver"
		dsn = mssqlDSN(p, opts)
	case EnginePostgres, EngineCockroach, EngineRedshift, EngineAzurePG, EngineHorizonDB, EnginePgvector:
		driver = "pgx"
		dsn = postgresDSN(p, opts)
	case EngineOracle:
		driver = "oracle"
		dsn = oracleDSN(p, opts)
	default:
		if open := backendOpeners[p.Engine]; open != nil {
			b, err := open(ctx, p, opts)
			if err != nil {
				return nil, err
			}
			return openBackend(ctx, b)
		}
		return nil, fmt.Errorf("unsupported engine %q", p.Engine)
	}

	maxConns := 0
	if p.Engine == EngineSQLite {
		maxConns = 1
	}
	db, err := openAndPing(ctx, driver, dsn, maxConns)
	if err == nil && p.Engine != dialect(p.Engine) {
		dbFlavors.Store(db, p.Engine)
	}
	if err == nil && p.Engine == EngineOracle && opts["schema"] != "" {
		oracleSchemas.Store(db, strings.ToUpper(opts["schema"]))
	}
	return db, err
}

// backendOpeners connects to the engines reached through a backend.
var backendOpeners = map[string]func(ctx context.Context, p ConnParams, opts map[string]string) (backend, error){}

// dbFlavors remembers the engine of a database opened with the driver of its
// dialect (e.g. Redshift with PostgreSQL's), for the few differences.
var dbFlavors sync.Map

// flavorOf returns the engine db was opened for ("" when it is its dialect).
func flavorOf(db *sql.DB) string {
	if v, ok := dbFlavors.Load(db); ok {
		return v.(string)
	}
	return ""
}

func openAndPing(ctx context.Context, driver, dsn string, maxConns int) (*sql.DB, error) {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxConns)
	// Close pooled connections soon after use. A connection left idle is
	// eventually cut by the database server (wait_timeout), a firewall or a
	// VPN, and the next request on it fails; idle connections also count
	// against the server's connection limit, shared with other applications.
	db.SetConnMaxIdleTime(time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := db.PingContext(pctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// sqliteURI turns a filesystem path into an SQLite "file:" URI, escaping the
// characters URIs treat specially and handling Windows drive letters.
func sqliteURI(path string) string {
	p := strings.ReplaceAll(path, `\`, "/")
	p = strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23", " ", "%20").Replace(p)
	if len(p) >= 2 && p[1] == ':' { // C:/data/x.db -> file:///C:/data/x.db
		return "file:///" + p
	}
	return "file:" + p
}

// mssqlDSN builds a sqlserver:// URL. "host\INSTANCE" connects to a named
// instance (resolved by SQL Browser unless a port is given).
func mssqlDSN(p ConnParams, opts map[string]string) string {
	host, instance, _ := strings.Cut(p.Host, `\`)
	if host == "" {
		host = "localhost"
	}
	q := url.Values{}
	if p.Database != "" {
		q.Set("database", p.Database)
	}
	q.Set("app name", "cactusdb")
	q.Set("dial timeout", "10")
	if p.Engine == EngineAzureSQL {
		// Azure always encrypts, with a trusted certificate. ReadOnly
		// connects to a read replica of Hyperscale or Business Critical
		// when there is one (the primary otherwise).
		q.Set("encrypt", "true")
		q.Set("applicationintent", "ReadOnly")
	} else {
		// Most servers use self-signed certificates; accept them like SSMS's
		// "Trust server certificate" option.
		q.Set("trustservercertificate", "true")
	}
	for k, v := range opts {
		q.Set(k, v)
	}
	u := url.URL{Scheme: "sqlserver", User: url.UserPassword(p.Username, p.Password), RawQuery: q.Encode()}
	switch {
	case instance != "" && p.Port == 0:
		u.Host, u.Path = host, instance
	case instance != "":
		u.Host, u.Path = hostPort(host, p.Port, 1433), instance
	default:
		u.Host = hostPort(host, p.Port, 1433)
	}
	return u.String()
}

// postgresDSN builds a postgres:// URL. Every session starts read-only
// (default_transaction_read_only), which in PostgreSQL also blocks DDL.
func postgresDSN(p ConnParams, opts map[string]string) string {
	q := url.Values{}
	q.Set("connect_timeout", "10")
	switch p.Engine {
	case EngineRedshift:
		// Redshift refuses unknown startup parameters: its queries run in a
		// READ ONLY transaction (streamRows) instead.
		q.Set("sslmode", "require")
	case EngineAzurePG, EngineHorizonDB:
		q.Set("sslmode", "require")
		fallthrough
	default:
		q.Set("application_name", "cactusdb")
		q.Set("default_transaction_read_only", "on")
	}
	for k, v := range opts {
		q.Set(k, v)
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(p.Username, p.Password),
		Host:     hostPort(p.Host, p.Port, cmp.Or(engineByID(p.Engine).DefaultPort, 5432)),
		Path:     "/" + p.Database,
		RawQuery: q.Encode(),
	}
	return u.String()
}

func hostPort(host string, port, def int) string {
	if host == "" {
		host = "localhost"
	}
	if port == 0 {
		port = def
	}
	return host + ":" + strconv.Itoa(port)
}

// listColumns returns the column names of a table in ordinal order.
func listColumns(ctx context.Context, engine string, db *sql.DB, table string) ([]string, error) {
	switch engine {
	case EngineSQLite:
		rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
		if err != nil {
			return nil, err
		}
		return scanStrings(rows)
	case EngineMySQL, EngineMariaDB:
		rows, err := db.QueryContext(ctx, `SELECT column_name FROM information_schema.columns
			WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position`, table)
		if err != nil {
			return nil, err
		}
		return scanStrings(rows)
	case EngineFirebird:
		rows, err := db.QueryContext(ctx, `SELECT TRIM(RDB$FIELD_NAME) FROM RDB$RELATION_FIELDS
			WHERE RDB$RELATION_NAME = ? ORDER BY RDB$FIELD_POSITION`, table)
		if err != nil {
			return nil, err
		}
		return scanStrings(rows)
	case EnginePostgres:
		rows, err := db.QueryContext(ctx, `SELECT a.attname FROM pg_attribute a
			JOIN pg_class c ON c.oid = a.attrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname || '.' || c.relname = $1 AND a.attnum > 0 AND NOT a.attisdropped
			ORDER BY a.attnum`, table)
		if err != nil {
			return nil, err
		}
		return scanStrings(rows)
	case EngineOracle:
		owner, tab := oracleSplit(table)
		rows, err := db.QueryContext(ctx, `SELECT column_name FROM all_tab_columns
			WHERE owner = NVL(:1, SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA')) AND table_name = :2 ORDER BY column_id`, owner, tab)
		if err != nil {
			return nil, err
		}
		return scanStrings(rows)
	case EngineMSSQL:
		rows, err := db.QueryContext(ctx, `SELECT c.name FROM sys.columns c
			JOIN sys.objects o ON o.object_id = c.object_id
			JOIN sys.schemas s ON s.schema_id = o.schema_id
			WHERE s.name + '.' + o.name = @p1 ORDER BY c.column_id`, table)
		if err != nil {
			return nil, err
		}
		return scanStrings(rows)
	}
	return nil, fmt.Errorf("unsupported engine %q", engine)
}

// buildSelect builds "SELECT <every column> FROM <table>" with engine quoting.
func buildSelect(ctx context.Context, engine string, db *sql.DB, table string) (string, error) {
	if b := backendOf(db); b != nil {
		return b.SelectFor(ctx, table)
	}
	cols, err := listColumns(ctx, engine, db, table)
	if err != nil {
		return "", err
	}
	if len(cols) == 0 {
		return "SELECT * FROM " + quoteObject(engine, table), nil
	}
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = quoteIdent(engine, c)
	}
	return "SELECT " + strings.Join(quoted, ", ") + "\nFROM " + quoteObject(engine, table), nil
}

// quoteObject quotes a table or view name. SQL Server and PostgreSQL objects
// are listed as "schema.name" and quoted as [schema].[name] / "schema"."name".
func quoteObject(engine, name string) string {
	if engine == EngineMSSQL || engine == EnginePostgres || engine == EngineOracle {
		if schema, obj, ok := strings.Cut(name, "."); ok {
			return quoteIdent(engine, schema) + "." + quoteIdent(engine, obj)
		}
	}
	return quoteIdent(engine, name)
}

func quoteIdent(engine, name string) string {
	switch engine {
	case EngineMySQL, EngineMariaDB:
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	case EngineMSSQL:
		return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
	default:
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	}
}

func queryStrings(ctx context.Context, db *sql.DB, q string) ([]string, error) {
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	return scanStrings(rows)
}

func scanStrings(rows *sql.Rows) ([]string, error) {
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s sql.NullString
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, strings.TrimSpace(s.String))
	}
	return out, rows.Err()
}

// RowFunc receives each row as JSON-friendly values. Returning false stops iteration.
type RowFunc func(row []any) (bool, error)

// streamQuery runs q and calls header once with the column names, then fn per row.
// The query is checked by checkReadOnly and, for server engines, executed in a
// READ ONLY transaction that is rolled back afterwards, so it cannot change data.
func streamQuery(ctx context.Context, engine string, db *sql.DB, q string, header func(cols []string) error, fn RowFunc) error {
	var types []string
	return streamRows(ctx, engine, db, q, func(cols, t []string) error {
		types = t
		return header(cols)
	}, func(raw []any) (bool, error) {
		row := make([]any, len(raw))
		for i, v := range raw {
			row[i] = normalize(v, types[i])
		}
		return fn(row)
	})
}

// streamRows is streamQuery with the values as the driver returns them, and
// the upper-case database type name of each column. raw is reused per row.
func streamRows(ctx context.Context, engine string, db *sql.DB, q string, header func(cols, types []string) error, fn func(raw []any) (bool, error)) error {
	if err := checkReadOnly(engine, q); err != nil {
		return err
	}
	var rows *sql.Rows
	var err error
	switch {
	case engine == EngineSQLite || backendOf(db) != nil:
		// The SQLite connection itself is opened read-only (mode=ro,
		// query_only); a backend only reads.
		rows, err = db.QueryContext(ctx, q)
	case engine == EngineOracle:
		// go-ora refuses TxOptions.ReadOnly: the transaction is made read-only
		// with its first statement, and rolled back like the others.
		var tx *sql.Tx
		if tx, err = db.BeginTx(ctx, nil); err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, "SET TRANSACTION READ ONLY"); err != nil {
			return err
		}
		rows, err = tx.QueryContext(ctx, q)
	default:
		// SQL Server has no READ ONLY transactions, but its DML and DDL are
		// transactional, so the unconditional rollback below undoes them.
		opts := &sql.TxOptions{ReadOnly: engine != EngineMSSQL}
		var tx *sql.Tx
		tx, err = db.BeginTx(ctx, opts)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		rows, err = tx.QueryContext(ctx, q)
	}
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	types := make([]string, len(cols))
	if cts, err := rows.ColumnTypes(); err == nil && len(cts) == len(cols) {
		for i, ct := range cts {
			types[i] = strings.ToUpper(ct.DatabaseTypeName())
			// Firebird reports NUMERIC(p,s) by storage type (e.g. INT64) with a scale.
			if _, scale, ok := ct.DecimalSize(); ok && scale != 0 && strings.Contains(types[i], "INT") {
				types[i] = "DECIMAL"
			}
		}
	}
	if err := header(cols, types); err != nil {
		return err
	}
	raw := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range raw {
		ptrs[i] = &raw[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		more, err := fn(raw)
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
	}
	return rows.Err()
}

// normalize converts a driver value to nil, bool, int64, float64, json.Number
// (exact DECIMAL/NUMERIC values) or string. dbType is the column's database
// type name, used to format dates and recognise decimals.
func normalize(v any, dbType string) any {
	out := normalizeValue(v, dbType)
	if s, ok := out.(string); ok && isDecimalType(dbType) {
		if _, err := strconv.ParseFloat(s, 64); err == nil {
			return json.Number(s)
		}
	}
	return out
}

func isDecimalType(t string) bool {
	return strings.Contains(t, "DECIMAL") || strings.Contains(t, "NUMERIC") || strings.Contains(t, "MONEY")
}

func normalizeValue(v any, dbType string) any {
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
		if dbType == "UNIQUEIDENTIFIER" && len(t) == 16 {
			return formatGUID(t)
		}
		if utf8.Valid(t) {
			return string(t)
		}
		return "0x" + hex.EncodeToString(t)
	case string:
		return t
	case bool, int64, float64:
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
		return strconv.FormatUint(t, 10)
	case float32:
		return float64(t)
	case time.Time:
		switch {
		case dbType == "DATE":
			return t.Format("2006-01-02")
		case strings.HasPrefix(dbType, "TIME") && !strings.HasPrefix(dbType, "TIMESTAMP"):
			return t.Format("15:04:05.999999")
		}
		if strings.Contains(dbType, "TZ") || strings.Contains(dbType, "OFFSET") || strings.Contains(dbType, "TIME ZONE") {
			return t.Format("2006-01-02 15:04:05.999999-07:00") // keep the offset of zone-aware types
		}
		return t.Format("2006-01-02 15:04:05.999999")
	default:
		// Exact numeric types such as Firebird's scaled NUMERIC (decimal.Decimal)
		// or big integers: keep every digit by emitting a JSON number literal.
		s := fmt.Sprint(t)
		if plainNumber.MatchString(s) {
			return json.Number(s)
		}
		return s
	}
}

var plainNumber = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

// formatGUID renders SQL Server's mixed-endian UNIQUEIDENTIFIER bytes.
func formatGUID(b []byte) string {
	return fmt.Sprintf("%02X%02X%02X%02X-%02X%02X-%02X%02X-%02X%02X-%02X%02X%02X%02X%02X%02X",
		b[3], b[2], b[1], b[0], b[5], b[4], b[7], b[6], b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

// inContainer reports whether cactusdb runs in a container (the image sets
// CACTUSDB_CONTAINER; Docker and Podman also leave a marker file).
var inContainer = os.Getenv("CACTUSDB_CONTAINER") != "" || fileExists("/.dockerenv") || fileExists("/run/.containerenv")

// explainConnErr adds a hint to a failed connection to localhost from inside
// a container, where localhost is the container itself rather than the
// computer running it.
func explainConnErr(p ConnParams, err error) error {
	if err == nil || !inContainer || p.Engine == EngineSQLite {
		return err
	}
	host, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(p.Host)), `\`) // SQL Server: host\INSTANCE
	if host != "" && host != "localhost" && host != "::1" && host != "[::1]" && !strings.HasPrefix(host, "127.") {
		return err
	}
	if host == "" {
		host = "localhost"
	}
	return fmt.Errorf("%w\n\ncactusdb runs in a container, where %q is the container itself, not this computer. "+
		"For a database on the computer that runs Docker (also one in another container that publishes its port there), "+
		"use the host host.docker.internal; for another server, its name or IP address", err, host)
}
