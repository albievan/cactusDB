package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// SavedConn is a connection persisted in cactus.conn.db.
type SavedConn struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Created string `json:"created"`
	ConnParams
}

// Store keeps saved connections in a local SQLite database. Usernames and
// passwords are stored encrypted (see crypt.go); the other fields are not
// secret and stay readable.
type Store struct {
	db      *sql.DB
	box     *secretBox
	keyDesc string // where the encryption key came from, for the startup banner
}

func openStore(path, keyFile string) (*Store, error) {
	// secure_delete overwrites freed pages, so replaced plaintext doesn't linger.
	db, err := sql.Open("sqlite", sqliteURI(path)+"?_pragma=busy_timeout(5000)&_pragma=secure_delete(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.init(path, keyFile); err != nil {
		db.Close()
		return nil, err
	}
	// The file holds credentials, so keep it private to the current user (best effort).
	os.Chmod(path, 0o600)
	return s, nil
}

func (s *Store) init(path, keyFile string) error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS connections (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		name       TEXT NOT NULL UNIQUE,
		engine     TEXT NOT NULL,
		host       TEXT NOT NULL DEFAULT '',
		port       INTEGER NOT NULL DEFAULT 0,
		username   TEXT NOT NULL DEFAULT '',
		password   TEXT NOT NULL DEFAULT '',
		database   TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`)
	if err != nil {
		return err
	}

	// Per-store random salt for passphrase-derived keys.
	saltB64, err := s.meta("salt")
	if err != nil {
		return err
	}
	if saltB64 == "" {
		salt := make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return err
		}
		saltB64 = base64.StdEncoding.EncodeToString(salt)
		if err := s.setMeta("salt", saltB64); err != nil {
			return err
		}
	}
	salt, _ := base64.StdEncoding.DecodeString(saltB64)

	check, err := s.meta("keycheck")
	if err != nil {
		return err
	}
	src, err := loadKey(keyFile, salt, check == "")
	if err != nil {
		return err
	}
	s.keyDesc = strings.TrimPrefix(src.desc, "new ")
	if s.box, err = newSecretBox(src.key); err != nil {
		return err
	}

	// A known value encrypted with the key detects a wrong key or passphrase
	// before anything is decrypted into garbage or re-encrypted twice.
	if check == "" {
		if err := s.setMeta("keycheck", s.box.seal("keycheck", keyCheckPlain)); err != nil {
			return err
		}
	} else if plain, err := s.box.open("keycheck", check); err != nil || plain != keyCheckPlain {
		return fmt.Errorf("cannot decrypt the saved connections in %s with %s.\n"+
			"  Restore the original %s, or set the %s that was used before,\n"+
			"  or start with an empty file using --conn-db <new file>", path, src.desc, keyFileName, passphraseEnv)
	}
	return s.encryptPlaintextRows()
}

// encryptPlaintextRows encrypts credentials saved by cactusdb versions that
// stored them in plain text, then vacuums so no plaintext copy remains.
func (s *Store) encryptPlaintextRows() error {
	rows, err := s.db.Query(`SELECT id, username, password FROM connections
		WHERE username NOT LIKE 'enc:v1:%' OR password NOT LIKE 'enc:v1:%'`)
	if err != nil {
		return err
	}
	type cred struct {
		id       int64
		user, pw string
	}
	var plain []cred
	for rows.Next() {
		var c cred
		if err := rows.Scan(&c.id, &c.user, &c.pw); err != nil {
			rows.Close()
			return err
		}
		plain = append(plain, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(plain) == 0 {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for _, c := range plain {
		if !isSealed(c.user) {
			c.user = s.box.seal("username", c.user)
		}
		if !isSealed(c.pw) {
			c.pw = s.box.seal("password", c.pw)
		}
		if _, err := tx.Exec(`UPDATE connections SET username = ?, password = ? WHERE id = ?`, c.user, c.pw, c.id); err != nil {
			tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = s.db.Exec(`VACUUM`)
	return err
}

func (s *Store) meta(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) setMeta(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *Store) decrypt(c *SavedConn) error {
	var err error
	if c.Username, err = s.box.open("username", c.Username); err != nil {
		return fmt.Errorf("saved connection %q: cannot decrypt username: %w", c.Name, err)
	}
	if c.Password, err = s.box.open("password", c.Password); err != nil {
		return fmt.Errorf("saved connection %q: cannot decrypt password: %w", c.Name, err)
	}
	return nil
}

const connCols = `id, name, engine, host, port, username, password, database, created_at`

func scanConn(sc interface{ Scan(...any) error }) (SavedConn, error) {
	var c SavedConn
	err := sc.Scan(&c.ID, &c.Name, &c.Engine, &c.Host, &c.Port, &c.Username, &c.Password, &c.Database, &c.Created)
	return c, err
}

// List returns all saved connections with decrypted usernames and passwords.
func (s *Store) List() ([]SavedConn, error) {
	rows, err := s.db.Query(`SELECT ` + connCols + ` FROM connections ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SavedConn{}
	for rows.Next() {
		c, err := scanConn(rows)
		if err != nil {
			return nil, err
		}
		if err := s.decrypt(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Get(id int64) (SavedConn, error) {
	c, err := scanConn(s.db.QueryRow(`SELECT `+connCols+` FROM connections WHERE id = ?`, id))
	if err != nil {
		return c, err
	}
	return c, s.decrypt(&c)
}

// GetByName returns a saved connection by name, or sql.ErrNoRows.
func (s *Store) GetByName(name string) (SavedConn, error) {
	c, err := scanConn(s.db.QueryRow(`SELECT `+connCols+` FROM connections WHERE name = ?`, name))
	if err != nil {
		return c, err
	}
	return c, s.decrypt(&c)
}

// Save inserts a connection, or replaces the one with the same name.
func (s *Store) Save(c SavedConn) (int64, error) {
	_, err := s.db.Exec(`INSERT INTO connections (name, engine, host, port, username, password, database, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET engine = excluded.engine, host = excluded.host, port = excluded.port,
			username = excluded.username, password = excluded.password, database = excluded.database`,
		c.Name, c.Engine, c.Host, c.Port, s.box.seal("username", c.Username), s.box.seal("password", c.Password),
		c.Database, time.Now().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRow(`SELECT id FROM connections WHERE name = ?`, c.Name).Scan(&id)
	return id, err
}

func (s *Store) Delete(id int64) error {
	_, err := s.db.Exec(`DELETE FROM connections WHERE id = ?`, id)
	return err
}
