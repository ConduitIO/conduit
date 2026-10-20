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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matryer/is"
)

// The classic engine applies a default sdk.batch.size/sdk.batch.delay to the
// built-in postgres and kafka destinations
// (docs/design-documents/20261020-classic-default-destination-batching.md).
// These scenarios SIGKILL a process that runs the classic engine's
// connector.Destination over the real SDK batch write strategy, and check the
// invariants a batch must keep:
//
//   - invariant 1: no record is acked unless it is durable (acked is a subset
//     of the fsynced delivery log);
//   - invariant 3: after the restart, which replays everything after the last
//     ack, no position is missing (duplicates are allowed);
//   - the default really batches (a Write received more than one record), and
//     sdk.batch.size=1 really does not.

const (
	classicBatchTotal   = 300
	classicBatchTimeout = 30 * time.Second
)

func ackedPositions(c *childProcess) (all []uint64, highest uint64) {
	for _, l := range c.linesWithPrefix(markerClassicAcked) {
		n, err := strconv.ParseUint(strings.TrimPrefix(l, markerClassicAcked), 10, 64)
		if err == nil {
			all = append(all, n)
			if n > highest {
				highest = n
			}
		}
	}
	return all, highest
}

func writeSizes(c *childProcess) (sizes []int) {
	for _, l := range c.linesWithPrefix(markerClassicWrite) {
		n, err := strconv.Atoi(strings.TrimPrefix(l, markerClassicWrite))
		if err == nil {
			sizes = append(sizes, n)
		}
	}
	return sizes
}

func requireCompleteT(t *testing.T, dir string, total uint64) {
	t.Helper()
	is := is.New(t)
	got, err := (&deliveryLog{dir: dir}).Positions()
	is.NoErr(err)
	seen := map[uint64]bool{}
	for _, p := range got {
		seen[p] = true
	}
	for p := uint64(1); p <= total; p++ {
		if !seen[p] {
			t.Fatalf("position %d was never delivered (invariant 3)", p)
		}
	}
}

// TestSIGKILL_ClassicDefaultBatch_MidBatch kills the process while the first
// default batch is inside the connector's Write and nothing is durable yet:
// nothing may be acked and nothing may be in the log, and after the restart
// (which replays from position 1) no position is missing.
func TestSIGKILL_ClassicDefaultBatch_MidBatch(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	first := spawnChildWithEnv(t, classicBatchChildConfig{logDir: dir, from: 1, total: classicBatchTotal, paceMS: 2, block: true}.env())
	first.waitForMarker(t, markerClassicBlock, classicBatchTimeout)
	first.sigkill(t)

	sizes := writeSizes(first)
	is.True(len(sizes) > 0)
	is.True(sizes[0] > 1) // the default batched: one Write carried several records
	acked, _ := ackedPositions(first)
	is.Equal(len(acked), 0) // invariant 1: the open batch was never acked
	got, err := (&deliveryLog{dir: dir}).Positions()
	is.NoErr(err)
	is.Equal(len(got), 0) // and nothing was written

	second := spawnChildWithEnv(t, classicBatchChildConfig{logDir: dir, from: 1, total: classicBatchTotal, paceMS: 2}.env())
	second.waitForMarker(t, markerClassicDone, classicBatchTimeout)
	second.waitExit(t, classicBatchTimeout)
	requireCompleteT(t, dir, classicBatchTotal)
}

// TestSIGKILL_ClassicDefaultBatch_MidStream kills the process after at least
// one batch was acked and while later records are still in flight or pending.
// Every acked record must be durable, and the restart from the last ack must
// leave no gap.
func TestSIGKILL_ClassicDefaultBatch_MidStream(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	first := spawnChildWithEnv(t, classicBatchChildConfig{logDir: dir, from: 1, total: classicBatchTotal, paceMS: 5}.env())
	first.waitForMarker(t, markerClassicAcked, classicBatchTimeout)
	first.sigkill(t)

	acked, maxAcked := ackedPositions(first)
	is.True(len(acked) > 0)
	got, err := (&deliveryLog{dir: dir}).Positions()
	is.NoErr(err)
	durable := map[uint64]bool{}
	for _, p := range got {
		durable[p] = true
	}
	for _, p := range acked {
		is.True(durable[p]) // invariant 1: acked implies durable
	}
	is.True(maxAcked < classicBatchTotal) // killed mid-run, not after it

	// The source resumes after the last ack.
	second := spawnChildWithEnv(t, classicBatchChildConfig{logDir: dir, from: maxAcked + 1, total: classicBatchTotal, paceMS: 2}.env())
	second.waitForMarker(t, markerClassicDone, classicBatchTimeout)
	second.waitExit(t, classicBatchTimeout)
	requireCompleteT(t, dir, classicBatchTotal)
}

// TestClassicDefaultBatch_OptOutWritesOneRecordAtATime: with the user's
// sdk.batch.size=1 nothing is injected, so every Write carries one record, and
// the run completes. The default run, on the other hand, batches.
func TestClassicDefaultBatch_OptOutWritesOneRecordAtATime(t *testing.T) {
	is := is.New(t)

	optOut := spawnChildWithEnv(t, classicBatchChildConfig{logDir: t.TempDir(), from: 1, total: 100, paceMS: 1, settings: "sdk.batch.size=1"}.env())
	optOut.waitForMarker(t, markerClassicDone, classicBatchTimeout)
	optOut.waitExit(t, classicBatchTimeout)
	sizes := writeSizes(optOut)
	is.Equal(len(sizes), 100)
	for _, n := range sizes {
		is.Equal(n, 1)
	}

	def := spawnChildWithEnv(t, classicBatchChildConfig{logDir: t.TempDir(), from: 1, total: 100, paceMS: 1}.env())
	def.waitForMarker(t, markerClassicDone, classicBatchTimeout)
	def.waitExit(t, classicBatchTimeout)
	most := 0
	for _, n := range writeSizes(def) {
		if n > most {
			most = n
		}
	}
	is.True(most > 1)
	acked, _ := ackedPositions(def)
	is.Equal(len(acked), 100) // Stop flushed the last batch (invariant 7) and every record was acked
}
