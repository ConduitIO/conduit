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
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/lifecycle-poc/funnel"
	"github.com/matryer/is"
)

// Property 4 (docs/design-documents/20260726-dbz2-cdc-correctness-suite.md,
// Property 4 section) is DBZ-2's transport-half no-silent-drop gate
// (invariant 6). Unlike Properties 1-3, it is NOT a crash-safety scenario -
// it is a routing/ack/position correctness property, so it needs no
// SIGKILL and no cross-process re-exec. What it DOES need, and what the
// existing harness has never had, is a real funnel.Worker (pkg/lifecycle-poc/
// funnel) plus a synthetic destination and a real funnel.DLQ - the direct
// child.go read/ack loop (runReadAckLoop) deliberately bypasses all three
// (see doc.go), so there was previously no routing path to assert invariant
// 6 against at all.
//
// This file builds that path in-process: a REAL *connector.Source (via
// buildChild - byte-for-byte the same construction runChild uses for
// Properties 1-3) is wrapped in a REAL funnel.Worker, with a small
// transport-level drift-detection Task (driftDetectTask) between the source
// and a synthetic main destination, and its own synthetic destination behind
// a REAL funnel.DLQ. Per the design doc's Q1 boundary, driftDetectTask is
// NOT a schema engine: it only inspects chaosPlugin's synthetic
// metadataDriftKey marker (upstream.go) and flags the record as nacked -
// the actual disposition (routed to DLQ vs. propagated/halted) is decided
// entirely by funnel.DLQ's real windowNackThreshold semantics, exactly as
// production DLQ policy would.

// driftDetectTask is Property 4's transport-level policy-ENFORCEMENT POINT -
// deliberately NOT a schema engine (see this file's package doc and the
// design doc's explicit Q1 boundary). It inspects each record for
// chaosPlugin's synthetic drift marker (metadataDriftKey, upstream.go) and
// marks matching records Nack'd; funnel.Worker's real Nack path
// (worker.go:507, which calls w.DLQ.Nack then Source.Ack for the
// successfully-DLQ'd prefix) makes the actual deliver/DLQ/halt decision,
// driven by how the test configures the DLQ's windowNackThreshold - see
// newFunnelHarness's doc comment for the two configurations this test uses.
type driftDetectTask struct {
	id string
}

func (t *driftDetectTask) ID() string                  { return t.id }
func (t *driftDetectTask) Open(context.Context) error  { return nil }
func (t *driftDetectTask) Close(context.Context) error { return nil }

func (t *driftDetectTask) Do(_ context.Context, b *funnel.Batch) error {
	for i, r := range b.ActiveRecords() {
		if _, ok := r.Metadata[metadataDriftKey]; ok {
			b.Nack(i, fmt.Errorf("chaos: record at position %s carries a synthetic drift/poison marker", r.Position))
		}
	}
	return nil
}

// synthDestination is Property 4's synthetic in-memory funnel.Destination -
// standing in for a real sink exactly as chaosPlugin (upstream.go) stands in
// for a real source plugin. Write appends every written record's position
// (in order); Ack immediately acks everything written so far, matching
// DestinationTask.Do's polling contract (destination.go): one Write followed
// by repeated Ack calls until every position has been acknowledged.
type synthDestination struct {
	id string

	mu        sync.Mutex
	delivered []opencdc.Position
	pending   []connector.DestinationAck
}

func (d *synthDestination) ID() string                     { return d.id }
func (d *synthDestination) Open(context.Context) error     { return nil }
func (d *synthDestination) Teardown(context.Context) error { return nil }
func (d *synthDestination) Errors() <-chan error           { return make(chan error) }

func (d *synthDestination) Write(_ context.Context, recs []opencdc.Record) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range recs {
		d.delivered = append(d.delivered, r.Position)
		d.pending = append(d.pending, connector.DestinationAck{Position: r.Position})
	}
	return nil
}

func (d *synthDestination) Ack(context.Context) ([]connector.DestinationAck, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	acks := d.pending
	d.pending = nil
	return acks, nil
}

func (d *synthDestination) deliveredCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.delivered)
}

func (d *synthDestination) hasPosition(pos opencdc.Position) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, p := range d.delivered {
		if string(p) == string(pos) {
			return true
		}
	}
	return false
}

// funnelHarness stands up Property 4's transport-half integration: SourceTask
// (wrapping a real *connector.Source) -> driftDetectTask -> DestinationTask
// (mainDest), with a real funnel.DLQ (its own synthDestination, dlqDest)
// wired into the Worker. See newFunnelHarness for the two DLQ configurations
// this file uses to select between the DLQ-routing and halt policies.
type funnelHarness struct {
	t   *testing.T
	cfg childEnv

	built    *childBuilt
	mainDest *synthDestination
	dlqDest  *synthDestination
	worker   *funnel.Worker

	doErr chan error
}

// newFunnelHarness builds the pipeline described above around a real
// *connector.Source (buildChild - identical construction to runChild's).
//
// dlqWindowSize/dlqWindowThreshold select Property 4's two policies, via
// funnel.DLQ's real dlqWindow semantics (pkg/lifecycle-poc/funnel/dlq.go) -
// confirmed against that package's own worker_ack_order_test.go comment
// ("windowSize=0 disables the DLQ nack-threshold window, so every nacked
// record ... is routed to the DLQ"):
//
//   - (0, 0): the window is disabled outright (len(window)==0 short-circuits
//     dlqWindow.store to unconditionally accept), so the drift record is
//     ALWAYS routed to the DLQ - Property 4's "DLQ-routing" policy case.
//   - (1, 0): windowNackThreshold=0 forces the window to size 1 (dlqWindow's
//     own documented optimization) and the very first nack immediately
//     exceeds a zero threshold, so dlqWindow.store returns 0 accepted -
//     nothing is EVER written to the DLQ destination, and the nack's error
//     propagates immediately as a fatal error. This is Property 4's "halt"
//     policy case: the DLQ is, in effect, disabled.
func newFunnelHarness(t *testing.T, driftAt, total uint64, dlqWindowSize, dlqWindowThreshold int) *funnelHarness {
	t.Helper()
	is := is.New(t)
	dir := t.TempDir()

	cfg := childEnv{
		dbDir:       dir + "/db",
		upstreamDir: dir + "/upstream",
		prune:       false, // Property 4 has no crash/resume path of its own - prune-vs-durable is Property 1/2's axis
		paceMS:      0,     // as fast as possible - this is a routing/ack correctness property, not a timing one
		total:       total,
		driftAt:     driftAt,
	}

	built, err := buildChild(context.Background(), cfg)
	is.NoErr(err)

	logger := log.Test(t)

	mainDest := &synthDestination{id: "chaos-main-dest"}
	dlqDest := &synthDestination{id: "chaos-dlq-dest"}

	srcTask := funnel.NewSourceTask("chaos-source-task", built.src, logger, funnel.NoOpConnectorMetrics{})
	driftTask := &driftDetectTask{id: "chaos-drift-detect"}
	destTask := funnel.NewDestinationTask("chaos-main-dest-task", mainDest, logger, funnel.NoOpConnectorMetrics{})

	destNode := &funnel.TaskNode{Task: destTask}
	driftNode := &funnel.TaskNode{Task: driftTask, Next: []*funnel.TaskNode{destNode}}
	srcNode := &funnel.TaskNode{Task: srcTask, Next: []*funnel.TaskNode{driftNode}}

	dlq := funnel.NewDLQ("chaos-dlq", dlqDest, logger, funnel.NoOpConnectorMetrics{}, dlqWindowSize, dlqWindowThreshold)

	worker, err := funnel.NewWorker(srcNode, dlq, logger, noop.Timer{})
	is.NoErr(err)

	// worker.Open opens every task in the pipeline, including SourceTask,
	// which calls built.src.Open internally (funnel/source.go's
	// SourceTask.Open) - calling src.Open again here directly would hit
	// Source.Open's own "another instance of the connector is already
	// running" guard, so this is the only Open call the source gets.
	is.NoErr(worker.Open(context.Background()))

	h := &funnelHarness{
		t:        t,
		cfg:      cfg,
		built:    built,
		mainDest: mainDest,
		dlqDest:  dlqDest,
		worker:   worker,
		doErr:    make(chan error, 1),
	}

	go func() { h.doErr <- worker.Do(context.Background()) }()

	return h
}

// waitDelivered polls until mainDest+dlqDest have together delivered at
// least n records, or fails the test after timeout.
func (h *funnelHarness) waitDelivered(t *testing.T, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if h.mainDest.deliveredCount()+h.dlqDest.deliveredCount() >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf(
		"timed out waiting for %d records delivered (main=%d, dlq=%d)",
		n, h.mainDest.deliveredCount(), h.dlqDest.deliveredCount(),
	)
}

// waitUpstreamCommitted polls until the synthetic upstream's committed watermark
// equals want, or fails after timeout. On the halt path, waitHalted returns as
// soon as Do propagates the fatal error, but the funnel's acks for records
// handled BEFORE the halt (and thus the upstream commits they drive) land
// ASYNCHRONOUSLY relative to that — so reading the watermark the instant
// waitHalted returns races that pipeline. Under CI load (the full -race suite
// contending) the last handled position's commit can lag the read, and a commit
// still in flight when the test returns can even race t.TempDir cleanup (a
// "rename ... no such file" on the store's atomic commit). Polling to the exact
// expected value removes both races: it waits for precisely the commits that
// must happen and guarantees they have landed before the test proceeds. It
// fails loudly if the watermark advances PAST want — that would be the real
// invariant-6 violation (a halted/DLQ'd record acked upstream "for free").
func (h *funnelHarness) waitUpstreamCommitted(t *testing.T, want uint64, timeout time.Duration) {
	t.Helper()
	is := is.New(t)
	deadline := time.Now().Add(timeout)
	var last uint64
	for time.Now().Before(deadline) {
		committed, err := h.built.upstream.Committed()
		is.NoErr(err)
		last = committed
		if committed == want {
			return
		}
		if committed > want {
			t.Fatalf("upstream committed watermark advanced to %d, past the expected %d - a handled-record was acked upstream that should not have been (invariant 6)", committed, want)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for upstream committed watermark to reach %d (last observed %d)", want, last)
}

// waitPersistedPosition polls Conduit's own durably-persisted resume position
// until it equals want, for the same async-ack reason as waitUpstreamCommitted.
func (h *funnelHarness) waitPersistedPosition(t *testing.T, want uint64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last uint64
	for time.Now().Before(deadline) {
		last = h.persistedPosition(t)
		if last == want {
			return
		}
		if last > want {
			t.Fatalf("persisted resume position advanced to %d, past the expected %d (invariant 6)", last, want)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for persisted resume position to reach %d (last observed %d)", want, last)
}

// stopGracefully stops the worker (draining any in-flight batch, tearing
// down the source) and waits for the Do goroutine to return, asserting it
// returned cleanly (no error) - used by the DLQ-routing case, which is
// expected to complete every record without ever halting.
func (h *funnelHarness) stopGracefully(t *testing.T, timeout time.Duration) {
	t.Helper()
	is := is.New(t)
	is.NoErr(h.worker.Stop(context.Background()))
	select {
	case err := <-h.doErr:
		is.NoErr(err)
	case <-time.After(timeout):
		t.Fatal("timed out waiting for worker.Do to return after Stop")
	}
	is.NoErr(h.worker.Close(context.Background()))
}

// waitHalted waits for the Do goroutine to return an error (the halt case:
// doTask propagates the DLQ-disabled Nack's fatal error up through Do without
// ever calling Stop), then closes the worker so the source is torn down (see
// worker.go's Close doc: "on a fatal-error path Stop is never called, so this
// is where the source's resources are released").
func (h *funnelHarness) waitHalted(t *testing.T, timeout time.Duration) error {
	t.Helper()
	is := is.New(t)
	select {
	case err := <-h.doErr:
		is.NoErr(h.worker.Close(context.Background()))
		return err
	case <-time.After(timeout):
		t.Fatal("timed out waiting for worker.Do to halt")
		return nil // unreachable
	}
}

// persistedPosition reads Conduit's own durably-persisted resume position
// directly from the store (not the in-memory Instance the harness already
// holds), the same round-trip through connector.Store.Get/decode every other
// scenario in this suite uses to confirm durability, not just in-memory
// state.
func (h *funnelHarness) persistedPosition(t *testing.T) uint64 {
	t.Helper()
	is := is.New(t)
	instance, err := h.built.store.Get(context.Background(), instanceID)
	is.NoErr(err)
	state, ok := instance.State.(connector.SourceState)
	if !ok {
		return 0 // never acked anything yet
	}
	pos, err := decodePosition(state.Position)
	is.NoErr(err)
	return pos
}

// redeliverAfterRestart models a genuine process restart in-process: it
// reloads the connector.Instance FRESH from the same on-disk store (the same
// durability round-trip a brand-new process's loadOrCreateInstance would
// make - see child.go) and builds a brand-new *connector.Source around a
// brand-new chaosPlugin (standing in for the reconnected upstream, exactly
// as chaosPlugin already stands in for a real source plugin elsewhere in
// this suite), reusing the same badger DB/persister/upstreamStore (a single
// OS process never needs to reacquire the badger directory lock, unlike the
// cross-process SIGKILL cases). It returns the very first record the
// restarted source reads - proving at-least-once redelivery of whatever
// Conduit's persisted position says comes next, without needing a real
// SIGKILL/re-exec cycle (Property 4 is a routing/ack property, not a
// crash-safety one - see this file's package doc).
func (h *funnelHarness) redeliverAfterRestart(t *testing.T) opencdc.Record {
	t.Helper()
	is := is.New(t)
	ctx := context.Background()

	instance2 := loadOrCreateInstance(ctx, h.built.store)
	instance2.Init(h.built.logger, h.built.persister)

	plugin2 := &chaosPlugin{
		store:   h.built.upstream,
		total:   h.cfg.total,
		paceMS:  0,
		driftAt: h.cfg.driftAt,
	}
	fetcher2 := staticFetcher{instance2.Plugin: staticDispenser{source: plugin2}}
	c2, err := instance2.Connector(ctx, fetcher2)
	is.NoErr(err)
	src2, ok := c2.(*connector.Source)
	is.True(ok)

	is.NoErr(src2.Open(ctx))
	t.Cleanup(func() { _ = src2.Teardown(ctx) })

	recs, err := src2.Read(ctx)
	is.NoErr(err)
	is.True(len(recs) > 0)
	return recs[0]
}

// TestSchemaDrift_TransportHalf_DLQRouting is Property 4's DLQ-routing case
// (docs/design-documents/20260726-dbz2-cdc-correctness-suite.md, Property 4
// section). It drives 5 records through the funnel, one of them (position 3)
// carrying the synthetic drift marker, under a DLQ config that always
// accepts (see newFunnelHarness's doc). Assertion: the drift record lands in
// the DLQ's own synthetic destination (never the main one), and Source.Ack
// IS called for it (it was handled, not silently dropped) - so the final
// persisted/upstream position reaches every position through total, not just
// the non-drift ones.
func TestSchemaDrift_TransportHalf_DLQRouting(t *testing.T) {
	is := is.New(t)

	const (
		total   = 5
		driftAt = 3
	)

	h := newFunnelHarness(t, driftAt, total, 0, 0) // windowSize=0: DLQ always accepts, see doc
	h.waitDelivered(t, total, 10*time.Second)
	h.stopGracefully(t, 10*time.Second)

	// The drift record landed in the DLQ's synthetic destination, never the
	// main one - routed per policy, never silently coerced through.
	is.True(h.dlqDest.hasPosition(encodePosition(driftAt)))
	is.True(!h.mainDest.hasPosition(encodePosition(driftAt)))

	// Every other position went through the main destination undisturbed.
	for _, pos := range []uint64{1, 2, 4, 5} {
		if !h.mainDest.hasPosition(encodePosition(pos)) {
			t.Fatalf("position %d (non-drift) was never delivered to the main destination", pos)
		}
	}
	is.Equal(h.dlqDest.deliveredCount(), 1) // exactly the one drift record, nothing else

	// The core non-silent-drop assertion: the drift record WAS handled (DLQ
	// routing counts as handled, per the design doc), so it was acked
	// upstream like everything else - the persisted/upstream position
	// reaches every position through total, not stopping short at the
	// drift record.
	// Wait for the ack->commit to land before asserting. Acking is deferred
	// behind connector.Persister's debounce and the source's deferred-ack
	// delivery goroutine, so the upstream commit is an ASYNCHRONOUS side
	// effect of the delivery asserted above - reading it immediately can
	// legitimately observe total-1 on a loaded machine, which is how this
	// failed in CI as "4 != 5" (#2534, three occurrences, never reproducible
	// locally).
	//
	// This does not weaken the check: the wait is bounded, and if the commit
	// never reaches total the assertions below still fail exactly as before.
	// It only removes the assumption that an async effect has already
	// happened at the instant the synchronous one completed.
	if !waitForUpstreamAtLeast(h.built.upstream, uint64(total), 10*time.Second) {
		t.Fatalf("upstream commit never reached %d within the timeout - the drift "+
			"record was not acked upstream (a silent drop is exactly what this "+
			"test exists to catch)", total)
	}

	committed, err := h.built.upstream.Committed()
	is.NoErr(err)
	is.Equal(committed, uint64(total))
	is.Equal(h.persistedPosition(t), uint64(total))
}

// TestSchemaDrift_TransportHalf_Halt_PositionNeverAdvances is Property 4's
// halt case. The drift record (position 3 of 5) hits a DLQ config that never
// accepts (windowNackThreshold=0, see newFunnelHarness's doc), so
// funnel.Worker.Nack propagates a fatal error without ever calling
// Source.Ack for it - the pipeline halts. Assertion: positions 1-2 (before
// the drift record) were delivered and acked normally; the drift record and
// everything after it (3, 4, 5) were NEVER delivered anywhere (not the main
// destination, not the DLQ); the persisted/upstream position never advances
// past 2; and a modeled restart (redeliverAfterRestart) redelivers exactly
// the drift record, never skipping it - at-least-once preserved.
func TestSchemaDrift_TransportHalf_Halt_PositionNeverAdvances(t *testing.T) {
	is := is.New(t)

	const (
		total   = 5
		driftAt = 3
	)

	h := newFunnelHarness(t, driftAt, total, 1, 0) // windowSize=1, threshold=0: DLQ disabled, see doc
	err := h.waitHalted(t, 10*time.Second)
	is.True(err != nil) // the pipeline halted - Do did not complete all 5 records

	for _, pos := range []uint64{1, 2} {
		if !h.mainDest.hasPosition(encodePosition(pos)) {
			t.Fatalf("position %d (before the drift record) should have been delivered normally before the halt", pos)
		}
	}
	for _, pos := range []uint64{driftAt, 4, 5} {
		if h.mainDest.hasPosition(encodePosition(pos)) || h.dlqDest.hasPosition(encodePosition(pos)) {
			t.Fatalf(
				"position %d (the drift record or anything after it) was delivered SOMEWHERE despite the "+
					"halt - invariant 6 requires it never be silently coerced/delivered once the pipeline "+
					"has halted on it",
				pos,
			)
		}
	}

	// The core invariant-6 transport assertion: the persisted/upstream
	// position must NEVER advance past the last successfully handled record
	// (2) - the drift record must never be acked upstream "for free" just
	// because the pipeline gave up on it. Poll (don't read instantly): the
	// halt path's acks for positions 1-2 land asynchronously relative to
	// waitHalted returning - see waitUpstreamCommitted. Both helpers fail loudly
	// if the watermark ever creeps PAST 2, which is the actual invariant-6
	// violation this test exists to catch.
	h.waitUpstreamCommitted(t, 2, 10*time.Second)
	h.waitPersistedPosition(t, 2, 10*time.Second) // Conduit's own persisted resume position agrees

	// At-least-once: a restart must redeliver the unhandled record, not skip
	// it.
	redelivered := h.redeliverAfterRestart(t)
	is.Equal(redelivered.Position, encodePosition(driftAt))
	_, hasMarker := redelivered.Metadata[metadataDriftKey]
	is.True(hasMarker) // it's genuinely the SAME poison record, not a coincidentally-numbered one
}
