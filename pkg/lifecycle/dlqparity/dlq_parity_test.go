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

// Package dlqparity is a pre-flip gate for the arch-v2 default flip
// (Preview.PipelineArchV2, targeted for v0.20).
//
// pipeline.DLQ.WindowSize and pipeline.DLQ.WindowNackThreshold are persisted
// pipeline config. Architecture v1 (pkg/lifecycle/stream.DLQHandlerNode) feeds
// them into a window that counts one message at a time. Architecture v2
// (pkg/lifecycle-poc/funnel.DLQ) feeds the exact same two numbers into a
// window that counts a whole destination-write batch at once
// (funnel/dlq.go calls d.window.Ack(len(batch.records)) /
// d.window.Nack(len(batch.records))). Nothing in the codebase asserts these
// two are actually equivalent - it's true "by construction", i.e. by two
// people copy-pasting the same ring-buffer logic into two packages and hoping
// batching it doesn't change when the threshold trips.
//
// If it did diverge, a pipeline that tolerated its error rate on v1 would
// either start hard-failing after the v0.20 upgrade, or start DLQ-ing
// records v1 would have halted on - and the second failure mode is
// invariant-3-adjacent (a policy a user relied on silently loosens) and is
// triggered purely by upgrading, with nothing in either package's own test
// suite positioned to catch it (neither pkg/lifecycle/stream nor
// pkg/lifecycle-poc/funnel has a test that looks at the other).
//
// This test drives both DLQHandlerNode (v1) and DLQ (v2) with the identical
// logical sequence of per-message acks/nacks, for the identical
// WindowSize/WindowNackThreshold config, and asserts the two freeze
// (threshold-exceeded) at the exact same message. For v2 the sequence is
// grouped into batches the way the real pipeline would produce them -
// maximal runs of the same outcome for the scripted cases, and randomly
// sub-chunked runs for the property-based case - since the whole point is to
// prove that batching does not change the answer.
package dlqparity

import (
	"context"
	"fmt"
	"math/rand"
	"slices"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/lifecycle-poc/funnel"
	"github.com/conduitio/conduit/pkg/lifecycle/stream"
	"github.com/matryer/is"
)

// -- v1 (pkg/lifecycle/stream.DLQHandlerNode) test harness --------------------

// stubDLQHandler is a v1 stream.DLQHandler that always accepts writes and
// records which record positions actually reached the DLQ.
//
// It records rather than discarding because "the two windows agree on the
// COUNT" is not the property this gate needs to hold. An engine that accepts a
// nack and then never writes the record anywhere still passes a count-only
// comparison — and in v2, Worker.Nack acks that position upstream on the
// strength of the accepted count, so the record is gone for good. Comparing the
// delivered positions is what makes the gate cover invariant 3.
type stubDLQHandler struct {
	written []string
}

func (*stubDLQHandler) Open(context.Context) error  { return nil }
func (*stubDLQHandler) Close(context.Context) error { return nil }

func (h *stubDLQHandler) Write(_ context.Context, r opencdc.Record) error {
	h.written = append(h.written, wrappedPosition(r))
	return nil
}

// wrappedPosition digs the FAILED record's position out of a DLQ record.
//
// Both engines nest the original in Payload.After via opencdc.Record.Map(),
// but they disagree on the wrapper's own Position: v1 uses msg.ID()
// ("<connectorID>/<position>", stream/dlq.go), v2 uses the raw record position
// (funnel/dlq.go). Comparing the wrapper positions would therefore fail on a
// formatting difference and say nothing about which records were delivered,
// so read through to the original. (The wrapper-format divergence is real and
// worth its own decision, but it is not what this gate is measuring.)
func wrappedPosition(r opencdc.Record) string {
	after, ok := r.Payload.After.(opencdc.StructuredData)
	if !ok {
		return fmt.Sprintf("UNEXPECTED-PAYLOAD(%T)", r.Payload.After)
	}
	pos, ok := after["position"].([]byte)
	if !ok {
		return fmt.Sprintf("UNEXPECTED-POSITION(%T)", after["position"])
	}
	return string(pos)
}

// runningV1Node starts a stream.DLQHandlerNode configured with the given
// window and returns it along with a stop function. Ack/Nack block (via
// csync.ValueWatcher.Watch) until the node's Run goroutine has flipped its
// state to "running", so no extra synchronization is needed after starting
// the goroutine.
func runningV1Node(t *testing.T, windowSize, windowNackThreshold int) (*stream.DLQHandlerNode, *stubDLQHandler, func()) {
	t.Helper()
	is := is.New(t)

	h := &stubDLQHandler{}
	n := &stream.DLQHandlerNode{
		Name:                "dlq-parity-v1",
		Handler:             h,
		WindowSize:          windowSize,
		WindowNackThreshold: windowNackThreshold,
		Timer:               noop.Timer{},
		Histogram:           metrics.NewRecordBytesHistogram(noop.Histogram{}),
	}
	n.Add(1) // kept alive until stop() calls Done

	ctx := context.Background()
	done := make(chan struct{})
	// Captured, not asserted here: is.NoErr calls t.FailNow, which must run on
	// the test goroutine. Assert it in stop() instead.
	var runErr error
	go func() {
		defer close(done)
		runErr = n.Run(ctx)
	}()

	stop := func() {
		n.Done()
		<-done
		is.NoErr(runErr)
	}
	return n, h, stop
}

// outcome is what the two engines must agree on, per message.
//
// fatal is compared as well as accepted because fatality is not a detail of
// error formatting - cerrors.IsFatalError is what decides whether a pipeline
// auto-recovers or goes StatusDegraded (pkg/lifecycle/service.go and
// pkg/lifecycle-poc/service.go both branch on it). "A pipeline that tolerated
// its error rate on v1 starts hard-failing after the upgrade" IS a fatality
// divergence, so a gate that compares only accepted/rejected cannot see the
// regression it exists to catch.
type outcome struct {
	accepted bool
	fatal    bool
}

// replayV1 feeds events (true = nack, false = ack) one message at a time
// through a v1 DLQHandlerNode and records, per event, whether it was
// accepted and whether the rejection was fatal. Acks are always accepted and
// never fatal - Message.Ack has no failure mode. It also returns the record
// positions that actually reached the DLQ handler.
func replayV1(t *testing.T, windowSize, windowNackThreshold int, events []bool) ([]outcome, []string) {
	t.Helper()

	n, handler, stop := runningV1Node(t, windowSize, windowNackThreshold)
	defer stop()

	results := make([]outcome, len(events))
	for i, nack := range events {
		msg := &stream.Message{
			Ctx:    context.Background(),
			Record: opencdc.Record{Position: opencdc.Position(fmt.Sprintf("pos-%d", i))},
		}
		if !nack {
			n.Ack(msg)
			results[i] = outcome{accepted: true}
			continue
		}
		err := n.Nack(msg, stream.NackMetadata{Reason: cerrors.New("boom"), NodeID: "dlq-parity"})
		results[i] = outcome{accepted: err == nil, fatal: cerrors.IsFatalError(err)}
	}
	return results, handler.written
}

// -- v2 (pkg/lifecycle-poc/funnel.DLQ) test harness ----------------------------

// stubDestination is a funnel.Destination that always accepts writes and acks
// everything it was just given, in order. Like stubDLQHandler, it exists so
// the test isolates window/threshold arithmetic from DLQ I/O.
type stubDestination struct {
	pending []opencdc.Position
	// written accumulates every position ever handed to Write, and is never
	// drained - pending is transient bookkeeping for Ack, and asserting on it
	// would only ever observe an empty slice. See stubDLQHandler for why the
	// delivered set, not just the accepted count, is the property under test.
	written []string
}

func (d *stubDestination) ID() string                     { return "dlq-parity-dest" }
func (d *stubDestination) Open(context.Context) error     { return nil }
func (d *stubDestination) Teardown(context.Context) error { return nil }
func (d *stubDestination) Errors() <-chan error           { return nil }

func (d *stubDestination) Write(_ context.Context, recs []opencdc.Record) error {
	for _, r := range recs {
		d.pending = append(d.pending, r.Position)
		d.written = append(d.written, wrappedPosition(r))
	}
	return nil
}

func (d *stubDestination) Ack(context.Context) ([]connector.DestinationAck, error) {
	acks := make([]connector.DestinationAck, len(d.pending))
	for i, p := range d.pending {
		acks[i] = connector.DestinationAck{Position: p}
	}
	d.pending = nil
	return acks, nil
}

func newV2DLQ(t *testing.T, windowSize, windowNackThreshold int) (*funnel.DLQ, *stubDestination) {
	t.Helper()
	dest := &stubDestination{}
	return funnel.NewDLQ(
		"dlq-parity-v2",
		dest,
		log.Test(t),
		funnel.NoOpConnectorMetrics{},
		windowSize,
		windowNackThreshold,
	), dest
}

// chunker splits a run of runLen homogeneous events (all acks, or all nacks)
// into one or more v2 batch sizes summing to runLen.
type chunker func(runLen int) []int

// maximalChunks puts a whole homogeneous run into a single v2 batch call -
// the largest, and therefore most adversarial, batch shape for exercising
// the mid-batch threshold-exceeded path in dlqWindow.store.
func maximalChunks(runLen int) []int { return []int{runLen} }

// randomChunks splits a homogeneous run into randomly sized sub-batches,
// modelling the fact that real destination flush batches can land in
// arbitrary sizes.
func randomChunks(rng *rand.Rand) chunker {
	return func(runLen int) []int {
		var chunks []int
		remaining := runLen
		for remaining > 0 {
			c := rng.Intn(remaining) + 1
			chunks = append(chunks, c)
			remaining -= c
		}
		return chunks
	}
}

// replayV2 feeds events (true = nack, false = ack) through a v2 DLQ, grouping
// each maximal run of identical outcomes into one or more batches per split.
// It records, per original message, whether it was accepted (true) or
// rejected (false), the same shape replayV1 returns, so the two can be
// compared directly.
func replayV2(t *testing.T, windowSize, windowNackThreshold int, events []bool, split chunker) ([]outcome, []string) {
	t.Helper()
	is := is.New(t)

	dlq, dest := newV2DLQ(t, windowSize, windowNackThreshold)
	ctx := context.Background()

	results := make([]outcome, len(events))
	i := 0
	for i < len(events) {
		nack := events[i]
		j := i
		for j < len(events) && events[j] == nack {
			j++
		}
		runLen := j - i

		pos := i
		for _, c := range split(runLen) {
			recs := make([]opencdc.Record, c)
			for k := range c {
				recs[k] = opencdc.Record{Position: opencdc.Position(fmt.Sprintf("pos-%d", pos+k))}
			}
			batch := funnel.NewBatch(recs)

			if !nack {
				dlq.Ack(ctx, batch)
				for k := 0; k < c; k++ {
					results[pos+k] = outcome{accepted: true}
				}
			} else {
				errs := make([]error, c)
				for k := range errs {
					errs[k] = cerrors.New("boom")
				}
				batch.Nack(0, errs...)

				accepted, err := dlq.Nack(ctx, batch, "dlq-parity")

				// The contract that actually constrains the caller: a partial
				// acceptance MUST be reported as an error, because Worker.Nack
				// acks exactly positions[:accepted] upstream and the rejected
				// tail has to stop the pipeline rather than vanish. (The old
				// assertions here - accepted in [0,c], and accepted < c when
				// err != nil - were tautologies over dlqWindow.store's return
				// domain and a stub that never fails.)
				if accepted < c {
					is.True(err != nil)
				}

				fatal := cerrors.IsFatalError(err)
				for k := 0; k < c; k++ {
					results[pos+k] = outcome{accepted: k < accepted, fatal: k >= accepted && fatal}
				}
			}
			pos += c
		}
		i = j
	}
	return results, dest.written
}

// -- differential assertions --------------------------------------------------

// assertParity replays the same event sequence through v1 and v2 (v2 batched
// per split) and fails loudly, with the diverging index, if they disagree
// about which messages were accepted.
func assertParity(t *testing.T, windowSize, windowNackThreshold int, events []bool, split chunker) {
	t.Helper()
	is := is.New(t)

	v1, v1DLQ := replayV1(t, windowSize, windowNackThreshold, events)
	v2, v2DLQ := replayV2(t, windowSize, windowNackThreshold, events, split)

	is.Equal(len(v1), len(v2))
	for i := range events {
		if v1[i] != v2[i] {
			t.Fatalf(
				"DLQ v1/v2 parity DIVERGED at message %d (windowSize=%d windowNackThreshold=%d, nack=%v): "+
					"v1 %+v v2 %+v\nfull v1=%v\nfull v2=%v",
				i, windowSize, windowNackThreshold, events[i], v1[i], v2[i], v1, v2,
			)
		}
	}

	// Agreeing on which nacks were ACCEPTED is not enough. An engine that
	// accepts a nack and then writes the record nowhere satisfies every
	// assertion above, and v2 would still ack that position upstream on the
	// strength of the accepted count (Worker.Nack) - a silently lost record,
	// invariant 3, with the gate green. Compare what each engine actually
	// delivered.
	//
	// Sorted, not set-compared: a duplicate delivery is also a divergence
	// worth failing on, and v1 delivers strictly one record per Nack call
	// while v2 delivers a whole batch per call.
	v1Sorted := slices.Clone(v1DLQ)
	v2Sorted := slices.Clone(v2DLQ)
	slices.Sort(v1Sorted)
	slices.Sort(v2Sorted)
	if !slices.Equal(v1Sorted, v2Sorted) {
		t.Fatalf(
			"DLQ v1/v2 DELIVERY DIVERGED (windowSize=%d windowNackThreshold=%d): "+
				"the engines agreed on which nacks were accepted but not on which records reached the DLQ\n"+
				"v1 delivered %d: %v\nv2 delivered %d: %v",
			windowSize, windowNackThreshold, len(v1Sorted), v1Sorted, len(v2Sorted), v2Sorted,
		)
	}

	// Guard the guard: if neither engine ever wrote to the DLQ, the comparison
	// above is vacuously true. Any scenario with an accepted nack must deliver.
	acceptedNacks := 0
	for i, nack := range events {
		if nack && v1[i].accepted {
			acceptedNacks++
		}
	}
	is.Equal(len(v1Sorted), acceptedNacks)
}

// scriptedEvents reproduces the exact scenario from
// stream.TestDLQWindow_NackThresholdExceeded: fill the tolerance with nacks,
// dilute it back to all-acks, fill the tolerance again, push one more nack
// over the threshold, then prove the freeze is permanent (acks after that
// don't un-freeze it, and a further nack still fails).
func scriptedEvents(windowSize, windowNackThreshold int) []bool {
	var events []bool
	for range windowNackThreshold {
		events = append(events, true) // nacks up to the threshold: must be accepted
	}
	for range windowSize {
		events = append(events, false) // acks fill the window back up
	}
	for range windowNackThreshold {
		events = append(events, true) // nacks up to the threshold again: accepted
	}
	events = append(events, true) // one more nack: pushes over the threshold
	for range windowSize {
		events = append(events, false) // acks after the freeze: must not un-freeze it
	}
	events = append(events, true) // still frozen: must still be rejected
	return events
}

func TestDLQParity_Scripted(t *testing.T) {
	// Same (windowSize, windowNackThreshold) pairs as
	// stream.TestDLQWindow_NackThresholdExceeded, so this test is directly
	// comparable to the v1-only coverage that already exists.
	testCases := []struct {
		windowSize    int
		nackThreshold int
	}{
		{1, 0},
		{2, 0},
		{2, 1},
		{100, 0},
		{100, 99},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("w%d-t%d", tc.windowSize, tc.nackThreshold), func(t *testing.T) {
			events := scriptedEvents(tc.windowSize, tc.nackThreshold)
			assertParity(t, tc.windowSize, tc.nackThreshold, events, maximalChunks)
		})
	}
}

// TestDLQParity_WindowDisabled mirrors stream.TestDLQWindow_WindowDisabled:
// WindowSize=0 is a distinct sentinel (not "threshold 0"), meaning nacks are
// never rejected by the window at all.
func TestDLQParity_WindowDisabled(t *testing.T) {
	events := []bool{false, true, true, false, true}
	assertParity(t, 0, 0, events, maximalChunks)
}

// TestDLQParity_RandomizedBatching stresses the actual concern in the task:
// that grouping consecutive same-outcome messages into arbitrarily sized v2
// batches (rather than v1's strictly one-at-a-time processing) must not
// change which message trips the threshold. Every sub-test uses a fixed seed
// so a failure is reproducible.
func TestDLQParity_RandomizedBatching(t *testing.T) {
	configs := []struct {
		windowSize    int
		nackThreshold int
		nackProb      float64
	}{
		{1, 0, 0.5},
		{5, 2, 0.4},
		{10, 0, 0.2},
		{20, 10, 0.5},
		{50, 25, 0.6},
		{100, 1, 0.3},
	}

	for _, cfg := range configs {
		t.Run(fmt.Sprintf("w%d-t%d-p%.1f", cfg.windowSize, cfg.nackThreshold, cfg.nackProb), func(t *testing.T) {
			for seed := int64(0); seed < 5; seed++ {
				t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
					eventRng := rand.New(rand.NewSource(seed))
					events := make([]bool, 300)
					for i := range events {
						events[i] = eventRng.Float64() < cfg.nackProb
					}

					// Use a fresh, independently-seeded rng for batch
					// splitting so the split shape isn't correlated with
					// the event sequence itself.
					splitRng := rand.New(rand.NewSource(seed + 1000))
					assertParity(t, cfg.windowSize, cfg.nackThreshold, events, randomChunks(splitRng))
				})
			}
		})
	}
}
