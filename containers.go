package main

// ☰ → DB containers: deploy a database server as a container, on the Docker
// (Docker CE or Docker Desktop) or Kubernetes (k8s, k3s) of the computer
// cactusdb runs on. Everything goes through the docker and kubectl commands,
// never a shell; passwords reach them through the environment (docker) or a
// Secret sent on stdin (kubectl), never the command line. Deploying, removing
// and reading container logs need the superuser password.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// ---- the catalogue -------------------------------------------------------------

type envVar struct {
	Name, Value string
	Secret      bool // a password: never on a command line
}

// containerSpec is a database cactusdb can deploy.
type containerSpec struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Engine   string `json:"engine"` // the cactusdb engine to connect with
	Image    string `json:"image"`
	Port     int    `json:"port"`     // the port inside the container
	DataPath string `json:"dataPath"` // where the database keeps its files, inside the container
	UID      int    `json:"-"`        // the user the database runs as, when not root: owns the data folder

	User         string `json:"user,omitempty"`         // the user to sign in as
	UserEditable bool   `json:"userEditable,omitempty"` // the user is created with the name given
	NoPassword   bool   `json:"noPassword,omitempty"`   // the image has no sign-in to set up
	PassRule     string `json:"passRule,omitempty"`     // "mssql": SQL Server's complexity rules; "min8"
	PassLabel    string `json:"passLabel,omitempty"`
	DB           string `json:"db,omitempty"`         // the database to connect to
	DBEditable   bool   `json:"dbEditable,omitempty"` // a database of this name is created
	DBLabel      string `json:"dbLabel,omitempty"`
	MemoryMB     int    `json:"memoryMB,omitempty"` // what it needs at least
	Note         string `json:"note,omitempty"`

	env  func(d deployReq) []envVar
	args []string // the container's command (arguments to the image's entrypoint)
	// dbPath turns the database field into the connection's database.
	dbPath func(d deployReq) string
}

var containerCatalog = []containerSpec{
	{ID: "mssql", Name: "SQL Server 2022 (Developer)", Engine: EngineMSSQL, Image: "mcr.microsoft.com/mssql/server:2022-latest",
		Port: 1433, DataPath: "/var/opt/mssql", UID: 10001, User: "sa", PassRule: "mssql", PassLabel: "sa password", MemoryMB: 2048,
		Note: "The free Developer edition: for development and testing, not production. Images are x86-64 only: on Apple silicon Docker Desktop runs it with Rosetta.",
		env: func(d deployReq) []envVar {
			return []envVar{{"ACCEPT_EULA", "Y", false}, {"MSSQL_PID", "Developer", false}, {"MSSQL_SA_PASSWORD", d.Password, true}}
		}},
	{ID: "postgres", Name: "PostgreSQL 17", Engine: EnginePostgres, Image: "postgres:17",
		Port: 5432, DataPath: "/var/lib/postgresql/data", User: "postgres", UserEditable: true, DB: "postgres", DBEditable: true,
		env: postgresEnv},
	{ID: "pgvector", Name: "PostgreSQL 17 + pgvector", Engine: EnginePgvector, Image: "pgvector/pgvector:pg17",
		Port: 5432, DataPath: "/var/lib/postgresql/data", User: "postgres", UserEditable: true, DB: "postgres", DBEditable: true,
		Note: "The vector extension is installed; switch it on in a database with CREATE EXTENSION vector (cactusdb itself only reads).",
		env:  postgresEnv},
	{ID: "mysql", Name: "MySQL 8.4", Engine: EngineMySQL, Image: "mysql:8.4",
		Port: 3306, DataPath: "/var/lib/mysql", User: "root", DBEditable: true, DBLabel: "Database to create (optional)",
		env: func(d deployReq) []envVar {
			e := []envVar{{"MYSQL_ROOT_PASSWORD", d.Password, true}}
			if d.Database != "" {
				e = append(e, envVar{"MYSQL_DATABASE", d.Database, false})
			}
			return e
		}},
	{ID: "mariadb", Name: "MariaDB 11.4", Engine: EngineMariaDB, Image: "mariadb:11.4",
		Port: 3306, DataPath: "/var/lib/mysql", User: "root", DBEditable: true, DBLabel: "Database to create (optional)",
		env: func(d deployReq) []envVar {
			e := []envVar{{"MARIADB_ROOT_PASSWORD", d.Password, true}}
			if d.Database != "" {
				e = append(e, envVar{"MARIADB_DATABASE", d.Database, false})
			}
			return e
		}},
	{ID: "oracle", Name: "Oracle Database 23ai Free", Engine: EngineOracle, Image: "gvenzl/oracle-free:23-slim",
		Port: 1521, DataPath: "/opt/oracle/oradata", UID: 54321, User: "system", PassLabel: "SYS and SYSTEM password", DB: "FREEPDB1", MemoryMB: 2048,
		Note: "Oracle Database Free (community image gvenzl/oracle-free). The first start creates the database and takes a few minutes.",
		env:  func(d deployReq) []envVar { return []envVar{{"ORACLE_PASSWORD", d.Password, true}} }},
	{ID: "firebird", Name: "Firebird 5", Engine: EngineFirebird, Image: "firebirdsql/firebird:5",
		Port: 3050, DataPath: "/var/lib/firebird/data", User: "SYSDBA", PassLabel: "SYSDBA password", DB: "cactus.fdb", DBEditable: true, DBLabel: "Database file to create",
		env: func(d deployReq) []envVar {
			return []envVar{{"FIREBIRD_ROOT_PASSWORD", d.Password, true}, {"FIREBIRD_DATABASE", d.Database, false}}
		},
		dbPath: func(d deployReq) string { return "/var/lib/firebird/data/" + d.Database }},
	{ID: "mongodb", Name: "MongoDB 8.0", Engine: EngineMongoDB, Image: "mongo:8.0",
		Port: 27017, DataPath: "/data/db", User: "admin", UserEditable: true,
		env: func(d deployReq) []envVar {
			return []envVar{{"MONGO_INITDB_ROOT_USERNAME", d.Username, false}, {"MONGO_INITDB_ROOT_PASSWORD", d.Password, true}}
		}},
	{ID: "redis", Name: "Redis 7.4", Engine: EngineRedis, Image: "redis:7.4",
		Port: 6379, DataPath: "/data",
		// The password comes from the environment, so it isn't in the container's command.
		args: []string{"sh", "-c", `exec redis-server --appendonly yes --requirepass "$REDIS_PASSWORD"`},
		env:  func(d deployReq) []envVar { return []envVar{{"REDIS_PASSWORD", d.Password, true}} }},
	{ID: "neo4j", Name: "Neo4j 5 (Community)", Engine: EngineNeo4j, Image: "neo4j:5",
		Port: 7687, DataPath: "/data", User: "neo4j", PassRule: "min8", PassLabel: "neo4j password",
		env: func(d deployReq) []envVar { return []envVar{{"NEO4J_AUTH", "neo4j/" + d.Password, true}} }},
	{ID: "cassandra", Name: "Apache Cassandra 5.0", Engine: EngineCassandra, Image: "cassandra:5.0",
		Port: 9042, DataPath: "/var/lib/cassandra", NoPassword: true, MemoryMB: 2048,
		Note: "Without sign-in, as the image comes: anyone who can reach the port can use it. The first start takes about a minute.",
		env: func(deployReq) []envVar {
			return []envVar{{"MAX_HEAP_SIZE", "1G", false}, {"HEAP_NEWSIZE", "200M", false}}
		}},
	{ID: "cockroachdb", Name: "CockroachDB 24.3 (single node)", Engine: EngineCockroach, Image: "cockroachdb/cockroach:v24.3.0",
		Port: 26257, DataPath: "/cockroach/cockroach-data", User: "root", NoPassword: true, DB: "defaultdb",
		Note: "Insecure mode: no password and no TLS, for development only.",
		args: []string{"start-single-node", "--insecure"},
		env:  func(deployReq) []envVar { return nil }},
	{ID: "weaviate", Name: "Weaviate 1.28", Engine: EngineWeaviate, Image: "cr.weaviate.io/semitechnologies/weaviate:1.28.4",
		Port: 8080, DataPath: "/var/lib/weaviate", PassLabel: "API key",
		env: func(d deployReq) []envVar {
			return []envVar{{"PERSISTENCE_DATA_PATH", "/var/lib/weaviate", false}, {"DEFAULT_VECTORIZER_MODULE", "none", false},
				{"CLUSTER_HOSTNAME", "node1", false}, {"AUTHENTICATION_ANONYMOUS_ACCESS_ENABLED", "false", false},
				{"AUTHENTICATION_APIKEY_ENABLED", "true", false}, {"AUTHENTICATION_APIKEY_USERS", "admin", false},
				{"AUTHENTICATION_APIKEY_ALLOWED_KEYS", d.Password, true}}
		}},
}

func postgresEnv(d deployReq) []envVar {
	return []envVar{{"POSTGRES_USER", d.Username, false}, {"POSTGRES_PASSWORD", d.Password, true}, {"POSTGRES_DB", d.Database, false},
		// A subfolder: a volume's root may hold lost+found, which initdb refuses.
		{"PGDATA", "/var/lib/postgresql/data/pgdata", false}}
}

func specByID(id string) (containerSpec, bool) {
	for _, s := range containerCatalog {
		if s.ID == id {
			return s, true
		}
	}
	return containerSpec{}, false
}

// ---- finding Docker and Kubernetes -----------------------------------------------

// ctrRuntime is a place containers can be deployed to.
type ctrRuntime struct {
	ID      string `json:"id"`   // docker | kubernetes
	Name    string `json:"name"` // Docker Desktop, Docker CE, k3s, Kubernetes…
	Version string `json:"version,omitempty"`
	Context string `json:"context,omitempty"` // the kubectl context
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	tool    []string
}

// toolDirs are where docker, kubectl and k3s are usually installed: cactusdb
// running as a service may have a short PATH.
var toolDirs = map[string][]string{
	"docker": {"/usr/local/bin", "/usr/bin", "/opt/homebrew/bin", "/Applications/Docker.app/Contents/Resources/bin",
		`C:\Program Files\Docker\Docker\resources\bin`},
	"kubectl": {"/usr/local/bin", "/usr/bin", "/opt/homebrew/bin", "/snap/bin", "/Applications/Docker.app/Contents/Resources/bin",
		`C:\Program Files\Docker\Docker\resources\bin`},
	"k3s": {"/usr/local/bin", "/usr/bin"},
}

func findTool(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	exe := name
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	for _, d := range toolDirs[name] {
		p := filepath.Join(d, exe)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// toolCmd runs a tool with its own folder first on PATH, so docker finds its
// credential helpers.
func toolCmd(ctx context.Context, tool []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, tool[0], append(append([]string{}, tool[1:]...), args...)...)
	cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(tool[0])+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// runTool runs a command and returns its output; the error carries what it said.
func runTool(ctx context.Context, cmd *exec.Cmd, stdin []byte) (string, error) {
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if ctx.Err() != nil {
			return out.String(), fmt.Errorf("%s timed out", filepath.Base(cmd.Path))
		}
		if msg != "" {
			return out.String(), errors.New(lastLines(msg, 6))
		}
		return out.String(), err
	}
	return out.String(), nil
}

func lastLines(s string, n int) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, "\n")
}

func detectDocker(ctx context.Context) *ctrRuntime {
	rt := &ctrRuntime{ID: "docker", Name: "Docker"}
	p := findTool("docker")
	if p == "" {
		rt.Error = "not installed"
		return rt
	}
	rt.tool = []string{p}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, err := runTool(ctx, toolCmd(ctx, rt.tool, "version", "--format", "{{json .}}"), nil)
	var v struct {
		Server *struct {
			Version  string
			Platform struct{ Name string }
		}
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(out)), &v)
	if v.Server == nil {
		rt.Error = "the docker command is installed, but its engine isn't running or can't be reached"
		if err != nil {
			rt.Error += ": " + err.Error()
		}
		return rt
	}
	rt.OK, rt.Version = true, v.Server.Version
	switch pn := v.Server.Platform.Name; {
	case strings.Contains(pn, "Desktop"):
		rt.Name = "Docker Desktop"
		if f := strings.Fields(pn); len(f) >= 3 {
			rt.Name += " " + f[2]
		}
	case strings.Contains(pn, "Community"):
		rt.Name = "Docker CE"
	case pn != "":
		rt.Name = pn
	}
	return rt
}

func detectKubernetes(ctx context.Context) *ctrRuntime {
	rt := &ctrRuntime{ID: "kubernetes", Name: "Kubernetes"}
	if p := findTool("kubectl"); p != "" {
		rt.tool = []string{p}
	} else if p := findTool("k3s"); p != "" {
		rt.tool = []string{p, "kubectl"}
	} else {
		rt.Error = "kubectl isn't installed"
		return rt
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cur, _ := runTool(ctx, toolCmd(ctx, rt.tool, "config", "current-context"), nil)
	rt.Context = strings.TrimSpace(cur)
	out, err := runTool(ctx, toolCmd(ctx, rt.tool, "version", "-o", "json", "--request-timeout=6s"), nil)
	var v struct {
		ServerVersion *struct{ GitVersion string } `json:"serverVersion"`
	}
	_ = json.Unmarshal([]byte(out), &v)
	if v.ServerVersion == nil {
		rt.Error = "kubectl is installed, but no cluster answers"
		if err != nil {
			rt.Error += ": " + err.Error()
		}
		return rt
	}
	rt.OK, rt.Version = true, v.ServerVersion.GitVersion
	switch gv := v.ServerVersion.GitVersion; {
	case strings.Contains(gv, "k3s"):
		rt.Name = "k3s"
	case strings.Contains(gv, "rke2"):
		rt.Name = "RKE2"
	}
	return rt
}

func detectRuntimes(ctx context.Context) (docker, kube *ctrRuntime) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); docker = detectDocker(ctx) }()
	go func() { defer wg.Done(); kube = detectKubernetes(ctx) }()
	wg.Wait()
	return
}

// ---- what to deploy -----------------------------------------------------------------

type deployReq struct {
	Runtime     string `json:"runtime"` // docker | kubernetes
	Spec        string `json:"spec"`
	Name        string `json:"name"`
	Image       string `json:"image"`
	Port        int    `json:"port"`    // on this computer (docker), the service port (kubernetes)
	Public      bool   `json:"public"`  // docker: reachable from other computers, not only this one
	Restart     string `json:"restart"` // docker: no, on-failure, always or unless-stopped (""); kubernetes always restarts
	DataDir     string `json:"dataDir"` // a folder on this computer (docker) or on the node (kubernetes); "" = a volume
	Username    string `json:"username"`
	Password    string `json:"password"`
	Database    string `json:"database"`
	Namespace   string `json:"namespace"`
	ServiceType string `json:"serviceType"` // LoadBalancer | NodePort
	Storage     string `json:"storage"`     // the volume's size, e.g. 10Gi

	SuperPassword string `json:"superPassword"`
}

var (
	ctrNameRe    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)
	ctrImageRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]{0,254}$`)
	ctrUserRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
	ctrDBRe      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,62}$`)
	ctrStorageRe = regexp.MustCompile(`^[1-9][0-9]{0,5}(Mi|Gi|Ti)$`)
)

// check validates the request and fills in the defaults of its spec.
func (d *deployReq) check(sp containerSpec) error {
	d.Name, d.Image, d.DataDir = strings.TrimSpace(d.Name), strings.TrimSpace(d.Image), strings.TrimSpace(d.DataDir)
	if d.Image == "" {
		d.Image = sp.Image
	}
	switch d.Restart {
	case "":
		d.Restart = "unless-stopped"
	case "no", "on-failure", "always", "unless-stopped":
	default:
		return fmt.Errorf("unknown restart policy %q: use no, on-failure, always or unless-stopped", d.Restart)
	}
	if !ctrNameRe.MatchString(d.Name) {
		return errors.New("the name may have lowercase letters, digits and dashes (not at the start or end), at most 40")
	}
	if !ctrImageRe.MatchString(d.Image) {
		return fmt.Errorf("%q isn't an image name", d.Image)
	}
	if d.Port < 1 || d.Port > 65535 {
		return errors.New("the port must be between 1 and 65535")
	}
	if d.DataDir != "" {
		if strings.ContainsAny(d.DataDir, ",\n\r\x00") || (runtime.GOOS != "windows" || d.Runtime == "kubernetes") && strings.Contains(d.DataDir, ":") {
			return errors.New("the data folder can't contain a comma or colon")
		}
		abs := filepath.IsAbs(d.DataDir)
		if d.Runtime == "kubernetes" {
			abs = path.IsAbs(d.DataDir) // a folder on the node, which runs Linux
		}
		if !abs {
			return errors.New("the data folder must be a full path, e.g. " + exampleDataDir(d.Name))
		}
		if c := filepath.Clean(d.DataDir); c == "/" || c == filepath.VolumeName(c)+`\` {
			return errors.New("the data folder can't be the root of the disk")
		}
	}
	switch {
	case !sp.UserEditable || sp.User == "":
		d.Username = sp.User
	case d.Username == "":
		d.Username = sp.User
	case !ctrUserRe.MatchString(d.Username):
		return errors.New("the username may have letters, digits and _, and starts with a letter or _")
	}
	switch {
	case !sp.DBEditable:
		d.Database = sp.DB
	case d.Database == "":
		d.Database = sp.DB
	case !ctrDBRe.MatchString(d.Database):
		return errors.New("the database name may have letters, digits, _, . and -")
	}
	if sp.ID == "firebird" && d.Database == "" {
		return errors.New("enter the name of the database file to create")
	}
	if sp.NoPassword {
		d.Password = ""
	} else if err := checkCtrPassword(sp, d.Password); err != nil {
		return err
	}
	if d.Runtime == "kubernetes" {
		if d.Namespace == "" {
			d.Namespace = "cactusdb"
		}
		if !ctrNameRe.MatchString(d.Namespace) {
			return errors.New("the namespace may have lowercase letters, digits and dashes")
		}
		switch d.ServiceType {
		case "":
			d.ServiceType = "LoadBalancer"
		case "LoadBalancer":
		case "NodePort":
			if d.Port < 30000 || d.Port > 32767 {
				return errors.New("a NodePort must be between 30000 and 32767")
			}
		default:
			return errors.New("the service type must be LoadBalancer or NodePort")
		}
		if d.Storage == "" {
			d.Storage = "10Gi"
		}
		if !ctrStorageRe.MatchString(d.Storage) {
			return errors.New("the volume size is a number with Mi, Gi or Ti, e.g. 10Gi")
		}
	}
	return nil
}

func checkCtrPassword(sp containerSpec, pw string) error {
	if pw == "" {
		return errors.New("enter a password")
	}
	if strings.ContainsAny(pw, "\n\r\x00") || len(pw) > 128 {
		return errors.New("the password can't contain line breaks and has at most 128 characters")
	}
	switch sp.PassRule {
	case "mssql":
		classes := 0
		for _, f := range []func(rune) bool{unicode.IsUpper, unicode.IsLower, unicode.IsDigit, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}} {
			if strings.IndexFunc(pw, f) >= 0 {
				classes++
			}
		}
		if len(pw) < 8 || classes < 3 {
			return errors.New("SQL Server needs a password of at least 8 characters with three of: uppercase, lowercase, digits and symbols")
		}
	case "min8":
		if len(pw) < 8 {
			return errors.New("the password needs at least 8 characters")
		}
		if sp.ID == "neo4j" && pw == "neo4j" {
			return errors.New("Neo4j doesn't accept neo4j as the password")
		}
	}
	return nil
}

func exampleDataDir(name string) string {
	if runtime.GOOS == "windows" {
		return `C:\cactusdb-data\` + name
	}
	if home, err := os.UserHomeDir(); err == nil && runtime.GOOS == "darwin" {
		return filepath.Join(home, "cactusdb-data", name)
	}
	return "/srv/cactusdb-data/" + name
}

// connFor is the connection to the deployed database, as the New connection
// dialog wants it (without the password).
func connFor(sp containerSpec, d deployReq, host string, port int) ConnParams {
	db := d.Database
	if sp.dbPath != nil {
		db = sp.dbPath(d)
	}
	return ConnParams{Engine: sp.Engine, Host: host, Port: port, Username: d.Username, Database: db}
}

// ---- deploy jobs ------------------------------------------------------------------------

type ctrJob struct {
	mu      sync.Mutex
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	Runtime string      `json:"runtime"`
	State   string      `json:"state"` // running, ready, failed
	Step    string      `json:"step"`  // what it does now
	Lines   []string    `json:"lines"`
	Error   string      `json:"error,omitempty"`
	Conn    *ConnParams `json:"conn,omitempty"` // when ready
	Started time.Time   `json:"started"`
}

var (
	ctrJobsMu sync.Mutex
	ctrJobs   = map[string]*ctrJob{}
)

func newCtrJob(name, rt string) *ctrJob {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	j := &ctrJob{ID: hex.EncodeToString(b), Name: name, Runtime: rt, State: "running", Started: time.Now()}
	ctrJobsMu.Lock()
	defer ctrJobsMu.Unlock()
	for id, o := range ctrJobs { // forget jobs finished an hour ago
		if o.State != "running" && time.Since(o.Started) > time.Hour {
			delete(ctrJobs, id)
		}
	}
	ctrJobs[j.ID] = j
	return j
}

func (j *ctrJob) log(format string, args ...any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Lines = append(j.Lines, fmt.Sprintf(format, args...))
	if len(j.Lines) > 400 {
		j.Lines = j.Lines[len(j.Lines)-400:]
	}
}

func (j *ctrJob) step(s string) {
	j.mu.Lock()
	j.Step = s
	j.mu.Unlock()
	j.log("» %s", s)
}

func (j *ctrJob) snapshot() ctrJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	return ctrJob{ID: j.ID, Name: j.Name, Runtime: j.Runtime, State: j.State, Step: j.Step,
		Lines: append([]string{}, j.Lines...), Error: j.Error, Conn: j.Conn, Started: j.Started}
}

// stream runs a command, adding what it prints to the job's log.
func (j *ctrJob) stream(cmd *exec.Cmd, stdin []byte) error {
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var last []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			l := strings.TrimRight(sc.Text(), " \r")
			if l == "" {
				continue
			}
			j.log("%s", l)
			last = append(last, l)
			if len(last) > 6 {
				last = last[1:]
			}
		}
	}()
	err := cmd.Run()
	pw.Close()
	<-done
	if err != nil && len(last) > 0 {
		return errors.New(strings.Join(last, "\n"))
	}
	return err
}

// ---- docker -----------------------------------------------------------------------------

const ctrLabel = "com.cactusdb.managed"

// prepareDataDir creates the data folder; a database that doesn't run as
// root must be able to write in it.
func prepareDataDir(j *ctrJob, dir string, uid int) error {
	if fi, err := os.Stat(dir); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("%s is a file, not a folder", dir)
		}
		j.log("data folder %s exists: its files are used", dir)
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("can't create the data folder: %w", err)
	}
	j.log("created data folder %s", dir)
	if uid > 0 && runtime.GOOS == "linux" {
		if err := os.Chown(dir, uid, uid); err != nil {
			// Not root: let the database's user write anyway.
			if err := os.Chmod(dir, 0o777); err != nil {
				return fmt.Errorf("can't let the database write in the data folder: %w", err)
			}
			j.log("made the data folder writable for everyone (the database runs as user %d)", uid)
		}
	}
	return nil
}

func dockerRunArgs(sp containerSpec, d deployReq) []string {
	bind := "127.0.0.1:"
	if d.Public {
		bind = ""
	}
	args := []string{"run", "-d", "--name", d.Name, "--restart", d.Restart,
		"--label", ctrLabel + "=true", "--label", "com.cactusdb.spec=" + sp.ID, "--label", "com.cactusdb.port=" + strconv.Itoa(d.Port),
		"-p", bind + strconv.Itoa(d.Port) + ":" + strconv.Itoa(sp.Port)}
	if d.DataDir != "" {
		args = append(args, "-v", d.DataDir+":"+sp.DataPath)
	} else {
		args = append(args, "-v", d.Name+"-data:"+sp.DataPath)
	}
	for _, e := range sp.env(d) {
		if e.Secret {
			args = append(args, "-e", e.Name) // the value comes from docker's environment
		} else {
			args = append(args, "-e", e.Name+"="+e.Value)
		}
	}
	args = append(args, d.Image)
	return append(args, sp.args...)
}

func secretEnv(sp containerSpec, d deployReq) []string {
	var env []string
	for _, e := range sp.env(d) {
		if e.Secret {
			env = append(env, e.Name+"="+e.Value)
		}
	}
	return env
}

func deployDocker(ctx context.Context, j *ctrJob, rt *ctrRuntime, sp containerSpec, d deployReq) error {
	if d.DataDir != "" {
		j.step("preparing the data folder")
		if err := prepareDataDir(j, d.DataDir, sp.UID); err != nil {
			return err
		}
	} else {
		j.log("the files go in the Docker volume %s-data", d.Name)
	}
	// An image already here is used as it is: no download, and it works offline.
	if _, err := runTool(ctx, toolCmd(ctx, rt.tool, "image", "inspect", "--format", "{{.Id}}", d.Image), nil); err == nil {
		j.log("image %s is already on this computer", d.Image)
	} else {
		j.step("downloading " + d.Image)
		if err := j.stream(toolCmd(ctx, rt.tool, "pull", d.Image), nil); err != nil {
			return fmt.Errorf("downloading %s failed: %w", d.Image, err)
		}
	}
	newVolume := false
	if d.DataDir == "" {
		_, err := runTool(ctx, toolCmd(ctx, rt.tool, "volume", "inspect", d.Name+"-data"), nil)
		newVolume = err != nil
	}
	j.step("starting container " + d.Name)
	cmd := toolCmd(ctx, rt.tool, dockerRunArgs(sp, d)...)
	cmd.Env = append(cmd.Env, secretEnv(sp, d)...)
	if err := j.stream(cmd, nil); err != nil {
		// docker run may have created the container before failing: don't leave it.
		if _, rmErr := runTool(ctx, toolCmd(ctx, rt.tool, "rm", "-f", d.Name), nil); rmErr != nil {
			j.log("couldn't remove the half-made container %s: %v", d.Name, rmErr)
		} else {
			j.log("removed the half-made container %s", d.Name)
		}
		if newVolume { // only a volume this attempt made: an older one may hold databases
			_, _ = runTool(ctx, toolCmd(ctx, rt.tool, "volume", "rm", d.Name+"-data"), nil)
		}
		return fmt.Errorf("starting the container failed: %w", err)
	}
	alive := func(ctx context.Context) error {
		st, err := runTool(ctx, toolCmd(ctx, rt.tool, "inspect", "-f", "{{.State.Status}} {{.State.ExitCode}}", d.Name), nil)
		if err != nil {
			return err
		}
		if f := strings.Fields(st); len(f) > 0 && (f[0] == "exited" || f[0] == "dead") {
			cmd := toolCmd(ctx, rt.tool, "logs", "--tail", "15", d.Name)
			var b bytes.Buffer
			cmd.Stdout, cmd.Stderr = &b, &b // databases log to both
			_ = cmd.Run()
			return fmt.Errorf("the container stopped (exit code %s):\n%s", f[len(f)-1], lastLines(b.String(), 15))
		}
		return nil
	}
	return waitForDB(ctx, j, sp, d, "localhost", d.Port, alive)
}

// waitForDB connects until the database answers: its port can be open
// (Docker's proxy) long before the database is ready.
func waitForDB(ctx context.Context, j *ctrJob, sp containerSpec, d deployReq, host string, port int, alive func(context.Context) error) error {
	j.step(fmt.Sprintf("waiting for %s to accept connections on %s", sp.Name, net.JoinHostPort(host, strconv.Itoa(port))))
	p := connFor(sp, d, host, port)
	p.Password = d.Password
	if sp.Engine == EngineCassandra {
		p.Username = ""
	}
	limit := time.Now().Add(15 * time.Minute)
	lastSaid, lastErr := time.Now(), ""
	for {
		if err := alive(ctx); err != nil {
			return err
		}
		tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		db, err := openDB(tctx, p)
		if err == nil {
			err = db.PingContext(tctx)
			if err == nil {
				ver := serverVersion(tctx, p.Engine, db)
				db.Close()
				cancel()
				if ver != "" {
					j.log("connected: %s", ver)
				}
				p.Password = ""
				j.mu.Lock()
				j.Conn = &p
				j.mu.Unlock()
				return nil
			}
			db.Close()
		}
		cancel()
		lastErr = err.Error()
		if time.Since(lastSaid) > 20*time.Second {
			j.log("not ready yet (%s)", firstLine(lastErr))
			lastSaid = time.Now()
		}
		if time.Now().After(limit) {
			return fmt.Errorf("the database didn't accept connections within 15 minutes: %s", lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// ---- kubernetes ---------------------------------------------------------------------------

type obj = map[string]any

// kubeManifest is what kubectl apply gets: a Namespace, a Secret with the
// password, a volume claim (without a data folder), the Deployment and the
// Service. As JSON, which kubectl reads like YAML.
func kubeManifest(sp containerSpec, d deployReq) []byte {
	labels := obj{"app.kubernetes.io/name": sp.ID, "app.kubernetes.io/instance": d.Name, "app.kubernetes.io/managed-by": "cactusdb"}
	meta := func(name string) obj {
		return obj{"name": name, "namespace": d.Namespace, "labels": labels, "annotations": obj{"cactusdb/spec": sp.ID, "cactusdb/port": strconv.Itoa(d.Port)}}
	}
	var items []any
	if d.Namespace != "default" {
		items = append(items, obj{"apiVersion": "v1", "kind": "Namespace", "metadata": obj{"name": d.Namespace}})
	}
	secretName := d.Name + "-credentials"
	var env []any
	secret := obj{}
	for _, e := range sp.env(d) {
		if e.Secret {
			secret[e.Name] = e.Value
			env = append(env, obj{"name": e.Name, "valueFrom": obj{"secretKeyRef": obj{"name": secretName, "key": e.Name}}})
		} else {
			env = append(env, obj{"name": e.Name, "value": e.Value})
		}
	}
	if len(secret) > 0 {
		items = append(items, obj{"apiVersion": "v1", "kind": "Secret", "metadata": meta(secretName), "type": "Opaque", "stringData": secret})
	}
	var volume obj
	if d.DataDir != "" {
		volume = obj{"name": "data", "hostPath": obj{"path": d.DataDir, "type": "DirectoryOrCreate"}}
	} else {
		items = append(items, obj{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": meta(d.Name + "-data"),
			"spec": obj{"accessModes": []any{"ReadWriteOnce"}, "resources": obj{"requests": obj{"storage": d.Storage}}}})
		volume = obj{"name": "data", "persistentVolumeClaim": obj{"claimName": d.Name + "-data"}}
	}
	container := obj{"name": "db", "image": d.Image, "ports": []any{obj{"containerPort": sp.Port, "name": "db"}},
		"volumeMounts":   []any{obj{"name": "data", "mountPath": sp.DataPath}},
		"readinessProbe": obj{"tcpSocket": obj{"port": sp.Port}, "periodSeconds": 5}}
	if env != nil {
		container["env"] = env
	}
	if sp.args != nil {
		container["args"] = sp.args
	}
	if sp.MemoryMB > 0 {
		container["resources"] = obj{"requests": obj{"memory": strconv.Itoa(sp.MemoryMB) + "Mi"}}
	}
	pod := obj{"containers": []any{container}, "volumes": []any{volume}}
	if sp.UID > 0 {
		pod["securityContext"] = obj{"fsGroup": sp.UID}
		if d.DataDir != "" { // fsGroup doesn't change a hostPath folder: chown it first
			pod["initContainers"] = []any{obj{"name": "data-owner", "image": "busybox:1.36",
				"command":         []any{"chown", "-R", fmt.Sprintf("%d:%d", sp.UID, sp.UID), sp.DataPath},
				"securityContext": obj{"runAsUser": 0},
				"volumeMounts":    []any{obj{"name": "data", "mountPath": sp.DataPath}}}}
		}
	}
	items = append(items, obj{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": meta(d.Name),
		"spec": obj{"replicas": 1, "strategy": obj{"type": "Recreate"},
			"selector": obj{"matchLabels": obj{"app.kubernetes.io/instance": d.Name}},
			"template": obj{"metadata": obj{"labels": labels}, "spec": pod}}})
	port := obj{"name": "db", "port": d.Port, "targetPort": sp.Port}
	if d.ServiceType == "NodePort" {
		port["nodePort"] = d.Port
	}
	items = append(items, obj{"apiVersion": "v1", "kind": "Service", "metadata": meta(d.Name),
		"spec": obj{"type": d.ServiceType, "selector": obj{"app.kubernetes.io/instance": d.Name}, "ports": []any{port}}})
	b, _ := json.MarshalIndent(obj{"apiVersion": "v1", "kind": "List", "items": items}, "", "  ")
	return b
}

func deployKubernetes(ctx context.Context, j *ctrJob, rt *ctrRuntime, sp containerSpec, d deployReq) error {
	kc := func(args ...string) *exec.Cmd { return toolCmd(ctx, rt.tool, args...) }
	if out, err := runTool(ctx, kc("get", "deployment", d.Name, "-n", d.Namespace, "--ignore-not-found", "-o", "name"), nil); err == nil && strings.TrimSpace(out) != "" {
		return fmt.Errorf("namespace %s already has a deployment called %s", d.Namespace, d.Name)
	}
	j.step(fmt.Sprintf("creating %s in namespace %s (context %s)", d.Name, d.Namespace, rt.Context))
	if d.DataDir != "" {
		j.log("the files go in %s on the node the pod runs on", d.DataDir)
	} else {
		j.log("the files go in a %s volume claim, %s-data", d.Storage, d.Name)
	}
	if err := j.stream(kc("apply", "-f", "-"), kubeManifest(sp, d)); err != nil {
		return fmt.Errorf("kubectl apply failed: %w", err)
	}
	j.step("waiting for the pod: downloading " + d.Image + " and starting it")
	if err := j.stream(kc("rollout", "status", "deployment/"+d.Name, "-n", d.Namespace, "--timeout=15m"), nil); err != nil {
		events, _ := runTool(ctx, kc("get", "events", "-n", d.Namespace, "--field-selector", "type=Warning", "--sort-by=.lastTimestamp"), nil)
		return fmt.Errorf("the pod didn't start: %w\n%s", err, lastLines(events, 6))
	}
	j.step("finding the service's address")
	host, port, err := kubeAddress(ctx, kc, d)
	if err != nil {
		return err
	}
	j.log("address: %s", net.JoinHostPort(host, strconv.Itoa(port)))
	return waitForDB(ctx, j, sp, d, host, port, func(context.Context) error { return nil })
}

// kubeAddress is where cactusdb reaches the service: the load balancer's
// address, or a node's for a NodePort.
func kubeAddress(ctx context.Context, kc func(...string) *exec.Cmd, d deployReq) (string, int, error) {
	if d.ServiceType == "LoadBalancer" {
		for i := 0; i < 40; i++ {
			out, err := runTool(ctx, kc("get", "service", d.Name, "-n", d.Namespace, "-o", "json"), nil)
			if err != nil {
				return "", 0, err
			}
			var svc struct {
				Status struct {
					LoadBalancer struct {
						Ingress []struct{ IP, Hostname string }
					}
				}
			}
			_ = json.Unmarshal([]byte(out), &svc)
			for _, in := range svc.Status.LoadBalancer.Ingress {
				if in.Hostname != "" {
					return in.Hostname, d.Port, nil
				}
				if in.IP != "" {
					return in.IP, d.Port, nil
				}
			}
			select {
			case <-ctx.Done():
				return "", 0, ctx.Err()
			case <-time.After(3 * time.Second):
			}
		}
		return "", 0, errors.New("the load balancer got no address in 2 minutes: the cluster may have no load balancer; deploy with NodePort instead")
	}
	out, err := runTool(ctx, kc("get", "nodes", "-o", "json"), nil)
	if err != nil {
		return "", 0, err
	}
	var nodes struct {
		Items []struct {
			Status struct {
				Addresses []struct{ Type, Address string }
			}
		}
	}
	_ = json.Unmarshal([]byte(out), &nodes)
	for _, n := range nodes.Items {
		for _, a := range n.Status.Addresses {
			if a.Type == "InternalIP" {
				return a.Address, d.Port, nil
			}
		}
	}
	return "", 0, errors.New("no node address found")
}

// ---- deployed containers ---------------------------------------------------------------

type deployedCtr struct {
	Runtime   string `json:"runtime"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Spec      string `json:"spec"`
	Engine    string `json:"engine,omitempty"`
	Image     string `json:"image"`
	State     string `json:"state"`
	Status    string `json:"status"`
	Port      int    `json:"port,omitempty"`
}

func listDocker(ctx context.Context, rt *ctrRuntime) ([]deployedCtr, error) {
	out, err := runTool(ctx, toolCmd(ctx, rt.tool, "ps", "-a", "--filter", "label="+ctrLabel+"=true", "--format", "{{json .}}"), nil)
	if err != nil {
		return nil, err
	}
	list := []deployedCtr{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		var c struct{ Names, Image, State, Status, Labels string }
		if json.Unmarshal([]byte(l), &c) != nil {
			continue
		}
		d := deployedCtr{Runtime: "docker", Name: c.Names, Image: c.Image, State: c.State, Status: c.Status}
		for _, kv := range strings.Split(c.Labels, ",") {
			k, v, _ := strings.Cut(kv, "=")
			switch k {
			case "com.cactusdb.spec":
				d.Spec = v
			case "com.cactusdb.port":
				d.Port, _ = strconv.Atoi(v)
			}
		}
		if sp, ok := specByID(d.Spec); ok {
			d.Engine = sp.Engine
		}
		list = append(list, d)
	}
	return list, nil
}

func listKubernetes(ctx context.Context, rt *ctrRuntime) ([]deployedCtr, error) {
	out, err := runTool(ctx, toolCmd(ctx, rt.tool, "get", "deployments", "-A", "-l", "app.kubernetes.io/managed-by=cactusdb", "-o", "json", "--request-timeout=8s"), nil)
	if err != nil {
		return nil, err
	}
	var v struct {
		Items []struct {
			Metadata struct {
				Name, Namespace string
				Annotations     map[string]string
			}
			Spec struct {
				Replicas int
				Template struct {
					Spec struct{ Containers []struct{ Image string } }
				}
			}
			Status struct{ ReadyReplicas int }
		}
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return nil, err
	}
	list := []deployedCtr{}
	for _, it := range v.Items {
		d := deployedCtr{Runtime: "kubernetes", Name: it.Metadata.Name, Namespace: it.Metadata.Namespace, Spec: it.Metadata.Annotations["cactusdb/spec"]}
		d.Port, _ = strconv.Atoi(it.Metadata.Annotations["cactusdb/port"])
		if c := it.Spec.Template.Spec.Containers; len(c) > 0 {
			d.Image = c[0].Image
		}
		d.State, d.Status = "running", fmt.Sprintf("%d of %d ready", it.Status.ReadyReplicas, it.Spec.Replicas)
		if it.Status.ReadyReplicas < it.Spec.Replicas {
			d.State = "starting"
		}
		if it.Spec.Replicas == 0 {
			d.State = "stopped"
		}
		if sp, ok := specByID(d.Spec); ok {
			d.Engine = sp.Engine
		}
		list = append(list, d)
	}
	return list, nil
}

// portFree tells whether a port can be published: free on 127.0.0.1 and, for
// a container reachable from other computers, on every interface. (On macOS
// binding every interface succeeds while 127.0.0.1 is taken.)
func portFree(port int, public bool) bool {
	hosts := []string{"127.0.0.1"}
	if public {
		hosts = append(hosts, "")
	}
	// Something answering on it: Docker Desktop's port forwarder can share
	// the address with a listener cactusdb opens (SO_REUSEADDR).
	if c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 500*time.Millisecond); err == nil {
		c.Close()
		return false
	}
	for _, h := range hosts {
		l, err := net.Listen("tcp", net.JoinHostPort(h, strconv.Itoa(port)))
		if err != nil {
			return false
		}
		l.Close()
	}
	return true
}

// linuxDistro is the Linux distribution cactusdb runs on, from /etc/os-release
// (id, its family and its name), for the install instructions of Docker and
// Kubernetes; nil on other systems.
func linuxDistro() map[string]string {
	if runtime.GOOS != "linux" {
		return nil
	}
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return nil
	}
	d := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "ID":
			d["id"] = v
		case "ID_LIKE":
			d["like"] = v
		case "PRETTY_NAME":
			d["name"] = v
		}
	}
	return d
}

// ---- web ----------------------------------------------------------------------------------

func (s *server) handleContainers(w http.ResponseWriter, r *http.Request) {
	docker, kube := detectRuntimes(r.Context())
	out := map[string]any{"runtimes": []*ctrRuntime{docker, kube}, "catalog": containerCatalog,
		"superuserSet": s.store.SuperuserSet(), "exampleDir": exampleDataDir("NAME"), "os": runtime.GOOS,
		"arch": runtime.GOARCH, "distro": linuxDistro()}
	deployed := []deployedCtr{}
	var errs []string
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	if docker.OK {
		l, err := listDocker(ctx, docker)
		if err != nil {
			errs = append(errs, "Docker: "+err.Error())
		}
		deployed = append(deployed, l...)
	}
	if kube.OK {
		l, err := listKubernetes(ctx, kube)
		if err != nil {
			errs = append(errs, "Kubernetes: "+err.Error())
		}
		deployed = append(deployed, l...)
	}
	out["deployed"] = deployed
	out["listErrors"] = errs
	writeJSON(w, 200, out)
}

// superuserOK checks the superuser password: only the superuser deploys.
func (s *server) superuserOK(w http.ResponseWriter, r *http.Request, pw, what string) bool {
	if wait := s.throttle.wait(superThrottleID); wait > 0 {
		writeErr(w, 429, fmt.Errorf("too many wrong passwords: try again in %d seconds", int(wait.Seconds())+1))
		return false
	}
	switch err := s.store.CheckSuperuser(pw); {
	case errors.Is(err, errNoSuperuser):
		writeErr(w, 409, errors.New("only the superuser can "+what+", and no superuser password is set: set one on the cactusdb server with: cactusdb --superuser set"))
		return false
	case errors.Is(err, errLockWrong):
		s.throttle.failed(superThrottleID)
		logAction(r, "%s FAILED: wrong superuser password", what)
		writeJSON(w, 403, map[string]any{"error": "wrong superuser password", "locked": true})
		return false
	case err != nil:
		writeErr(w, 500, err)
		return false
	}
	s.throttle.ok(superThrottleID)
	return true
}

func (s *server) handleContainerDeploy(w http.ResponseWriter, r *http.Request) {
	var d deployReq
	if err := readJSON(r, &d); err != nil {
		writeErr(w, 400, err)
		return
	}
	sp, ok := specByID(d.Spec)
	if !ok {
		writeErr(w, 400, fmt.Errorf("unknown database %q", d.Spec))
		return
	}
	if d.Runtime != "docker" && d.Runtime != "kubernetes" {
		writeErr(w, 400, errors.New("deploy to docker or kubernetes"))
		return
	}
	if err := d.check(sp); err != nil {
		writeErr(w, 400, err)
		return
	}
	if !s.superuserOK(w, r, d.SuperPassword, "deploy a database container") {
		return
	}
	var rt *ctrRuntime
	if d.Runtime == "docker" {
		rt = detectDocker(r.Context())
	} else {
		rt = detectKubernetes(r.Context())
	}
	if !rt.OK {
		writeErr(w, 409, fmt.Errorf("%s: %s", rt.Name, rt.Error))
		return
	}
	if d.Runtime == "docker" {
		if out, _ := runTool(r.Context(), toolCmd(r.Context(), rt.tool, "ps", "-a", "-q", "--filter", "name=^/"+d.Name+"$"), nil); strings.TrimSpace(out) != "" {
			writeErr(w, 409, fmt.Errorf("there is already a container called %s", d.Name))
			return
		}
		if !portFree(d.Port, d.Public) {
			writeErr(w, 409, fmt.Errorf("port %d is in use on this computer: choose another", d.Port))
			return
		}
	}
	j := newCtrJob(d.Name, rt.Name)
	where := rt.Name
	if rt.Context != "" {
		where += " (context " + rt.Context + ", namespace " + d.Namespace + ")"
	}
	data := "a volume"
	if d.DataDir != "" {
		data = d.DataDir
	}
	logAction(r, "deploying %s as %s on %s, port %d, data in %s", sp.Name, d.Name, where, d.Port, data)
	s.notify(r, "containers", 0, "started deploying %s as the container %s on %s", sp.Name, d.Name, rt.Name)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
		defer cancel()
		var err error
		if d.Runtime == "docker" {
			err = deployDocker(ctx, j, rt, sp, d)
		} else {
			err = deployKubernetes(ctx, j, rt, sp, d)
		}
		j.mu.Lock()
		if err != nil {
			j.State, j.Error = "failed", err.Error()
		} else {
			j.State, j.Step = "ready", sp.Name+" is ready"
		}
		j.mu.Unlock()
		if err != nil {
			j.log("✘ %s", err)
			logAction(r, "deploying %s FAILED: %s", d.Name, firstLine(err.Error()))
			s.notify(r, "containers", 0, "couldn't deploy the container %s: %s", d.Name, firstLine(err.Error()))
		} else {
			j.log("✔ %s is ready", sp.Name)
			logAction(r, "deployed %s (%s) on %s", d.Name, sp.Name, where)
			s.notify(r, "containers", 0, "deployed the container %s (%s) on %s: it is ready", d.Name, sp.Name, rt.Name)
		}
	}()
	writeJSON(w, 200, map[string]string{"job": j.ID})
}

func (s *server) handleContainerJob(w http.ResponseWriter, r *http.Request) {
	ctrJobsMu.Lock()
	j := ctrJobs[r.PathValue("job")]
	ctrJobsMu.Unlock()
	if j == nil {
		writeErr(w, 404, errors.New("no such deployment"))
		return
	}
	writeJSON(w, 200, j.snapshot())
}

type ctrTarget struct {
	Runtime       string `json:"runtime"`
	Name          string `json:"name"`
	Namespace     string `json:"namespace"`
	SuperPassword string `json:"superPassword"`
	Lines         int    `json:"lines"`
	Action        string `json:"action"`  // start, stop, restart, delete, purge
	Confirm       string `json:"confirm"` // purge: the container's name, typed again
	All           bool   `json:"all"`     // logs: every line, not the last ones
	Key           string `json:"key"`     // reveal: the environment variable
}

// target finds a container cactusdb deployed: it never touches others. Only
// looking at it (super false) works without the superuser password.
func (s *server) ctrTarget(w http.ResponseWriter, r *http.Request, what string, super bool) (ctrTarget, *ctrRuntime, deployedCtr, bool) {
	var t ctrTarget
	var none deployedCtr
	if err := readJSON(r, &t); err != nil {
		writeErr(w, 400, err)
		return t, nil, none, false
	}
	if !ctrNameRe.MatchString(t.Name) || (t.Runtime == "kubernetes" && !ctrNameRe.MatchString(t.Namespace)) {
		writeErr(w, 400, errors.New("not a container cactusdb deployed"))
		return t, nil, none, false
	}
	if super && !s.superuserOK(w, r, t.SuperPassword, what) {
		return t, nil, none, false
	}
	var rt *ctrRuntime
	var list []deployedCtr
	var err error
	switch t.Runtime {
	case "docker":
		if rt = detectDocker(r.Context()); rt.OK {
			list, err = listDocker(r.Context(), rt)
		}
	case "kubernetes":
		if rt = detectKubernetes(r.Context()); rt.OK {
			list, err = listKubernetes(r.Context(), rt)
		}
	default:
		writeErr(w, 400, errors.New("unknown runtime"))
		return t, nil, none, false
	}
	if !rt.OK {
		writeErr(w, 409, fmt.Errorf("%s: %s", rt.Name, rt.Error))
		return t, nil, none, false
	}
	if err != nil {
		writeErr(w, 502, err)
		return t, nil, none, false
	}
	for _, c := range list {
		if c.Name == t.Name && c.Namespace == t.Namespace {
			return t, rt, c, true
		}
	}
	writeErr(w, 404, fmt.Errorf("%s isn't a container cactusdb deployed", t.Name))
	return t, nil, none, false
}

func (s *server) handleContainerLogs(w http.ResponseWriter, r *http.Request) {
	t, rt, _, ok := s.ctrTarget(w, r, "read container logs", true)
	if !ok {
		return
	}
	n := t.Lines
	if n <= 0 || n > 5000 {
		n = 300
	}
	if t.All {
		n = 0
	}
	logs, err := ctrLogs(r.Context(), rt, t, n)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	if t.All {
		logAction(r, "download the log of container %s", t.Name)
	}
	writeJSON(w, 200, map[string]string{"logs": logs})
}

// ctrLogs is the container's log: its last n lines, or all of it (n 0).
func ctrLogs(ctx context.Context, rt *ctrRuntime, t ctrTarget, n int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if t.Runtime == "docker" {
		tail := "all"
		if n > 0 {
			tail = strconv.Itoa(n)
		}
		cmd = toolCmd(ctx, rt.tool, "logs", "--tail", tail, t.Name)
	} else {
		tail := "-1"
		if n > 0 {
			tail = strconv.Itoa(n)
		}
		cmd = toolCmd(ctx, rt.tool, "logs", "deployment/"+t.Name, "-n", t.Namespace, "--tail", tail, "--all-containers")
	}
	var b bytes.Buffer
	cmd.Stdout, cmd.Stderr = &b, &b // databases log to both
	if err := cmd.Run(); err != nil && b.Len() == 0 {
		return "", err
	}
	return b.String(), nil
}

// handleContainerLogExport writes the container's whole log to
// <export>/<container>/log/<container>_<date>-<time>.log, listed on the
// Exported files page.
func (s *server) handleContainerLogExport(w http.ResponseWriter, r *http.Request) {
	t, rt, _, ok := s.ctrTarget(w, r, "export container logs", true)
	if !ok {
		return
	}
	logs, err := ctrLogs(r.Context(), rt, t, 0)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	path, _, err := writeExportFile(filepath.Join(s.exportFolder(), connFolderName(t.Name)), t.Name, "log",
		func(w io.Writer) (int, error) { n, err := io.WriteString(w, logs); return n, err })
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	logAction(r, "export the log of container %s to %s", t.Name, path)
	writeJSON(w, 200, map[string]any{"path": path, "name": filepath.Base(path), "size": len(logs), "download": downloadURL(path)})
}

// handleContainerRemove removes the container (docker) or the deployment,
// service and secret (kubernetes). The data folder, volume or volume claim
// stays: deploying again with the same folder finds the databases.
func (s *server) handleContainerRemove(w http.ResponseWriter, r *http.Request) {
	t, rt, _, ok := s.ctrTarget(w, r, "remove a database container", true)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	err := deleteCtr(ctx, rt, t)
	if err != nil {
		logAction(r, "removing container %s FAILED: %v", t.Name, err)
		writeErr(w, 502, err)
		return
	}
	logAction(r, "removed container %s (%s); its data was kept", t.Name, rt.Name)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
