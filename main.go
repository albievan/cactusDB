// cactusdb is a small read-only database viewer and exporter for SQLite,
// MySQL, MariaDB, Firebird, SQL Server and PostgreSQL, packaged as a single
// self-contained binary. It serves a web UI, or exports tables straight from
// the command line.
package main

import (
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata" // TZ works in minimal containers and on Windows

	"golang.org/x/term"
)

var version = "1.42.1"

//go:embed web
var webFS embed.FS

type session struct {
	engine string // the dialect: its SQL and catalog (see dialect)
	flavor string // the database type it was opened as, e.g. cockroachdb
	label  string
	dbName string // short database name, for DDL file names
	folder string // its folder in the export folder: the connection name
	db     *sql.DB

	savedID int64      // the saved connection it was opened from (0: none), for saved queries
	connBox *secretBox // that connection's own key (nil: it has no password)

	lastUsed time.Time // the last request, for closing forgotten sessions; guarded by server.mu
}

// kind is the database type the session was opened as (e.g. cockroachdb).
func (ss *session) kind() string { return cmp.Or(ss.flavor, ss.engine) }

// exportDir is where this connection's exports go: <export folder>/<connection>.
func (ss *session) exportDir(base string) string {
	f := ss.folder
	if f == "" {
		f = connFolderName(ss.label)
	}
	return filepath.Join(base, f)
}

type server struct {
	store *Store

	exportMu         sync.RWMutex
	exportDir        string   // where exports go now; see exportFolder
	exportDirs       []string // every export folder of this run, for downloads
	defaultExportDir string   // export/ next to the program
	exportToFlag     string   // --exportto, which overrides the saved folder

	defaultConnDB string // cactus.conn.db next to the program
	connDBFlag    string // --conn-db, which overrides a moved database

	mu         sync.Mutex
	sessions   map[string]*session
	batches    map[string]*webBatch    // "export all tables" jobs, by id
	zips       map[string]*preparedZip // compressed downloads of the Exported files page, by id
	compares   map[string]*compareJob  // ☰ → Compare databases, by id (compare.go)
	migrations map[string]*migrateJob  // ☰ → Migrate, by id (migrate.go)

	throttle lockThrottle // slows down guessing connection passwords

	background bool          // started with --runas background: the UI may shut it down
	shutdown   chan struct{} // closed by /api/shutdown
	stopOnce   sync.Once

	previousRun string // how the previous run ended, when it didn't stop normally (crash.go)
	port        int    // the web UI's port: the key of its saved startup options (upgrade.go)
	upgrade     *upgradeJob
	restart     *restartPlan // set when a new version is in place: restart once stopped
	logPath     string       // the log file shown by ☰ → Logs; "" = only the lines in memory (logs.go)
	crashPath   string
}

func main() {
	port := flag.Int("port", 8080, "HTTP port to serve the web UI on")
	host := flag.String("host", "", "web UI: address to listen on (default 0.0.0.0: every network interface; 127.0.0.1 = this computer only)\ncommand-line export: database server host (default localhost)")
	web := flag.String("web", "", "yes: start the web UI (the default when no --dbtype is given)")
	connDB := flag.String("conn-db", "", "SQLite file used to store saved connections (default: cactus.conn.db next to the binary)")
	keyFile := flag.String("key-file", "", "key used to encrypt saved usernames/passwords (default: cactus.key next to the connection file; ignored when $"+passphraseEnv+" is set)")
	exportTo := flag.String("exportto", "", "folder exports are written to (default: ./export next to the binary)")
	dbType := flag.String("dbtype", "", "command-line export: the database type: "+engineIDs())
	dbUser := flag.String("user", "", "command-line export: database user name")
	dbPassword := flag.String("dbpassword", "", "command-line export: database password (prompted for when omitted, or set $"+dbPasswordEnv+")")
	dbName := flag.String("dbname", "", "command-line export: database name (SQLite/Access/Firebird: the database file; Oracle: the service name; BigQuery: the dataset…)")
	dbOptions := flag.String("dboptions", "", "command-line export: extra connection settings as key=value&key=value, e.g. workgroup=primary for Athena")
	dbPort := flag.Int("dbport", 0, "command-line export: database server port (default: the engine's standard port)")
	dbTable := flag.String("dbtable", "", "command-line export: table to export, several separated by commas, or all")
	showTable := flag.String("showtable", "", "command-line: all lists the tables and views; a table name prints its definition")
	superuser := flag.String("superuser", "", "set = set or change the superuser password (it opens every saved connection); status = show whether one is set")
	backupDB := flag.String("backup", "", "command-line: yes = back up the whole database (DDL and all rows) to a compressed .zip in the backup subfolder")
	exportDDL := flag.String("exportddl", "", "command-line: write the DDL (CREATE statements) of all objects, or of the named tables, to a .sql file")
	exportAs := flag.String("exportas", "csv", "command-line export: csv, json, xml or excel")
	threads := flag.Int("threads", 4, "command-line export: how many tables to export at the same time")
	saveConn := flag.String("saveconn", "yes", "command-line export: save the connection as cli-<host> in the connection file (yes or no)")
	runAs := flag.String("runas", "foreground", "web UI: foreground, or background to keep running after the terminal closes (logs to --logfile)")
	logFile := flag.String("logfile", "", "web UI: file every action is logged to (default with --runas background: cactusdb.log next to the binary)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "cactusdb %s - browse and export relational, cloud, NoSQL and vector databases, read-only\n\n", version)
		fmt.Fprintf(os.Stderr, "Usage:\n"+
			"  cactusdb --port 8080 [--web yes]                    start the web UI\n"+
			"  cactusdb --port 8080 --runas background             start the web UI in the background\n"+
			"  cactusdb --dbtype mariadb --host 10.66.1.136 --dbport 3306 --user root --dbname optima \\\n"+
			"           --dbtable orders|t1,t2|all --exportas csv|json|xml|excel [--exportto ./export]\n"+
			"                                                      export tables without the web UI\n"+
			"  cactusdb --dbtype firebird --host fb1 --user SYSDBA --dbname /data/shop.fdb --exportddl all|t1,t2\n"+
			"                                                      export the DDL to a .sql file\n"+
			"  cactusdb --dbtype postgres --host pg1 --user me --dbname shop --backup yes\n"+
			"                                                      back up the database to a compressed .zip\n"+
			"  cactusdb --dbtype sqlite --dbname ./shop.db --showtable all|tablename\n"+
			"                                                      list tables, or show a table's definition\n"+
			"\nThe password is prompted for unless --dbpassword or $%s is given.\n\nOptions:\n", dbPasswordEnv)
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("cactusdb", version)
		return
	}
	if flag.NArg() > 0 {
		fatalUsage(fmt.Sprintf("unexpected argument %q (options start with --)", flag.Arg(0)))
	}
	set := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { set[f.Name] = true })

	webOn, webGiven, err := parseYesNo(*web)
	if err != nil {
		fatalUsage(err.Error())
	}
	cliMode := set["dbtype"] || set["dbtable"] || set["showtable"] || set["exportddl"] || set["backup"]
	background := false
	switch strings.ToLower(*runAs) {
	case "foreground", "":
	case "background":
		background = true
	default:
		fatalUsage(fmt.Sprintf("--runas must be foreground or background, not %q", *runAs))
	}
	switch {
	case cliMode && background:
		fatalUsage(errBackgroundCLI.Error())
	case cliMode && webOn:
		fatalUsage("--web yes cannot be combined with --dbtype/--dbtable/--showtable/--exportddl/--backup")
	case !cliMode && webGiven && !webOn:
		fatalUsage("--web no: nothing to do; give --dbtype and --dbtable or --showtable to export without the web UI")
	}

	// The export folder: --exportto, else the one saved from the web UI
	// (read once the connection database is open), else export/ next to
	// the program.
	defaultExportDir := filepath.Join(exeDir(), "export")
	if *exportTo == "" {
		*exportTo = defaultExportDir
	}
	exportDir, err := filepath.Abs(*exportTo)
	if err != nil {
		log.Fatalf("bad export folder %s: %v", *exportTo, err)
	}

	defaultConnDB := filepath.Join(exeDir(), "cactus.conn.db")
	if *connDB == "" {
		// Moved from the web UI (see dbmove.go)?
		if *connDB = movedConnDB(exeDir()); *connDB == "" {
			*connDB = defaultConnDB
		}
	}
	if *keyFile == "" {
		*keyFile = filepath.Join(filepath.Dir(*connDB), keyFileName)
	}
	if set["superuser"] {
		st, err := openStore(*connDB, *keyFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cactusdb: cannot open %s: %v\n", *connDB, err)
			os.Exit(1)
		}
		err = superuserCLI(st, *superuser)
		st.DB().Close()
		if err != nil {
			fmt.Fprintln(os.Stderr, "cactusdb:", err)
			os.Exit(1)
		}
		return
	}

	if cliMode {
		for _, f := range []string{"port", "logfile"} {
			if set[f] {
				fatalUsage("--" + f + " only applies to the web UI (the database port is --dbport)")
			}
		}
		save, _, err := parseYesNo(*saveConn)
		if err != nil {
			fatalUsage(strings.Replace(err.Error(), "--web", "--saveconn", 1))
		}
		if *threads < 1 {
			fatalUsage("--threads must be 1 or more")
		}
		if !set["dbtype"] {
			fatalUsage("--dbtype is required")
		}
		backup, _, err := parseYesNo(*backupDB)
		if err != nil {
			fatalUsage(strings.Replace(err.Error(), "--web", "--backup", 1))
		}
		if *dbTable == "" && *showTable == "" && *exportDDL == "" && !backup {
			fatalUsage("give --dbtable to export data, --exportddl to export DDL, --backup yes to back up, or --showtable to list tables")
		}
		engine, err := parseEngine(*dbType)
		if err != nil {
			fatalUsage(err.Error())
		}
		format, err := parseExportFormat(*exportAs)
		if err != nil {
			fatalUsage(err.Error())
		}
		einfo := engineByID(engine)
		if *dbName == "" && !einfo.Backend && einfo.Dialect == "" || *dbName == "" && einfo.File {
			fatalUsage("--dbname is required")
		}
		if !einfo.File && !einfo.Backend && *dbUser == "" {
			fatalUsage("--user is required")
		}
		if einfo.File {
			if abs, err := filepath.Abs(*dbName); err == nil {
				*dbName = abs // saved connections must work from any folder
			}
		}
		var store *Store
		if save {
			if store, err = openStore(*connDB, *keyFile); err != nil {
				fmt.Fprintf(os.Stderr, "cactusdb: warning: the connection won't be saved: cannot open %s: %v\n", *connDB, err)
				store = nil
			} else {
				defer store.DB().Close()
			}
		}
		if !set["exportto"] {
			st := store
			if st == nil && fileExists(*connDB) { // --saveconn no: only read the setting
				if st, err = openStore(*connDB, *keyFile); err == nil {
					defer st.DB().Close()
				}
			}
			if dir := savedExportDir(st); dir != "" {
				exportDir = dir
			}
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		opts := cliOptions{
			ConnParams:  ConnParams{Engine: engine, Host: *host, Port: *dbPort, Username: *dbUser, Password: *dbPassword, Database: *dbName, Options: *dbOptions},
			PasswordSet: set["dbpassword"],
			Tables:      *dbTable,
			Show:        *showTable,
			DDL:         *exportDDL,
			Backup:      backup,
			Format:      format,
			ExportDir:   exportDir,
			Threads:     *threads,
			Progress:    term.IsTerminal(int(os.Stdout.Fd())) && enableANSI(os.Stdout),
			Store:       store,
		}
		start := time.Now()
		err = runCLI(ctx, opts, os.Stdout)
		stop()
		// The run goes into the usage log (and Google Analytics when the
		// server sends command-line runs), if there is a connection database.
		rec := store
		if rec == nil && fileExists(*connDB) {
			if st, e := openStore(*connDB, *keyFile); e == nil {
				rec = st
			}
		}
		recordCLI(rec, cliRun{o: opts, start: start, err: err, function: cliFunction(opts)})
		if rec != nil && rec != store {
			rec.DB().Close()
		}
		if store != nil {
			store.DB().Close()
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "cactusdb:", err)
			os.Exit(1)
		}
		return
	}

	for _, f := range []string{"user", "dbpassword", "dbname", "dbport", "dboptions", "exportas", "threads", "saveconn"} {
		if set[f] {
			fatalUsage("--" + f + " is for command-line export and needs --dbtype")
		}
	}
	if *host == "" {
		*host = "0.0.0.0" // every network interface, so other computers can connect
	}
	if *logFile == "" && background {
		*logFile = filepath.Join(exeDir(), "cactusdb.log")
	}
	if *logFile != "" {
		if *logFile, err = filepath.Abs(*logFile); err != nil {
			log.Fatalf("bad log file: %v", err)
		}
	}
	if background {
		probeHost := *host
		switch probeHost {
		case "0.0.0.0":
			probeHost = "127.0.0.1"
		case "::":
			probeHost = "::1"
		}
		// Fail here if the port is taken: otherwise the check below could
		// get its answer from whatever already listens there.
		addr := net.JoinHostPort(*host, strconv.Itoa(*port))
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cactusdb: cannot listen on %s: %v\n", addr, err)
			os.Exit(1)
		}
		ln.Close()
		probe := "http://" + net.JoinHostPort(probeHost, strconv.Itoa(*port)) + "/api/engines"
		if err := startBackground(*logFile, probe, browseURLs(*host, *port)); err != nil {
			fmt.Fprintln(os.Stderr, "cactusdb:", err)
			os.Exit(1)
		}
		return
	}
	childLog := os.Getenv(backgroundEnv) // set when this is the background copy
	var logCopy io.Writer                // the --logfile of a foreground cactusdb
	if *logFile != "" && childLog == "" {
		// Foreground with --logfile: log to the terminal and the file.
		f, err := openDailyLog(*logFile)
		if err != nil {
			log.Fatalf("cannot open log file: %v", err)
		}
		defer f.Close()
		log.SetOutput(io.MultiWriter(os.Stderr, f))
		logCopy = f
	}
	store, err := openStore(*connDB, *keyFile)
	if err != nil {
		log.Fatalf("cannot open connection store %s: %v", *connDB, err)
	}
	// The superuser password can come from the environment (containers); it
	// only ever sets a missing one, and isn't kept in the environment.
	if msg, err := store.superuserFromEnv(os.Getenv(superuserEnv)); err != nil {
		log.Printf("warning: %v", err)
	} else if msg != "" {
		log.Print(msg)
	}
	os.Unsetenv(superuserEnv)
	exportToFlag := ""
	if set["exportto"] {
		exportToFlag = exportDir
	} else if dir := savedExportDir(store); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Printf("warning: cannot use the saved export folder %s (%v); using %s", dir, err, exportDir)
		} else {
			exportDir = dir
		}
	}
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		log.Fatalf("cannot create export folder %s: %v", exportDir, err)
	}
	if err := startUsageLog(store); err != nil {
		log.Printf("warning: usage isn't recorded: %v", err)
	}
	s := &server{store: store, sessions: map[string]*session{}, defaultExportDir: defaultExportDir,
		defaultConnDB: defaultConnDB, connDBFlag: flagValueIf(set["conn-db"], *connDB),
		exportToFlag: exportToFlag, background: childLog != "", shutdown: make(chan struct{}), port: *port}
	s.setExportFolder(exportDir)

	static, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	// no-cache: after an upgrade the browser must load the new page, not keep the old one.
	files := http.FileServerFS(static)
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /api/engines", s.handleEngines)
	mux.HandleFunc("GET /api/drivers", s.handleDrivers)
	mux.HandleFunc("GET /api/info", s.handleInfo)
	mux.HandleFunc("GET /api/logs", s.handleLogs)
	mux.HandleFunc("GET /api/logs/zip", s.handleLogsZip)
	mux.HandleFunc("GET /api/update", s.handleUpdate)
	mux.HandleFunc("POST /api/upgrade", s.handleUpgrade)
	mux.HandleFunc("GET /api/upgrade", s.handleUpgradeStatus)
	mux.HandleFunc("POST /api/upgrade/upload", s.handleUpgradeUpload)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("POST /api/settings", s.handleSaveSettings)
	mux.HandleFunc("POST /api/shutdown", s.handleShutdown)
	mux.HandleFunc("POST /api/connect", s.handleConnect)
	mux.HandleFunc("POST /api/disconnect", s.handleDisconnect)
	mux.HandleFunc("POST /api/testconn", s.handleTestConn)
	mux.HandleFunc("POST /api/databases", s.handleDatabases)
	mux.HandleFunc("GET /api/objects", s.handleObjects)
	mux.HandleFunc("POST /api/refresh", s.handleRefresh)
	mux.HandleFunc("GET /api/tablestats", s.handleTableStats)
	mux.HandleFunc("GET /api/definition", s.handleDefinition)
	mux.HandleFunc("GET /api/select", s.handleSelect)
	mux.HandleFunc("POST /api/query", s.handleQuery)
	mux.HandleFunc("POST /api/export", s.handleExport)
	mux.HandleFunc("POST /api/exportddl", s.handleExportDDL)
	mux.HandleFunc("POST /api/backup", s.handleBackup)
	mux.HandleFunc("POST /api/exportall", s.handleExportAll)
	mux.HandleFunc("GET /api/exportall/{job}", s.handleExportAllStatus)
	mux.HandleFunc("GET /api/exports/{path...}", s.handleDownload)
	mux.HandleFunc("GET /api/files", s.handleListFiles)
	mux.HandleFunc("POST /api/files/zip", s.handlePrepareZip)
	mux.HandleFunc("GET /api/files/zip/{id}", s.handleDownloadZip)
	mux.HandleFunc("POST /api/files/preview", s.handlePreviewFile)
	mux.HandleFunc("POST /api/files/delete", s.handleDeleteFiles)
	mux.HandleFunc("POST /api/compare", s.handleCompare)
	mux.HandleFunc("GET /api/compare/{job}", s.handleCompareStatus)
	mux.HandleFunc("POST /api/migrate", s.handleMigrate)
	mux.HandleFunc("GET /api/migrate/{job}", s.handleMigrateStatus)
	mux.HandleFunc("POST /api/migrate/{job}/cancel", s.handleMigrateCancel)
	mux.HandleFunc("GET /api/migrate/{job}/script", s.handleMigrateScript)
	mux.HandleFunc("GET /api/connections", s.handleListConns)
	mux.HandleFunc("POST /api/connections", s.handleSaveConn)
	mux.HandleFunc("DELETE /api/connections/{id}", s.handleDeleteConn)
	// The web UI deletes with POST: some firewalls block a computer from the
	// port for minutes after an HTTP DELETE (see statusInHeader).
	mux.HandleFunc("POST /api/connections/{id}/delete", s.handleDeleteConn)
	mux.HandleFunc("POST /api/connections/{id}/unlock", s.handleUnlockConn)
	mux.HandleFunc("GET /api/superuser", s.handleSuperuserStatus)
	mux.HandleFunc("POST /api/superuser/reveal", s.handleSuperuserReveal)
	mux.HandleFunc("GET /api/queries", s.handleListQueries)
	mux.HandleFunc("POST /api/queries", s.handleSaveQuery)
	mux.HandleFunc("DELETE /api/queries/{id}", s.handleDeleteQuery)
	mux.HandleFunc("POST /api/queries/{id}/delete", s.handleDeleteQuery)
	mux.HandleFunc("GET /api/containers", s.handleContainers)
	mux.HandleFunc("POST /api/containers/deploy", s.handleContainerDeploy)
	mux.HandleFunc("GET /api/containers/deploy/{job}", s.handleContainerJob)
	mux.HandleFunc("POST /api/containers/logs", s.handleContainerLogs)
	mux.HandleFunc("POST /api/containers/remove", s.handleContainerRemove)
	mux.HandleFunc("POST /api/containers/inspect", s.handleContainerInspect)
	mux.HandleFunc("POST /api/containers/action", s.handleContainerAction)
	mux.HandleFunc("POST /api/containers/reveal", s.handleContainerReveal)
	mux.HandleFunc("GET /api/dbstats", s.handleDBStats)
	mux.HandleFunc("POST /api/containers/logs/export", s.handleContainerLogExport)
	mux.HandleFunc("POST /api/usage", s.handleUsage)
	mux.HandleFunc("GET /api/usage/summary", s.handleUsageSummary)
	mux.HandleFunc("POST /api/usage/delete", s.handleUsageDelete)

	addr := net.JoinHostPort(*host, strconv.Itoa(*port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("cannot listen on %s: %v%s", addr, err, portInUse(err, filepath.Dir(*connDB), *port))
	}
	// From here on the terminal must never hold up a request (see termlog.go).
	term := newTermWriter(os.Stderr)
	if childLog == "" {
		if logCopy != nil {
			log.SetOutput(io.MultiWriter(term, logCopy))
		} else {
			log.SetOutput(term)
		}
	}
	if childLog != "" {
		// The background copy: its output goes to the log file opened by the
		// command that started it; log through a file that starts again
		// every day instead.
		if dl, err := openDailyLog(childLog); err == nil {
			log.SetOutput(dl)
		} else {
			log.Printf("warning: the log won't start a new file every day: %v", err)
		}
	}
	log.SetOutput(io.MultiWriter(log.Writer(), logMem)) // for ☰ → Logs
	s.logPath = childLog
	if s.logPath == "" {
		s.logPath = *logFile
	}
	s.crashPath = filepath.Join(filepath.Dir(*connDB), crashLogName)
	// IdleTimeout closes a browser's idle keep-alive connection before a
	// firewall, VPN or NAT between it and cactusdb silently drops it: the
	// browser would otherwise send the next request into a dead connection
	// and get no answer ("Failed to fetch").
	srv := &http.Server{Handler: statusInHeader(logRequests(recoverPanics(mux))), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 20 * time.Second}
	go s.expireSessions()
	go logUpdate()
	previous, stopped := trackRun(filepath.Dir(*connDB), *port)
	s.previousRun = previous
	// How this cactusdb was started, so an upgrade from the web UI can start
	// the new version the same way (upgrade.go).
	if err := saveStartup(store, currentStartup(*port)); err != nil {
		log.Printf("warning: cannot save the startup options (an upgrade from the web UI can't restart cactusdb): %v", err)
	}

	fmt.Printf("cactusdb %s\n", version)
	for _, d := range driverInfo() {
		fmt.Printf("  driver %-15s %s %s (bundled)\n", d["engines"], d["module"], d["version"])
	}
	fmt.Printf("  saved connections: %s (credentials encrypted with %s)\n", *connDB, store.keyDesc)
	fmt.Printf("  export folder:     %s\n", exportDir)
	if store.SuperuserSet() {
		fmt.Println("  superuser:         set (opens every saved connection)")
	} else {
		fmt.Println("  superuser:         not set (cactusdb --superuser set)")
	}
	for _, u := range browseURLs(*host, *port) {
		fmt.Printf("  open %s\n", u)
	}
	if ip := net.ParseIP(*host); ip != nil && ip.IsLoopback() {
		fmt.Println("  (only this computer can connect; leave out --host to allow others)")
	} else if *host == "0.0.0.0" || *host == "::" {
		fmt.Println("  (listening on every network interface; --host 127.0.0.1 limits it to this computer)")
	}
	if previous != "" {
		log.Printf("warning: %s", previous)
	}
	if childLog != "" {
		log.Printf("started in the background, process id %d, listening on %s", os.Getpid(), ln.Addr())
		defer os.Remove(pidFile(childLog))
	} else {
		if *logFile != "" {
			fmt.Printf("  log file:          %s\n", *logFile)
			log.Printf("started, process id %d, listening on %s", os.Getpid(), ln.Addr())
		}
		fmt.Println("  press Ctrl+C to stop (closing this terminal doesn't stop it; --runas background runs it without one)")
	}

	// Ctrl+C, kill, or the web UI's Shutdown button all stop the server the
	// same way: no new requests, running ones finish, then everything is
	// released.
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		// A closed terminal (SSH session ended or dropped) sends SIGHUP, and
		// writing to a pipe whose reader is gone SIGPIPE: either would kill
		// cactusdb on the spot, without a word in the log or crash log.
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP, syscall.SIGPIPE)
		for {
			select {
			case sig := <-hup:
				log.Printf("%v received (the terminal was closed?): cactusdb keeps running as process %d; stop it with kill %d", sig, os.Getpid(), os.Getpid())
				continue
			case sig := <-stop:
				log.Printf("stopping (%v)", sig)
			case <-s.shutdown:
				s.mu.Lock()
				upgrading := s.restart != nil
				s.mu.Unlock()
				if upgrading {
					log.Printf("stopping to start the new version")
				} else {
					log.Printf("stopping (shutdown requested from the web UI)")
				}
			}
			break
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Print(err)
		term.flush(2 * time.Second)
		os.Exit(1)
	}
	s.release()
	s.mu.Lock()
	restart := s.restart
	s.mu.Unlock()
	store.DB().Close()
	if restart != nil {
		log.Printf("stopped; starting cactusdb %s", restart.to)
	} else {
		log.Printf("stopped")
	}
	stopped()
	term.flush(2 * time.Second)
	if restart != nil {
		restart.restartNow(childLog)
	}
}

// release cancels running exports, waits briefly for them to clean up their
// temporary files, and closes every database connection.
func (s *server) release() {
	s.mu.Lock()
	var running []*webBatch
	for _, b := range s.batches {
		b.cancel()
		running = append(running, b)
	}
	s.mu.Unlock()
	deadline := time.After(5 * time.Second)
	for _, b := range running {
		select {
		case <-b.done:
		case <-deadline:
		}
	}
	s.mu.Lock()
	for id, ss := range s.sessions {
		ss.db.Close()
		delete(s.sessions, id)
	}
	s.mu.Unlock()
	s.dropZips()
}

// handleInfo tells the page how this cactusdb runs, e.g. whether it offers
// the Shutdown button.
func (s *server) handleInfo(w http.ResponseWriter, r *http.Request) {
	info := map[string]any{"version": version, "background": s.background, "pid": os.Getpid(), "started": processStart.Format(time.RFC3339),
		"previousRun": s.previousRun}
	if o, err := loadStartup(s.store, s.port); err == nil {
		info["startCommand"] = o.Command
	}
	writeJSON(w, 200, info)
}

// handleShutdown stops a background cactusdb. It must be a JSON request, which
// a browser only sends cross-site after a CORS preflight that cactusdb never
// allows, so other websites can't shut it down.
func (s *server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if !s.background {
		writeErr(w, 403, errors.New("only a cactusdb started with --runas background can be shut down from the browser; press Ctrl+C in its terminal"))
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeErr(w, 415, errors.New("expected a JSON request"))
		return
	}
	logAction(r, "shutdown requested")
	writeJSON(w, 200, map[string]bool{"ok": true})
	s.stopOnce.Do(func() { close(s.shutdown) })
}

// processStart lets the page see that cactusdb was restarted.
var processStart = time.Now()

func fatalUsage(msg string) {
	fmt.Fprintf(os.Stderr, "cactusdb: %s\nRun cactusdb --help for usage.\n", msg)
	os.Exit(2)
}

// browseURLs lists the addresses to open the web UI at. When listening on
// all interfaces that is every IPv4 address of this machine.
func browseURLs(host string, port int) []string {
	hosts := []string{host}
	if host == "0.0.0.0" || host == "::" {
		hosts = []string{"localhost"}
		if addrs, err := net.InterfaceAddrs(); err == nil {
			for _, a := range addrs {
				if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() && !ipn.IP.IsLinkLocalUnicast() {
					hosts = append(hosts, ipn.IP.String())
				}
			}
		}
	}
	urls := make([]string, len(hosts))
	for i, h := range hosts {
		urls[i] = "http://" + net.JoinHostPort(h, strconv.Itoa(port))
	}
	return urls
}

// exeDir is the folder containing the running binary (symlinks resolved).
func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return filepath.Dir(exe)
}

// statusRecorder remembers the status code a handler sent.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Some firewalls and intrusion-prevention devices take an HTTP 403 (or an
// HTTP DELETE request) for an attack and block the client from the port for minutes, open connections
// included: one locked connection or expired session then made cactusdb on a
// server unreachable from that computer. The web UI therefore asks (with the
// header below, or errors=in-header in a download link) for errors as a 200
// whose real status is in a response header; other clients, such as
// scripts, get the status as usual.
const (
	statusInHeaderReq  = "X-Cactusdb-Errors"
	statusInHeaderResp = "X-Cactusdb-Status"
)

type statusInHeaderWriter struct {
	http.ResponseWriter
}

func (w statusInHeaderWriter) WriteHeader(code int) {
	if code >= 400 {
		w.Header().Set(statusInHeaderResp, strconv.Itoa(code))
		code = http.StatusOK
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w statusInHeaderWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w statusInHeaderWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func statusInHeader(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Download links can't send a header: they say so in the URL.
		if r.Header.Get(statusInHeaderReq) == "in-header" || r.URL.Query().Get("errors") == "in-header" {
			w = statusInHeaderWriter{w}
		}
		h.ServeHTTP(w, r)
	})
}

// reqLog is what the log lines of one request share: the session it used,
// set when the handler looks the session up (or connect creates it).
type reqLog struct {
	session string

	// For the usage log (usage.go): the session's connection, and what the
	// handler logged with logAction.
	mu                 sync.Mutex
	connection, engine string
	details            []string
	done               bool // recorded: later lines (of a background job) aren't added
}

type reqLogKey struct{}

// noteSession records the session of request r for its log lines.
func noteSession(r *http.Request, id string) {
	if l, ok := r.Context().Value(reqLogKey{}).(*reqLog); ok && id != "" {
		l.session = id
	}
}

// noteConn records the connection of request r's session for the usage log.
func noteConn(r *http.Request, ss *session) {
	if l, ok := r.Context().Value(reqLogKey{}).(*reqLog); ok && ss != nil {
		l.mu.Lock()
		l.connection, l.engine = ss.label, ss.kind()
		l.mu.Unlock()
	}
}

// sessionTag is " session=<first 8 characters>" for a request whose session
// is known. The whole id isn't logged: it works like a password while the
// session lasts, and ☰ → Logs shows the log.
func sessionTag(r *http.Request) string {
	l, ok := r.Context().Value(reqLogKey{}).(*reqLog)
	if !ok || l.session == "" {
		return ""
	}
	return " session=" + l.session[:min(8, len(l.session))]
}

// logRequests logs every API call with its result, duration, client and
// session. The handlers log what each action did (see logAction).
func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Progress polls of "export all tables" are not actions; the export
		// itself is logged.
		// Nor are the Logs tab's reads: live, it would fill the log with itself.
		// Nor the page's usage events (see usage.go).
		if !strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api/usage" || (r.Method == "GET" && (strings.HasPrefix(r.URL.Path, "/api/exportall/") || r.URL.Path == "/api/logs")) {
			h.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		rl := &reqLog{}
		r = r.WithContext(context.WithValue(r.Context(), reqLogKey{}, rl))
		if id := r.URL.Query().Get("session"); id != "" { // known before the handler reads the body
			rl.session = id
		}
		h.ServeHTTP(rec, r)
		d := time.Since(start)
		log.Printf("%s%s %s %s -> %d (%s)", clientIP(r), sessionTag(r), r.Method, r.URL.Path, rec.status, d.Round(time.Millisecond))
		rl.mu.Lock()
		rl.done = true
		rl.mu.Unlock()
		recordRequest(r, rl, rec.status, d)
	})
}

// recoverPanics turns a crash in a handler into an error the page can show:
// otherwise the connection is just cut and the browser only says "Failed to
// fetch". The details go to the log.
func recoverPanics(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tw := &headerTracker{ResponseWriter: w}
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler { // a deliberate abort, not a crash
				panic(v)
			}
			log.Printf("%s %s %s: INTERNAL ERROR: %v\n%s", clientIP(r), r.Method, r.URL.Path, v, debug.Stack())
			if !tw.wrote {
				writeErr(tw, 500, fmt.Errorf("internal error in cactusdb while handling %s %s: %v (the details are in the cactusdb log)", r.Method, r.URL.Path, v))
			}
		}()
		h.ServeHTTP(tw, r)
	})
}

// headerTracker remembers whether a response has started.
type headerTracker struct {
	http.ResponseWriter
	wrote bool
}

func (t *headerTracker) WriteHeader(code int) { t.wrote = true; t.ResponseWriter.WriteHeader(code) }
func (t *headerTracker) Write(b []byte) (int, error) {
	t.wrote = true
	return t.ResponseWriter.Write(b)
}

// Flush keeps streaming responses working through the wrapper.
func (t *headerTracker) Flush() {
	if f, ok := t.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func clientIP(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

// logAction logs what a request did. SQL is put on one line and shortened;
// passwords are never logged.
func logAction(r *http.Request, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("%s%s %s", clientIP(r), sessionTag(r), msg)
	if l, ok := r.Context().Value(reqLogKey{}).(*reqLog); ok {
		l.mu.Lock()
		if !l.done {
			l.details = append(l.details, msg)
		}
		l.mu.Unlock()
	}
}

func oneLine(sql string) string {
	sql = strings.Join(strings.Fields(sql), " ")
	if r := []rune(sql); len(r) > 500 {
		sql = string(r[:500]) + "…"
	}
	return sql
}

// ---- helpers ---------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20))
	return dec.Decode(v)
}

// flagValueIf is v when the option was given, else "".
func flagValueIf(given bool, v string) string {
	if given {
		return v
	}
	return ""
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *server) session(r *http.Request, id string) (*session, error) {
	if id == "" {
		id = r.URL.Query().Get("session")
	}
	noteSession(r, id)
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[id]
	if !ok {
		return nil, errors.New("not connected (session expired) - please connect again")
	}
	ss.lastUsed = time.Now()
	noteConn(r, ss)
	return ss, nil
}

// ---- handlers --------------------------------------------------------------

func (s *server) handleEngines(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, engines)
}

func (s *server) handleDrivers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, driverInfo())
}

type connectReq struct {
	ConnParams
	SavedID int64  `json:"savedId"` // connect using a saved connection
	Save    bool   `json:"save"`    // also save these parameters
	Name    string `json:"name"`    // name to save under
	// KeepPasswordOf uses the stored password of that saved connection when
	// Password is empty, so the browser never needs to hold saved passwords.
	KeepPasswordOf int64 `json:"keepPasswordOf"`
	// LockPassword is the connection password of SavedID / KeepPasswordOf;
	// NewLockPassword protects a connection saved with Save.
	LockPassword    string `json:"lockPassword"`
	NewLockPassword string `json:"newLockPassword"`
	SuperPassword   string `json:"superPassword"` // opens any saved connection instead of LockPassword
}

// unlockConn opens saved connection id with its connection password. On
// failure it has written the error (a wrong password counts towards the
// throttle) and returns ok false.
func (s *server) unlockConn(w http.ResponseWriter, r *http.Request, id int64, password string) (SavedConn, *secretBox, bool) {
	return s.unlockConnAs(w, r, id, password, "")
}

// superThrottleID throttles wrong superuser passwords.
const superThrottleID = -1

// unlockConnAs is unlockConn that also accepts the superuser password.
func (s *server) unlockConnAs(w http.ResponseWriter, r *http.Request, id int64, password, superPassword string) (SavedConn, *secretBox, bool) {
	tid := id
	if superPassword != "" {
		tid = superThrottleID
	}
	if wait := s.throttle.wait(tid); wait > 0 {
		writeErr(w, 429, fmt.Errorf("too many wrong passwords: try again in %d seconds", int(wait.Seconds())+1))
		return SavedConn{}, nil, false
	}
	var c SavedConn
	var box *secretBox
	var err error
	if superPassword != "" {
		c, box, err = s.store.UnlockAsSuperuser(id, superPassword)
		if errors.Is(err, errLockWrong) {
			s.throttle.failed(superThrottleID)
			logAction(r, "superuser unlock FAILED: wrong superuser password")
			writeJSON(w, 403, map[string]any{"error": "wrong superuser password", "locked": true})
			return c, nil, false
		}
		if errors.Is(err, errNoEscrow) { // the page asks for the connection's own password once
			writeJSON(w, 409, map[string]any{"error": err.Error(), "needsOwnPassword": true, "savedId": id})
			return c, nil, false
		}
		if errors.Is(err, errNoSuperuser) {
			writeErr(w, 409, err)
			return c, nil, false
		}
		if err == nil {
			s.throttle.ok(superThrottleID)
			logAction(r, "superuser opened connection %q", c.Name)
		}
	} else {
		c, box, err = s.store.Unlock(id, password)
	}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeErr(w, 404, errors.New("saved connection not found"))
	case errors.Is(err, errLockRequired):
		writeJSON(w, 403, map[string]any{"error": err.Error(), "locked": true})
	case errors.Is(err, errLockWrong):
		s.throttle.failed(id)
		logAction(r, "unlock %q FAILED: wrong connection password", c.Name)
		writeJSON(w, 403, map[string]any{"error": err.Error(), "locked": true})
	case err != nil:
		writeErr(w, 500, err)
	default:
		s.throttle.ok(id)
		return c, box, true
	}
	return c, box, false
}

func (s *server) handleConnect(w http.ResponseWriter, r *http.Request) {
	var req connectReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	p := req.ConnParams
	label := req.Name
	var savedID int64
	var connBox *secretBox
	if req.SavedID != 0 {
		c, box, ok := s.unlockConnAs(w, r, req.SavedID, req.LockPassword, req.SuperPassword)
		if !ok {
			return
		}
		p, label, savedID, connBox = c.ConnParams, c.Name, c.ID, box
	} else if req.KeepPasswordOf != 0 && p.Password == "" {
		c, _, ok := s.unlockConnAs(w, r, req.KeepPasswordOf, req.LockPassword, req.SuperPassword)
		if !ok {
			return
		}
		p.Password = c.Password
	}
	if req.Save && req.SavedID == 0 {
		// Every saved connection gets its own password: check it before connecting.
		if err := checkLockPassword(req.NewLockPassword); err != nil {
			writeErr(w, 400, err)
			return
		}
		if err := needsDatabase(p); err != nil {
			writeErr(w, 400, err)
			return
		}
	}
	start := time.Now()
	db, err := openDB(r.Context(), p)
	if err != nil {
		logAction(r, "connect FAILED %s after %s: %v", defaultConnName(p), time.Since(start).Round(time.Millisecond), err)
		writeErr(w, 400, fmt.Errorf("connection failed: %w", explainConnErr(p, err)))
		return
	}
	objects, err := listObjects(r.Context(), dialect(p.Engine), db)
	if err != nil {
		db.Close()
		writeErr(w, 400, fmt.Errorf("connected, but listing tables failed: %w", err))
		return
	}

	resp := map[string]any{"objects": objects}
	if req.Save && req.SavedID == 0 {
		if strings.TrimSpace(req.Name) == "" {
			req.Name = defaultConnName(p)
		}
		id, box, err := s.store.SaveLocked(SavedConn{Name: strings.TrimSpace(req.Name), ConnParams: p}, req.NewLockPassword, nil)
		if err != nil {
			resp["saveError"] = err.Error()
		} else {
			logAction(r, "save connection %q (password-protected)", req.Name)
			resp["savedId"] = id
			label, savedID, connBox = strings.TrimSpace(req.Name), id, box
		}
	}
	folder := connFolderName(label)
	if label == "" {
		label = defaultConnName(p)
		folder = unnamedConnFolder(p)
	}

	id := newID()
	s.mu.Lock()
	s.sessions[id] = &session{engine: dialect(p.Engine), flavor: p.Engine, label: label, dbName: ddlFileName(p), folder: folder, db: db,
		savedID: savedID, connBox: connBox, lastUsed: time.Now()}
	noteConn(r, s.sessions[id])
	s.mu.Unlock()
	noteSession(r, id)
	logAction(r, "connect %s as %q in %s", defaultConnName(p), label, time.Since(start).Round(time.Millisecond))
	resp["session"] = id
	resp["engine"] = p.Engine
	resp["dashboard"] = hasDashboard(dialect(p.Engine))
	resp["label"] = label
	resp["savedConn"] = savedID // saved queries are kept for saved connections
	resp["folder"] = folder     // its folder in the export folder (the Exported files page)
	writeJSON(w, 200, resp)
}

// handleTestConn checks that a connection works, without saving it or
// keeping it open: it connects, lists the tables and reports the server.
func (s *server) handleTestConn(w http.ResponseWriter, r *http.Request) {
	var req connectReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	p := req.ConnParams
	if req.KeepPasswordOf != 0 && p.Password == "" {
		c, _, ok := s.unlockConnAs(w, r, req.KeepPasswordOf, req.LockPassword, req.SuperPassword)
		if !ok {
			return
		}
		p.Password = c.Password
	}
	start := time.Now()
	db, err := openDB(r.Context(), p)
	if err != nil {
		logAction(r, "test connection FAILED %s: %v", defaultConnName(p), err)
		writeErr(w, 400, explainConnErr(p, err))
		return
	}
	defer db.Close()
	// Without a database, the form offers the server's databases to pick from.
	var dbs []string
	if strings.TrimSpace(p.Database) == "" && canListDatabases(p.Engine) {
		dbs, _ = listDatabases(r.Context(), p.Engine, db)
	}
	groups, err := listObjects(r.Context(), dialect(p.Engine), db)
	if err != nil && dbs == nil {
		logAction(r, "test connection FAILED %s: %v", defaultConnName(p), err)
		writeErr(w, 400, fmt.Errorf("connected, but listing tables failed: %w", err))
		return
	}
	tables, tablesLabel := 0, "tables"
	for _, g := range groups {
		if g.Type == ObjTable {
			tables, tablesLabel = len(g.Items), g.Label
		}
	}
	ver := serverVersion(r.Context(), p.Engine, db)
	if p.Engine == EnginePgvector {
		ver += pgvectorNote(r.Context(), db)
	}
	logAction(r, "test connection OK %s (%s, %d tables)", defaultConnName(p), ver, tables)
	writeJSON(w, 200, map[string]any{"version": ver, "tables": tables, "tablesLabel": tablesLabel, "elapsedMs": time.Since(start).Milliseconds(), "databases": dbs})
}

// serverVersion describes the database server, e.g. "MariaDB 11.8.3", or
// returns "" if it can't be read.
func serverVersion(ctx context.Context, engine string, db *sql.DB) string {
	if b := backendOf(db); b != nil {
		return b.Version(ctx)
	}
	flavor := engine
	engine = dialect(engine)
	q := map[string]string{
		EngineOracle:   `SELECT banner FROM v$version WHERE ROWNUM = 1`,
		EngineSQLite:   `SELECT 'SQLite ' || sqlite_version()`,
		EngineMySQL:    `SELECT VERSION()`,
		EngineMariaDB:  `SELECT VERSION()`,
		EngineFirebird: `SELECT 'Firebird ' || rdb$get_context('SYSTEM', 'ENGINE_VERSION') FROM rdb$database`,
		EngineMSSQL:    `SELECT @@VERSION`,
		EnginePostgres: `SELECT version()`,
	}[engine]
	var v sql.NullString
	if q == "" || db.QueryRowContext(ctx, q).Scan(&v) != nil {
		return ""
	}
	// SQL Server and PostgreSQL add build details after the first line/comma.
	ver := strings.TrimSpace(strings.SplitN(v.String, "\n", 2)[0])
	switch engine {
	case EnginePostgres:
		ver = strings.SplitN(ver, ",", 2)[0]
		switch flavor {
		case EngineCockroach: // "CockroachDB CCL v24.3.1 (aarch64-…)"
			ver, _, _ = strings.Cut(v.String, " (")
		case EngineRedshift: // "PostgreSQL 8.0.2 on i686-pc-linux-gnu, …, Redshift 1.0.77467"
			if i := strings.Index(v.String, "Redshift "); i >= 0 {
				ver = strings.TrimSpace(v.String[i:])
			}
		}
	case EngineMySQL, EngineMariaDB:
		// "11.8.9-MariaDB-ubu2404" or "9.1.0"; both drivers report either.
		if num, _, _ := strings.Cut(ver, "-"); strings.Contains(ver, "MariaDB") {
			ver = "MariaDB " + num
		} else {
			ver = "MySQL " + num
		}
	}
	return ver
}

func defaultConnName(p ConnParams) string {
	if e := engineByID(p.Engine); e != nil && e.File {
		return p.Engine + ": " + p.Database
	}
	where := hostPort(p.Host, p.Port, 0)
	if p.Port == 0 {
		where = p.Host // a region, project or URL
	}
	if p.Username != "" {
		where = p.Username + "@" + where
	}
	if p.Database != "" {
		where += "/" + p.Database
	}
	return p.Engine + ": " + where
}

func (s *server) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string `json:"session"`
	}
	readJSON(r, &req)
	noteSession(r, req.Session)
	s.mu.Lock()
	if ss, ok := s.sessions[req.Session]; ok {
		noteConn(r, ss)
		logAction(r, "disconnect %q", ss.label)
		s.cancelBatches(req.Session)
		ss.db.Close()
		delete(s.sessions, req.Session)
	}
	s.mu.Unlock()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// handleRefresh refreshes a session's connection and lists its objects
// again: the pooled connections that aren't in use are closed (a firewall or
// the database server may have cut them), a fresh one is opened and checked,
// and the tables, views, procedures, triggers… are read again.
func (s *server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string `json:"session"`
	}
	readJSON(r, &req)
	ss, err := s.session(r, req.Session)
	if err != nil {
		writeErr(w, 401, err)
		return
	}
	start := time.Now()
	ss.db.SetMaxIdleConns(0) // closes the idle ones
	ss.db.SetMaxIdleConns(2) // database/sql's default
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := ss.db.PingContext(ctx); err != nil {
		logAction(r, "[%s] refresh FAILED: %v", ss.label, err)
		writeErr(w, 502, fmt.Errorf("the database doesn't answer: %w (disconnect and connect again)", err))
		return
	}
	objects, err := listObjects(r.Context(), ss.engine, ss.db)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	n := 0
	for _, g := range objects {
		n += len(g.Items)
	}
	logAction(r, "[%s] refresh: connection checked, %d object(s) in %s", ss.label, n, time.Since(start).Round(time.Millisecond))
	writeJSON(w, 200, map[string]any{"objects": objects})
}

func (s *server) handleObjects(w http.ResponseWriter, r *http.Request) {
	ss, err := s.session(r, "")
	if err != nil {
		writeErr(w, 401, err)
		return
	}
	objects, err := listObjects(r.Context(), ss.engine, ss.db)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"objects": objects})
}

// handleDefinition returns the source / DDL of a table, view, procedure,
// trigger, generator or other object.
func (s *server) handleDefinition(w http.ResponseWriter, r *http.Request) {
	ss, err := s.session(r, "")
	if err != nil {
		writeErr(w, 401, err)
		return
	}
	typ, name := r.URL.Query().Get("type"), r.URL.Query().Get("name")
	if typ == "" || name == "" {
		writeErr(w, 400, errors.New("type and name are required"))
		return
	}
	logAction(r, "[%s] definition of %s %s", ss.label, typ, name)
	src, err := objectDefinition(r.Context(), ss.engine, ss.db, typ, name)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"type": typ, "name": name, "source": src})
}

// handleSelect builds the SELECT for a table and returns the first rows.
func (s *server) handleSelect(w http.ResponseWriter, r *http.Request) {
	ss, err := s.session(r, "")
	if err != nil {
		writeErr(w, 401, err)
		return
	}
	table := r.URL.Query().Get("table")
	if table == "" {
		writeErr(w, 400, errors.New("table is required"))
		return
	}
	q, err := buildSelect(r.Context(), ss.engine, ss.db, table)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	logAction(r, "[%s] open table %s", ss.label, table)
	s.runGrid(w, r, ss, q, limit)
}

type queryReq struct {
	Session string `json:"session"`
	SQL     string `json:"sql"`
	Limit   int    `json:"limit"`
	Format  string `json:"format"`
	Name    string `json:"name"`
	// SavedQuery: the export of a saved query, to queries/<date>/ (export.go).
	SavedQuery bool `json:"savedQuery"`
}

func (s *server) handleQuery(w http.ResponseWriter, r *http.Request) {
	var req queryReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	ss, err := s.session(r, req.Session)
	if err != nil {
		writeErr(w, 401, err)
		return
	}
	logAction(r, "[%s] query: %s", ss.label, oneLine(req.SQL))
	s.runGrid(w, r, ss, req.SQL, req.Limit)
}

// runGrid executes q and returns at most limit rows for display.
func (s *server) runGrid(w http.ResponseWriter, r *http.Request, ss *session, q string, limit int) {
	q = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(q), ";"))
	if q == "" {
		writeErr(w, 400, errors.New("SQL is empty"))
		return
	}
	if limit <= 0 {
		limit = 1000
	}
	// Cancelling the context once enough rows are read stops the server from
	// streaming the rest of a large table.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	start := time.Now()
	var cols []string
	rows := [][]any{}
	truncated := false
	err := streamQuery(ctx, ss.engine, ss.db, q, func(c []string) error {
		cols = c
		return nil
	}, func(row []any) (bool, error) {
		if len(rows) >= limit {
			truncated = true
			return false, nil
		}
		rows = append(rows, row)
		return true, nil
	})
	if err != nil {
		logAction(r, "[%s] query FAILED: %v", ss.label, err)
		writeErr(w, 400, err)
		return
	}
	more := ""
	if truncated {
		more = " (more not shown)"
	}
	logAction(r, "[%s] %d row(s)%s in %s", ss.label, len(rows), more, time.Since(start).Round(time.Millisecond))
	if cols == nil {
		cols = []string{}
	}
	writeJSON(w, 200, map[string]any{
		"sql":       q,
		"columns":   cols,
		"rows":      rows,
		"truncated": truncated,
		"limit":     limit,
		"elapsedMs": time.Since(start).Milliseconds(),
	})
}

// handleExport streams the full (unlimited) result of SQL as a file download.
// handleExport writes the full (unlimited) result of SQL to a file in the
// export folder and returns where it was saved.
func (s *server) handleExport(w http.ResponseWriter, r *http.Request) {
	var req queryReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	ss, err := s.session(r, req.Session)
	if err != nil {
		writeErr(w, 401, err)
		return
	}
	q := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(req.SQL), ";"))
	if q == "" {
		writeErr(w, 400, errors.New("SQL is empty"))
		return
	}
	var path string
	var rows int
	if req.SavedQuery {
		path, rows, err = queryExportToDir(r.Context(), ss.exportDir(s.exportFolder()), ss.engine, ss.db, q, req.Format, req.Name)
	} else {
		path, rows, err = exportToDir(r.Context(), ss.exportDir(s.exportFolder()), ss.engine, ss.db, q, req.Format, req.Name, nil)
	}
	if err != nil {
		logAction(r, "[%s] export %s FAILED: %v (sql: %s)", ss.label, req.Format, err, oneLine(q))
		writeErr(w, 400, err)
		return
	}
	logAction(r, "[%s] export %s: %d row(s) to %s (sql: %s)", ss.label, req.Format, rows, path, oneLine(q))
	writeJSON(w, 200, map[string]any{
		"path":     path,
		"file":     filepath.Base(path),
		"folder":   filepath.Dir(path),
		"rows":     rows,
		"download": downloadURL(path),
	})
}

// handleExportDDL writes the DDL of every object in the database to a .sql
// file in the export folder.
func (s *server) handleExportDDL(w http.ResponseWriter, r *http.Request) {
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
	path, n, err := exportDDLToDir(r.Context(), ss.exportDir(s.exportFolder()), ss.engine, ss.db, ss.label, ss.dbName, ddlTargets(groups), nil)
	if err != nil {
		logAction(r, "[%s] DDL export FAILED: %v", ss.label, err)
		writeErr(w, 400, err)
		return
	}
	logAction(r, "[%s] DDL export: %d object(s) to %s", ss.label, n, path)
	writeJSON(w, 200, map[string]any{
		"path":     path,
		"file":     filepath.Base(path),
		"folder":   filepath.Dir(path),
		"objects":  n,
		"download": downloadURL(path),
	})
}

// downloadURL is the web UI link to an exported file:
// /api/exports/<connection>/<type>/<file>.
func downloadURL(path string) string {
	kindDir := filepath.Dir(path)
	if dayDir := kindDir; filepath.Base(filepath.Dir(dayDir)) == queriesSubdir { // <conn>/queries/<day>/<file>
		connDir := filepath.Dir(filepath.Dir(dayDir))
		return "/api/exports/" + url.PathEscape(filepath.Base(connDir)) + "/" + queriesSubdir + "/" +
			url.PathEscape(filepath.Base(dayDir)) + "/" + url.PathEscape(filepath.Base(path))
	}
	return "/api/exports/" + url.PathEscape(filepath.Base(filepath.Dir(kindDir))) + "/" +
		url.PathEscape(filepath.Base(kindDir)) + "/" + url.PathEscape(filepath.Base(path))
}

// handleDownload serves an exported file: <connection>/<type>/<file>,
// <connection>/queries/<day>/<file> (saved queries), or <type>/<file> for
// exports made before files were sorted by connection.
func (s *server) handleDownload(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.PathValue("path"), "/")
	conn, day := "", ""
	if len(parts) == 4 && parts[1] == queriesSubdir {
		conn, day, parts = parts[0], parts[2], []string{queriesSubdir, parts[3]}
	} else if len(parts) == 3 {
		conn, parts = parts[0], parts[1:]
	}
	if len(parts) != 2 {
		writeErr(w, 400, errors.New("bad file name"))
		return
	}
	kind, file := parts[0], parts[1]
	if day != "" && (conn == "" || !validFileName(day)) {
		writeErr(w, 400, errors.New("bad file name"))
		return
	}
	if !validFileName(file) || !(exportSubdirs[kind] || day != "") || (conn != "" && (!validFileName(conn) || conn != connFolderName(conn))) {
		writeErr(w, 400, errors.New("bad file name"))
		return
	}
	// A protected connection's files: only for the session that opened it.
	if id, ok := s.protectedFolders()[conn]; ok && conn != "" {
		s.mu.Lock()
		ss := s.sessions[r.URL.Query().Get("session")]
		s.mu.Unlock()
		if ss == nil || ss.savedID != id {
			writeErr(w, 403, errors.New("this connection's files are protected: download them from the Exported files page with its connection password"))
			return
		}
	}
	// The newest folder first: the export folder may have been changed since.
	var path string
	for _, dir := range s.exportFolders() {
		p := filepath.Join(exportSubdir(filepath.Join(dir, conn), kind), file)
		if day != "" {
			p = filepath.Join(dir, conn, queriesSubdir, day, file)
		}
		if fileExists(p) {
			path = p
			break
		}
	}
	if path == "" {
		writeErr(w, 404, errors.New("export not found"))
		return
	}
	logAction(r, "download %s", path)
	w.Header().Set("Content-Disposition", `attachment; filename="`+file+`"`)
	http.ServeFile(w, r, path)
}

// validFileName is a plain name: no path, not hidden, not "." or "..".
func validFileName(name string) bool {
	return name != "" && name == filepath.Base(name) && !strings.HasPrefix(name, ".") && !strings.ContainsAny(name, `/\`)
}

// savedConnInfo is what the browser sees of a saved connection: everything
// except the password, which stays encrypted on the server.
type savedConnInfo struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Engine      string `json:"engine"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	Database    string `json:"database"`
	Options     string `json:"options"`
	HasPassword bool   `json:"hasPassword"`
	Locked      bool   `json:"locked"` // has its own connection password; username shown only after unlocking
	Created     string `json:"created"`
}

func (s *server) handleListConns(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.List()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	out := make([]savedConnInfo, len(list))
	for i, c := range list {
		out[i] = savedConnInfo{ID: c.ID, Name: c.Name, Engine: c.Engine, Host: c.Host, Port: c.Port,
			Username: c.Username, Database: c.Database, Options: c.Options, HasPassword: c.Password != "", Locked: c.Lock != "", Created: c.Created}
	}
	writeJSON(w, 200, out)
}

// handleUnlockConn checks a connection password and returns the connection's
// details for editing (never its database password).
func (s *server) handleUnlockConn(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, errors.New("bad id"))
		return
	}
	var req struct {
		LockPassword  string `json:"lockPassword"`
		SuperPassword string `json:"superPassword"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	c, _, ok := s.unlockConnAs(w, r, id, req.LockPassword, req.SuperPassword)
	if !ok {
		return
	}
	writeJSON(w, 200, savedConnInfo{ID: c.ID, Name: c.Name, Engine: c.Engine, Host: c.Host, Port: c.Port,
		Username: c.Username, Database: c.Database, Options: c.Options, HasPassword: c.Password != "", Locked: c.Lock != "", Created: c.Created})
}

// handleSaveConn saves a connection with its own connection password: a new
// one (no id; the password is required), or an existing one (id), which
// needs its current connection password and may get a new one.
func (s *server) handleSaveConn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SavedConn
		KeepPassword    bool   `json:"keepPassword"`    // empty database password = keep the stored one
		LockPassword    string `json:"lockPassword"`    // the current connection password (editing)
		NewLockPassword string `json:"newLockPassword"` // a new connection password ("" = keep it)
		SuperPassword   string `json:"superPassword"`   // the superuser password instead of LockPassword
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	c := req.SavedConn
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		c.Name = defaultConnName(c.ConnParams)
	}
	if c.Engine == "" {
		writeErr(w, 400, errors.New("engine is required"))
		return
	}
	if err := needsDatabase(c.ConnParams); err != nil {
		writeErr(w, 400, err)
		return
	}
	var oldBox *secretBox
	if c.ID != 0 {
		old, box, ok := s.unlockConnAs(w, r, c.ID, req.LockPassword, req.SuperPassword)
		if !ok {
			return
		}
		oldBox = box
		if req.KeepPassword && c.Password == "" {
			c.Password = old.Password
		}
		if old.Lock == "" && req.NewLockPassword == "" {
			writeErr(w, 400, errors.New("choose a connection password: every saved connection is protected by its own password"))
			return
		}
	} else if req.NewLockPassword == "" {
		writeErr(w, 400, errors.New("choose a connection password: every saved connection is protected by its own password"))
		return
	}
	id, box, err := s.store.SaveLocked(c, req.NewLockPassword, oldBox)
	if err != nil {
		code := 400
		if strings.Contains(err.Error(), "already exists") {
			code = 409
		}
		writeErr(w, code, err)
		return
	}
	// Open sessions of this connection follow a new name or password.
	s.mu.Lock()
	for _, ss := range s.sessions {
		if ss.savedID == id {
			ss.connBox, ss.label = box, c.Name
		}
	}
	s.mu.Unlock()
	logAction(r, "save connection %q (%s, password-protected)", c.Name, defaultConnName(c.ConnParams))
	writeJSON(w, 200, map[string]any{"id": id})
}

// handleDeleteConn deletes a saved connection and its saved queries; one
// with its own password needs it. JSON only, like the other changes.
func (s *server) handleDeleteConn(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, errors.New("bad id"))
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeErr(w, 415, errors.New("expected a JSON request"))
		return
	}
	var req struct {
		LockPassword  string `json:"lockPassword"`
		SuperPassword string `json:"superPassword"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	c, _, ok := s.unlockConnAs(w, r, id, req.LockPassword, req.SuperPassword)
	if !ok {
		return
	}
	if err := s.store.Delete(id); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	for _, ss := range s.sessions {
		if ss.savedID == id {
			ss.savedID, ss.connBox = 0, nil
		}
	}
	s.mu.Unlock()
	logAction(r, "delete saved connection %q and its saved queries", c.Name)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
