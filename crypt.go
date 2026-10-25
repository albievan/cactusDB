package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Saved usernames and passwords are encrypted with AES-256-GCM. The key is
// either derived from the CACTUSDB_PASSPHRASE environment variable or read
// from a random key file (cactus.key) kept next to cactus.conn.db.

const (
	encPrefix        = "enc:v1:"
	keyFileName      = "cactus.key"
	passphraseEnv    = "CACTUSDB_PASSPHRASE"
	pbkdf2Iterations = 600_000
	keyCheckPlain    = "cactusdb key check"
)

var errWrongKey = errors.New("wrong key")

type secretBox struct {
	aead cipher.AEAD
}

func newSecretBox(key []byte) (*secretBox, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &secretBox{aead: aead}, nil
}

// seal encrypts plain. field is authenticated with it, so a ciphertext
// cannot be moved into another column.
func (b *secretBox) seal(field, plain string) string {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	out := b.aead.Seal(nonce, nonce, []byte(plain), []byte("cactusdb:"+field))
	return encPrefix + base64.StdEncoding.EncodeToString(out)
}

func (b *secretBox) open(field, sealed string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, encPrefix))
	if err != nil || len(raw) < b.aead.NonceSize() {
		return "", errors.New("corrupt encrypted value")
	}
	n := b.aead.NonceSize()
	plain, err := b.aead.Open(nil, raw[:n], raw[n:], []byte("cactusdb:"+field))
	if err != nil {
		return "", errWrongKey
	}
	return string(plain), nil
}

func isSealed(s string) bool { return strings.HasPrefix(s, encPrefix) }

// keySource describes where the encryption key comes from, for messages.
type keySource struct {
	desc string
	key  []byte
}

// loadKey returns the passphrase-derived key when CACTUSDB_PASSPHRASE is set
// (salt is a random per-store value), otherwise the key in keyFile. A missing
// key file is created with a fresh random key only when create is true, i.e.
// for a store that holds no encrypted data yet.
func loadKey(keyFile string, salt []byte, create bool) (keySource, error) {
	if pass := os.Getenv(passphraseEnv); pass != "" {
		key, err := pbkdf2.Key(sha256.New, pass, salt, pbkdf2Iterations, 32)
		return keySource{desc: "the " + passphraseEnv + " passphrase", key: key}, err
	}
	data, err := os.ReadFile(keyFile)
	switch {
	case err == nil:
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if err != nil || len(key) != 32 {
			return keySource{}, fmt.Errorf("key file %s is not a valid cactusdb key", keyFile)
		}
		return keySource{desc: "key file " + keyFile, key: key}, nil
	case errors.Is(err, os.ErrNotExist) && !create:
		return keySource{}, fmt.Errorf("key file %s is missing, but the saved connections were encrypted with it.\n"+
			"  Restore %s, or set the %s that was used before,\n"+
			"  or start with an empty file using --conn-db <new file>", keyFile, keyFileName, passphraseEnv)
	case errors.Is(err, os.ErrNotExist):
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return keySource{}, err
		}
		if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
			return keySource{}, err
		}
		// O_EXCL: never overwrite a key another process just created.
		f, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return keySource{}, fmt.Errorf("cannot create key file %s: %w", keyFile, err)
		}
		_, werr := f.WriteString(base64.StdEncoding.EncodeToString(key) + "\n")
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			os.Remove(keyFile)
			return keySource{}, werr
		}
		return keySource{desc: "new key file " + keyFile, key: key}, nil
	default:
		return keySource{}, fmt.Errorf("cannot read key file %s: %w", keyFile, err)
	}
}
