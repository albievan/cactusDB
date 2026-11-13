package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// The export folder can be changed in the web UI. The choice is saved in the
// connection database (table meta) and used the next time cactusdb starts,
// unless --exportto is given, which always wins.

const exportDirKey = "export_dir"

// The idle time: a connection unused for this long is disconnected, by the
// page and, for pages that were closed, by the server. 0 means never.
const (
	idleMinutesKey     = "idle_minutes"
	defaultIdleMinutes = 15
	maxIdleMinutes     = 24 * 60
)

// idleMinutes returns the idle time setting.
func (s *server) idleMinutes() int {
	if v, err := s.store.meta(idleMinutesKey); err == nil && v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return defaultIdleMinutes
}

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
	out := map[string]any{
		"version":          version,
		"exportDir":        s.exportFolder(),
		"defaultExportDir": s.defaultExportDir,
		"exportToFlag":     s.exportToFlag, // set when --exportto overrides the saved folder at startup
		"idleMinutes":      s.idleMinutes(),
		"dbPath":           s.store.Path(),
		"defaultDbPath":    s.defaultConnDB,
		"dbFlag":           s.connDBFlag, // set when started with --conn-db, which wins over a move
		"defaultIdle":      defaultIdleMinutes,
	}
	for k, v := range loadGAConfig(s.store).json() {
		out[k] = v
	}
	return out
}

func (s *server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.settingsJSON())
}

// handleSaveSettings changes the export folder and/or the idle time and saves
// them for the next start. An empty folder means the default one. Like
// /api/shutdown it only accepts JSON, so other websites can't change it.
func (s *server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeErr(w, 415, errors.New("expected a JSON request"))
		return
	}
	var req struct {
		ExportDir   *string `json:"exportDir"`
		IdleMinutes *int    `json:"idleMinutes"`
		DBPath      *string `json:"dbPath"` // move the connection database here

		GAID          *string `json:"gaId"`
		GASecret      *string `json:"gaSecret"` // "" keeps the saved one
		GAClearSecret *bool   `json:"gaClearSecret"`
		GACLI         *bool   `json:"gaCli"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.IdleMinutes != nil {
		n := *req.IdleMinutes
		if n < 0 || n > maxIdleMinutes {
			writeErr(w, 400, fmt.Errorf("the idle time must be between 0 (never disconnect) and %d minutes", maxIdleMinutes))
			return
		}
		if err := s.store.setMeta(idleMinutesKey, strconv.Itoa(n)); err != nil {
			writeErr(w, 500, fmt.Errorf("cannot save the setting: %w", err))
			return
		}
		logAction(r, "idle time set to %d minute(s)", n)
		s.event(r, "settings", false, "idle time set to %d minute(s)", n)
	}
	if req.ExportDir != nil {
		dir, saved, err := checkExportDir(*req.ExportDir, s.defaultExportDir)
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
		s.event(r, "settings", false, "export folder set to %s", dir)
	}
	if req.GAID != nil || req.GASecret != nil || req.GAClearSecret != nil || req.GACLI != nil {
		if err := saveGASettings(s.store, req.GAID, req.GASecret, req.GAClearSecret, req.GACLI); err != nil {
			writeErr(w, 400, err)
			return
		}
		c := loadGAConfig(s.store)
		logAction(r, "Google Analytics: measurement id %q, API secret set: %v, command-line runs: %v", c.ID, c.Secret != "", c.CLI)
		s.event(r, "settings", false, "Google Analytics: measurement id %q, API secret set: %v, command-line runs: %v", c.ID, c.Secret != "", c.CLI)
	}
	var moved *moveResult
	if req.DBPath != nil {
		p, err := checkDBPath(*req.DBPath, s.defaultConnDB)
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		if moved, err = s.store.moveTo(r.Context(), p, filepath.Dir(s.defaultConnDB), s.defaultConnDB); err != nil {
			writeErr(w, 400, err)
			return
		}
		logAction(r, "connection database moved from %s to %s%s", moved.From, moved.To, keyMoveNote(moved))
		s.event(r, "settings", false, "connection database moved from %s to %s%s", moved.From, moved.To, keyMoveNote(moved))
		for _, w := range moved.Warnings {
			log.Printf("warning: %s", w)
		}
	}
	out := s.settingsJSON()
	if moved != nil {
		out["moved"] = map[string]any{"from": moved.From, "to": moved.To, "keyFrom": moved.KeyFrom, "keyTo": moved.KeyTo, "warnings": moved.Warnings}
	}
	writeJSON(w, 200, out)
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
	if err := os.MkdirAll(dir, 0o700); err != nil {
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

func keyMoveNote(m *moveResult) string {
	if m.KeyFrom == "" {
		return ""
	}
	return fmt.Sprintf(", and the key from %s to %s", m.KeyFrom, m.KeyTo)
}
