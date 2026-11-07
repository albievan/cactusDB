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
	got, size, err := tailLines(path, 3)
	if err != nil || strings.Join(got, ",") != "line 2998,line 2999,line 3000" || size != int64(b.Len()) {
		t.Fatalf("tailLines = %q, %d, %v", got, size, err)
	}

	// The live view: only what was added, whole lines only.
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	f.WriteString("new 1\nnew 2\npart")
	f.Close()
	lines, end, reset, err := linesAfter(path, size)
	if err != nil || reset || strings.Join(lines, ",") != "new 1,new 2" || end != size+int64(len("new 1\nnew 2\n")) {
		t.Fatalf("linesAfter = %q, %d, %v, %v", lines, end, reset, err)
	}
	if _, _, reset, _ := linesAfter(path, 1<<40); !reset {
		t.Fatal("a file shorter than the offset (a new day) must reset")
	}

	os.WriteFile(path, nil, 0o600)
	if got, _, err := tailLines(path, 10); err != nil || len(got) != 0 {
		t.Fatalf("empty file: %q, %v", got, err)
	}
}

func TestLogMemory(t *testing.T) {
	m := &logMemory{}
	for i := 1; i <= logMemLines+10; i++ {
		fmt.Fprintf(m, "line %d\n", i)
	}
	if l, total, ok := m.since(logMemLines + 8); !ok || total != logMemLines+10 || len(l) != 2 {
		t.Fatalf("since = %d lines, total %d, %v", len(l), total, ok)
	}
	if _, _, ok := m.since(3); ok {
		t.Fatal("lines no longer kept must not be reported as complete")
	}
	got := m.last(2)
	if len(m.lines) != logMemLines || strings.Join(got, ",") != fmt.Sprintf("line %d,line %d", logMemLines+9, logMemLines+10) {
		t.Fatalf("kept %d lines, last %q", len(m.lines), got)
	}
}
