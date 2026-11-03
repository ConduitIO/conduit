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

// TestSIGKILL_FanoutSiblingNack_BatchingDestinationGapless is the chaos case for
// #3323: one source fans out to destA, which batches (sdk.batch.size 50, so it
// holds up to 49 records unwritten), and destB, which fails every 5th position;
// the DLQ accepts every nack. The persister debounce is 5ms, so an acked
// position is durable well before destA's next flush. Before the fix, destB's nack of a later record
// was released (DLQ write, then Source.Ack) while destA still buffered earlier
// records, so the source position moved past them. A SIGKILL at that moment
// lost them from destA for good: the restart resumes after the acked position.
//
// The child is SIGKILLed mid-run, restarted against the same state, and run to
// completion. Duplicates are allowed (at-least-once); a gap is not:
//   - destA (never nacks) must hold every position 1..total;
//   - every position must be in destB's ledger or in the DLQ ledger.
func TestSIGKILL_FanoutSiblingNack_BatchingDestinationGapless(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	const total = 200

	cfg := fanoutChildConfig{
		dbDir:          dir + "/db",
		upstreamDir:    dir + "/upstream",
		destADir:       dir + "/destA",
		destBDir:       dir + "/destB",
		dlqDir:         dir + "/dlq",
		total:          total,
		paceMS:         2,
		destABatch:     50,
		destBNackEvery: 5,
		persistDelayMS: 5,
	}

	cp := spawnChildWithEnv(t, cfg.env())
	// Past destA's second flush (100), inside its third buffer: before the
	// fix the source was acked through destB's nack of 105, 110, ... while
	// destA held 101 onwards.
	cp.waitForReadCount(t, 125, 15*time.Second)
	cp.sigkill(t)

	cp2 := spawnChildWithEnv(t, cfg.env())
	// The restart must run to total. A run that stops short exits without the
	// done marker; fail on it as a harness failure, so a gap reported below is
	// never one the run simply did not reach.
	for deadline := time.Now().Add(45 * time.Second); ; time.Sleep(time.Millisecond) {
		if _, ok := cp2.line(markerFanoutDone); ok {
			break
		}
		if line, ok := cp2.line(markerFanoutShortCommit); ok {
			t.Fatalf("restart run stopped short (harness failure, not a delivery result): %s\n%s", line, cp2.diagnostics())
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for marker %q\n%s", markerFanoutDone, cp2.diagnostics())
		}
	}
	cp2.waitExit(t, 10*time.Second)

	logA, err := openDeliveryLog(cfg.destADir)
	is.NoErr(err)
	logB, err := openDeliveryLog(cfg.destBDir)
	is.NoErr(err)
	logDLQ, err := openDeliveryLog(cfg.dlqDir)
	is.NoErr(err)

	finalA, err := logA.Positions()
	is.NoErr(err)
	finalB, err := logB.Positions()
	is.NoErr(err)
	finalDLQ, err := logDLQ.Positions()
	is.NoErr(err)

	assertGaplessDelivery(t, "destA", finalA, total)
	assertGaplessDelivery(t, "destB+dlq", append(finalB, finalDLQ...), total)
	for _, p := range finalB {
		is.True(p%5 != 0) // destB never records a position it failed
	}
}
