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

package engines_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/internal/enginetest"
	"github.com/conduitio/conduit/pkg/internal/enginetest/engines"
	"github.com/matryer/is"
)

// testTimeout bounds every wait in this file. Nothing here sleeps to give the
// engine time: each step waits on a signal from a fake, and the timeout only
// turns a hang (itself a finding) into a failure.
const testTimeout = 20 * time.Second

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	return ctx
}

// eachEngine runs fn as a subtest per engine.
func eachEngine(t *testing.T, fn func(t *testing.T, eng engines.Engine)) {
	t.Helper()
	for _, eng := range engines.All() {
		t.Run(eng.Name(), func(t *testing.T) { fn(t, eng) })
	}
}

// start runs the topology and guarantees the pipeline is killed and joined when
// the test ends, so a failed assertion cannot leak goroutines into the next test.
func start(t *testing.T, ctx context.Context, eng engines.Engine, topo engines.Topology) engines.Run {
	t.Helper()
	run, err := eng.Start(ctx, topo)
	if err != nil {
		t.Fatalf("start %s: %v", eng.Name(), err)
	}
	t.Cleanup(func() {
		run.Kill()
		// Bounded: a pipeline that ignores Kill is a finding, not a reason to
		// hang the whole test binary.
		wctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		_ = run.Wait(wctx)
	})
	return run
}

func positions(src string, n int) []opencdc.Position {
	out := make([]opencdc.Position, n)
	for i := range out {
		out[i] = enginetest.Position(src, i)
	}
	return out
}

// Invariant 1: never acknowledge a record upstream before it is durably handled
// downstream. Acks are withheld at the destination; the source must see none
// until they are released, and when they are, the ledger must show every acked
// record was durable first.
func TestInvariant1_NoSourceAckBeforeDestinationAck(t *testing.T) {
	eachEngine(t, func(t *testing.T, eng engines.Engine) {
		is := is.New(t)
		ctx := testCtx(t)

		ledger := enginetest.NewLedger().Require("dst").DLQ("dlq")
		src := enginetest.NewSource(enginetest.SourceConfig{ID: "src", Total: 10, Ledger: ledger})
		dst := enginetest.NewDestination(enginetest.DestinationConfig{ID: "dst", Ledger: ledger})
		dst.AckGate.Close()

		run := start(t, ctx, eng, engines.Topology{
			Sources:      []*enginetest.Source{src},
			Destinations: []*enginetest.Destination{dst},
		})

		// The destination has the record (it is durable there) but the engine
		// has not been told so: the source must not be acked.
		is.NoErr(dst.WaitWritten(ctx, 1))
		is.NoErr(dst.WaitDelivered(ctx, 1))
		is.Equal(src.AckedCount(), 0)

		dst.AckGate.Open()
		is.NoErr(src.WaitAcked(ctx, 10))
		is.NoErr(run.Stop(ctx))
		is.NoErr(run.Wait(ctx))

		is.Equal(ledger.Violations(), []string(nil))
		is.Equal(src.Acked(), positions("src", 10))
	})
}

// Invariant 1 and 3 on the error path: a failed destination write must leave
// the record unacknowledged, and the run must fail rather than carry on.
func TestInvariant1_FailedWriteIsNeverAcked(t *testing.T) {
	eachEngine(t, func(t *testing.T, eng engines.Engine) {
		is := is.New(t)
		ctx := testCtx(t)

		errWrite := cerrors.New("destination unavailable")
		ledger := enginetest.NewLedger().Require("dst").DLQ("dlq")
		src := enginetest.NewSource(enginetest.SourceConfig{ID: "src", Total: 10, Ledger: ledger})
		dst := enginetest.NewDestination(enginetest.DestinationConfig{
			ID:     "dst",
			Ledger: ledger,
			WriteHook: func(_ context.Context, call int, _ []opencdc.Record) error {
				if call == 4 {
					return errWrite
				}
				return nil
			},
		})

		run := start(t, ctx, eng, engines.Topology{
			Sources:      []*enginetest.Source{src},
			Destinations: []*enginetest.Destination{dst},
		})

		err := run.Wait(ctx)
		is.True(err != nil)
		is.True(cerrors.Is(err, errWrite))

		// Records 0..2 were handled; record 3's write failed and must not be
		// acked, nor anything after it.
		for _, p := range src.Acked() {
			is.True(string(p) != string(enginetest.Position("src", 3)))
		}
		is.Equal(ledger.Violations(), []string(nil))
		delivered := map[string]bool{}
		for _, r := range dst.Delivered() {
			delivered[string(r.Position)] = true
		}
		for _, p := range src.Acked() {
			is.True(delivered[string(p)]) // acked implies durably delivered
		}
	})
}

// Invariant 2 (per-partition ordering of acks): whatever shape the destination
// acks in, each source sees its positions acked in order, with no gaps and no
// duplicates, and the full set once the pipeline has drained.
func TestInvariant2_AcksReachEachSourceInOrder(t *testing.T) {
	shapes := map[string]enginetest.DestinationConfig{
		"immediate":       {},
		"one-ack-a-call":  {AckChunk: 1},
		"buffered-pump":   {AckPump: true, AckBuffer: 8},
		"sdk-batch-of-5":  {Batching: enginetest.Batching{Size: 5, Delay: 5 * time.Millisecond}},
		"batch-with-pump": {Batching: enginetest.Batching{Size: 4, Delay: 5 * time.Millisecond}, AckPump: true},
	}
	for name, dcfg := range shapes {
		t.Run(name, func(t *testing.T) {
			eachEngine(t, func(t *testing.T, eng engines.Engine) {
				is := is.New(t)
				ctx := testCtx(t)

				const total = 40
				srcs := []*enginetest.Source{
					enginetest.NewSource(enginetest.SourceConfig{ID: "a", Total: total, BatchSize: 5}),
					enginetest.NewSource(enginetest.SourceConfig{ID: "b", Total: total, BatchSize: 5}),
				}
				var dsts []*enginetest.Destination
				for i := range 2 {
					c := dcfg
					c.ID = fmt.Sprintf("dst%d", i)
					dsts = append(dsts, enginetest.NewDestination(c))
				}

				run := start(t, ctx, eng, engines.Topology{Sources: srcs, Destinations: dsts})

				for _, s := range srcs {
					is.NoErr(s.WaitAcked(ctx, total))
				}
				is.NoErr(run.Stop(ctx))
				is.NoErr(run.Wait(ctx))

				for _, s := range srcs {
					is.Equal(s.Acked(), positions(s.ID(), total))
				}
				for _, d := range dsts {
					delivered := d.Delivered()
					is.Equal(len(delivered), 2*total) // every record reaches every destination
					// Ordering guarantee: a destination sees each source's
					// records in the order that source produced them. Sources
					// may interleave with each other.
					for _, s := range srcs {
						var got []opencdc.Position
						for _, r := range delivered {
							if sid, _ := r.Metadata.GetConduitSourceConnectorID(); sid == s.ID() {
								got = append(got, r.Position)
							}
						}
						is.Equal(got, positions(s.ID(), total))
					}
				}
			})
		})
	}
}

// Invariant 3: at-least-once is the floor. A record the destination rejects
// goes to the DLQ instead of vanishing; every record produced is either
// delivered or in the DLQ, and every one is acked.
func TestInvariant3_NackedRecordsReachTheDLQ(t *testing.T) {
	eachEngine(t, func(t *testing.T, eng engines.Engine) {
		is := is.New(t)
		ctx := testCtx(t)

		const total = 30
		errBad := cerrors.New("bad record")
		ledger := enginetest.NewLedger().Require("dst").DLQ("dlq")
		src := enginetest.NewSource(enginetest.SourceConfig{ID: "src", Total: total, BatchSize: 3, Ledger: ledger})
		dst := enginetest.NewDestination(enginetest.DestinationConfig{
			ID:     "dst",
			Ledger: ledger,
			Verdict: func(r opencdc.Record) error {
				if string(r.Key.Bytes())[len(r.Key.Bytes())-1] == '7' { // keys 7, 17, 27
					return errBad
				}
				return nil
			},
		})
		dlq := enginetest.NewDestination(enginetest.DestinationConfig{ID: "dlq", Ledger: ledger})

		run := start(t, ctx, eng, engines.Topology{
			Sources:      []*enginetest.Source{src},
			Destinations: []*enginetest.Destination{dst},
			DLQ:          engines.SingleDLQ(dlq),
			DLQWindow:    100, DLQThreshold: 100,
		})

		is.NoErr(src.WaitAcked(ctx, total))
		is.NoErr(run.Stop(ctx))
		is.NoErr(run.Wait(ctx))

		got := map[string]string{}
		for _, r := range dst.Delivered() {
			got[string(r.Position)] = "dst"
		}
		for _, r := range dlq.Delivered() {
			is.Equal(got[string(r.Position)], "") // not in both
			got[string(r.Position)] = "dlq"
		}
		is.Equal(len(got), total) // nothing dropped
		is.Equal(len(dlq.Delivered()), 3)
		is.Equal(ledger.Violations(), []string(nil))
	})
}

// Invariant 3 with the DLQ disabled: a nack halts the pipeline, and the
// rejected record is not acknowledged.
func TestInvariant3_NackWithoutDLQHaltsAndIsNotAcked(t *testing.T) {
	eachEngine(t, func(t *testing.T, eng engines.Engine) {
		is := is.New(t)
		ctx := testCtx(t)

		errBad := cerrors.New("bad record")
		src := enginetest.NewSource(enginetest.SourceConfig{ID: "src", Total: 10})
		dst := enginetest.NewDestination(enginetest.DestinationConfig{
			ID: "dst",
			Verdict: func(r opencdc.Record) error {
				if string(r.Position) == string(enginetest.Position("src", 4)) {
					return errBad
				}
				return nil
			},
		})

		run := start(t, ctx, eng, engines.Topology{
			Sources:      []*enginetest.Source{src},
			Destinations: []*enginetest.Destination{dst},
		})

		is.True(run.Wait(ctx) != nil)
		for _, p := range src.Acked() {
			is.True(string(p) != string(enginetest.Position("src", 4)))
		}
	})
}

// Invariant 3 on a plugin crash: when the destination dies mid-run the run
// fails, and no record is acked that the destination had not made durable.
func TestInvariant3_DestinationCrashFailsTheRunWithoutLoss(t *testing.T) {
	eachEngine(t, func(t *testing.T, eng engines.Engine) {
		is := is.New(t)
		ctx := testCtx(t)

		ledger := enginetest.NewLedger().Require("dst").DLQ("dlq")
		src := enginetest.NewSource(enginetest.SourceConfig{ID: "src", Ledger: ledger})
		dst := enginetest.NewDestination(enginetest.DestinationConfig{ID: "dst", Ledger: ledger})

		run := start(t, ctx, eng, engines.Topology{
			Sources:      []*enginetest.Source{src},
			Destinations: []*enginetest.Destination{dst},
		})

		is.NoErr(src.WaitAcked(ctx, 5))
		dst.Crash(nil)

		err := run.Wait(ctx)
		is.True(err != nil)
		is.Equal(ledger.Violations(), []string(nil))
		delivered := map[string]bool{}
		for _, r := range dst.Delivered() {
			delivered[string(r.Position)] = true
		}
		for _, p := range src.Acked() {
			is.True(delivered[string(p)])
		}
	})
}

// Invariant 7: a graceful stop drains. With acks in flight, Stop must let them
// complete: every record the destination durably handled is acked to the source,
// and every connector is torn down.
func TestInvariant7_StopDrainsInFlightRecords(t *testing.T) {
	eachEngine(t, func(t *testing.T, eng engines.Engine) {
		is := is.New(t)
		ctx := testCtx(t)

		ledger := enginetest.NewLedger().Require("dst").DLQ("dlq")
		src := enginetest.NewSource(enginetest.SourceConfig{ID: "src", Ledger: ledger})
		dst := enginetest.NewDestination(enginetest.DestinationConfig{ID: "dst", Ledger: ledger})
		dst.AckGate.Close()

		run := start(t, ctx, eng, engines.Topology{
			Sources:      []*enginetest.Source{src},
			Destinations: []*enginetest.Destination{dst},
		})

		is.NoErr(dst.WaitDelivered(ctx, 1)) // something is in flight

		stopErr := make(chan error, 1)
		go func() { stopErr <- run.Stop(ctx) }()
		dst.AckGate.Open()

		is.NoErr(<-stopErr)
		is.NoErr(run.Wait(ctx))

		is.Equal(ledger.Violations(), []string(nil))
		is.Equal(len(src.Acked()), len(dst.Delivered())) // drained: nothing durable left unacked
		is.True(src.TornDown())
		is.True(dst.TornDown())
	})
}

// Invariant 7 (stop time): a source parked inside Read must not make Stop hang.
// The Read is parked by a hook, Stop is requested, and the pipeline must exit
// cleanly having acked everything it had read before the park.
func TestInvariant7_StopWhileSourceReadIsStalled(t *testing.T) {
	eachEngine(t, func(t *testing.T, eng engines.Engine) {
		is := is.New(t)
		ctx := testCtx(t)

		const before = 5
		parked := make(chan struct{})
		src := enginetest.NewSource(enginetest.SourceConfig{
			ID: "src",
			ReadHook: func(ctx context.Context, call int) error {
				if call == before+1 {
					close(parked)
					<-ctx.Done() // stalled until teardown or cancel
					return ctx.Err()
				}
				return nil
			},
		})
		dst := enginetest.NewDestination(enginetest.DestinationConfig{ID: "dst"})

		run := start(t, ctx, eng, engines.Topology{
			Sources:      []*enginetest.Source{src},
			Destinations: []*enginetest.Destination{dst},
		})

		select {
		case <-parked:
		case <-ctx.Done():
			t.Fatal("source never reached the stalled read")
		}
		is.NoErr(run.Stop(ctx))
		is.NoErr(run.Wait(ctx))

		is.Equal(src.Acked(), positions("src", before))
		is.True(src.TornDown())
	})
}

// Not a data-integrity invariant but a liveness one the kit exposed: an SDK
// destination that batches (size 100, no flush delay) behind one-record reads.
// The SDK holds the partial batch until it fills, a timer fires, or the
// destination is stopped. v1 keeps writing, so the batch fills. arch-v2 waits
// for the first write's ack before sending the next write, so the ack never
// comes and the pipeline makes no progress. Nothing is lost (invariants 1 to 3
// hold, nothing is acked) but nothing is delivered either.
//
// arch-v2 refuses this configuration at start (#2979) and #2990 tracks the
// non-SDK destinations the refusal cannot see. The v2 case stays skipped until
// the single-engine work (#2971) removes the stall; the skip is the pin, and
// the assertion below is what it must satisfy when it is lifted.
func TestLiveness_DestinationBatchingBehindSingleRecordReads(t *testing.T) {
	eachEngine(t, func(t *testing.T, eng engines.Engine) {
		if eng.Name() == "v2" {
			t.Skip("arch-v2 stalls on destination batching behind small reads: #2979, #2990, design #2971")
		}
		is := is.New(t)
		ctx := testCtx(t)

		src := enginetest.NewSource(enginetest.SourceConfig{ID: "src", Total: 300})
		dst := enginetest.NewDestination(enginetest.DestinationConfig{
			ID: "dst", Batching: enginetest.Batching{Size: 100},
		})
		run := start(t, ctx, eng, engines.Topology{
			Sources:      []*enginetest.Source{src},
			Destinations: []*enginetest.Destination{dst},
		})

		is.NoErr(src.WaitAcked(ctx, 300))
		is.NoErr(run.Stop(ctx))
		is.NoErr(run.Wait(ctx))
	})
}

// Invariant 7 (stop time), the part the engines do NOT bound: a graceful stop
// waits for the destination's acks, so a destination that withholds them holds
// the pipeline open, on both engines (#2990 for arch-v2's SIGTERM hang). What
// bounds shutdown in production is the runtime's exit timeout followed by a
// force stop. This test pins the force-stop half: cancelling the pipeline
// context releases a pipeline wedged on withheld acks, tears every connector
// down, and acks nothing that was not acked downstream.
func TestInvariant7_ForceStopReleasesAPipelineWedgedOnAcks(t *testing.T) {
	eachEngine(t, func(t *testing.T, eng engines.Engine) {
		is := is.New(t)
		ctx := testCtx(t)

		ledger := enginetest.NewLedger().Require("dst").DLQ("dlq")
		src := enginetest.NewSource(enginetest.SourceConfig{ID: "src", Discard: true, Ledger: ledger})
		dst := enginetest.NewDestination(enginetest.DestinationConfig{ID: "dst", Discard: true, Ledger: ledger})
		dst.AckGate.Close()

		run := start(t, ctx, eng, engines.Topology{
			Sources:      []*enginetest.Source{src},
			Destinations: []*enginetest.Destination{dst},
		})
		is.NoErr(dst.WaitWritten(ctx, 1)) // wedged: written, never acked

		run.Kill()
		is.True(run.Wait(ctx) != nil) // ended by cancellation, not a clean stop
		is.True(src.TornDown())
		is.True(dst.TornDown())
		is.Equal(src.AckedCount(), 0) // nothing acked upstream without a downstream ack
		is.Equal(ledger.Violations(), []string(nil))
	})
}

// Memory bound, the part that is deterministic to assert: with the destination
// withholding every ack, how many records does the engine pull from the source?
//
// arch-v2 reads one batch and then waits for its acks, so exactly one batch is
// in flight: Read is called once and cannot be called again until the batch is
// acked. v1 has no engine-side bound at all: the source node keeps reading
// while the destination acker queues messages without limit, so the only bound
// is back-pressure at the plugin boundary (gRPC flow control), tracked in #211.
// The v1 case is skipped with that link rather than pinned to a number, because
// the number it reaches is whatever the machine manages before the test stops.
func TestMemoryBound_RecordsInFlightWithWithheldAcks(t *testing.T) {
	const batch = 50
	eachEngine(t, func(t *testing.T, eng engines.Engine) {
		if eng.Name() == "v1" {
			t.Skip("v1 does not bound in-flight records in the engine; back-pressure is left to the plugin boundary: #211")
		}
		is := is.New(t)
		ctx := testCtx(t)

		src := enginetest.NewSource(enginetest.SourceConfig{ID: "src", BatchSize: batch, Discard: true})
		dst := enginetest.NewDestination(enginetest.DestinationConfig{ID: "dst", Discard: true})
		dst.AckGate.Close()

		start(t, ctx, eng, engines.Topology{
			Sources:      []*enginetest.Source{src},
			Destinations: []*enginetest.Destination{dst},
		})

		is.NoErr(dst.WaitDelivered(ctx, batch)) // the first batch is written and durable
		// The engine is now blocked on that batch's acks; a second Read cannot
		// have started, so exactly one batch was pulled from the source.
		is.Equal(src.ReadCalls(), 1)
		is.Equal(src.Produced(), batch)
		is.Equal(src.AckedCount(), 0)
	})
}
