package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTailLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cactusdb.log")
	var b strings.Builder
	for i := 1; i <= 3000; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	os.WriteFile(path, []byte(b.String()), 0o600)
	got, err := tailLines(path, 3)
	if err != nil || strings.Join(got, ",") != "line 2998,line 2999,line 3000" {
		t.Fatalf("tailLines = %q, %v", got, err)
	}
	os.WriteFile(path, nil, 0o600)
	if got, err := tailLines(path, 10); err != nil || len(got) != 0 {
		t.Fatalf("empty file: %q, %v", got, err)
	}
}

func TestLogMemory(t *testing.T) {
	m := &logMemory{}
	for i := 1; i <= logMemLines+10; i++ {
		fmt.Fprintf(m, "line %d\n", i)
	}
	got := m.last(2)
	if len(m.lines) != logMemLines || strings.Join(got, ",") != fmt.Sprintf("line %d,line %d", logMemLines+9, logMemLines+10) {
		t.Fatalf("kept %d lines, last %q", len(m.lines), got)
	}
}
