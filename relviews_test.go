package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The saved views of DB Relationships are sealed with the connection's key:
// saved, replaced by name, re-sealed with a new connection password, gone
// with the connection, and never in plain text on disk.
func TestRelViewsSealedWithConnection(t *testing.T) {
	st := newTestServer(t).store
	id, box, err := st.SaveLocked(SavedConn{Name: "library", ConnParams: ConnParams{Engine: EngineMariaDB, Host: "db1", Database: "opt"}}, "correct horse", nil)
	if err != nil {
		t.Fatal(err)
	}
	view := json.RawMessage(`{"v":1,"chosen":["table:opt_secret_titles"],"pos":{"table:opt_secret_titles":[10,20]}}`)
	vid, err := st.SaveRelView(id, box, RelView{Name: "Catalogue", View: view})
	if err != nil {
		t.Fatal(err)
	}
	// The same name (any case) saves over it; another view gets its own id.
	if again, err := st.SaveRelView(id, box, RelView{Name: "catalogue", View: json.RawMessage(`{"v":1,"chosen":["table:opt_secret_titles","table:opt_main"]}`)}); err != nil || again != vid {
		t.Errorf("saving over by name: id %d (want %d) %v", again, vid, err)
	}
	other, _ := st.SaveRelView(id, box, RelView{Name: "Users", View: json.RawMessage(`{"v":1}`)})
	if _, err := st.SaveRelView(id, box, RelView{ID: other, Name: "Catalogue", View: view}); err == nil {
		t.Error("renaming a view to another view's name was allowed")
	}
	if _, err := st.RelViews(id, nil); !errors.Is(err, errLockRequired) {
		t.Errorf("views opened without the connection key: %v", err)
	}
	// A new connection password re-seals them.
	c, box2, _ := st.Unlock(id, "correct horse")
	if _, _, err := st.SaveLocked(SavedConn{ID: id, Name: "library", ConnParams: c.ConnParams}, "new password!", box2); err != nil {
		t.Fatal(err)
	}
	_, box3, _ := st.Unlock(id, "new password!")
	vs, err := st.RelViews(id, box3)
	if err != nil || len(vs) != 2 || vs[0].Name != "catalogue" || vs[1].Name != "Users" || !bytes.Contains(vs[0].View, []byte("opt_main")) {
		t.Fatalf("views after a new password: %+v %v", vs, err)
	}
	st.DB().Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(dbPathOf(t, st)), "*"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, secret := range []string{"opt_secret_titles", "Catalogue", "catalogue"} {
			if bytes.Contains(b, []byte(secret)) {
				t.Errorf("%s holds %q in plain text", filepath.Base(f), secret)
			}
		}
	}
	if err := st.DeleteRelView(id, other); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteRelView(id, other); err == nil {
		t.Error("deleted a view twice")
	}
	st.Delete(id)
	var n int
	st.DB().QueryRow(`SELECT COUNT(*) FROM rel_views`).Scan(&n)
	if n != 0 {
		t.Errorf("%d views left after deleting the connection", n)
	}
}
