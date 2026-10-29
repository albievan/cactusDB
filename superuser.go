package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// The superuser password opens every saved connection, also those protected
// by their own connection password. It is never stored, and never part of
// the program: it encrypts the private half of a key pair, and every
// connection password is also sealed to the public half. Saving a connection
// therefore needs only the public key, while opening a connection's sealed
// password needs the superuser password.
//
// meta "superuser_pub":  the X25519 public key, base64
// meta "superuser_priv": "<argon2id lock>|<private key sealed with its key>"
// connections.escrow:    the connection password sealed to the public key
//                        ("x25519:v1:<ephemeral public key, nonce, ciphertext>")

const escrowPrefix = "x25519:v1:"

// superuserEnv sets the superuser password when the store has none yet.
const superuserEnv = "CACTUSDB_SUPERUSER_PASSWORD"

var (
	errNoSuperuser = errors.New("no superuser password has been set (cactusdb --superuser set)")
	errNoEscrow    = errors.New("this connection was saved before the superuser password was set and hasn't been opened since: open it once with its own connection password")
)

// migrateSuperuser adds the escrow column to older stores.
func (s *Store) migrateSuperuser() error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('connections') WHERE name = 'escrow'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		_, err := s.db.Exec(`ALTER TABLE connections ADD COLUMN escrow TEXT NOT NULL DEFAULT ''`)
		return err
	}
	return nil
}

// SuperuserSet reports whether a superuser password exists.
func (s *Store) SuperuserSet() bool {
	pub, _ := s.meta("superuser_pub")
	return pub != ""
}

// SetSuperuser sets the superuser password, or changes it (old must then be
// the current one). Changing it keeps the key pair, so every connection
// stays openable.
func (s *Store) SetSuperuser(newPassword, old string) error {
	if len([]rune(newPassword)) < 10 {
		return errors.New("the superuser password must be at least 10 characters")
	}
	var priv *ecdh.PrivateKey
	if s.SuperuserSet() {
		var err error
		if priv, err = s.openSuperuser(old); err != nil {
			return err
		}
	} else {
		var err error
		if priv, err = ecdh.X25519().GenerateKey(rand.Reader); err != nil {
			return err
		}
	}
	lock := newConnLock()
	sealed := lock.String() + "|" + lock.box(newPassword).seal("superuser", base64.StdEncoding.EncodeToString(priv.Bytes()))
	if err := s.setMeta("superuser_priv", s.box.seal("superuser", sealed)); err != nil {
		return err
	}
	return s.setMeta("superuser_pub", base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()))
}

// openSuperuser returns the superuser private key; a wrong password is
// errLockWrong.
func (s *Store) openSuperuser(password string) (*ecdh.PrivateKey, error) {
	enc, err := s.meta("superuser_priv")
	if err != nil {
		return nil, err
	}
	if enc == "" {
		return nil, errNoSuperuser
	}
	sealed, err := s.box.open("superuser", enc)
	if err != nil {
		return nil, err
	}
	spec, data, ok := strings.Cut(sealed, "|")
	if !ok {
		return nil, errors.New("corrupt superuser key")
	}
	lock, err := parseConnLock(spec)
	if err != nil {
		return nil, err
	}
	b64, err := lock.box(password).open("superuser", data)
	if err != nil {
		return nil, errLockWrong
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	return ecdh.X25519().NewPrivateKey(raw)
}

// escrowFor seals a connection password to the superuser's public key, or
// returns "" when there is no superuser.
func (s *Store) escrowFor(connPassword string) (string, error) {
	pubB64, err := s.meta("superuser_pub")
	if err != nil || pubB64 == "" {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		return "", err
	}
	pub, err := ecdh.X25519().NewPublicKey(raw)
	if err != nil {
		return "", err
	}
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	aead, err := escrowAEAD(eph, pub, eph.PublicKey().Bytes())
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	rand.Read(nonce)
	out := append(append(eph.PublicKey().Bytes(), nonce...), aead.Seal(nil, nonce, []byte(connPassword), []byte("cactusdb:escrow"))...)
	return s.box.seal("escrow", escrowPrefix+base64.StdEncoding.EncodeToString(out)), nil
}

// openEscrow recovers a connection password with the superuser private key.
func (s *Store) openEscrow(priv *ecdh.PrivateKey, escrow string) (string, error) {
	if escrow == "" {
		return "", errNoEscrow
	}
	plain, err := s.box.open("escrow", escrow)
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(plain, escrowPrefix))
	if err != nil || len(raw) < 32+12 {
		return "", errors.New("corrupt escrow")
	}
	ephPub, err := ecdh.X25519().NewPublicKey(raw[:32])
	if err != nil {
		return "", err
	}
	aead, err := escrowAEAD(priv, ephPub, raw[:32])
	if err != nil {
		return "", err
	}
	n := aead.NonceSize()
	pw, err := aead.Open(nil, raw[32:32+n], raw[32+n:], []byte("cactusdb:escrow"))
	if err != nil {
		return "", errLockWrong
	}
	return string(pw), nil
}

// escrowAEAD derives the AES-GCM key for one escrow from an X25519 exchange.
func escrowAEAD(priv *ecdh.PrivateKey, pub *ecdh.PublicKey, ephPub []byte) (cipher.AEAD, error) {
	shared, err := priv.ECDH(pub)
	if err != nil {
		return nil, err
	}
	key, err := hkdf.Key(sha256.New, shared, ephPub, "cactusdb superuser escrow", 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// setEscrow stores a connection's password sealed for the superuser.
func (s *Store) setEscrow(id int64, connPassword string) error {
	esc, err := s.escrowFor(connPassword)
	if err != nil || esc == "" {
		return err
	}
	_, err = s.db.Exec(`UPDATE connections SET escrow = ? WHERE id = ?`, esc, id)
	return err
}

// UnlockAsSuperuser opens a saved connection with the superuser password.
func (s *Store) UnlockAsSuperuser(id int64, superPassword string) (SavedConn, *secretBox, error) {
	priv, err := s.openSuperuser(superPassword)
	if err != nil {
		return SavedConn{}, nil, err
	}
	var lock, escrow string
	if err := s.db.QueryRow(`SELECT lock, escrow FROM connections WHERE id = ?`, id).Scan(&lock, &escrow); err != nil {
		return SavedConn{}, nil, err
	}
	if lock == "" { // no connection password: nothing to recover
		return s.Unlock(id, "")
	}
	pw, err := s.openEscrow(priv, escrow)
	if err != nil {
		return SavedConn{}, nil, err
	}
	return s.Unlock(id, pw)
}

// CheckSuperuser checks the superuser password.
func (s *Store) CheckSuperuser(password string) error {
	_, err := s.openSuperuser(password)
	return err
}

// superuserFromEnv sets the superuser password from CACTUSDB_SUPERUSER_PASSWORD
// when the store has none yet; it never changes an existing one.
func (s *Store) superuserFromEnv(env string) (string, error) {
	if env == "" {
		return "", nil
	}
	if s.SuperuserSet() {
		if s.CheckSuperuser(env) != nil {
			return "", fmt.Errorf("%s doesn't match the superuser password already set; it was not changed (use cactusdb --superuser set)", superuserEnv)
		}
		return "", nil
	}
	if err := s.SetSuperuser(env, ""); err != nil {
		return "", err
	}
	return "superuser password set from " + superuserEnv, nil
}

// ---- web --------------------------------------------------------------------

func (s *server) handleSuperuserStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]bool{"set": s.store.SuperuserSet()})
}

// handleSuperuserReveal checks the superuser password and lists every saved
// connection with its details (never its database password).
func (s *server) handleSuperuserReveal(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeErr(w, 415, errors.New("expected a JSON request"))
		return
	}
	var req struct {
		SuperPassword string `json:"superPassword"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if wait := s.throttle.wait(superThrottleID); wait > 0 {
		writeErr(w, 429, fmt.Errorf("too many wrong passwords: try again in %d seconds", int(wait.Seconds())+1))
		return
	}
	switch err := s.store.CheckSuperuser(req.SuperPassword); {
	case errors.Is(err, errLockWrong):
		s.throttle.failed(superThrottleID)
		logAction(r, "superuser sign-in FAILED: wrong superuser password")
		writeJSON(w, 403, map[string]any{"error": "wrong superuser password", "locked": true})
		return
	case errors.Is(err, errNoSuperuser):
		writeErr(w, 409, err)
		return
	case err != nil:
		writeErr(w, 500, err)
		return
	}
	s.throttle.ok(superThrottleID)
	list, err := s.store.List()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	type info struct {
		savedConnInfo
		NeedsOwnPassword bool `json:"needsOwnPassword,omitempty"` // saved before the superuser password; not openable by it yet
	}
	out := []info{}
	for _, l := range list {
		c, _, err := s.store.UnlockAsSuperuser(l.ID, req.SuperPassword)
		i := info{savedConnInfo: savedConnInfo{ID: l.ID, Name: l.Name, Engine: l.Engine, Host: l.Host, Port: l.Port,
			Database: l.Database, Locked: l.Lock != "", Created: l.Created}}
		if err == nil {
			i.Username, i.HasPassword = c.Username, c.Password != ""
		} else {
			i.NeedsOwnPassword = true
		}
		out = append(out, i)
	}
	logAction(r, "superuser signed in")
	writeJSON(w, 200, out)
}

// ---- command line -------------------------------------------------------------

// superuserCLI is --superuser set|status.
func superuserCLI(st *Store, action string) error {
	switch action {
	case "status":
		if st.SuperuserSet() {
			fmt.Println("A superuser password is set: it opens every saved connection.")
		} else {
			fmt.Println("No superuser password is set. Set one with: cactusdb --superuser set")
		}
		return nil
	case "set":
	default:
		return fmt.Errorf("--superuser must be set or status, not %q", action)
	}
	old := ""
	if st.SuperuserSet() {
		var err error
		if old, err = promptPassword("Current superuser password: "); err != nil {
			return err
		}
		if err := st.CheckSuperuser(old); err != nil {
			return errors.New("that is not the current superuser password")
		}
	}
	pw, err := promptPassword("New superuser password (at least 10 characters): ")
	if err != nil {
		return err
	}
	again, err := promptPassword("Type it again: ")
	if err != nil {
		return err
	}
	if pw != again {
		return errors.New("the two passwords differ; nothing was changed")
	}
	if err := st.SetSuperuser(pw, old); err != nil {
		return err
	}
	fmt.Println("Superuser password set. It opens every connection saved from now on; connections saved")
	fmt.Println("before can be opened by it once they have been opened with their own password.")
	return nil
}
