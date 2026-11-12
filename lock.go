package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/argon2"
)

// Each saved connection has its own password. The connection's username and
// password, and its saved queries, are sealed with a key derived from that
// password (Argon2id), inside the store-wide encryption of crypt.go. Without
// the connection password they can't be read, not even with cactus.key.
//
// The lock column holds the key derivation's parameters and salt:
// "argon2id:v1:<time>:<memory KiB>:<threads>:<salt base64>"; "" means the
// connection has no password yet (saved by an older cactusdb, or by the
// command line).

const lockPrefix = "argon2id:v1:"

// Argon2id cost: about a tenth of a second and 64 MiB per attempt.
const (
	lockTime    = 3
	lockMemory  = 64 * 1024
	lockThreads = 2
	minLockLen  = 6 // characters in a connection password
)

var (
	errLockWrong    = errors.New("wrong connection password")
	errLockRequired = errors.New("this connection is protected: enter its connection password")
	errLockedSave   = errors.New("a password-protected connection with this name exists; it was not replaced")
)

// connLock is a connection's key derivation, and the key once unlocked.
type connLock struct {
	time, memory uint32
	threads      uint8
	salt         []byte
}

func newConnLock() connLock {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		panic(err)
	}
	return connLock{time: lockTime, memory: lockMemory, threads: lockThreads, salt: salt}
}

func (l connLock) String() string {
	return fmt.Sprintf("%s%d:%d:%d:%s", lockPrefix, l.time, l.memory, l.threads, base64.StdEncoding.EncodeToString(l.salt))
}

func parseConnLock(s string) (connLock, error) {
	parts := strings.Split(strings.TrimPrefix(s, lockPrefix), ":")
	if !strings.HasPrefix(s, lockPrefix) || len(parts) != 4 {
		return connLock{}, errors.New("unknown connection lock format")
	}
	t, err1 := strconv.ParseUint(parts[0], 10, 32)
	m, err2 := strconv.ParseUint(parts[1], 10, 32)
	p, err3 := strconv.ParseUint(parts[2], 10, 8)
	salt, err4 := base64.StdEncoding.DecodeString(parts[3])
	if err := errors.Join(err1, err2, err3, err4); err != nil || len(salt) < 16 {
		return connLock{}, errors.New("corrupt connection lock")
	}
	return connLock{time: uint32(t), memory: uint32(m), threads: uint8(p), salt: salt}, nil
}

// kdfSlots limits how many Argon2 derivations run at once: each takes
// l.memory (64 MiB), so a burst of password guesses would otherwise run the
// server out of memory.
var (
	kdfSlots  = make(chan struct{}, 2)
	kdfQueued atomic.Int32
)

// key derives n bytes from password with Argon2id, waiting for a free slot.
func (l connLock) key(password string, n uint32) []byte {
	kdfSlots <- struct{}{}
	defer func() { <-kdfSlots }()
	return argon2.IDKey([]byte(password), l.salt, l.time, l.memory, l.threads, n)
}

// box derives the connection key from password.
func (l connLock) box(password string) *secretBox {
	b, err := newSecretBox(l.key(password, 32))
	if err != nil {
		panic(err) // a 32-byte key is always valid
	}
	return b
}

// checkLockPassword says what is wrong with a new connection password.
func checkLockPassword(pw string) error {
	if len([]rune(pw)) < minLockLen {
		return fmt.Errorf("the connection password must be at least %d characters", minLockLen)
	}
	return nil
}

// ---- store ----------------------------------------------------------------

// migrateLocks adds the lock column and the queries table to stores made by
// older versions.
func (s *Store) migrateLocks() error {
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM pragma_table_info('connections') WHERE name = 'lock'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := s.DB().Exec(`ALTER TABLE connections ADD COLUMN lock TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	// options: extra connection settings (ConnParams.Options), since 1.32.0.
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM pragma_table_info('connections') WHERE name = 'options'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := s.DB().Exec(`ALTER TABLE connections ADD COLUMN options TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	_, err := s.DB().Exec(`CREATE TABLE IF NOT EXISTS queries (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		conn_id    INTEGER NOT NULL,
		data       TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS queries_conn ON queries(conn_id);
	-- Each saved version of a query, sealed like the query (since 1.46.0).
	CREATE TABLE IF NOT EXISTS query_versions (
		id       INTEGER PRIMARY KEY AUTOINCREMENT,
		query_id INTEGER NOT NULL,
		conn_id  INTEGER NOT NULL,
		version  INTEGER NOT NULL,
		data     TEXT NOT NULL,
		saved_at TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS query_versions_query ON query_versions(query_id);
	-- The saved views of DB Relationships, sealed like the queries (since 1.53.0).
	CREATE TABLE IF NOT EXISTS rel_views (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		conn_id    INTEGER NOT NULL,
		data       TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS rel_views_conn ON rel_views(conn_id)`)
	return err
}

// lockOf returns a saved connection's lock ("" when it has none).
func (s *Store) lockOf(id int64) (string, error) {
	var lock string
	err := s.DB().QueryRow(`SELECT lock FROM connections WHERE id = ?`, id).Scan(&lock)
	return lock, err
}

// Unlock opens a saved connection with its connection password and returns
// it with its credentials, and the key for its saved queries (nil for a
// connection without a password).
func (s *Store) Unlock(id int64, password string) (SavedConn, *secretBox, error) {
	c, err := scanConn(s.DB().QueryRow(`SELECT `+connCols+` FROM connections WHERE id = ?`, id))
	if err != nil {
		return c, nil, err
	}
	if c.Lock == "" {
		return c, nil, s.decrypt(&c)
	}
	if password == "" {
		return c, nil, errLockRequired
	}
	l, err := parseConnLock(c.Lock)
	if err != nil {
		return c, nil, err
	}
	if err := s.decrypt(&c); err != nil { // the store-wide layer
		return c, nil, err
	}
	box := l.box(password)
	if c.Username, err = box.open("username", c.Username); err != nil {
		return c, nil, errLockWrong
	}
	if c.Password, err = box.open("password", c.Password); err != nil {
		return c, nil, errLockWrong
	}
	// Opened with its own password: make sure the superuser can open it too
	// (connections saved before the superuser password was set).
	var escrow string
	if s.DB().QueryRow(`SELECT escrow FROM connections WHERE id = ?`, id).Scan(&escrow) == nil && escrow == "" {
		s.setEscrow(id, password)
	}
	return c, box, nil
}

// SaveLocked saves a connection protected by its own password: a new one
// (c.ID 0, the name must be free), or an existing one that oldBox unlocked
// (nil for one that had no password). With a new password the saved queries
// are sealed again with the new key; with password "" the current password
// is kept.
func (s *Store) SaveLocked(c SavedConn, password string, oldBox *secretBox) (int64, *secretBox, error) {
	// Sealed for the superuser before the transaction: the store has a single
	// database connection, which the transaction holds.
	escrow := ""
	if password != "" {
		var err error
		if escrow, err = s.escrowFor(password); err != nil {
			return 0, nil, err
		}
	}
	tx, err := s.DB().Begin()
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()

	var lock connLock
	var box *secretBox
	if c.ID != 0 {
		var cur string
		if err := tx.QueryRow(`SELECT lock FROM connections WHERE id = ?`, c.ID).Scan(&cur); err != nil {
			return 0, nil, err
		}
		if cur != "" && password == "" { // keep the password
			if lock, err = parseConnLock(cur); err != nil {
				return 0, nil, err
			}
			box = oldBox
		}
		var other int64
		if err := tx.QueryRow(`SELECT id FROM connections WHERE name = ? AND id <> ?`, c.Name, c.ID).Scan(&other); err == nil {
			return 0, nil, fmt.Errorf("a connection named %q already exists", c.Name)
		}
	} else {
		var other int64
		if err := tx.QueryRow(`SELECT id FROM connections WHERE name = ?`, c.Name).Scan(&other); err == nil {
			return 0, nil, fmt.Errorf("a connection named %q already exists: open it with Edit, or choose another name", c.Name)
		}
	}
	if box == nil {
		if err := checkLockPassword(password); err != nil {
			return 0, nil, err
		}
		lock = newConnLock()
		box = lock.box(password)
	}
	if box == nil {
		return 0, nil, errLockRequired
	}
	user := s.box.seal("username", box.seal("username", c.Username))
	pw := s.box.seal("password", box.seal("password", c.Password))
	now := time.Now().Format(time.RFC3339)
	if c.ID == 0 {
		res, err := tx.Exec(`INSERT INTO connections (name, engine, host, port, username, password, database, created_at, lock, options)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, c.Name, c.Engine, c.Host, c.Port, user, pw, c.Database, now, lock.String(), c.Options)
		if err != nil {
			return 0, nil, err
		}
		if c.ID, err = res.LastInsertId(); err != nil {
			return 0, nil, err
		}
	} else {
		if _, err := tx.Exec(`UPDATE connections SET name = ?, engine = ?, host = ?, port = ?, username = ?, password = ?,
			database = ?, lock = ?, options = ? WHERE id = ?`, c.Name, c.Engine, c.Host, c.Port, user, pw, c.Database, lock.String(), c.Options, c.ID); err != nil {
			return 0, nil, err
		}
		if box != oldBox { // a new key: seal the saved queries again
			if err := s.resealQueries(tx, c.ID, oldBox, box); err != nil {
				return 0, nil, err
			}
		}
	}
	if password != "" { // a new connection password: sealed for the superuser
		if _, err := tx.Exec(`UPDATE connections SET escrow = ? WHERE id = ?`, escrow, c.ID); err != nil {
			return 0, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return c.ID, box, nil
}

// SaveUnlocked saves a connection without a password of its own (the
// command line's cli-<host> connections). It never replaces one that has a
// password.
func (s *Store) SaveUnlocked(c SavedConn) (int64, error) {
	var lock string
	err := s.DB().QueryRow(`SELECT lock FROM connections WHERE name = ?`, c.Name).Scan(&lock)
	if err == nil && lock != "" {
		return 0, errLockedSave
	}
	return s.Save(c)
}

// ---- saved queries --------------------------------------------------------

// SavedQuery is a read-only query kept for a saved connection.
type SavedQuery struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	SQL         string `json:"sql"`
	Created     string `json:"created"`
	Updated     string `json:"updated"`
	Version     int    `json:"version"` // the current version's number
}

// QueryVersion is one saved version of a query.
type QueryVersion struct {
	Version     int    `json:"version"`
	Name        string `json:"name"`
	Description string `json:"description"`
	SQL         string `json:"sql"`
	Saved       string `json:"saved"`
}

// maxQueryVersions is how many versions of a query are kept: the oldest go.
const maxQueryVersions = 100

type queryData struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	SQL         string `json:"sql"`
}

// sealQuery encrypts a query's name, description and SQL (see sealData).
func (s *Store) sealQuery(box *secretBox, d queryData) string {
	b, _ := json.Marshal(d)
	return s.sealData(box, "query", string(b))
}

func (s *Store) openQuery(box *secretBox, data string) (queryData, error) {
	var d queryData
	plain, err := s.openData(box, "query", data)
	if err != nil {
		return d, err
	}
	return d, json.Unmarshal([]byte(plain), &d)
}

// sealData encrypts what is kept for a saved connection (purpose: "query",
// "relview"): with the connection's key when it has one, always with the
// store's key. openData opens it.
func (s *Store) sealData(box *secretBox, purpose, plain string) string {
	if box != nil {
		plain = box.seal(purpose, plain)
	}
	return s.box.seal(purpose, plain)
}

func (s *Store) openData(box *secretBox, purpose, data string) (string, error) {
	plain, err := s.box.open(purpose, data)
	if err != nil {
		return "", err
	}
	if box != nil {
		if plain, err = box.open(purpose, plain); err != nil {
			return "", errLockWrong
		}
	} else if isSealed(plain) {
		return "", errLockRequired
	}
	return plain, nil
}

// Queries lists a connection's saved queries by name.
func (s *Store) Queries(connID int64, box *secretBox) ([]SavedQuery, error) {
	rows, err := s.DB().Query(`SELECT id, data, created_at, updated_at,
		(SELECT COALESCE(MAX(version), 1) FROM query_versions v WHERE v.query_id = queries.id)
		FROM queries WHERE conn_id = ?`, connID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SavedQuery{}
	for rows.Next() {
		var q SavedQuery
		var data string
		if err := rows.Scan(&q.ID, &data, &q.Created, &q.Updated, &q.Version); err != nil {
			return nil, err
		}
		d, err := s.openQuery(box, data)
		if err != nil {
			return nil, err
		}
		q.Name, q.Description, q.SQL = d.Name, d.Description, d.SQL
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortQueries(out)
	return out, nil
}

// SaveQuery adds a query (q.ID 0) or changes one of the connection's. Each
// save is kept as a new version (see QueryVersions); saving a query unchanged
// makes none. It returns the query's id and current version.
func (s *Store) SaveQuery(connID int64, box *secretBox, q SavedQuery) (int64, int, error) {
	existing, err := s.Queries(connID, box)
	if err != nil {
		return 0, 0, err
	}
	var old *SavedQuery
	for i, e := range existing {
		if e.ID == q.ID {
			old = &existing[i]
		} else if strings.EqualFold(strings.TrimSpace(e.Name), strings.TrimSpace(q.Name)) {
			return 0, 0, fmt.Errorf("this connection already has a query named %q", e.Name)
		}
	}
	if q.ID != 0 && old == nil {
		return 0, 0, sql.ErrNoRows
	}
	if old != nil && old.Name == q.Name && old.Description == q.Description && old.SQL == q.SQL {
		return q.ID, old.Version, nil
	}
	d := queryData{Name: q.Name, Description: q.Description, SQL: q.SQL}
	now := time.Now().Format(time.RFC3339)
	tx, err := s.DB().Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	id, version := q.ID, 1
	if old == nil {
		res, err := tx.Exec(`INSERT INTO queries (conn_id, data, created_at, updated_at) VALUES (?, ?, ?, ?)`, connID, s.sealQuery(box, d), now, now)
		if err != nil {
			return 0, 0, err
		}
		if id, err = res.LastInsertId(); err != nil {
			return 0, 0, err
		}
	} else {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM query_versions WHERE query_id = ?`, id).Scan(&n); err != nil {
			return 0, 0, err
		}
		if n == 0 { // saved before versions were kept: its contents so far are version 1
			first := queryData{Name: old.Name, Description: old.Description, SQL: old.SQL}
			if _, err := tx.Exec(`INSERT INTO query_versions (query_id, conn_id, version, data, saved_at) VALUES (?, ?, 1, ?, ?)`,
				id, connID, s.sealQuery(box, first), old.Updated); err != nil {
				return 0, 0, err
			}
		}
		if _, err := tx.Exec(`UPDATE queries SET data = ?, updated_at = ? WHERE id = ? AND conn_id = ?`, s.sealQuery(box, d), now, id, connID); err != nil {
			return 0, 0, err
		}
		if err := tx.QueryRow(`SELECT MAX(version) + 1 FROM query_versions WHERE query_id = ?`, id).Scan(&version); err != nil {
			return 0, 0, err
		}
	}
	if _, err := tx.Exec(`INSERT INTO query_versions (query_id, conn_id, version, data, saved_at) VALUES (?, ?, ?, ?, ?)`,
		id, connID, version, s.sealQuery(box, d), now); err != nil {
		return 0, 0, err
	}
	if _, err := tx.Exec(`DELETE FROM query_versions WHERE query_id = ? AND version <= ?`, id, version-maxQueryVersions); err != nil {
		return 0, 0, err
	}
	return id, version, tx.Commit()
}

// QueryVersions lists the saved versions of one of the connection's queries,
// newest first. A query saved before versions were kept has one, its current.
func (s *Store) QueryVersions(connID int64, box *secretBox, id int64) ([]QueryVersion, error) {
	rows, err := s.DB().Query(`SELECT version, data, saved_at FROM query_versions WHERE query_id = ? AND conn_id = ? ORDER BY version DESC`, id, connID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QueryVersion{}
	for rows.Next() {
		var v QueryVersion
		var data string
		if err := rows.Scan(&v.Version, &data, &v.Saved); err != nil {
			return nil, err
		}
		d, err := s.openQuery(box, data)
		if err != nil {
			return nil, err
		}
		v.Name, v.Description, v.SQL = d.Name, d.Description, d.SQL
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		var data, updated string
		err := s.DB().QueryRow(`SELECT data, updated_at FROM queries WHERE id = ? AND conn_id = ?`, id, connID).Scan(&data, &updated)
		if err != nil {
			return nil, err
		}
		d, err := s.openQuery(box, data)
		if err != nil {
			return nil, err
		}
		out = append(out, QueryVersion{Version: 1, Name: d.Name, Description: d.Description, SQL: d.SQL, Saved: updated})
	}
	return out, nil
}

// DeleteQuery deletes one of the connection's saved queries.
func (s *Store) DeleteQuery(connID, id int64) error {
	res, err := s.DB().Exec(`DELETE FROM queries WHERE id = ? AND conn_id = ?`, id, connID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	_, err = s.DB().Exec(`DELETE FROM query_versions WHERE query_id = ?`, id)
	return err
}

// resealQueries re-encrypts a connection's queries, their versions and its
// saved relationship views, from oldBox to newBox.
func (s *Store) resealQueries(tx *sql.Tx, connID int64, oldBox, newBox *secretBox) error {
	for _, t := range []struct{ table, purpose string }{{"queries", "query"}, {"query_versions", "query"}, {"rel_views", "relview"}} {
		if err := s.resealTable(tx, t.table, t.purpose, connID, oldBox, newBox); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) resealTable(tx *sql.Tx, table, purpose string, connID int64, oldBox, newBox *secretBox) error {
	rows, err := tx.Query(`SELECT id, data FROM `+table+` WHERE conn_id = ?`, connID)
	if err != nil {
		return err
	}
	type row struct {
		id   int64
		data string
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.data); err != nil {
			rows.Close()
			return err
		}
		all = append(all, r)
	}
	rows.Close()
	for _, r := range all {
		plain, err := s.openData(oldBox, purpose, r.data)
		if err != nil {
			return fmt.Errorf("saved %s (%s %d): %w", purpose, table, r.id, err)
		}
		if _, err := tx.Exec(`UPDATE `+table+` SET data = ? WHERE id = ?`, s.sealData(newBox, purpose, plain), r.id); err != nil {
			return err
		}
	}
	return nil
}

func sortQueries(qs []SavedQuery) {
	for i := 1; i < len(qs); i++ {
		for j := i; j > 0 && strings.ToLower(qs[j].Name) < strings.ToLower(qs[j-1].Name); j-- {
			qs[j], qs[j-1] = qs[j-1], qs[j]
		}
	}
}

// ---- wrong-password throttle ------------------------------------------------

// lockThrottle slows down guessing: after 5 wrong connection passwords for
// a connection, each further try must wait (2 s, 4 s, … up to 5 minutes).
type lockThrottle struct {
	mu    sync.Mutex
	fails map[int64]int
	until map[int64]time.Time
}

func (t *lockThrottle) wait(id int64) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if d := time.Until(t.until[id]); d > 0 {
		return d
	}
	return 0
}

func (t *lockThrottle) failed(id int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fails == nil {
		t.fails, t.until = map[int64]int{}, map[int64]time.Time{}
	}
	t.fails[id]++
	if n := t.fails[id]; n >= 5 {
		d := min(time.Duration(1<<min(n-4, 9))*time.Second, 5*time.Minute)
		t.until[id] = time.Now().Add(d)
	}
}

func (t *lockThrottle) ok(id int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.fails, id)
	delete(t.until, id)
}
