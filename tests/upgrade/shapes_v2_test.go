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

// shapes_v2_test.go drives each batch SHAPE through the v2 (funnel) engine
// and asserts resume correctness. See doc.go for the package-level
// rationale, and plugin.go's seqPlugin for why this suite is not built on
// conduit-connector-generator.
package upgrade

import (
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/lifecycle-poc/funnel"
)

const shapeTimeout = 10 * time.Second

// pos is a small helper for building an opencdc.Position from a 1-based
// sequence index, matching seqPlugin's encoding.
func pos(n int) opencdc.Position { return encodeSeqPosition(n) }

// TestV2Filter_ResumeCorrectness drives a batch with one record filtered
// mid-batch through the v2 engine. Filtered records are still included in
// the ack (they reached a terminal, "handled" disposition - see batch.go's
// Filter doc), so the persisted position must reach the very end even
// though the destination never sees the filtered record.
func TestV2Filter_ResumeCorrectness(t *testing.T) {
	const n = 5
	sh := newSourceHarness(t, n)
	dest := newMemDestination("main")

	p := newV2Pipeline(t, sh, v2Config{
		Middle: []funnel.Task{&filterAtTask{id: "filter", indices: map[int]bool{2: true}}}, // filters record "3"
		Dests:  []*memDestination{dest},
	})
	p.waitTotalDelivered(n-1, shapeTimeout, dest)
	p.stopGracefully(shapeTimeout)

	if dest.hasPosition(pos(3)) {
		t.Fatal("filtered record (position 3) was delivered to the destination")
	}
	for _, i := range []int{1, 2, 4, 5} {
		if !dest.hasPosition(pos(i)) {
			t.Fatalf("position %d (not filtered) was never delivered", i)
		}
	}
	if got := dest.count(); got != n-1 {
		t.Fatalf("destination delivered %d records, want %d", got, n-1)
	}

	sh.waitPersistedPosition(n, shapeTimeout) // filtered record still counts as handled
}

// TestV2Retry_ResumeCorrectness drives a batch where a task marks a
// mid-batch range for retry (a processor returning fewer records than it
// received) and converges cleanly on the very next attempt - no split. Every
// record must reach the destination exactly once and the persisted position
// must reach the end.
func TestV2Retry_ResumeCorrectness(t *testing.T) {
	const n = 4
	sh := newSourceHarness(t, n)
	dest := newMemDestination("main")

	p := newV2Pipeline(t, sh, v2Config{
		// Retry records "2" and "3" (0-based active indices [1,3)) on the
		// first pass; converges (plain ack, no further split) on retry.
		Middle: []funnel.Task{&retryRangeOnceTask{id: "retry", from: 1, to: 3}},
		Dests:  []*memDestination{dest},
	})
	p.waitTotalDelivered(n, shapeTimeout, dest)
	p.stopGracefully(shapeTimeout)

	seen := map[string]int{}
	for _, pp := range dest.positions() {
		seen[string(pp)]++
	}
	for i := 1; i <= n; i++ {
		if seen[string(pos(i))] != 1 {
			t.Fatalf("position %d delivered %d times, want exactly 1 (delivered=%v)", i, seen[string(pos(i))], seen)
		}
	}

	sh.waitPersistedPosition(n, shapeTimeout)
}

// TestV2DLQNack_Routing_ResumeCorrectness drives a batch with one record
// nacked mid-batch under a DLQ policy that always accepts (windowSize=0 -
// see pkg/lifecycle-poc/funnel/dlq.go's dlqWindow.store: size 0 disables the
// window, unconditionally accepting). The nacked record is "handled" (DLQ
// routing counts as handled), so it must be acked upstream like every other
// record - the persisted position must reach the very end.
func TestV2DLQNack_Routing_ResumeCorrectness(t *testing.T) {
	const n = 5
	sh := newSourceHarness(t, n)
	dest := newMemDestination("main")
	dlqDest := newMemDestination("dlq")

	p := newV2Pipeline(t, sh, v2Config{
		Middle:              []funnel.Task{&nackAtTask{id: "nack", indices: map[int]bool{2: true}}}, // nacks record "3"
		Dests:               []*memDestination{dest},
		DLQDest:             dlqDest,
		DLQWindowSize:       0,
		DLQWindowNackThresh: 0,
	})
	p.waitTotalDelivered(n, shapeTimeout, dest, dlqDest)
	p.stopGracefully(shapeTimeout)

	if !dlqDest.hasPosition(pos(3)) {
		t.Fatal("nacked record (position 3) never reached the DLQ")
	}
	if dest.hasPosition(pos(3)) {
		t.Fatal("nacked record (position 3) was delivered to the main destination")
	}
	for _, i := range []int{1, 2, 4, 5} {
		if !dest.hasPosition(pos(i)) {
			t.Fatalf("position %d (not nacked) was never delivered to the main destination", i)
		}
	}

	sh.waitPersistedPosition(n, shapeTimeout) // DLQ routing counts as handled
}

// TestV2DLQNack_Halt_PositionNeverAdvances drives the same shape under a DLQ
// policy that never accepts (windowSize=1, threshold=0 - see
// pkg/lifecycle-poc/funnel/dlq.go's newDLQWindow: the very first nack
// immediately exceeds a zero threshold, so the DLQ is, in effect, disabled).
// The pipeline halts: positions before the nack are delivered and acked
// normally; the nacked record and everything after it are NEVER delivered
// anywhere, and the persisted position never advances past the last
// successfully handled record. A modeled restart must redeliver exactly the
// nacked record, never skipping it.
func TestV2DLQNack_Halt_PositionNeverAdvances(t *testing.T) {
	const n = 5
	sh := newSourceHarness(t, n)
	dest := newMemDestination("main")
	dlqDest := newMemDestination("dlq")

	p := newV2Pipeline(t, sh, v2Config{
		Middle:              []funnel.Task{&nackAtTask{id: "nack", indices: map[int]bool{2: true}}}, // nacks record "3"
		Dests:               []*memDestination{dest},
		DLQDest:             dlqDest,
		DLQWindowSize:       1,
		DLQWindowNackThresh: 0,
	})
	err := p.waitFatal(shapeTimeout)
	if err == nil {
		t.Fatal("expected the pipeline to halt on the disabled-DLQ nack")
	}

	for _, i := range []int{1, 2} {
		if !dest.hasPosition(pos(i)) {
			t.Fatalf("position %d (before the halt) should have been delivered normally", i)
		}
	}
	for _, i := range []int{3, 4, 5} {
		if dest.hasPosition(pos(i)) || dlqDest.hasPosition(pos(i)) {
			t.Fatalf("position %d (the nack or anything after it) was delivered SOMEWHERE despite the halt", i)
		}
	}

	sh.waitPersistedPosition(2, shapeTimeout)

	// At-least-once: a restart must redeliver the unhandled record, never
	// skip it.
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

// TestV2Split_ResumeCorrectness drives a batch where a task splits one
// record into several pieces (sdk.MultiRecord). A pure split (no
// retry/nack) never enters Worker.doTaskAttempt's tainted sub-batch loop at
// all - the whole batch, split pieces included, is forwarded and acked in
// one atomic pass - so this is a baseline correctness check, not the #2722
// mutation-check shape (see TestV2Combo_RetryThenSplit for that).
func TestV2Split_ResumeCorrectness(t *testing.T) {
	const n = 4
	sh := newSourceHarness(t, n)
	dest := newMemDestination("main")

	p := newV2Pipeline(t, sh, v2Config{
		Middle: []funnel.Task{&splitAtTask{id: "split", index: 1, into: 3}}, // splits record "2" into "2a","2b","2c"
		Dests:  []*memDestination{dest},
	})
	p.waitTotalDelivered(n-1+3, shapeTimeout, dest) // 1,2a,2b,2c,3,4 = 6 active records
	p.stopGracefully(shapeTimeout)

	for _, suffix := range []string{"a", "b", "c"} {
		if !dest.hasPosition(opencdc.Position("2" + suffix)) {
			t.Fatalf("split piece 2%s was never delivered", suffix)
		}
	}
	for _, i := range []int{1, 3, 4} {
		if !dest.hasPosition(pos(i)) {
			t.Fatalf("position %d was never delivered", i)
		}
	}
	if got, want := dest.count(), n-1+3; got != want {
		t.Fatalf("destination delivered %d records, want %d", got, want)
	}

	sh.waitPersistedPosition(n, shapeTimeout)
}

// TestV2FanOut_ResumeCorrectness drives a plain batch (no split, no
// retry, no nack) through an M=2 destination fan-out. Every destination
// must independently receive every record (broadcast, not partition), and
// the persisted position must only reach the end once BOTH branches have
// acked every position.
func TestV2FanOut_ResumeCorrectness(t *testing.T) {
	const n = 4
	sh := newSourceHarness(t, n)
	d1 := newMemDestination("d1")
	d2 := newMemDestination("d2")

	p := newV2Pipeline(t, sh, v2Config{
		Dests: []*memDestination{d1, d2},
	})
	p.waitEachDelivered(n, shapeTimeout, d1, d2)
	p.stopGracefully(shapeTimeout)

	for _, d := range []*memDestination{d1, d2} {
		for i := 1; i <= n; i++ {
			if !d.hasPosition(pos(i)) {
				t.Fatalf("destination %q never received position %d", d.id, i)
			}
		}
		if got := d.count(); got != n {
			t.Fatalf("destination %q delivered %d records, want %d", d.id, got, n)
		}
	}

	sh.waitPersistedPosition(n, shapeTimeout)
}
