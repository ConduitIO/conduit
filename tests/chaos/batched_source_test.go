// Copyright © 2026 Meroxa, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build linux || darwin

package chaos

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	json "github.com/goccy/go-json"
)

// Batched-source chaos (docs/design-documents/20261027-default-source-batching.md,
// "Failure modes" and "Test plan"). These tests run the real conduit binary
// with the built-in file source left at its SDK v0.18 default (sdk.batch.size
// 100, sdk.batch.delay 0, adaptive drain) and the built-in file destination,
// on both pipeline engines, because the classic engine is still the default.
// They are what backs the claim that default source batching keeps invariants
// 1, 3, 4 and 7:
//
//   - TestSIGKILL_BatchedSource_WhileCollecting: SIGKILL while the SDK holds
//     read-ahead and a partly collected batch, after the persister flushed a
//     position: the restart resumes from that position (not from zero), loses
//     nothing and keeps order. It does not by itself see an ack-ordering bug.
//   - TestSIGKILL_BatchedSource_AfterWriteBeforePersist: SIGKILL long after
//     the destination wrote more than one persister window; the replay is
//     bounded by the window, which counts positions.
//   - TestSIGKILL_BatchedSource_StalledDestination_NoLoss: the destination is
//     a named pipe that nobody reads, so its Write blocks and stays blocked
//     while the test controls exactly what it has written. The source is
//     killed while stalled, after persister flushes. A persisted position
//     must never cover a record the destination did not write: this is the
//     deterministic invariant 1 detector, on both engines.
//   - TestSIGKILL_BatchedSource_NSource: two file sources into one destination
//     (per-source order and no loss across a kill).
//   - TestSIGTERM_BatchedSource_GracefulDrain: SIGTERM mid-stream drains,
//     checkpoints and exits; resuming loses nothing and duplicates nothing.
//
// Every kill test also asserts per-source order (invariant 4): a source's
// records may go backwards only once per restart, where the replay begins.
// Each test asserts the resolved batch mode from the SDK's own startup log
// line, so a regression of the default cannot turn these into unbatched runs
// that still pass.

const (
	batchedInputName  = "input.txt"
	batchedOutputName = "output.txt"

	// persisterWindow is connector.DefaultPersisterBundleCountThreshold: a
	// crash may replay up to this many positions (the persister flushes when
	// the count reaches it). Written out here, not imported, so that a change
	// to the engine's default has to be noticed in this test.
	persisterWindow = 10000
	// replayCredit covers what keeps flowing while a flush is in flight: one
	// source batch (100), the SDK read-ahead (100), a batch in the destination
	// and the records that pass during the persister's write transaction. The
	// flush is asynchronous, so the bound is the window plus this credit.
	replayCredit = 2000

	batchedProcessTimeout = 90 * time.Second
	batchedExitTimeout    = 20 * time.Second
	// batchedQuiet is how long the destination file may stay unchanged before
	// the pipeline counts as idle. A tailing file source delivers everything
	// it can promptly, so an idle pipeline with records missing is data loss;
	// a pipeline that is still delivering is never failed for being slow.
	batchedQuiet = 10 * time.Second
	// batchedOverall is a backstop for a pipeline that keeps delivering but
	// never finishes; it is reported as a timeout, not as loss.
	batchedOverall = 10 * time.Minute
)

type batchedEngine struct {
	name  string
	flags []string
}

var batchedEngines = []batchedEngine{
	{name: "classic"},
	{name: "arch-v2", flags: []string{"--preview.pipeline-arch-v2"}},
}

// batchedOpts shapes the working directory.
type batchedOpts struct {
	// lines is how many numbered lines each source starts with.
	lines int
	// sources is the number of file sources (default 1). Source k (0-based)
	// holds the numbers [k*sourceStride+1, k*sourceStride+lines].
	sources int
	// fifo makes the destination a named pipe at output.fifo instead of a file.
	fifo bool
}

// sourceStride separates the number ranges of the sources in an N-source run.
const sourceStride = 10_000_000

func inputPath(dir string, k int) string {
	return filepath.Join(dir, fmt.Sprintf("input-%d.txt", k))
}

func fifoPath(dir string) string { return filepath.Join(dir, "output.fifo") }

// batchedDir lays out a working directory with file sources over numbered
// lines and a destination. No connector sets sdk.batch.*: the default is the
// point.
func batchedDir(t *testing.T, o batchedOpts) string {
	t.Helper()
	if o.sources == 0 {
		o.sources = 1
	}
	dir := t.TempDir()
	for _, d := range []string{"pipelines", "connectors", "processors"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dest := filepath.Join(dir, batchedOutputName)
	if o.fifo {
		dest = fifoPath(dir)
		if err := syscall.Mkfifo(dest, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var p strings.Builder
	p.WriteString("version: \"2.2\"\npipelines:\n  - id: batched\n    status: running\n    connectors:\n")
	for k := 0; k < o.sources; k++ {
		if err := appendLines(inputPath(dir, k), k*sourceStride+1, k*sourceStride+o.lines); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&p, "      - id: src%d\n        type: source\n        plugin: builtin:file\n        settings:\n          path: %q\n", k, inputPath(dir, k))
	}
	// builtin:file v0.13.0 defaults the destination to sdk.batch.size 1000 and
	// sdk.batch.delay 100ms. Both engines run that shipped default: the classic
	// engine through the SDK batcher, arch-v2 by adopting the declared defaults
	// itself and handing the plugin "0" (#3339).
	fmt.Fprintf(&p, "      - id: dst\n        type: destination\n        plugin: builtin:file\n        settings:\n          path: %q\n", dest)
	if err := os.WriteFile(filepath.Join(dir, "pipelines", "p.yaml"), []byte(p.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// appendLines appends the numbered lines [from, to] to path. It returns the
// error instead of failing the test so a goroutine can use it.
func appendLines(path string, from, to int) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for i := from; i <= to; i++ {
		fmt.Fprintf(w, "%d\n", i)
	}
	return w.Flush()
}

// parseRecord returns the number a destination line carries, or false for a
// line that is not a whole record. Callers fail the test on false; see
// sequence and fifoReader.
func parseRecord(line []byte) (int, bool) {
	var rec struct {
		Payload struct {
			After string `json:"after"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line, &rec); err != nil {
		return 0, false
	}
	b, err := base64.StdEncoding.DecodeString(rec.Payload.After)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(string(b))
	return n, err == nil
}

// sequence returns the record numbers in the destination file, in order.
//
// Every complete line must be a record, or the test fails: a line the reader
// skipped could hide a replayed record or fake a loss, so loss and replay
// counts would not be trustworthy. The destination appends to a regular file,
// and a SIGKILL during its write(2) can leave the start of a line without its
// newline. The restarted process appends its first line to that fragment, and
// the merged line does not parse. Every test that SIGKILLs Conduit therefore
// calls trimTornTail before the restart, so a merged line cannot form and an
// unparseable line is always a real fault.
//
// Bytes after the last newline are not counted: while Conduit runs they are a
// line still being written. trimTornTail (after a kill) and requireNoTornTail
// (after a graceful stop) deal with them.
func sequence(t *testing.T, dir string) []int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, batchedOutputName))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	seq, _, err := parseDestination(raw)
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

// parseDestination splits the destination's bytes into the record numbers of
// its complete lines and the tail after the last newline. It returns an error
// for the first complete line that is not a record.
func parseDestination(raw []byte) (seq []int, tail []byte, err error) {
	end := bytes.LastIndexByte(raw, '\n') + 1
	raw, tail = raw[:end], raw[end:]
	seq = make([]int, 0, bytes.Count(raw, []byte{'\n'}))
	for len(raw) > 0 {
		i := bytes.IndexByte(raw, '\n')
		n, ok := parseRecord(raw[:i])
		if !ok {
			return seq, tail, fmt.Errorf("destination line %d is not a record (%d bytes: %.160q); the reader does not skip lines, so this is a fault in the destination or a kill boundary the test did not trim", len(seq)+1, i, raw[:i])
		}
		seq = append(seq, n)
		raw = raw[i+1:]
	}
	return seq, tail, nil
}

// tornTail returns the bytes after the last newline of the destination file.
func tornTail(t *testing.T, dir string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, batchedOutputName))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return raw[bytes.LastIndexByte(raw, '\n')+1:]
}

// trimTornTail cuts the destination file back to its last newline and logs
// what it cut. Call it after a SIGKILL, once the process is gone, and before
// the restart. The cut bytes are what the killed destination wrote of a Write
// that never returned, so their records were never acknowledged and must be
// replayed: removing them cannot hide a loss (it only ever removes records, so
// a record the engine wrongly acked and does not replay is still missing), and
// it keeps the restart's first line from being glued to the fragment.
func trimTornTail(t *testing.T, dir string) {
	t.Helper()
	tail := tornTail(t, dir)
	if len(tail) == 0 {
		t.Log("torn tail: none, the destination file ended in a newline at the kill")
		return
	}
	path := filepath.Join(dir, batchedOutputName)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, fi.Size()-int64(len(tail))); err != nil {
		t.Fatalf("truncate %s: %v", path, err)
	}
	t.Logf("torn tail: cut %d bytes of a line the killed destination did not finish writing: %.160q", len(tail), tail)
}

// requireNoTornTail fails if the destination file does not end in a newline
// after a graceful stop: a clean shutdown completes every Write (invariant 7).
func requireNoTornTail(t *testing.T, dir string) {
	t.Helper()
	if tail := tornTail(t, dir); len(tail) > 0 {
		t.Fatalf("a graceful stop left %d bytes of an unfinished line in the destination (invariant 7): %.160q", len(tail), tail)
	}
}

func countsOf(seq []int) map[int]int {
	c := make(map[int]int, len(seq))
	for _, n := range seq {
		c[n]++
	}
	return c
}

// lineCount counts complete lines in the destination file without parsing
// them, so polling is cheap next to the pipeline under -race.
func lineCount(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, batchedOutputName))
	if err != nil {
		return 0
	}
	return bytes.Count(b, []byte{'\n'})
}

// lineCounter counts newlines in the destination file incrementally: it keeps
// an offset and reads only what was appended, so polling every millisecond
// costs the same however large the file is and the kill point does not
// overshoot.
type lineCounter struct {
	path   string
	offset int64
	n      int
	buf    []byte
}

func newLineCounter(dir string) *lineCounter {
	return &lineCounter{path: filepath.Join(dir, batchedOutputName), buf: make([]byte, 1<<20)}
}

func (c *lineCounter) count() int {
	f, err := os.Open(c.path)
	if err != nil {
		return c.n
	}
	defer f.Close()
	for {
		k, err := f.ReadAt(c.buf, c.offset)
		c.n += bytes.Count(c.buf[:k], []byte{'\n'})
		c.offset += int64(k)
		if err != nil || k == 0 {
			return c.n
		}
	}
}

// waitDelivered polls the destination file until it holds at least n records.
func waitDelivered(t *testing.T, p *conduitProcess, dir string, n int) int {
	t.Helper()
	deadline := time.After(batchedProcessTimeout)
	counter := newLineCounter(dir)
	for {
		if total := counter.count(); total >= n {
			return total
		}
		select {
		case <-p.exited:
			t.Fatalf("conduit exited (%v) before %d records were written\n%s", p.err, n, p.output())
		case <-deadline:
			t.Fatalf("TIMEOUT (not a data-loss verdict): fewer than %d records written within %s\n%s", n, batchedProcessTimeout, p.output())
		case <-time.After(time.Millisecond):
		}
	}
}

// waitComplete waits until every record of want was written, using only the
// cheap line count while the pipeline is delivering. It fails with a loss
// verdict only if the destination went quiet for batchedQuiet with records
// still missing; slowness alone ends in a TIMEOUT message at batchedOverall.
func waitComplete(t *testing.T, p *conduitProcess, dir string, want []int) {
	t.Helper()
	last, lastChange := lineCount(dir), time.Now()
	start := time.Now()
	for {
		n := lineCount(dir)
		if n != last {
			last, lastChange = n, time.Now()
		}
		if n >= len(want) {
			counts := countsOf(sequence(t, dir))
			missing := 0
			for _, w := range want {
				if counts[w] == 0 {
					missing++
				}
			}
			if missing == 0 {
				return
			}
			if time.Since(lastChange) >= batchedQuiet {
				t.Fatalf("DATA LOSS: the destination went quiet for %s with %d of %d records never delivered (invariants 1 and 3)", batchedQuiet, missing, len(want))
			}
		} else if time.Since(lastChange) >= batchedQuiet {
			t.Fatalf("DATA LOSS: the destination went quiet for %s at %d lines, fewer than the %d records expected (invariants 1 and 3)", batchedQuiet, n, len(want))
		}
		if time.Since(start) > batchedOverall {
			t.Fatalf("TIMEOUT (not a data-loss verdict): still delivering after %s (%d lines of %d)", batchedOverall, n, len(want))
		}
		select {
		case <-p.exited:
			t.Fatalf("conduit exited (%v) before delivering everything\n%s", p.err, p.output())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func rangeOf(from, to int) []int {
	r := make([]int, 0, to-from+1)
	for i := from; i <= to; i++ {
		r = append(r, i)
	}
	return r
}

// requireBatchedSource fails unless the SDK's startup line shows the source
// running with the shipped default: size 100, delay 0, adaptive drain. It
// returns when the line was seen, which is when the pipeline opened.
func requireBatchedSource(t *testing.T, p *conduitProcess) time.Time {
	t.Helper()
	deadline := time.Now().Add(batchedProcessTimeout)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(p.output(), "\n") {
			if !strings.Contains(line, `"message":"source batching configured"`) {
				continue
			}
			var l struct {
				Size int    `json:"size"`
				Mode string `json:"mode"`
				Name string `json:"plugin_name"`
			}
			if err := json.Unmarshal([]byte(line), &l); err != nil {
				continue
			}
			if l.Name != "builtin:file" {
				continue
			}
			if l.Size != 100 || l.Mode != "adaptive" {
				t.Fatalf("source is not running the default batching (size %d, mode %q): %s", l.Size, l.Mode, line)
			}
			t.Logf("resolved source batching: %s", line)
			return time.Now()
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no \"source batching configured\" line from builtin:file: the source is not running a batched path\n%s", p.output())
	return time.Time{}
}

func (p *conduitProcess) sigkill(t *testing.T) {
	t.Helper()
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	select {
	case <-p.exited:
	case <-time.After(batchedExitTimeout):
		t.Fatal("conduit did not die after SIGKILL")
	}
}

// sigterm stops the process gracefully and returns once it exited cleanly.
func (p *conduitProcess) sigterm(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	select {
	case <-p.exited:
	case <-time.After(batchedExitTimeout):
		t.Fatalf("conduit did not exit within %s of SIGTERM\n%s", batchedExitTimeout, p.output())
	}
	if p.err != nil {
		t.Fatalf("conduit exited with %v after SIGTERM\n%s", p.err, p.output())
	}
}

func duplicates(counts map[int]int) int {
	d := 0
	for _, c := range counts {
		d += c - 1
	}
	return d
}

// requireOrder asserts invariant 4 for one source: its records, in
// destination order, only ever go backwards where a restart replays, so at
// most maxReplays times. Numbers outside [lo, hi] belong to other sources.
func requireOrder(t *testing.T, seq []int, lo, hi, maxReplays int) {
	t.Helper()
	prev, back := 0, 0
	for _, n := range seq {
		if n < lo || n > hi {
			continue
		}
		if n < prev {
			back++
		}
		prev = n
	}
	if back > maxReplays {
		t.Fatalf("ORDER: source range [%d,%d] went backwards %d times, at most %d (one per restart) allowed (invariant 4)", lo, hi, back, maxReplays)
	}
}

func requireReplayBound(t *testing.T, counts map[int]int) {
	t.Helper()
	if d := duplicates(counts); d > persisterWindow+replayCredit {
		t.Fatalf("replayed %d records, over the bound %d (window %d + credit %d)", d, persisterWindow+replayCredit, persisterWindow, replayCredit)
	}
}

// TestSIGKILL_BatchedSource_WhileCollecting kills Conduit while the file
// source's SDK holds records it read but has not sent: a writer appends 37
// lines (never aligned to the 100 batch size) every 2ms, so the SDK is
// regularly part-way through a batch with read-ahead queued behind it. The
// kill lands after the persister's first flush, so the restart resumes from a
// persisted position, not from zero. Records read but not sent were never
// acked, so nothing may be lost, order must hold, and the replay must be
// strictly less than everything delivered (the position did advance).
func TestSIGKILL_BatchedSource_WhileCollecting(t *testing.T) {
	conduitBin, _ := buildStandaloneBinaries(t)
	const total = 60000
	for _, eng := range batchedEngines {
		t.Run(eng.name, func(t *testing.T) {
			dir := batchedDir(t, batchedOpts{})
			input := inputPath(dir, 0)
			writerErr := make(chan error, 1)
			go func() { // the writer outlives the first process
				for w := 0; w < total; {
					next := min(w+37, total)
					if err := appendLines(input, w+1, next); err != nil {
						writerErr <- err
						return
					}
					w = next
					time.Sleep(2 * time.Millisecond)
				}
				writerErr <- nil
			}()
			defer func() {
				if err := <-writerErr; err != nil {
					t.Errorf("input writer: %v", err)
				}
			}()

			first := startConduit(t, conduitBin, dir, "never", eng.flags...)
			opened := requireBatchedSource(t, first)
			time.Sleep(1500*time.Millisecond - time.Since(opened)) // after the first flush, writer still running
			first.sigkill(t)
			trimTornTail(t, dir)
			atKill := len(sequence(t, dir))
			if atKill == 0 || atKill >= total {
				t.Fatalf("kill did not land mid-stream (%d of %d delivered)", atKill, total)
			}

			second := startConduit(t, conduitBin, dir, "never", eng.flags...)
			requireBatchedSource(t, second)
			waitComplete(t, second, dir, rangeOf(1, total))
			second.sigterm(t)
			requireNoTornTail(t, dir)

			seq := sequence(t, dir)
			counts := countsOf(seq)
			requireOrder(t, seq, 1, total, 1)
			requireReplayBound(t, counts)
			d := duplicates(counts)
			t.Logf("%s: %d delivered at kill, %d replayed", eng.name, atKill, d)
			if d >= atKill {
				t.Fatalf("restart replayed everything delivered (%d of %d): no position was persisted before the kill", d, atKill)
			}
		})
	}
}

// TestSIGKILL_BatchedSource_AfterWriteBeforePersist kills Conduit well after
// the destination wrote more than one persister window of records, while the
// source position is only persisted by the persister (every 10,000 positions
// or one second). The restart replays what was written after the last
// persisted position: nothing may be lost, order must hold, and the replay
// must stay within the persister window plus a small credit.
//
// The persister counts positions, not Ack calls, and flushes when the count
// reaches the threshold (>=): with source batches of 1..100 records a count of
// calls would reach 10,000 only after about a million records, and an equality
// test would be stepped over, so only the one second timer would flush. This
// test catches that on arch-v2, which acks whole batches. The classic engine
// acks one position per call and is unaffected.
func TestSIGKILL_BatchedSource_AfterWriteBeforePersist(t *testing.T) {
	conduitBin, _ := buildStandaloneBinaries(t)
	const (
		// killAt is 2.5 persister windows, about twice the replay bound (12,000).
		// total leaves 8x headroom over killAt: the poll is incremental, so the
		// kill overshoots by milliseconds of delivery, but a loaded runner can
		// deschedule the test for much longer than that.
		total  = 200000
		killAt = 25000
	)
	for _, eng := range batchedEngines {
		t.Run(eng.name, func(t *testing.T) {
			dir := batchedDir(t, batchedOpts{lines: total})

			first := startConduit(t, conduitBin, dir, "never", eng.flags...)
			requireBatchedSource(t, first)
			atKill := waitDelivered(t, first, dir, killAt)
			first.sigkill(t)
			trimTornTail(t, dir)
			if lineCount(dir) >= total {
				t.Fatalf("the whole input (%d) was delivered before the kill landed; raise total", total)
			}

			second := startConduit(t, conduitBin, dir, "never", eng.flags...)
			requireBatchedSource(t, second)
			waitComplete(t, second, dir, rangeOf(1, total))
			second.sigterm(t)
			requireNoTornTail(t, dir)

			seq := sequence(t, dir)
			counts := countsOf(seq)
			requireOrder(t, seq, 1, total, 1)
			dups := duplicates(counts)
			t.Logf("%s: ~%d delivered at kill, %d replayed", eng.name, atKill, dups)
			if dups > persisterWindow+replayCredit {
				t.Fatalf("replayed %d records after the kill, over the bound %d (window %d + credit %d): the persister is not flushing on the position count",
					dups, persisterWindow+replayCredit, persisterWindow, replayCredit)
			}
		})
	}
}

// fifoReader drains the destination named pipe into a sequence. It holds the
// pipe open read-write and non-blocking so the destination can open it, never
// sees EOF, and a drain can end when the pipe stays empty. (A read deadline
// does not work on a FIFO on every platform, so it polls the raw descriptor.)
//
// Because the reader holds the pipe open, bytes a killed process wrote stay in
// the pipe across the restart. The file destination writes up to 64 KiB per
// write(2), and on Linux a pipe write larger than PIPE_BUF can complete in
// part, so a SIGKILL during a blocked Write can leave the start of a line
// without its newline. That Write never returned, so its records were never
// acked and replay. The test must call dropPartial after the post-kill drain:
// otherwise the next process's first line is appended to the fragment, the
// merged line fails to parse, and the first replayed record is silently
// missing from the sequence. When the fragment was the start of that same
// record, the test reported one record lost that the engine had replayed.
type fifoReader struct {
	fd  int
	mu  sync.Mutex
	buf []byte
	seq []int
	// torn holds every complete line that did not parse. With dropPartial at
	// each kill, no torn line can form, so requireNoTornLines fails on any:
	// a line the reader dropped must never pass as loss, or hide one.
	torn []string
}

func openFIFO(t *testing.T, path string) *fifoReader {
	t.Helper()
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatalf("open fifo: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	return &fifoReader{fd: fd}
}

// drain reads until the pipe stays empty for quiet.
func (r *fifoReader) drain(quiet time.Duration) {
	chunk := make([]byte, 256*1024)
	idleSince := time.Now()
	for {
		n, err := syscall.Read(r.fd, chunk)
		if n > 0 {
			idleSince = time.Now()
			r.mu.Lock()
			r.buf = append(r.buf, chunk[:n]...)
			for {
				i := bytes.IndexByte(r.buf, '\n')
				if i < 0 {
					break
				}
				if v, ok := parseRecord(r.buf[:i]); ok {
					r.seq = append(r.seq, v)
				} else {
					r.torn = append(r.torn, fmt.Sprintf("after record %d of the sequence, %d bytes: %.160q", len(r.seq), i, r.buf[:i]))
				}
				r.buf = r.buf[i+1:]
			}
			r.mu.Unlock()
			continue
		}
		if err != nil && err != syscall.EAGAIN && err != syscall.EINTR {
			return
		}
		if time.Since(idleSince) >= quiet {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitAll keeps draining the pipe until every record of want arrived. It fails
// with a loss verdict only when nothing arrived for batchedQuiet.
func (r *fifoReader) waitAll(t *testing.T, p *conduitProcess, want []int, context string) {
	t.Helper()
	done := make(chan struct{})
	stop := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			r.drain(100 * time.Millisecond)
		}
	}()
	defer func() { close(stop); <-done }()
	deadline := time.Now().Add(batchedOverall)
	quietSince, last := time.Now(), -1
	for {
		seq := r.snapshot()
		counts := countsOf(seq)
		missing := 0
		for _, w := range want {
			if counts[w] == 0 {
				missing++
			}
		}
		if missing == 0 {
			return
		}
		if len(seq) != last {
			last, quietSince = len(seq), time.Now()
		}
		if time.Since(quietSince) >= batchedQuiet {
			t.Fatalf("DATA LOSS%s: the destination went quiet for %s with %d of %d records never delivered (invariants 1 and 3); unparseable lines: %q", context, batchedQuiet, missing, len(want), r.tornLines())
		}
		if time.Now().After(deadline) {
			t.Fatalf("TIMEOUT (not a data-loss verdict)")
		}
		select {
		case <-p.exited:
			t.Fatalf("conduit exited (%v) before delivering everything\n%s", p.err, p.output())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// dropPartial discards and returns the bytes after the last newline. Call it
// only once the writer has exited and the pipe was drained: those bytes are
// what remains of a Write the killed process never completed.
func (r *fifoReader) dropPartial() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	tail := r.buf
	r.buf = nil
	return tail
}

func (r *fifoReader) tornLines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.torn...)
}

// requireNoTornLines fails if any complete line in the pipe did not parse.
func (r *fifoReader) requireNoTornLines(t *testing.T) {
	t.Helper()
	if torn := r.tornLines(); len(torn) > 0 {
		t.Fatalf("%d unparseable destination lines (the reader dropped them, so loss and replay counts are not trustworthy): %q", len(torn), torn)
	}
}

func (r *fifoReader) snapshot() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.seq...)
}

// TestSIGKILL_BatchedSource_StalledDestination_NoLoss is the deterministic
// invariant 1 detector. The destination is a named pipe nobody reads, so after
// the pipe fills its Write blocks and the destination is stuck for as long as
// the test wants, with the source several batches ahead of it. Positions are
// persisted by the timer while it is stuck (the kill lands after two flush
// periods). The test then kills Conduit, drains the pipe (everything a
// completed Write put there counts as written), restarts with a reader and
// requires every record to arrive. A persisted position may cover only
// records the destination wrote; if any engine acked the source before the
// destination write, the stuck read-ahead is covered by a persisted position
// and is lost. Nothing here depends on how fast the pipeline runs.
func TestSIGKILL_BatchedSource_StalledDestination_NoLoss(t *testing.T) {
	conduitBin, _ := buildStandaloneBinaries(t)
	const total = 5000
	for _, eng := range batchedEngines {
		t.Run(eng.name, func(t *testing.T) {
			dir := batchedDir(t, batchedOpts{lines: total, fifo: true})
			pipe := openFIFO(t, fifoPath(dir))

			first := startConduit(t, conduitBin, dir, "never", eng.flags...)
			opened := requireBatchedSource(t, first)
			// two persister periods: the position has been flushed at least
			// twice while the destination was blocked on the full pipe
			time.Sleep(2500*time.Millisecond - time.Since(opened))
			first.sigkill(t)
			pipe.drain(500 * time.Millisecond)
			// The killed destination was blocked in Write, so the pipe may end
			// in part of a line. That Write never returned and its records
			// replay; the fragment must not be glued to the restart's first line.
			if tail := pipe.dropPartial(); len(tail) > 0 {
				t.Logf("dropped %d bytes of a line the killed destination did not finish writing", len(tail))
			}
			pipe.requireNoTornLines(t)
			stalled := pipe.snapshot()
			if len(stalled) == 0 || len(stalled) > total/2 {
				t.Fatalf("the destination did not stall as designed (%d of %d written)", len(stalled), total)
			}

			second := startConduit(t, conduitBin, dir, "never", eng.flags...)
			requireBatchedSource(t, second)
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					select {
					case <-second.exited:
						return
					default:
					}
					pipe.drain(200 * time.Millisecond)
				}
			}()
			deadline := time.Now().Add(batchedOverall)
			quietSince, last := time.Now(), -1
			for {
				seq := pipe.snapshot()
				counts := countsOf(seq)
				if len(counts) >= total {
					break
				}
				if len(seq) != last {
					last, quietSince = len(seq), time.Now()
				}
				if time.Since(quietSince) >= batchedQuiet {
					var missing []int
					for i := 1; i <= total; i++ {
						if counts[i] == 0 {
							missing = append(missing, i)
						}
					}
					t.Fatalf("DATA LOSS: after %d records written while stalled, a persisted position covered records the destination never wrote; %d of %d never delivered, first %v (invariant 1); unparseable lines: %q",
						len(stalled), len(missing), total, missing[:min(len(missing), 10)], pipe.tornLines())
				}
				if time.Now().After(deadline) {
					t.Fatalf("TIMEOUT (not a data-loss verdict)")
				}
				time.Sleep(50 * time.Millisecond)
			}
			second.sigterm(t)
			<-done

			pipe.requireNoTornLines(t)
			seq := pipe.snapshot()
			requireOrder(t, seq, 1, total, 1)
			requireReplayBound(t, countsOf(seq))
			t.Logf("%s: %d written while stalled, %d replayed", eng.name, len(stalled), duplicates(countsOf(seq)))
		})
	}
}

// TestSIGKILL_BatchedSource_NSource runs two file sources into one
// destination (the N-source shape) and kills mid-stream: nothing is lost,
// each source's order holds, and the replay is bounded.
func TestSIGKILL_BatchedSource_NSource(t *testing.T) {
	conduitBin, _ := buildStandaloneBinaries(t)
	const lines = 40000
	for _, eng := range batchedEngines {
		t.Run(eng.name, func(t *testing.T) {
			dir := batchedDir(t, batchedOpts{lines: lines, sources: 2})
			want := append(rangeOf(1, lines), rangeOf(sourceStride+1, sourceStride+lines)...)

			first := startConduit(t, conduitBin, dir, "never", eng.flags...)
			requireBatchedSource(t, first)
			waitDelivered(t, first, dir, 15000)
			first.sigkill(t)
			trimTornTail(t, dir)
			if lineCount(dir) >= 2*lines {
				t.Fatalf("the whole input was delivered before the kill landed; raise lines")
			}

			second := startConduit(t, conduitBin, dir, "never", eng.flags...)
			requireBatchedSource(t, second)
			waitComplete(t, second, dir, want)
			second.sigterm(t)
			requireNoTornTail(t, dir)

			seq := sequence(t, dir)
			requireOrder(t, seq, 1, lines, 1)
			requireOrder(t, seq, sourceStride+1, sourceStride+lines, 1)
			requireReplayBound(t, countsOf(seq))
			t.Logf("%s: %d replayed", eng.name, duplicates(countsOf(seq)))
		})
	}
}

// TestSIGTERM_BatchedSource_GracefulDrain stops Conduit with SIGTERM while
// records are provably in flight (invariant 7): the destination is a named
// pipe nobody reads, so its Write is blocked and the source is ahead of it
// when the signal lands. Only then does the test start draining the pipe, so
// the pipeline can finish what it has in flight, checkpoint and exit 0. A
// restart must deliver the rest with no loss, no duplicates and no
// reordering: records the SDK read ahead but did not send were dropped
// unacked, and nothing sent was left unpersisted. The result does not depend
// on throughput.
func TestSIGTERM_BatchedSource_GracefulDrain(t *testing.T) {
	conduitBin, _ := buildStandaloneBinaries(t)
	const total = 5000
	for _, eng := range batchedEngines {
		t.Run(eng.name, func(t *testing.T) {
			dir := batchedDir(t, batchedOpts{lines: total, fifo: true})
			pipe := openFIFO(t, fifoPath(dir))

			first := startConduit(t, conduitBin, dir, "never", eng.flags...)
			opened := requireBatchedSource(t, first)
			time.Sleep(1500*time.Millisecond - time.Since(opened)) // destination is blocked on the full pipe
			if n := len(pipe.snapshot()); n != 0 {
				t.Fatalf("the test read %d records before the stall", n)
			}
			// Real in-flight evidence: the pipe holds bytes the destination
			// wrote and nobody read, and the amount is not changing, so the
			// destination is blocked with the source ahead of it.
			pending, capacity, supported, err := pipeFill(pipe.fd)
			if err != nil {
				t.Fatalf("pipe probe: %v", err)
			}
			if supported {
				time.Sleep(300 * time.Millisecond)
				again, _, _, err := pipeFill(pipe.fd)
				if err != nil {
					t.Fatalf("pipe probe: %v", err)
				}
				// near full and not moving: the destination is blocked in Write
				if pending < capacity/2 || pending != again {
					t.Fatalf("the destination is not stalled on a full pipe (%d then %d of %d bytes)", pending, again, capacity)
				}
			} else {
				t.Log("FIONREAD is not available on this platform; the in-flight check is skipped")
			}
			// SIGTERM now, then let the destination drain while the process
			// shuts down.
			stopDone := make(chan struct{})
			go func() {
				defer close(stopDone)
				for {
					pipe.drain(100 * time.Millisecond)
					select {
					case <-first.exited:
						pipe.drain(100 * time.Millisecond)
						return
					default:
					}
				}
			}()
			first.sigterm(t)
			<-stopDone
			// A graceful stop completes every Write, so nothing may be left
			// half-written in the pipe.
			if tail := pipe.dropPartial(); len(tail) > 0 {
				t.Fatalf("a graceful stop left %d bytes of an unfinished line in the destination (invariant 7): %.160q", len(tail), tail)
			}
			pipe.requireNoTornLines(t)
			midTotal := len(pipe.snapshot())
			if midTotal == 0 || midTotal >= total {
				t.Fatalf("SIGTERM did not land mid-stream (%d of %d written)", midTotal, total)
			}
			if d := duplicates(countsOf(pipe.snapshot())); d != 0 {
				t.Fatalf("%d duplicates before any restart", d)
			}

			second := startConduit(t, conduitBin, dir, "never", eng.flags...)
			requireBatchedSource(t, second)
			pipe.waitAll(t, second, rangeOf(1, total), "")
			second.sigterm(t)

			pipe.requireNoTornLines(t)
			seq := pipe.snapshot()
			requireOrder(t, seq, 1, total, 0)
			t.Logf("%s: %d written before the stop completed, %d in total", eng.name, midTotal, len(seq))
			if d := duplicates(countsOf(seq)); d != 0 {
				t.Fatalf("%d records were delivered twice across a graceful stop and restart (invariants 3 and 7)", d)
			}
		})
	}
}
