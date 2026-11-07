package main

import (
	"context"
	"database/sql"
	"regexp"
	"strconv"
	"strings"
)

// When the database doesn't report a table's size (Firebird keeps none per
// table), it is estimated as rows × row size, the row size being the sum of
// the declared sizes of its columns: varchar(20) counts 20 bytes, int 4,
// datetime 8. Columns without a declared size (blob, text) count a nominal
// size, so the result is a rough estimate, marked ≈ in the UI.

const (
	nominalTextBytes = 256  // text, clob, json, varchar(max), blob sub_type text
	nominalBlobBytes = 1024 // blob, bytea, image, varbinary(max)
	unknownTypeBytes = 8    // a type not listed, e.g. a Firebird domain
)

var typeLenRe = regexp.MustCompile(`\(\s*(\d+)\s*(?:,\s*(\d+)\s*)?\)`)

// columnBytes returns the size in bytes of one value of SQL type typ.
func columnBytes(typ string) int64 {
	up := strings.ToUpper(strings.TrimSpace(typ))
	if strings.HasSuffix(up, "[]") || strings.Contains(up, " ARRAY") {
		return nominalTextBytes
	}
	n, hasLen := int64(0), false
	if m := typeLenRe.FindStringSubmatch(up); m != nil {
		n, _ = strconv.ParseInt(m[1], 10, 64)
		hasLen = true
	}
	// The base type: the words before the length, without UNSIGNED etc.
	base := up
	if i := strings.IndexAny(base, "("); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimSpace(strings.Trim(base, "`\"[]"))
	has := func(words ...string) bool {
		for _, w := range words {
			if base == w || strings.HasPrefix(base, w+" ") {
				return true
			}
		}
		return false
	}
	switch {
	case strings.HasPrefix(up, "BLOB SUB_TYPE TEXT") || strings.HasPrefix(up, "BLOB SUB_TYPE 1"):
		return nominalTextBytes
	case has("TINYINT", "BOOL", "BOOLEAN", "BIT", "YEAR", "ENUM", "SET"):
		if base == "BIT" && hasLen {
			return max(1, (n+7)/8)
		}
		return 1
	case has("SMALLINT", "INT2", "SMALLSERIAL"):
		return 2
	case has("MEDIUMINT"):
		return 3
	case has("INT", "INTEGER", "INT4", "SERIAL", "REAL", "FLOAT4", "DATE", "SMALLDATETIME", "SMALLMONEY"):
		return 4
	case has("FLOAT"):
		if hasLen && n <= 24 {
			return 4
		}
		return 8
	case has("BIGINT", "INT8", "BIGSERIAL", "DOUBLE", "DOUBLE PRECISION", "FLOAT8", "MONEY",
		"DATETIME", "DATETIME2", "TIMESTAMP", "TIMESTAMPTZ", "TIME", "TIMETZ", "INT64"):
		return 8
	case has("DATETIMEOFFSET", "INTERVAL", "UUID", "UNIQUEIDENTIFIER", "INET", "INT128", "DECFLOAT"):
		return 16
	case has("DECIMAL", "NUMERIC", "DEC", "NUMBER"):
		if hasLen {
			return n/2 + 1
		}
		return 9
	case has("NCHAR", "NVARCHAR", "NATIONAL CHAR", "NATIONAL CHARACTER", "NATIONAL VARCHAR", "NCHAR VARYING"):
		if hasLen {
			return 2 * n
		}
		if strings.Contains(up, "MAX") {
			return nominalTextBytes
		}
		return 2
	case has("CHAR", "CHARACTER", "VARCHAR", "VARCHAR2", "CHARACTER VARYING", "CHAR VARYING", "BINARY", "VARBINARY", "BPCHAR"):
		if hasLen {
			return n
		}
		if strings.Contains(up, "MAX") {
			if strings.Contains(base, "BINARY") {
				return nominalBlobBytes
			}
			return nominalTextBytes
		}
		if has("VARCHAR", "CHARACTER VARYING") { // PostgreSQL: no limit
			return nominalTextBytes
		}
		return 1
	case has("BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB", "BYTEA", "IMAGE", "LONG RAW", "GEOMETRY", "GEOGRAPHY"):
		return nominalBlobBytes
	case has("TEXT", "TINYTEXT", "MEDIUMTEXT", "LONGTEXT", "NTEXT", "CLOB", "JSON", "JSONB", "XML", "CITEXT", "STRING"):
		return nominalTextBytes
	}
	return unknownTypeBytes
}

// rowBytes returns the size of one row of table: the sum of its columns'
// sizes, read from its definition.
func rowBytes(ctx context.Context, engine string, db *sql.DB, table string) (int64, error) {
	var def string
	var err error
	switch engine {
	case EngineFirebird: // only the CREATE TABLE: its indexes aren't needed here
		def, err = firebirdTableDDL(ctx, db, table)
	case EngineMSSQL:
		def, err = mssqlTableDDL(ctx, db, table)
	case EnginePostgres:
		def, err = postgresTableDDL(ctx, db, table, false)
	default:
		def, err = objectDefinition(ctx, engine, db, ObjTable, table)
	}
	if err != nil {
		return 0, err
	}
	return rowBytesOf(def), nil
}

// rowBytesOf sums the sizes of the columns of the CREATE TABLE def; 0 when
// it can't be read.
func rowBytesOf(def string) int64 {
	t, ok := parseTable(def)
	if !ok {
		return 0
	}
	var sum int64
	for _, c := range t.cols {
		typ, _, _, _ := columnParts(c.text)
		if f := strings.Fields(typ); len(f) > 0 && strings.EqualFold(f[0], "COMPUTED") || strings.HasPrefix(strings.ToUpper(typ), "AS ") {
			continue // computed: not stored
		}
		sum += columnBytes(typ)
	}
	return sum
}
