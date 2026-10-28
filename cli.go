package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"
)

// Command-line mode: export tables or show what a database contains without
// starting the web UI, e.g.
//
//	cactusdb --dbtype mariadb --host 10.66.1.136 --user root --dbname optima --dbtable orders --exportas excel

// dbPasswordEnv supplies the database password in command-line mode, for
// scripts that should neither prompt nor put the password on the command line.
const dbPasswordEnv = "CACTUSDB_DBPASSWORD"

type cliOptions struct {
	ConnParams
	PasswordSet bool   // --dbpassword was given (an empty password is allowed)
	Tables      string // --dbtable: name, comma-separated names, or "all"
	Show        string // --showtable: "all" or a table name
	DDL         string // --exportddl: "all" or comma-separated table names
	Backup      bool   // --backup yes
	Format      string // --exportas
	ExportDir   string
	Threads     int    // --threads: tables exported at the same time
	Progress    bool   // draw live progress bars (out is a terminal)
	Store       *Store // where the connection is saved as cli-<host>; nil = don't save
}

// engineAliases maps the names accepted by --dbtype to engine ids.
var engineAliases = map[string]string{
	"sqlite": EngineSQLite, "sqlite3": EngineSQLite,
	"mysql":    EngineMySQL,
	"mariadb":  EngineMariaDB,
	"firebird": EngineFirebird, "fb": EngineFirebird,
	"mssql": EngineMSSQL, "sqlserver": EngineMSSQL,
	"postgres": EnginePostgres, "postgresql": EnginePostgres, "pg": EnginePostgres,
}

func parseEngine(s string) (string, error) {
	if e, ok := engineAliases[strings.ToLower(strings.TrimSpace(s))]; ok {
		return e, nil
	}
	return "", fmt.Errorf("unknown --dbtype %q (use sqlite, mysql, mariadb, firebird, mssql or postgres)", s)
}

// parseExportFormat maps --exportas to an export format; excel means xlsx.
func parseExportFormat(s string) (string, error) {
	f := strings.ToLower(strings.TrimSpace(s))
	if f == "excel" {
		f = "xlsx"
	}
	if _, ok := exportFormats[f]; !ok {
		return "", fmt.Errorf("unknown --exportas %q (use csv, json, xml or excel)", s)
	}
	return f, nil
}

// parseYesNo parses --web. An empty value means the flag was not given.
func parseYesNo(s string) (on, given bool, err error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return false, false, nil
	case "yes", "y", "true", "on", "1":
		return true, true, nil
	case "no", "n", "false", "off", "0":
		return false, true, nil
	}
	return false, true, fmt.Errorf("--web must be yes or no, not %q", s)
}

// runCLI connects, then shows and/or exports the requested tables. Progress
// goes to out; the password prompt goes to the terminal.
func runCLI(ctx context.Context, o cliOptions, out io.Writer) error {
	if o.Engine != EngineSQLite && !o.PasswordSet {
		if pw, ok := os.LookupEnv(dbPasswordEnv); ok {
			o.Password = pw
		} else {
			pw, err := promptPassword(fmt.Sprintf("Password for %s@%s: ", o.Username, hostPort(o.Host, o.Port, defaultPort(o.Engine))))
			if err != nil {
				return fmt.Errorf("cannot read password: %w", err)
			}
			o.Password = pw
		}
	}

	db, err := openDB(ctx, o.ConnParams)
	if err != nil {
		return fmt.Errorf("connection failed: %w", explainConnErr(o.ConnParams, err))
	}
	defer db.Close()
	if o.Store != nil {
		name := cliConnName(o.ConnParams)
		if _, err := o.Store.Save(SavedConn{Name: name, ConnParams: o.ConnParams}); err != nil {
			fmt.Fprintf(os.Stderr, "cactusdb: warning: could not save the connection: %v\n", err)
		} else {
			fmt.Fprintf(out, "Connection saved as %q\n", name)
		}
	}
	groups, err := listObjects(ctx, o.Engine, db)
	if err != nil {
		return fmt.Errorf("connected, but listing tables failed: %w", err)
	}
	// Everything goes to the connection's own folder: <exportto>/cli-<host>/<type>.
	o.ExportDir = filepath.Join(o.ExportDir, connFolderName(cliConnName(o.ConnParams)))

	if o.Show != "" {
		if err := showTables(ctx, o, db, groups, out); err != nil {
			return err
		}
	}
	if o.DDL != "" {
		if err := exportDDL(ctx, o, db, groups, out); err != nil {
			return err
		}
	}
	if o.Backup {
		if err := backupCLI(ctx, o, db, groups, out); err != nil {
			return err
		}
	}
	if o.Tables == "" {
		return nil
	}

	var targets []tableRef
	if strings.EqualFold(strings.TrimSpace(o.Tables), "all") {
		for _, g := range groups {
			if g.Type == ObjTable {
				for _, t := range g.Items {
					targets = append(targets, tableRef{typ: ObjTable, name: t})
				}
			}
		}
		if len(targets) == 0 {
			return errors.New("the database has no tables")
		}
	} else {
		// Every name is checked before anything is exported; a table named
		// twice (even spelt differently) is exported once.
		seen := map[tableRef]bool{}
		for _, name := range strings.Split(o.Tables, ",") {
			if name = strings.TrimSpace(name); name == "" {
				continue
			}
			t, err := resolveTable(groups, name)
			if err != nil {
				return err
			}
			if !seen[t] {
				seen[t] = true
				targets = append(targets, t)
			}
		}
		if len(targets) == 0 {
			return errors.New("--dbtable has no table names")
		}
	}

	return runExports(ctx, o, db, targets, out)
}

type tableRef struct{ typ, name string }

// exportDDL writes the definitions of every object (--exportddl all) or of
// the named tables to one .sql file.
func exportDDL(ctx context.Context, o cliOptions, db *sql.DB, groups []ObjectGroup, out io.Writer) error {
	var targets []ddlTarget
	if strings.EqualFold(strings.TrimSpace(o.DDL), "all") {
		targets = ddlTargets(groups)
	} else {
		seen := map[tableRef]bool{}
		for _, name := range strings.Split(o.DDL, ",") {
			if name = strings.TrimSpace(name); name == "" {
				continue
			}
			t, err := resolveTable(groups, name)
			if err != nil {
				return err
			}
			if !seen[t] {
				seen[t] = true
				targets = append(targets, t)
			}
		}
	}
	if len(targets) == 0 {
		return errors.New("--exportddl: nothing to export")
	}
	progress := func(int) {}
	if o.Progress {
		progress = func(done int) {
			fmt.Fprintf(out, "\r\x1b[2KWriting DDL: %d/%d objects", done, len(targets))
		}
	}
	path, n, err := exportDDLToDir(ctx, o.ExportDir, o.Engine, db, defaultConnName(o.ConnParams), ddlFileName(o.ConnParams), targets, progress)
	if o.Progress {
		fmt.Fprint(out, "\r\x1b[2K")
	}
	if err != nil {
		return fmt.Errorf("DDL export failed: %w", err)
	}
	fmt.Fprintf(out, "DDL of %d object(s) written to %s\n", n, path)
	if skipped := len(targets) - n; skipped > 0 {
		fmt.Fprintf(out, "  (%d object(s) had no readable definition; see the comments in the file)\n", skipped)
	}
	return nil
}

// ddlFileName names a DDL file after the database: the file name without
// extension for SQLite and Firebird paths.
func ddlFileName(p ConnParams) string {
	name := p.Database
	if p.Engine == EngineSQLite || p.Engine == EngineFirebird {
		name = strings.TrimSuffix(filepath.Base(strings.ReplaceAll(name, "\\", "/")), filepath.Ext(name))
	}
	if name == "" {
		name = p.Engine
	}
	return name
}

// resolveTable finds a table or view by name: exactly, then ignoring case,
// then without the schema prefix (so "orders" finds "public.orders").
func resolveTable(groups []ObjectGroup, name string) (tableRef, error) {
	var all []tableRef
	for _, g := range groups {
		if g.Queryable {
			for _, it := range g.Items {
				all = append(all, tableRef{typ: g.Type, name: it})
			}
		}
	}
	matchers := []func(string) bool{
		func(it string) bool { return it == name },
		func(it string) bool { return strings.EqualFold(it, name) },
		func(it string) bool {
			i := strings.LastIndex(it, ".")
			return i >= 0 && strings.EqualFold(it[i+1:], name)
		},
	}
	for _, match := range matchers {
		var found []tableRef
		for _, t := range all {
			if match(t.name) {
				found = append(found, t)
			}
		}
		switch len(found) {
		case 0:
			continue
		case 1:
			return found[0], nil
		default:
			names := make([]string, len(found))
			for i, f := range found {
				names[i] = f.name
			}
			return tableRef{}, fmt.Errorf("table %q is ambiguous: %s", name, strings.Join(names, ", "))
		}
	}
	return tableRef{}, fmt.Errorf("table %q not found (--showtable all lists the tables)", name)
}

// showTables prints the tables and views (--showtable all) or the definition
// of one table (--showtable name).
func showTables(ctx context.Context, o cliOptions, db *sql.DB, groups []ObjectGroup, out io.Writer) error {
	if strings.EqualFold(strings.TrimSpace(o.Show), "all") {
		for _, g := range groups {
			if !g.Queryable {
				continue
			}
			fmt.Fprintf(out, "%s (%d)\n", g.Label, len(g.Items))
			for _, it := range g.Items {
				fmt.Fprintf(out, "  %s\n", it)
			}
		}
		return nil
	}
	t, err := resolveTable(groups, strings.TrimSpace(o.Show))
	if err != nil {
		return err
	}
	src, err := objectDefinition(ctx, o.Engine, db, t.typ, t.name)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "-- %s %s\n%s\n", t.typ, t.name, strings.TrimRight(src, "\n"))
	return nil
}

// cliConnName is the name a command-line connection is saved under:
// cli-<host>, or cli-<file name> for SQLite.
func cliConnName(p ConnParams) string {
	if p.Engine == EngineSQLite {
		return "cli-" + filepath.Base(p.Database)
	}
	host := p.Host
	if host == "" {
		host = "localhost"
	}
	return "cli-" + host
}

func defaultPort(engine string) int {
	for _, e := range engines {
		if e["id"] == engine {
			return e["defaultPort"].(int)
		}
	}
	return 0
}

// promptPassword reads a password without echo from the terminal, or a line
// from standard input when it is not a terminal (e.g. piped from a file).
func promptPassword(prompt string) (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, prompt)
		pw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return string(pw), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", fmt.Errorf("no password given: use --dbpassword, $%s, or run in a terminal to be prompted", dbPasswordEnv)
	}
	return strings.TrimRight(line, "\r\n"), nil
}
