package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMoveConnDB(t *testing.T) {
	t.Setenv(passphraseEnv, "")
	exe := t.TempDir()
	def := filepath.Join(exe, "cactus.conn.db")
	st, err := openStore(def, filepath.Join(exe, keyFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { st.DB().Close() }()
	if _, err := st.Save(SavedConn{Name: "shop", ConnParams: ConnParams{Engine: EngineMariaDB, Host: "db1", Username: "me", Password: "secret"}}); err != nil {
		t.Fatal(err)
	}

	// A folder: the file keeps its name; the folder is created.
	target, err := checkDBPath(filepath.Join(t.TempDir(), "data")+"/", def)
	if err != nil {
		t.Fatal(err)
	}
	res, err := st.moveTo(context.Background(), target, exe, def)
	if err != nil {
		t.Fatal(err)
	}
	newDir := filepath.Dir(target)
	if res.To != filepath.Join(newDir, "cactus.conn.db") || res.KeyTo != filepath.Join(newDir, keyFileName) || len(res.Warnings) > 0 {
		t.Fatalf("result %+v", res)
	}
	for _, f := range []string{def, filepath.Join(exe, keyFileName)} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("%s still exists", f)
		}
	}
	if got := movedConnDB(exe); got != target {
		t.Errorf("location file says %q", got)
	}
	// Still works, and the moved files open on their own (the next start).
	if l, err := st.List(); err != nil || len(l) != 1 || l[0].Password != "secret" {
		t.Fatalf("after the move: %+v %v", l, err)
	}
	st2, err := openStore(target, filepath.Join(newDir, keyFileName))
	if err != nil {
		t.Fatal(err)
	}
	if l, err := st2.List(); err != nil || len(l) != 1 || l[0].Password != "secret" {
		t.Fatalf("reopened: %+v %v", l, err)
	}
	st2.DB().Close()

	// Never over an existing file.
	if _, err := st.moveTo(context.Background(), target, exe, def); err == nil {
		t.Error("moved onto itself")
	}
	other := filepath.Join(t.TempDir(), "x.db")
	os.WriteFile(other, []byte("x"), 0o600)
	if _, err := st.moveTo(context.Background(), other, exe, def); err == nil {
		t.Error("overwrote an existing file")
	}

	// Back to the default place: the location file goes.
	if _, err := st.moveTo(context.Background(), def, exe, def); err != nil {
		t.Fatal(err)
	}
	if movedConnDB(exe) != "" {
		t.Error("location file left behind")
	}
	if _, err := os.Stat(filepath.Join(exe, keyFileName)); err != nil {
		t.Errorf("key not moved back: %v", err)
	}
	if _, err := checkDBPath("relative/x.db", def); err == nil {
		t.Error("relative path accepted")
	}
}
