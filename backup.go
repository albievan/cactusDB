package main

import (
	"archive/zip"
	"bufio"
	"compress/flate"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// A backup is a .zip, compressed as hard as Deflate can (level 9), holding
// <database>.sql: a script that recreates every object and inserts every row
// of every table. For SQLite it also holds an exact copy of the database
// file. Nothing is written to the database itself.

// backupResult says what a backup contains.
type backupResult struct {
	Path    string
	Tables  int
	Rows    int64
	Objects int
	Size    int64 // of the .zip
	Failed  []string
}

// backupInsertRows is how many rows one INSERT statement carries. Firebird
// has no multi-row VALUES.
func backupInsertRows(engine string) int {
	if engine == EngineFirebird {
		return 1
	}
	return 100
}

// backupToDir backs up the database to <name>_backup_<date>-<time>.zip in
// the backup subfolder of dir. progress, if not nil, is told each table name
// and the rows written so far.
func backupToDir(ctx context.Context, dir, engine string, db *sql.DB, label, name string, groups []ObjectGroup, progress func(table string, rows int64)) (backupResult, error) {
	if progress == nil {
		progress = func(string, int64) {}
	}
	var res backupResult
	path, _, err := writeExportFile(dir, name+"_backup", "zip", func(w io.Writer) (int, error) {
		zw := zip.NewWriter(w)
		zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
			return flate.NewWriter(out, flate.BestCompression)
		})
		zw.SetComment(fmt.Sprintf("Backup of %s (%s) by cactusdb %s", label, engine, version))
		f, err := zw.CreateHeader(&zip.FileHeader{Name: name + ".sql", Method: zip.Deflate, Modified: time.Now()})
		if err != nil {
			return 0, err
		}
		if err := writeBackupScript(ctx, f, engine, db, label, groups, &res, progress); err != nil {
			return 0, err
		}
		if engine == EngineSQLite {
			if err := addSQLiteCopy(ctx, zw, db, name); err != nil {
				return 0, fmt.Errorf("copying the SQLite file: %w", err)
			}
		}
		return 0, zw.Close()
	})
	if err != nil {
		return res, err
	}
	res.Path = path
	if fi, err := os.Stat(path); err == nil {
		res.Size = fi.Size()
	}
	return res, nil
}

// writeBackupScript writes the DDL of every object with the rows of every
// table in between (see writeScript).
func writeBackupScript(ctx context.Context, w io.Writer, engine string, db *sql.DB, label string, groups []ObjectGroup, res *backupResult, progress func(string, int64)) error {
	var tables []string
	for _, g := range groups {
		if g.Type == ObjTable {
			tables = append(tables, g.Items...)
		}
	}
	targets := ddlTargets(groups)
	data := func(bw *bufio.Writer) error {
		bw.WriteString("\n-- ==== data ====\n")
		switch engine {
		case EngineSQLite:
			bw.WriteString("PRAGMA foreign_keys = OFF;\nBEGIN TRANSACTION;\n")
		case EngineMySQL, EngineMariaDB:
			bw.WriteString("SET NAMES utf8mb4;\n")
		}
		for _, t := range tables {
			n, err := writeTableRows(ctx, bw, engine, db, t, func(rows int64) { progress(t, rows) })
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				// One unreadable table doesn't lose the rest of the backup.
				res.Failed = append(res.Failed, t)
				fmt.Fprintf(bw, "-- (rows of %s not available: %s)\n", t, strings.ReplaceAll(err.Error(), "\n", " "))
				continue
			}
			res.Tables++
			res.Rows += n
		}
		writeSequenceValues(ctx, bw, engine, db)
		switch engine {
		case EngineSQLite, EngineFirebird:
			bw.WriteString("COMMIT;\n")
		}
		bw.WriteString("-- ==== end of data ====\n")
		return nil
	}
	n, err := writeScript(ctx, w, engine, db, "Backup", label, targets, nil, data)
	res.Objects = n
	return err
}

// writeTableRows writes INSERT statements for every row of table.
func writeTableRows(ctx context.Context, bw *bufio.Writer, engine string, db *sql.DB, table string, progress func(rows int64)) (int64, error) {
	cols, err := backupColumns(ctx, engine, db, table)
	if err != nil {
		return 0, err
	}
	if len(cols) == 0 {
		return 0, fmt.Errorf("no columns")
	}
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = quoteIdent(engine, c)
	}
	obj := quoteObject(engine, table)
	insert := "INSERT INTO " + obj + " (" + strings.Join(quoted, ", ") + ")"
	if engine == EnginePostgres {
		insert += " OVERRIDING SYSTEM VALUE" // keep the values of identity columns
	}
	insert += " VALUES"
	identity := engine == EngineMSSQL && mssqlHasIdentity(ctx, db, table)

	fmt.Fprintf(bw, "\n-- rows of %s\n", table)
	if identity {
		fmt.Fprintf(bw, "SET IDENTITY_INSERT %s ON;\n", obj)
	}
	per := backupInsertRows(engine)
	var n int64
	inBatch := 0
	var types []string
	var line strings.Builder
	err = streamRows(ctx, engine, db, "SELECT "+strings.Join(quoted, ", ")+" FROM "+obj,
		func(_, t []string) error { types = t; return nil },
		func(raw []any) (bool, error) {
			line.Reset()
			if inBatch == 0 {
				line.WriteString(insert)
				line.WriteString("\n(")
			} else {
				line.WriteString(",\n(")
			}
			for i, v := range raw {
				if i > 0 {
					line.WriteString(", ")
				}
				line.WriteString(sqlLiteral(engine, types[i], v))
			}
			line.WriteString(")")
			inBatch++
			if inBatch == per {
				line.WriteString(";\n")
				inBatch = 0
			}
			if _, err := bw.WriteString(line.String()); err != nil {
				return false, err
			}
			n++
			if n%1000 == 0 {
				progress(n)
			}
			return true, nil
		})
	if inBatch > 0 {
		bw.WriteString(";\n")
	}
	if identity {
		fmt.Fprintf(bw, "SET IDENTITY_INSERT %s OFF;\n", obj)
	}
	if engine == EngineMSSQL {
		bw.WriteString("GO\n")
	}
	progress(n)
	return n, err
}

// writeSequenceValues sets sequences and identity columns to where they were,
// so rows inserted after a restore don't reuse an existing key. (MySQL and
// MariaDB keep AUTO_INCREMENT in the table definition; SQLite and SQL Server
// identity columns follow the inserted rows by themselves.)
func writeSequenceValues(ctx context.Context, bw *bufio.Writer, engine string, db *sql.DB) {
	var stmts []string
	var err error
	switch engine {
	case EnginePostgres:
		// By name for every sequence, and through the column for serial and
		// identity columns, whose sequence may get another name on restore.
		stmts, err = queryStrings(ctx, db, `SELECT 'SELECT pg_catalog.setval(' || quote_literal(format('%I.%I', schemaname, sequencename))
				|| ', ' || last_value || ', true);'
			FROM pg_sequences WHERE last_value IS NOT NULL ORDER BY 1`)
		if err == nil {
			var cols []string
			cols, err = queryStrings(ctx, db, `SELECT 'SELECT pg_catalog.setval(pg_get_serial_sequence(' || quote_literal(q.tbl) || ', '
					|| quote_literal(a.attname) || '), ' || s.last_value || ', true);'
				FROM pg_attribute a
				JOIN pg_class c ON c.oid = a.attrelid AND c.relkind IN ('r', 'p')
				JOIN pg_namespace n ON n.oid = c.relnamespace
				CROSS JOIN LATERAL (SELECT format('%I.%I', n.nspname, c.relname) AS tbl) q
				JOIN pg_sequences s ON format('%I.%I', s.schemaname, s.sequencename) = pg_get_serial_sequence(q.tbl, a.attname)
				WHERE a.attnum > 0 AND NOT a.attisdropped AND s.last_value IS NOT NULL
				  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
				ORDER BY 1`)
			stmts = append(stmts, cols...)
		}
	case EngineFirebird:
		// SET GENERATOR sets the current value on every version. An identity
		// column's generator is renamed on restore, so it restarts through the
		// column; RESTART WITH means "next value" on Firebird 4+ and "current
		// value" on 3, which at worst leaves a gap.
		var gens []string
		gens, err = queryStrings(ctx, db, `SELECT TRIM(RDB$GENERATOR_NAME) FROM RDB$GENERATORS
			WHERE COALESCE(RDB$SYSTEM_FLAG, 0) IN (0, 6) ORDER BY 1`) // 6: identity column generator
		identity := map[string][2]string{}
		if rows, ierr := db.QueryContext(ctx, `SELECT TRIM(RDB$GENERATOR_NAME), TRIM(RDB$RELATION_NAME), TRIM(RDB$FIELD_NAME)
			FROM RDB$RELATION_FIELDS WHERE RDB$GENERATOR_NAME IS NOT NULL`); ierr == nil {
			for rows.Next() {
				var g, t, c string
				if rows.Scan(&g, &t, &c) == nil {
					identity[g] = [2]string{t, c}
				}
			}
			rows.Close()
		}
		for _, g := range gens {
			var cur, incr sql.NullInt64
			if db.QueryRowContext(ctx, `SELECT GEN_ID(`+quoteIdent(engine, g)+`, 0) FROM RDB$DATABASE`).Scan(&cur) != nil || !cur.Valid {
				continue
			}
			if tc, ok := identity[g]; ok {
				incr.Int64 = 1
				db.QueryRowContext(ctx, `SELECT RDB$GENERATOR_INCREMENT FROM RDB$GENERATORS WHERE RDB$GENERATOR_NAME = ?`, g).Scan(&incr)
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s RESTART WITH %d;",
					quoteIdent(engine, tc[0]), quoteIdent(engine, tc[1]), cur.Int64+max(incr.Int64, 1)))
			} else if !strings.HasPrefix(g, "RDB$") {
				stmts = append(stmts, fmt.Sprintf("SET GENERATOR %s TO %d;", quoteIdent(engine, g), cur.Int64))
			}
		}
	case EngineMSSQL:
		// RESTART WITH is the next value NEXT VALUE FOR returns.
		stmts, err = queryStrings(ctx, db, `SELECT 'ALTER SEQUENCE ' + QUOTENAME(SCHEMA_NAME(schema_id)) + '.' + QUOTENAME(name)
				+ ' RESTART WITH ' + CONVERT(varchar(40), CONVERT(decimal(38, 0), current_value) + CONVERT(decimal(38, 0), increment)) + ';'
			FROM sys.sequences WHERE current_value IS NOT NULL AND (last_used_value IS NOT NULL) ORDER BY 1`)
	default:
		return
	}
	if err != nil {
		fmt.Fprintf(bw, "\n-- (sequence values not available: %s)\n", strings.ReplaceAll(err.Error(), "\n", " "))
		return
	}
	if len(stmts) == 0 {
		return
	}
	bw.WriteString("\n-- sequences and identity columns continue where they were\n")
	if engine == EngineFirebird {
		bw.WriteString("COMMIT; -- Firebird can't alter a table with uncommitted rows\n")
	}
	for _, st := range stmts {
		bw.WriteString(st + "\n")
	}
	if engine == EngineMSSQL {
		bw.WriteString("GO\n")
	}
}

// backupColumns lists the columns a restore can insert into: every column
// except computed/generated ones (and SQL Server rowversion columns).
func backupColumns(ctx context.Context, engine string, db *sql.DB, table string) ([]string, error) {
	switch engine {
	case EngineSQLite:
		return queryStringsArgs(ctx, db, `SELECT name FROM pragma_table_xinfo(?) WHERE hidden = 0 ORDER BY cid`, table)
	case EngineMySQL, EngineMariaDB:
		return queryStringsArgs(ctx, db, `SELECT column_name FROM information_schema.columns
			WHERE table_schema = DATABASE() AND table_name = ? AND COALESCE(generation_expression, '') = ''
			ORDER BY ordinal_position`, table)
	case EngineFirebird:
		return queryStringsArgs(ctx, db, `SELECT TRIM(rf.RDB$FIELD_NAME) FROM RDB$RELATION_FIELDS rf
			JOIN RDB$FIELDS f ON f.RDB$FIELD_NAME = rf.RDB$FIELD_SOURCE
			WHERE rf.RDB$RELATION_NAME = ? AND f.RDB$COMPUTED_BLR IS NULL ORDER BY rf.RDB$FIELD_POSITION`, table)
	case EnginePostgres:
		return queryStringsArgs(ctx, db, `SELECT a.attname FROM pg_attribute a
			JOIN pg_class c ON c.oid = a.attrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname || '.' || c.relname = $1 AND a.attnum > 0 AND NOT a.attisdropped
			  AND COALESCE(to_jsonb(a)->>'attgenerated', '') = ''
			ORDER BY a.attnum`, table)
	case EngineMSSQL:
		return queryStringsArgs(ctx, db, `SELECT c.name FROM sys.columns c
			JOIN sys.objects o ON o.object_id = c.object_id
			JOIN sys.schemas s ON s.schema_id = o.schema_id
			WHERE s.name + '.' + o.name = @p1 AND c.is_computed = 0 AND TYPE_NAME(c.system_type_id) <> 'timestamp'
			ORDER BY c.column_id`, table)
	}
	return nil, fmt.Errorf("unsupported engine %q", engine)
}

func mssqlHasIdentity(ctx context.Context, db *sql.DB, table string) bool {
	n, err := queryStringsArgs(ctx, db, `SELECT CAST(COUNT(*) AS varchar(10)) FROM sys.identity_columns c
		JOIN sys.objects o ON o.object_id = c.object_id
		JOIN sys.schemas s ON s.schema_id = o.schema_id
		WHERE s.name + '.' + o.name = @p1`, table)
	return err == nil && len(n) == 1 && n[0] != "0"
}

// sqlLiteral writes v, as the driver returned it for a column of type
// dbType, as an SQL literal for engine.
func sqlLiteral(engine, dbType string, v any) string {
	switch t := v.(type) {
	case nil:
		return "NULL"
	case bool:
		if engine == EnginePostgres {
			return strings.ToUpper(strconv.FormatBool(t))
		}
		if t {
			return "1"
		}
		return "0"
	case float32:
		return floatLiteral(engine, float64(t))
	case float64:
		return floatLiteral(engine, t)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return fmt.Sprint(t)
	case []byte:
		switch {
		case dbType == "UNIQUEIDENTIFIER" && len(t) == 16:
			return quoteString(engine, formatGUID(t))
		case engine == EngineSQLite, isBinaryType(engine, dbType), !utf8.Valid(t):
			// The SQLite driver returns TEXT as string: bytes are a BLOB.
			return hexLiteral(engine, t)
		case isNumericType(dbType) && plainNumber.MatchString(string(t)):
			return string(t)
		}
		return quoteString(engine, string(t))
	case string:
		if isNumericType(dbType) && plainNumber.MatchString(t) {
			return t
		}
		return quoteString(engine, t)
	}
	switch n := normalizeValue(v, dbType).(type) { // dates, decimals, …
	case json.Number:
		return string(n)
	case string:
		return quoteString(engine, n)
	case nil:
		return "NULL"
	default:
		return quoteString(engine, fmt.Sprint(n))
	}
}

func floatLiteral(engine string, f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		if engine == EnginePostgres {
			return "'" + strconv.FormatFloat(f, 'g', -1, 64) + "'" // 'NaN', '+Inf', '-Inf'
		}
		return "NULL"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// isBinaryType reports whether a column holds bytes rather than text.
func isBinaryType(engine, t string) bool {
	switch {
	case strings.Contains(t, "BINARY"), strings.Contains(t, "BYTEA"), t == "IMAGE", t == "GEOMETRY", t == "BIT" && engine != EngineMSSQL && engine != EnginePostgres:
		return true
	case strings.Contains(t, "BLOB"):
		// Firebird reports text and binary BLOBs alike; text is valid UTF-8,
		// which the caller checks.
		return engine != EngineFirebird
	}
	return false
}

func isNumericType(t string) bool {
	if isDecimalType(t) {
		return true
	}
	for _, s := range []string{"INT", "FLOAT", "DOUBLE", "REAL", "SERIAL"} {
		if strings.Contains(t, s) && !strings.Contains(t, "INTERVAL") && !strings.Contains(t, "POINT") {
			return true
		}
	}
	return false
}

func quoteString(engine, s string) string {
	s = strings.ReplaceAll(s, "'", "''")
	switch engine {
	case EngineMySQL, EngineMariaDB:
		// A backslash starts an escape in MySQL strings unless
		// NO_BACKSLASH_ESCAPES is set, as mysqldump assumes too.
		s = strings.NewReplacer(`\`, `\\`, "\x00", `\0`, "\x1a", `\Z`).Replace(s)
	case EngineMSSQL:
		return "N'" + s + "'"
	}
	return "'" + s + "'"
}

func hexLiteral(engine string, b []byte) string {
	h := hex.EncodeToString(b)
	switch engine {
	case EngineMSSQL:
		return "0x" + h
	case EnginePostgres:
		return `'\x` + h + `'::bytea`
	}
	return "X'" + h + "'"
}

// addSQLiteCopy adds an exact copy of the SQLite database to the backup.
// VACUUM INTO writes a consistent copy even while others use the database.
// It runs on a separate connection opened read-only (mode=ro), so the
// database file itself can't be changed.
func addSQLiteCopy(ctx context.Context, zw *zip.Writer, db *sql.DB, name string) error {
	files, err := queryStrings(ctx, db, `SELECT file FROM pragma_database_list WHERE name = 'main'`)
	if err != nil || len(files) == 0 || files[0] == "" {
		return fmt.Errorf("cannot find the database file: %v", err)
	}
	src := files[0]
	tmpDir, err := os.MkdirTemp("", "cactusdb-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	copyPath := filepath.Join(tmpDir, "copy.db")

	ro, err := sql.Open("sqlite", sqliteURI(src)+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer ro.Close()
	if _, err := ro.ExecContext(ctx, `VACUUM INTO ?`, copyPath); err != nil {
		return err
	}
	in, err := os.Open(copyPath)
	if err != nil {
		return err
	}
	defer in.Close()
	ext := filepath.Ext(src)
	if ext == "" {
		ext = ".db"
	}
	f, err := zw.CreateHeader(&zip.FileHeader{Name: name + ext, Method: zip.Deflate, Modified: time.Now()})
	if err != nil {
		return err
	}
	_, err = io.Copy(f, in)
	return err
}

// backupCLI is --backup yes.
func backupCLI(ctx context.Context, o cliOptions, db *sql.DB, groups []ObjectGroup, out io.Writer) error {
	progress := func(string, int64) {}
	if o.Progress {
		progress = func(table string, rows int64) {
			fmt.Fprintf(out, "\r\x1b[2KBacking up %s: %s rows", table, groupDigits(rows))
		}
	}
	res, err := backupToDir(ctx, o.ExportDir, o.Engine, db, defaultConnName(o.ConnParams), ddlFileName(o.ConnParams), groups, progress)
	if o.Progress {
		fmt.Fprint(out, "\r\x1b[2K")
	}
	if err != nil {
		return fmt.Errorf("backup failed: %w", err)
	}
	fmt.Fprintf(out, "Backup of %d table(s), %s rows and %d object definition(s) written to %s (%s)\n",
		res.Tables, groupDigits(res.Rows), res.Objects, res.Path, humanSize(res.Size))
	if len(res.Failed) > 0 {
		fmt.Fprintf(out, "  rows not backed up (see the comments in the .sql file): %s\n", strings.Join(res.Failed, ", "))
	}
	return nil
}

// handleBackup backs up the session's database to a .zip in the backup
// subfolder of the export folder.
func (s *server) handleBackup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string `json:"session"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	ss, err := s.session(r, req.Session)
	if err != nil {
		writeErr(w, 401, err)
		return
	}
	groups, err := listObjects(r.Context(), ss.engine, ss.db)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	logAction(r, "[%s] backup started", ss.label)
	res, err := backupToDir(r.Context(), ss.exportDir(s.exportFolder()), ss.engine, ss.db, ss.label, ss.dbName, groups, nil)
	if err != nil {
		logAction(r, "[%s] backup FAILED: %v", ss.label, err)
		writeErr(w, 400, err)
		return
	}
	logAction(r, "[%s] backup: %d table(s), %d row(s), %s to %s", ss.label, res.Tables, res.Rows, humanSize(res.Size), res.Path)
	writeJSON(w, 200, map[string]any{
		"path":     res.Path,
		"file":     filepath.Base(res.Path),
		"folder":   filepath.Dir(res.Path),
		"tables":   res.Tables,
		"rows":     res.Rows,
		"objects":  res.Objects,
		"size":     res.Size,
		"sizeText": humanSize(res.Size),
		"failed":   res.Failed,
		"download": downloadURL(res.Path),
	})
}

// humanSize formats a byte count as 532 B, 12.4 KB, 3.1 MB, …
func humanSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	f, unit := float64(n), ""
	for _, u := range []string{"KB", "MB", "GB", "TB"} {
		f /= 1024
		unit = u
		if f < 1024 {
			break
		}
	}
	return fmt.Sprintf("%.1f %s", f, unit)
}
