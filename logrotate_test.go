package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDailyLogRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cactusdb.log")
	yesterday := time.Now().AddDate(0, 0, -1)
	day := yesterday.Format(dayFormat)
	os.WriteFile(path, []byte("from yesterday\n"), 0o600)
	os.Chtimes(path, yesterday, yesterday)
	old := time.Now().AddDate(0, 0, -(logKeepDays + 5)).Format(dayFormat)
	os.WriteFile(datedName(path, old), []byte("too old\n"), 0o600)

	d, err := openDailyLog(path) // yesterday's file is renamed, the too-old one deleted
	if err != nil {
		t.Fatal(err)
	}
	d.Write([]byte("today 1\n"))
	if b, _ := os.ReadFile(datedName(path, day)); string(b) != "from yesterday\n" {
		t.Fatalf("dated file: %q", b)
	}
	if fileExists(datedName(path, old)) {
		t.Error("a file older than logKeepDays was kept")
	}

	d.day = day // as if the day changed while running
	d.Write([]byte("today 2\n"))
	d.Close()
	if b, _ := os.ReadFile(datedName(path, day)); string(b) != "from yesterday\ntoday 1\n" {
		t.Fatalf("after the day changed, the dated file is %q", b)
	}
	if b, _ := os.ReadFile(path); string(b) != "today 2\n" {
		t.Fatalf("today's file is %q", b)
	}
	if got := strings.Join(logDays(path), ","); got != today()+","+day {
		t.Fatalf("logDays = %s", got)
	}
	if logFileFor(path, today()) != path || logFileFor(path, day) != datedName(path, day) {
		t.Fatal("logFileFor")
	}
}
