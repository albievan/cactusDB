package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The export folder can be changed in the web UI. The choice is saved in the
// connection database (table meta) and used the next time cactusdb starts,
// unless --exportto is given, which always wins.

const exportDirKey = "export_dir"

// savedExportDir returns the export folder saved from the web UI, or "".
func savedExportDir(st *Store) string {
	if st == nil {
		return ""
	}
	dir, err := st.meta(exportDirKey)
	if err != nil {
		return ""
	}
	return dir
}

// exportFolder is the folder exports are written to now.
func (s *server) exportFolder() string {
	s.exportMu.RLock()
	defer s.exportMu.RUnlock()
	return s.exportDir
}

// setExportFolder switches exports to dir. Folders used earlier stay
// downloadable until cactusdb stops.
func (s *server) setExportFolder(dir string) {
	s.exportMu.Lock()
	defer s.exportMu.Unlock()
	s.exportDir = dir
	if !slices.Contains(s.exportDirs, dir) {
		s.exportDirs = append(s.exportDirs, dir)
	}
}

// exportFolders lists every export folder of this run, the current one first.
func (s *server) exportFolders() []string {
	s.exportMu.RLock()
	defer s.exportMu.RUnlock()
	dirs := []string{s.exportDir}
	for _, d := range s.exportDirs {
		if d != s.exportDir {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

func (s *server) settingsJSON() map[string]any {
	return map[string]any{
		"exportDir":        s.exportFolder(),
		"defaultExportDir": s.defaultExportDir,
		"exportToFlag":     s.exportToFlag, // set when --exportto overrides the saved folder at startup
	}
}

func (s *server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.settingsJSON())
}

// handleSaveSettings changes the export folder and saves it for the next
// start. An empty folder means the default one. Like /api/shutdown it only
// accepts JSON, so other websites can't change it.
func (s *server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeErr(w, 415, errors.New("expected a JSON request"))
		return
	}
	var req struct {
		ExportDir string `json:"exportDir"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	dir, saved, err := checkExportDir(req.ExportDir, s.defaultExportDir)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.store.setMeta(exportDirKey, saved); err != nil {
		writeErr(w, 500, fmt.Errorf("cannot save the setting: %w", err))
		return
	}
	s.setExportFolder(dir)
	logAction(r, "export folder set to %s", dir)
	writeJSON(w, 200, s.settingsJSON())
}

// checkExportDir validates a folder typed in the web UI: a full path (a
// leading ~ is the home folder), created if missing, and writable. It
// returns the folder to use and the value to save ("" for the default).
func checkExportDir(in, defaultDir string) (dir, saved string, err error) {
	in = strings.TrimSpace(in)
	if in == "" {
		dir = defaultDir
	} else {
		dir = in
		if dir == "~" || strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, `~\`) {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", "", fmt.Errorf("cannot find the home folder: %w", err)
			}
			dir = filepath.Join(home, dir[1:])
		}
		if !filepath.IsAbs(dir) {
			return "", "", fmt.Errorf("%q is not a full path: give the whole path, e.g. %s", in, filepath.Join(filepath.Dir(defaultDir), "exports"))
		}
		dir = filepath.Clean(dir)
		if dir != defaultDir {
			saved = dir
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("cannot create %s: %w", dir, err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", "", fmt.Errorf("%s is not a folder", dir)
	}
	f, err := os.CreateTemp(dir, ".cactusdb-write-test-*")
	if err != nil {
		return "", "", fmt.Errorf("cannot write to %s: %w", dir, err)
	}
	f.Close()
	os.Remove(f.Name())
	return dir, saved, nil
}
