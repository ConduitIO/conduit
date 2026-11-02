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

//go:build unix

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
// 1, 3 and 7:
//
//   - SIGKILL while the SDK holds read-ahead and a partly collected batch
//     (TestSIGKILL_BatchedSource_WhileCollecting): nothing the SDK read but
//     did not send may be acked, so nothing is lost on restart.
//   - SIGKILL after the destination wrote but before the position was
//     persisted (TestSIGKILL_BatchedSource_AfterWriteBeforePersist): the
//     replay is bounded by the persister window, which counts positions.
//   - SIGTERM mid-stream (TestSIGTERM_BatchedSource_GracefulDrain): the
//     pipeline drains, checkpoints and exits; resuming loses nothing and
//     duplicates nothing.
//
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
	// batchedCompleteTimeout bounds the wait for a restarted pipeline to
	// deliver the rest of the input; a lost record never arrives, so it is also
	// how long a failing run takes to report the loss.
	batchedCompleteTimeout = 45 * time.Second
)

type batchedEngine struct {
	name  string
	flags []string
}

var batchedEngines = []batchedEngine{
	{name: "classic"},
	{name: "arch-v2", flags: []string{"--preview.pipeline-arch-v2"}},
}

// batchedDir lays out a working directory with a file source over n numbered
// lines (appended to later by tests that need a growing file) and a file
// destination. Neither connector sets sdk.batch.*: the default is the point.
func batchedDir(t *testing.T, n int) string {
	t.Helper()
	return batchedDirWith(t, n, "")
}

// batchedDirWith is batchedDir with an optional pipeline processor block
// (already indented under "processors:").
func batchedDirWith(t *testing.T, n int, processors string) string {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"pipelines", "connectors", "processors"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	appendLines(t, filepath.Join(dir, batchedInputName), 1, n)
	pipeline := fmt.Sprintf(`version: "2.2"
pipelines:
  - id: batched
    status: running
    connectors:
      - id: src
        type: source
        plugin: builtin:file
        settings:
          path: %q
      - id: dst
        type: destination
        plugin: builtin:file
        settings:
          path: %q
%s`, filepath.Join(dir, batchedInputName), filepath.Join(dir, batchedOutputName), processors)
	if err := os.WriteFile(filepath.Join(dir, "pipelines", "p.yaml"), []byte(pipeline), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// appendLines appends the numbered lines [from, to] to path.
func appendLines(t *testing.T, path string, from, to int) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for i := from; i <= to; i++ {
		fmt.Fprintf(w, "%d\n", i)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
}

// delivered returns how often each line was written to the destination file.
// A torn last line (the process was killed mid-write) is ignored.
func delivered(t *testing.T, dir string) (counts map[int]int, total int) {
	t.Helper()
	counts = map[int]int{}
	f, err := os.Open(filepath.Join(dir, batchedOutputName))
	if os.IsNotExist(err) {
		return counts, 0
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var rec struct {
			Payload struct {
				After string `json:"after"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			continue // torn line
		}
		b, err := base64.StdEncoding.DecodeString(rec.Payload.After)
		if err != nil {
			continue
		}
		n, err := strconv.Atoi(string(b))
		if err != nil {
			continue
		}
		counts[n]++
		total++
	}
	return counts, total
}

// lineCount counts complete lines in the destination file without parsing
// them, so a poll is cheap enough to catch a kill point on a fast pipeline.
func lineCount(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, batchedOutputName))
	if err != nil {
		return 0
	}
	return bytes.Count(b, []byte{'\n'})
}

// waitDelivered polls the destination file until it holds at least n records.
func waitDelivered(t *testing.T, p *conduitProcess, dir string, n int) int {
	t.Helper()
	deadline := time.After(batchedProcessTimeout)
	for {
		if total := lineCount(dir); total >= n {
			return total
		}
		select {
		case <-p.exited:
			t.Fatalf("conduit exited (%v) before %d records were written\n%s", p.err, n, p.output())
		case <-deadline:
			t.Fatalf("fewer than %d records written within %s\n%s", n, batchedProcessTimeout, p.output())
		case <-time.After(time.Millisecond):
		}
	}
}

// requireBatchedSource fails unless the SDK's startup line shows the source
// running with the shipped default: size 100, delay 0, adaptive drain.
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

// requireNoLoss fails if any of 1..n was never written.
func requireNoLoss(t *testing.T, counts map[int]int, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		if counts[i] == 0 {
			t.Fatalf("record %d was never delivered (%d distinct of %d): data loss (invariants 1 and 3)", i, len(counts), n)
		}
	}
}

func duplicates(counts map[int]int) int {
	d := 0
	for _, c := range counts {
		d += c - 1
	}
	return d
}

// TestSIGKILL_BatchedSource_WhileCollecting kills Conduit while the file
// source's SDK holds records it read but has not sent: the file grows in
// bursts of 37 lines (never aligned to the 100 batch size), so the SDK is
// regularly part-way through a batch, with read-ahead queued behind it. Those
// records were never sent and never acked, so after the restart they must be
// read again from the persisted position: nothing may be lost.
func TestSIGKILL_BatchedSource_WhileCollecting(t *testing.T) {
	conduitBin, _ := buildStandaloneBinaries(t)
	const total = 3000
	for _, eng := range batchedEngines {
		t.Run(eng.name, func(t *testing.T) {
			dir := batchedDir(t, 0)
			input := filepath.Join(dir, batchedInputName)
			written := 0
			burst := func(upTo int) {
				for written < upTo {
					next := min(written+37, upTo)
					appendLines(t, input, written+1, next)
					written = next
					time.Sleep(time.Millisecond)
				}
			}

			first := startConduit(t, conduitBin, dir, "never", eng.flags...)
			requireBatchedSource(t, first)
			burst(1500)
			waitDelivered(t, first, dir, 400) // mid-stream, with a position persisted or not
			burst(1600)                       // keep the SDK busy at the instant of the kill
			first.sigkill(t)

			second := startConduit(t, conduitBin, dir, "never", eng.flags...)
			requireBatchedSource(t, second)
			burst(total)
			deadline := time.Now().Add(batchedCompleteTimeout)
			for {
				counts, _ := delivered(t, dir)
				if len(counts) >= total {
					break
				}
				if time.Now().After(deadline) {
					requireNoLoss(t, counts, total)
				}
				time.Sleep(10 * time.Millisecond)
			}
			second.sigterm(t)

			counts, sum := delivered(t, dir)
			requireNoLoss(t, counts, total)
			t.Logf("%s: %d records, %d distinct, %d replayed", eng.name, sum, len(counts), duplicates(counts))
			// replay after a kill is bounded by the persister window
			if d := duplicates(counts); d > persisterWindow+replayCredit {
				t.Fatalf("replayed %d records, over the bound %d", d, persisterWindow+replayCredit)
			}
		})
	}
}

// TestSIGKILL_BatchedSource_AfterWriteBeforePersist kills Conduit well after
// the destination wrote more than one persister window of records, while the
// source position is only persisted by the persister (every 10,000 positions
// or one second). The restart replays what was written after the last
// persisted position: nothing may be lost, and the replay must stay within the
// persister window plus a small credit.
//
// The persister counts positions, not Ack calls, and flushes when the count
// reaches the threshold (>=): with source batches of 1..100 records a count of
// calls would reach 10,000 only after about a million records, and an equality
// test would be stepped over, so only the one second timer would flush. This
// test fails in both cases when the whole input is delivered inside one
// second, and otherwise whenever the timer has not just fired.
func TestSIGKILL_BatchedSource_AfterWriteBeforePersist(t *testing.T) {
	conduitBin, _ := buildStandaloneBinaries(t)
	const (
		total  = 400000
		killAt = 4 * persisterWindow
	)
	for _, eng := range batchedEngines {
		t.Run(eng.name, func(t *testing.T) {
			dir := batchedDir(t, total)

			first := startConduit(t, conduitBin, dir, "never", eng.flags...)
			requireBatchedSource(t, first)
			atKill := waitDelivered(t, first, dir, killAt)
			first.sigkill(t)
			_, afterKill := delivered(t, dir)
			if afterKill >= total {
				t.Fatalf("the whole input (%d) was delivered before the kill landed; raise total", total)
			}

			second := startConduit(t, conduitBin, dir, "never", eng.flags...)
			requireBatchedSource(t, second)
			deadline := time.Now().Add(batchedCompleteTimeout)
			for {
				counts, _ := delivered(t, dir)
				if len(counts) >= total {
					break
				}
				if time.Now().After(deadline) {
					requireNoLoss(t, counts, total)
				}
				time.Sleep(20 * time.Millisecond)
			}
			second.sigterm(t)

			counts, sum := delivered(t, dir)
			requireNoLoss(t, counts, total)
			dups := duplicates(counts)
			t.Logf("%s: delivered %d at kill (%d on disk), %d total, %d replayed", eng.name, atKill, afterKill, sum, dups)
			if dups > persisterWindow+replayCredit {
				t.Fatalf("replayed %d records after the kill, over the bound %d (window %d + credit %d): the persister is not flushing on the position count",
					dups, persisterWindow+replayCredit, persisterWindow, replayCredit)
			}
		})
	}
}

// TestSIGTERM_BatchedSource_GracefulDrain stops Conduit with SIGTERM while a
// batched source is mid-stream (invariant 7): the pipeline drains what is in
// flight, checkpoints and exits cleanly. Resuming must then deliver the rest
// with no loss and no duplicates, because records the SDK read ahead but did
// not send were dropped unacked and nothing sent was left unpersisted.
func TestSIGTERM_BatchedSource_GracefulDrain(t *testing.T) {
	conduitBin, _ := buildStandaloneBinaries(t)
	const total = 1000000
	for _, eng := range batchedEngines {
		t.Run(eng.name, func(t *testing.T) {
			dir := batchedDir(t, total)

			first := startConduit(t, conduitBin, dir, "never", eng.flags...)
			requireBatchedSource(t, first)
			waitDelivered(t, first, dir, 10000)
			first.sigterm(t)
			mid, midTotal := delivered(t, dir)
			if midTotal >= total {
				t.Fatalf("the whole input was delivered before SIGTERM landed; raise total")
			}
			if d := duplicates(mid); d != 0 {
				t.Fatalf("%d duplicates before any restart", d)
			}

			second := startConduit(t, conduitBin, dir, "never", eng.flags...)
			requireBatchedSource(t, second)
			deadline := time.Now().Add(batchedCompleteTimeout)
			for {
				counts, _ := delivered(t, dir)
				if len(counts) >= total {
					break
				}
				if time.Now().After(deadline) {
					requireNoLoss(t, counts, total)
				}
				time.Sleep(20 * time.Millisecond)
			}
			second.sigterm(t)

			counts, sum := delivered(t, dir)
			requireNoLoss(t, counts, total)
			t.Logf("%s: %d delivered before SIGTERM, %d records in total", eng.name, midTotal, sum)
			if d := duplicates(counts); d != 0 {
				t.Fatalf("%d records were delivered twice across a graceful stop and restart (invariants 3 and 7)", d)
			}
		})
	}
}

// slowProcessor holds each record for about three milliseconds, so the destination
// runs behind the source at roughly three hundred records a second and a few
// hundred records (the SDK read-ahead plus the batch in the engine) sit
// between the source's read position and what the destination has written.
const slowProcessor = `    processors:
      - id: slow
        plugin: custom.javascript
        settings:
          script: |
            function process(rec) {
              var end = Date.now() + 3;
              while (Date.now() < end) {}
              return rec;
            }
`

// TestSIGKILL_BatchedSource_SlowDestination_NoLoss is the test that catches a
// source position persisted ahead of the destination write (invariant 1). The
// persister's timer fires one second after the first position is handed to
// it (at Open), so the kill lands shortly after the first flush (1.15s), while the destination
// is still a few hundred records behind the source read-ahead. If the
// persisted position were taken at read time, or the source were acked before
// the destination wrote, the restart would resume past records that were never
// delivered and this test would report them lost. Verified by acking on read
// (see the PR description).
func TestSIGKILL_BatchedSource_SlowDestination_NoLoss(t *testing.T) {
	conduitBin, _ := buildStandaloneBinaries(t)
	const total = 1500
	for _, eng := range batchedEngines {
		t.Run(eng.name, func(t *testing.T) {
			dir := batchedDirWith(t, total, slowProcessor)

			first := startConduit(t, conduitBin, dir, "never", eng.flags...)
			opened := requireBatchedSource(t, first)
			// The persister's timer starts with the first Persist, which is the
			// lifecycle-event persist at Open, so its first flush is one second
			// after the source opened. Kill shortly after it, inside the window
			// in which the destination is still behind the source read-ahead.
			time.Sleep(time.Second + 150*time.Millisecond - time.Since(opened))
			first.sigkill(t)
			_, atKill := delivered(t, dir)
			if atKill >= total {
				t.Fatalf("the whole input was delivered before the kill")
			}

			second := startConduit(t, conduitBin, dir, "never", eng.flags...)
			requireBatchedSource(t, second)
			deadline := time.Now().Add(batchedCompleteTimeout)
			for {
				counts, _ := delivered(t, dir)
				if len(counts) >= total {
					break
				}
				if time.Now().After(deadline) {
					requireNoLoss(t, counts, total)
				}
				time.Sleep(20 * time.Millisecond)
			}
			second.sigterm(t)

			counts, sum := delivered(t, dir)
			requireNoLoss(t, counts, total)
			t.Logf("%s: %d delivered at kill, %d total, %d replayed", eng.name, atKill, sum, duplicates(counts))
			if d := duplicates(counts); d > persisterWindow+replayCredit {
				t.Fatalf("replayed %d records, over the bound %d", d, persisterWindow+replayCredit)
			}
		})
	}
}
