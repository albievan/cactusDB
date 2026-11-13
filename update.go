package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Checking for a newer cactusdb: the VERSION file on the main branch on
// GitHub names the latest version, whose binaries are in its GitHub release
// (tag v<version>). The web UI shows it with the command to download the
// binary for each system.

const (
	repoURL         = "https://github.com/albievan/cactusDB"
	rawURL          = "https://raw.githubusercontent.com/albievan/cactusDB/main/"
	updateCacheTime = time.Hour
	updateTimeout   = 6 * time.Second
)

var latestURL = rawURL + "VERSION" // a variable so tests can serve it

// releaseURL is where GitHub serves the files of releases (a variable so
// tests can serve it).
var releaseURL = repoURL + "/releases/download/"

// releaseDir is where the binaries, SHA256SUMS and SHA256SUMS.sig of
// version v are.
func releaseDir(v string) string { return releaseURL + "v" + v + "/" }

var versionRe = regexp.MustCompile(`^\d+(\.\d+)*$`)

type updateCheck struct {
	at     time.Time
	latest string
	err    error
}

var (
	updateMu   sync.Mutex
	lastUpdate updateCheck
)

// latestVersion returns the newest released version, asking GitHub at most
// once an hour unless refresh is set.
func latestVersion(ctx context.Context, refresh bool) (string, error) {
	updateMu.Lock()
	defer updateMu.Unlock()
	if !refresh && !lastUpdate.at.IsZero() && time.Since(lastUpdate.at) < updateCacheTime {
		return lastUpdate.latest, lastUpdate.err
	}
	latest, err := fetchLatestVersion(ctx)
	lastUpdate = updateCheck{at: time.Now(), latest: latest, err: err}
	return latest, err
}

func fetchLatestVersion(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, updateTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", latestURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "cactusdb/"+version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		var ue interface{ Timeout() bool }
		if errors.As(err, &ue) && ue.Timeout() {
			return "", fmt.Errorf("GitHub did not answer within %s (does this computer have internet access?)", updateTimeout)
		}
		return "", fmt.Errorf("cannot reach GitHub: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GitHub answered %s for %s", resp.Status, latestURL)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(b))
	if !versionRe.MatchString(v) {
		return "", fmt.Errorf("unexpected version %q in %s", v, latestURL)
	}
	return v, nil
}

// newerVersion reports whether version a is newer than b ("1.10.0" > "1.9.2").
func newerVersion(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(pa), len(pb)) {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// updateJSON is what the web UI shows: this version and system, the latest
// version, and where the setup scripts and binaries are.
func updateJSON(latest string, err error) map[string]any {
	out := map[string]any{
		"current": version, "os": runtime.GOOS, "arch": runtime.GOARCH,
		"latest": latest, "newer": err == nil && newerVersion(latest, version),
		"repo": repoURL, "raw": rawURL, "dist": repoURL + "/releases/latest/download/", "latestUrl": latestURL,
		"releases": releaseURL, "sums": rawURL + "release/",
		"changelog": repoURL + "/blob/main/CHANGELOG.md",
	}
	if err != nil {
		out["error"] = err.Error()
	}
	return out
}

func (s *server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	latest, err := latestVersion(r.Context(), r.URL.Query().Get("refresh") == "1")
	out := updateJSON(latest, err)
	for k, v := range s.upgradeInfo() {
		out[k] = v
	}
	writeJSON(w, 200, out)
}

// logUpdate says at startup when a newer cactusdb is available.
func logUpdate() {
	latest, err := latestVersion(context.Background(), false)
	if err == nil && newerVersion(latest, version) {
		log.Printf("cactusdb %s is available (this is %s): see ☰ → Check for update in the web UI, or run setup.sh / setup.ps1 from %s", latest, version, repoURL)
	}
}
