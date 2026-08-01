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

package funnel

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/matryer/is"
)

// This file is the randomised, full-stack regression suite for #2728 and
// #2729, run through the REAL ProcessorTask and DestinationTask (not the
// hand-driven Batch manipulation run_ledger_test.go's property test uses) -
// see fullPlanOutcome below for the shapes it exercises.

// fullPlanOutcome enumerates what a single original source record does at
// the processor stage in TestFullPipeline_Property_MixedProcessorOutputAndDestinationFailure.
type fullPlanOutcome int

const (
	fpFilterRecord fullPlanOutcome = iota // sdk.FilterRecord{}
	fpErrorRecord                         // sdk.ErrorRecord{}
	fpMultiZero                           // sdk.MultiRecord{} (0 records) - equivalent to Filter
	fpMultiOne                            // sdk.MultiRecord with 1 record - no split (SetRecords)
	fpMultiTwo                            // sdk.MultiRecord with 2 records - splits (SplitRecord)
	fpPassthrough                         // sdk.SingleRecord, unchanged
)

// propProcessor is a minimal, non-gomock funnel.Processor test double whose
// Process call returns a precomputed, per-iteration plan: one
// sdk.ProcessedRecord per input record, same order. Real processors decide
// this dynamically; this test cares about ProcessorTask's handling of every
// shape combination, not about processor logic itself.
type propProcessor struct {
	process func(context.Context, []opencdc.Record) []sdk.ProcessedRecord
}

func (p *propProcessor) Open(context.Context) error     { return nil }
func (p *propProcessor) Teardown(context.Context) error { return nil }
func (p *propProcessor) Process(ctx context.Context, recs []opencdc.Record) []sdk.ProcessedRecord {
	return p.process(ctx, recs)
}

// fullPlanFor decides, for a single original record and a randomly-chosen
// outcome, what the fake processor should output for it, what (if anything)
// the fake destination should fail, and whether the record is expected to end
// up acked or DLQ'd. Split out from
// TestFullPipeline_Property_MixedProcessorOutputAndDestinationFailure to keep
// that function's cyclomatic complexity down - the branching lives here, in a
// function with a single job (plan one record), not interleaved with running
// the pipeline and asserting on it.
func fullPlanFor(rng *rand.Rand, id string, rec opencdc.Record, o fullPlanOutcome) (proc sdk.ProcessedRecord, destFailPos string, acked, dlqd bool, destFailErr error) {
	switch o {
	case fpFilterRecord:
		return sdk.FilterRecord{}, "", true, false, nil
	case fpErrorRecord:
		return sdk.ErrorRecord{Error: cerrors.New("processor error " + id)}, "", false, true, nil
	case fpMultiZero:
		return sdk.MultiRecord{}, "", true, false, nil
	case fpMultiOne:
		// A 1-element MultiRecord goes through SetRecords, not SplitRecord -
		// it never creates a run, and Batch's own tracked position (used for
		// the eventual source ack) is untouched by SetRecords. Deliberately
		// leave the piece's own Record.Position equal to the original id (do
		// NOT rename it the way fpMultiTwo's pieces must be renamed):
		// originalBatch only restores the true original content for a
		// SplitRecord run (via splitRecords), so a renamed SetRecords piece
		// would carry its own (different) position all the way into the DLQ
		// envelope, and this test's DLQ-lookup-by-id would no longer find it
		// under id - a test artifact, not anything Nack itself gets wrong.
		piece := rec.Clone()
		if rng.Intn(2) == 0 {
			return sdk.MultiRecord{piece}, id, false, true, cerrors.New("dest failed " + id)
		}
		return sdk.MultiRecord{piece}, "", true, false, nil
	case fpMultiTwo:
		pieceA := rec.Clone()
		pieceA.Position = opencdc.Position(id + "-a")
		pieceB := rec.Clone()
		pieceB.Position = opencdc.Position(id + "-b")
		return sdk.MultiRecord{pieceA, pieceB}, "", false, false, nil
	case fpPassthrough:
		// Same reasoning as fpMultiOne: leave the position as-is.
		piece := rec.Clone()
		if rng.Intn(2) == 0 {
			return sdk.SingleRecord(piece), id, false, true, cerrors.New("dest failed " + id)
		}
		return sdk.SingleRecord(piece), "", true, false, nil
	default:
		panic(fmt.Sprintf("fullPlanFor: unhandled outcome %d", o))
	}
}

// fullPlanMultiTwoFailures decides, for an fpMultiTwo outcome, which of the
// two split pieces (if any) the fake destination should fail - split out
// separately because fpMultiTwo is the one outcome where BOTH pieces can
// independently fail, which fullPlanFor's single (pos, err) return can't
// represent.
func fullPlanMultiTwoFailures(rng *rand.Rand, id string, proc sdk.ProcessedRecord) (destFail map[string]error, dlqd bool) {
	multi, ok := proc.(sdk.MultiRecord)
	if !ok || len(multi) != 2 {
		return nil, false
	}
	destFail = make(map[string]error, 2)
	failA := rng.Intn(3) == 0
	failB := rng.Intn(3) == 0
	if failA {
		destFail[string(multi[0].Position)] = cerrors.New("dest failed " + id + "-a")
	}
	if failB {
		destFail[string(multi[1].Position)] = cerrors.New("dest failed " + id + "-b")
	}
	return destFail, failA || failB // nack-wins across the run (run_ledger.go)
}

// TestFullPipeline_Property_MixedProcessorOutputAndDestinationFailure is the
// randomised test requested alongside the #2728/#2729 fixes: over many random
// combinations of processor output shapes - including multiple ADJACENT
// MultiRecords of varying arity in the same Process() call, which is exactly
// what stresses ProcessorTask.markBatchRecords' reversed MultiRecord loop
// (#2728) - and independent, random per-piece destination failures (which
// stresses DestinationTask.markBatchRecords and Batch.Nack's now-independent-
// per-record semantics, #2729), every original source record ends up acked
// exactly once XOR DLQ'd exactly once - never both, never neither.
func TestFullPipeline_Property_MixedProcessorOutputAndDestinationFailure(t *testing.T) {
	const iterations = 300
	seed := time.Now().UnixNano()
	t.Logf("seed=%d (reproduce a failure by hardcoding this seed)", seed)
	rng := rand.New(rand.NewSource(seed))

	for iter := 0; iter < iterations; iter++ {
		n := 2 + rng.Intn(6) // 2..7 original records

		records := make([]opencdc.Record, n)
		outcomes := make([]fullPlanOutcome, n)
		for i := 0; i < n; i++ {
			records[i] = opencdc.Record{
				Position:  opencdc.Position("p" + strconv.Itoa(i)),
				Operation: opencdc.OperationCreate,
				Metadata:  opencdc.Metadata{},
				Payload:   opencdc.Change{After: opencdc.RawData("v" + strconv.Itoa(i))},
			}
			outcomes[i] = fullPlanOutcome(rng.Intn(6))
		}

		// destFail is keyed by a PIECE's own Record.Position (what the fake
		// destination keys writes/nacks by - see fakeDestination.Write) -
		// distinct from Batch's own tracked original position.
		destFail := make(map[string]error)
		wantAcked := make(map[string]bool) // plan_id -> must end up acked
		wantDLQd := make(map[string]bool)  // plan_id -> must end up DLQ'd

		processed := make([]sdk.ProcessedRecord, n)
		for i, o := range outcomes {
			id := string(records[i].Position)
			proc, failPos, acked, dlqd, failErr := fullPlanFor(rng, id, records[i], o)
			processed[i] = proc
			if failPos != "" {
				destFail[failPos] = failErr
			}
			if o == fpMultiTwo {
				multiFail, multiDLQd := fullPlanMultiTwoFailures(rng, id, proc)
				for pos, err := range multiFail {
					destFail[pos] = err
				}
				acked, dlqd = !multiDLQd, multiDLQd
			}
			// Only ever record a positive: wantAcked/wantDLQd's LENGTH is
			// used below as "how many records are expected to end up DLQ'd",
			// so an id must be absent (not present with a false value) when
			// this particular outcome doesn't want it.
			if acked {
				wantAcked[id] = true
			}
			if dlqd {
				wantDLQd[id] = true
			}
		}

		src := newFakeSource("src", append([]opencdc.Record(nil), records...))
		dest := newFakeDestination("dest")
		dlqDest := newFakeDestination("dlq")
		for pos, err := range destFail {
			dest.setNackErr(opencdc.Position(pos), err)
		}
		logger := log.Test(t)
		dlq := NewDLQ("dlq", dlqDest, logger, NoOpConnectorMetrics{}, 0, 0)

		proc := &propProcessor{process: func(_ context.Context, _ []opencdc.Record) []sdk.ProcessedRecord {
			return processed
		}}
		procNode := &TaskNode{Task: NewProcessorTask("proc", proc, logger, NoOpProcessorMetrics{})}
		destNode := &TaskNode{Task: NewDestinationTask(dest.id, dest, logger, NoOpConnectorMetrics{})}
		srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{procNode}}
		procNode.Next = []*TaskNode{destNode}

		w, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
		if err != nil {
			t.Fatalf("iter %d (seed %d): NewWorker: %v", iter, seed, err)
		}

		batch := NewBatch(append([]opencdc.Record(nil), records...))
		if err := w.doTask(context.Background(), procNode, batch, newRunAckNacker(w)); err != nil {
			t.Fatalf("iter %d (seed %d): doTask: %v (outcomes=%v)", iter, seed, err, outcomes)
		}

		acked := src.ackedPositions()
		ackedSet := make(map[string]int, len(acked))
		for _, p := range acked {
			ackedSet[string(p)]++
		}

		dlqIDs := make(map[string]int)
		for _, r := range dlqDest.written {
			dlqIDs[string(r.Position)]++
		}

		for i := 0; i < n; i++ {
			id := string(records[i].Position)
			ackCount := ackedSet[id]
			dlqCount := dlqIDs[id]

			if ackCount != 1 {
				t.Fatalf("iter %d (seed %d): record %q acked %d times, want exactly 1 (dlq=%d, outcome=%v)",
					iter, seed, id, ackCount, dlqCount, outcomes[i])
			}
			if wantDLQd[id] && dlqCount != 1 {
				t.Fatalf("iter %d (seed %d): record %q expected exactly 1 DLQ write, got %d (outcome=%v)",
					iter, seed, id, dlqCount, outcomes[i])
			}
			if wantAcked[id] && dlqCount != 0 {
				t.Fatalf("iter %d (seed %d): record %q expected 0 DLQ writes (clean ack), got %d (outcome=%v)",
					iter, seed, id, dlqCount, outcomes[i])
			}
		}
		is := is.New(t)
		is.Equal(len(acked), n) // no extras, no gaps, no double-acks
		is.Equal(len(dlqDest.written), len(wantDLQd))
	}
}
