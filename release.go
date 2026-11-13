package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Releases are signed: build.sh signs release/SHA256SUMS with a private
// Ed25519 key kept outside the repository, and the public key is built into
// the binaries (releaseKey, set with -ldflags). An update from GitHub is only
// installed when SHA256SUMS carries a valid signature by that key, so a
// change pushed to the repository by anyone else can't reach a server.

// releaseKey is the base64 public key releases are signed with ("" in a
// build that isn't a release: its updates aren't checked for a signature).
var releaseKey = ""

// releaseCommand runs build.sh's helpers: release-keygen <key file> makes a
// key (once), release-pubkey <key file> prints its public key, and
// release-sign <key file> <file> writes <file>.sig. ok is false for any
// other command line.
func releaseCommand(args []string) (ok bool, err error) {
	if len(args) < 2 || !strings.HasPrefix(args[0], "release-") {
		return false, nil
	}
	keyFile := args[1]
	switch args[0] {
	case "release-keygen":
		if _, err := os.Stat(keyFile); err == nil {
			return true, fmt.Errorf("%s exists already: a new key would make every installed cactusdb refuse the next update", keyFile)
		}
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return true, err
		}
		if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
			return true, err
		}
		f, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return true, err
		}
		defer f.Close()
		_, err = f.WriteString(base64.StdEncoding.EncodeToString(priv.Seed()) + "\n")
		return true, err
	case "release-pubkey":
		priv, err := readReleaseKey(keyFile)
		if err != nil {
			return true, err
		}
		fmt.Println(base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)))
		return true, nil
	case "release-sign":
		if len(args) < 3 {
			return true, errors.New("usage: release-sign <key file> <file>")
		}
		priv, err := readReleaseKey(keyFile)
		if err != nil {
			return true, err
		}
		b, err := os.ReadFile(args[2])
		if err != nil {
			return true, err
		}
		sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, b))
		return true, os.WriteFile(args[2]+".sig", []byte(sig+"\n"), 0o644)
	}
	return false, nil
}

func readReleaseKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s isn't a release key", path)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// verifyRelease checks sig (base64, from SHA256SUMS.sig) over sums with the
// built-in key. A build without a key can't check, and says so.
func verifyRelease(sums, sig []byte) error {
	if releaseKey == "" {
		return nil
	}
	pub, err := base64.StdEncoding.DecodeString(releaseKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("this cactusdb has a damaged release key")
	}
	s, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || !ed25519.Verify(ed25519.PublicKey(pub), sums, s) {
		return errors.New("SHA256SUMS isn't signed with the cactusdb release key: the update isn't installed")
	}
	return nil
}
