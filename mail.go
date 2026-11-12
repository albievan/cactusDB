package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ☰ → E-mail server: the mail server cactusdb sends e-mail through (the
// links and sign-in codes of --remotelogin, see auth.go). Set up the same
// way as a mail client: a provider preset (Gmail,
// Outlook / Microsoft 365, Custom), host, port, security, user name,
// password, Send as, and "Use this server for all e-mail". The settings are
// kept in the meta table; the password sealed with cactusdb's key
// (crypt.go), never shown again.

const smtpMetaKey = "smtp"

// smtpSettings is the mail server as saved (the password apart).
type smtpSettings struct {
	Enabled  bool   `json:"enabled"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	From     string `json:"from"` // "Example Ltd <it@example.com>"; "" = the user name
	TLS      string `json:"tls"`  // starttls | tls | none
	// LinkBase is this cactusdb's address as the people it e-mails reach
	// it, e.g. http://10.66.1.136:8087: the links in its e-mails start
	// with it. Never taken from a request's Host header, which anyone
	// can set (a password reset link would point elsewhere).
	LinkBase string `json:"linkBase"`
}

// storedSMTP is the meta row: the settings and the sealed password.
type storedSMTP struct {
	smtpSettings
	Password string `json:"password,omitempty"`
}

// gmailSMTP is the preset shown before anything was saved.
func gmailSMTP() smtpSettings {
	return smtpSettings{Host: "smtp.gmail.com", Port: 587, TLS: "starttls"}
}

// validate tidies and checks the settings. Disabled ones may be incomplete.
func (st *smtpSettings) validate() error {
	st.Host = strings.TrimSpace(st.Host)
	st.Username = strings.TrimSpace(st.Username)
	st.From = strings.TrimSpace(st.From)
	st.LinkBase = strings.TrimRight(strings.TrimSpace(st.LinkBase), "/")
	if st.TLS == "" {
		st.TLS = "starttls"
	}
	switch {
	case st.TLS != "starttls" && st.TLS != "tls" && st.TLS != "none":
		return errors.New("security must be STARTTLS, SSL/TLS or None")
	case st.Port < 0 || st.Port > 65535:
		return errors.New("the port must be a TCP port (1–65535)")
	case len(st.Host) > 255 || len(st.Username) > 254 || len(st.From) > 320 || len(st.LinkBase) > 500:
		return errors.New("a value is too long")
	}
	if st.LinkBase != "" {
		if u, err := url.Parse(st.LinkBase); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("the address in links must be like http://server:8080")
		}
	}
	if !st.Enabled {
		return nil
	}
	if st.Host == "" || strings.ContainsAny(st.Host, " /:") {
		return errors.New("the SMTP host is required (a host name, e.g. smtp.gmail.com)")
	}
	// Go's SMTP client sends a password only over TLS (or to this computer):
	// with None every e-mail would fail to sign in.
	if st.TLS == "none" && st.Username != "" && !isLocalhost(st.Host) {
		return errors.New("a user name and password are only sent encrypted: choose STARTTLS or SSL/TLS, or leave the user name empty")
	}
	if st.Port == 0 {
		return errors.New("the port is required")
	}
	if st.From == "" && st.Username != "" {
		st.From = st.Username
	}
	if a, err := mail.ParseAddress(st.From); err != nil || a.Address == "" {
		return errors.New("Send as must be an e-mail address, optionally with a name")
	}
	if st.LinkBase == "" {
		return errors.New("the address in links is required: how the people cactusdb e-mails reach it, e.g. http://server:8080")
	}
	return nil
}

func isLocalhost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" // as net/smtp's PlainAuth
}

// senderWarnings says when the provider is known not to send as the Send as
// address: Gmail rewrites From to the signed-in account unless it is a
// verified "Send mail as" alias, and Microsoft 365 may refuse unless it is
// the mailbox's own address or the mailbox has Send As permission. It warns,
// never refuses: the alias list lives at the provider. Never nil.
func senderWarnings(st smtpSettings) []string {
	warnings := []string{}
	host := strings.TrimSuffix(strings.ToLower(st.Host), ".")
	if st.Username == "" || st.From == "" {
		return warnings
	}
	a, err := mail.ParseAddress(st.From)
	if err != nil || strings.EqualFold(a.Address, st.Username) {
		return warnings
	}
	switch host {
	case "smtp.gmail.com", "smtp.googlemail.com":
		warnings = append(warnings, "Gmail sends as "+st.Username+" unless "+a.Address+
			" is a verified \"Send mail as\" address of that account (Gmail → Settings → Accounts → Send mail as).")
	case "smtp.office365.com", "smtp-mail.outlook.com":
		warnings = append(warnings, "Microsoft 365 may refuse to send as "+a.Address+
			" unless it is the mailbox's own address or "+st.Username+" has Send As permission for it.")
	}
	return warnings
}

// loadSMTP reads the saved mail server; ok is false before one was saved.
func (s *Store) loadSMTP() (st storedSMTP, ok bool, err error) {
	v, err := s.meta(smtpMetaKey)
	if err != nil || v == "" {
		return storedSMTP{}, false, err
	}
	if err := json.Unmarshal([]byte(v), &st); err != nil {
		return storedSMTP{}, false, fmt.Errorf("the saved e-mail server settings are damaged: %w", err)
	}
	return st, true, nil
}

func (s *Store) saveSMTP(st storedSMTP) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.setMeta(smtpMetaKey, string(b))
}

// smtpView is the mail server as the page sees it: never the password.
func (s *server) smtpView() (map[string]any, error) {
	st, ok, err := s.store.loadSMTP()
	if err != nil {
		return nil, err
	}
	if !ok {
		st.smtpSettings = gmailSMTP()
	}
	return map[string]any{"enabled": st.Enabled, "host": st.Host, "port": st.Port, "username": st.Username, "from": st.From,
		"tls": st.TLS, "linkBase": st.LinkBase, "passwordSet": st.Password != "", "warnings": senderWarnings(st.smtpSettings), "saved": ok}, nil
}

func (s *server) handleGetSMTP(w http.ResponseWriter, r *http.Request) {
	v, err := s.smtpView()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, v)
}

// handleSaveSMTP saves the mail server. A password of null keeps the saved
// one, "" removes it, anything else replaces it. The saved password is only
// kept for the same server, port, security and user name: otherwise saving
// another server would send it there.
func (s *server) handleSaveSMTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		smtpSettings
		Password *string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	st := req.smtpSettings
	if err := st.validate(); err != nil {
		writeErr(w, 400, err)
		return
	}
	old, _, err := s.store.loadSMTP()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	pw := old.Password
	if req.Password == nil && pw != "" && (!strings.EqualFold(old.Host, st.Host) || old.Port != st.Port || old.TLS != st.TLS || old.Username != st.Username) {
		writeErr(w, 400, errors.New("enter the password again: the server, port, security or user name changed"))
		return
	}
	if req.Password != nil {
		pw = ""
		if *req.Password != "" {
			pw = s.store.box.seal("smtp_password", *req.Password)
		}
	}
	if st.Enabled && st.Username != "" && pw == "" {
		writeErr(w, 400, errors.New("the password is required when a user name is set (for Gmail: an app password)"))
		return
	}
	if err := s.store.saveSMTP(storedSMTP{smtpSettings: st, Password: pw}); err != nil {
		writeErr(w, 500, err)
		return
	}
	state := "off"
	if st.Enabled {
		state = "on"
	}
	changed := ""
	if req.Password != nil {
		changed = ", password changed"
	}
	logAction(r, "saved the e-mail server: %s:%d (%s) as %q, %s%s", st.Host, st.Port, st.TLS, st.Username, state, changed)
	s.event(r, "settings", false, "saved the e-mail server: %s:%d (%s) as %q, %s%s", st.Host, st.Port, st.TLS, st.Username, state, changed)
	v, _ := s.smtpView()
	writeJSON(w, 200, v)
}

// handleTestSMTP sends a test e-mail through the saved mail server.
func (s *server) handleTestSMTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		To string `json:"to"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	to := strings.TrimSpace(req.To)
	if !validEmail(to) {
		writeErr(w, 400, errors.New("enter one e-mail address to send the test to"))
		return
	}
	st, _, _ := s.store.loadSMTP()
	if err := s.sendMail(r, to, "cactusdb test e-mail",
		"This is a test e-mail from cactusdb. Your e-mail server settings work.\n\n"+linkLine(st.LinkBase)); err != nil {
		logAction(r, "test e-mail to %s failed: %v", to, err)
		writeErr(w, 400, err)
		return
	}
	logAction(r, "sent a test e-mail to %s", to)
	writeJSON(w, 200, map[string]any{"ok": true, "warnings": senderWarnings(st.smtpSettings)})
}

func linkLine(base string) string {
	if base == "" {
		return ""
	}
	return "cactusdb: " + base + "\n"
}

var errNoMailServer = errors.New("no e-mail server is set up: set one up under ☰ → E-mail server")

// sendMail sends a plain-text e-mail through the saved mail server.
func (s *server) sendMail(r *http.Request, to, subject, body string) (err error) {
	// The event says what was sent, never a sign-in code.
	about, _, _ := strings.Cut(subject, ": ")
	defer func() {
		if err != nil {
			s.event(r, "email", true, "e-mail to %s failed (%s): %v", to, about, err)
		} else {
			s.event(r, "email", false, "e-mailed %s: %s", to, about)
		}
	}()
	st, ok, err := s.store.loadSMTP()
	if err != nil {
		return err
	}
	if !ok || !st.Enabled {
		return errNoMailServer
	}
	pw := ""
	if st.Password != "" {
		if pw, err = s.store.box.open("smtp_password", st.Password); err != nil {
			return fmt.Errorf("cannot decrypt the e-mail server's password: %w", err)
		}
	}
	from, err := mail.ParseAddress(st.From)
	if err != nil {
		return fmt.Errorf("Send as: %w", err)
	}
	if err := smtpSend(r.Context(), st.smtpSettings, pw, from, to, subject, body, nil); err != nil {
		return fmt.Errorf("the e-mail server refused: %w", err)
	}
	return nil
}

// smtpSend delivers one message. tlsCfg overrides the TLS settings (tests).
func smtpSend(ctx context.Context, st smtpSettings, password string, from *mail.Address, to, subject, body string, tlsCfg *tls.Config) error {
	if strings.ContainsAny(to, "\r\n") {
		return errors.New("invalid recipient")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	addr := net.JoinHostPort(st.Host, strconv.Itoa(st.Port))
	if tlsCfg == nil {
		tlsCfg = &tls.Config{ServerName: st.Host, MinVersion: tls.VersionTLS12}
	}
	var conn net.Conn
	var err error
	if st.TLS == "tls" {
		conn, err = (&tls.Dialer{Config: tlsCfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	c, err := smtp.NewClient(conn, st.Host)
	if err != nil {
		return fmt.Errorf("greeting: %w", err)
	}
	defer c.Close()
	helo := "localhost"
	if _, d, ok := strings.Cut(from.Address, "@"); ok && d != "" {
		helo = d
	}
	if err := c.Hello(helo); err != nil {
		return fmt.Errorf("hello: %w", err)
	}
	if st.TLS == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("the server does not offer STARTTLS: choose SSL/TLS (port 465) or None")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if st.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", st.Username, password, st.Host)); err != nil {
			return fmt.Errorf("sign-in: %w", err)
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("recipient %s: %w", to, err)
	}
	wc, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := wc.Write(buildMail(from, to, subject, body, time.Now())); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("end of data: %w", err)
	}
	// Accepted (250 after the data): a failing QUIT doesn't make it unsent.
	_ = c.Quit()
	return nil
}

// buildMail renders a plain-text message, quoted-printable.
func buildMail(from *mail.Address, to, subject, body string, now time.Time) []byte {
	var b bytes.Buffer
	h := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	h("From", from.String())
	h("To", to)
	h("Subject", mime.QEncoding.Encode("utf-8", strings.NewReplacer("\r", " ", "\n", " ").Replace(subject)))
	h("Date", now.Format(time.RFC1123Z))
	_, domain, _ := strings.Cut(from.Address, "@")
	h("Message-ID", "<"+newID()+"@"+domain+">")
	h("MIME-Version", "1.0")
	h("Content-Type", "text/plain; charset=utf-8")
	h("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	qp.Write([]byte(strings.ReplaceAll(body, "\n", "\r\n")))
	qp.Close()
	return b.Bytes()
}

// validEmail: one plain address, user@domain.tld.
func validEmail(s string) bool {
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || a.Name != "" || len(s) > 254 {
		return false
	}
	_, domain, ok := strings.Cut(s, "@")
	return ok && strings.Contains(domain, ".") && !strings.HasSuffix(domain, ".") && !strings.ContainsAny(s, " <>,;")
}
