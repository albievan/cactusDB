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
	"COPY": true, "SET_CONFIG": true, "DBLINK": true, "DBLINK_EXEC": true,
	"LO_IMPORT": true, "LO_EXPORT": true, "LO_UNLINK": true, "LO_CREATE": true, "LO_PUT": true,
	"PG_TERMINATE_BACKEND": true, "PG_CANCEL_BACKEND": true, "PG_RELOAD_CONF": true,
	// Sequence/generator advancers look like reads but change state, and are
	// not undone by a rollback (NEXT VALUE FOR is checked separately).
	"NEXTVAL": true, "SETVAL": true, "GEN_ID": true,
}

var wordRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_$]*`)

var errReadOnly = errors.New("cactusdb is read-only: only SELECT queries are allowed (no INSERT, UPDATE, DELETE or schema changes)")

// checkReadOnly returns an error unless q is a single read-only statement.
func checkReadOnly(engine, q string) error {
	code := stripLiterals(engine, q)
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
// so that keywords inside them are not mistaken for SQL.
func stripLiterals(engine, q string) string {
	mysqlish := engine == EngineMySQL || engine == EngineMariaDB
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
			// MySQL executes /*! ... */ "comments"; treat their body as code.
			if mysqlish && i+2 < n && q[i+2] == '!' {
				i += 2
				b.WriteByte(' ')
				continue
			}
			end := strings.Index(q[i+2:], "*/")
			if end < 0 {
				i = n
			} else {
				i += end + 3
			}
			b.WriteByte(' ')
		case c == '\'' || c == '"' || c == '`':
			for i++; i < n; i++ {
				if mysqlish && q[i] == '\\' && c != '`' {
					i++
					continue
				}
				if q[i] == c {
					if i+1 < n && q[i+1] == c { // doubled quote escape
						i++
						continue
					}
					break
				}
			}
			b.WriteString(" x ")
		case c == '$' && engine == EnginePostgres && (i == 0 || !isIdentChar(q[i-1])) && dollarTag.MatchString(q[i:]):
			// PostgreSQL dollar quoting: $$ ... $$ or $tag$ ... $tag$
			tag := dollarTag.FindString(q[i:])
			end := strings.Index(q[i+len(tag):], tag)
			if end < 0 {
				i = n
			} else {
				i += len(tag) + end + len(tag) - 1
			}
			b.WriteString(" x ")
		case c == '[' && (engine == EngineSQLite || engine == EngineMSSQL): // [identifier]
			for i < n && q[i] != ']' {
				i++
			}
			b.WriteString(" x ")
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

var dollarTag = regexp.MustCompile(`^\$([A-Za-z_][A-Za-z0-9_]*)?\$`)

func isIdentChar(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}
