package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// cactus.conn.db can be moved from the web UI (☰ → Folder setup). It is
// copied with VACUUM INTO while cactusdb holds its only connection, so nothing
// is written in between; the copy is checked, used from then on, and the old
// file is deleted. The key file goes along when it was next to it.
//
// So that the next start finds it, the new place is written to
// cactus.conn.db.location next to the program; it is read when cactusdb
// starts without --conn-db (which always wins).

const locationFileName = "cactus.conn.db.location"

// storeDSN opens the connection database. secure_delete overwrites freed
// pages, so replaced plaintext doesn't linger.
func storeDSN(path string) string {
	return sqliteURI(path) + "?_pragma=busy_timeout(5000)&_pragma=secure_delete(1)"
}

// movedConnDB returns the connection database named in the location file in
// dir, or "" when there is none.
func movedConnDB(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, locationFileName))
	if err != nil {
		return ""
	}
	p := strings.TrimSpace(string(b))
	if !filepath.IsAbs(p) {
		return ""
	}
	return filepath.Clean(p)
}

// checkDBPath validates a new place typed in the web UI: a full path (a
// leading ~ is the home folder) of a file, or of a folder to put
// cactus.conn.db in. "" is the default place.
func checkDBPath(in, defaultPath string) (string, error) {
	in = strings.TrimSpace(in)
	if in == "" {
		return defaultPath, nil
	}
	p := in
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot find the home folder: %w", err)
		}
		p = filepath.Join(home, p[1:])
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%q is not a full path: give the whole path, e.g. %s", in, filepath.Join(filepath.Dir(filepath.Dir(defaultPath)), "cactusdb-data", filepath.Base(defaultPath)))
	}
	// A folder: one that exists, a path ending in a slash, or a new path
	// without an extension (e.g. /srv/cactusdb-data).
	fi, err := os.Stat(p)
	if (err == nil && fi.IsDir()) || strings.HasSuffix(in, "/") || strings.HasSuffix(in, `\`) || (err != nil && filepath.Ext(p) == "") {
		p = filepath.Join(p, filepath.Base(defaultPath))
	}
	return filepath.Clean(p), nil
}

// moveResult says what a move did.
type moveResult struct {
	From, To         string
	KeyFrom, KeyTo   string // "" when the key stayed (passphrase, or --key-file elsewhere)
	Warnings         []string
	LocationFile     string // where the new place is remembered ("" when it isn't)
	LocationFileNote string
}

// moveTo moves the connection database to newPath and remembers it in the
// location file in locDir ("" to not remember it). defaultPath is where
// cactusdb looks without a location file: moving there removes the file.
func (st *Store) moveTo(ctx context.Context, newPath, locDir, defaultPath string) (*moveResult, error) {
	st.moveMu.Lock()
	defer st.moveMu.Unlock()
	old := st.path
	res := &moveResult{From: old, To: newPath}
	if samePath(old, newPath) {
		return nil, fmt.Errorf("the connection database is already %s", old)
	}
	if _, err := os.Stat(newPath); err == nil {
		return nil, fmt.Errorf("%s already exists: cactusdb won't overwrite it. Choose another name, or move that file away first", newPath)
	}
	dir := filepath.Dir(newPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("cannot create %s: %w", dir, err)
	}
	if f, err := os.CreateTemp(dir, ".cactusdb-write-test-*"); err != nil {
		return nil, fmt.Errorf("cannot write to %s: %w", dir, err)
	} else {
		f.Close()
		os.Remove(f.Name())
	}

	// The key goes along when it is the one next to the database.
	var key []byte
	if st.keyFile != "" && samePath(filepath.Dir(st.keyFile), filepath.Dir(old)) {
		k, err := os.ReadFile(st.keyFile)
		if err != nil {
			return nil, fmt.Errorf("cannot read the key %s: %w", st.keyFile, err)
		}
		res.KeyFrom, res.KeyTo = st.keyFile, filepath.Join(dir, filepath.Base(st.keyFile))
		if cur, err := os.ReadFile(res.KeyTo); err == nil && !bytes.Equal(cur, k) {
			return nil, fmt.Errorf("%s already holds a different key: cactusdb won't overwrite it", res.KeyTo)
		} else if err == nil {
			res.KeyFrom = "" // the same key is there already: nothing to copy or delete
		}
		key = k
	}

	// Hold the only connection: nothing else is written until the switch.
	oldDB := st.DB()
	conn, err := oldDB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	undo := func() {
		os.Remove(newPath)
		if res.KeyFrom != "" {
			os.Remove(res.KeyTo)
		}
	}
	if _, err := conn.ExecContext(ctx, `VACUUM INTO ?`, newPath); err != nil {
		os.Remove(newPath)
		return nil, fmt.Errorf("cannot copy the connection database to %s: %w", newPath, err)
	}
	os.Chmod(newPath, 0o600)
	if res.KeyFrom != "" {
		if err := os.WriteFile(res.KeyTo, key, 0o600); err != nil {
			undo()
			return nil, fmt.Errorf("cannot copy the key to %s: %w", res.KeyTo, err)
		}
	}
	newDB, err := sql.Open("sqlite", storeDSN(newPath))
	if err != nil {
		undo()
		return nil, err
	}
	newDB.SetMaxOpenConns(1)
	var nOld, nNew int64
	conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM connections`).Scan(&nOld)
	if err := newDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM connections`).Scan(&nNew); err != nil || nNew != nOld {
		newDB.Close()
		undo()
		if err == nil {
			err = fmt.Errorf("it has %d saved connections instead of %d", nNew, nOld)
		}
		return nil, fmt.Errorf("the copy in %s is not right: %w", newPath, err)
	}

	// Remember the new place for the next start.
	if locDir != "" {
		loc := filepath.Join(locDir, locationFileName)
		if samePath(newPath, defaultPath) {
			err = os.Remove(loc)
			if errors.Is(err, os.ErrNotExist) {
				err = nil
			}
		} else {
			err = os.WriteFile(loc, []byte(newPath+"\n"), 0o644)
			res.LocationFile = loc
		}
		if err != nil {
			newDB.Close()
			undo()
			return nil, fmt.Errorf("cannot remember the new place in %s: %w", loc, err)
		}
	}

	st.cur.Store(newDB)
	st.path = newPath
	if res.KeyTo != "" {
		st.keyFile = res.KeyTo
	}
	oldDB.Close() // requests waiting for the old file fail rather than write to it
	conn.Close()

	// The old copy holds the (encrypted) credentials: don't leave it behind.
	for _, f := range []string{old, old + "-wal", old + "-shm", old + "-journal"} {
		if err := removeRetry(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("the old file %s could not be deleted (%v): delete it yourself", f, err))
		}
	}
	if res.KeyFrom != "" {
		if err := removeRetry(res.KeyFrom); err != nil && !errors.Is(err, os.ErrNotExist) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("the old key %s could not be deleted (%v): delete it yourself", res.KeyFrom, err))
		}
	}
	return res, nil
}

// removeRetry deletes f, trying again for a moment: on Windows a file that
// was just closed can still be held briefly.
func removeRetry(f string) error {
	var err error
	for i := 0; i < 10; i++ {
		if err = os.Remove(f); err == nil || errors.Is(err, os.ErrNotExist) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}

// samePath reports whether a and b name the same file or folder.
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	fa, errA := os.Stat(a)
	fb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}
