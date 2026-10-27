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
	"testing"
	"time"

	"github.com/matryer/is"
)

// TestSIGKILL_PipelinedFlusher_CrashMidWindow proves the read-ahead pipelining
// (docs/design-documents/20261019-funnel-read-ahead-pipelining.md) does not
// weaken invariant 1 or invariant 3 across a hard crash: the destination has
// sdk.batch.size set, so the funnel's destination-write batching AND the
// pipelined flusher are active, and the child is SIGKILLed while the flusher
// has batches in flight (records written to the destination but not yet acked
// upstream). It reuses recovery_test.go's cross-process machinery and both of
// its invariant assertions:
//
//   - Invariant 1 at the kill (assertRecoveryInvariantsAtKill): every position
//     the upstream ledger has committed at the instant of the kill was already
//     durably delivered to the destination — the flusher must not have acked
//     upstream before the destination's durable write.
//   - Invariant 3 after restart (assertRecoveryInvariantsAfterRestart): the
//     upstream ledger reaches exactly cfg.total and the destination's delivery
//     ledger covers every position 1..total at least once — in-flight batches
//     are replayed, never lost.
//
// The restart runs without destination batching; see pipelinedFlusherRestart.
func TestSIGKILL_PipelinedFlusher_CrashMidWindow(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	cfg := lcChildConfig{
		dbDir:         dir + "/db",
		upstreamDir:   dir + "/upstream",
		mainDir:       dir + "/main-delivered",
		dlqDir:        dir + "/dlq-delivered",
		total:         64, // 8 whole batches of 8
		paceMS:        3,
		failAt:        0,  // no injected transient error: a single, plain crashable run
		holdAt:        32, // hard ceiling on this process's production; see lcChildConfig.holdAt
		destBatchSize: 8,  // enables destination-write batching + read-ahead pipelining
		minDelayMS:    20,
		maxDelayMS:    20,
		graceful:      false,
	}

	// killAfterCommitted brackets the kill with cfg.holdAt exactly as
	// TestSIGKILL_RecoveryLoop_CrashDuringRecoveredRun does: the watermark at
	// the kill is in [16, 32], strictly inside (0, total), so records are in
	// flight (buffered in the flusher, or produced-but-not-yet-committed) when
	// the SIGKILL lands.
	const killAfterCommitted = 16

	first := spawnLCChild(t, cfg)
	waitForUpstreamCommittedAtLeast(t, first, cfg.upstreamDir, killAfterCommitted, 10*time.Second)
	first.sigkill(t)

	committedAtKill := assertRecoveryInvariantsAtKill(t, cfg)
	is.True(committedAtKill >= killAfterCommitted) // the gate's own guarantee, restated against the ledger
	is.True(committedAtKill <= cfg.holdAt)         // the production ceiling, not the margin, is what bounds the kill
	is.True(committedAtKill < cfg.total)

	second := spawnLCChild(t, pipelinedFlusherRestart(cfg))
	second.waitExit(t, parentWaitExit)
	_, done := second.line(markerLCDone)
	is.True(done)

	assertRecoveryInvariantsAfterRestart(t, cfg)
}

// TestSIGKILL_PipelinedFlusher_RestartFromUnalignedPosition pins the restart
// half of TestSIGKILL_PipelinedFlusher_CrashMidWindow to the case that made it
// flaky: a resume position that is not a multiple of the destination batch
// size.
//
// The kill in that test lands at an arbitrary instant, and flushPending acks
// the source batches of one flush one at a time, so the persisted position at
// the kill can fall inside a batch (CI saw a restart resume at 22 and read
// 23..64). A batched restart then has (total-resume) mod batchSize records
// left in a trailing partial batch once the source stops producing. The funnel
// flushes a partial batch only on the next arrival or on Close (by design:
// docs/design-documents/20261017-funnel-destination-batching.md), and Close
// runs after the source is torn down, so those records are never acked
// upstream and the graceful child's "acked through total" gate times out.
//
// Here the first child runs unbatched with production capped at 22, so the
// resume position is exactly 22 on every run, and the restart is batched at 8.
// It used to time out deterministically (#3231); Worker.Stop now drains the
// trailing partial batch before tearing the source down.
func TestSIGKILL_PipelinedFlusher_RestartFromUnalignedPosition(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	const resumeAt = 22 // not a multiple of the scenario's batch size of 8
	cfg := lcChildConfig{
		dbDir:         dir + "/db",
		upstreamDir:   dir + "/upstream",
		mainDir:       dir + "/main-delivered",
		dlqDir:        dir + "/dlq-delivered",
		total:         64,
		paceMS:        3,
		holdAt:        resumeAt,
		destBatchSize: 0, // unbatched, so every produced record is acked and the resume position is exactly resumeAt
		minDelayMS:    20,
		maxDelayMS:    20,
		graceful:      false,
	}

	first := spawnLCChild(t, cfg)
	waitForUpstreamCommittedAtLeast(t, first, cfg.upstreamDir, resumeAt, 10*time.Second)
	first.sigkill(t)
	is.Equal(assertRecoveryInvariantsAtKill(t, cfg), uint64(resumeAt))

	restart := pipelinedFlusherRestart(cfg)
	restart.destBatchSize = 8 // batched restart from an unaligned resume: needs Stop's drain (#3231)
	second := spawnLCChild(t, restart)
	second.waitExit(t, parentWaitExit)
	_, done := second.line(markerLCDone)
	is.True(done)

	assertRecoveryInvariantsAfterRestart(t, cfg)
}

// pipelinedFlusherRestart returns the graceful restart config for a crashed
// pipelined-flusher child: same directories and total, run to completion.
//
// Destination batching stays ON, as in the crashed child: the restart resumes
// from an arbitrary position, so it ends with a trailing partial batch that has
// no flush timer. Worker.Stop drains that batch and acks it while the source is
// still running (#3231), which is exactly what this restart now exercises; the
// child therefore waits for production, not for the ack, before stopping (see
// runChildLifecycle).
func pipelinedFlusherRestart(cfg lcChildConfig) lcChildConfig {
	return lcChildConfig{
		dbDir:         cfg.dbDir,
		upstreamDir:   cfg.upstreamDir,
		mainDir:       cfg.mainDir,
		dlqDir:        cfg.dlqDir,
		total:         cfg.total,
		paceMS:        cfg.paceMS,
		failAt:        0,
		destBatchSize: cfg.destBatchSize,
		minDelayMS:    10,
		maxDelayMS:    10,
		graceful:      true,
	}
}
