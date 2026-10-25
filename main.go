// cactusdb is a small browser-based database viewer for SQLite, MySQL,
// MariaDB and Firebird, packaged as a single self-contained binary.
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

var version = "1.0.0"

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
	host := flag.String("host", "127.0.0.1", "address to listen on (use 0.0.0.0 to allow other machines)")
	connDB := flag.String("conn-db", "", "SQLite file used to store saved connections (default: cactus.conn.db next to the binary)")
	exportTo := flag.String("exportto", "", "folder exports are written to (default: ./export next to the binary)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "cactusdb %s - browse SQLite, MySQL, MariaDB and Firebird databases in your browser\n\n", version)
		fmt.Fprintf(os.Stderr, "Usage:\n  cactusdb --port 8080\n\nOptions:\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("cactusdb", version)
		return
	}

	if *connDB == "" {
		*connDB = filepath.Join(exeDir(), "cactus.conn.db")
	}
	if *exportTo == "" {
		*exportTo = filepath.Join(exeDir(), "export")
	}
	exportDir, err := filepath.Abs(*exportTo)
	if err != nil {
		log.Fatalf("bad export folder %s: %v", *exportTo, err)
	}
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		log.Fatalf("cannot create export folder %s: %v", exportDir, err)
	}

	store, err := openStore(*connDB)
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

	browseHost := *host
	if browseHost == "0.0.0.0" || browseHost == "::" || browseHost == "" {
		browseHost = "localhost"
	}
	fmt.Printf("cactusdb %s\n", version)
	for _, d := range driverInfo() {
		fmt.Printf("  driver %-15s %s %s (bundled)\n", d["engines"], d["module"], d["version"])
	}
	fmt.Printf("  saved connections: %s\n  export folder:     %s\n  open http://%s\n  press Ctrl+C to stop\n",
		*connDB, exportDir, net.JoinHostPort(browseHost, strconv.Itoa(*port)))

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

func (s *server) handleListConns(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.List()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, list)
}

func (s *server) handleSaveConn(w http.ResponseWriter, r *http.Request) {
	var c SavedConn
	if err := readJSON(r, &c); err != nil {
		writeErr(w, 400, err)
		return
	}
	c.Name = strings.TrimSpace(c.Name)
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
