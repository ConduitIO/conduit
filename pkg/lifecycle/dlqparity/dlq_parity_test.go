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

// stubDLQHandler is a v1 stream.DLQHandler that always accepts writes. The
// point of this test is the window/threshold arithmetic, not DLQ I/O, so the
// handler itself is trivial.
type stubDLQHandler struct{}

func (stubDLQHandler) Open(context.Context) error                  { return nil }
func (stubDLQHandler) Write(context.Context, opencdc.Record) error { return nil }
func (stubDLQHandler) Close(context.Context) error                 { return nil }

// runningV1Node starts a stream.DLQHandlerNode configured with the given
// window and returns it along with a stop function. Ack/Nack block (via
// csync.ValueWatcher.Watch) until the node's Run goroutine has flipped its
// state to "running", so no extra synchronization is needed after starting
// the goroutine.
func runningV1Node(t *testing.T, windowSize, windowNackThreshold int) (*stream.DLQHandlerNode, func()) {
	t.Helper()
	is := is.New(t)

	n := &stream.DLQHandlerNode{
		Name:                "dlq-parity-v1",
		Handler:             stubDLQHandler{},
		WindowSize:          windowSize,
		WindowNackThreshold: windowNackThreshold,
		Timer:               noop.Timer{},
		Histogram:           metrics.NewRecordBytesHistogram(noop.Histogram{}),
	}
	n.Add(1) // kept alive until stop() calls Done

	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := n.Run(ctx)
		is.NoErr(err)
	}()

	stop := func() {
		n.Done()
		<-done
	}
	return n, stop
}

// replayV1 feeds events (true = nack, false = ack) one message at a time
// through a v1 DLQHandlerNode and records, per event, whether it was
// accepted (true) or rejected because the nack threshold was exceeded
// (false). Acks are always "accepted" - Message.Ack has no failure mode.
func replayV1(t *testing.T, windowSize, windowNackThreshold int, events []bool) []bool {
	t.Helper()

	n, stop := runningV1Node(t, windowSize, windowNackThreshold)
	defer stop()

	results := make([]bool, len(events))
	for i, nack := range events {
		msg := &stream.Message{
			Ctx:    context.Background(),
			Record: opencdc.Record{Position: opencdc.Position(fmt.Sprintf("pos-%d", i))},
		}
		if !nack {
			n.Ack(msg)
			results[i] = true
			continue
		}
		err := n.Nack(msg, stream.NackMetadata{Reason: cerrors.New("boom"), NodeID: "dlq-parity"})
		results[i] = err == nil
	}
	return results
}

// -- v2 (pkg/lifecycle-poc/funnel.DLQ) test harness ----------------------------

// stubDestination is a funnel.Destination that always accepts writes and acks
// everything it was just given, in order. Like stubDLQHandler, it exists so
// the test isolates window/threshold arithmetic from DLQ I/O.
type stubDestination struct {
	pending []opencdc.Position
}

func (d *stubDestination) ID() string                     { return "dlq-parity-dest" }
func (d *stubDestination) Open(context.Context) error     { return nil }
func (d *stubDestination) Teardown(context.Context) error { return nil }
func (d *stubDestination) Errors() <-chan error           { return nil }

func (d *stubDestination) Write(_ context.Context, recs []opencdc.Record) error {
	for _, r := range recs {
		d.pending = append(d.pending, r.Position)
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

func newV2DLQ(t *testing.T, windowSize, windowNackThreshold int) *funnel.DLQ {
	t.Helper()
	return funnel.NewDLQ(
		"dlq-parity-v2",
		&stubDestination{},
		log.Test(t),
		funnel.NoOpConnectorMetrics{},
		windowSize,
		windowNackThreshold,
	)
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
func replayV2(t *testing.T, windowSize, windowNackThreshold int, events []bool, split chunker) []bool {
	t.Helper()
	is := is.New(t)

	dlq := newV2DLQ(t, windowSize, windowNackThreshold)
	ctx := context.Background()

	results := make([]bool, len(events))
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
					results[pos+k] = true
				}
			} else {
				errs := make([]error, c)
				for k := range errs {
					errs[k] = cerrors.New("boom")
				}
				batch.Nack(0, errs...)

				accepted, err := dlq.Nack(ctx, batch, "dlq-parity")
				is.True(accepted >= 0 && accepted <= c)
				if err != nil {
					// accepted..c-1 were rejected by the window (or, if
					// accepted==0 and threshold==0, all of them were -
					// either way err must be non-nil for exactly the
					// rejected tail.
					is.True(accepted < c)
				}
				for k := 0; k < c; k++ {
					results[pos+k] = k < accepted
				}
			}
			pos += c
		}
		i = j
	}
	return results
}

// -- differential assertions --------------------------------------------------

// assertParity replays the same event sequence through v1 and v2 (v2 batched
// per split) and fails loudly, with the diverging index, if they disagree
// about which messages were accepted.
func assertParity(t *testing.T, windowSize, windowNackThreshold int, events []bool, split chunker) {
	t.Helper()
	is := is.New(t)

	v1 := replayV1(t, windowSize, windowNackThreshold, events)
	v2 := replayV2(t, windowSize, windowNackThreshold, events, split)

	is.Equal(len(v1), len(v2))
	for i := range events {
		if v1[i] != v2[i] {
			t.Fatalf(
				"DLQ v1/v2 parity DIVERGED at message %d (windowSize=%d windowNackThreshold=%d, nack=%v): "+
					"v1 accepted=%v v2 accepted=%v\nfull v1=%v\nfull v2=%v",
				i, windowSize, windowNackThreshold, events[i], v1[i], v2[i], v1, v2,
			)
		}
	}
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
