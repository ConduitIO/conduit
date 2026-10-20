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
// total is a multiple of destBatchSize so the graceful restart child flushes
// exactly whole batches (no trailing partial batch to confound its
// waitForLCTotalAcked exit gate, which expects the source to ack through total
// during normal processing).
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

	restart := lcChildConfig{
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
	second := spawnLCChild(t, restart)
	second.waitExit(t, parentWaitExit)
	_, done := second.line(markerLCDone)
	is.True(done)

	assertRecoveryInvariantsAfterRestart(t, cfg)
}
