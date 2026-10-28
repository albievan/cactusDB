package main

import (
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
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	_ "github.com/nakagami/firebirdsql"
	_ "modernc.org/sqlite"
)

// ConnParams describes how to reach a database. For SQLite only Database
// (the file path) is used. For SQL Server, Host may name an instance
// (host\INSTANCE).
type ConnParams struct {
	Engine   string `json:"engine"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	Database string `json:"database"`
}

const (
	EngineSQLite   = "sqlite"
	EngineMySQL    = "mysql"
	EngineMariaDB  = "mariadb"
	EngineFirebird = "firebird"
	EngineMSSQL    = "mssql"
	EnginePostgres = "postgres"
)

var engines = []map[string]any{
	{"id": EngineSQLite, "name": "SQLite", "defaultPort": 0},
	{"id": EngineMySQL, "name": "MySQL", "defaultPort": 3306},
	{"id": EngineMariaDB, "name": "MariaDB", "defaultPort": 3306},
	{"id": EngineFirebird, "name": "Firebird", "defaultPort": 3050},
	{"id": EngineMSSQL, "name": "SQL Server", "defaultPort": 1433},
	{"id": EnginePostgres, "name": "PostgreSQL", "defaultPort": 5432},
}

// driverModules maps each engine to the Go module that implements it. All of
// them are pure Go and compiled into the binary, so unlike JDBC-based tools
// nothing has to be downloaded or installed on the host.
var driverModules = []struct{ Engines, Module string }{
	{"SQLite", "modernc.org/sqlite"},
	{"MySQL, MariaDB", "github.com/go-sql-driver/mysql"},
	{"Firebird", "github.com/nakagami/firebirdsql"},
	{"SQL Server", "github.com/microsoft/go-mssqldb"},
	{"PostgreSQL", "github.com/jackc/pgx/v5"},
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
		out = append(out, map[string]string{"engines": d.Engines, "module": d.Module, "version": v, "status": "bundled"})
	}
	return out
}

// openDB opens and pings a database for the given parameters.
func openDB(ctx context.Context, p ConnParams) (*sql.DB, error) {
	var driver, dsn string
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
	case EngineMSSQL:
		driver = "sqlserver"
		dsn = mssqlDSN(p)
	case EnginePostgres:
		driver = "pgx"
		dsn = postgresDSN(p)
	default:
		return nil, fmt.Errorf("unsupported engine %q", p.Engine)
	}

	maxConns := 0
	if p.Engine == EngineSQLite {
		maxConns = 1
	}
	return openAndPing(ctx, driver, dsn, maxConns)
}

func openAndPing(ctx context.Context, driver, dsn string, maxConns int) (*sql.DB, error) {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxConns)
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
func mssqlDSN(p ConnParams) string {
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
	// Most servers use self-signed certificates; accept them like SSMS's
	// "Trust server certificate" option.
	q.Set("TrustServerCertificate", "true")
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
func postgresDSN(p ConnParams) string {
	q := url.Values{}
	q.Set("application_name", "cactusdb")
	q.Set("connect_timeout", "10")
	q.Set("default_transaction_read_only", "on")
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(p.Username, p.Password),
		Host:     hostPort(p.Host, p.Port, 5432),
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
	if engine == EngineMSSQL || engine == EnginePostgres {
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
	if engine == EngineSQLite {
		// The SQLite connection itself is opened read-only (mode=ro, query_only).
		rows, err = db.QueryContext(ctx, q)
	} else {
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
