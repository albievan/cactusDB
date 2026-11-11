package main

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// --remotelogin: nothing of cactusdb can be seen or used before signing in.
//
//   - The first user is admin, password admin; signing in with it asks for a
//     new password straight away.
//   - Every other user is an e-mail address. After the right password,
//     cactusdb e-mails an 8-digit code, which the next screen asks for (and
//     can send again). admin has no address, so no code.
//   - Any signed-in user can add users: the new user is e-mailed a link,
//     valid for an hour, to set a password.
//   - The sign-in page can e-mail a password reset link (valid for an hour),
//     and anyone can request access with their address and a password. Any
//     signed-in user can approve the request; the person is then e-mailed.
//
// Passwords are kept as Argon2id hashes. Links, codes and sign-ins are random
// tokens of which only a SHA-256 is stored, so the database never holds one
// that works. Of a user only the user name (the e-mail address) is stored in
// plain text: the name, password hash, status, reason, who added them and
// dates are one record encrypted with the key of the saved connections, as
// are the IP addresses of sign-ins. A signed-in browser holds its token in an HttpOnly cookie.
// Answers to the page use status 200 with the status in a header (see
// statusInHeader): a firewall on the way may block a client after a 401 or 403.

const (
	adminUser       = "admin"
	authCookie      = "cactusdb_auth"
	pendingCookie   = "cactusdb_login"
	sessionIdle     = 12 * time.Hour   // a sign-in ends after this long unused
	linkLife        = time.Hour        // invitation and password reset links
	codeLife        = 10 * time.Minute // a sign-in code
	pendingLife     = 15 * time.Minute // between the password and the code
	codeResendAfter = 30 * time.Second
	maxCodeTries    = 5
	minPasswordLen  = 8
)

var errNotSignedIn = errors.New("sign in first")

// ---- store -------------------------------------------------------------------

// migrateUsers creates the tables of --remotelogin (since 1.48.0). Since
// 1.49.0 a user's row holds only the user name (the e-mail address) in plain
// text: everything else is one encrypted record in data. It runs once the
// key is loaded, and moves the users of 1.48 into the encrypted form.
func (s *Store) migrateUsers() error {
	var cols int
	s.DB().QueryRow(`SELECT COUNT(*) FROM pragma_table_info('users') WHERE name = 'status'`).Scan(&cols)
	if cols > 0 {
		if err := s.encryptOldUsers(); err != nil {
			return fmt.Errorf("encrypting the users: %w", err)
		}
	}
	_, err := s.DB().Exec(`CREATE TABLE IF NOT EXISTS users (
		id       INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT NOT NULL UNIQUE COLLATE NOCASE, -- an e-mail address, or the first user
		data     TEXT NOT NULL                        -- the rest of the user, encrypted (userData)
	);
	-- Sign-ins, pending sign-ins and e-mailed links, by the SHA-256 of their token.
	CREATE TABLE IF NOT EXISTS auth_tokens (
		hash       TEXT PRIMARY KEY,
		user_id    INTEGER NOT NULL,
		purpose    TEXT NOT NULL, -- session | login-change | login-code | invite | reset
		code       TEXT NOT NULL DEFAULT '', -- login-code: SHA-256 of the code
		tries      INTEGER NOT NULL DEFAULT 0,
		sent_at    TEXT NOT NULL DEFAULT '',
		expires_at TEXT NOT NULL,
		ip         TEXT NOT NULL DEFAULT ''  -- encrypted
	);
	CREATE INDEX IF NOT EXISTS auth_tokens_user ON auth_tokens(user_id)`)
	return err
}

// encryptOldUsers rewrites the plain-text users table of 1.48 as the
// encrypted one, keeping the ids, and encrypts the tokens' IP addresses.
func (s *Store) encryptOldUsers() error {
	rows, err := s.DB().Query(`SELECT id, username, name, pw, status, must_change, reason, added_by, created_at, last_login FROM users`)
	if err != nil {
		return err
	}
	var old []appUser
	for rows.Next() {
		var u appUser
		if err := rows.Scan(&u.ID, &u.Username, &u.Name, &u.pw, &u.Status, &u.MustChange, &u.Reason, &u.AddedBy, &u.Created, &u.LastLogin); err != nil {
			rows.Close()
			return err
		}
		old = append(old, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	tx, err := s.DB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DROP TABLE users`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE users (
		id       INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT NOT NULL UNIQUE COLLATE NOCASE,
		data     TEXT NOT NULL
	)`); err != nil {
		return err
	}
	for _, u := range old {
		if _, err := tx.Exec(`INSERT INTO users (id, username, data) VALUES (?, ?, ?)`, u.ID, u.Username, s.sealUser(u)); err != nil {
			return err
		}
	}
	ips, err := tx.Query(`SELECT hash, ip FROM auth_tokens WHERE ip != '' AND ip NOT LIKE 'enc:v1:%'`)
	if err == nil { // no auth_tokens table: nothing to encrypt
		type tokIP struct{ hash, ip string }
		var plain []tokIP
		for ips.Next() {
			var t tokIP
			if ips.Scan(&t.hash, &t.ip) == nil {
				plain = append(plain, t)
			}
		}
		ips.Close()
		for _, t := range plain {
			if _, err := tx.Exec(`UPDATE auth_tokens SET ip = ? WHERE hash = ?`, s.box.seal("token.ip", t.ip), t.hash); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = s.DB().Exec(`VACUUM`) // no plain-text copy left in free pages
	return err
}

type appUser struct {
	ID         int64  `json:"id"`
	Username   string `json:"username"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	MustChange bool   `json:"mustChange,omitempty"`
	Reason     string `json:"reason,omitempty"`
	AddedBy    string `json:"addedBy,omitempty"`
	Created    string `json:"created"`
	LastLogin  string `json:"lastLogin,omitempty"`
	pw         string
}

// userData is what users.data holds, encrypted.
type userData struct {
	Name       string `json:"name,omitempty"`
	Pw         string `json:"pw,omitempty"` // Argon2id hash; "" = none set yet
	Status     string `json:"status"`       // active | invited | requested
	MustChange bool   `json:"mustChange,omitempty"`
	Reason     string `json:"reason,omitempty"` // why access was requested
	AddedBy    string `json:"addedBy,omitempty"`
	Created    string `json:"created"`
	LastLogin  string `json:"lastLogin,omitempty"`
}

func (s *Store) sealUser(u appUser) string {
	b, _ := json.Marshal(userData{u.Name, u.pw, u.Status, u.MustChange, u.Reason, u.AddedBy, u.Created, u.LastLogin})
	return s.box.seal("user", string(b))
}

// email reports whether u signs in with an e-mail address (and a code).
func (u appUser) email() bool { return !strings.EqualFold(u.Username, adminUser) }

func (s *Store) scanUser(sc interface{ Scan(...any) error }) (appUser, error) {
	var u appUser
	var sealed string
	if err := sc.Scan(&u.ID, &u.Username, &sealed); err != nil {
		return u, err
	}
	plain, err := s.box.open("user", sealed)
	if err != nil {
		return u, fmt.Errorf("user %s: %w", u.Username, err)
	}
	var d userData
	if err := json.Unmarshal([]byte(plain), &d); err != nil {
		return u, fmt.Errorf("user %s: %w", u.Username, err)
	}
	u.Name, u.pw, u.Status, u.MustChange, u.Reason, u.AddedBy, u.Created, u.LastLogin =
		d.Name, d.Pw, d.Status, d.MustChange, d.Reason, d.AddedBy, d.Created, d.LastLogin
	return u, nil
}

func (s *Store) userByName(name string) (appUser, error) {
	return s.scanUser(s.DB().QueryRow(`SELECT id, username, data FROM users WHERE username = ?`, strings.TrimSpace(name)))
}

func (s *Store) userByID(id int64) (appUser, error) {
	return s.scanUser(s.DB().QueryRow(`SELECT id, username, data FROM users WHERE id = ?`, id))
}

// listUsers: requests first, then invitations, then the active users, each
// by name.
func (s *Store) listUsers() ([]appUser, error) {
	rows, err := s.DB().Query(`SELECT id, username, data FROM users ORDER BY username COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []appUser{}
	for rows.Next() {
		u, err := s.scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Status != "active" && out[j].Status == "active" })
	return out, rows.Err()
}

// countUsers counts the users of a status, leaving out one id (0: none).
func (s *Store) countUsers(status string, except int64) int {
	list, _ := s.listUsers()
	n := 0
	for _, u := range list {
		if u.Status == status && u.ID != except {
			n++
		}
	}
	return n
}

// addUser stores a new user; added is false when the user name is taken.
func (s *Store) addUser(u appUser) (appUser, bool, error) {
	if u.Created == "" {
		u.Created = nowText()
	}
	res, err := s.DB().Exec(`INSERT INTO users (username, data) VALUES (?, ?) ON CONFLICT(username) DO NOTHING`, u.Username, s.sealUser(u))
	if err != nil {
		return u, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return u, false, nil
	}
	u.ID, _ = res.LastInsertId()
	return u, true, nil
}

// updateUser changes the stored user id with change, one change at a time.
func (s *Store) updateUser(id int64, change func(*appUser)) error {
	s.usersMu.Lock()
	defer s.usersMu.Unlock()
	u, err := s.userByID(id)
	if err != nil {
		return err
	}
	change(&u)
	_, err = s.DB().Exec(`UPDATE users SET data = ? WHERE id = ?`, s.sealUser(u), id)
	return err
}

func (s *Store) deleteUser(id int64) {
	s.DB().Exec(`DELETE FROM users WHERE id = ?`, id)
	s.dropTokens(id)
}

// ensureAdmin adds admin/admin to an empty users table.
func (s *Store) ensureAdmin() (bool, error) {
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil || n > 0 {
		return false, err
	}
	_, added, err := s.addUser(appUser{Username: adminUser, pw: hashPassword(adminUser), Status: "active", MustChange: true})
	return added, err
}

// resetAdmin sets admin's password back to admin (to be changed at the next
// sign-in), adding admin if it is missing: cactusdb --resetadmin.
func (s *Store) resetAdmin() error {
	u, err := s.userByName(adminUser)
	if errors.Is(err, sql.ErrNoRows) {
		_, _, err = s.addUser(appUser{Username: adminUser, pw: hashPassword(adminUser), Status: "active", MustChange: true})
		return err
	}
	if err != nil {
		return err
	}
	if err := s.updateUser(u.ID, func(u *appUser) {
		u.pw, u.Status, u.MustChange = hashPassword(adminUser), "active", true
	}); err != nil {
		return err
	}
	s.dropTokens(u.ID)
	return nil
}

func nowText() string { return time.Now().UTC().Format(time.RFC3339) }

// ---- passwords, tokens, codes ------------------------------------------------

// hashPassword is "argon2id:v1:<time>:<memory>:<threads>:<salt>:<hash>".
func hashPassword(pw string) string {
	l := newConnLock()
	sum := argon2.IDKey([]byte(pw), l.salt, l.time, l.memory, l.threads, 32)
	return l.String() + ":" + base64.StdEncoding.EncodeToString(sum)
}

// dummyHash makes a sign-in with an unknown user as slow as with a known one.
var dummyHash = sync.OnceValue(func() string { return hashPassword(newID()) })

func checkPassword(hash, pw string) bool {
	i := strings.LastIndexByte(hash, ':')
	if i < 0 {
		return false
	}
	l, err := parseConnLock(hash[:i])
	want, err2 := base64.StdEncoding.DecodeString(hash[i+1:])
	if err != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), l.salt, l.time, l.memory, l.threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// checkNewPassword says what is wrong with a new password for u.
func checkNewPassword(username, pw string) error {
	switch {
	case len([]rune(pw)) < minPasswordLen:
		return fmt.Errorf("the password must be at least %d characters", minPasswordLen)
	case strings.EqualFold(pw, adminUser) || strings.EqualFold(pw, username):
		return errors.New("choose a password that isn't the user name or admin")
	case len(pw) > 200:
		return errors.New("the password is too long")
	}
	return nil
}

func tokenHash(t string) string {
	sum := sha256.Sum256([]byte("cactusdb token\x00" + t))
	return hex.EncodeToString(sum[:])
}

func newToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// newCode is an 8-digit sign-in code.
func newCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(100_000_000))
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%08d", n.Int64())
}

type authToken struct {
	userID  int64
	purpose string
	code    string
	tries   int
	sentAt  time.Time
	expires time.Time
}

// addToken stores a new token for u and returns it.
func (s *Store) addToken(userID int64, purpose string, life time.Duration, ip string) (string, error) {
	t := newToken()
	_, err := s.DB().Exec(`INSERT INTO auth_tokens (hash, user_id, purpose, expires_at, ip) VALUES (?, ?, ?, ?, ?)`,
		tokenHash(t), userID, purpose, time.Now().Add(life).UTC().Format(time.RFC3339), s.box.seal("token.ip", ip))
	return t, err
}

// token looks up a token that hasn't expired, of one of the purposes.
func (s *Store) token(t string, purposes ...string) (authToken, error) {
	if t == "" {
		return authToken{}, sql.ErrNoRows
	}
	var a authToken
	var sent, exp string
	err := s.DB().QueryRow(`SELECT user_id, purpose, code, tries, sent_at, expires_at FROM auth_tokens WHERE hash = ?`, tokenHash(t)).
		Scan(&a.userID, &a.purpose, &a.code, &a.tries, &sent, &exp)
	if err != nil {
		return a, err
	}
	a.expires, _ = time.Parse(time.RFC3339, exp)
	a.sentAt, _ = time.Parse(time.RFC3339, sent)
	if time.Now().After(a.expires) {
		s.DB().Exec(`DELETE FROM auth_tokens WHERE hash = ?`, tokenHash(t))
		return a, sql.ErrNoRows
	}
	for _, p := range purposes {
		if a.purpose == p {
			return a, nil
		}
	}
	return a, sql.ErrNoRows
}

func (s *Store) dropToken(t string) {
	s.DB().Exec(`DELETE FROM auth_tokens WHERE hash = ?`, tokenHash(t))
}

// dropTokens removes a user's tokens of the purposes (all when none given).
func (s *Store) dropTokens(userID int64, purposes ...string) {
	if len(purposes) == 0 {
		s.DB().Exec(`DELETE FROM auth_tokens WHERE user_id = ?`, userID)
		return
	}
	for _, p := range purposes {
		s.DB().Exec(`DELETE FROM auth_tokens WHERE user_id = ? AND purpose = ?`, userID, p)
	}
}

// pruneTokens forgets the expired tokens.
func (s *Store) pruneTokens() {
	s.DB().Exec(`DELETE FROM auth_tokens WHERE expires_at < ?`, nowText())
}

// ---- throttle ----------------------------------------------------------------

// authThrottle slows down guessing passwords: after 5 wrong ones for a user
// name (or from an IP), each further try waits 2 s, 4 s, … up to 5 minutes.
type authThrottle struct {
	mu    sync.Mutex
	fails map[string]int
	until map[string]time.Time
}

func (t *authThrottle) wait(keys ...string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	var d time.Duration
	for _, k := range keys {
		d = max(d, time.Until(t.until[k]))
	}
	return d
}

func (t *authThrottle) failed(keys ...string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fails == nil {
		t.fails, t.until = map[string]int{}, map[string]time.Time{}
	}
	for _, k := range keys {
		t.fails[k]++
		if n := t.fails[k]; n >= 5 {
			t.until[k] = time.Now().Add(min(time.Duration(1<<min(n-4, 9))*time.Second, 5*time.Minute))
		}
	}
}

func (t *authThrottle) ok(keys ...string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, k := range keys {
		delete(t.fails, k)
		delete(t.until, k)
	}
}

// ---- the gate ----------------------------------------------------------------

type userKey struct{}

// currentUser is the signed-in user of r (none without --remotelogin).
func currentUser(r *http.Request) (appUser, bool) {
	u, ok := r.Context().Value(userKey{}).(appUser)
	return u, ok
}

// openPaths can be fetched before signing in: the sign-in page and what it
// loads, and its API.
func openPath(p string) bool {
	return p == "/login" || p == "/cactusdb.svg" || strings.HasPrefix(p, "/vendor/") || strings.HasPrefix(p, "/api/auth/")
}

// requireLogin lets only signed-in browsers past, with --remotelogin. The
// page itself redirects to /login; the API answers "sign in first".
func (s *server) requireLogin(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.remoteLogin {
			h.ServeHTTP(w, r)
			return
		}
		if u, ok := s.sessionUser(w, r); ok {
			if l, ok := r.Context().Value(reqLogKey{}).(*reqLog); ok {
				l.user = u.Username
			}
			h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, u)))
			return
		}
		if openPath(r.URL.Path) {
			h.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		writeOpaqueErr(w, 401, map[string]string{"signin": "true"}, errNotSignedIn.Error())
	})
}

// sessionUser is the user of the browser's sign-in cookie, if it is valid;
// using it keeps it valid for sessionIdle more.
func (s *server) sessionUser(w http.ResponseWriter, r *http.Request) (appUser, bool) {
	c, err := r.Cookie(authCookie)
	if err != nil {
		return appUser{}, false
	}
	a, err := s.store.token(c.Value, "session")
	if err != nil {
		return appUser{}, false
	}
	u, err := s.store.userByID(a.userID)
	if err != nil || u.Status != "active" {
		return appUser{}, false
	}
	if time.Until(a.expires) < sessionIdle-time.Minute {
		s.store.DB().Exec(`UPDATE auth_tokens SET expires_at = ? WHERE hash = ?`, time.Now().Add(sessionIdle).UTC().Format(time.RFC3339), tokenHash(c.Value))
	}
	return u, true
}

func setCookie(w http.ResponseWriter, r *http.Request, name, value string, life time.Duration) {
	c := &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil}
	if life < 0 {
		c.MaxAge = -1
	} else if life > 0 {
		c.MaxAge = int(life / time.Second)
	}
	http.SetCookie(w, c)
}

// signIn makes the browser signed in as u.
func (s *server) signIn(w http.ResponseWriter, r *http.Request, u appUser) error {
	t, err := s.store.addToken(u.ID, "session", sessionIdle, clientIP(r))
	if err != nil {
		return err
	}
	s.store.updateUser(u.ID, func(u *appUser) { u.LastLogin = nowText() })
	setCookie(w, r, authCookie, t, 0) // until the browser closes, or sessionIdle unused
	setCookie(w, r, pendingCookie, "", -1)
	log.Printf("%s %s signed in", clientIP(r), u.Username)
	s.eventAs(r, u.Username, "signin", false, "signed in")
	return nil
}

// ---- e-mails -----------------------------------------------------------------

// link is the address of a page of the sign-in screen for e-mails.
func (s *server) link(fragment string) (string, error) {
	st, ok, err := s.store.loadSMTP()
	if err != nil {
		return "", err
	}
	if !ok || !st.Enabled {
		return "", errNoMailServer
	}
	return st.LinkBase + "/login#" + fragment, nil
}

// mailSetPassword e-mails u a link to set a password: an invitation (for a
// new user) or a reset.
func (s *server) mailSetPassword(r *http.Request, u appUser, purpose, by string) error {
	t, err := s.store.addToken(u.ID, purpose, linkLife, clientIP(r))
	if err != nil {
		return err
	}
	link, err := s.link("set=" + t)
	if err != nil {
		s.store.dropToken(t)
		return err
	}
	subject, body := "Set your cactusdb password", ""
	if purpose == "invite" {
		subject = "You have been given access to cactusdb"
		body = fmt.Sprintf("Hello%s,\n\n%s gave you access to cactusdb. Set your password here:\n\n%s\n\n"+
			"The link works for one hour. Then sign in with this e-mail address and your password;\n"+
			"cactusdb e-mails you a code each time you sign in.\n", greetName(u.Name), by, link)
	} else {
		body = fmt.Sprintf("Hello%s,\n\nSomeone (hopefully you) asked to reset your cactusdb password. Set a new one here:\n\n%s\n\n"+
			"The link works for one hour. If you didn't ask for it, ignore this e-mail: your password stays as it is.\n", greetName(u.Name), link)
	}
	if err := s.sendMail(r, u.Username, subject, body); err != nil {
		s.store.dropToken(t)
		return err
	}
	return nil
}

func greetName(n string) string {
	if n == "" {
		return ""
	}
	return " " + n
}

// sendCode e-mails a new sign-in code for the pending sign-in t.
func (s *server) sendCode(r *http.Request, t string, u appUser) error {
	code := newCode()
	if _, err := s.store.DB().Exec(`UPDATE auth_tokens SET purpose = 'login-code', code = ?, tries = 0, sent_at = ?, expires_at = ? WHERE hash = ?`,
		tokenHash(code), nowText(), time.Now().Add(codeLife).UTC().Format(time.RFC3339), tokenHash(t)); err != nil {
		return err
	}
	return s.sendMail(r, u.Username, "Your cactusdb sign-in code: "+code,
		fmt.Sprintf("Your cactusdb sign-in code is\n\n    %s\n\nIt works for %d minutes, once. Someone signed in with your password from %s;\n"+
			"if it wasn't you, change your password.\n", code, int(codeLife/time.Minute), clientIP(r)))
}

// ---- the sign-in API ---------------------------------------------------------

func (s *server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"remoteLogin": s.remoteLogin, "version": version}
	if u, ok := s.sessionUser(w, r); ok && s.remoteLogin {
		out["user"] = map[string]any{"username": u.Username, "name": u.Name, "email": u.email()}
		out["requests"] = s.store.countUsers("requested", 0)
	}
	writeJSON(w, 200, out)
}

func (s *server) needRemoteLogin(w http.ResponseWriter) bool {
	if !s.remoteLogin {
		writeErr(w, 400, errors.New("cactusdb wasn't started with --remotelogin"))
		return false
	}
	return true
}

// handleLogin checks the user name and password. Next: a new password (admin
// with admin), a code (e-mail users), or signed in.
func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.needRemoteLogin(w) {
		return
	}
	var req struct{ Username, Password string }
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	name, ip := strings.TrimSpace(req.Username), clientIP(r)
	keys := []string{"u:" + strings.ToLower(name), "ip:" + ip}
	if d := s.authThrottle.wait(keys...); d > 0 {
		s.eventAs(r, name, "signin-failed", true, "sign-in refused: too many wrong passwords, try again in %s", d.Round(time.Second))
		writeErr(w, 429, fmt.Errorf("too many wrong passwords: try again in %s", d.Round(time.Second)))
		return
	}
	u, err := s.store.userByName(name)
	hash := u.pw
	if err != nil || hash == "" {
		hash = dummyHash()
	}
	if !checkPassword(hash, req.Password) || err != nil || u.pw == "" {
		s.authThrottle.failed(keys...)
		log.Printf("%s sign-in as %q failed: wrong user name or password", ip, name)
		s.eventAs(r, name, "signin-failed", true, "sign-in failed: wrong user name or password")
		writeErr(w, 400, errors.New("wrong user name or password"))
		return
	}
	s.authThrottle.ok(keys...)
	if u.Status == "requested" {
		s.eventAs(r, u.Username, "signin-failed", true, "sign-in refused: the request for access isn't approved yet")
		writeErr(w, 400, errors.New("your request for access hasn't been approved yet: you'll get an e-mail when it is"))
		return
	}
	s.store.pruneTokens()
	s.store.dropTokens(u.ID, "login-change", "login-code")
	if u.MustChange {
		t, err := s.store.addToken(u.ID, "login-change", pendingLife, ip)
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		setCookie(w, r, pendingCookie, t, pendingLife)
		writeJSON(w, 200, map[string]any{"next": "change", "username": u.Username})
		return
	}
	if u.email() {
		t, err := s.store.addToken(u.ID, "login-code", pendingLife, ip)
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		if err := s.sendCode(r, t, u); err != nil {
			s.store.dropToken(t)
			log.Printf("%s sign-in code for %s couldn't be sent: %v", ip, u.Username, err)
			writeErr(w, 400, fmt.Errorf("cactusdb couldn't e-mail your sign-in code (%v). Ask someone to check ☰ → E-mail server", err))
			return
		}
		setCookie(w, r, pendingCookie, t, pendingLife)
		writeJSON(w, 200, map[string]any{"next": "code", "username": u.Username})
		return
	}
	if err := s.signIn(w, r, u); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"next": "done"})
}

// pending is the sign-in waiting for a new password or a code.
func (s *server) pending(r *http.Request, purposes ...string) (string, authToken, appUser, error) {
	c, err := r.Cookie(pendingCookie)
	if err != nil {
		return "", authToken{}, appUser{}, errors.New("the sign-in has expired: sign in again")
	}
	a, err := s.store.token(c.Value, purposes...)
	if err != nil {
		return "", authToken{}, appUser{}, errors.New("the sign-in has expired: sign in again")
	}
	u, err := s.store.userByID(a.userID)
	if err != nil {
		return "", authToken{}, appUser{}, errors.New("the sign-in has expired: sign in again")
	}
	return c.Value, a, u, nil
}

// handleLoginPassword is the new password of a sign-in that must change it
// (admin with admin).
func (s *server) handleLoginPassword(w http.ResponseWriter, r *http.Request) {
	if !s.needRemoteLogin(w) {
		return
	}
	var req struct{ Password string }
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	t, _, u, err := s.pending(r, "login-change")
	if err != nil {
		writeOpaqueErr(w, 400, map[string]string{"restart": "true"}, err.Error())
		return
	}
	if err := checkNewPassword(u.Username, req.Password); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.store.updateUser(u.ID, func(u *appUser) { u.pw, u.MustChange = hashPassword(req.Password), false }); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.store.dropTokens(u.ID, "session", "reset", "invite")
	log.Printf("%s %s changed the password at sign-in", clientIP(r), u.Username)
	s.eventAs(r, u.Username, "password", false, "chose a new password at sign-in")
	if u.email() {
		if err := s.sendCode(r, t, u); err != nil {
			writeErr(w, 400, fmt.Errorf("the password is changed, but cactusdb couldn't e-mail your sign-in code: %v", err))
			return
		}
		writeJSON(w, 200, map[string]any{"next": "code", "username": u.Username})
		return
	}
	s.store.dropToken(t)
	if err := s.signIn(w, r, u); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"next": "done"})
}

// handleLoginCode checks the e-mailed code.
func (s *server) handleLoginCode(w http.ResponseWriter, r *http.Request) {
	if !s.needRemoteLogin(w) {
		return
	}
	var req struct{ Code string }
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	t, a, u, err := s.pending(r, "login-code")
	if err != nil {
		writeOpaqueErr(w, 400, map[string]string{"restart": "true"}, err.Error())
		return
	}
	code := strings.Map(func(c rune) rune {
		if c >= '0' && c <= '9' {
			return c
		}
		return -1
	}, req.Code)
	if subtle.ConstantTimeCompare([]byte(tokenHash(code)), []byte(a.code)) != 1 || time.Since(a.sentAt) > codeLife {
		a.tries++
		if a.tries >= maxCodeTries {
			s.store.dropToken(t)
			log.Printf("%s %s: %d wrong sign-in codes, sign-in cancelled", clientIP(r), u.Username, a.tries)
			s.eventAs(r, u.Username, "signin-failed", true, "%d wrong sign-in codes: the sign-in was cancelled", a.tries)
			writeOpaqueErr(w, 400, map[string]string{"restart": "true"}, "too many wrong codes: sign in again")
			return
		}
		s.store.DB().Exec(`UPDATE auth_tokens SET tries = ? WHERE hash = ?`, a.tries, tokenHash(t))
		msg := "that isn't the code: check the newest e-mail"
		if time.Since(a.sentAt) > codeLife {
			msg = "the code has expired: send a new one"
		}
		s.eventAs(r, u.Username, "signin-failed", true, "wrong sign-in code (%d tries left)", maxCodeTries-a.tries)
		writeErr(w, 400, fmt.Errorf("%s (%d tries left)", msg, maxCodeTries-a.tries))
		return
	}
	s.store.dropToken(t)
	if err := s.signIn(w, r, u); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"next": "done"})
}

// handleLoginResend e-mails a new code (the old one stops working).
func (s *server) handleLoginResend(w http.ResponseWriter, r *http.Request) {
	if !s.needRemoteLogin(w) {
		return
	}
	t, a, u, err := s.pending(r, "login-code")
	if err != nil {
		writeOpaqueErr(w, 400, map[string]string{"restart": "true"}, err.Error())
		return
	}
	if d := codeResendAfter - time.Since(a.sentAt); d > 0 {
		writeErr(w, 429, fmt.Errorf("a code was sent just now: you can ask for another in %d s", int(d.Seconds())+1))
		return
	}
	s.store.DB().Exec(`UPDATE auth_tokens SET expires_at = ? WHERE hash = ?`, time.Now().Add(pendingLife).UTC().Format(time.RFC3339), tokenHash(t))
	if err := s.sendCode(r, t, u); err != nil {
		writeErr(w, 400, fmt.Errorf("cactusdb couldn't e-mail a new code: %v", err))
		return
	}
	log.Printf("%s %s asked for a new sign-in code", clientIP(r), u.Username)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(authCookie); err == nil {
		if a, err := s.store.token(c.Value, "session"); err == nil {
			if u, err := s.store.userByID(a.userID); err == nil {
				log.Printf("%s %s signed out", clientIP(r), u.Username)
				s.eventAs(r, u.Username, "signout", false, "signed out")
			}
		}
		s.store.dropToken(c.Value)
	}
	setCookie(w, r, authCookie, "", -1)
	setCookie(w, r, pendingCookie, "", -1)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// handleForgot e-mails a password reset link. The answer is the same whether
// or not the address has an account.
func (s *server) handleForgot(w http.ResponseWriter, r *http.Request) {
	if !s.needRemoteLogin(w) {
		return
	}
	var req struct{ Username string }
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	name := strings.TrimSpace(req.Username)
	if !validEmail(name) {
		writeErr(w, 400, errors.New("enter the e-mail address you sign in with (admin's password is reset with cactusdb --resetadmin on the server)"))
		return
	}
	key := "reset:" + clientIP(r)
	if d := s.authThrottle.wait(key); d > 0 {
		writeErr(w, 429, fmt.Errorf("too many requests: try again in %s", d.Round(time.Second)))
		return
	}
	s.authThrottle.failed(key) // every request counts: a few, then slower
	if u, err := s.store.userByName(name); err == nil && u.Status == "active" {
		s.store.dropTokens(u.ID, "reset")
		if err := s.mailSetPassword(r, u, "reset", ""); err != nil {
			log.Printf("%s password reset for %s: the e-mail failed: %v", clientIP(r), u.Username, err)
			if errors.Is(err, errNoMailServer) {
				writeErr(w, 400, errors.New("cactusdb can't send e-mail yet: ask someone to set up ☰ → E-mail server"))
				return
			}
		} else {
			log.Printf("%s asked for a password reset for %s: link e-mailed", clientIP(r), u.Username)
			s.eventAs(r, u.Username, "password", false, "asked for a password reset: link e-mailed")
		}
	} else {
		log.Printf("%s asked for a password reset for %q: no such active user, nothing sent", clientIP(r), name)
		s.eventAs(r, name, "password", true, "asked for a password reset: no such active user, nothing sent")
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// handleSetPasswordInfo says whose password a link sets.
func (s *server) handleSetPasswordInfo(w http.ResponseWriter, r *http.Request) {
	var req struct{ Token string }
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	a, err := s.store.token(req.Token, "invite", "reset")
	if err != nil {
		writeErr(w, 400, errors.New("this link has expired or was already used: links work for one hour. Ask for a new one"))
		return
	}
	u, err := s.store.userByID(a.userID)
	if err != nil {
		writeErr(w, 400, errors.New("this link's user no longer exists"))
		return
	}
	writeJSON(w, 200, map[string]any{"username": u.Username, "purpose": a.purpose, "expires": a.expires})
}

// handleSetPassword sets the password of an e-mailed link.
func (s *server) handleSetPassword(w http.ResponseWriter, r *http.Request) {
	if !s.needRemoteLogin(w) {
		return
	}
	var req struct{ Token, Password string }
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	a, err := s.store.token(req.Token, "invite", "reset")
	if err != nil {
		writeErr(w, 400, errors.New("this link has expired or was already used: links work for one hour. Ask for a new one"))
		return
	}
	u, err := s.store.userByID(a.userID)
	if err != nil {
		writeErr(w, 400, errors.New("this link's user no longer exists"))
		return
	}
	if err := checkNewPassword(u.Username, req.Password); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.store.updateUser(u.ID, func(u *appUser) { u.pw, u.Status, u.MustChange = hashPassword(req.Password), "active", false }); err != nil {
		writeErr(w, 500, err)
		return
	}
	// Every sign-in and link of the user ends: a reset locks out whoever had the old password.
	s.store.dropTokens(u.ID)
	log.Printf("%s %s set a password (%s link)", clientIP(r), u.Username, a.purpose)
	s.eventAs(r, u.Username, "password", false, "set a password with the e-mailed %s link", map[string]string{"invite": "invitation", "reset": "password reset"}[a.purpose])
	writeJSON(w, 200, map[string]any{"ok": true, "username": u.Username})
}

// handleRegister is a request for access: it waits for a signed-in user's
// approval. The answer doesn't say whether the address already has an account.
func (s *server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.needRemoteLogin(w) {
		return
	}
	var req struct{ Email, Name, Password, Reason string }
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	email := strings.TrimSpace(req.Email)
	if !validEmail(email) {
		writeErr(w, 400, errors.New("enter a valid e-mail address, e.g. name@example.com"))
		return
	}
	if err := checkNewPassword(email, req.Password); err != nil {
		writeErr(w, 400, err)
		return
	}
	name, reason := cleanName(req.Name), strings.TrimSpace(req.Reason)
	if len([]rune(reason)) > 500 {
		reason = string([]rune(reason)[:500])
	}
	key := "register:" + clientIP(r)
	if d := s.authThrottle.wait(key); d > 0 {
		writeErr(w, 429, fmt.Errorf("too many requests: try again in %s", d.Round(time.Second)))
		return
	}
	s.authThrottle.failed(key)
	_, added, err := s.store.addUser(appUser{Username: email, Name: name, pw: hashPassword(req.Password), Status: "requested", Reason: reason})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if added {
		log.Printf("%s %s requested access", clientIP(r), email)
		s.eventAs(r, email, "register", false, "requested access%s", map[bool]string{true: ": " + reason}[reason != ""])
		s.notify(nil, "users", 0, "a request for access from %s: approve it under ☰ → Users", email)
	} else {
		log.Printf("%s a request for access as %s was ignored: the address has an account or a request already", clientIP(r), email)
		s.eventAs(r, email, "register", true, "request for access ignored: the address has an account or a request already")
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---- users (signed in) -------------------------------------------------------

func (s *server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if !s.needRemoteLogin(w) {
		return
	}
	list, err := s.store.listUsers()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	me, _ := currentUser(r)
	writeJSON(w, 200, map[string]any{"users": list, "me": me.Username})
}

// handleAddUser adds a user and e-mails them a link to set a password.
func (s *server) handleAddUser(w http.ResponseWriter, r *http.Request) {
	if !s.needRemoteLogin(w) {
		return
	}
	me, _ := currentUser(r)
	var req struct{ Email, Name string }
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	email := strings.TrimSpace(req.Email)
	if !validEmail(email) {
		writeErr(w, 400, errors.New("the user name must be a valid e-mail address, e.g. name@example.com"))
		return
	}
	if _, err := s.store.userByName(email); err == nil {
		writeErr(w, 400, fmt.Errorf("%s is a user already", email))
		return
	}
	u, added, err := s.store.addUser(appUser{Username: email, Name: cleanName(req.Name), Status: "invited", AddedBy: me.Username})
	if err != nil || !added {
		writeErr(w, 500, cmp.Or(err, fmt.Errorf("%s is a user already", email)))
		return
	}
	if err := s.mailSetPassword(r, u, "invite", me.Username); err != nil {
		s.store.deleteUser(u.ID)
		writeErr(w, 400, fmt.Errorf("%s wasn't added: the invitation couldn't be e-mailed: %v", email, err))
		return
	}
	logAction(r, "added the user %s and e-mailed the invitation", email)
	s.event(r, "user", false, "added the user %s and e-mailed the invitation", email)
	s.notify(r, "users", 0, "added the user %s", email)
	writeJSON(w, 200, u)
}

// userAction: approve or reject a request for access, send an invitation (or
// a reset link) again, or delete a user.
func (s *server) handleUserAction(w http.ResponseWriter, r *http.Request) {
	if !s.needRemoteLogin(w) {
		return
	}
	me, _ := currentUser(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, errors.New("bad user id"))
		return
	}
	u, err := s.store.userByID(id)
	if err != nil {
		writeErr(w, 404, errors.New("no such user (deleted meanwhile?)"))
		return
	}
	switch act := r.PathValue("action"); act {
	case "approve":
		if u.Status != "requested" {
			writeErr(w, 400, fmt.Errorf("%s isn't waiting for approval", u.Username))
			return
		}
		link, err := s.link("")
		if err != nil {
			writeErr(w, 400, fmt.Errorf("not approved: the e-mail to %s can't be sent: %v", u.Username, err))
			return
		}
		link = strings.TrimSuffix(link, "#")
		if err := s.sendMail(r, u.Username, "Your access to cactusdb was approved",
			fmt.Sprintf("Hello%s,\n\n%s approved your request for access to cactusdb. Sign in here with this e-mail address\n"+
				"and the password you chose:\n\n%s\n\ncactusdb e-mails you a code each time you sign in.\n", greetName(u.Name), me.Username, link)); err != nil {
			writeErr(w, 400, fmt.Errorf("not approved: the e-mail to %s couldn't be sent: %v", u.Username, err))
			return
		}
		s.store.updateUser(u.ID, func(u *appUser) { u.Status, u.AddedBy = "active", me.Username })
		logAction(r, "approved the request for access of %s (e-mailed)", u.Username)
		s.event(r, "user", false, "approved the request for access of %s", u.Username)
		s.notify(r, "users", 0, "approved the request for access of %s", u.Username)
	case "reject":
		if u.Status != "requested" {
			writeErr(w, 400, fmt.Errorf("%s isn't waiting for approval", u.Username))
			return
		}
		s.store.deleteUser(u.ID)
		logAction(r, "rejected the request for access of %s", u.Username)
		s.event(r, "user", false, "rejected the request for access of %s", u.Username)
		s.notify(r, "users", 0, "rejected the request for access of %s", u.Username)
	case "resend":
		if !u.email() || u.Status == "requested" {
			writeErr(w, 400, fmt.Errorf("%s can't be sent a link", u.Username))
			return
		}
		purpose := "reset"
		if u.Status == "invited" {
			purpose = "invite"
		}
		s.store.dropTokens(u.ID, purpose)
		if err := s.mailSetPassword(r, u, purpose, me.Username); err != nil {
			writeErr(w, 400, err)
			return
		}
		logAction(r, "e-mailed %s a new %s link", u.Username, map[string]string{"invite": "invitation", "reset": "password reset"}[purpose])
		s.event(r, "user", false, "sent %s a new %s link", u.Username, map[string]string{"invite": "invitation", "reset": "password reset"}[purpose])
	case "delete":
		if u.ID == me.ID {
			writeErr(w, 400, errors.New("you can't delete yourself"))
			return
		}
		if s.store.countUsers("active", u.ID) == 0 {
			writeErr(w, 400, errors.New("that is the last user who can sign in"))
			return
		}
		s.store.deleteUser(u.ID)
		logAction(r, "deleted the user %s", u.Username)
		s.event(r, "user", false, "deleted the user %s", u.Username)
		s.notify(r, "users", 0, "deleted the user %s", u.Username)
	default:
		writeErr(w, 404, fmt.Errorf("unknown action %q", act))
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// handleChangePassword changes the signed-in user's own password.
func (s *server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if !s.needRemoteLogin(w) {
		return
	}
	me, _ := currentUser(r)
	var req struct{ Current, Password string }
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	key := "u:" + strings.ToLower(me.Username)
	if d := s.authThrottle.wait(key); d > 0 {
		writeErr(w, 429, fmt.Errorf("too many wrong passwords: try again in %s", d.Round(time.Second)))
		return
	}
	if !checkPassword(me.pw, req.Current) {
		s.authThrottle.failed(key)
		s.event(r, "password", true, "changing the password failed: the current password is wrong")
		writeErr(w, 400, errors.New("the current password is wrong"))
		return
	}
	if err := checkNewPassword(me.Username, req.Password); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.store.updateUser(me.ID, func(u *appUser) { u.pw, u.MustChange = hashPassword(req.Password), false }); err != nil {
		writeErr(w, 500, err)
		return
	}
	// The other browsers signed in as this user are signed out.
	if c, err := r.Cookie(authCookie); err == nil {
		s.store.DB().Exec(`DELETE FROM auth_tokens WHERE user_id = ? AND hash != ?`, me.ID, tokenHash(c.Value))
	}
	logAction(r, "changed their password")
	s.event(r, "password", false, "changed their password")
	writeJSON(w, 200, map[string]any{"ok": true})
}
