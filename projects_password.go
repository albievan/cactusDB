package main

import (
	"encoding/json"
	"errors"
	"strconv"
)

// A project password opens a project and every connection it links in one go:
// it seals the linked connections' passwords (like the superuser's escrow,
// superuser.go), with a key derived from it by Argon2id. Without one, a
// project asks each linked connection's own password, as before.

var errProjectPassword = errors.New("wrong project password")

// migrateProjectPassword adds the project password's columns to older stores.
func (s *Store) migrateProjectPassword() error {
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM pragma_table_info('projects') WHERE name = 'pw_lock'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := s.DB().Exec(`ALTER TABLE projects ADD COLUMN pw_lock TEXT NOT NULL DEFAULT '';
		ALTER TABLE projects ADD COLUMN pw_seal TEXT NOT NULL DEFAULT ''`)
	return err
}

// ProjectPasswords opens the connection passwords a project password seals
// (connection id -> password); errProjectPassword when it is wrong, and an
// empty map when the project has no password.
func (s *Store) ProjectPasswords(id int64, password string) (map[string]string, error) {
	var lock, seal string
	if err := s.DB().QueryRow(`SELECT pw_lock, pw_seal FROM projects WHERE id = ?`, id).Scan(&lock, &seal); err != nil {
		return nil, err
	}
	out := map[string]string{}
	if lock == "" {
		return out, nil
	}
	l, err := parseConnLock(lock)
	if err != nil {
		return nil, err
	}
	inner, err := s.box.open("projectpw", seal) // the store-wide layer
	if err != nil {
		return nil, err
	}
	plain, err := l.box(password).open("projectpw:"+strconv.FormatInt(id, 10), inner)
	if err != nil {
		return nil, errProjectPassword
	}
	return out, json.Unmarshal([]byte(plain), &out)
}

// SetProjectPassword seals the connection passwords with a project password
// ("" removes the project password).
func (s *Store) SetProjectPassword(id int64, password string, conns map[string]string) error {
	if password == "" {
		_, err := s.DB().Exec(`UPDATE projects SET pw_lock = '', pw_seal = '' WHERE id = ?`, id)
		return err
	}
	b, err := json.Marshal(conns)
	if err != nil {
		return err
	}
	l := newConnLock()
	seal := s.box.seal("projectpw", l.box(password).seal("projectpw:"+strconv.FormatInt(id, 10), string(b)))
	_, err = s.DB().Exec(`UPDATE projects SET pw_lock = ?, pw_seal = ? WHERE id = ?`, l.String(), seal, id)
	return err
}

// resealProjectPassword keeps a project password's sealed passwords up to
// date after the project was opened with it: those that worked replace the
// sealed ones (a connection added since, or one whose password changed).
func (s *Store) resealProjectPassword(id int64, password string, sealed, used map[string]string) error {
	changed := false
	for k, v := range used {
		if v != "" && sealed[k] != v {
			sealed[k], changed = v, true
		}
	}
	if !changed {
		return nil
	}
	return s.SetProjectPassword(id, password, sealed)
}
