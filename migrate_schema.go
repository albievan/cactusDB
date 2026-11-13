package main

// ☰ → Migrate: the tables of a database described without its engine (the
// columns' kind, size, nullability, default and identity; the primary key,
// unique constraints, indexes, foreign keys and checks), read from the
// source's catalog and written as the target engine's DDL.

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Column kinds: what a column holds, whatever the engine calls it.
const (
	kBool        = "bool"
	kInt16       = "int16"
	kInt32       = "int32"
	kInt64       = "int64"
	kDecimal     = "decimal"
	kFloat32     = "float32"
	kFloat64     = "float64"
	kChar        = "char"
	kVarchar     = "varchar"
	kText        = "text"
	kBinary      = "binary"
	kVarbinary   = "varbinary"
	kBlob        = "blob"
	kDate        = "date"
	kTime        = "time"
	kTimestamp   = "timestamp"
	kTimestampTZ = "timestamptz"
	kUUID        = "uuid"
	kJSON        = "json"
)

type mCol struct {
	Name, Src   string // the target's name, the source's
	SrcType     string // the source's type, for the report
	Kind        string
	Len         int64 // characters (char, varchar) or bytes (binary, varbinary)
	Prec, Scale int64
	Nullable    bool
	Default     string // neutral: a number, a 'string', TRUE, FALSE, CURRENT_TIMESTAMP, CURRENT_DATE or CURRENT_TIME
	Identity    bool
	Computed    bool // a computed column of the source: its values are copied
}

type mIndex struct {
	Name   string
	Cols   []string
	Desc   bool
	Unique bool
	Cons   bool // a unique constraint (else an index)
}

type mFK struct {
	Name               string
	Cols, RefCols      []string
	RefTable           string // the source name of the referenced table
	OnDelete, OnUpdate string // CASCADE, SET NULL, SET DEFAULT or "" (no action)
}

type mCheck struct{ Name, Expr string }

type mTable struct {
	Name, Src string // the target's name, the source's (as listObjects lists it)
	Cols      []mCol
	PK        []string
	PKName    string
	Indexes   []mIndex
	FKs       []mFK
	Checks    []mCheck
	Notes     []string
}

func (t *mTable) col(src string) *mCol {
	for i := range t.Cols {
		if strings.EqualFold(t.Cols[i].Src, src) {
			return &t.Cols[i]
		}
	}
	return nil
}

type mSeq struct {
	Name, Src   string
	Next, Incr  int64 // the next value it gives, its increment
	SourceKnown bool
}

// ---- types ----------------------------------------------------------------

var reTypeArgs = regexp.MustCompile(`\(([^()]*)\)`)

// classifyType turns a source type name, e.g. "NUMERIC(15,2)", "character
// varying(80)", "int(11) unsigned" or "BLOB SUB_TYPE TEXT", into a kind.
func classifyType(engine, raw string) (kind string, l, p, s int64) {
	t := strings.ToUpper(strings.TrimSpace(raw))
	var args []int64
	if m := reTypeArgs.FindStringSubmatchIndex(t); m != nil {
		for _, a := range strings.Split(t[m[2]:m[3]], ",") {
			a = strings.TrimSpace(a)
			if a == "MAX" {
				args = append(args, -1)
			} else if n, err := strconv.ParseInt(a, 10, 64); err == nil {
				args = append(args, n)
			}
		}
		t = strings.TrimSpace(t[:m[0]] + " " + t[m[1]:])
	}
	arg := func(i int, def int64) int64 {
		if i < len(args) {
			return args[i]
		}
		return def
	}
	if strings.HasSuffix(t, "[]") || strings.Contains(t, " ARRAY") {
		return kText, 0, 0, 0
	}
	t = strings.Join(strings.Fields(t), " ")
	unsigned := strings.Contains(t, "UNSIGNED")
	octets := strings.Contains(t, "OCTETS")
	tz := strings.Contains(t, "WITH TIME ZONE") || strings.HasSuffix(t, "TZ")
	word := strings.Fields(t + " ?")[0]
	switch {
	case word == "BOOL" || word == "BOOLEAN":
		return kBool, 0, 0, 0
	case word == "BIT":
		if engine == EngineMSSQL || arg(0, 1) == 1 {
			return kBool, 0, 0, 0
		}
		return kVarbinary, (arg(0, 1) + 7) / 8, 0, 0
	case word == "TINYINT":
		// MySQL's TINYINT(1) is often used as a flag, but holds -128..127: a
		// BOOLEAN would turn 2 into TRUE. A small integer keeps every value.
		return kInt16, 0, 0, 0
	case (mysqlFamily(engine) || engine == EngineMSSQL) && spatialTypes[word]:
		// Binary values (WKB, SQL Server's own format): as text they would
		// be invalid UTF-8 and fail the copy.
		return kBlob, 0, 0, 0
	case word == "SMALLINT" || word == "INT2" || word == "SMALLSERIAL" || word == "YEAR":
		if unsigned {
			return kInt32, 0, 0, 0
		}
		return kInt16, 0, 0, 0
	case word == "MEDIUMINT" || word == "INT" || word == "INTEGER" || word == "INT4" || word == "SERIAL":
		if unsigned || engine == EngineSQLite {
			return kInt64, 0, 0, 0
		}
		return kInt32, 0, 0, 0
	case word == "BIGINT" || word == "INT8" || word == "BIGSERIAL":
		if unsigned {
			return kDecimal, 0, 20, 0
		}
		return kInt64, 0, 0, 0
	case word == "INT128":
		return kDecimal, 0, 38, 0
	case word == "DECIMAL" || word == "NUMERIC" || word == "DEC" || word == "NUMBER":
		return kDecimal, 0, arg(0, 0), arg(1, 0)
	case word == "MONEY":
		return kDecimal, 0, 19, 4
	case word == "SMALLMONEY":
		return kDecimal, 0, 10, 4
	case word == "DECFLOAT":
		return kDecimal, 0, 34, 8
	case word == "REAL" || word == "FLOAT4":
		if engine == EngineSQLite {
			return kFloat64, 0, 0, 0
		}
		return kFloat32, 0, 0, 0
	case word == "FLOAT":
		if engine == EngineMSSQL && arg(0, 53) > 24 || engine == EngineSQLite || len(args) > 0 && args[0] > 24 {
			return kFloat64, 0, 0, 0
		}
		return kFloat32, 0, 0, 0
	case word == "DOUBLE" || word == "FLOAT8" || word == "BINARY_DOUBLE":
		return kFloat64, 0, 0, 0
	case word == "BINARY_FLOAT":
		return kFloat32, 0, 0, 0
	case word == "BLOB" && strings.Contains(t, "SUB_TYPE"):
		if strings.Contains(t, "TEXT") || strings.Contains(t, "SUB_TYPE 1") {
			return kText, 0, 0, 0
		}
		return kBlob, 0, 0, 0
	case word == "CHAR" || word == "CHARACTER" || word == "NCHAR" || word == "BPCHAR" || word == "NATIONAL":
		if strings.Contains(t, "VARYING") {
			return textKind(kVarchar, arg(0, 0), octets)
		}
		return textKind(kChar, arg(0, 1), octets)
	case word == "VARCHAR" || word == "NVARCHAR" || word == "VARCHAR2" || word == "NVARCHAR2" || word == "CSTRING" || word == "VARYING":
		return textKind(kVarchar, arg(0, 0), octets)
	case word == "TEXT" || word == "NTEXT" || word == "TINYTEXT" || word == "MEDIUMTEXT" || word == "LONGTEXT" || word == "CLOB" ||
		word == "NCLOB" || word == "CITEXT" || word == "XML" || word == "LONG" || word == "STRING" || word == "SYSNAME" || word == "NAME":
		return kText, 0, 0, 0
	case word == "ENUM" || word == "SET":
		return kVarchar, 0, 0, 0
	case word == "BINARY":
		return kBinary, arg(0, 1), 0, 0
	case word == "VARBINARY" || word == "RAW":
		if arg(0, -1) <= 0 {
			return kBlob, 0, 0, 0
		}
		return kVarbinary, args[0], 0, 0
	case word == "BLOB" || word == "TINYBLOB" || word == "MEDIUMBLOB" || word == "LONGBLOB" || word == "BYTEA" || word == "IMAGE" || word == "LONGVARBINARY":
		return kBlob, 0, 0, 0
	case word == "ROWVERSION" || word == "TIMESTAMP" && engine == EngineMSSQL:
		return kBinary, 8, 0, 0
	case word == "DATE":
		if engine == EngineOracle {
			return kTimestamp, 0, 0, 0
		}
		return kDate, 0, 0, 0
	case word == "TIME" || word == "TIMETZ":
		return kTime, 0, 0, 0
	case word == "DATETIMEOFFSET" || word == "TIMESTAMPTZ" || word == "TIMESTAMP" && tz:
		return kTimestampTZ, 0, 0, 0
	case word == "DATETIME" || word == "DATETIME2" || word == "SMALLDATETIME" || word == "TIMESTAMP":
		return kTimestamp, 0, 0, 0
	case word == "UUID" || word == "UNIQUEIDENTIFIER":
		return kUUID, 0, 0, 0
	case word == "JSON" || word == "JSONB":
		return kJSON, 0, 0, 0
	}
	if engine == EngineSQLite { // the affinity rules
		switch {
		case strings.Contains(t, "INT"):
			return kInt64, 0, 0, 0
		case strings.Contains(t, "CHAR") || strings.Contains(t, "CLOB") || strings.Contains(t, "TEXT"):
			return kText, 0, 0, 0
		case t == "" || strings.Contains(t, "BLOB"):
			return kBlob, 0, 0, 0
		case strings.Contains(t, "REAL") || strings.Contains(t, "FLOA") || strings.Contains(t, "DOUB"):
			return kFloat64, 0, 0, 0
		}
		return kDecimal, 0, 0, 0
	}
	return kText, 0, 0, 0 // intervals, geometry, network types…: as their text
}

var spatialTypes = map[string]bool{"GEOMETRY": true, "POINT": true, "LINESTRING": true, "POLYGON": true, "MULTIPOINT": true,
	"MULTILINESTRING": true, "MULTIPOLYGON": true, "GEOMETRYCOLLECTION": true, "GEOMCOLLECTION": true, "GEOGRAPHY": true, "HIERARCHYID": true}

func textKind(k string, n int64, octets bool) (string, int64, int64, int64) {
	if octets {
		if k == kChar {
			return kBinary, n, 0, 0
		}
		return kVarbinary, n, 0, 0
	}
	if n <= 0 {
		return kText, 0, 0, 0
	}
	return k, n, 0, 0
}

// colType renders a column's type for the target engine.
func colType(target string, c *mCol, fbMajor int) string {
	l := c.Len
	switch target {
	case EngineMySQL, EngineMariaDB:
		switch c.Kind {
		case kBool:
			return "BOOLEAN"
		case kInt16:
			return "SMALLINT"
		case kInt32:
			return "INT"
		case kInt64:
			return "BIGINT"
		case kDecimal:
			return decimalType("DECIMAL", c, 65, 30)
		case kFloat32:
			return "FLOAT"
		case kFloat64:
			return "DOUBLE"
		case kChar:
			if l <= 255 {
				return fmt.Sprintf("CHAR(%d)", l)
			}
			return fmt.Sprintf("VARCHAR(%d)", l)
		case kVarchar:
			if l <= 16000 {
				return fmt.Sprintf("VARCHAR(%d)", l)
			}
			return "LONGTEXT"
		case kText:
			return "LONGTEXT"
		case kBinary:
			if l <= 255 {
				return fmt.Sprintf("BINARY(%d)", l)
			}
			return fmt.Sprintf("VARBINARY(%d)", l)
		case kVarbinary:
			if l <= 16000 {
				return fmt.Sprintf("VARBINARY(%d)", l)
			}
			return "LONGBLOB"
		case kBlob:
			return "LONGBLOB"
		case kDate:
			return "DATE"
		case kTime:
			return "TIME(6)"
		case kTimestamp, kTimestampTZ:
			return "DATETIME(6)"
		case kUUID:
			return "CHAR(36)"
		case kJSON:
			return "JSON"
		}
	case EnginePostgres:
		switch c.Kind {
		case kBool:
			return "BOOLEAN"
		case kInt16:
			return "SMALLINT"
		case kInt32:
			return "INTEGER"
		case kInt64:
			return "BIGINT"
		case kDecimal:
			return decimalType("NUMERIC", c, 1000, 1000)
		case kFloat32:
			return "REAL"
		case kFloat64:
			return "DOUBLE PRECISION"
		case kChar:
			return fmt.Sprintf("CHAR(%d)", l)
		case kVarchar:
			return fmt.Sprintf("VARCHAR(%d)", l)
		case kText:
			return "TEXT"
		case kBinary, kVarbinary, kBlob:
			return "BYTEA"
		case kDate:
			return "DATE"
		case kTime:
			return "TIME"
		case kTimestamp:
			return "TIMESTAMP"
		case kTimestampTZ:
			return "TIMESTAMPTZ"
		case kUUID:
			return "UUID"
		case kJSON:
			return "JSON"
		}
	case EngineMSSQL:
		switch c.Kind {
		case kBool:
			return "BIT"
		case kInt16:
			return "SMALLINT"
		case kInt32:
			return "INT"
		case kInt64:
			return "BIGINT"
		case kDecimal:
			return decimalType("DECIMAL", c, 38, 38)
		case kFloat32:
			return "REAL"
		case kFloat64:
			return "FLOAT"
		case kChar:
			if l <= 4000 {
				return fmt.Sprintf("NCHAR(%d)", l)
			}
			return "NVARCHAR(MAX)"
		case kVarchar:
			if l <= 4000 {
				return fmt.Sprintf("NVARCHAR(%d)", l)
			}
			return "NVARCHAR(MAX)"
		case kText, kJSON:
			return "NVARCHAR(MAX)"
		case kBinary:
			if l <= 8000 {
				return fmt.Sprintf("BINARY(%d)", l)
			}
			return "VARBINARY(MAX)"
		case kVarbinary:
			if l <= 8000 {
				return fmt.Sprintf("VARBINARY(%d)", l)
			}
			return "VARBINARY(MAX)"
		case kBlob:
			return "VARBINARY(MAX)"
		case kDate:
			return "DATE"
		case kTime:
			return "TIME(7)"
		case kTimestamp:
			return "DATETIME2(7)"
		case kTimestampTZ:
			return "DATETIMEOFFSET(7)"
		case kUUID:
			return "UNIQUEIDENTIFIER"
		}
	case EngineSQLite:
		switch c.Kind {
		case kBool:
			return "BOOLEAN"
		case kInt16, kInt32, kInt64:
			return "INTEGER"
		case kDecimal:
			return decimalType("NUMERIC", c, 1000, 1000)
		case kFloat32, kFloat64:
			return "REAL"
		case kChar:
			return fmt.Sprintf("CHAR(%d)", l)
		case kVarchar:
			return fmt.Sprintf("VARCHAR(%d)", l)
		case kText, kJSON:
			return "TEXT"
		case kBinary, kVarbinary, kBlob:
			return "BLOB"
		case kDate:
			return "DATE"
		case kTime:
			return "TIME"
		case kTimestamp, kTimestampTZ:
			return "TIMESTAMP"
		case kUUID:
			return "VARCHAR(36)"
		}
	case EngineFirebird:
		maxP := int64(18)
		if fbMajor >= 4 {
			maxP = 38
		}
		switch c.Kind {
		case kBool:
			return "BOOLEAN"
		case kInt16:
			return "SMALLINT"
		case kInt32:
			return "INTEGER"
		case kInt64:
			return "BIGINT"
		case kDecimal:
			return decimalType("NUMERIC", c, maxP, maxP)
		case kFloat32:
			return "FLOAT"
		case kFloat64:
			return "DOUBLE PRECISION"
		case kChar:
			if l <= 8191 {
				return fmt.Sprintf("CHAR(%d)", l)
			}
			return "BLOB SUB_TYPE TEXT"
		case kVarchar:
			if l <= 8191 {
				return fmt.Sprintf("VARCHAR(%d)", l)
			}
			return "BLOB SUB_TYPE TEXT"
		case kText, kJSON:
			return "BLOB SUB_TYPE TEXT"
		case kBinary:
			if l <= 32767 {
				return fmt.Sprintf("CHAR(%d) CHARACTER SET OCTETS", l)
			}
			return "BLOB SUB_TYPE BINARY"
		case kVarbinary:
			if l <= 32765 {
				return fmt.Sprintf("VARCHAR(%d) CHARACTER SET OCTETS", l)
			}
			return "BLOB SUB_TYPE BINARY"
		case kBlob:
			return "BLOB SUB_TYPE BINARY"
		case kDate:
			return "DATE"
		case kTime:
			return "TIME"
		case kTimestamp:
			return "TIMESTAMP"
		case kTimestampTZ:
			if fbMajor >= 4 {
				return "TIMESTAMP WITH TIME ZONE"
			}
			return "TIMESTAMP"
		case kUUID:
			return "CHAR(36)"
		}
	}
	return "TEXT"
}

// decimalType renders DECIMAL(p,s) within the target's limits; a decimal
// without a precision gets the widest the target has room for.
func decimalType(name string, c *mCol, maxP, maxS int64) string {
	p, s := c.Prec, c.Scale
	if p <= 0 {
		if maxP >= 1000 {
			return name
		}
		p, s = min(maxP, 38), min(10, maxS)
	}
	p = min(p, maxP)
	s = min(max(s, 0), p, maxS)
	return fmt.Sprintf("%s(%d,%d)", name, p, s)
}

// ---- defaults ---------------------------------------------------------------

var (
	reDefaultCast = regexp.MustCompile(`::[a-zA-Z_][a-zA-Z0-9_ ]*(\[\])?(\([0-9, ]*\))?$`)
	reNumber      = regexp.MustCompile(`^[-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$`)
)

// neutralDefault reads a source column default as a neutral one ("" when it
// is none, or an expression only the source understands).
func neutralDefault(engine, def string, kind string) string {
	d := strings.TrimSpace(def)
	if len(d) >= 8 && strings.EqualFold(d[:8], "DEFAULT ") {
		d = strings.TrimSpace(d[8:])
	}
	for len(d) >= 2 && d[0] == '(' && d[len(d)-1] == ')' { // SQL Server: ((0)), ('x')
		d = strings.TrimSpace(d[1 : len(d)-1])
	}
	d = reDefaultCast.ReplaceAllString(d, "") // PostgreSQL: 'x'::character varying
	u := strings.ToUpper(d)
	// MySQL's zero date is no date any other database takes: the default is
	// left out (with a note) rather than failing the whole table.
	if strings.HasPrefix(u, "'0000-00-00") {
		return ""
	}
	switch u {
	case "", "NULL":
		return ""
	case "CURRENT_TIMESTAMP", "NOW()", "GETDATE()", "SYSDATETIME()", "CURRENT_TIMESTAMP()", "LOCALTIMESTAMP", "'NOW'", "SYSDATE",
		"SYSTIMESTAMP", "TRANSACTION_TIMESTAMP()", "STATEMENT_TIMESTAMP()", "GETUTCDATE()", "SYSUTCDATETIME()", "DATETIME('NOW')":
		if kind == kDate {
			return "CURRENT_DATE"
		}
		return "CURRENT_TIMESTAMP"
	case "CURRENT_DATE", "CURDATE()", "'TODAY'", "CURRENT_DATE()", "DATE('NOW')":
		return "CURRENT_DATE"
	case "CURRENT_TIME", "CURTIME()", "LOCALTIME", "CURRENT_TIME()":
		return "CURRENT_TIME"
	case "TRUE", "B'1'", "'T'", "'TRUE'":
		if kind == kBool {
			return "TRUE"
		}
	case "FALSE", "B'0'", "'F'", "'FALSE'":
		if kind == kBool {
			return "FALSE"
		}
	}
	if strings.HasPrefix(u, "CURRENT_TIMESTAMP(") || strings.HasPrefix(u, "NOW(") {
		return "CURRENT_TIMESTAMP"
	}
	if reNumber.MatchString(d) {
		if kind == kBool {
			if n, _ := strconv.ParseFloat(d, 64); n != 0 {
				return "TRUE"
			}
			return "FALSE"
		}
		return d
	}
	if len(d) >= 2 && d[0] == '\'' && d[len(d)-1] == '\'' {
		return d
	}
	if len(d) >= 3 && (d[0] == 'N' || d[0] == 'E') && d[1] == '\'' && d[len(d)-1] == '\'' {
		return d[1:]
	}
	return "" // an expression: left out, and noted
}

// renderDefault writes a neutral default for the target.
func renderDefault(target string, c *mCol) string {
	d := c.Default
	switch d {
	case "":
		return ""
	case "TRUE", "FALSE":
		if target == EngineMSSQL || target == EngineSQLite || mysqlFamily(target) {
			return map[string]string{"TRUE": "1", "FALSE": "0"}[d]
		}
		return d
	case "CURRENT_TIMESTAMP":
		if mysqlFamily(target) {
			return "CURRENT_TIMESTAMP(6)"
		}
		return d
	case "CURRENT_DATE":
		switch {
		case mysqlFamily(target):
			return "(CURRENT_DATE)"
		case target == EngineMSSQL:
			return "CAST(GETDATE() AS DATE)"
		}
		return d
	case "CURRENT_TIME":
		switch {
		case mysqlFamily(target):
			return "(CURRENT_TIME)"
		case target == EngineMSSQL:
			return "CAST(GETDATE() AS TIME)"
		}
		return d
	}
	if mysqlFamily(target) && (c.Kind == kText || c.Kind == kBlob || c.Kind == kJSON || c.Kind == kVarchar && c.Len > 16000) {
		return "(" + d + ")" // MySQL: a TEXT or BLOB default must be an expression
	}
	if d[0] == '\'' && (target == EngineMSSQL) {
		return "N" + d
	}
	return d
}

// ---- reading the source -------------------------------------------------------

// readTable reads one table of the source.
func readTable(ctx context.Context, engine string, db *sql.DB, name string) (*mTable, error) {
	t := &mTable{Src: name}
	var err error
	switch engine {
	case EngineMySQL, EngineMariaDB:
		err = mysqlTable(ctx, db, t)
	case EnginePostgres:
		err = postgresTable(ctx, db, t)
	case EngineMSSQL:
		err = mssqlTable(ctx, db, t)
	case EngineFirebird:
		err = firebirdTable(ctx, db, t)
	case EngineSQLite:
		err = sqliteTable(ctx, db, t)
	default:
		err = genericTable(ctx, engine, db, t)
	}
	if err != nil {
		return nil, err
	}
	if len(t.Cols) == 0 {
		return nil, fmt.Errorf("no columns found")
	}
	return t, nil
}

func scanRows(ctx context.Context, db *sql.DB, q string, args []any, fn func(scan func(...any) error) error) error {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows.Scan); err != nil {
			return err
		}
	}
	return rows.Err()
}

func fkAction(a string) string {
	a = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(a), "_", " "))
	switch a {
	case "CASCADE", "SET NULL", "SET DEFAULT":
		return a
	}
	return ""
}

// addIndex adds a column to the index named name (indexes are read one
// column per row, in order).
func addIndex(t *mTable, name, col string, unique, desc, cons bool) {
	for i := range t.Indexes {
		if t.Indexes[i].Name == name {
			t.Indexes[i].Cols = append(t.Indexes[i].Cols, col)
			t.Indexes[i].Desc = t.Indexes[i].Desc || desc
			return
		}
	}
	t.Indexes = append(t.Indexes, mIndex{Name: name, Cols: []string{col}, Unique: unique, Desc: desc, Cons: cons})
}

func addFK(t *mTable, name, col, refTable, refCol, onDel, onUpd string) {
	for i := range t.FKs {
		if t.FKs[i].Name == name {
			t.FKs[i].Cols = append(t.FKs[i].Cols, col)
			t.FKs[i].RefCols = append(t.FKs[i].RefCols, refCol)
			return
		}
	}
	t.FKs = append(t.FKs, mFK{Name: name, Cols: []string{col}, RefTable: refTable, RefCols: []string{refCol}, OnDelete: fkAction(onDel), OnUpdate: fkAction(onUpd)})
}

func mysqlTable(ctx context.Context, db *sql.DB, t *mTable) error {
	mariadb := false
	var ver string
	if db.QueryRowContext(ctx, `SELECT VERSION()`).Scan(&ver) == nil {
		mariadb = strings.Contains(strings.ToLower(ver), "mariadb")
	}
	err := scanRows(ctx, db, `SELECT COLUMN_NAME, COLUMN_TYPE, CHARACTER_MAXIMUM_LENGTH, IS_NULLABLE, COLUMN_DEFAULT, EXTRA
		FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`, []any{t.Src},
		func(scan func(...any) error) error {
			var name, typ, null, extra string
			var clen sql.NullInt64
			var def sql.NullString
			if err := scan(&name, &typ, &clen, &null, &def, &extra); err != nil {
				return err
			}
			c := mCol{Src: name, SrcType: typ, Nullable: null == "YES"}
			c.Kind, c.Len, c.Prec, c.Scale = classifyType(EngineMySQL, typ)
			if c.Kind == kVarchar && c.Len == 0 && clen.Valid { // ENUM, SET
				c.Len = clen.Int64
			}
			ex := strings.ToLower(extra)
			c.Identity = strings.Contains(ex, "auto_increment")
			c.Computed = strings.Contains(ex, "generated") && !strings.Contains(ex, "default_generated")
			if def.Valid && !c.Computed {
				d := def.String
				// MySQL lists a string default unquoted (MariaDB quotes it).
				if !mariadb && !strings.Contains(ex, "default_generated") && !reNumber.MatchString(d) && !strings.HasPrefix(d, "'") {
					d = "'" + strings.ReplaceAll(d, "'", "''") + "'"
				}
				c.Default = neutralDefault(EngineMySQL, d, c.Kind)
				if c.Default == "" && !strings.EqualFold(d, "NULL") {
					t.Notes = append(t.Notes, fmt.Sprintf("the default of %s (%s) isn't carried over", name, d))
				}
			}
			t.Cols = append(t.Cols, c)
			return nil
		})
	if err != nil {
		return err
	}
	err = scanRows(ctx, db, `SELECT INDEX_NAME, NON_UNIQUE, COLUMN_NAME, COLLATION FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? ORDER BY INDEX_NAME, SEQ_IN_INDEX`, []any{t.Src},
		func(scan func(...any) error) error {
			var name string
			var nonUnique int
			var col, coll sql.NullString
			if err := scan(&name, &nonUnique, &col, &coll); err != nil {
				return err
			}
			if !col.Valid {
				t.Notes = append(t.Notes, "the expression index "+name+" isn't carried over")
				return nil
			}
			if name == "PRIMARY" {
				t.PK = append(t.PK, col.String)
				return nil
			}
			addIndex(t, name, col.String, nonUnique == 0, coll.String == "D", false)
			return nil
		})
	if err != nil {
		return err
	}
	err = scanRows(ctx, db, `SELECT k.CONSTRAINT_NAME, k.COLUMN_NAME, k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME, r.DELETE_RULE, r.UPDATE_RULE
		FROM information_schema.KEY_COLUMN_USAGE k JOIN information_schema.REFERENTIAL_CONSTRAINTS r
		  ON r.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA AND r.CONSTRAINT_NAME = k.CONSTRAINT_NAME AND r.TABLE_NAME = k.TABLE_NAME
		WHERE k.TABLE_SCHEMA = DATABASE() AND k.TABLE_NAME = ? AND k.REFERENCED_TABLE_NAME IS NOT NULL
		ORDER BY k.CONSTRAINT_NAME, k.ORDINAL_POSITION`, []any{t.Src},
		func(scan func(...any) error) error {
			var name, col, rt, rc, del, upd string
			if err := scan(&name, &col, &rt, &rc, &del, &upd); err != nil {
				return err
			}
			addFK(t, name, col, rt, rc, del, upd)
			return nil
		})
	if err != nil {
		return err
	}
	// CHECK constraints: MySQL 8.0.16+ and MariaDB 10.2+ (older servers: none).
	scanRows(ctx, db, `SELECT tc.CONSTRAINT_NAME, cc.CHECK_CLAUSE FROM information_schema.TABLE_CONSTRAINTS tc
		JOIN information_schema.CHECK_CONSTRAINTS cc ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
		WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = ? AND tc.CONSTRAINT_TYPE = 'CHECK'`, []any{t.Src},
		func(scan func(...any) error) error {
			var name, expr string
			if scan(&name, &expr) == nil && !strings.Contains(strings.ToLower(expr), "json_valid") {
				t.Checks = append(t.Checks, mCheck{name, expr})
			}
			return nil
		})
	return nil
}

func postgresTable(ctx context.Context, db *sql.DB, t *mTable) error {
	rel := quoteObject(EnginePostgres, t.Src)
	cols := func(q string) error {
		return scanRows(ctx, db, q, []any{rel}, func(scan func(...any) error) error {
			var name, typ, ident, gen string
			var notNull bool
			var def sql.NullString
			if err := scan(&name, &typ, &notNull, &def, &ident, &gen); err != nil {
				return err
			}
			c := mCol{Src: name, SrcType: typ, Nullable: !notNull, Identity: ident != "", Computed: gen != ""}
			c.Kind, c.Len, c.Prec, c.Scale = classifyType(EnginePostgres, typ)
			if def.Valid && !c.Computed {
				if strings.HasPrefix(def.String, "nextval(") {
					c.Identity = true // serial
				} else if c.Default = neutralDefault(EnginePostgres, def.String, c.Kind); c.Default == "" {
					t.Notes = append(t.Notes, fmt.Sprintf("the default of %s (%s) isn't carried over", name, def.String))
				}
			}
			t.Cols = append(t.Cols, c)
			return nil
		})
	}
	const from = ` FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE a.attrelid = $1::regclass AND a.attnum > 0 AND NOT a.attisdropped ORDER BY a.attnum`
	if err := cols(`SELECT a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull, pg_get_expr(d.adbin, d.adrelid),
		a.attidentity::text, a.attgenerated::text` + from); err != nil {
		t.Cols = nil // PostgreSQL 11 and older: no identity or generated columns
		if err := cols(`SELECT a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull, pg_get_expr(d.adbin, d.adrelid), '', ''` + from); err != nil {
			return err
		}
	}
	colList := func(arr, rel string) string {
		return `(SELECT string_agg(a.attname, E'\t' ORDER BY k.n) FROM unnest(` + arr + `) WITH ORDINALITY k(attnum, n)
			JOIN pg_attribute a ON a.attrelid = ` + rel + ` AND a.attnum = k.attnum)`
	}
	err := scanRows(ctx, db, `SELECT c.conname, c.contype::text, COALESCE(`+colList("c.conkey", "c.conrelid")+`, ''),
			COALESCE((SELECT n.nspname || '.' || r.relname FROM pg_class r JOIN pg_namespace n ON n.oid = r.relnamespace WHERE r.oid = c.confrelid), ''),
			COALESCE(`+colList("c.confkey", "c.confrelid")+`, ''), c.confdeltype::text, c.confupdtype::text, pg_get_constraintdef(c.oid)
		FROM pg_constraint c WHERE c.conrelid = $1::regclass ORDER BY c.conname`, []any{rel},
		func(scan func(...any) error) error {
			var name, typ, cols, rt, rcols, del, upd, def string
			if err := scan(&name, &typ, &cols, &rt, &rcols, &del, &upd, &def); err != nil {
				return err
			}
			cl := strings.Split(cols, "\t")
			act := map[string]string{"c": "CASCADE", "n": "SET NULL", "d": "SET DEFAULT"}
			switch typ {
			case "p":
				t.PK, t.PKName = cl, name
			case "u":
				t.Indexes = append(t.Indexes, mIndex{Name: name, Cols: cl, Unique: true, Cons: true})
			case "f":
				t.FKs = append(t.FKs, mFK{Name: name, Cols: cl, RefTable: rt, RefCols: strings.Split(rcols, "\t"), OnDelete: act[del], OnUpdate: act[upd]})
			case "c":
				t.Checks = append(t.Checks, mCheck{name, strings.TrimSpace(strings.TrimPrefix(def, "CHECK"))})
			}
			return nil
		})
	if err != nil {
		return err
	}
	return scanRows(ctx, db, `SELECT i.relname, ix.indisunique, ix.indpred IS NOT NULL,
			COALESCE((SELECT string_agg(CASE WHEN k.attnum = 0 THEN '' ELSE a.attname END || CASE WHEN ix.indoption[k.n - 1] & 1 = 1 THEN ' DESC' ELSE '' END, E'\t' ORDER BY k.n)
			  FROM unnest(ix.indkey) WITH ORDINALITY k(attnum, n) LEFT JOIN pg_attribute a ON a.attrelid = ix.indrelid AND a.attnum = k.attnum
			  -- the key columns only: INCLUDE columns (PostgreSQL 11+) made keys would weaken a UNIQUE index
			  WHERE k.n <= COALESCE((to_jsonb(ix)->>'indnkeyatts')::int, ix.indnatts)), '')
		FROM pg_index ix JOIN pg_class i ON i.oid = ix.indexrelid
		WHERE ix.indrelid = $1::regclass AND NOT ix.indisprimary
		  AND NOT EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conindid = ix.indexrelid AND c.conrelid = ix.indrelid)
		ORDER BY i.relname`, []any{rel},
		func(scan func(...any) error) error {
			var name, cols string
			var unique, partial bool
			if err := scan(&name, &unique, &partial, &cols); err != nil {
				return err
			}
			ix := mIndex{Name: name, Unique: unique}
			for _, c := range strings.Split(cols, "\t") {
				if c == "" || strings.HasPrefix(c, " ") {
					t.Notes = append(t.Notes, "the expression index "+name+" isn't carried over")
					return nil
				}
				c, desc := strings.CutSuffix(c, " DESC")
				ix.Desc = ix.Desc || desc
				ix.Cols = append(ix.Cols, c)
			}
			if partial {
				t.Notes = append(t.Notes, "the index "+name+" is partial (WHERE …): it is created on every row")
			}
			t.Indexes = append(t.Indexes, ix)
			return nil
		})
}

func mssqlTable(ctx context.Context, db *sql.DB, t *mTable) error {
	obj := quoteObject(EngineMSSQL, t.Src)
	err := scanRows(ctx, db, `SELECT c.name, TYPE_NAME(c.system_type_id), c.max_length, c.precision, c.scale, c.is_nullable, c.is_identity,
			c.is_computed, OBJECT_DEFINITION(c.default_object_id)
		FROM sys.columns c WHERE c.object_id = OBJECT_ID(@p1) ORDER BY c.column_id`, []any{obj},
		func(scan func(...any) error) error {
			var name, typ string
			var maxLen, prec, scale int64
			var null, ident, comp bool
			var def sql.NullString
			if err := scan(&name, &typ, &maxLen, &prec, &scale, &null, &ident, &comp, &def); err != nil {
				return err
			}
			full := typ
			switch strings.ToLower(typ) {
			case "nvarchar", "nchar":
				if maxLen > 0 {
					maxLen /= 2
				}
				full = fmt.Sprintf("%s(%d)", typ, maxLen)
			case "varchar", "char", "varbinary", "binary":
				full = fmt.Sprintf("%s(%d)", typ, maxLen)
			case "decimal", "numeric":
				full = fmt.Sprintf("%s(%d,%d)", typ, prec, scale)
			case "float":
				full = fmt.Sprintf("float(%d)", prec)
			}
			if maxLen == -1 {
				full = typ + "(max)"
			}
			c := mCol{Src: name, SrcType: full, Nullable: null, Identity: ident, Computed: comp}
			c.Kind, c.Len, c.Prec, c.Scale = classifyType(EngineMSSQL, full)
			if def.Valid {
				if c.Default = neutralDefault(EngineMSSQL, def.String, c.Kind); c.Default == "" {
					t.Notes = append(t.Notes, fmt.Sprintf("the default of %s (%s) isn't carried over", name, def.String))
				}
			}
			t.Cols = append(t.Cols, c)
			return nil
		})
	if err != nil {
		return err
	}
	err = scanRows(ctx, db, `SELECT i.name, i.is_unique, i.is_primary_key, i.is_unique_constraint, i.has_filter, c.name, ic.is_descending_key
		FROM sys.indexes i JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
		JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
		WHERE i.object_id = OBJECT_ID(@p1) AND i.type > 0 AND ic.is_included_column = 0 ORDER BY i.index_id, ic.key_ordinal`, []any{obj},
		func(scan func(...any) error) error {
			var name, col string
			var unique, pk, ucons, filter, desc bool
			if err := scan(&name, &unique, &pk, &ucons, &filter, &col, &desc); err != nil {
				return err
			}
			if pk {
				t.PK, t.PKName = append(t.PK, col), name
				return nil
			}
			if filter && len(t.Indexes) == 0 || filter && t.Indexes[len(t.Indexes)-1].Name != name {
				t.Notes = append(t.Notes, "the index "+name+" is filtered (WHERE …): it is created on every row")
			}
			addIndex(t, name, col, unique, desc, ucons)
			return nil
		})
	if err != nil {
		return err
	}
	err = scanRows(ctx, db, `SELECT fk.name, pc.name, SCHEMA_NAME(rt.schema_id) + '.' + rt.name, rc.name,
			fk.delete_referential_action_desc, fk.update_referential_action_desc
		FROM sys.foreign_keys fk JOIN sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id
		JOIN sys.columns pc ON pc.object_id = fkc.parent_object_id AND pc.column_id = fkc.parent_column_id
		JOIN sys.tables rt ON rt.object_id = fkc.referenced_object_id
		JOIN sys.columns rc ON rc.object_id = fkc.referenced_object_id AND rc.column_id = fkc.referenced_column_id
		WHERE fk.parent_object_id = OBJECT_ID(@p1) ORDER BY fk.name, fkc.constraint_column_id`, []any{obj},
		func(scan func(...any) error) error {
			var name, col, rt, rc, del, upd string
			if err := scan(&name, &col, &rt, &rc, &del, &upd); err != nil {
				return err
			}
			addFK(t, name, col, rt, rc, del, upd)
			return nil
		})
	if err != nil {
		return err
	}
	return scanRows(ctx, db, `SELECT name, definition FROM sys.check_constraints WHERE parent_object_id = OBJECT_ID(@p1) ORDER BY name`, []any{obj},
		func(scan func(...any) error) error {
			var name, def string
			if err := scan(&name, &def); err != nil {
				return err
			}
			t.Checks = append(t.Checks, mCheck{name, def})
			return nil
		})
}

func firebirdTable(ctx context.Context, db *sql.DB, t *mTable) error {
	identity := map[string]bool{}
	scanRows(ctx, db, `SELECT TRIM(RDB$FIELD_NAME) FROM RDB$RELATION_FIELDS WHERE RDB$RELATION_NAME = ? AND RDB$IDENTITY_TYPE IS NOT NULL`,
		[]any{t.Src}, func(scan func(...any) error) error {
			var n string
			if scan(&n) == nil {
				identity[n] = true
			}
			return nil
		})
	err := scanRows(ctx, db, `SELECT TRIM(rf.RDB$FIELD_NAME), `+fbFieldCols+`, f.RDB$CHARACTER_SET_ID,
			COALESCE(rf.RDB$NULL_FLAG, 0) + COALESCE(f.RDB$NULL_FLAG, 0), COALESCE(rf.RDB$DEFAULT_SOURCE, f.RDB$DEFAULT_SOURCE), f.RDB$COMPUTED_SOURCE
		FROM RDB$RELATION_FIELDS rf JOIN RDB$FIELDS f ON f.RDB$FIELD_NAME = rf.RDB$FIELD_SOURCE
		WHERE rf.RDB$RELATION_NAME = ? ORDER BY rf.RDB$FIELD_POSITION`, []any{t.Src},
		func(scan func(...any) error) error {
			var f fbField
			var name string
			var charset sql.NullInt64
			var notNull int64
			var def, comp sql.NullString
			if err := scan(append(append([]any{&name}, f.typePtrs()...), &charset, &notNull, &def, &comp)...); err != nil {
				return err
			}
			typ := f.typeName(false)
			if charset.Int64 == 1 && (f.typ.Int64 == 14 || f.typ.Int64 == 37) {
				typ += " CHARACTER SET OCTETS"
			}
			c := mCol{Src: name, SrcType: typ, Nullable: notNull == 0, Identity: identity[name], Computed: comp.Valid}
			c.Kind, c.Len, c.Prec, c.Scale = classifyType(EngineFirebird, typ)
			if def.Valid && !c.Computed {
				if c.Default = neutralDefault(EngineFirebird, def.String, c.Kind); c.Default == "" && !strings.EqualFold(strings.TrimSpace(def.String), "DEFAULT NULL") {
					t.Notes = append(t.Notes, fmt.Sprintf("the default of %s (%s) isn't carried over", name, strings.TrimSpace(def.String)))
				}
			}
			t.Cols = append(t.Cols, c)
			return nil
		})
	if err != nil {
		return err
	}
	segs := func(index string) ([]string, error) {
		return queryStringsArgs(ctx, db, `SELECT TRIM(RDB$FIELD_NAME) FROM RDB$INDEX_SEGMENTS WHERE RDB$INDEX_NAME = ? ORDER BY RDB$FIELD_POSITION`, index)
	}
	// Constraints and the indexes that hold them.
	consIndex := map[string]bool{}
	type fcons struct{ name, typ, index string }
	var cons []fcons
	err = scanRows(ctx, db, `SELECT TRIM(RDB$CONSTRAINT_NAME), TRIM(RDB$CONSTRAINT_TYPE), TRIM(COALESCE(RDB$INDEX_NAME, ''))
		FROM RDB$RELATION_CONSTRAINTS WHERE RDB$RELATION_NAME = ? ORDER BY 1`, []any{t.Src},
		func(scan func(...any) error) error {
			var c fcons
			if err := scan(&c.name, &c.typ, &c.index); err != nil {
				return err
			}
			cons = append(cons, c)
			if c.index != "" {
				consIndex[c.index] = true
			}
			return nil
		})
	if err != nil {
		return err
	}
	for _, c := range cons {
		switch c.typ {
		case "PRIMARY KEY":
			if t.PK, err = segs(c.index); err != nil {
				return err
			}
			t.PKName = fbOwnName(c.name)
		case "UNIQUE":
			cols, err := segs(c.index)
			if err != nil {
				return err
			}
			t.Indexes = append(t.Indexes, mIndex{Name: fbOwnName(c.name), Cols: cols, Unique: true, Cons: true})
		case "FOREIGN KEY":
			var ref, del, upd sql.NullString
			if err := db.QueryRowContext(ctx, `SELECT TRIM(RDB$CONST_NAME_UQ), TRIM(RDB$DELETE_RULE), TRIM(RDB$UPDATE_RULE)
				FROM RDB$REF_CONSTRAINTS WHERE RDB$CONSTRAINT_NAME = ?`, c.name).Scan(&ref, &del, &upd); err != nil {
				return err
			}
			var refTable, refIndex string
			if err := db.QueryRowContext(ctx, `SELECT TRIM(RDB$RELATION_NAME), TRIM(RDB$INDEX_NAME) FROM RDB$RELATION_CONSTRAINTS
				WHERE RDB$CONSTRAINT_NAME = ?`, ref.String).Scan(&refTable, &refIndex); err != nil {
				return err
			}
			cols, err := segs(c.index)
			if err != nil {
				return err
			}
			refCols, err := segs(refIndex)
			if err != nil {
				return err
			}
			t.FKs = append(t.FKs, mFK{Name: fbOwnName(c.name), Cols: cols, RefTable: refTable, RefCols: refCols,
				OnDelete: fkAction(del.String), OnUpdate: fkAction(upd.String)})
		case "CHECK":
			src, _ := fbString(ctx, db, `SELECT FIRST 1 t.RDB$TRIGGER_SOURCE FROM RDB$CHECK_CONSTRAINTS cc
				JOIN RDB$TRIGGERS t ON t.RDB$TRIGGER_NAME = cc.RDB$TRIGGER_NAME WHERE cc.RDB$CONSTRAINT_NAME = ?`, c.name)
			if expr, ok := strings.CutPrefix(strings.TrimSpace(src), "CHECK"); ok {
				expr = strings.TrimSpace(expr)
				if len(expr) > 1 && expr[0] == '(' && expr[len(expr)-1] == ')' {
					expr = expr[1 : len(expr)-1]
				}
				t.Checks = append(t.Checks, mCheck{fbOwnName(c.name), expr})
			}
		}
	}
	return scanRows(ctx, db, `SELECT TRIM(RDB$INDEX_NAME), COALESCE(RDB$UNIQUE_FLAG, 0), COALESCE(RDB$INDEX_TYPE, 0), RDB$EXPRESSION_SOURCE
		FROM RDB$INDICES WHERE RDB$RELATION_NAME = ? AND COALESCE(RDB$SYSTEM_FLAG, 0) = 0 ORDER BY 1`, []any{t.Src},
		func(scan func(...any) error) error {
			var name string
			var unique, typ int64
			var expr sql.NullString
			if err := scan(&name, &unique, &typ, &expr); err != nil {
				return err
			}
			if consIndex[name] {
				return nil
			}
			if expr.Valid {
				t.Notes = append(t.Notes, "the expression index "+name+" isn't carried over")
				return nil
			}
			cols, err := segs(name)
			if err != nil {
				return err
			}
			t.Indexes = append(t.Indexes, mIndex{Name: name, Cols: cols, Unique: unique == 1, Desc: typ == 1})
			return nil
		})
}

// fbOwnName is a constraint's name, or "" for the INTEG_n names Firebird makes up.
func fbOwnName(name string) string {
	if strings.HasPrefix(name, "INTEG_") {
		return ""
	}
	return name
}

func sqliteTable(ctx context.Context, db *sql.DB, t *mTable) error {
	type pk struct {
		n   int
		col string
	}
	var pks []pk
	err := scanRows(ctx, db, `SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_info(?)`, []any{t.Src},
		func(scan func(...any) error) error {
			var name, typ string
			var notNull, n int
			var def sql.NullString
			if err := scan(&name, &typ, &notNull, &def, &n); err != nil {
				return err
			}
			c := mCol{Src: name, SrcType: typ, Nullable: notNull == 0}
			c.Kind, c.Len, c.Prec, c.Scale = classifyType(EngineSQLite, typ)
			if def.Valid {
				if c.Default = neutralDefault(EngineSQLite, def.String, c.Kind); c.Default == "" {
					t.Notes = append(t.Notes, fmt.Sprintf("the default of %s (%s) isn't carried over", name, def.String))
				}
			}
			if n > 0 {
				pks = append(pks, pk{n, name})
			}
			t.Cols = append(t.Cols, c)
			return nil
		})
	if err != nil {
		return err
	}
	for i := 1; i <= len(pks); i++ {
		for _, p := range pks {
			if p.n == i {
				t.PK = append(t.PK, p.col)
			}
		}
	}
	if len(t.PK) == 1 { // INTEGER PRIMARY KEY is the rowid: it numbers itself
		if c := t.col(t.PK[0]); c != nil && strings.EqualFold(c.SrcType, "INTEGER") {
			c.Identity = true
		}
	}
	type sidx struct {
		name   string
		unique bool
		origin string
	}
	var idx []sidx
	err = scanRows(ctx, db, `SELECT name, "unique", origin FROM pragma_index_list(?)`, []any{t.Src},
		func(scan func(...any) error) error {
			var x sidx
			if err := scan(&x.name, &x.unique, &x.origin); err != nil {
				return err
			}
			idx = append(idx, x)
			return nil
		})
	if err != nil {
		return err
	}
	for _, x := range idx {
		if x.origin == "pk" {
			continue
		}
		cols, err := queryStringsArgs(ctx, db, `SELECT name FROM pragma_index_info(?) ORDER BY seqno`, x.name)
		if err != nil {
			return err
		}
		name := x.name
		if strings.HasPrefix(name, "sqlite_autoindex_") {
			name = "uq_" + t.Src + "_" + strings.Join(cols, "_")
		}
		t.Indexes = append(t.Indexes, mIndex{Name: name, Cols: cols, Unique: x.unique, Cons: x.origin == "u"})
	}
	return scanRows(ctx, db, `SELECT id, seq, "table", "from", COALESCE("to", ''), on_delete, on_update FROM pragma_foreign_key_list(?) ORDER BY id, seq`,
		[]any{t.Src}, func(scan func(...any) error) error {
			var id, seq int
			var rt, col, rc, del, upd string
			if err := scan(&id, &seq, &rt, &col, &rc, &del, &upd); err != nil {
				return err
			}
			if rc == "" { // the referenced table's primary key: its seq-th column
				if keys, _ := queryStringsArgs(ctx, db, `SELECT name FROM pragma_table_info(?) WHERE pk > 0 ORDER BY pk`, rt); seq < len(keys) {
					rc = keys[seq]
				}
			}
			addFK(t, fmt.Sprintf("fk_%s_%d", t.Src, id), col, rt, rc, del, upd)
			return nil
		})
}

// genericTable reads only the columns, from the driver's description of a
// query's result (Oracle, Access, Informix…): no keys or indexes.
func genericTable(ctx context.Context, engine string, db *sql.DB, t *mTable) error {
	q, err := buildSelect(ctx, engine, db, t.Src)
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	types, err := rows.ColumnTypes()
	rows.Close()
	if err != nil {
		return err
	}
	for _, ct := range types {
		typ := ct.DatabaseTypeName()
		if l, ok := ct.Length(); ok && l > 0 && l < 1<<20 {
			typ = fmt.Sprintf("%s(%d)", typ, l)
		} else if p, s, ok := ct.DecimalSize(); ok && p > 0 {
			typ = fmt.Sprintf("%s(%d,%d)", typ, p, s)
		}
		null, _ := ct.Nullable()
		c := mCol{Src: ct.Name(), SrcType: typ, Nullable: true}
		_ = null
		c.Kind, c.Len, c.Prec, c.Scale = classifyType(engine, typ)
		t.Cols = append(t.Cols, c)
	}
	t.Notes = append(t.Notes, "only the columns are read from "+engineName(engine)+": add its keys and indexes by hand")
	return nil
}

// readSequences reads the sequences (generators) and their next values.
func readSequences(ctx context.Context, engine string, db *sql.DB, names []string) []mSeq {
	var out []mSeq
	for _, n := range names {
		s := mSeq{Src: n, Incr: 1, Next: 1}
		var err error
		switch engine {
		case EngineFirebird:
			var cur int64
			var incr sql.NullInt64
			if err = db.QueryRowContext(ctx, `SELECT GEN_ID(`+quoteIdent(engine, n)+`, 0) FROM RDB$DATABASE`).Scan(&cur); err == nil {
				db.QueryRowContext(ctx, `SELECT RDB$GENERATOR_INCREMENT FROM RDB$GENERATORS WHERE RDB$GENERATOR_NAME = ?`, n).Scan(&incr)
				if incr.Valid && incr.Int64 != 0 {
					s.Incr = incr.Int64
				}
				s.Next = cur + s.Incr
			}
		case EnginePostgres:
			schema, name, _ := strings.Cut(n, ".")
			var last, start, incr int64
			var called bool
			if err = db.QueryRowContext(ctx, `SELECT s.last_value IS NOT NULL, COALESCE(s.last_value, s.start_value), s.start_value, s.increment_by
				FROM pg_sequences s WHERE s.schemaname = $1 AND s.sequencename = $2`, schema, name).Scan(&called, &last, &start, &incr); err == nil {
				s.Incr = incr
				s.Next = start
				if called {
					s.Next = last + incr
				}
			}
			// A sequence that numbers a serial or identity column comes with it.
			var owned bool
			db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_depend WHERE objid = $1::regclass AND deptype IN ('a', 'i'))`,
				quoteObject(engine, n)).Scan(&owned)
			if owned {
				continue
			}
		case EngineMSSQL:
			var cur, incr int64
			if err = db.QueryRowContext(ctx, `SELECT CAST(current_value AS BIGINT), CAST(increment AS BIGINT) FROM sys.sequences WHERE object_id = OBJECT_ID(@p1)`,
				quoteObject(engine, n)).Scan(&cur, &incr); err == nil {
				s.Incr, s.Next = incr, cur+incr
			}
		case EngineMySQL, EngineMariaDB:
			var next, incr int64
			if err = db.QueryRowContext(ctx, `SELECT next_not_cached_value, increment FROM `+quoteIdent(engine, n)).Scan(&next, &incr); err == nil {
				s.Incr, s.Next = incr, next
			}
		}
		s.SourceKnown = err == nil
		out = append(out, s)
	}
	return out
}
