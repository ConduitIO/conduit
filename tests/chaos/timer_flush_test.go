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

package chaos

import (
	"hash/fnv"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matryer/is"
)

// This file is the chaos coverage of the arch-v2 destination flush timer
// (#3231, docs/design-documents/20261103-archv2-destination-batch-timing.md,
// "Test plan > Chaos"). When a destination sets sdk.batch.delay, the first
// record of an empty buffer arms a one-shot timer that flushes the partial
// batch if nothing else takes it, so a tail held by a quiet source is written
// and acked within the delay instead of waiting for a stop. Until the timer
// fires the tail is neither written nor acked, so a SIGKILL in that window
// must lose nothing (it replays from the persisted position), and a SIGKILL
// during the timer's own flush must neither ack early nor lose the batch.
//
// The scenarios use a small scale: the destination batch size is 10 and the
// total 25, so two full batches are flushed by size and the tail of 5 waits
// for the timer. The design doc's size of 10000 only exists to make every
// record after the last full batch a partial one; the shape is the same.

// envChaosSeed pins the seed of every random kill instant in this file, so a
// failure reproduces: CHAOS_SEED=<n> go test -run <test> ./tests/chaos/.
// Unset, a time-based seed is drawn. Each test mixes its own name into the
// seed, so one value reproduces any single test.
const envChaosSeed = "CHAOS_SEED"

// chaosRand returns a random source for t's kill instants, seeded from
// CHAOS_SEED (or the clock) and the test name, and logs the seed.
func chaosRand(t *testing.T) *rand.Rand {
	t.Helper()
	seed := time.Now().UnixNano()
	if v := os.Getenv(envChaosSeed); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			t.Fatalf("invalid %s %q: %v", envChaosSeed, v, err)
		}
		seed = n
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(t.Name()))
	t.Logf("%s=%d (set it to reproduce this run's kill instants)", envChaosSeed, seed)
	return rand.New(rand.NewSource(seed ^ int64(h.Sum64())))
}

// randDuration returns a uniformly random duration in [lo, hi].
func randDuration(r *rand.Rand, lo, hi time.Duration) time.Duration {
	return lo + time.Duration(r.Int63n(int64(hi-lo)+1))
}

// destWrites returns the positions of the child's LC_DEST_WRITE lines, in the
// order the destination durably recorded them.
func destWrites(cp *childProcess) []uint64 {
	var out []uint64
	for _, l := range cp.linesWithPrefix(markerLCDestWrite + " ") {
		n, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(l, markerLCDestWrite+" ")), 10, 64)
		if err == nil {
			out = append(out, n)
		}
	}
	return out
}

// assertWriteOrder fails unless writes are in non-decreasing position order:
// the source emits positions in increasing order, and invariant 4 (per-source
// order) means no timer flush, size flush or drain may write them out of it.
func assertWriteOrder(t *testing.T, label string, writes []uint64) {
	t.Helper()
	for i := 1; i < len(writes); i++ {
		if writes[i] < writes[i-1] {
			t.Fatalf("%s: destination wrote position %d after %d (per-source order broken): %v", label, writes[i], writes[i-1], writes)
		}
	}
}

// assertTailWrittenBeforeStop fails unless the child wrote position total to
// the destination before it asked the source to stop. A graceful child waits
// for the ack of total before stopping when sdk.batch.delay is set, so this
// holds only if the flush timer (or a size flush) wrote the tail; a tail left
// to Worker.Stop's drain would be written after the stop.
func assertTailWrittenBeforeStop(t *testing.T, cp *childProcess, total uint64) {
	t.Helper()
	tailAt, stopAt := -1, -1
	for i, l := range cp.linesSnapshot() {
		switch {
		case l == markerLCDestWrite+" "+strconv.FormatUint(total, 10) && tailAt < 0:
			tailAt = i
		case l == markerLCSourceStopCalled && stopAt < 0:
			stopAt = i
		}
	}
	if tailAt < 0 {
		t.Fatalf("the restart never wrote position %d to the destination\n%s", total, cp.diagnostics())
	}
	if stopAt >= 0 && tailAt > stopAt {
		t.Fatalf("position %d was written after the source stop (line %d vs %d): the tail was left to the stop's drain, not flushed by the timer\n%s",
			total, tailAt, stopAt, cp.diagnostics())
	}
}

// assertBoundedReplay fails if the restart wrote more than maxReplay positions
// that the killed process had already durably delivered, or resumed anywhere
// but just past the persisted position.
func assertBoundedReplay(t *testing.T, restart *childProcess, deliveredAtKill []uint64, committedAtKill uint64, maxReplay int) {
	t.Helper()
	had := make(map[uint64]bool, len(deliveredAtKill))
	for _, p := range deliveredAtKill {
		had[p] = true
	}
	replayed := 0
	for _, p := range destWrites(restart) {
		if had[p] {
			replayed++
		}
	}
	if replayed > maxReplay {
		t.Fatalf("restart replayed %d already-delivered positions, more than the bound of %d (one batch)", replayed, maxReplay)
	}
	if got := maxProgressPosition(t, restart, markerLCResume); got != committedAtKill {
		t.Fatalf("restart resumed from %d, want the position persisted at the kill, %d", got, committedAtKill)
	}
}

// timerFlushScenario is the common shape of the single-destination timer
// scenarios: batch size 10, total 25 (two full batches, a tail of 5).
func timerFlushScenario(dir string, delayMS int) lcChildConfig {
	return lcChildConfig{
		dbDir:            dir + "/db",
		upstreamDir:      dir + "/upstream",
		mainDir:          dir + "/main-delivered",
		dlqDir:           dir + "/dlq-delivered",
		total:            25,
		paceMS:           3,
		destBatchSize:    10,
		destBatchDelayMS: delayMS,
		minDelayMS:       20,
		maxDelayMS:       20,
	}
}

// restartAfterTimerKill finishes a killed timer scenario: a graceful restart
// against the same state with a short sdk.batch.delay, so the tail is flushed
// by the timer and the child waits for its ack before stopping.
func restartAfterTimerKill(t *testing.T, cfg lcChildConfig) *childProcess {
	t.Helper()
	restart := pipelinedFlusherRestart(cfg)
	second := spawnLCChild(t, restart)
	second.waitExit(t, parentWaitExit)
	_, done := second.line(markerLCDone)
	if !done {
		t.Fatalf("restart did not finish\n%s", second.diagnostics())
	}
	return second
}

// TestSIGKILL_PartialBatchHeldByTimerWindow proves a partial destination
// batch held by the flush timer is not lost to a hard crash inside the timer
// window. The destination batches 10 records with sdk.batch.delay 2s, the
// total (25) is not a multiple of the batch, and the source idles after its
// last record, so records 21..25 sit buffered, unwritten and unacked, waiting
// for the timer. The child is SIGKILLed at a seeded random instant inside that
// 2s window, then restarted.
//
// Invariants:
//   - 1 (ack only after durable write): at the kill nothing of the tail is
//     delivered, and the persisted position stays at the last full batch (20),
//     never past what the destination holds.
//   - 2 (monotonic positions): the restart resumes exactly from the position
//     persisted at the kill.
//   - 3 (at-least-once): after the restart the ledger reaches total with no
//     gap; the replay is at most one batch; and per-source order holds in
//     both processes.
//
// It also fails if the timer never flushes the tail: the restart's graceful
// child waits for the tail to be acked before it stops, which only the timer
// can bring about.
func TestSIGKILL_PartialBatchHeldByTimerWindow(t *testing.T) {
	is := is.New(t)
	rnd := chaosRand(t)
	cfg := timerFlushScenario(t.TempDir(), 2000)

	first := spawnLCChild(t, cfg)
	// Both full batches are flushed by size and acked: the position the
	// restart will resume from is exactly 20.
	first.waitForReadCount(t, int(cfg.total), 10*time.Second)
	readAll := time.Now()
	waitForUpstreamCommittedAtLeast(t, first, cfg.upstreamDir, 20, 10*time.Second)

	// The tail's timer was armed by record 21, a few paces before the last
	// read, and fires 2s later. [100ms, 1200ms] after the last read leaves
	// 800ms of margin for the parent being descheduled.
	killAt := randDuration(rnd, 100*time.Millisecond, 1200*time.Millisecond)
	t.Logf("kill %s after the last record was read (timer window: 2s)", killAt)
	time.Sleep(time.Until(readAll.Add(killAt)))
	first.sigkill(t)

	committedAtKill := assertRecoveryInvariantsAtKill(t, cfg)
	is.Equal(committedAtKill, uint64(20)) // only the full batches were acked; the held tail was not
	mainLog, err := openDeliveryLog(cfg.mainDir)
	is.NoErr(err)
	deliveredAtKill, err := mainLog.Positions()
	is.NoErr(err)
	if len(deliveredAtKill) != 20 {
		t.Fatalf("at the kill the destination holds %d positions, want 20: the tail was flushed before the kill, so the kill missed the timer window (%s after the last read)\n%v",
			len(deliveredAtKill), killAt, deliveredAtKill)
	}
	assertWriteOrder(t, "killed process", destWrites(first))

	second := restartAfterTimerKill(t, cfg)

	assertRecoveryInvariantsAfterRestart(t, cfg)
	assertWriteOrder(t, "restart", destWrites(second))
	assertBoundedReplay(t, second, deliveredAtKill, committedAtKill, cfg.destBatchSize)
	assertTailWrittenBeforeStop(t, second, cfg.total)
}

// TestSIGKILL_DuringTimerFlush proves a hard crash while the flush timer's own
// Write is in flight loses nothing and acks nothing early. The destination
// batches 10 records with sdk.batch.delay 500ms and its write of the tail
// (the only slow request, 1.5s) is held open: half before the records are
// recorded, half after they are recorded and before they are acked. The child
// is SIGKILLed at a seeded instant inside that 1.5s, so depending on the seed
// the batch is not yet durable or durable but unacked, then restarted.
//
// Invariants:
//   - 1 (ack only after durable write): the persisted position at the kill is
//     still 20, though the tail may already be durably recorded: the timer
//     flush did not ack before its Write and Ack completed.
//   - 3 (at-least-once): after the restart the ledger reaches total with no
//     gap, the replay is at most one batch, and the tail is flushed by the
//     timer again (the restart waits for its ack before stopping).
//   - 2: the restart resumes from the position persisted at the kill, 20.
func TestSIGKILL_DuringTimerFlush(t *testing.T) {
	is := is.New(t)
	rnd := chaosRand(t)
	cfg := timerFlushScenario(t.TempDir(), 500)
	cfg.destWriteDelayMS = 1500
	cfg.destSlowAfter = 20 // only the request carrying the tail is slow

	first := spawnLCChild(t, cfg)
	// The tail request reaching the destination proves the timer flushed it
	// (nothing else flushes a partial batch while the source idles).
	first.waitForMarker(t, markerLCDestReq+" 21 25", 15*time.Second)
	inFlight := time.Now()
	killAt := randDuration(rnd, 50*time.Millisecond, 1400*time.Millisecond)
	t.Logf("kill %s into the timer flush's %dms Write", killAt, cfg.destWriteDelayMS)
	time.Sleep(time.Until(inFlight.Add(killAt)))
	first.sigkill(t)

	committedAtKill := assertRecoveryInvariantsAtKill(t, cfg)
	is.Equal(committedAtKill, uint64(20)) // the in-flight flush was not acked
	mainLog, err := openDeliveryLog(cfg.mainDir)
	is.NoErr(err)
	deliveredAtKill, err := mainLog.Positions()
	is.NoErr(err)
	assertWriteOrder(t, "killed process", destWrites(first))

	second := restartAfterTimerKill(t, cfg)

	assertRecoveryInvariantsAfterRestart(t, cfg)
	assertWriteOrder(t, "restart", destWrites(second))
	assertBoundedReplay(t, second, deliveredAtKill, committedAtKill, cfg.destBatchSize)
	assertTailWrittenBeforeStop(t, second, cfg.total)
}

// TestSIGTERM_TimerFlushedTailCheckpointed proves a tail flushed by the timer
// is acked and checkpointed, so a graceful stop afterwards leaves nothing to
// replay. The destination batches 10 records with sdk.batch.delay 300ms and
// the source idles after record 25. The test waits until the upstream ledger
// shows the tail acked (committed through 25), which only the timer can cause
// while the source idles, and only then sends SIGTERM. The child stops the
// pipeline gracefully and exits 0; the restart must resume from 25 and write
// no position 1..25 again.
//
// Invariants:
//   - 1: the tail is acked upstream only after the destination durably holds it.
//   - 7 (graceful shutdown drains and checkpoints): the SIGTERM exits cleanly
//     with the position checkpointed at total.
//   - 2: the persisted position is exactly total after the stop.
//   - 3: nothing is lost and, because the tail was acked before the stop,
//     nothing is replayed.
//
// The source plugin emits one record past its total when it resumes at total
// (see recoverySourcePlugin.produceLoop), so the restart may write 26; the
// assertion is that it writes nothing at or below total.
func TestSIGTERM_TimerFlushedTailCheckpointed(t *testing.T) {
	is := is.New(t)
	cfg := timerFlushScenario(t.TempDir(), 300)
	cfg.awaitSigterm = true

	first := spawnLCChild(t, cfg)
	// Acked before the SIGTERM: this wait is the proof the timer flushed it.
	waitForUpstreamCommittedAtLeast(t, first, cfg.upstreamDir, cfg.total, 15*time.Second)
	first.sigterm(t, parentWaitExit)
	_, exited := first.line(markerLCSigtermExit)
	is.True(exited)
	is.Equal(destWrites(first), func() []uint64 {
		out := make([]uint64, cfg.total)
		for i := range out {
			out[i] = uint64(i) + 1
		}
		return out
	}()) // every position written exactly once, in order, before the stop

	upstream, err := openUpstreamStore(cfg.upstreamDir, false)
	is.NoErr(err)
	committed, err := upstream.Committed()
	is.NoErr(err)
	is.Equal(committed, cfg.total)

	restart := cfg
	restart.awaitSigterm = false
	restart.graceful = true
	second := spawnLCChild(t, restart)
	second.waitExit(t, parentWaitExit)
	_, done := second.line(markerLCDone)
	is.True(done)

	is.Equal(maxProgressPosition(t, second, markerLCResume), cfg.total) // checkpointed at total, not behind it
	for _, p := range destWrites(second) {
		if p <= cfg.total {
			t.Fatalf("restart replayed position %d, but the tail was acked and checkpointed before the SIGTERM", p)
		}
	}
}

// TestSIGKILL_FanOutTimerFlush proves the flush timer on the synchronous
// fan-out path does not let the source ack run ahead of the slower
// destination. Two destinations batch 10 records but have different
// sdk.batch.delay (A 1s, B 3s), and the source idles after record 25, so each
// destination's timer flushes the tail of 5 at its own deadline: A at about
// 1s, B at about 3s. The child is SIGKILLed at a seeded instant between the
// two deadlines, when A holds the tail durably and B does not, then restarted.
//
// Invariants:
//   - 1 (ack only after durable write, on every branch): at the kill the
//     persisted position is no further than what B durably holds (20), though
//     A already holds everything; the source is acked only when both branches
//     wrote, so A's earlier timer flush acked nothing.
//   - 2 (monotonic positions): the persisted position sampled while waiting
//     for the kill never decreases.
//   - 3 (at-least-once): after the restart both destinations hold every
//     position 1..total (A's tail is a tolerated duplicate) and the persisted
//     position reaches total.
func TestSIGKILL_FanOutTimerFlush(t *testing.T) {
	is := is.New(t)
	rnd := chaosRand(t)
	dir := t.TempDir()
	cfg := fanoutChildConfig{
		dbDir:             dir + "/db",
		upstreamDir:       dir + "/upstream",
		destADir:          dir + "/destA",
		destBDir:          dir + "/destB",
		dlqDir:            dir + "/dlq",
		total:             25,
		paceMS:            3,
		destABatch:        10,
		destABatchDelayMS: 1000,
		destBBatch:        10,
		destBBatchDelayMS: 3000,
	}

	first := spawnChildWithEnv(t, cfg.env())
	first.waitForReadCount(t, int(cfg.total), 10*time.Second)
	readAll := time.Now()

	// Deadlines are 1s and 3s after record 21 was buffered, a few paces
	// before the last read; [1.3s, 2.7s] keeps 300ms of margin on each side.
	killAt := randDuration(rnd, 1300*time.Millisecond, 2700*time.Millisecond)
	t.Logf("kill %s after the last record was read (timer deadlines: A 1s, B 3s)", killAt)
	upstream, err := openUpstreamStore(cfg.upstreamDir, false)
	is.NoErr(err)
	var prev uint64
	for time.Now().Before(readAll.Add(killAt)) {
		got, err := upstream.Committed()
		is.NoErr(err)
		if got < prev {
			t.Fatalf("persisted position went backwards: %d after %d", got, prev)
		}
		prev = got
		time.Sleep(2 * time.Millisecond)
	}
	first.sigkill(t)

	logA, err := openDeliveryLog(cfg.destADir)
	is.NoErr(err)
	logB, err := openDeliveryLog(cfg.destBDir)
	is.NoErr(err)
	preA, err := logA.Positions()
	is.NoErr(err)
	preB, err := logB.Positions()
	is.NoErr(err)
	committedAtKill, err := upstream.Committed()
	is.NoErr(err)
	if committedAtKill < prev {
		t.Fatalf("persisted position went backwards across the kill: %d after %d", committedAtKill, prev)
	}
	if len(preA) != int(cfg.total) || len(preB) != 20 {
		t.Fatalf("at the kill A holds %d positions and B %d, want 25 and 20: the kill missed the window between the two deadlines (%s after the last read)",
			len(preA), len(preB), killAt)
	}
	// Invariant 1: nothing is acked that either branch lacks.
	assertNoGapThrough(t, preA, committedAtKill, "invariant 1: persisted a position destination A never durably received")
	assertNoGapThrough(t, preB, committedAtKill, "invariant 1: persisted a position destination B never durably received")
	is.True(committedAtKill <= 20) // B never held the tail, so the source cannot have been acked for it

	restart := cfg
	restart.destABatchDelayMS, restart.destBBatchDelayMS = 200, 400 // shorter deadlines: the restart need not wait 3s
	second := spawnChildWithEnv(t, restart.env())
	second.waitForMarker(t, markerFanoutDone, 45*time.Second)
	second.waitExit(t, 10*time.Second)

	finalA, err := logA.Positions()
	is.NoErr(err)
	finalB, err := logB.Positions()
	is.NoErr(err)
	assertGaplessDelivery(t, "destA", finalA, cfg.total)
	assertGaplessDelivery(t, "destB", finalB, cfg.total)
	committed, err := upstream.Committed()
	is.NoErr(err)
	is.Equal(committed, cfg.total)
	is.True(committed >= committedAtKill) // monotonic across the restart
}
