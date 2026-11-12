package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is a mail server on this computer that keeps what it is sent.
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	mail []sentMail
}

type sentMail struct{ to, subject, body string }

func startFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	rd := bufio.NewReader(c)
	say := func(s string) { c.Write([]byte(s + "\r\n")) }
	say("220 fake ESMTP")
	var to string
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			say("250-fake\r\n250 8BITMIME")
		case strings.HasPrefix(cmd, "MAIL FROM"):
			say("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO"):
			to = strings.Trim(strings.TrimSpace(line)[8:], "<> ")
			say("250 ok")
		case cmd == "DATA":
			say("354 go on")
			var b strings.Builder
			for {
				l, err := rd.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			head, body, _ := strings.Cut(b.String(), "\r\n\r\n")
			subject := regexp.MustCompile(`(?m)^Subject: (.*)\r$`).FindStringSubmatch(head)[1]
			plain, _ := io.ReadAll(quotedprintable.NewReader(strings.NewReader(body)))
			f.mu.Lock()
			f.mail = append(f.mail, sentMail{to: to, subject: subject, body: strings.ReplaceAll(string(plain), "\r\n", "\n")})
			f.mu.Unlock()
			say("250 queued")
		case cmd == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

// last is the newest message to an address.
func (f *fakeSMTP) last(t *testing.T, to string) sentMail {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.mail) - 1; i >= 0; i-- {
		if f.mail[i].to == to {
			return f.mail[i]
		}
	}
	t.Fatalf("no e-mail to %s", to)
	return sentMail{}
}

func (f *fakeSMTP) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.mail)
}

// authHarness is a --remotelogin cactusdb with a page and its API behind the
// sign-in, and a mail server.
type authHarness struct {
	t    *testing.T
	s    *server
	ts   *httptest.Server
	mail *fakeSMTP
}

func newAuthHarness(t *testing.T) *authHarness {
	s := newTestServer(t)
	s.remoteLogin = true
	if added, err := s.store.ensureAdmin(); err != nil || !added {
		t.Fatalf("ensureAdmin: %v %v", added, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("the page")) })
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("sign in")) })
	mux.HandleFunc("GET /api/connections", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, []string{"secret"}) })
	mux.HandleFunc("GET /api/auth/me", s.handleAuthMe)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/login/password", s.handleLoginPassword)
	mux.HandleFunc("POST /api/auth/login/code", s.handleLoginCode)
	mux.HandleFunc("POST /api/auth/login/resend", s.handleLoginResend)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("POST /api/auth/forgot", s.handleForgot)
	mux.HandleFunc("POST /api/auth/setpassword/info", s.handleSetPasswordInfo)
	mux.HandleFunc("POST /api/auth/setpassword", s.handleSetPassword)
	mux.HandleFunc("POST /api/auth/register", s.handleRegister)
	mux.HandleFunc("GET /api/users", s.handleListUsers)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("POST /api/users", s.handleAddUser)
	mux.HandleFunc("POST /api/users/{id}/{action}", s.handleUserAction)
	mux.HandleFunc("POST /api/account/password", s.handleChangePassword)
	mux.HandleFunc("POST /api/email", s.handleSaveSMTP)
	mux.HandleFunc("POST /api/email/test", s.handleTestSMTP)
	ts := httptest.NewServer(statusInHeader(s.requireLogin(mux)))
	t.Cleanup(ts.Close)
	h := &authHarness{t: t, s: s, ts: ts, mail: startFakeSMTP(t)}
	return h
}

// browser is a cookie jar: one person's browser.
func (h *authHarness) browser() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// call posts body (or GETs, with a nil body) like the page: the status comes
// in the X-Cactusdb-Status header.
func (h *authHarness) call(c *http.Client, method, path string, body any) (int, map[string]any) {
	h.t.Helper()
	var rd *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	req, _ := http.NewRequest(method, h.ts.URL+path, rd)
	req.Header.Set("X-Cactusdb-Errors", "in-header")
	req.Header.Set("Content-Type", "application/json")
	res, err := c.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	status := res.StatusCode
	if st := res.Header.Get("X-Cactusdb-Status"); st != "" {
		json.Unmarshal([]byte(st), &status)
	}
	if res.StatusCode != 200 && res.StatusCode != 303 {
		h.t.Errorf("%s %s: the page was answered with HTTP %d (a firewall may block it)", method, path, res.StatusCode)
	}
	return status, out
}

func (h *authHarness) mustOK(c *http.Client, path string, body any) map[string]any {
	h.t.Helper()
	code, out := h.call(c, "POST", path, body)
	if code != 200 {
		h.t.Fatalf("%s: %d %v", path, code, out)
	}
	return out
}

var linkRe = regexp.MustCompile(`http://\S+/login#set=([\w-]+)`)
var codeRe = regexp.MustCompile(`\b(\d{8})\b`)

func TestRemoteLogin(t *testing.T) {
	h := newAuthHarness(t)
	admin := h.browser()

	// Signed out: the page goes to /login, the API says "sign in first".
	res, _ := admin.Get(h.ts.URL + "/")
	if res.StatusCode != 303 || res.Header.Get("Location") != "/login" {
		t.Fatalf("GET / signed out: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if code, out := h.call(admin, "GET", "/api/connections", nil); code != 401 || out["signin"] != true {
		t.Fatalf("API signed out: %d %v", code, out)
	}
	if res, _ := admin.Get(h.ts.URL + "/login"); res.StatusCode != 200 {
		t.Fatalf("the sign-in page: %d", res.StatusCode)
	}

	// admin/admin: a new password first; then signed in (admin gets no code).
	if code, _ := h.call(admin, "POST", "/api/auth/login", map[string]string{"username": "admin", "password": "wrong"}); code != 400 {
		t.Fatalf("a wrong password: %d", code)
	}
	if out := h.mustOK(admin, "/api/auth/login", map[string]string{"username": "admin", "password": "admin"}); out["next"] != "change" {
		t.Fatalf("admin/admin: %v", out)
	}
	if code, _ := h.call(admin, "GET", "/api/connections", nil); code != 401 {
		t.Fatal("signed in before the new password")
	}
	if code, _ := h.call(admin, "POST", "/api/auth/login/password", map[string]string{"password": "admin"}); code != 400 {
		t.Fatal("admin kept the password admin")
	}
	if out := h.mustOK(admin, "/api/auth/login/password", map[string]string{"password": "Admin-pass-1"}); out["next"] != "done" {
		t.Fatalf("new password: %v", out)
	}
	if code, _ := h.call(admin, "GET", "/api/connections", nil); code != 200 {
		t.Fatalf("signed in: %d", code)
	}

	// No mail server yet: a user can't be added.
	if code, out := h.call(admin, "POST", "/api/users", map[string]string{"email": "ann@example.com"}); code != 400 || !strings.Contains(out["error"].(string), "E-mail server") {
		t.Fatalf("add without a mail server: %d %v", code, out)
	}
	port := h.mail.ln.Addr().(*net.TCPAddr).Port
	h.mustOK(admin, "/api/email", map[string]any{"enabled": true, "host": "127.0.0.1", "port": port, "tls": "none", "from": "cactusdb <db@example.com>", "linkBase": "http://db.example.com:8080"})
	h.mustOK(admin, "/api/email/test", map[string]string{"to": "it@example.com"})
	if m := h.mail.last(t, "it@example.com"); m.subject != "cactusdb test e-mail" {
		t.Fatalf("test e-mail: %+v", m)
	}

	// The user name must be an e-mail address.
	if code, _ := h.call(admin, "POST", "/api/users", map[string]string{"email": "ann"}); code != 400 {
		t.Fatal("a user that isn't an e-mail address was added")
	}
	h.mustOK(admin, "/api/users", map[string]string{"email": "ann@example.com", "name": "Ann"})
	invite := h.mail.last(t, "ann@example.com")
	m := linkRe.FindStringSubmatch(invite.body)
	if m == nil || !strings.HasPrefix(m[0], "http://db.example.com:8080/login#set=") {
		t.Fatalf("invitation: %+v", invite)
	}
	ann := h.browser()
	if out := h.mustOK(ann, "/api/auth/setpassword/info", map[string]string{"token": m[1]}); out["username"] != "ann@example.com" || out["purpose"] != "invite" {
		t.Fatalf("link info: %v", out)
	}
	h.mustOK(ann, "/api/auth/setpassword", map[string]string{"token": m[1], "password": "ann-secret-1"})
	if code, _ := h.call(ann, "POST", "/api/auth/setpassword", map[string]string{"token": m[1], "password": "ann-secret-2"}); code != 400 {
		t.Fatal("a link worked twice")
	}

	// Ann signs in: password, then the e-mailed code.
	if out := h.mustOK(ann, "/api/auth/login", map[string]string{"username": "ANN@example.com", "password": "ann-secret-1"}); out["next"] != "code" {
		t.Fatalf("ann's sign-in: %v", out)
	}
	first := codeRe.FindStringSubmatch(h.mail.last(t, "ann@example.com").body)[1]
	if code, _ := h.call(ann, "GET", "/api/connections", nil); code != 401 {
		t.Fatal("signed in before the code")
	}
	if code, out := h.call(ann, "POST", "/api/auth/login/resend", nil); code != 429 {
		t.Fatalf("a new code straight away: %d %v", code, out)
	}
	h.s.store.DB().Exec(`UPDATE auth_tokens SET sent_at = ? WHERE purpose = 'login-code'`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339))
	h.mustOK(ann, "/api/auth/login/resend", nil)
	second := codeRe.FindStringSubmatch(h.mail.last(t, "ann@example.com").body)[1]
	if first != second {
		if code, _ := h.call(ann, "POST", "/api/auth/login/code", map[string]string{"code": first}); code != 400 {
			t.Fatal("the replaced code still works")
		}
	}
	if out := h.mustOK(ann, "/api/auth/login/code", map[string]string{"code": second[:4] + " " + second[4:]}); out["next"] != "done" {
		t.Fatalf("code: %v", out)
	}
	if code, _ := h.call(ann, "GET", "/api/connections", nil); code != 200 {
		t.Fatal("ann isn't signed in")
	}

	// Too many wrong codes end the sign-in.
	eve := h.browser()
	h.mustOK(eve, "/api/auth/login", map[string]string{"username": "ann@example.com", "password": "ann-secret-1"})
	for i := range maxCodeTries {
		code, out := h.call(eve, "POST", "/api/auth/login/code", map[string]string{"code": "00000000"})
		if code != 400 || (i == maxCodeTries-1) != (out["restart"] == true) {
			t.Fatalf("wrong code %d: %d %v", i, code, out)
		}
	}

	// Forgot the password: a reset link; the old password stops working.
	h.mustOK(h.browser(), "/api/auth/forgot", map[string]string{"username": "ann@example.com"})
	reset := linkRe.FindStringSubmatch(h.mail.last(t, "ann@example.com").body)
	before := h.mail.count()
	h.mustOK(h.browser(), "/api/auth/forgot", map[string]string{"username": "nobody@example.com"}) // the same answer, nothing sent
	if h.mail.count() != before {
		t.Fatal("a reset e-mail to an address without an account")
	}
	h.mustOK(h.browser(), "/api/auth/setpassword", map[string]string{"token": reset[1], "password": "ann-secret-new"})
	if code, _ := h.call(ann, "GET", "/api/connections", nil); code != 401 {
		t.Fatal("a reset didn't sign out ann's browser")
	}
	if code, _ := h.call(h.browser(), "POST", "/api/auth/login", map[string]string{"username": "ann@example.com", "password": "ann-secret-1"}); code != 400 {
		t.Fatal("the old password works after a reset")
	}

	// A request for access: no sign-in until approved, then an e-mail.
	bob := h.browser()
	h.mustOK(bob, "/api/auth/register", map[string]string{"email": "bob@example.com", "name": "Bob", "password": "bob-secret-1", "reason": "reports"})
	if code, out := h.call(bob, "POST", "/api/auth/login", map[string]string{"username": "bob@example.com", "password": "bob-secret-1"}); code != 400 || !strings.Contains(out["error"].(string), "approved") {
		t.Fatalf("bob before approval: %d %v", code, out)
	}
	_, list := h.call(admin, "GET", "/api/users", nil)
	var bobID string
	for _, u := range list["users"].([]any) {
		if u := u.(map[string]any); u["username"] == "bob@example.com" {
			if u["status"] != "requested" {
				t.Fatalf("bob: %v", u)
			}
			bobID = jsonNum(u["id"])
		}
	}
	if code, _ := h.call(bob, "POST", "/api/users/"+bobID+"/approve", nil); code != 401 {
		t.Fatal("approved without being signed in")
	}
	h.mustOK(admin, "/api/users/"+bobID+"/approve", nil)
	if m := h.mail.last(t, "bob@example.com"); !strings.Contains(m.subject, "approved") || !strings.Contains(m.body, "http://db.example.com:8080/login") {
		t.Fatalf("approval e-mail: %+v", m)
	}
	if out := h.mustOK(bob, "/api/auth/login", map[string]string{"username": "bob@example.com", "password": "bob-secret-1"}); out["next"] != "code" {
		t.Fatalf("bob after approval: %v", out)
	}

	// Nobody deletes themselves; signing out ends the sign-in.
	_, me := h.call(admin, "GET", "/api/auth/me", nil)
	if me["user"].(map[string]any)["username"] != "admin" {
		t.Fatalf("me: %v", me)
	}
	h.mustOK(admin, "/api/auth/logout", nil)
	if code, _ := h.call(admin, "GET", "/api/connections", nil); code != 401 {
		t.Fatal("still signed in after signing out")
	}
}

func jsonNum(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestPasswordHashAndThrottle(t *testing.T) {
	h := hashPassword("correct horse")
	if !checkPassword(h, "correct horse") || checkPassword(h, "correct horsE") || checkPassword("junk", "x") {
		t.Fatal("checkPassword")
	}
	if strings.Contains(h, "correct") {
		t.Fatal("the hash holds the password")
	}
	var th authThrottle
	for range 4 {
		th.failed("u:a")
	}
	if th.wait("u:a") != 0 {
		t.Fatal("throttled after 4")
	}
	th.failed("u:a")
	if th.wait("u:a", "ip:x") == 0 {
		t.Fatal("not throttled after 5")
	}
	th.ok("u:a")
	if th.wait("u:a") != 0 {
		t.Fatal("still throttled")
	}
	for _, c := range []string{newCode(), newCode(), newCode()} {
		if len(c) != 8 || strings.Trim(c, "0123456789") != "" {
			t.Fatalf("code %q", c)
		}
	}
	for in, want := range map[string]bool{"a@b.co": true, "ann.smith@example.com": true, "admin": false, "a@b": false, "Ann <a@b.co>": false, "a@b.co ": false} {
		if validEmail(in) != want {
			t.Errorf("validEmail(%q) = %v", in, !want)
		}
	}
}

// The users of 1.48 (in plain text) are encrypted at startup, and nothing of
// a user but the e-mail address can be read in the file.
func TestUsersEncrypted(t *testing.T) {
	dir := t.TempDir()
	path, key := filepath.Join(dir, "c.db"), filepath.Join(dir, "c.key")
	st, err := openStore(path, key)
	if err != nil {
		t.Fatal(err)
	}
	// Put the 1.48 tables back, as an old file has them.
	if _, err := st.DB().Exec(`DROP TABLE users; CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL UNIQUE COLLATE NOCASE, name TEXT NOT NULL DEFAULT '',
		pw TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, must_change INTEGER NOT NULL DEFAULT 0, reason TEXT NOT NULL DEFAULT '',
		added_by TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, last_login TEXT NOT NULL DEFAULT '');
		INSERT INTO users (id, username, name, pw, status, reason, added_by, created_at, last_login)
		VALUES (7, 'bob@example.com', 'Bobby Tables', ?, 'requested', 'quarterly figures', 'carol@example.com', '2026-11-01T08:00:00Z', '2026-11-02T09:00:00Z');
		INSERT INTO auth_tokens (hash, user_id, purpose, expires_at, ip) VALUES ('h1', 7, 'session', '2999-01-01T00:00:00Z', '192.0.2.77')`,
		hashPassword("bobs-password")); err != nil {
		t.Fatal(err)
	}
	st.DB().Close()

	st, err = openStore(path, key)
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.userByName("BOB@example.com")
	if err != nil || u.ID != 7 || u.Name != "Bobby Tables" || u.Status != "requested" || u.Reason != "quarterly figures" ||
		u.AddedBy != "carol@example.com" || u.LastLogin != "2026-11-02T09:00:00Z" || !checkPassword(u.pw, "bobs-password") {
		t.Fatalf("the moved user: %+v %v", u, err)
	}
	if added, err := st.ensureAdmin(); err != nil || added {
		t.Fatalf("ensureAdmin with users: %v %v", added, err)
	}
	nu, added, err := st.addUser(appUser{Username: "dave@example.com", Name: "Dave Secretname", Status: "invited", AddedBy: "bob@example.com"})
	if err != nil || !added {
		t.Fatal(added, err)
	}
	if _, added, _ := st.addUser(appUser{Username: "DAVE@example.com", Status: "requested"}); added {
		t.Fatal("the same address added twice")
	}
	st.updateUser(nu.ID, func(u *appUser) { u.Status = "active" })
	if n := st.countUsers("active", 0); n != 1 {
		t.Fatalf("active users: %d", n)
	}
	if list, _ := st.listUsers(); len(list) != 2 || list[0].Username != "bob@example.com" {
		t.Fatalf("list: %+v", list)
	}
	if _, err := st.addToken(nu.ID, "session", time.Hour, "198.51.100.23"); err != nil {
		t.Fatal(err)
	}
	st.DB().Close()

	raw, _ := os.ReadFile(path)
	for _, wal := range []string{"-wal", "-journal"} {
		b, _ := os.ReadFile(path + wal)
		raw = append(raw, b...)
	}
	for _, secret := range []string{"Bobby Tables", "quarterly figures", "requested", "2026-11-02", "192.0.2.77", "Dave Secretname", "198.51.100.23", "argon2id", "carol@example.com"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Errorf("c.db holds %q in plain text", secret)
		}
	}
	if !bytes.Contains(raw, []byte("bob@example.com")) {
		t.Error("the user name isn't in the file (it is meant to be plain text)")
	}
}

// Sign-ins, failed ones and password changes are events; only signed-in users
// see them, and what they say isn't readable in the file.
func TestEvents(t *testing.T) {
	h := newAuthHarness(t)
	admin := h.browser()
	h.call(admin, "POST", "/api/auth/login", map[string]string{"username": "admin", "password": "not-it"})
	if code, out := h.call(admin, "GET", "/api/events", nil); code != 401 || out["signin"] != true {
		t.Fatalf("events signed out: %d %v", code, out)
	}
	h.mustOK(admin, "/api/auth/login", map[string]string{"username": "admin", "password": "admin"})
	h.mustOK(admin, "/api/auth/login/password", map[string]string{"password": "a-new-admin-pw"})

	events := func(query string) []map[string]any {
		t.Helper()
		code, out := h.call(admin, "GET", "/api/events"+query, nil)
		if code != 200 {
			t.Fatalf("events%s: %d %v", query, code, out)
		}
		var list []map[string]any
		for _, e := range out["events"].([]any) {
			list = append(list, e.(map[string]any))
		}
		return list
	}
	all := events("")
	var kinds []string
	for _, e := range all {
		kinds = append(kinds, e["kind"].(string))
	}
	if got := strings.Join(kinds, ","); got != "signin,password,signin-failed" {
		t.Fatalf("events newest first: %s", got)
	}
	if f := all[2]; f["user"] != "admin" || f["failed"] != true || f["ip"] != "127.0.0.1" || !strings.Contains(f["text"].(string), "wrong user name or password") {
		t.Fatalf("the failed sign-in: %v", f)
	}
	if l := events("?kind=signin-failed"); len(l) != 1 {
		t.Fatalf("kind filter: %v", l)
	}
	if l := events("?failed=1"); len(l) != 1 {
		t.Fatalf("failed filter: %v", l)
	}
	if l := events("?q=NEW+PASSWORD"); len(l) != 1 || l[0]["kind"] != "password" {
		t.Fatalf("search: %v", l)
	}
	if l := events("?before=" + strconv.Itoa(int(all[1]["id"].(float64)))); len(l) != 1 {
		t.Fatalf("older: %v", l)
	}
	today := time.Now().Format("2006-01-02")
	_, offset := time.Now().Zone()
	if l := events("?day=" + today + "&tz=" + strconv.Itoa(-offset/60)); len(l) != 3 {
		t.Fatalf("today: %v", l)
	}
	if l := events("?day=2001-01-01&tz=0"); len(l) != 0 {
		t.Fatalf("another day: %v", l)
	}

	raw, _ := os.ReadFile(h.s.store.path)
	b, _ := os.ReadFile(h.s.store.path + "-wal")
	raw = append(raw, b...)
	for _, secret := range []string{"wrong user name", "127.0.0.1", "new password at sign-in"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Errorf("c.db holds %q in plain text", secret)
		}
	}
}
