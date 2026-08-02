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

// shapes_v1_test.go drives the same shapes through the v1 (classic stream)
// engine, as the baseline the v2 shapes in shapes_v2_test.go are measured
// against. v1 acks one record at a time and has no batch-accounting surface
// at all: retry (a batch-partial response) and split/fan-out
// (sdk.MultiRecord) are structurally impossible on it - see
// tasks_v1.go and pkg/lifecycle/stream/processor.go's handleProcessedRecord.
// Those shapes are represented here as an assertion that v1 refuses them
// LOUDLY, before any write, rather than silently mishandling them - the
// "upgrade parity" question for those shapes is answered by that refusal
// existing and firing correctly (see a980aa0), not by resume-correctness
// numbers.
package upgrade

import (
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/lifecycle/stream"
)

// TestV1Filter_ResumeCorrectness is v1's counterpart to
// TestV2Filter_ResumeCorrectness: a processor filters one record mid-stream;
// every other record is delivered and acked, and the persisted position
// reaches the end (v1 acks a filtered message too - see
// pkg/lifecycle/stream/processor.go's handleProcessedRecord FilterRecord
// case, which forwards the message so DestinationAckerNode / SourceAckerNode
// still see and ack it, only skipping the destination Write).
func TestV1Filter_ResumeCorrectness(t *testing.T) {
	const n = 5
	sh := newSourceHarness(t, n)

	p := newV1Pipeline(t, sh, v1Config{
		Proc: &filterAtPosProcessor{positions: map[string]bool{"3": true}},
	})
	errs := p.waitDone(shapeTimeout)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", fmtErrs(errs))
	}

	if p.dest.hasPosition(pos(3)) {
		t.Fatal("filtered record (position 3) was delivered to the destination")
	}
	for _, i := range []int{1, 2, 4, 5} {
		if !p.dest.hasPosition(pos(i)) {
			t.Fatalf("position %d (not filtered) was never delivered", i)
		}
	}
	if got := p.dest.count(); got != n-1 {
		t.Fatalf("destination delivered %d records, want %d", got, n-1)
	}

	sh.waitPersistedPosition(n, shapeTimeout)
}

// TestV1_WrongCountIsFatalNotSilentRetry documents and asserts v1's actual
// behavior for the only shape "a processor returned fewer records than it
// received" can take when every Process call is given exactly one record:
// returning zero. This is immediately FATAL (pkg/lifecycle/stream/processor.go's
// ProcessorNode.Run: `len(recsIn) != len(recsOut)`), never a silent skip or
// a v2-style retry - records before the bad one are already durably acked,
// and the persisted position never advances past them. A restart must
// redeliver exactly the record the processor choked on.
func TestV1_WrongCountIsFatalNotSilentRetry(t *testing.T) {
	const n = 4
	sh := newSourceHarness(t, n)

	p := newV1Pipeline(t, sh, v1Config{
		Proc: &wrongCountAtPosProcessor{targetPos: "2"},
		// DLQ disabled (windowSize=1, threshold=0 - see newDLQWindow/dlqWindow.store):
		// a wrong-count response is a pipeline misconfiguration, not a
		// per-record failure, so it must halt rather than "successfully"
		// route to a DLQ that happens to be configured to accept everything.
		DLQWindowSize:       1,
		DLQWindowNackThresh: 0,
	})
	errs := p.waitDone(shapeTimeout)
	if len(errs) == 0 {
		t.Fatal("expected a fatal error from the wrong-count processor response, got none")
	}

	if !p.dest.hasPosition(pos(1)) {
		t.Fatal("position 1 (before the bad record) should have been delivered normally")
	}
	for _, i := range []int{2, 3, 4} {
		if p.dest.hasPosition(pos(i)) {
			t.Fatalf("position %d (the bad record or anything after it) was delivered despite the fatal error", i)
		}
	}

	sh.waitPersistedPosition(1, shapeTimeout)

	sh2 := sh.restart(n)
	defer func() { _ = sh2.Source.Teardown(t.Context()) }()
	if err := sh2.Source.Open(t.Context()); err != nil {
		t.Fatalf("restart: open: %v", err)
	}
	recs, err := sh2.Source.Read(t.Context())
	if err != nil {
		t.Fatalf("restart: read: %v", err)
	}
	if len(recs) == 0 || string(recs[0].Position) != string(pos(2)) {
		t.Fatalf("restart redelivered %v first, want position 2", recs)
	}
}

// TestV1DLQNack_Routing_ResumeCorrectness is v1's counterpart to
// TestV2DLQNack_Routing_ResumeCorrectness: one record errors mid-stream
// under a DLQ policy that always accepts. It is routed to the DLQ and
// counted as handled, so it is acked in the source like every other record
// (see pkg/lifecycle/stream/source_acker.go's registerNackHandler: "the
// nacked record was successfully stored in the DLQ, we consider the record
// processed").
func TestV1DLQNack_Routing_ResumeCorrectness(t *testing.T) {
	const n = 5
	sh := newSourceHarness(t, n)

	p := newV1Pipeline(t, sh, v1Config{
		Proc:                &errorAtPosProcessor{positions: map[string]bool{"3": true}},
		DLQWindowSize:       0,
		DLQWindowNackThresh: 0,
	})
	errs := p.waitDone(shapeTimeout)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", fmtErrs(errs))
	}

	if p.dlq.count() != 1 {
		t.Fatalf("DLQ received %d records, want exactly 1", p.dlq.count())
	}
	if p.dest.hasPosition(pos(3)) {
		t.Fatal("nacked record (position 3) was delivered to the main destination")
	}
	for _, i := range []int{1, 2, 4, 5} {
		if !p.dest.hasPosition(pos(i)) {
			t.Fatalf("position %d (not nacked) was never delivered to the main destination", i)
		}
	}

	sh.waitPersistedPosition(n, shapeTimeout)
}

// TestV1DLQNack_Halt_PositionNeverAdvances is v1's counterpart to
// TestV2DLQNack_Halt_PositionNeverAdvances: the DLQ is disabled
// (WindowSize=1, WindowNackThreshold=0), so the nack becomes a fatal error
// (pkg/lifecycle/stream/processor.go's handleProcessedRecord always
// escalates a non-nil msg.Nack() error to a FatalError). Positions before
// the error are delivered and acked normally; the errored record and
// everything after it are never delivered anywhere, and the persisted
// position never advances past the last successfully handled record. A
// restart must redeliver exactly the errored record.
func TestV1DLQNack_Halt_PositionNeverAdvances(t *testing.T) {
	const n = 5
	sh := newSourceHarness(t, n)

	p := newV1Pipeline(t, sh, v1Config{
		Proc:                &errorAtPosProcessor{positions: map[string]bool{"3": true}},
		DLQWindowSize:       1,
		DLQWindowNackThresh: 0,
	})
	errs := p.waitDone(shapeTimeout)
	if len(errs) == 0 {
		t.Fatal("expected the pipeline to halt on the disabled-DLQ nack")
	}

	for _, i := range []int{1, 2} {
		if !p.dest.hasPosition(pos(i)) {
			t.Fatalf("position %d (before the halt) should have been delivered normally", i)
		}
	}
	for _, i := range []int{3, 4, 5} {
		if p.dest.hasPosition(pos(i)) {
			t.Fatalf("position %d (the halt record or anything after it) was delivered to the main destination despite the halt", i)
		}
	}
	if p.dlq.count() != 0 {
		t.Fatalf("DLQ received %d records, want 0 (DLQ is disabled in this configuration)", p.dlq.count())
	}

	sh.waitPersistedPosition(2, shapeTimeout)

	sh2 := sh.restart(n)
	defer func() { _ = sh2.Source.Teardown(t.Context()) }()
	if err := sh2.Source.Open(t.Context()); err != nil {
		t.Fatalf("restart: open: %v", err)
	}
	recs, err := sh2.Source.Read(t.Context())
	if err != nil {
		t.Fatalf("restart: read: %v", err)
	}
	if len(recs) == 0 || string(recs[0].Position) != string(pos(3)) {
		t.Fatalf("restart redelivered %v first, want position 3", recs)
	}
}

// TestV1_SplitAndFanOut_RefusedLoudlyBeforeAnyWrite covers batch shapes 4
// (split) and 5 (fan-out): both are represented on v1 by a processor
// returning sdk.MultiRecord, which pkg/lifecycle/stream/processor.go's
// handleProcessedRecord always rejects with the actionable, coded
// CodeFanOutRequiresArchV2 error - a pipeline misconfiguration for this
// engine, fatal, never a per-record DLQ (see a980aa0). This asserts that
// refusal fires on the VERY FIRST record (targetPos "1"), so nothing is
// ever written to the destination - the "upgrade parity" story for these
// shapes on v1 is that migrating a fan-out pipeline to a not-yet-arch-v2
// engine fails loud and fast, never silently.
func TestV1_SplitAndFanOut_RefusedLoudlyBeforeAnyWrite(t *testing.T) {
	const n = 3
	sh := newSourceHarness(t, n)

	p := newV1Pipeline(t, sh, v1Config{
		Proc: &multiRecordAtPosProcessor{targetPos: "1", into: 2},
		// DLQ disabled - see TestV1_WrongCountIsFatalNotSilentRetry's identical
		// rationale: this is a pipeline misconfiguration (processor.go's own
		// comment: "Fatal ... not a per-record DLQ"), so it must never
		// "successfully" land in a DLQ that happens to be configured to
		// accept everything.
		DLQWindowSize:       1,
		DLQWindowNackThresh: 0,
	})
	errs := p.waitDone(shapeTimeout)
	if len(errs) == 0 {
		t.Fatal("expected a fatal CodeFanOutRequiresArchV2 error, got none")
	}

	var found bool
	for id, err := range errs {
		ce, ok := conduiterr.Get(err)
		if !ok {
			continue
		}
		if ce.Code.Reason() == stream.CodeFanOutRequiresArchV2.Reason() {
			found = true
		}
		if !strings.Contains(ce.Suggestion, "--preview.pipeline-arch-v2") {
			t.Fatalf("node %q error is missing the actionable --preview.pipeline-arch-v2 suggestion: %q", id, ce.Suggestion)
		}
	}
	if !found {
		t.Fatalf("no node returned the coded CodeFanOutRequiresArchV2 error (errors: %v)", fmtErrs(errs))
	}

	if p.dest.count() != 0 {
		t.Fatalf("destination received %d records; refusal must happen BEFORE any write", p.dest.count())
	}
	if p.dlq.count() != 0 {
		t.Fatalf("DLQ received %d records; this is a fatal pipeline misconfiguration, not a per-record DLQ", p.dlq.count())
	}

	sh.waitPersistedPosition(0, shapeTimeout) // nothing was ever handled, nothing was ever acked
}
