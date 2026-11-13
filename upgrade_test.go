package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStartupOptions(t *testing.T) {
	s := newTestServer(t)
	o := currentStartup(8123)
	o.Args = []string{"--port", "8123", "--exportto", "/data/my exports", "--runas", "background"}
	o.Command = commandLine("cactusdb", o.Args)
	if err := saveStartup(s.store, o); err != nil {
		t.Fatal(err)
	}
	got, err := loadStartup(s.store, 8123)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Args, "|") != strings.Join(o.Args, "|") || got.Command != `cactusdb --port 8123 --exportto '/data/my exports' --runas background` ||
		got.Version != version || got.PID != os.Getpid() || got.Executable == "" {
		t.Fatalf("%+v", got)
	}
	// The background copy runs without --runas; the restart too.
	if a := strings.Join(restartArgs(got, true), " "); a != "--port 8123 --exportto /data/my exports" {
		t.Errorf("background restart args %q", a)
	}
	if a := restartArgs(got, false); len(a) != 6 {
		t.Errorf("foreground restart args %q", a)
	}
	if _, err := loadStartup(s.store, 9999); err == nil {
		t.Error("options for an unknown port")
	}

	// A background copy saves the options as typed, passed on by its starter.
	t.Setenv(backgroundEnv, "/x/cactusdb.log")
	t.Setenv(startedArgsEnv, `["--runas","background","--port","8123"]`)
	t.Setenv("CACTUSDB_PASSPHRASE", "secret value")
	b := currentStartup(8123)
	if b.RunAs != "background" || strings.Join(b.Args, " ") != "--runas background --port 8123" {
		t.Errorf("%+v", b)
	}
	if !strings.Contains(b.Env, "CACTUSDB_PASSPHRASE") || strings.Contains(b.Env, "secret") || strings.Contains(b.Env, startedArgsEnv) {
		t.Errorf("env %q", b.Env)
	}
}

func TestSumFor(t *testing.T) {
	sums := "abc123  cactusdb-linux-amd64\nDEF456 *cactusdb-windows-amd64.exe\n"
	if v, err := sumFor(sums, "cactusdb-windows-amd64.exe"); err != nil || v != "def456" {
		t.Errorf("%q %v", v, err)
	}
	if _, err := sumFor(sums, "cactusdb-linux-arm64"); err == nil {
		t.Error("missing name found")
	}
	if oldName("/a/cactusdb") != "/a/cactusdb.old" || oldName(`C:\a\cactusdb.exe`) != `C:\a\cactusdb.old.exe` {
		t.Error(oldName("/a/cactusdb"), oldName(`C:\a\cactusdb.exe`))
	}
}

// fakeRelease serves VERSION, dist/<binary> and dist/SHA256SUMS like GitHub,
// with a shell script standing in for the new cactusdb.
func fakeRelease(t *testing.T, newVersion string, badSum bool) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake binary is a shell script")
	}
	bin := []byte("#!/bin/sh\necho cactusdb " + newVersion + "\n")
	sum := sha256.Sum256(bin)
	sumHex := hex.EncodeToString(sum[:])
	if badSum {
		sumHex = strings.Repeat("0", 64)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/VERSION":
			fmt.Fprintln(w, newVersion)
		case "/dist/" + binaryName():
			w.Write(bin)
		case "/dist/SHA256SUMS":
			fmt.Fprintf(w, "%s  %s\n", sumHex, binaryName())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	oldL, oldD := latestURL, distURL
	latestURL, distURL = ts.URL+"/VERSION", ts.URL+"/dist/"
	t.Cleanup(func() { latestURL, distURL = oldL, oldD; lastUpdate = updateCheck{} })
}

// fakeExe is the "running program" an upgrade replaces.
func fakeExe(t *testing.T) string {
	dir, _ := filepath.EvalSymlinks(t.TempDir()) // macOS: /var is /private/var
	exe := filepath.Join(dir, "cactusdb")
	os.WriteFile(exe, []byte("#!/bin/sh\necho cactusdb "+version+"\n"), 0o755)
	old := executable
	executable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { executable = old })
	return exe
}

func upgradeServer(t *testing.T) *server {
	s := newTestServer(t)
	s.port, s.shutdown = 8123, make(chan struct{})
	o := currentStartup(8123)
	o.Args = []string{"--port", "8123", "--runas", "background"}
	o.Command = commandLine("cactusdb", o.Args)
	if err := saveStartup(s.store, o); err != nil {
		t.Fatal(err)
	}
	return s
}

func postUpgrade(s *server) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/upgrade", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleUpgrade(rec, req)
	return rec
}

func waitUpgrade(t *testing.T, s *server) upgradeStatus {
	for i := 0; i < 100; i++ {
		if st := s.currentUpgrade().snapshot(); st.State == "restarting" || st.State == "failed" {
			return st
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the upgrade didn't finish")
	return upgradeStatus{}
}

func TestUpgradeInstalls(t *testing.T) {
	fakeRelease(t, "99.1.0", false)
	exe := fakeExe(t)
	s := upgradeServer(t)
	s.background = true

	// /api/update says it can install, and how it will restart.
	rec := httptest.NewRecorder()
	s.handleUpdate(rec, httptest.NewRequest("GET", "/api/update?refresh=1", nil))
	var u map[string]any
	json.Unmarshal(rec.Body.Bytes(), &u)
	if u["newer"] != true || u["canInstall"] != true || u["startCommand"] != "cactusdb --port 8123 --runas background" || u["executable"] != exe {
		t.Fatalf("%s", rec.Body)
	}

	if rec := postUpgrade(s); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	st := waitUpgrade(t, s)
	if st.State != "restarting" || st.To != "99.1.0" || st.Backup != exe+".old" {
		t.Fatalf("%+v", st)
	}
	if b, _ := os.ReadFile(exe); !strings.Contains(string(b), "99.1.0") {
		t.Errorf("new version not in place: %s", b)
	}
	if b, _ := os.ReadFile(exe + ".old"); !strings.Contains(string(b), version) {
		t.Errorf("old version not kept: %s", b)
	}
	select {
	case <-s.shutdown:
	case <-time.After(2 * time.Second):
		t.Fatal("the server wasn't stopped for the restart")
	}
	s.mu.Lock()
	p := s.restart
	s.mu.Unlock()
	if p == nil || p.exe != exe || strings.Join(p.args, " ") != "--port 8123" || p.to != "99.1.0" {
		t.Fatalf("restart plan %+v", p)
	}
	// A second one is refused while this one restarts.
	if rec := postUpgrade(s); rec.Code != 409 {
		t.Errorf("second upgrade: %d", rec.Code)
	}
}

func TestUpgradeRefusesDamagedFile(t *testing.T) {
	fakeRelease(t, "99.1.0", true)
	exe := fakeExe(t)
	s := upgradeServer(t)
	if rec := postUpgrade(s); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	st := waitUpgrade(t, s)
	if st.State != "failed" || !strings.Contains(st.Error, "damaged") {
		t.Fatalf("%+v", st)
	}
	if b, _ := os.ReadFile(exe); !strings.Contains(string(b), version) {
		t.Errorf("program changed: %s", b)
	}
	if _, err := os.Stat(exe + ".download"); err == nil {
		t.Error("download left behind")
	}
	if s.restart != nil {
		t.Error("restart planned")
	}
}

func TestUpgradeUpToDateAndContainer(t *testing.T) {
	fakeRelease(t, version, false)
	fakeExe(t)
	s := upgradeServer(t)
	if rec := postUpgrade(s); rec.Code != 409 || !strings.Contains(rec.Body.String(), "latest") {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	t.Setenv("CACTUSDB_CONTAINER", "1")
	if rec := postUpgrade(s); rec.Code != 409 || !strings.Contains(rec.Body.String(), "container") {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

func TestUpgradeUploadNeedsSuperuser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake binary is a shell script")
	}
	exe := fakeExe(t)
	s := upgradeServer(t)
	bin := "#!/bin/sh\necho cactusdb 99.2.0\n"
	upload := func(pw, sum string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/upgrade/upload", strings.NewReader(bin))
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-Cactusdb-Superuser", pw)
		req.Header.Set("X-Cactusdb-Sha256", sum)
		rec := httptest.NewRecorder()
		s.handleUpgradeUpload(rec, req)
		return rec
	}
	if rec := upload("x", ""); rec.Code != 409 || !strings.Contains(rec.Body.String(), "superuser") {
		t.Fatalf("no superuser: %d %s", rec.Code, rec.Body)
	}
	const super = "a long superuser pw"
	if err := s.store.SetSuperuser(super, ""); err != nil {
		t.Fatal(err)
	}
	if rec := upload("wrong password", ""); rec.Code != 403 {
		t.Fatalf("wrong password: %d %s", rec.Code, rec.Body)
	}
	if b, _ := os.ReadFile(exe); strings.Contains(string(b), "99.2.0") {
		t.Fatal("installed with a wrong password")
	}
	s.throttle.ok(superThrottleID)
	if rec := upload(super, strings.Repeat("1", 64)); rec.Code != 400 || !strings.Contains(rec.Body.String(), "damaged") {
		t.Fatalf("bad sum: %d %s", rec.Code, rec.Body)
	}
	sum := sha256.Sum256([]byte(bin))
	rec := upload(super, hex.EncodeToString(sum[:]))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"state":"restarting"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if b, _ := os.ReadFile(exe); !strings.Contains(string(b), "99.2.0") {
		t.Errorf("not installed: %s", b)
	}
}

// A release's SHA256SUMS is signed by build.sh and checked before an update
// is installed: a changed file or another key is refused.
func TestReleaseSignature(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "k")
	if _, err := releaseCommand([]string{"release-keygen", key}); err != nil {
		t.Fatal(err)
	}
	if _, err := releaseCommand([]string{"release-keygen", key}); err == nil {
		t.Error("a second keygen replaced the key")
	}
	sums := filepath.Join(dir, "SHA256SUMS")
	os.WriteFile(sums, []byte("abc  cactusdb-linux-amd64\n"), 0o644)
	if _, err := releaseCommand([]string{"release-sign", key, sums}); err != nil {
		t.Fatal(err)
	}
	priv, _ := readReleaseKey(key)
	old := releaseKey
	defer func() { releaseKey = old }()
	releaseKey = base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	b, _ := os.ReadFile(sums)
	sig, _ := os.ReadFile(sums + ".sig")
	if err := verifyRelease(b, sig); err != nil {
		t.Errorf("good signature: %v", err)
	}
	if verifyRelease([]byte("def  cactusdb-linux-amd64\n"), sig) == nil {
		t.Error("a changed SHA256SUMS passed")
	}
	other := filepath.Join(dir, "other")
	releaseCommand([]string{"release-keygen", other})
	releaseCommand([]string{"release-sign", other, sums})
	sig2, _ := os.ReadFile(sums + ".sig")
	if verifyRelease(b, sig2) == nil {
		t.Error("another key's signature passed")
	}
}
