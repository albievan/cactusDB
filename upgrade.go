package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Installing a newer cactusdb from the web UI (☰ → Check for update →
// Install). The server downloads the binary for its own system from the GitHub
// release of the new version, checks it against that release's SHA256SUMS and by running it with
// --version, puts it in place of the running program (which is kept as
// <program>.old), stops like Shutdown does, and starts the new version with
// the options the running one was started with.
//
// Those options are saved in the connection database (table startup_options,
// one row per port) every time the web UI starts, and the restart reads them
// back from there. On Linux and macOS the new version replaces the running
// process (same process id, terminal, log file and service manager); on
// Windows it is started as a new process.
//
// A server that can't reach GitHub can be sent the binary from the browser
// instead; that needs the superuser password, as it runs a program the server
// didn't download itself.

// startedArgsEnv carries the options as typed from the cactusdb started with
// --runas background to the copy it starts (which runs without --runas).
const startedArgsEnv = "CACTUSDB_STARTED_ARGS"

const startupSchema = `CREATE TABLE IF NOT EXISTS startup_options (
	port        INTEGER PRIMARY KEY, -- the web UI's port: one row per cactusdb
	args        TEXT NOT NULL,       -- JSON array of the options, as typed
	command     TEXT NOT NULL,       -- the same as one command line
	executable  TEXT NOT NULL,
	working_dir TEXT NOT NULL,
	run_as      TEXT NOT NULL,       -- foreground or background
	env         TEXT NOT NULL DEFAULT '', -- names (never values) of the CACTUSDB_ variables set
	version     TEXT NOT NULL,
	pid         INTEGER NOT NULL,
	started_at  TEXT NOT NULL,
	started_by  TEXT NOT NULL DEFAULT '',
	hostname    TEXT NOT NULL DEFAULT ''
)`

// startupOptions is how this cactusdb was started.
type startupOptions struct {
	Port       int       `json:"port"`
	Args       []string  `json:"args"`
	Command    string    `json:"command"`
	Executable string    `json:"executable"`
	WorkingDir string    `json:"workingDir"`
	RunAs      string    `json:"runAs"`
	Env        string    `json:"env"`
	Version    string    `json:"version"`
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"startedAt"`
	StartedBy  string    `json:"startedBy"`
	Hostname   string    `json:"hostname"`
}

// currentStartup describes this process. args are the options as typed:
// a background copy gets them from the cactusdb that started it.
func currentStartup(port int) startupOptions {
	args := os.Args[1:]
	runAs := "foreground"
	if os.Getenv(backgroundEnv) != "" {
		runAs = "background"
		var typed []string
		if err := json.Unmarshal([]byte(os.Getenv(startedArgsEnv)), &typed); err == nil {
			args = typed
		} else {
			args = append(append([]string{}, args...), "--runas", "background")
		}
	}
	exe, _ := os.Executable()
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	wd, _ := os.Getwd()
	host, _ := os.Hostname()
	who := ""
	if u, err := user.Current(); err == nil {
		who = u.Username
	}
	var env []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(strings.ToUpper(k), "CACTUSDB_") &&
			k != backgroundEnv && k != startedArgsEnv {
			env = append(env, k)
		}
	}
	return startupOptions{Port: port, Args: args, Command: commandLine("cactusdb", args), Executable: exe, WorkingDir: wd,
		RunAs: runAs, Env: strings.Join(env, " "), Version: version, PID: os.Getpid(), StartedAt: processStart,
		StartedBy: who, Hostname: host}
}

// saveStartup records how this cactusdb was started, for the restart after an
// upgrade (and for anyone looking in the connection database).
func saveStartup(st *Store, o startupOptions) error {
	db := st.DB()
	if _, err := db.Exec(startupSchema); err != nil {
		return err
	}
	args, _ := json.Marshal(o.Args)
	_, err := db.Exec(`INSERT OR REPLACE INTO startup_options
		(port, args, command, executable, working_dir, run_as, env, version, pid, started_at, started_by, hostname)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.Port, string(args), o.Command, o.Executable, o.WorkingDir, o.RunAs, o.Env, o.Version, o.PID,
		o.StartedAt.UTC().Format(time.RFC3339), o.StartedBy, o.Hostname)
	return err
}

// loadStartup reads the saved options of the cactusdb on port.
func loadStartup(st *Store, port int) (startupOptions, error) {
	var o startupOptions
	var args, started string
	err := st.DB().QueryRow(`SELECT port, args, command, executable, working_dir, run_as, env, version, pid, started_at, started_by, hostname
		FROM startup_options WHERE port = ?`, port).Scan(&o.Port, &args, &o.Command, &o.Executable, &o.WorkingDir, &o.RunAs,
		&o.Env, &o.Version, &o.PID, &started, &o.StartedBy, &o.Hostname)
	if errors.Is(err, sql.ErrNoRows) {
		return o, fmt.Errorf("no startup options are saved for port %d", port)
	} else if err != nil {
		return o, err
	}
	if err := json.Unmarshal([]byte(args), &o.Args); err != nil {
		return o, fmt.Errorf("the saved startup options can't be read: %w", err)
	}
	o.StartedAt, _ = time.Parse(time.RFC3339, started)
	return o, nil
}

// commandLine shows args as one command, quoting what needs it.
func commandLine(name string, args []string) string {
	parts := []string{name}
	for _, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\"'$`\\;&|<>*?()") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// restartArgs are the options to start the new version with: the saved ones,
// without --runas when this is already the background copy.
func restartArgs(o startupOptions, inBackground bool) []string {
	if inBackground {
		return withoutFlag(o.Args, "runas")
	}
	return o.Args
}

// ---- download and install ---------------------------------------------------

const (
	maxBinarySize   = 256 << 20
	downloadTimeout = 10 * time.Minute
)

// binaryName is the file of a release for this system.
func binaryName() string {
	n := "cactusdb-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		n += ".exe"
	}
	return n
}

// upgradeJob is the one upgrade running, shown in the web UI.
type upgradeJob struct {
	mu sync.Mutex
	upgradeStatus
}

type upgradeStatus struct {
	State   string `json:"state"` // downloading (receiving), checking, installing, restarting, failed
	From    string `json:"from"`
	To      string `json:"to"`
	Done    int64  `json:"done"`
	Total   int64  `json:"total"`
	Error   string `json:"error,omitempty"`
	Command string `json:"command"`
	Backup  string `json:"backup,omitempty"`
}

func (j *upgradeJob) set(f func(j *upgradeJob)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	f(j)
}

func (j *upgradeJob) snapshot() upgradeStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.upgradeStatus
}

// restartPlan is set once the new version is in place: main restarts with it
// after the server has stopped.
type restartPlan struct {
	exe, backup string
	args        []string
	dir         string
	to          string
}

// executable is the running program (a variable so tests can give another).
var executable = os.Executable

// installable says whether this cactusdb can replace itself, and if not why.
func installable() (exe string, err error) {
	if os.Getenv("CACTUSDB_CONTAINER") != "" {
		return "", errors.New("this cactusdb runs in a container: a new version only lasts until the container restarts. Update the image instead (deploy.sh or deploy-container.sh)")
	}
	exe, err = executable()
	if err != nil {
		return "", fmt.Errorf("cannot find the running program: %w", err)
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	dir := filepath.Dir(exe)
	f, err := os.CreateTemp(dir, ".cactusdb-write-test-*")
	if err != nil {
		return "", fmt.Errorf("cactusdb can't write to %s, the folder it runs from (%v): install it with the command for this server below, as a user that may", dir, err)
	}
	f.Close()
	os.Remove(f.Name())
	return exe, nil
}

// createFresh creates path as a new executable file. What is there (an old
// download, or a link someone put there) is removed, never written through.
func createFresh(path string) (*os.File, error) {
	os.Remove(path)
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
}

// downloadBinary fetches name from the release folder dir into dst,
// reporting progress.
func downloadBinary(ctx context.Context, dir, name, dst string, progress func(done, total int64)) error {
	req, err := http.NewRequestWithContext(ctx, "GET", dir+name, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "cactusdb/"+version)
	resp, err := (&http.Client{Timeout: downloadTimeout}).Do(req)
	if err != nil {
		return fmt.Errorf("cannot download %s: %w", dir+name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GitHub answered %s for %s", resp.Status, dir+name)
	}
	progress(0, resp.ContentLength)
	f, err := createFresh(dst)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, &progressReader{r: io.LimitReader(resp.Body, maxBinarySize+1), total: resp.ContentLength, report: progress})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("the download of %s stopped: %w", name, err)
	}
	if n > maxBinarySize {
		return fmt.Errorf("%s is larger than %d MB: not a cactusdb", name, maxBinarySize>>20)
	}
	if resp.ContentLength > 0 && n != resp.ContentLength {
		return fmt.Errorf("the download of %s is incomplete (%d of %d bytes)", name, n, resp.ContentLength)
	}
	return nil
}

type progressReader struct {
	r      io.Reader
	done   int64
	total  int64
	report func(done, total int64)
	last   time.Time
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if time.Since(p.last) > 200*time.Millisecond || err != nil {
		p.report(p.done, p.total)
		p.last = time.Now()
	}
	return n, err
}

// expectedSum looks name up in the SHA256SUMS of the release folder dir,
// after checking the file's signature (SHA256SUMS.sig) with the release key
// built into cactusdb.
func expectedSum(ctx context.Context, dir, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	sums, err := fetchDist(ctx, dir, "SHA256SUMS")
	if err != nil {
		return "", err
	}
	if releaseKey != "" {
		sig, err := fetchDist(ctx, dir, "SHA256SUMS.sig")
		if err != nil {
			return "", fmt.Errorf("the update isn't signed: %w", err)
		}
		if err := verifyRelease(sums, sig); err != nil {
			return "", err
		}
	}
	return sumFor(string(sums), name)
}

// fetchDist downloads a small file of the release folder dir on GitHub. A
// release always has SHA256SUMS (and its signature): a missing one is an error.
func fetchDist(ctx context.Context, dir, name string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", dir+name, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot download %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub answered %s for %s", resp.Status, dir+name)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<10))
}

// sumFor finds name in the text of a SHA256SUMS file.
func sumFor(sums, name string) (string, error) {
	for _, l := range strings.Split(sums, "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("SHA256SUMS has no checksum for %s", name)
}

func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// checkBinary runs the new file with --version: it must start on this system
// and be a cactusdb newer than this one. It returns its version.
func checkBinary(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("the new file doesn't run on this server (%s/%s): %v", runtime.GOOS, runtime.GOARCH, err)
	}
	v, ok := strings.CutPrefix(strings.TrimSpace(string(out)), "cactusdb ")
	if !ok || !versionRe.MatchString(v) {
		return "", fmt.Errorf("the new file is not a cactusdb: it says %q", clip(strings.TrimSpace(string(out)), 80))
	}
	if !newerVersion(v, version) {
		return "", fmt.Errorf("the new file is cactusdb %s, not newer than this %s", v, version)
	}
	return v, nil
}

// putInPlace moves the running program aside to <exe>.old and the new file in
// its place (both in the same folder, so these are renames). Windows allows
// renaming, not deleting, a running program.
func putInPlace(newFile, exe string) (backup string, err error) {
	backup = oldName(exe)
	if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		backup = oldName(exe) + "-" + strconv.FormatInt(time.Now().Unix(), 10) // the old .old is still running (Windows)
	}
	if err := os.Rename(exe, backup); err != nil {
		return "", fmt.Errorf("cannot move %s aside: %w", exe, err)
	}
	if err := os.Rename(newFile, exe); err != nil {
		os.Rename(backup, exe)
		return "", fmt.Errorf("cannot put the new version in place of %s: %w", exe, err)
	}
	return backup, nil
}

// oldName is where the previous version is kept: cactusdb.old, or
// cactusdb.old.exe on Windows.
func oldName(exe string) string {
	if ext := filepath.Ext(exe); strings.EqualFold(ext, ".exe") {
		return strings.TrimSuffix(exe, ext) + ".old" + ext
	}
	return exe + ".old"
}

// ---- web --------------------------------------------------------------------

// upgradeInfo is added to /api/update: whether Install can work here, and the
// command the new version will be started with.
func (s *server) upgradeInfo() map[string]any {
	out := map[string]any{"binary": binaryName(), "inBackground": s.background}
	if o, err := loadStartup(s.store, s.port); err == nil {
		out["startCommand"] = o.Command
		out["startArgs"] = o.Args
		out["runAs"] = o.RunAs
		out["workingDir"] = o.WorkingDir
	}
	if exe, err := installable(); err != nil {
		out["installError"] = err.Error()
	} else {
		out["executable"] = exe
		out["backup"] = oldName(exe)
		out["canInstall"] = true
	}
	out["superuserSet"] = s.store.SuperuserSet()
	if j := s.currentUpgrade(); j != nil {
		out["job"] = j.snapshot()
	}
	return out
}

func (s *server) currentUpgrade() *upgradeJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.upgrade
}

// startUpgrade claims the one upgrade slot.
func (s *server) startUpgrade(from, to string) (*upgradeJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j := s.upgrade; j != nil {
		if st := j.snapshot().State; st != "failed" {
			return nil, fmt.Errorf("an upgrade is already %s", st)
		}
	}
	j := &upgradeJob{upgradeStatus: upgradeStatus{State: "downloading", From: from, To: to}}
	s.upgrade = j
	return j, nil
}

// handleUpgrade downloads and installs the latest version, then restarts.
// Like /api/shutdown it only accepts JSON, so other websites can't start it.
func (s *server) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeErr(w, 415, errors.New("expected a JSON request"))
		return
	}
	var req struct {
		SuperPassword string `json:"superPassword"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	exe, err := installable()
	if err != nil {
		writeErr(w, 409, err)
		return
	}
	// Installing restarts cactusdb for everyone: with a superuser password
	// set, it takes that password, as installing an uploaded file does.
	if s.store.SuperuserSet() {
		if wait := s.throttle.wait(superThrottleID); wait > 0 {
			writeErr(w, 429, fmt.Errorf("too many wrong passwords: try again in %d seconds", int(wait.Seconds())+1))
			return
		}
		switch err := s.store.CheckSuperuser(req.SuperPassword); {
		case errors.Is(err, errLockWrong):
			s.throttle.failed(superThrottleID)
			logAction(r, "upgrade REFUSED: wrong superuser password")
			s.event(r, "signin-failed", true, "upgrade REFUSED: wrong superuser password")
			writeJSON(w, 403, map[string]any{"error": "the superuser password is needed to install an update", "needSuper": true})
			return
		case err != nil:
			writeErr(w, 500, err)
			return
		}
		s.throttle.ok(superThrottleID)
	}
	opts, err := loadStartup(s.store, s.port)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	latest, err := latestVersion(r.Context(), true)
	if err != nil {
		writeJSON(w, 502, map[string]any{"error": "the cactusdb server can't reach GitHub: " + err.Error(), "offline": true})
		return
	}
	if !newerVersion(latest, version) {
		writeErr(w, 409, fmt.Errorf("cactusdb %s is the latest version", version))
		return
	}
	j, err := s.startUpgrade(version, latest)
	if err != nil {
		writeErr(w, 409, err)
		return
	}
	j.set(func(j *upgradeJob) { j.Command = opts.Command })
	logAction(r, "upgrade to cactusdb %s started: downloading %s", latest, releaseDir(latest)+binaryName())
	s.event(r, "server", false, "upgrade to cactusdb %s started: downloading %s", latest, releaseDir(latest)+binaryName())
	s.notify(r, "upgrade", 0, "started updating cactusdb from %s to %s: it restarts once installed, and this page reloads", version, latest)
	go s.runUpgrade(j, exe, opts, latest, nil)
	writeJSON(w, 200, j.snapshot())
}

// handleUpgradeUpload installs a binary the browser downloaded, for a server
// without internet access. It needs the superuser password (in a header, so
// the body can be the file): the file is run on the server.
func (s *server) handleUpgradeUpload(w http.ResponseWriter, r *http.Request) {
	// A refusal reads the file first: the browser shows a connection closed
	// mid-upload as "Failed to fetch" instead of the reason.
	refuse := func(code int, err error) {
		io.Copy(io.Discard, io.LimitReader(r.Body, maxBinarySize))
		writeErr(w, code, err)
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/octet-stream" {
		refuse(415, errors.New("expected the cactusdb binary as application/octet-stream"))
		return
	}
	exe, err := installable()
	if err != nil {
		refuse(409, err)
		return
	}
	if !s.store.SuperuserSet() {
		refuse(409, errors.New("installing a file sent from the browser needs the superuser password, and none is set: set one with cactusdb --superuser set, or install with the command for this server below"))
		return
	}
	if wait := s.throttle.wait(superThrottleID); wait > 0 {
		refuse(429, fmt.Errorf("too many wrong passwords: try again in %d seconds", int(wait.Seconds())+1))
		return
	}
	switch err := s.store.CheckSuperuser(r.Header.Get("X-Cactusdb-Superuser")); {
	case errors.Is(err, errLockWrong):
		s.throttle.failed(superThrottleID)
		logAction(r, "upgrade from an uploaded file REFUSED: wrong superuser password")
		s.event(r, "signin-failed", true, "upgrade from an uploaded file REFUSED: wrong superuser password")
		refuse(403, errors.New("wrong superuser password"))
		return
	case err != nil:
		refuse(500, err)
		return
	}
	s.throttle.ok(superThrottleID)
	opts, err := loadStartup(s.store, s.port)
	if err != nil {
		refuse(500, err)
		return
	}
	j, err := s.startUpgrade(version, "")
	if err != nil {
		refuse(409, err)
		return
	}
	j.set(func(j *upgradeJob) { j.Command = opts.Command; j.State = "receiving" })
	tmp := exe + ".download"
	fail := func(code int, err error) {
		os.Remove(tmp)
		j.set(func(j *upgradeJob) { j.State, j.Error = "failed", err.Error() })
		writeErr(w, code, err)
	}
	f, err := createFresh(tmp)
	if err != nil {
		fail(500, err)
		return
	}
	n, err := io.Copy(f, io.LimitReader(r.Body, maxBinarySize+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		fail(400, fmt.Errorf("the file didn't arrive: %w", err))
		return
	case n > maxBinarySize:
		fail(400, fmt.Errorf("the file is larger than %d MB: not a cactusdb", maxBinarySize>>20))
		return
	}
	if want := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Cactusdb-Sha256"))); want != "" {
		if got, err := fileSum(tmp); err != nil || got != want {
			fail(400, errors.New("the file is damaged: its SHA-256 isn't the one in SHA256SUMS on GitHub"))
			return
		}
	}
	logAction(r, "upgrade from a file sent by the browser (%d bytes), superuser password given", n)
	s.event(r, "server", false, "upgrade from a file sent by the browser (%d bytes), superuser password given", n)
	s.notify(r, "upgrade", 0, "started updating cactusdb %s from a file: it restarts once installed, and this page reloads", version)
	s.runUpgrade(j, exe, opts, "", func(context.Context, func(int64, int64)) (string, error) { return tmp, nil })
	snap := j.snapshot()
	if snap.State == "failed" {
		writeErr(w, 400, errors.New(snap.Error))
		return
	}
	writeJSON(w, 200, snap)
}

// runUpgrade gets the new file (download, or fetch for an upload), checks it,
// puts it in place and stops the server, which then restarts (see main).
func (s *server) runUpgrade(j *upgradeJob, exe string, opts startupOptions, latest string,
	fetch func(ctx context.Context, progress func(int64, int64)) (string, error)) {
	fail := func(err error) {
		j.set(func(j *upgradeJob) { j.State, j.Error = "failed", err.Error() })
		log.Printf("upgrade FAILED: %v", err)
		s.notify(nil, "upgrade", 0, "the update of cactusdb failed: %s; cactusdb %s keeps running", firstLine(err.Error()), version)
	}
	if fetch == nil {
		fetch = func(ctx context.Context, progress func(int64, int64)) (string, error) {
			tmp := exe + ".download"
			dir := releaseDir(latest)
			want, err := expectedSum(ctx, dir, binaryName())
			if err != nil {
				return "", err
			}
			if err := downloadBinary(ctx, dir, binaryName(), tmp, progress); err != nil {
				os.Remove(tmp)
				return "", err
			}
			if got, err := fileSum(tmp); err != nil || got != want {
				os.Remove(tmp)
				return "", fmt.Errorf("the download of %s is damaged: its SHA-256 isn't the one in SHA256SUMS", binaryName())
			}
			return tmp, nil
		}
	}
	tmp, err := fetch(context.Background(), func(done, total int64) {
		j.set(func(j *upgradeJob) { j.Done, j.Total = done, total })
	})
	if err != nil {
		fail(err)
		return
	}
	j.set(func(j *upgradeJob) { j.State = "checking" })
	got, err := checkBinary(tmp)
	if err != nil {
		os.Remove(tmp)
		fail(err)
		return
	}
	if latest != "" && got != latest && !newerVersion(got, latest) {
		os.Remove(tmp)
		fail(fmt.Errorf("GitHub says the latest version is %s, but the file is %s", latest, got))
		return
	}
	j.set(func(j *upgradeJob) { j.State, j.To = "installing", got })
	backup, err := putInPlace(tmp, exe)
	if err != nil {
		os.Remove(tmp)
		fail(err)
		return
	}
	args := restartArgs(opts, s.background)
	log.Printf("cactusdb %s installed as %s (the previous version is kept as %s); restarting with: %s",
		got, exe, backup, commandLine("cactusdb", args))
	insertUsage(s.store.DB(), []usageEvent{{at: time.Now(), source: "server", function: "upgrade installed",
		detail: fmt.Sprintf("%s → %s, restarting with: %s", version, got, opts.Command), ip: "local", status: 200}})
	s.mu.Lock()
	s.restart = &restartPlan{exe: exe, backup: backup, args: args, dir: opts.WorkingDir, to: got}
	s.mu.Unlock()
	j.set(func(j *upgradeJob) { j.State, j.Backup = "restarting", backup })
	time.AfterFunc(300*time.Millisecond, func() { s.stopOnce.Do(func() { close(s.shutdown) }) }) // let the answer go out first
}

func (s *server) handleUpgradeStatus(w http.ResponseWriter, r *http.Request) {
	j := s.currentUpgrade()
	if j == nil {
		writeJSON(w, 200, map[string]any{"state": ""})
		return
	}
	writeJSON(w, 200, j.snapshot())
}

// restartNow starts the new version once the server has stopped. On Linux
// and macOS it doesn't return when it works. If the new version can't be
// started, the previous one is put back and started instead.
func (p *restartPlan) restartNow(childLog string) {
	if p.dir != "" {
		os.Chdir(p.dir)
	}
	env := os.Environ()
	err := restartProcess(p.exe, p.args, env, childLog)
	log.Printf("cannot start cactusdb %s: %v; putting the previous version back", p.to, err)
	if rerr := os.Rename(p.exe, p.exe+".failed"); rerr == nil {
		if rerr := os.Rename(p.backup, p.exe); rerr == nil {
			err = restartProcess(p.exe, p.args, env, childLog)
		}
	}
	log.Printf("cannot restart cactusdb: %v; start it again by hand", err)
	os.Exit(1)
}
