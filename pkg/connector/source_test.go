// Copyright © 2022 Meroxa, Inc.
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

package connector

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	"github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

func TestSource_NoLifecycleEvent(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, sourceMock := newTestSource(ctx, t, ctrl)

	// assume that the same config was already active last time
	src.Instance.LastActiveConfig = src.Instance.Config

	_ = expectSourceOpen(src, sourceMock)

	// source should not trigger any lifecycle event, because the config did not change

	err := src.Open(ctx)
	is.NoErr(err)

	// after plugin is started the last active config is still the same
	is.Equal(src.Instance.LastActiveConfig, src.Instance.Config)
}

func TestSource_LifecycleOnCreated_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, sourceMock := newTestSource(ctx, t, ctrl)

	// before plugin is started we expect LastActiveConfig to be empty
	is.Equal(src.Instance.LastActiveConfig, Config{})

	_ = expectSourceOpen(src, sourceMock)

	// source should know it's the first run and trigger LifecycleOnCreated
	sourceMock.EXPECT().LifecycleOnCreated(
		gomock.Any(),
		pconnector.SourceLifecycleOnCreatedRequest{Config: src.Instance.Config.Settings},
	).Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil)

	err := src.Open(ctx)
	is.NoErr(err)

	// after plugin is started we expect LastActiveConfig to be set to Config
	is.Equal(src.Instance.LastActiveConfig, src.Instance.Config)
}

func TestSource_LifecycleOnUpdated_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, sourceMock := newTestSource(ctx, t, ctrl)

	// assume that there was a config already active, but with different settings
	src.Instance.LastActiveConfig.Settings = map[string]string{"last-active": "yes"}

	_ = expectSourceOpen(src, sourceMock)

	// source should know it was already run once with a different config and trigger LifecycleOnUpdated
	sourceMock.EXPECT().LifecycleOnUpdated(
		gomock.Any(),
		pconnector.SourceLifecycleOnUpdatedRequest{
			ConfigBefore: src.Instance.LastActiveConfig.Settings,
			ConfigAfter:  src.Instance.Config.Settings,
		},
	).Return(pconnector.SourceLifecycleOnUpdatedResponse{}, nil)

	err := src.Open(ctx)
	is.NoErr(err)

	// after plugin is started we expect LastActiveConfig to be set to Config
	is.Equal(src.Instance.LastActiveConfig, src.Instance.Config)
}

func TestSource_LifecycleOnCreated_Error(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, sourceMock := newTestSource(ctx, t, ctrl)

	// before plugin is started we expect LastActiveConfig to be empty
	is.Equal(src.Instance.LastActiveConfig, Config{})

	sourceMock.EXPECT().Configure(
		gomock.Any(),
		pconnector.SourceConfigureRequest{Config: src.Instance.Config.Settings},
	).Return(pconnector.SourceConfigureResponse{}, nil)

	// source should know it's the first run and trigger LifecycleOnCreated, but it fails
	want := cerrors.New("whoops")
	sourceMock.EXPECT().LifecycleOnCreated(
		gomock.Any(),
		pconnector.SourceLifecycleOnCreatedRequest{Config: src.Instance.Config.Settings},
	).Return(pconnector.SourceLifecycleOnCreatedResponse{}, want)

	// source should terminate plugin in case of an error
	sourceMock.EXPECT().Teardown(gomock.Any(), pconnector.SourceTeardownRequest{}).Return(pconnector.SourceTeardownResponse{}, nil)

	err := src.Open(ctx)
	is.True(cerrors.Is(err, want))

	// after plugin is started we expect LastActiveConfig to be left unchanged
	is.Equal(src.Instance.LastActiveConfig, Config{})
}

func TestSource_LifecycleOnDeleted_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, sourceMock := newTestSource(ctx, t, ctrl)

	// assume that there was a config already active, but with different settings
	src.Instance.LastActiveConfig.Settings = map[string]string{"last-active": "yes"}

	sourceMock.EXPECT().LifecycleOnDeleted(
		gomock.Any(),
		pconnector.SourceLifecycleOnDeletedRequest{Config: src.Instance.LastActiveConfig.Settings},
	).Return(pconnector.SourceLifecycleOnDeletedResponse{}, nil)

	sourceMock.EXPECT().Teardown(gomock.Any(), pconnector.SourceTeardownRequest{}).Return(pconnector.SourceTeardownResponse{}, nil)

	err := src.OnDelete(ctx)
	is.NoErr(err)
}

func TestSource_LifecycleOnCreated_BackwardsCompatibility(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, sourceMock := newTestSource(ctx, t, ctrl)

	// before plugin is started we expect LastActiveConfig to be empty
	is.Equal(src.Instance.LastActiveConfig, Config{})

	_ = expectSourceOpen(src, sourceMock)

	// An older connector that predates lifecycle events returns
	// pconnector.ErrUnimplemented (the "Unimplemented" gRPC status, unwrapped by
	// the protocol client). Conduit must treat this as backwards compatible and
	// open the source without a fatal error. Regression test for #1999 — this is
	// the exact path (created event during Open) that was crashing real
	// pipelines against older connectors.
	sourceMock.EXPECT().LifecycleOnCreated(
		gomock.Any(),
		pconnector.SourceLifecycleOnCreatedRequest{Config: src.Instance.Config.Settings},
	).Return(pconnector.SourceLifecycleOnCreatedResponse{}, pconnector.ErrUnimplemented)

	err := src.Open(ctx)
	is.NoErr(err)
}

func TestSource_LifecycleOnDeleted_BackwardsCompatibility(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, sourceMock := newTestSource(ctx, t, ctrl)

	// assume that there was a config already active, but with different settings
	src.Instance.LastActiveConfig.Settings = map[string]string{"last-active": "yes"}

	// we should ignore the error if the plugin does not implement lifecycle
	// events. Older connectors surface this as pconnector.ErrUnimplemented (see
	// the trigger in source.go and issue #1999).
	sourceMock.EXPECT().LifecycleOnDeleted(
		gomock.Any(),
		pconnector.SourceLifecycleOnDeletedRequest{Config: src.Instance.LastActiveConfig.Settings},
	).Return(pconnector.SourceLifecycleOnDeletedResponse{}, pconnector.ErrUnimplemented)

	sourceMock.EXPECT().Teardown(gomock.Any(), pconnector.SourceTeardownRequest{}).Return(pconnector.SourceTeardownResponse{}, nil)

	err := src.OnDelete(ctx)
	is.NoErr(err)
}

func TestSource_LifecycleOnDeleted_Skip(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, _ := newTestSource(ctx, t, ctrl)

	// assume that no config was active before, in that case deleted event
	// should be skipped
	src.Instance.LastActiveConfig = Config{}

	err := src.OnDelete(ctx)
	is.NoErr(err)
}

func TestSource_Ack_Deadlock(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, sourceMock := newTestSource(ctx, t, ctrl)

	stream := expectSourceOpen(src, sourceMock)
	sourceMock.EXPECT().LifecycleOnCreated(
		gomock.Any(),
		pconnector.SourceLifecycleOnCreatedRequest{Config: src.Instance.Config.Settings},
	).Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil)

	err := src.Open(ctx)
	is.NoErr(err)

	const msgs = 5
	for i := 0; i < msgs; i++ {
		go func() {
			err := src.Ack(ctx, []opencdc.Position{opencdc.Position("test-pos")})
			is.NoErr(err)
		}()
	}

	serverStream := stream.Server()
	for i := 0; i < msgs; i++ {
		resp, err := serverStream.Recv()
		is.NoErr(err)
		is.Equal(resp.AckPositions, []opencdc.Position{opencdc.Position("test-pos")})
	}
}

// TestSource_Ack_DeferredUntilDurablyFlushed is the sev-0 fix's core unit-level
// regression test (Approach A, docs/design-documents/
// 20260723-source-ack-persist-ordering-fix.md): the plugin must NOT observe
// an ack before the resulting position has been durably flushed by the
// persister, and MUST observe it once the flush completes. This pins
// invariant 1 at the pkg/connector level, independent of the chaos harness's
// subprocess/SIGKILL mechanics (tests/chaos).
func TestSource_Ack_DeferredUntilDurablyFlushed(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	logger := log.Nop()
	db := &inmemory.DB{}
	// A high bundle-count threshold means the ack below stays batched (not
	// auto-flushed) until the fake clock is advanced past the delay
	// threshold - giving this test explicit, deterministic control over
	// when the durable flush (and therefore the deferred ack) happens.
	persister := NewPersister(logger, db, DefaultPersisterDelayThreshold, 100)
	clk := newFakeClock()
	persister.clock = clk

	src, sourceMock := newTestSourceWithPersister(ctx, t, ctrl, persister)
	stream := expectSourceOpen(src, sourceMock)
	sourceMock.EXPECT().LifecycleOnCreated(
		gomock.Any(),
		pconnector.SourceLifecycleOnCreatedRequest{Config: src.Instance.Config.Settings},
	).Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil)

	is.NoErr(src.Open(ctx))

	is.NoErr(src.Ack(ctx, []opencdc.Position{opencdc.Position("test-pos")}))

	serverStream := stream.Server()
	recvDone := make(chan struct{})
	var recvErr error
	go func() {
		defer close(recvDone)
		_, recvErr = serverStream.Recv()
	}()

	select {
	case <-recvDone:
		t.Fatal("invariant 1 violated: plugin observed the ack before the position was durably flushed")
	case <-time.After(50 * time.Millisecond):
		// Expected: nothing delivered yet, the batch is still sitting in the
		// persister waiting for the delay threshold (or a forced Flush).
	}

	// Advancing the fake clock past the delay threshold triggers the flush
	// synchronously firing any due timers (see fakeClock.Advance's doc in
	// persister_test.go) - the resulting durable write's callback
	// (onPersistFlushed) is what sends the deferred ack.
	clk.Advance(DefaultPersisterDelayThreshold + time.Millisecond)

	select {
	case <-recvDone:
		is.NoErr(recvErr)
	case <-time.After(5 * time.Second):
		t.Fatal("plugin never observed the ack after the flush completed")
	}
}

// TestSource_OnPersistFlushed_OutOfOrderCompletionStillDeliversInOrder pins
// onPersistFlushed's core safety property directly (see its doc comment):
// connector.Persister's flush callbacks can complete out of order relative to
// the Ack calls that registered them (a later-registered flush's transaction
// can finish before an earlier one still in flight). Regardless of which
// order the callbacks fire in, the plugin must see every position exactly
// once, strictly in the order Ack originally queued them (invariant 4) -
// never a gap, never a double-send.
func TestSource_OnPersistFlushed_OutOfOrderCompletionStillDeliversInOrder(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, sourceMock := newTestSource(ctx, t, ctrl)
	stream := expectSourceOpen(src, sourceMock)
	sourceMock.EXPECT().LifecycleOnCreated(
		gomock.Any(),
		pconnector.SourceLifecycleOnCreatedRequest{Config: src.Instance.Config.Settings},
	).Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil)
	is.NoErr(src.Open(ctx))

	// Seed three pending acks directly (bypassing Ack/Persist, which would
	// race an automatic flush given newTestSource's bundleCountThreshold=1)
	// to precisely control the seq each carries.
	src.ackMu.Lock()
	src.pendingAcks = []pendingAck{
		{seq: 1, positions: []opencdc.Position{opencdc.Position("pos-1")}},
		{seq: 2, positions: []opencdc.Position{opencdc.Position("pos-2")}},
		{seq: 3, positions: []opencdc.Position{opencdc.Position("pos-3")}},
	}
	src.nextAckSeq = 3
	src.ackMu.Unlock()

	// Simulate the highest seq's flush completing FIRST (out of order): this
	// must drain all three and deliver them, in order. Under Approach A2
	// (docs/design-documents/20260728-snapshot-handoff-deferred-ack-deadlock.md)
	// onPersistFlushed no longer sends inline — it enqueues the drained
	// positions onto the deferredAckQueue (in FIFO order) and the dedicated
	// per-source delivery goroutine (started in Open) performs the sends. So
	// the ordering guarantee this test pins is now: whatever order the flush
	// callbacks fire in, the delivery goroutine sends every position exactly
	// once, strictly in the order Ack queued them. onPersistFlushed itself
	// returns fast (it does not block on a send), so the `go` is no longer
	// required for liveness, but is kept to exercise it running concurrently
	// with the Recv calls exactly as a real persister callback would.
	go src.onPersistFlushed(3, nil)

	serverStream := stream.Server()
	for _, want := range []opencdc.Position{opencdc.Position("pos-1"), opencdc.Position("pos-2"), opencdc.Position("pos-3")} {
		resp, err := serverStream.Recv()
		is.NoErr(err)
		is.Equal(resp.AckPositions, []opencdc.Position{want})
	}

	// The earlier-registered flushes' callbacks arriving late must be safe
	// no-ops: nothing left to send, no double-send.
	src.onPersistFlushed(1, nil)
	src.onPersistFlushed(2, nil)

	src.ackMu.Lock()
	remaining := len(src.pendingAcks)
	durableSeq := src.durableAckSeq
	src.ackMu.Unlock()
	is.Equal(remaining, 0)
	is.Equal(durableSeq, uint64(3))
}

// TestSource_Teardown_SendsPendingDeferredAckBeforeReturning pins invariant 7
// (graceful shutdown must not drop the final ack) at the pkg/connector level:
// Teardown must not return until any ack still pending at the time it was
// called has actually been sent to the plugin. Without this, StopAndWait's
// existing WaitPersisted call (which runs strictly after node/connector
// teardown) would have nothing left to wait for - see Teardown's doc comment.
func TestSource_Teardown_SendsPendingDeferredAckBeforeReturning(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	// This test proves invariant 7 (Teardown drains the deferred ack before
	// returning) DETERMINISTICALLY, without racing a receiver goroutine's
	// scheduling against a non-blocking check. It does so by gating the flush -
	// the only thing that sends the deferred ack - on a blockingDB the test
	// controls: while the flush is blocked, Teardown must NOT return and the ack
	// must NOT be sent; only once the flush is unblocked does the ack go out and
	// Teardown return. A regression that let Teardown return WITHOUT waiting for
	// the flush would return (or let the async flush send the ack) while the
	// flush is still blocked here, tripping one of the "still blocked" asserts -
	// which a naive bounded-wait-after-Teardown check would instead silently
	// mask, since the async flush would deliver the ack a few microseconds late.
	logger := log.Nop()
	unblock := make(chan struct{})
	db := &blockingDB{DB: &inmemory.DB{}, unblock: unblock}
	// A long delay threshold and high bundle-count threshold mean the ack below
	// is never auto-flushed - the only flush is Teardown's own forced Flush
	// call, which blockingDB gates on `unblock`.
	persister := NewPersister(logger, db, time.Hour, 100)

	src, sourceMock := newTestSourceWithPersister(ctx, t, ctrl, persister)
	// Generous flush timeout so the bounded-wait fallback (the stuck-flush path,
	// covered by TestSource_Teardown_BoundedWaitOnStuckFlush) never fires here -
	// we unblock the flush well within it, exercising the wait-then-succeed path.
	src.teardownFlushTimeout = 10 * time.Second
	stream := expectSourceOpen(src, sourceMock)
	sourceMock.EXPECT().LifecycleOnCreated(
		gomock.Any(),
		pconnector.SourceLifecycleOnCreatedRequest{Config: src.Instance.Config.Settings},
	).Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil)
	sourceMock.EXPECT().Teardown(gomock.Any(), pconnector.SourceTeardownRequest{}).
		Return(pconnector.SourceTeardownResponse{}, nil)

	is.NoErr(src.Open(ctx))
	is.NoErr(src.Ack(ctx, []opencdc.Position{opencdc.Position("final-pos")}))

	serverStream := stream.Server()
	recvDone := make(chan opencdc.Position, 1)
	go func() {
		resp, err := serverStream.Recv()
		is.NoErr(err)
		recvDone <- resp.AckPositions[0]
	}()

	teardownErr := make(chan error, 1)
	go func() { teardownErr <- src.Teardown(ctx) }()

	// While the flush is stuck, Teardown must be blocked waiting for it, and the
	// ack must not have been sent. This is the deterministic anti-regression
	// assertion: Teardown genuinely WAITS for the drain.
	select {
	case err := <-teardownErr:
		t.Fatalf("Teardown returned before the deferred-ack flush completed (err=%v) - it did not wait to drain the ack", err)
	case pos := <-recvDone:
		t.Fatalf("deferred ack (%s) was sent before the flush was even allowed to complete - it bypassed the persister", pos)
	case <-time.After(200 * time.Millisecond):
		// Correct: Teardown is blocked waiting for the (still-stuck) flush.
	}

	// Let the flush complete: onPersistFlushed drains the pending ack and sends
	// it, WaitPendingWritesContext returns, Teardown returns.
	close(unblock)

	select {
	case pos := <-recvDone:
		is.Equal(pos, opencdc.Position("final-pos")) // the deferred ack was delivered by the flush Teardown waited for
	case <-time.After(5 * time.Second):
		t.Fatal("deferred ack was never delivered after the flush completed")
	}
	select {
	case err := <-teardownErr:
		is.NoErr(err) // Teardown returned cleanly, after the ack went out
	case <-time.After(5 * time.Second):
		t.Fatal("Teardown did not return after the flush completed")
	}
}

// blockingDB wraps a real database.DB and makes NewTransaction block until
// unblock is closed (or ctx is done), whichever happens first. It exists to
// deterministically simulate a stuck/slow persister flush (e.g. a stalled
// disk or a badger compaction pause) for
// TestSource_Teardown_BoundedWaitOnStuckFlush, without relying on a real
// sleep racing against the assertion.
type blockingDB struct {
	database.DB
	unblock chan struct{}
}

func (b *blockingDB) NewTransaction(ctx context.Context, update bool) (database.Transaction, context.Context, error) {
	select {
	case <-b.unblock:
	case <-ctx.Done():
		return nil, ctx, ctx.Err()
	}
	return b.DB.NewTransaction(ctx, update)
}

// TestSource_Teardown_BoundedWaitOnStuckFlush is the regression test for the
// bounded-Teardown fix (source.go's Teardown, persister.go's
// WaitPendingWritesContext): a stuck/slow persister flush must not hang
// graceful shutdown. Before that fix, Teardown had no bound at all on this
// wait, so a stalled disk (or a badger compaction pause) mid-flush would
// have hung Teardown indefinitely - trading the sev-0 ack-before-persist bug
// for a hang-on-shutdown bug. See Teardown's doc comment, "Failure mode:
// graceful shutdown racing a stuck/slow flush", and
// docs/design-documents/20260723-source-ack-persist-ordering-fix.md.
//
// blockingDB below never unblocks the in-flight flush until this test
// itself is done asserting, which is what makes "Teardown returned quickly"
// proof of the bounded-wait fallback firing rather than a coincidence: the
// only other way Teardown could return here is the underlying flush
// actually completing, and it structurally cannot until this test closes
// the channel.
func TestSource_Teardown_BoundedWaitOnStuckFlush(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	var logBuf bytes.Buffer
	logger := log.New(zerolog.New(&logBuf))

	unblock := make(chan struct{})
	// Let the stuck flush actually finish once the test is done asserting,
	// so its background goroutine (persister.go's flushNow) doesn't leak
	// past this test.
	defer close(unblock)

	db := &blockingDB{DB: &inmemory.DB{}, unblock: unblock}
	// A long delay threshold and high bundle-count threshold mean the ack
	// below is never auto-flushed - the only flush is Teardown's own forced
	// Flush call, which blockingDB then stalls forever (for the lifetime of
	// this test), simulating a stuck store.
	persister := NewPersister(logger, db, time.Hour, 100)

	src, sourceMock := newTestSourceWithPersister(ctx, t, ctrl, persister)
	// newTestSourceWithPersister wires up its own Nop logger on the
	// Instance; replace it so this test can observe the bounded-wait
	// warning Teardown logs on timeout.
	src.Instance.logger = logger
	// Short override so this test doesn't have to wait
	// DefaultTeardownFlushTimeout (10s) for the bounded-wait fallback to
	// kick in - see the teardownFlushTimeout field doc.
	const shortTimeout = 30 * time.Millisecond
	src.teardownFlushTimeout = shortTimeout

	_ = expectSourceOpen(src, sourceMock)
	sourceMock.EXPECT().LifecycleOnCreated(
		gomock.Any(),
		pconnector.SourceLifecycleOnCreatedRequest{Config: src.Instance.Config.Settings},
	).Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil)
	sourceMock.EXPECT().Teardown(gomock.Any(), pconnector.SourceTeardownRequest{}).
		Return(pconnector.SourceTeardownResponse{}, nil)

	is.NoErr(src.Open(ctx))
	is.NoErr(src.Ack(ctx, []opencdc.Position{opencdc.Position("stuck-pos")}))

	start := time.Now()
	err := src.Teardown(ctx)
	elapsed := time.Since(start)

	is.NoErr(err) // Teardown itself must not fail just because the flush wait timed out
	// Comfortably below DefaultTeardownFlushTimeout (10s, let alone "forever"):
	// proves Teardown returned via the bounded-wait fallback, not by
	// coincidentally winning a race against the still-blocked flush.
	is.True(elapsed < 2*time.Second)

	is.True(strings.Contains(logBuf.String(), "timed out waiting for the final flush"))
}

// TestSource_Teardown_FastFlushCompletesWithinBoundedTimeout is the
// fast-path complement to TestSource_Teardown_BoundedWaitOnStuckFlush: the
// same short teardownFlushTimeout override must not truncate a normal,
// quickly-completing flush. Teardown must wait for it and deliver the
// deferred ack, and must NOT log the bounded-wait timeout warning - proving
// the bound only ever fires on an actually-stuck flush, never as a false
// positive against a healthy one.
func TestSource_Teardown_FastFlushCompletesWithinBoundedTimeout(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	var logBuf bytes.Buffer
	logger := log.New(zerolog.New(&logBuf))

	db := &inmemory.DB{}
	// Long delay/high bundle-count thresholds mean the ack below is only
	// flushed by Teardown's own forced Flush call - but, unlike the
	// stuck-flush test above, this store is not blocked, so that flush
	// completes almost immediately.
	persister := NewPersister(logger, db, time.Hour, 100)

	src, sourceMock := newTestSourceWithPersister(ctx, t, ctrl, persister)
	src.Instance.logger = logger
	// Same short override as the stuck-flush test, to prove it's the
	// stuck-ness (not the timeout value) that determines which path fires.
	src.teardownFlushTimeout = 200 * time.Millisecond

	stream := expectSourceOpen(src, sourceMock)
	sourceMock.EXPECT().LifecycleOnCreated(
		gomock.Any(),
		pconnector.SourceLifecycleOnCreatedRequest{Config: src.Instance.Config.Settings},
	).Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil)
	sourceMock.EXPECT().Teardown(gomock.Any(), pconnector.SourceTeardownRequest{}).
		Return(pconnector.SourceTeardownResponse{}, nil)

	is.NoErr(src.Open(ctx))
	is.NoErr(src.Ack(ctx, []opencdc.Position{opencdc.Position("fast-pos")}))

	serverStream := stream.Server()
	recvDone := make(chan opencdc.Position, 1)
	go func() {
		resp, err := serverStream.Recv()
		is.NoErr(err)
		recvDone <- resp.AckPositions[0]
	}()

	is.NoErr(src.Teardown(ctx))

	// The deferred ack must be delivered. A bounded wait (not a non-blocking
	// check) accommodates the receiver goroutine's scheduling without flaking;
	// nothing else in this test sends this ack (delayThreshold=time.Hour,
	// bundleCountThreshold=100, so no auto-flush), so its arrival proves
	// Teardown's forced flush delivered it. The strictly-before-return
	// invariant-7 guarantee is proven deterministically by
	// TestSource_Teardown_SendsPendingDeferredAckBeforeReturning; this case's
	// distinct job is that the SHORT teardownFlushTimeout did not falsely
	// truncate a healthy fast flush (asserted by the no-timeout-log check below).
	select {
	case pos := <-recvDone:
		is.Equal(pos, opencdc.Position("fast-pos"))
	case <-time.After(5 * time.Second):
		t.Fatal("Teardown returned without the pending deferred ack ever being sent")
	}

	is.True(!strings.Contains(logBuf.String(), "timed out waiting"))
}

// faultySourceStream wraps a real source-run stream client and injects a
// bounded number of transient Send failures before delegating to the real
// stream. It exists to deterministically reproduce the natural, timing-
// dependent bug the snapshot-handoff fix targets (a transient stream.Send
// failure for a deferred ack), which the pre-A2 code logged-and-dropped. Recv
// and every other method delegate to the embedded client unchanged.
type faultySourceStream struct {
	pconnector.SourceRunStreamClient

	mu        sync.Mutex
	failsLeft int
	failErr   error
	failCount int
}

func (f *faultySourceStream) Send(req pconnector.SourceRunRequest) error {
	f.mu.Lock()
	if f.failsLeft > 0 {
		f.failsLeft--
		f.failCount++
		f.mu.Unlock()
		return f.failErr
	}
	f.mu.Unlock()
	return f.SourceRunStreamClient.Send(req)
}

func (f *faultySourceStream) failures() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failCount
}

// TestSource_DeferredAck_TransientSendFailure_EventuallyDelivered is the core
// regression test for the snapshot→CDC handoff deadlock
// (docs/design-documents/20260728-snapshot-handoff-deferred-ack-deadlock.md,
// Approach A2). It is the deterministic analogue of the natural, load-
// dependent bug: it fault-injects transient stream.Send failures for the first
// N attempts of a deferred ack WHILE THE PLUGIN IS RUNNING, and asserts the
// ack is EVENTUALLY delivered (retried), never dropped.
//
// Pre-A2, sendDeferredAck logged-and-dropped the first failure. For a snapshot-
// gating source (Postgres) that emits no further records until it receives the
// snapshot-boundary ack, that drop is a permanent handoff deadlock plus silent
// loss of all post-snapshot CDC (invariant 3) — not the benign no-op the old
// code assumed. This test would fail (Recv would block until the timeout)
// against the pre-A2 drop-on-failure behavior, and passes with A2's retry.
func TestSource_DeferredAck_TransientSendFailure_EventuallyDelivered(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	// bundleCountThreshold=1 (newTestSource) means the Ack below is flushed
	// immediately (asynchronously), so onPersistFlushed enqueues the deferred
	// ack for the delivery goroutine without needing a fake clock.
	src, sourceMock := newTestSource(ctx, t, ctrl)
	stream := expectSourceOpen(src, sourceMock)
	sourceMock.EXPECT().LifecycleOnCreated(
		gomock.Any(),
		pconnector.SourceLifecycleOnCreatedRequest{Config: src.Instance.Config.Settings},
	).Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil)
	sourceMock.EXPECT().Teardown(gomock.Any(), pconnector.SourceTeardownRequest{}).
		Return(pconnector.SourceTeardownResponse{}, nil)

	is.NoErr(src.Open(ctx))

	// Wrap the (now open) stream so the delivery goroutine's first N sends of
	// the deferred ack fail transiently. Set BEFORE the Ack that triggers
	// delivery; no delivery is in flight yet (no ack queued), and the Ack ->
	// enqueue -> signal -> goroutine-read chain establishes a happens-before
	// edge over this write, so there is no race (verified under -race).
	const transientFailures = 4
	faulty := &faultySourceStream{
		SourceRunStreamClient: src.stream,
		failsLeft:             transientFailures,
		failErr:               cerrors.New("transient send failure"),
	}
	src.stream = faulty
	// Retry generously and back off ~instantly so the test is fast and
	// deterministic (test-only overrides, mirroring teardownFlushTimeout).
	src.deferredAckMaxRetries = 100
	src.deferredAckBackoffCap = time.Millisecond

	is.NoErr(src.Ack(ctx, []opencdc.Position{opencdc.Position("boundary-pos")}))

	serverStream := stream.Server()
	recvDone := make(chan opencdc.Position, 1)
	go func() {
		// The delivery goroutine retries past the injected failures; the send
		// that finally succeeds pairs with this Recv. If the ack were dropped
		// (the pre-A2 bug), this Recv would block forever.
		resp, err := serverStream.Recv()
		is.NoErr(err)
		recvDone <- resp.AckPositions[0]
	}()

	select {
	case pos := <-recvDone:
		is.Equal(pos, opencdc.Position("boundary-pos")) // eventually delivered, not dropped
	case <-time.After(10 * time.Second):
		t.Fatal("deferred ack was never delivered despite retries — the snapshot-handoff deadlock regressed")
	}
	// All injected failures were retried through, not dropped: the successful
	// send happens strictly after them in the single delivery goroutine, so by
	// the time recvDone fired the count is settled.
	is.Equal(faulty.failures(), transientFailures)

	// Clean up the delivery goroutine.
	is.NoErr(src.Teardown(ctx))
}

// TestSource_DeferredAck_PersistentSendFailure_EscalatesViaErrs pins the other
// half of Approach A2's contract: a deferred-ack send that NEVER recovers while
// the plugin is running must fail LOUDLY (escalate via errs) once retries are
// exhausted — not silently drop (which for a snapshot-gating source is a silent
// deadlock) and not hang. Escalation is safe while running because the node is
// reading errs; it is suppressed only during teardown (see deliverOneAck).
func TestSource_DeferredAck_PersistentSendFailure_EscalatesViaErrs(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, sourceMock := newTestSource(ctx, t, ctrl)
	_ = expectSourceOpen(src, sourceMock)
	sourceMock.EXPECT().LifecycleOnCreated(
		gomock.Any(),
		pconnector.SourceLifecycleOnCreatedRequest{Config: src.Instance.Config.Settings},
	).Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil)
	sourceMock.EXPECT().Teardown(gomock.Any(), pconnector.SourceTeardownRequest{}).
		Return(pconnector.SourceTeardownResponse{}, nil)

	is.NoErr(src.Open(ctx))

	wantErr := cerrors.New("permanent send failure")
	faulty := &faultySourceStream{
		SourceRunStreamClient: src.stream,
		failsLeft:             1 << 30, // effectively always fail
		failErr:               wantErr,
	}
	src.stream = faulty
	// Small retry bound + near-instant backoff: exhaust quickly, then escalate.
	src.deferredAckMaxRetries = 3
	src.deferredAckBackoffCap = time.Millisecond

	is.NoErr(src.Ack(ctx, []opencdc.Position{opencdc.Position("doomed-pos")}))

	// Read errs as the node would while the plugin is running. A regression
	// that dropped the exhausted ack (or hung) would leave this blocked.
	select {
	case err := <-src.Errors():
		is.True(cerrors.Is(err, wantErr))
	case <-time.After(10 * time.Second):
		t.Fatal("a persistent deferred-ack send failure while running was never escalated via errs — it was silently dropped or hung")
	}

	// After escalation the connector is on its way down; tear it down. The
	// stream is still failing, so the bounded drain can't deliver — that is
	// fine (bounded, benign). A short teardown timeout keeps the test fast.
	src.teardownFlushTimeout = 100 * time.Millisecond
	is.NoErr(src.Teardown(ctx))
}

func newTestSource(ctx context.Context, t testing.TB, ctrl *gomock.Controller) (*Source, *mock.SourcePlugin) {
	logger := log.Nop()
	db := &inmemory.DB{}
	// bundleCountThreshold=1 means every Persist call hits the threshold and
	// triggers an immediate (though still asynchronous) flush - tests using
	// this helper don't exercise the debounce window itself. Tests that do
	// (e.g. TestSource_Ack_DeferredUntilDurablyFlushed) build their own
	// persister via newTestSourceWithPersister instead.
	persister := NewPersister(logger, db, DefaultPersisterDelayThreshold, 1)
	return newTestSourceWithPersister(ctx, t, ctrl, persister)
}

func newTestSourceWithPersister(ctx context.Context, t testing.TB, ctrl *gomock.Controller, persister *Persister) (*Source, *mock.SourcePlugin) {
	is := is.New(t)
	logger := log.Nop()

	instance := &Instance{
		ID:   "test-connector-id",
		Type: TypeSource,
		Config: Config{
			Name: "test-name",
			Settings: map[string]string{
				"foo": "bar",
			},
		},
		PipelineID:    "test-pipeline-id",
		Plugin:        "test-plugin",
		ProvisionedBy: ProvisionTypeAPI,
	}
	instance.Init(logger, persister)

	sourceMock := mock.NewSourcePlugin(ctrl)
	pluginDispenser := mock.NewDispenser(ctrl)
	pluginDispenser.EXPECT().DispenseSource().Return(sourceMock, nil).AnyTimes()

	conn, err := instance.Connector(ctx, fakePluginFetcher{instance.Plugin: pluginDispenser})
	is.NoErr(err)
	src, ok := conn.(*Source)
	is.True(ok)
	return src, sourceMock
}

func expectSourceOpen(src *Source, sourceMock *mock.SourcePlugin) *builtin.InMemorySourceRunStream {
	stream := &builtin.InMemorySourceRunStream{}

	sourceMock.EXPECT().Configure(
		gomock.Any(),
		pconnector.SourceConfigureRequest{
			Config: src.Instance.Config.Settings,
		},
	).Return(pconnector.SourceConfigureResponse{}, nil)
	sourceMock.EXPECT().Open(gomock.Any(), pconnector.SourceOpenRequest{}).Return(pconnector.SourceOpenResponse{}, nil)
	sourceMock.EXPECT().NewStream().Return(stream)
	sourceMock.EXPECT().Run(gomock.Any(), stream).DoAndReturn(func(ctx context.Context, _ pconnector.SourceRunStream) error {
		stream.Init(ctx)
		return nil
	})

	return stream
}
