package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
)

// Export job states.
const (
	jobWaiting int32 = iota
	jobCounting
	jobRunning
	jobDone
	jobFailed
)

type exportJob struct {
	table tableRef
	state atomic.Int32
	total atomic.Int64 // row count from COUNT(*); -1 when unknown
	rows  atomic.Int64 // rows written so far
	start time.Time

	// Set before state becomes jobDone or jobFailed.
	path    string
	err     error
	elapsed time.Duration
}

// exportBatch exports several tables in parallel, each on its own
// connection, into one folder. done is closed when every job has finished.
type exportBatch struct {
	format string
	jobs   []*exportJob
	done   chan struct{}
}

// startExportBatch starts exporting targets, threads at a time, and returns
// at once. finished, if not nil, is called as each table completes.
func startExportBatch(ctx context.Context, engine string, db *sql.DB, dir, format string, targets []tableRef, threads int, finished func(*exportJob)) *exportBatch {
	threads = max(1, min(threads, len(targets)))
	if engine == EngineSQLite {
		// openDB allows one SQLite connection; every extra one is opened
		// read-only with the same settings.
		db.SetMaxOpenConns(threads)
	}
	b := &exportBatch{format: format, jobs: make([]*exportJob, len(targets)), done: make(chan struct{})}
	for i, t := range targets {
		b.jobs[i] = &exportJob{table: t}
		b.jobs[i].total.Store(-1)
	}
	if finished == nil {
		finished = func(*exportJob) {}
	}
	queue := make(chan *exportJob)
	var wg sync.WaitGroup
	for range threads {
		wg.Go(func() {
			for j := range queue {
				runExportJob(ctx, engine, db, dir, format, j)
				finished(j)
			}
		})
	}
	go func() {
		defer close(b.done)
		for _, j := range b.jobs {
			queue <- j // after cancelling, the workers fail the rest quickly
		}
		close(queue)
		wg.Wait()
	}()
	return b
}

// runExports exports targets with up to o.Threads tables in parallel and
// reports progress to out: live progress bars when o.Progress is set,
// otherwise one line per finished table.
func runExports(ctx context.Context, o cliOptions, db *sql.DB, targets []tableRef, out io.Writer) error {
	threads := max(1, min(o.Threads, len(targets)))
	fmt.Fprintf(out, "Exporting %d table(s) as %s to %s (%d at a time)\n", len(targets), o.Format, exportSubdir(o.ExportDir, exportFormats[o.Format]), threads)

	var outMu sync.Mutex
	finished := func(j *exportJob) {
		if o.Progress {
			return // the progress display shows it
		}
		outMu.Lock()
		defer outMu.Unlock()
		if j.err != nil {
			fmt.Fprintf(out, "  %-30s FAILED: %v\n", j.table.name, j.err)
		} else {
			fmt.Fprintf(out, "  %-30s %9d rows  %s\n", j.table.name, j.rows.Load(), j.path)
		}
	}
	b := startExportBatch(ctx, o.Engine, db, o.ExportDir, o.Format, targets, threads, finished)
	stopDraw := func() {}
	if o.Progress {
		stopDraw = drawProgress(out, b.jobs)
	}
	<-b.done
	stopDraw()

	failed := 0
	for _, j := range b.jobs {
		if j.err != nil {
			failed++
		}
	}
	if o.Progress {
		fmt.Fprintln(out, "Files:")
		for _, j := range b.jobs {
			if j.err == nil {
				fmt.Fprintf(out, "  %s\n", j.path)
			}
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d export(s) failed", failed, len(b.jobs))
	}
	return nil
}

func runExportJob(ctx context.Context, engine string, db *sql.DB, dir, format string, j *exportJob) {
	j.start = time.Now()
	defer func() {
		// The job runs in its own goroutine, where a panic would stop all of
		// cactusdb: fail this table instead.
		if v := recover(); v != nil {
			log.Printf("export %s: INTERNAL ERROR: %v\n%s", j.table.name, v, debug.Stack())
			j.err = fmt.Errorf("internal error in cactusdb while exporting %s: %v (the details are in the cactusdb log)", j.table.name, v)
		}
		j.elapsed = time.Since(j.start)
		if j.err != nil {
			j.state.Store(jobFailed)
		} else {
			j.state.Store(jobDone)
		}
	}()
	if err := ctx.Err(); err != nil {
		j.err = err
		return
	}
	// The row count is only for the progress bar; exporting works without it.
	j.state.Store(jobCounting)
	if n, err := countRows(ctx, engine, db, j.table.name); err == nil {
		j.total.Store(n)
	}
	j.state.Store(jobRunning)
	q, err := buildSelect(ctx, engine, db, j.table.name)
	if err != nil {
		j.err = err
		return
	}
	var n int
	j.path, n, j.err = exportToDir(ctx, dir, engine, db, q, format, j.table.name, func(rows int) {
		j.rows.Store(int64(rows))
	})
	j.rows.Store(int64(n))
}

// countRows counts the rows of a table, through the same read-only path as
// every other query.
func countRows(ctx context.Context, engine string, db *sql.DB, table string) (int64, error) {
	var n int64 = -1
	err := streamQuery(ctx, engine, db, "SELECT COUNT(*) FROM "+quoteObject(engine, table), func([]string) error { return nil },
		func(row []any) (bool, error) {
			if len(row) > 0 {
				n, _ = strconv.ParseInt(fmt.Sprint(row[0]), 10, 64)
			}
			return false, nil
		})
	if err == nil && n < 0 {
		err = fmt.Errorf("no row count")
	}
	return n, err
}

// drawProgress redraws one progress bar per job in place until the returned
// function is called, which draws the final state.
func drawProgress(out io.Writer, jobs []*exportJob) (stop func()) {
	nameW := 10
	for _, j := range jobs {
		nameW = max(nameW, utf8.RuneCountInString(j.table.name))
	}
	nameW = min(nameW, 28)
	width := 100
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 20 {
		width = w
	}

	frame := 0
	draw := func(first bool) {
		var b strings.Builder
		if !first {
			fmt.Fprintf(&b, "\x1b[%dA", len(jobs)) // back to the first bar
		}
		for _, j := range jobs {
			line := progressLine(j, nameW, frame)
			if utf8.RuneCountInString(line) > width-1 {
				line = string([]rune(line)[:width-2]) + "…"
			}
			b.WriteString("\r\x1b[2K" + line + "\n")
		}
		out.Write([]byte(b.String()))
		frame++
	}

	draw(true)
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		t := time.NewTicker(150 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				draw(false)
				return
			case <-t.C:
				draw(false)
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

var (
	barFull, barEmpty = "█", "░"
	tick, cross       = "✔", "✘"
)

func init() {
	if runtime.GOOS == "windows" { // older consoles lack the block glyphs
		barFull, barEmpty, tick, cross = "#", "-", "+", "x"
	}
}

const barWidth = 24

func progressLine(j *exportJob, nameW, frame int) string {
	name := j.table.name
	if r := []rune(name); len(r) > nameW {
		name = string(r[:nameW-1]) + "…"
	}
	prefix := fmt.Sprintf("  %-*s ", nameW, name)
	rows, total := j.rows.Load(), j.total.Load()
	switch j.state.Load() {
	case jobWaiting:
		return prefix + "[" + strings.Repeat(" ", barWidth) + "]  waiting"
	case jobCounting:
		return prefix + "[" + strings.Repeat(" ", barWidth) + "]  counting rows…"
	case jobFailed:
		return prefix + cross + " FAILED: " + strings.ReplaceAll(fmt.Sprint(j.err), "\n", " ")
	case jobDone:
		return fmt.Sprintf("%s[%s] %s %s rows  %s", prefix, strings.Repeat(barFull, barWidth), tick,
			groupDigits(rows), j.elapsed.Round(100*time.Millisecond))
	}
	elapsed := time.Since(j.start).Round(100 * time.Millisecond)
	if total <= 0 { // unknown size: a bouncing block
		pos := frame % (2 * (barWidth - 3))
		if pos >= barWidth-3 {
			pos = 2*(barWidth-3) - pos
		}
		bar := strings.Repeat(barEmpty, pos) + strings.Repeat(barFull, 3) + strings.Repeat(barEmpty, barWidth-3-pos)
		return fmt.Sprintf("%s[%s]  %s rows  %s", prefix, bar, groupDigits(rows), elapsed)
	}
	pct := min(100, int(rows*100/total))
	filled := min(barWidth, int(rows*barWidth/total))
	return fmt.Sprintf("%s[%s%s] %3d%%  %s/%s rows  %s", prefix, strings.Repeat(barFull, filled),
		strings.Repeat(barEmpty, barWidth-filled), pct, groupDigits(rows), groupDigits(total), elapsed)
}

// groupDigits formats 1234567 as 1,234,567.
func groupDigits(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}
