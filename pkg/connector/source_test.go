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
	// must drain and send all three, in order. onPersistFlushed sends
	// synchronously (that's what lets Persister.WaitPendingWrites prove the
	// send happened, see its doc), so it must run concurrently with the
	// Recv calls below rather than before them, or it would block forever
	// waiting for a reader on the first send.
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

	// A long delay threshold and high bundle-count threshold mean the ack
	// below would NOT be auto-flushed for the lifetime of this test - the
	// only thing that can flush it is Teardown's own forced Flush call.
	logger := log.Nop()
	db := &inmemory.DB{}
	persister := NewPersister(logger, db, time.Hour, 100)

	src, sourceMock := newTestSourceWithPersister(ctx, t, ctrl, persister)
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
	recvDone := make(chan opencdc.Position)
	go func() {
		resp, err := serverStream.Recv()
		is.NoErr(err)
		recvDone <- resp.AckPositions[0]
	}()

	is.NoErr(src.Teardown(ctx))

	// Teardown already returned by this point - the ack must already have
	// been delivered, not merely "eventually" delivered on some later,
	// unrelated event.
	select {
	case pos := <-recvDone:
		is.Equal(pos, opencdc.Position("final-pos"))
	default:
		t.Fatal("Teardown returned without the pending deferred ack having been sent")
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

	// Teardown already returned by this point - the ack must already have
	// been delivered (same invariant-7 property as
	// TestSource_Teardown_SendsPendingDeferredAckBeforeReturning), not
	// merely "eventually" delivered on some later, unrelated event.
	select {
	case pos := <-recvDone:
		is.Equal(pos, opencdc.Position("fast-pos"))
	default:
		t.Fatal("Teardown returned without the pending deferred ack having been sent")
	}

	is.True(!strings.Contains(logBuf.String(), "timed out waiting"))
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

	sourceMock.EXPECT().Configure(gomock.Any(),
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
