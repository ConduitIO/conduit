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

// This file is arch-v2's drain-safety chaos coverage
// (docs/design-documents/20260731-archv2-drain-reconfigure.md, AC-1/AC-3/AC-6).
// Unlike sigkill_test.go/sigterm_test.go, it needs no cross-process re-exec:
// it drives the exact same in-process funnel.Worker + real *connector.Source
// path Property 4 (property4_test.go) already established for transport-half
// correctness, reused here for the drain-under-load and O2-bound properties
// instead. TestArchV2_LiveApplyDrain_UnderLoad_NoGap is AC-1/AC-3: every
// pre-stop-acked record is durable and the final position is checkpointed,
// with continuous load in flight when Stop is called — a "live apply while
// records are still arriving" scenario, not just a quiescent pipeline.
// TestArchV2_LiveApplyDrain_StuckDestination_O2Bound is AC-6: a wedged
// destination makes Worker.Stop hang without a bound (see
// pkg/lifecycle-poc.Service.StopAndWait's O2 fix), and this test proves the
// same ctx-deadline mechanism that fix relies on actually bounds Worker.Stop
// itself, with no force-kill and no gap.

// blockingDestination is a synthetic funnel.Destination whose Ack blocks on a
// channel until released, standing in for a wedged/stuck real destination
// (a hung connection, a stalled disk) exactly the way chaosPlugin
// (upstream.go) stands in for a real source plugin. Write always succeeds
// (buffers immediately, matching DestinationTask.Do's Write-then-poll-Ack
// contract); Ack blocks on release before returning the accumulated acks —
// so a batch that reached this destination holds funnel.Worker's
// processingLock for as long as release stays open.
type blockingDestination struct {
	id      string
	release <-chan struct{}

	mu        sync.Mutex
	delivered []opencdc.Position
	pending   []connector.DestinationAck
}

func (d *blockingDestination) ID() string                     { return d.id }
func (d *blockingDestination) Open(context.Context) error     { return nil }
func (d *blockingDestination) Teardown(context.Context) error { return nil }
func (d *blockingDestination) Errors() <-chan error           { return make(chan error) }

func (d *blockingDestination) Write(_ context.Context, recs []opencdc.Record) error {
	d.mu.Lock()
	for _, r := range recs {
		d.delivered = append(d.delivered, r.Position)
		d.pending = append(d.pending, connector.DestinationAck{Position: r.Position})
	}
	d.mu.Unlock()
	return nil
}

func (d *blockingDestination) Ack(ctx context.Context) ([]connector.DestinationAck, error) {
	select {
	case <-d.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	acks := d.pending
	d.pending = nil
	return acks, nil
}

func (d *blockingDestination) deliveredCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.delivered)
}

// buildDrainWorker stands up a real *connector.Source (buildChild, identical
// construction to every other scenario in this suite) wrapped in a real
// funnel.Worker with a plain, no-DLQ-window destination — the minimal shape
// needed to exercise Worker.Stop's drain (processingLock + Source.Teardown),
// the exact primitive lifecycle-poc.Service.StopAndWait composes on top of.
func buildDrainWorker(t *testing.T, cfg childEnv, dest funnel.Destination) (*childBuilt, *funnel.Worker) {
	t.Helper()
	is := is.New(t)

	built, err := buildChild(context.Background(), cfg)
	is.NoErr(err)

	logger := log.Test(t)
	srcTask := funnel.NewSourceTask("chaos-drain-source", built.src, logger, funnel.NoOpConnectorMetrics{})
	destTask := funnel.NewDestinationTask("chaos-drain-dest", dest, logger, funnel.NoOpConnectorMetrics{})
	destNode := &funnel.TaskNode{Task: destTask}
	srcNode := &funnel.TaskNode{Task: srcTask, Next: []*funnel.TaskNode{destNode}}

	dlqDest := &synthDestination{id: "chaos-drain-dlq"}
	dlq := funnel.NewDLQ("chaos-drain-dlq", dlqDest, logger, funnel.NoOpConnectorMetrics{}, 0, 0)

	worker, err := funnel.NewWorker(srcNode, dlq, logger, noop.Timer{})
	is.NoErr(err)
	is.NoErr(worker.Open(context.Background())) // opens SourceTask, which opens built.src internally

	return built, worker
}

// persistedPosition reads Conduit's own durably-persisted resume position
// directly from the store, mirroring property4_test.go's identical helper.
// Unlike that helper, this one also tolerates the connector never having been
// persisted to the store at all yet (store.Get returns a raw "key does not
// exist" error, not a decodable Instance with a zero State) - the case before
// the very first flush - reporting position 0 for it, same as an Instance
// that exists but has no SourceState set.
func drainPersistedPosition(t *testing.T, built *childBuilt) uint64 {
	t.Helper()
	is := is.New(t)
	instance, err := built.store.Get(context.Background(), instanceID)
	if err != nil {
		return 0 // not persisted yet at all
	}
	state, ok := instance.State.(connector.SourceState)
	if !ok {
		return 0
	}
	pos, err := decodePosition(state.Position)
	is.NoErr(err)
	return pos
}

// waitUpstreamCommitted polls until the upstream's committed watermark equals
// want, or fails after timeout. Necessary (not just belt-and-suspenders):
// Worker.Close/Source.Teardown returning only guarantees Conduit's own
// deferred ack was SENT to the plugin (Approach A/A2's own contract) — it
// says nothing about how long the plugin's own receive-and-commit loop takes
// to actually process that message, which runs in its own goroutine.
// Mirrors property4_test.go's identical-purpose helper and its doc comment on
// exactly this async-ack-landing race.
func waitUpstreamCommitted(t *testing.T, built *childBuilt, want uint64, timeout time.Duration) {
	t.Helper()
	is := is.New(t)
	deadline := time.Now().Add(timeout)
	var last uint64
	for time.Now().Before(deadline) {
		committed, err := built.upstream.Committed()
		is.NoErr(err)
		last = committed
		if committed == want {
			return
		}
		if committed > want {
			t.Fatalf("upstream committed watermark advanced to %d, past the expected %d - a not-yet-durable record was acked upstream (invariant 1)", committed, want)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for upstream committed watermark to reach %d (last observed %d)", want, last)
}

// waitPersistedPosition polls Conduit's own durably-persisted resume
// position until it equals want, for the same async-landing reason as
// waitUpstreamCommitted.
func waitPersistedPosition(t *testing.T, built *childBuilt, want uint64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last uint64
	for time.Now().Before(deadline) {
		last = drainPersistedPosition(t, built)
		if last == want {
			return
		}
		if last > want {
			t.Fatalf("persisted resume position advanced to %d, past the expected %d (invariant 1)", last, want)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for persisted resume position to reach %d (last observed %d)", want, last)
}

// TestArchV2_LiveApplyDrain_UnderLoad_NoGap is AC-1/AC-3: a live-apply-under-load
// drain (Worker.Stop called while the source is still actively producing and
// delivering records, exactly what provisioning.Service.ApplyPlanLive's
// StopAndWait triggers against a running pipeline) must deliver every
// pre-stop-acked record durably downstream, with the final position
// checkpointed — and, per the funnel drain audit's point 2 (design doc §3.1),
// never a gap: the delivered/committed/persisted positions must all be
// contiguous from 1 through whatever was last acked, with nothing skipped.
func TestArchV2_LiveApplyDrain_UnderLoad_NoGap(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	cfg := childEnv{
		dbDir:       dir + "/db",
		upstreamDir: dir + "/upstream",
		prune:       false,
		paceMS:      2, // steady load - Stop must race genuinely in-flight production
		total:       0, // unbounded: this run terminates via Stop, not by reaching a count
	}

	mainDest := &synthDestination{id: "chaos-drain-main"}
	built, worker := buildDrainWorker(t, cfg, mainDest)

	doErr := make(chan error, 1)
	go func() { doErr <- worker.Do(context.Background()) }()

	// Let a healthy amount of load flow through before stopping - this is the
	// "under load" condition: Stop below races genuinely ongoing production,
	// not a quiescent pipeline.
	const minDelivered = 25
	deadline := time.Now().Add(30 * time.Second)
	for mainDest.deliveredCount() < minDelivered {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d records delivered (got %d)", minDelivered, mainDest.deliveredCount())
		}
		time.Sleep(time.Millisecond)
	}

	is.NoErr(worker.Stop(context.Background()))

	select {
	case err := <-doErr:
		is.NoErr(err) // graceful: Stop must not surface an error under normal (non-wedged) drain
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for worker.Do to return after Stop")
	}
	is.NoErr(worker.Close(context.Background()))

	lastDelivered := mainDest.deliveredCount()
	if lastDelivered < minDelivered {
		t.Fatalf("delivered count decreased across Stop (%d -> %d) - impossible, harness bug", minDelivered, lastDelivered)
	}

	// No-gap assertion: every position from 1..lastDelivered was delivered,
	// with nothing skipped (invariant 3) and nothing delivered twice on this
	// single, uninterrupted run (invariant 4 - ordering).
	for pos := uint64(1); pos <= uint64(lastDelivered); pos++ {
		if !mainDest.hasPosition(encodePosition(pos)) {
			t.Fatalf("gap detected: position %d was never delivered to the destination (delivered count=%d)", pos, lastDelivered)
		}
	}

	// Durability: the upstream's committed watermark and Conduit's own
	// persisted resume position must both (eventually - see
	// waitUpstreamCommitted's doc on why this is a poll, not an immediate
	// read) agree with the last delivered position exactly - Worker.Stop's
	// drain (processingLock quiescence + Source.Teardown's forced flush)
	// must have made it durable, not left it in memory only.
	waitUpstreamCommitted(t, built, uint64(lastDelivered), 10*time.Second)
	waitPersistedPosition(t, built, uint64(lastDelivered), 10*time.Second)
}

// TestArchV2_LiveApplyDrain_StuckDestination_O2Bound is AC-6: the same
// wedged-destination scenario pkg/lifecycle-poc.Service.StopAndWait's O2 fix
// bounds (see the design doc's "O2: bounding the drain"), exercised directly
// at the funnel.Worker level — the primitive StopAndWait composes on top of.
// A batch reaches the destination and is held there (processingLock stays
// held); Worker.Stop, called with a short ctx deadline, must return promptly
// (ctx.DeadlineExceeded, not hang forever), and — critically — nothing must
// be force-killed or lost: the wedged record must never be acked upstream nor
// have its position advanced. Releasing the wedge afterward lets the batch
// complete normally, proving the worker was left fully functional, not torn
// down, by the bounded timeout.
func TestArchV2_LiveApplyDrain_StuckDestination_O2Bound(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	cfg := childEnv{
		dbDir:       dir + "/db",
		upstreamDir: dir + "/upstream",
		prune:       false,
		paceMS:      0,
		total:       1, // exactly one record - it will reach the destination and wedge there
	}

	release := make(chan struct{})
	dest := &blockingDestination{id: "chaos-drain-stuck", release: release}
	built, worker := buildDrainWorker(t, cfg, dest)

	doErr := make(chan error, 1)
	go func() { doErr <- worker.Do(context.Background()) }()

	// Wait for the record to reach the destination (Write called) - it is now
	// wedged in Ack, holding processingLock.
	deadline := time.Now().Add(10 * time.Second)
	for dest.deliveredCount() < 1 {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the record to reach the stuck destination")
		}
		time.Sleep(time.Millisecond)
	}

	// The O2 bound: a short ctx deadline must make Stop return promptly
	// instead of hanging on the wedged batch forever.
	stopCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := worker.Stop(stopCtx)
	elapsed := time.Since(start)

	is.True(err != nil)
	is.Equal(err, context.DeadlineExceeded)
	is.True(elapsed < 5*time.Second) // bounded, did not hang

	// No-gap / no-force-kill assertion: nothing was acked upstream and no
	// position was persisted - the wedge means the batch never completed, so
	// this record remains exactly as "not yet durably handled" as it was
	// before Stop was ever called (invariant 1).
	committed, cErr := built.upstream.Committed()
	is.NoErr(cErr)
	is.Equal(committed, uint64(0))
	is.Equal(drainPersistedPosition(t, built), uint64(0))

	// Cleanup: release the wedge so the batch completes normally, proving the
	// worker was left fully functional (not torn down) by the timeout. The
	// earlier Stop call never actually set the worker's internal stop flag
	// (it timed out before reaching that point - the same reasoning
	// stopRunnablePipeline's intentionalStop rollback in
	// pkg/lifecycle-poc/service.go relies on), so the batch, once it
	// completes, would otherwise have the worker try to read a next batch
	// from a source that has nothing more to offer; issue a real
	// (unbounded) graceful Stop now that the wedge is clear to bring it down
	// cleanly instead.
	close(release)

	is.NoErr(worker.Stop(context.Background()))

	select {
	case err := <-doErr:
		is.NoErr(err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for worker.Do to return after releasing the wedge")
	}
	is.NoErr(worker.Close(context.Background()))

	// Once released, the single record completes normally: delivered,
	// acked, and (eventually — the plugin's own receive-and-commit loop
	// runs asynchronously relative to Worker.Close returning, see
	// waitUpstreamCommitted's doc) durable both upstream and in Conduit's
	// own store.
	waitUpstreamCommitted(t, built, 1, 10*time.Second)
	waitPersistedPosition(t, built, 1, 10*time.Second)
}
