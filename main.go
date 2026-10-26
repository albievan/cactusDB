// cactusdb is a small read-only database viewer and exporter for SQLite,
// MySQL, MariaDB, Firebird, SQL Server and PostgreSQL, packaged as a single
// self-contained binary. It serves a web UI, or exports tables straight from
// the command line.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var version = "1.3.0"

//go:embed web
var webFS embed.FS

type session struct {
	engine string
	label  string
	db     *sql.DB
}

type server struct {
	store     *Store
	exportDir string

	mu       sync.Mutex
	sessions map[string]*session
}

func main() {
	port := flag.Int("port", 8080, "HTTP port to serve the web UI on")
	host := flag.String("host", "", "web UI: address to listen on (default 127.0.0.1; 0.0.0.0 allows other machines)\ncommand-line export: database server host (default localhost)")
	web := flag.String("web", "", "yes: start the web UI (the default when no --dbtype is given)")
	connDB := flag.String("conn-db", "", "SQLite file used to store saved connections (default: cactus.conn.db next to the binary)")
	keyFile := flag.String("key-file", "", "key used to encrypt saved usernames/passwords (default: cactus.key next to the connection file; ignored when $"+passphraseEnv+" is set)")
	exportTo := flag.String("exportto", "", "folder exports are written to (default: ./export next to the binary)")
	dbType := flag.String("dbtype", "", "command-line export: sqlite, mysql, mariadb, firebird, mssql or postgres")
	dbUser := flag.String("user", "", "command-line export: database user name")
	dbPassword := flag.String("dbpassword", "", "command-line export: database password (prompted for when omitted, or set $"+dbPasswordEnv+")")
	dbName := flag.String("dbname", "", "command-line export: database name (SQLite/Firebird: the database file)")
	dbPort := flag.Int("dbport", 0, "command-line export: database server port (default: the engine's standard port)")
	dbTable := flag.String("dbtable", "", "command-line export: table to export, several separated by commas, or all")
	showTable := flag.String("showtable", "", "command-line: all lists the tables and views; a table name prints its definition")
	exportAs := flag.String("exportas", "csv", "command-line export: csv, json, xml or excel")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "cactusdb %s - browse and export SQLite, MySQL, MariaDB, Firebird, SQL Server and PostgreSQL databases\n\n", version)
		fmt.Fprintf(os.Stderr, "Usage:\n"+
			"  cactusdb --port 8080 [--web yes]                    start the web UI\n"+
			"  cactusdb --dbtype mariadb --host 10.66.1.136 --dbport 3306 --user root --dbname optima \\\n"+
			"           --dbtable orders|t1,t2|all --exportas csv|json|xml|excel [--exportto ./export]\n"+
			"                                                      export tables without the web UI\n"+
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
	cliMode := set["dbtype"] || set["dbtable"] || set["showtable"]
	switch {
	case cliMode && webOn:
		fatalUsage("--web yes cannot be combined with --dbtype/--dbtable/--showtable")
	case !cliMode && webGiven && !webOn:
		fatalUsage("--web no: nothing to do; give --dbtype and --dbtable or --showtable to export without the web UI")
	}

	if *exportTo == "" {
		*exportTo = filepath.Join(exeDir(), "export")
	}
	exportDir, err := filepath.Abs(*exportTo)
	if err != nil {
		log.Fatalf("bad export folder %s: %v", *exportTo, err)
	}

	if cliMode {
		for _, f := range []string{"port", "conn-db", "key-file"} {
			if set[f] {
				fatalUsage("--" + f + " only applies to the web UI (the database port is --dbport)")
			}
		}
		if !set["dbtype"] {
			fatalUsage("--dbtype is required")
		}
		if *dbTable == "" && *showTable == "" {
			fatalUsage("give --dbtable to export, or --showtable to list tables")
		}
		engine, err := parseEngine(*dbType)
		if err != nil {
			fatalUsage(err.Error())
		}
		format, err := parseExportFormat(*exportAs)
		if err != nil {
			fatalUsage(err.Error())
		}
		if *dbName == "" {
			fatalUsage("--dbname is required")
		}
		if engine != EngineSQLite && *dbUser == "" {
			fatalUsage("--user is required")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		err = runCLI(ctx, cliOptions{
			ConnParams:  ConnParams{Engine: engine, Host: *host, Port: *dbPort, Username: *dbUser, Password: *dbPassword, Database: *dbName},
			PasswordSet: set["dbpassword"],
			Tables:      *dbTable,
			Show:        *showTable,
			Format:      format,
			ExportDir:   exportDir,
		}, os.Stdout)
		if err != nil {
			fmt.Fprintln(os.Stderr, "cactusdb:", err)
			os.Exit(1)
		}
		return
	}

	for _, f := range []string{"user", "dbpassword", "dbname", "dbport", "exportas"} {
		if set[f] {
			fatalUsage("--" + f + " is for command-line export and needs --dbtype")
		}
	}
	if *host == "" {
		*host = "127.0.0.1"
	}
	if *connDB == "" {
		*connDB = filepath.Join(exeDir(), "cactus.conn.db")
	}
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		log.Fatalf("cannot create export folder %s: %v", exportDir, err)
	}

	if *keyFile == "" {
		*keyFile = filepath.Join(filepath.Dir(*connDB), keyFileName)
	}
	store, err := openStore(*connDB, *keyFile)
	if err != nil {
		log.Fatalf("cannot open connection store %s: %v", *connDB, err)
	}
	s := &server{store: store, exportDir: exportDir, sessions: map[string]*session{}}

	static, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /api/engines", s.handleEngines)
	mux.HandleFunc("GET /api/drivers", s.handleDrivers)
	mux.HandleFunc("POST /api/connect", s.handleConnect)
	mux.HandleFunc("POST /api/disconnect", s.handleDisconnect)
	mux.HandleFunc("GET /api/objects", s.handleObjects)
	mux.HandleFunc("GET /api/definition", s.handleDefinition)
	mux.HandleFunc("GET /api/select", s.handleSelect)
	mux.HandleFunc("POST /api/query", s.handleQuery)
	mux.HandleFunc("POST /api/export", s.handleExport)
	mux.HandleFunc("GET /api/exports/{file}", s.handleDownload)
	mux.HandleFunc("GET /api/connections", s.handleListConns)
	mux.HandleFunc("POST /api/connections", s.handleSaveConn)
	mux.HandleFunc("DELETE /api/connections/{id}", s.handleDeleteConn)

	addr := net.JoinHostPort(*host, strconv.Itoa(*port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("cannot listen on %s: %v", addr, err)
	}
	srv := &http.Server{Handler: logRequests(mux), ReadHeaderTimeout: 10 * time.Second}

	fmt.Printf("cactusdb %s\n", version)
	for _, d := range driverInfo() {
		fmt.Printf("  driver %-15s %s %s (bundled)\n", d["engines"], d["module"], d["version"])
	}
	fmt.Printf("  saved connections: %s (credentials encrypted with %s)\n", *connDB, store.keyDesc)
	fmt.Printf("  export folder:     %s\n", exportDir)
	for _, u := range browseURLs(*host, *port) {
		fmt.Printf("  open %s\n", u)
	}
	if ip := net.ParseIP(*host); ip != nil && ip.IsLoopback() {
		fmt.Println("  (only this computer can connect; start with --host 0.0.0.0 to allow others)")
	}
	fmt.Println("  press Ctrl+C to stop")

	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt)
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	s.mu.Lock()
	for _, ss := range s.sessions {
		ss.db.Close()
	}
	s.mu.Unlock()
	store.db.Close()
}

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

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			log.Printf("%s %s", r.Method, r.URL.Path)
		}
		h.ServeHTTP(w, r)
	})
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

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *server) session(r *http.Request, id string) (*session, error) {
	if id == "" {
		id = r.URL.Query().Get("session")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[id]
	if !ok {
		return nil, errors.New("not connected (session expired) - please connect again")
	}
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
}

func (s *server) handleConnect(w http.ResponseWriter, r *http.Request) {
	var req connectReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	p := req.ConnParams
	label := req.Name
	if req.SavedID != 0 {
		c, err := s.store.Get(req.SavedID)
		if err != nil {
			writeErr(w, 404, fmt.Errorf("saved connection not found"))
			return
		}
		p, label = c.ConnParams, c.Name
	} else if req.KeepPasswordOf != 0 && p.Password == "" {
		c, err := s.store.Get(req.KeepPasswordOf)
		if err != nil {
			writeErr(w, 404, fmt.Errorf("saved connection not found"))
			return
		}
		p.Password = c.Password
	}
	db, err := openDB(r.Context(), p)
	if err != nil {
		writeErr(w, 400, fmt.Errorf("connection failed: %w", err))
		return
	}
	objects, err := listObjects(r.Context(), p.Engine, db)
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
		id, err := s.store.Save(SavedConn{Name: strings.TrimSpace(req.Name), ConnParams: p})
		if err != nil {
			resp["saveError"] = err.Error()
		} else {
			resp["savedId"] = id
			label = req.Name
		}
	}
	if label == "" {
		label = defaultConnName(p)
	}

	id := newID()
	s.mu.Lock()
	s.sessions[id] = &session{engine: p.Engine, label: label, db: db}
	s.mu.Unlock()
	resp["session"] = id
	resp["engine"] = p.Engine
	resp["label"] = label
	writeJSON(w, 200, resp)
}

func defaultConnName(p ConnParams) string {
	if p.Engine == EngineSQLite {
		return "sqlite: " + p.Database
	}
	return fmt.Sprintf("%s: %s@%s/%s", p.Engine, p.Username, hostPort(p.Host, p.Port, 0), p.Database)
}

func (s *server) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string `json:"session"`
	}
	readJSON(r, &req)
	s.mu.Lock()
	if ss, ok := s.sessions[req.Session]; ok {
		ss.db.Close()
		delete(s.sessions, req.Session)
	}
	s.mu.Unlock()
	writeJSON(w, 200, map[string]bool{"ok": true})
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
	s.runGrid(w, r, ss, q, limit)
}

type queryReq struct {
	Session string `json:"session"`
	SQL     string `json:"sql"`
	Limit   int    `json:"limit"`
	Format  string `json:"format"`
	Name    string `json:"name"`
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
		writeErr(w, 400, err)
		return
	}
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
	path, rows, err := exportToDir(r.Context(), s.exportDir, ss.engine, ss.db, q, req.Format, req.Name)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	file := filepath.Base(path)
	writeJSON(w, 200, map[string]any{
		"path":     path,
		"file":     file,
		"rows":     rows,
		"download": "/api/exports/" + url.PathEscape(file),
	})
}

// handleDownload serves a file from the export folder.
func (s *server) handleDownload(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	if file == "" || file != filepath.Base(file) || strings.HasPrefix(file, ".") {
		writeErr(w, 400, errors.New("bad file name"))
		return
	}
	path := filepath.Join(s.exportDir, file)
	if !fileExists(path) {
		writeErr(w, 404, errors.New("export not found"))
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+file+`"`)
	http.ServeFile(w, r, path)
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
	HasPassword bool   `json:"hasPassword"`
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
			Username: c.Username, Database: c.Database, HasPassword: c.Password != "", Created: c.Created}
	}
	writeJSON(w, 200, out)
}

func (s *server) handleSaveConn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SavedConn
		KeepPassword bool `json:"keepPassword"` // empty password = keep the stored one
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	c := req.SavedConn
	c.Name = strings.TrimSpace(c.Name)
	if req.KeepPassword && c.Password == "" {
		if old, err := s.store.GetByName(c.Name); err == nil {
			c.Password = old.Password
		}
	}
	if c.Name == "" {
		c.Name = defaultConnName(c.ConnParams)
	}
	if c.Engine == "" {
		writeErr(w, 400, errors.New("engine is required"))
		return
	}
	id, err := s.store.Save(c)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *server) handleDeleteConn(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, errors.New("bad id"))
		return
	}
	if err := s.store.Delete(id); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
