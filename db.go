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
	_ "github.com/nakagami/firebirdsql"
	_ "modernc.org/sqlite"
)

// ConnParams describes how to reach a database. For SQLite only Database
// (the file path) is used.
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
)

var engines = []map[string]any{
	{"id": EngineSQLite, "name": "SQLite", "defaultPort": 0},
	{"id": EngineMySQL, "name": "MySQL", "defaultPort": 3306},
	{"id": EngineMariaDB, "name": "MariaDB", "defaultPort": 3306},
	{"id": EngineFirebird, "name": "Firebird", "defaultPort": 3050},
}

// driverModules maps each engine to the Go module that implements it. All of
// them are pure Go and compiled into the binary, so unlike JDBC-based tools
// nothing has to be downloaded or installed on the host.
var driverModules = []struct{ Engines, Module string }{
	{"SQLite", "modernc.org/sqlite"},
	{"MySQL, MariaDB", "github.com/go-sql-driver/mysql"},
	{"Firebird", "github.com/nakagami/firebirdsql"},
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

func hostPort(host string, port, def int) string {
	if host == "" {
		host = "localhost"
	}
	if port == 0 {
		port = def
	}
	return host + ":" + strconv.Itoa(port)
}

// listTables returns all user tables and views of the connected database.
func listTables(ctx context.Context, engine string, db *sql.DB) ([]string, error) {
	var q string
	switch engine {
	case EngineSQLite:
		q = `SELECT name FROM sqlite_master WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' ORDER BY name`
	case EngineMySQL, EngineMariaDB:
		q = `SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() ORDER BY table_name`
	case EngineFirebird:
		q = `SELECT TRIM(RDB$RELATION_NAME) FROM RDB$RELATIONS WHERE COALESCE(RDB$SYSTEM_FLAG, 0) = 0 ORDER BY 1`
	default:
		return nil, fmt.Errorf("unsupported engine %q", engine)
	}
	return queryStrings(ctx, db, q)
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
		return "SELECT * FROM " + quoteIdent(engine, table), nil
	}
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = quoteIdent(engine, c)
	}
	return "SELECT " + strings.Join(quoted, ", ") + "\nFROM " + quoteIdent(engine, table), nil
}

func quoteIdent(engine, name string) string {
	switch engine {
	case EngineMySQL, EngineMariaDB:
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
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
	if err := checkReadOnly(engine, q); err != nil {
		return err
	}
	var rows *sql.Rows
	var err error
	if engine == EngineSQLite {
		// The SQLite connection itself is opened read-only (mode=ro, query_only).
		rows, err = db.QueryContext(ctx, q)
	} else {
		var tx *sql.Tx
		tx, err = db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
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
	if err := header(cols); err != nil {
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
		row := make([]any, len(cols))
		for i, v := range raw {
			row[i] = normalize(v, types[i])
		}
		more, err := fn(row)
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
	return strings.Contains(t, "DECIMAL") || strings.Contains(t, "NUMERIC")
}

func normalizeValue(v any, dbType string) any {
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
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
