package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestNewerVersion(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"1.19.0", "1.18.0", true},
		{"1.10.0", "1.9.2", true},
		{"1.18.0", "1.18.0", false},
		{"1.18", "1.18.0", false},
		{"1.18.1", "1.18", true},
		{"1.17.9", "1.18.0", false},
		{"2.0.0", "1.99.99", true},
	} {
		if got := newerVersion(c.a, c.b); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// The VERSION file is what running cactusdbs compare themselves with, so it
// must name the version the binaries are built as.
func TestVersionFile(t *testing.T) {
	b, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatal(err)
	}
	if v := strings.TrimSpace(string(b)); v != version {
		t.Fatalf("VERSION says %q but main.go's version is %q: change both", v, version)
	}
}

func TestLatestVersion(t *testing.T) {
	answer := "9.0.0\n"
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if answer == "" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(answer))
	}))
	defer gh.Close()
	old := latestURL
	latestURL = gh.URL + "/VERSION"
	defer func() { latestURL = old; lastUpdate = updateCheck{} }()

	v, err := latestVersion(context.Background(), true)
	if err != nil || v != "9.0.0" {
		t.Fatalf("latestVersion = %q, %v", v, err)
	}
	if u := updateJSON(v, err); u["newer"] != true || u["current"] != version {
		t.Fatalf("updateJSON = %v", u)
	}

	answer = "" // cached for an hour: GitHub isn't asked again
	if v, err := latestVersion(context.Background(), false); err != nil || v != "9.0.0" {
		t.Fatalf("cached latestVersion = %q, %v", v, err)
	}
	if _, err := latestVersion(context.Background(), true); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("want a 404 error, got %v", err)
	}
	answer = "<html>not a version</html>"
	if _, err := latestVersion(context.Background(), true); err == nil {
		t.Fatal("want an error for a page that isn't a version")
	}
	if u := updateJSON("", errors.New("offline")); u["newer"] != false || u["error"] != "offline" {
		t.Fatalf("updateJSON with an error = %v", u)
	}
}
