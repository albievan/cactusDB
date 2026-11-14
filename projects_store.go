package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Projects link several saved connections, a chosen set of their tables and
// queries that join tables of different databases (see projects.go).
//
// Sealing: every project has its own random key. Its description, links,
// tables and queries are sealed with it (then with the store's key, like
// everything else). The key itself is split into one share per linked
// connection (XOR, all of them needed), each sealed with that connection's
// own key (or only the store's key for a connection without a password). So
// opening a project needs the password of every password-protected
// connection it links (or the superuser password), and a password change
// re-seals only that connection's share.

const (
	maxProjectLinks  = 10 // SQLite attaches at most 10 databases (staging, see projectrun.go)
	maxProjectTables = 500
	maxProjectName   = 100
	maxProjectDesc   = 1000
)

type projectLink struct {
	ConnID int64  `json:"connId"`
	Alias  string `json:"alias"`
}

type projectTable struct {
	Alias  string `json:"alias"`            // the database alias it belongs to
	Source string `json:"source"`           // its name in the source database, as listed (e.g. dbo.customers)
	Name   string `json:"name"`             // its name in the project, unique per alias
	Kind   string `json:"kind,omitempty"`   // table or view
	Schema string `json:"schema,omitempty"` // its schema, when the source has schemas
}

type projectLimits struct {
	MaxRows    int64 `json:"maxRows"`    // per table
	MaxMB      int64 `json:"maxMB"`      // staging in all
	TimeoutSec int   `json:"timeoutSec"` // per run
}

func defaultProjectLimits() projectLimits {
	return projectLimits{MaxRows: 1_000_000, MaxMB: 2048, TimeoutSec: 600}
}

// projectData is what is sealed with the project key.
type projectData struct {
	Description string         `json:"description"`
	Links       []projectLink  `json:"links"`
	Tables      []projectTable `json:"tables"`
	Limits      projectLimits  `json:"limits"`
}

// Project is a project as the API gives it.
type Project struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Created string `json:"created"`
	Updated string `json:"updated"`
	projectData
}

// ProjectSummary is a project in the list, readable without passwords.
type ProjectSummary struct {
	ID      int64   `json:"id"`
	Name    string  `json:"name"`
	ConnIDs []int64 `json:"connIds"`
	Updated string  `json:"updated"`
}

var errProjectName = errors.New("a project with this name exists already")

func (s *Store) migrateProjects() error {
	_, err := s.DB().Exec(`CREATE TABLE IF NOT EXISTS projects (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		name       TEXT NOT NULL,
		data       TEXT NOT NULL,
		pkeys      TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	-- Which project links which saved connection (plain ids, for deleting a connection).
	CREATE TABLE IF NOT EXISTS project_links (
		project_id INTEGER NOT NULL,
		conn_id    INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS project_links_conn ON project_links(conn_id);
	CREATE TABLE IF NOT EXISTS project_queries (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id INTEGER NOT NULL,
		data       TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS project_queries_project ON project_queries(project_id);
	CREATE TABLE IF NOT EXISTS project_query_versions (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		query_id   INTEGER NOT NULL,
		project_id INTEGER NOT NULL,
		version    INTEGER NOT NULL,
		data       TEXT NOT NULL,
		saved_at   TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS project_query_versions_query ON project_query_versions(query_id)`)
	return err
}

// ---- the project key ----------------------------------------------------------

// shareKey is where a share is kept: the connection id, or "d<id>" for a
// connection that was deleted (then sealed with the store's key only).
func shareKey(connID int64) string { return strconv.FormatInt(connID, 10) }

// splitKey makes one share per connection plus one sealed with the store's
// key only ("s", so a project without links has a key too), whose XOR is key.
func (s *Store) splitKey(key []byte, ids []int64, boxes map[int64]*secretBox) (string, error) {
	shares := map[string]string{}
	acc := slices.Clone(key)
	for _, id := range ids {
		box, ok := boxes[id]
		if !ok {
			return "", fmt.Errorf("connection %d isn't unlocked", id)
		}
		share := make([]byte, len(key))
		if _, err := rand.Read(share); err != nil {
			return "", err
		}
		for j := range acc {
			acc[j] ^= share[j]
		}
		shares[shareKey(id)] = s.sealData(box, "projectshare", base64.StdEncoding.EncodeToString(share))
	}
	shares["s"] = s.sealData(nil, "projectshare", base64.StdEncoding.EncodeToString(acc))
	b, _ := json.Marshal(shares)
	return s.box.seal("projectkeys", string(b)), nil
}

// joinKey opens every share (each with its connection's key from boxes) and
// returns the project key. A share of a password-protected connection
// missing from boxes gives errLockRequired.
func (s *Store) joinKey(pkeys string, boxes map[int64]*secretBox) ([]byte, error) {
	plain, err := s.box.open("projectkeys", pkeys)
	if err != nil {
		return nil, err
	}
	var shares map[string]string
	if err := json.Unmarshal([]byte(plain), &shares); err != nil {
		return nil, err
	}
	var key []byte
	for k, sealed := range shares {
		var box *secretBox
		if id, err := strconv.ParseInt(k, 10, 64); err == nil {
			box = boxes[id]
		}
		b64, err := s.openData(box, "projectshare", sealed)
		if err != nil {
			return nil, err
		}
		share, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || (key != nil && len(share) != len(key)) {
			return nil, errors.New("corrupt project key")
		}
		if key == nil {
			key = make([]byte, len(share))
		}
		for j := range key {
			key[j] ^= share[j]
		}
	}
	if len(key) != 32 {
		return nil, errors.New("corrupt project key")
	}
	return key, nil
}

func projectBox(key []byte) *secretBox {
	b, err := newSecretBox(key)
	if err != nil {
		panic(err) // a 32-byte key always makes a box
	}
	return b
}

// ---- projects -----------------------------------------------------------------

// Projects lists the projects (names only: no password needed).
func (s *Store) Projects() ([]ProjectSummary, error) {
	rows, err := s.DB().Query(`SELECT id, name, updated_at FROM projects`)
	if err != nil {
		return nil, err
	}
	out := []ProjectSummary{}
	for rows.Next() {
		var p ProjectSummary
		var name string
		if err := rows.Scan(&p.ID, &name, &p.Updated); err != nil {
			rows.Close()
			return nil, err
		}
		if p.Name, err = s.box.open("projectname", name); err != nil {
			rows.Close()
			return nil, err
		}
		p.ConnIDs = []int64{}
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	links, err := s.DB().Query(`SELECT project_id, conn_id FROM project_links ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer links.Close()
	for links.Next() {
		var pid, cid int64
		if err := links.Scan(&pid, &cid); err != nil {
			return nil, err
		}
		for i := range out {
			if out[i].ID == pid {
				out[i].ConnIDs = append(out[i].ConnIDs, cid)
			}
		}
	}
	slices.SortFunc(out, func(a, b ProjectSummary) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out, links.Err()
}

// projectLinks returns the connection ids a project links (from the plain table).
func (s *Store) projectLinks(id int64) ([]int64, error) {
	rows, err := s.DB().Query(`SELECT conn_id FROM project_links WHERE project_id = ? ORDER BY rowid`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var c int64
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		ids = append(ids, c)
	}
	return ids, rows.Err()
}

// ProjectsUsing names the projects that link a saved connection.
func (s *Store) ProjectsUsing(connID int64) ([]string, error) {
	all, err := s.Projects()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, p := range all {
		if slices.Contains(p.ConnIDs, connID) {
			names = append(names, p.Name)
		}
	}
	return names, nil
}

func (s *Store) checkProjectName(id int64, name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", errors.New("a project needs a name")
	case len(name) > maxProjectName:
		return "", fmt.Errorf("the name is longer than %d characters", maxProjectName)
	case strings.ContainsAny(name, "/\\\x00") || strings.HasPrefix(name, "."):
		return "", errors.New(`a project name can't contain / or \ or start with a dot (it names its export folder)`)
	}
	all, err := s.Projects()
	if err != nil {
		return "", err
	}
	for _, p := range all {
		if p.ID != id && strings.EqualFold(p.Name, name) {
			return "", errProjectName
		}
	}
	return name, nil
}

// CreateProject adds a project. boxes holds the key of every linked
// connection (nil for one without a password).
func (s *Store) CreateProject(name string, d projectData, boxes map[int64]*secretBox) (int64, []byte, error) {
	name, err := s.checkProjectName(0, name)
	if err != nil {
		return 0, nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return 0, nil, err
	}
	pkeys, err := s.splitKey(key, linkIDs(d.Links), boxes)
	if err != nil {
		return 0, nil, err
	}
	b, _ := json.Marshal(d)
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := s.DB().Begin()
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO projects (name, data, pkeys, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		s.box.seal("projectname", name), s.sealData(projectBox(key), "project", string(b)), pkeys, now, now)
	if err != nil {
		return 0, nil, err
	}
	id, _ := res.LastInsertId()
	if err := setProjectLinks(tx, id, d.Links); err != nil {
		return 0, nil, err
	}
	return id, key, tx.Commit()
}

func linkIDs(links []projectLink) []int64 {
	ids := make([]int64, 0, len(links))
	for _, l := range links {
		if !slices.Contains(ids, l.ConnID) {
			ids = append(ids, l.ConnID)
		}
	}
	return ids
}

func setProjectLinks(tx *sql.Tx, id int64, links []projectLink) error {
	if _, err := tx.Exec(`DELETE FROM project_links WHERE project_id = ?`, id); err != nil {
		return err
	}
	for _, c := range linkIDs(links) {
		if _, err := tx.Exec(`INSERT INTO project_links (project_id, conn_id) VALUES (?, ?)`, id, c); err != nil {
			return err
		}
	}
	return nil
}

// ProjectKey opens a project's key with the keys of its connections.
func (s *Store) ProjectKey(id int64, boxes map[int64]*secretBox) ([]byte, error) {
	var pkeys string
	if err := s.DB().QueryRow(`SELECT pkeys FROM projects WHERE id = ?`, id).Scan(&pkeys); err != nil {
		return nil, err
	}
	return s.joinKey(pkeys, boxes)
}

// GetProject opens a project with its key.
func (s *Store) GetProject(id int64, key []byte) (Project, error) {
	var p Project
	var name, data string
	err := s.DB().QueryRow(`SELECT id, name, data, created_at, updated_at FROM projects WHERE id = ?`, id).
		Scan(&p.ID, &name, &data, &p.Created, &p.Updated)
	if err != nil {
		return p, err
	}
	if p.Name, err = s.box.open("projectname", name); err != nil {
		return p, err
	}
	plain, err := s.openData(projectBox(key), "project", data)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal([]byte(plain), &p.projectData); err != nil {
		return p, err
	}
	if p.Limits == (projectLimits{}) {
		p.Limits = defaultProjectLimits()
	}
	return p, nil
}

// SaveProject changes a project opened with key. When its links change, the
// key is split again over the new links, so boxes must hold the key of every
// connection it links now.
func (s *Store) SaveProject(id int64, key []byte, name string, d projectData, boxes map[int64]*secretBox) error {
	name, err := s.checkProjectName(id, name)
	if err != nil {
		return err
	}
	old, err := s.projectLinks(id)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(d)
	tx, err := s.DB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE projects SET name = ?, data = ?, updated_at = ? WHERE id = ?`,
		s.box.seal("projectname", name), s.sealData(projectBox(key), "project", string(b)), time.Now().UTC().Format(time.RFC3339), id); err != nil {
		return err
	}
	if ids := linkIDs(d.Links); !slices.Equal(ids, old) {
		pkeys, err := s.splitKey(key, ids, boxes)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE projects SET pkeys = ? WHERE id = ?`, pkeys, id); err != nil {
			return err
		}
		if err := setProjectLinks(tx, id, d.Links); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteProject deletes a project and its queries.
func (s *Store) DeleteProject(id int64) error {
	tx, err := s.DB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM project_query_versions WHERE project_id = ?`, `DELETE FROM project_queries WHERE project_id = ?`,
		`DELETE FROM project_links WHERE project_id = ?`, `DELETE FROM projects WHERE id = ?`} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// reshareProjects re-seals a connection's share of every project that links
// it, from oldBox to newBox (its password changed, was set or removed).
// newBox nil with forget: the connection is being deleted, so its share is
// kept sealed with the store's key only, under "d<id>", and the project
// stays readable with its other connections' passwords.
func (s *Store) reshareProjects(tx *sql.Tx, connID int64, oldBox, newBox *secretBox, forget bool) error {
	rows, err := tx.Query(`SELECT p.id, p.pkeys FROM projects p JOIN project_links l ON l.project_id = p.id WHERE l.conn_id = ?`, connID)
	if err != nil {
		return err
	}
	type row struct {
		id    int64
		pkeys string
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.pkeys); err != nil {
			rows.Close()
			return err
		}
		all = append(all, r)
	}
	rows.Close()
	for _, r := range all {
		plain, err := s.box.open("projectkeys", r.pkeys)
		if err != nil {
			return err
		}
		var shares map[string]string
		if err := json.Unmarshal([]byte(plain), &shares); err != nil {
			return err
		}
		k := shareKey(connID)
		sealed, ok := shares[k]
		if !ok {
			continue
		}
		share, err := s.openData(oldBox, "projectshare", sealed)
		if err != nil {
			return fmt.Errorf("project %d: %w", r.id, err)
		}
		if forget {
			delete(shares, k)
			shares["d"+k] = s.sealData(nil, "projectshare", share)
		} else {
			shares[k] = s.sealData(newBox, "projectshare", share)
		}
		b, _ := json.Marshal(shares)
		if _, err := tx.Exec(`UPDATE projects SET pkeys = ? WHERE id = ?`, s.box.seal("projectkeys", string(b)), r.id); err != nil {
			return err
		}
		if forget {
			if _, err := tx.Exec(`DELETE FROM project_links WHERE project_id = ? AND conn_id = ?`, r.id, connID); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---- names ----------------------------------------------------------------------

var aliasRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,29}$`)

// sqliteKeywords are SQLite's keywords (sqlite.org/lang_keywords.html) and
// the names of its own schemas: none can be a database alias.
var sqliteKeywords = strings.Fields(`ABORT ACTION ADD AFTER ALL ALTER ALWAYS ANALYZE AND AS ASC ATTACH
	AUTOINCREMENT BEFORE BEGIN BETWEEN BY CASCADE CASE CAST CHECK COLLATE COLUMN COMMIT CONFLICT
	CONSTRAINT CREATE CROSS CURRENT CURRENT_DATE CURRENT_TIME CURRENT_TIMESTAMP DATABASE DEFAULT
	DEFERRABLE DEFERRED DELETE DESC DETACH DISTINCT DO DROP EACH ELSE END ESCAPE EXCEPT EXCLUDE
	EXCLUSIVE EXISTS EXPLAIN FAIL FILTER FIRST FOLLOWING FOR FOREIGN FROM FULL GENERATED GLOB GROUP
	GROUPS HAVING IF IGNORE IMMEDIATE IN INDEX INDEXED INITIALLY INNER INSERT INSTEAD INTERSECT INTO
	IS ISNULL JOIN KEY LAST LEFT LIKE LIMIT MATCH MATERIALIZED NATURAL NO NOT NOTHING NOTNULL NULL
	NULLS OF OFFSET ON OR ORDER OTHERS OUTER OVER PARTITION PLAN PRAGMA PRECEDING PRIMARY QUERY RAISE
	RANGE RECURSIVE REFERENCES REGEXP REINDEX RELEASE RENAME REPLACE RESTRICT RETURNING RIGHT
	ROLLBACK ROW ROWS SAVEPOINT SELECT SET TABLE TEMP TEMPORARY THEN TIES TO TRANSACTION TRIGGER
	UNBOUNDED UNION UNIQUE UPDATE USING VACUUM VALUES VIEW VIRTUAL WHEN WHERE WINDOW WITH WITHOUT
	MAIN TEMP SQLITE_MASTER SQLITE_SCHEMA SQLITE_TEMP_MASTER`)

// checkAlias says why alias can't name a linked database (nil: it can).
func checkAlias(alias string) error {
	if !aliasRe.MatchString(alias) {
		return fmt.Errorf("the alias %q must be 1 to 30 letters, digits or _, not starting with a digit", alias)
	}
	if slices.Contains(sqliteKeywords, strings.ToUpper(alias)) {
		return fmt.Errorf("the alias %q is an SQL keyword: choose another", alias)
	}
	return nil
}

// defaultAlias makes an alias from a connection name: its first letters,
// lower-case, made valid and unique among taken.
func defaultAlias(name string, taken []string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9' && b.Len() > 0:
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "_"):
			b.WriteByte('_')
		}
		if b.Len() >= 12 {
			break
		}
	}
	a := strings.Trim(b.String(), "_")
	if a == "" || checkAlias(a) != nil {
		a = "db" + a
	}
	if len(a) > 12 {
		a = strings.Trim(a[:12], "_")
	}
	base := a
	for i := 2; slices.ContainsFunc(taken, func(t string) bool { return strings.EqualFold(t, a) }) || checkAlias(a) != nil; i++ {
		a = base + strconv.Itoa(i)
	}
	return a
}

// checkProjectData checks links and tables: aliases valid and unique, every
// table under a linked alias with a unique project name per alias.
func checkProjectData(d *projectData) error {
	if len(d.Links) > maxProjectLinks {
		return fmt.Errorf("a project links at most %d databases (the most SQLite can attach at once)", maxProjectLinks)
	}
	if len(d.Tables) > maxProjectTables {
		return fmt.Errorf("a project holds at most %d tables", maxProjectTables)
	}
	if len(d.Description) > maxProjectDesc {
		return fmt.Errorf("the description is longer than %d characters", maxProjectDesc)
	}
	seen := map[string]bool{}
	conns := map[int64]string{}
	for _, l := range d.Links {
		if err := checkAlias(l.Alias); err != nil {
			return err
		}
		if seen[strings.ToLower(l.Alias)] {
			return fmt.Errorf("the alias %q is used twice", l.Alias)
		}
		seen[strings.ToLower(l.Alias)] = true
		if a, ok := conns[l.ConnID]; ok {
			return fmt.Errorf("that connection is linked already, as %q", a)
		}
		conns[l.ConnID] = l.Alias
	}
	names := map[string]bool{}
	for i, t := range d.Tables {
		if !seen[strings.ToLower(t.Alias)] {
			return fmt.Errorf("the table %s belongs to %q, which isn't linked", t.Source, t.Alias)
		}
		t.Name = strings.TrimSpace(t.Name)
		if t.Name == "" || strings.ContainsAny(t.Name, "\".\x00") || len(t.Name) > 128 {
			return fmt.Errorf("%q isn't a usable table name in the project (no dots or double quotes)", t.Name)
		}
		k := strings.ToLower(t.Alias + "." + t.Name)
		if names[k] {
			return fmt.Errorf("%s.%s is in the project twice: give one of them another project name", t.Alias, t.Name)
		}
		names[k] = true
		d.Tables[i] = t
	}
	if d.Limits == (projectLimits{}) {
		d.Limits = defaultProjectLimits()
	}
	if d.Limits.MaxRows < 1 || d.Limits.MaxMB < 1 || d.Limits.TimeoutSec < 1 {
		return errors.New("the limits must be at least 1")
	}
	return nil
}
