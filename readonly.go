package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// cactusdb is strictly read-only. checkReadOnly is the first of two layers:
// it rejects anything but a single query statement. The second layer is the
// database itself: SQLite is opened read-only, and MySQL/MariaDB/Firebird
// queries run inside a READ ONLY transaction that is always rolled back.

var allowedStarts = map[string]bool{
	"SELECT": true, "WITH": true, "VALUES": true,
	"SHOW": true, "DESCRIBE": true, "DESC": true, "EXPLAIN": true,
}

// Keywords that modify data or schema, or change session/transaction state.
var forbiddenWords = map[string]bool{
	"INSERT": true, "UPDATE": true, "DELETE": true, "MERGE": true, "UPSERT": true,
	"DROP": true, "ALTER": true, "CREATE": true, "TRUNCATE": true, "RENAME": true,
	"GRANT": true, "REVOKE": true, "ATTACH": true, "DETACH": true, "VACUUM": true,
	"REINDEX": true, "CALL": true, "EXECUTE": true, "EXEC": true, "LOCK": true,
	"HANDLER": true, "LOAD": true, "COMMIT": true, "ROLLBACK": true, "SAVEPOINT": true,
	"PRAGMA": true, "INTO": true, "OUTFILE": true, "DUMPFILE": true,
	// SQL Server
	"BACKUP": true, "RESTORE": true, "DBCC": true, "SHUTDOWN": true, "KILL": true,
	"BULK": true, "DENY": true, "WRITETEXT": true, "UPDATETEXT": true, "RECONFIGURE": true,
	"OPENQUERY": true, "OPENROWSET": true, "OPENDATASOURCE": true,
	// PostgreSQL
	"COPY": true, "SET_CONFIG": true,
	"LO_IMPORT": true, "LO_EXPORT": true, "LO_UNLINK": true, "LO_CREATE": true, "LO_PUT": true,
	"LO_FROM_BYTEA": true, "LO_TRUNCATE": true, "LO_TRUNCATE64": true, "LOWRITE": true,
	"PG_TERMINATE_BACKEND": true, "PG_CANCEL_BACKEND": true, "PG_RELOAD_CONF": true,
	"PG_SWITCH_WAL": true, "PG_PROMOTE": true, "PG_ROTATE_LOGFILE": true, "PG_LOGICAL_EMIT_MESSAGE": true,
	"PG_FILE_WRITE": true, "PG_FILE_UNLINK": true, "PG_FILE_RENAME": true,
	"PG_WAL_REPLAY_PAUSE": true, "PG_WAL_REPLAY_RESUME": true,
	// MySQL: a named lock outlives the rollback on a pooled connection.
	"GET_LOCK": true, "RELEASE_LOCK": true, "RELEASE_ALL_LOCKS": true,
	// Athena/Trino: write files or rewrite tables.
	"UNLOAD": true, "OPTIMIZE": true,
	// Oracle: an inline function can commit on its own.
	"AUTONOMOUS_TRANSACTION": true,
	// Sequence/generator advancers look like reads but change state, and are
	// not undone by a rollback (NEXT VALUE FOR is checked separately).
	"NEXTVAL": true, "SETVAL": true, "GEN_ID": true,
}

// Function families matched by prefix: dblink_* opens a second session outside
// the read-only transaction; advisory locks outlive the rollback on a pooled
// connection; stats resets and replication slots change server state.
var forbiddenPrefixes = []string{"DBLINK", "PG_ADVISORY_", "PG_TRY_ADVISORY_", "PG_STAT_RESET",
	"PG_CREATE_", "PG_DROP_REPLICATION", "PG_REPLICATION_", "PG_COPY_", "PG_BACKUP_", "PG_START_BACKUP", "PG_STOP_BACKUP"}

var wordRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_$]*`)

var errReadOnly = errors.New("cactusdb is read-only: only SELECT queries are allowed (no INSERT, UPDATE, DELETE or schema changes)")

// checkReadOnly returns an error unless q is a single read-only statement.
func checkReadOnly(engine, q string) error {
	if isBackend(engine) {
		return nil // each backend checks its own queries (backend.CheckReadOnly)
	}
	return checkSQL(engine, q)
}

// checkSQL is checkReadOnly for SQL, also of the backends that take SQL
// (BigQuery, Athena, Informix…): engine only picks the quoting rules.
func checkSQL(engine, q string) error {
	code, err := stripLiterals(engine, q)
	if err != nil {
		return fmt.Errorf("%w; %v", errReadOnly, err)
	}
	code = strings.TrimSpace(code)
	// Allow one trailing semicolon, but not multiple statements.
	code = strings.TrimSpace(strings.TrimRight(code, "; \t\r\n"))
	if strings.Contains(code, ";") {
		return fmt.Errorf("%w; multiple statements are not allowed", errReadOnly)
	}
	words := wordRe.FindAllStringIndex(code, -1)
	if len(words) == 0 {
		return errors.New("SQL is empty")
	}
	first := strings.ToUpper(code[words[0][0]:words[0][1]])
	if !allowedStarts[first] {
		return fmt.Errorf("%w (statement starts with %s)", errReadOnly, first)
	}
	for _, loc := range words {
		w := strings.ToUpper(code[loc[0]:loc[1]])
		if forbiddenWords[w] {
			return fmt.Errorf("%w (found %s)", errReadOnly, w)
		}
		for _, p := range forbiddenPrefixes {
			if strings.HasPrefix(w, p) {
				return fmt.Errorf("%w (found %s)", errReadOnly, w)
			}
		}
		// WITH FUNCTION / WITH PROCEDURE (Oracle) declares code inline.
		if w == "WITH" {
			next := strings.ToUpper(strings.TrimLeft(code[loc[1]:], " \t\r\n"))
			if strings.HasPrefix(next, "FUNCTION") || strings.HasPrefix(next, "PROCEDURE") {
				return fmt.Errorf("%w (found WITH FUNCTION)", errReadOnly)
			}
		}
		if w == "NEXT" && strings.HasPrefix(strings.ToUpper(strings.TrimLeft(code[loc[1]:], " \t\r\n")), "VALUE") {
			return fmt.Errorf("%w (found NEXT VALUE FOR)", errReadOnly)
		}
		// REPLACE(...) is a harmless string function; REPLACE INTO is not.
		if w == "REPLACE" && !strings.HasPrefix(strings.TrimLeft(code[loc[1]:], " \t\r\n"), "(") {
			return fmt.Errorf("%w (found REPLACE)", errReadOnly)
		}
	}
	return nil
}

// stripLiterals blanks out comments, string literals and quoted identifiers
// so that keywords inside them are not mistaken for SQL. It fails when a
// comment, string or identifier is never closed: whatever follows could be
// code the database runs.
func stripLiterals(engine, q string) (string, error) {
	mysqlish := engine == EngineMySQL || engine == EngineMariaDB
	pgish := engine == EnginePostgres // engine is the dialect: CockroachDB, Redshift… too
	// SQL Server and PostgreSQL nest /* */ comments.
	nested := pgish || engine == EngineMSSQL
	brackets := engine == EngineSQLite || engine == EngineMSSQL
	unclosed := errors.New("SQL has a comment, string or quoted name that is never closed")
	var b strings.Builder
	n := len(q)
	for i := 0; i < n; i++ {
		c := q[i]
		switch {
		case c == '-' && i+1 < n && q[i+1] == '-' && (!mysqlish || i+2 >= n || q[i+2] <= ' '), c == '#' && mysqlish:
			for i < n && q[i] != '\n' {
				i++
			}
			b.WriteByte(' ')
		case c == '/' && i+1 < n && q[i+1] == '*':
			// MySQL executes /*! ... */ and MariaDB /*M! ... */ "comments";
			// treat their body as code.
			if mysqlish && i+2 < n && (q[i+2] == '!' || q[i+2] == 'M' && i+3 < n && q[i+3] == '!') {
				i += 2
				if q[i] == 'M' {
					i++
				}
				b.WriteByte(' ')
				continue
			}
			depth := 0
			for ; i < n; i++ {
				if q[i] == '/' && i+1 < n && q[i+1] == '*' && (depth == 0 || nested) {
					depth++
					i++
				} else if q[i] == '*' && i+1 < n && q[i+1] == '/' {
					depth--
					i++
					if depth == 0 {
						break
					}
				}
			}
			if depth != 0 {
				return "", unclosed
			}
			b.WriteByte(' ')
		case c == '\'' && engine == EngineOracle && i > 0 && (q[i-1] == 'q' || q[i-1] == 'Q') && (i < 2 || !isIdentChar(q[i-2]) || q[i-2] == 'n' || q[i-2] == 'N') && i+1 < n:
			// Oracle alternative quoting: q'[...]', q'{...}', q'!...!'
			open := q[i+1]
			closeC := map[byte]byte{'[': ']', '{': '}', '(': ')', '<': '>'}[open]
			if closeC == 0 {
				closeC = open
			}
			end := strings.Index(q[i+2:], string(closeC)+"'")
			if end < 0 {
				return "", unclosed
			}
			i += 2 + end + 1
			b.WriteString(" x ")
		case c == '\'' || c == '"' || c == '`':
			// MySQL strings, and PostgreSQL E'...' strings, take backslash escapes.
			backslash := mysqlish && c != '`' ||
				pgish && c == '\'' && i > 0 && (q[i-1] == 'E' || q[i-1] == 'e') && (i < 2 || !isIdentChar(q[i-2]))
			closed := false
			for i++; i < n; i++ {
				if backslash && q[i] == '\\' {
					i++
					continue
				}
				if q[i] == c {
					if i+1 < n && q[i+1] == c { // doubled quote escape
						i++
						continue
					}
					closed = true
					break
				}
			}
			if !closed {
				return "", unclosed
			}
			b.WriteString(" x ")
		case c == '$' && pgish && (i == 0 || !isIdentChar(q[i-1])) && dollarTag.MatchString(q[i:]):
			// PostgreSQL dollar quoting: $$ ... $$ or $tag$ ... $tag$
			tag := dollarTag.FindString(q[i:])
			end := strings.Index(q[i+len(tag):], tag)
			if end < 0 {
				return "", unclosed
			}
			i += len(tag) + end + len(tag) - 1
			b.WriteString(" x ")
		case c == '[' && brackets: // [identifier], ]] escapes ]
			closed := false
			for i++; i < n; i++ {
				if q[i] == ']' {
					if i+1 < n && q[i+1] == ']' {
						i++
						continue
					}
					closed = true
					break
				}
			}
			if !closed {
				return "", unclosed
			}
			b.WriteString(" x ")
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), nil
}

var dollarTag = regexp.MustCompile(`^\$([A-Za-z_][A-Za-z0-9_]*)?\$`)

func isIdentChar(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}
