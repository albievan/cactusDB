package main

import (
	"database/sql"
	"os"
	"time"
)

// SavedConn is a connection persisted in cactus.conn.db.
type SavedConn struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Created string `json:"created"`
	ConnParams
}

// Store keeps saved connections in a local SQLite database.
type Store struct {
	db *sql.DB
}

func openStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", sqliteURI(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS connections (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		name       TEXT NOT NULL UNIQUE,
		engine     TEXT NOT NULL,
		host       TEXT NOT NULL DEFAULT '',
		port       INTEGER NOT NULL DEFAULT 0,
		username   TEXT NOT NULL DEFAULT '',
		password   TEXT NOT NULL DEFAULT '',
		database   TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	// The file holds passwords, so keep it private to the current user (best effort).
	os.Chmod(path, 0o600)
	return &Store{db: db}, nil
}

func (s *Store) List() ([]SavedConn, error) {
	rows, err := s.db.Query(`SELECT id, name, engine, host, port, username, password, database, created_at
		FROM connections ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SavedConn{}
	for rows.Next() {
		var c SavedConn
		if err := rows.Scan(&c.ID, &c.Name, &c.Engine, &c.Host, &c.Port, &c.Username, &c.Password, &c.Database, &c.Created); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Get(id int64) (SavedConn, error) {
	var c SavedConn
	err := s.db.QueryRow(`SELECT id, name, engine, host, port, username, password, database, created_at
		FROM connections WHERE id = ?`, id).
		Scan(&c.ID, &c.Name, &c.Engine, &c.Host, &c.Port, &c.Username, &c.Password, &c.Database, &c.Created)
	return c, err
}

// Save inserts a connection, or replaces the one with the same name.
func (s *Store) Save(c SavedConn) (int64, error) {
	_, err := s.db.Exec(`INSERT INTO connections (name, engine, host, port, username, password, database, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET engine = excluded.engine, host = excluded.host, port = excluded.port,
			username = excluded.username, password = excluded.password, database = excluded.database`,
		c.Name, c.Engine, c.Host, c.Port, c.Username, c.Password, c.Database, time.Now().Format(time.RFC3339))
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
