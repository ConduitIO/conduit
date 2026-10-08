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

package connector

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// Regression tests for ConduitIO/conduit#2925: Persister.flushNow shadowed its
// outer err inside the storeFunc loop, so a per-connector store failure was
// only logged, the transaction committed anyway, and every PersistCallback
// received nil. For a source that callback is onPersistFlushed, which
// releases the deferred plugin ack — an upstream ack for a position that was
// never stored (invariants 1 and 2). See
// docs/postmortems/20261007-persister-store-error-ignored.md.

// faultyStoreDB wraps a real database.DB and injects store failures:
//   - Set fails for any key ending in failID while setArmed is true, the way
//     badger's txn.Set fails with ErrTxnTooBig without poisoning the
//     transaction (the other writes in it stay committable);
//   - NewTransaction fails while txArmed is true.
//
// Both are atomics so a test can arm them after Open without racing the
// persister's flush goroutine.
type faultyStoreDB struct {
	database.DB
	failID   string
	setErr   error
	setArmed atomic.Bool
	txErr    error
	txArmed  atomic.Bool
}

func (f *faultyStoreDB) Set(ctx context.Context, key string, value []byte) error {
	if f.setArmed.Load() && strings.HasSuffix(key, f.failID) {
		return f.setErr
	}
	return f.DB.Set(ctx, key, value)
}

func (f *faultyStoreDB) NewTransaction(ctx context.Context, update bool) (database.Transaction, context.Context, error) {
	if f.txArmed.Load() {
		return nil, ctx, f.txErr
	}
	return f.DB.NewTransaction(ctx, update)
}

// collectCallback returns a PersistCallback that records the error it was
// called with, and a func that waits (bounded) for that call.
func collectCallback(t *testing.T) (PersistCallback, func() error) {
	t.Helper()
	ch := make(chan error, 1)
	return func(err error) { ch <- err }, func() error {
		t.Helper()
		select {
		case err := <-ch:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("PersistCallback was never called")
			return nil
		}
	}
}

// TestPersister_NewTransactionErrorReachesEveryCallback: before the fix,
// flushNow returned early on a NewTransaction error without calling any
// callback and without closing callbacksDone, so WaitPendingWrites (used
// unbounded by lifecycle StopAndWait and Persister.Wait at shutdown) hung
// forever and a source's queued acks were never resolved either way.
func TestPersister_NewTransactionErrorReachesEveryCallback(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	wantErr := cerrors.New("injected transaction failure")
	db := &faultyStoreDB{DB: &inmemory.DB{}, txErr: wantErr}
	db.txArmed.Store(true)
	persister := NewPersister(logger, db, time.Hour, 100)

	cb1, err1 := collectCallback(t)
	cb2, err2 := collectCallback(t)
	is.NoErr(persister.Persist(ctx, &Instance{ID: "c1", Type: TypeSource}, cb1))
	is.NoErr(persister.Persist(ctx, &Instance{ID: "c2", Type: TypeDestination}, cb2))

	persister.Flush(ctx)
	// Must not hang: callbacksDone has to close on this path too.
	is.NoErr(persister.WaitPendingWritesContext(ctx, 5*time.Second))

	is.True(cerrors.Is(err1(), wantErr))
	is.True(cerrors.Is(err2(), wantErr))
}

// TestPersister_StoreErrorThenRecovery: a failed batch must not poison the
// persister — the next batch for the same connector commits normally and
// reports nil.
func TestPersister_StoreErrorThenRecovery(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	inner := &inmemory.DB{}
	db := &faultyStoreDB{DB: inner, failID: "c1", setErr: cerrors.New("transient")}
	db.setArmed.Store(true)
	persister := NewPersister(logger, db, time.Hour, 100)

	conn := &Instance{ID: "c1", Type: TypeSource, State: SourceState{Position: opencdc.Position("p1")}}
	cb, errFn := collectCallback(t)
	is.NoErr(persister.Persist(ctx, conn, cb))
	persister.Flush(ctx)
	is.True(errFn() != nil)

	db.setArmed.Store(false)
	conn.State = SourceState{Position: opencdc.Position("p2")}
	cb, errFn = collectCallback(t)
	is.NoErr(persister.Persist(ctx, conn, cb))
	persister.Flush(ctx)
	is.NoErr(errFn())

	got, err := NewStore(inner, logger).Get(ctx, "c1")
	is.NoErr(err)
	is.Equal(got.State, SourceState{Position: opencdc.Position("p2")})
}

// TestSource_Ack_OwnStoreErrorDoesNotReleasePluginAck is the end-to-end form
// of #2925 for a source: the source's own position write fails in a batch it
// shares with a healthy connector. The deferred plugin ack (#2680) must NOT
// be released, and the failure must reach the pipeline via Errors(). Before
// #2932 the source's callback got nil, the plugin received the ack, and
// nothing was surfaced. Since #2930 the healthy connector is committed in a
// retry; that must not leak a nil to the source.
func TestSource_Ack_OwnStoreErrorDoesNotReleasePluginAck(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	logger := log.Nop()

	wantErr := cerrors.New("injected store failure")
	inner := &inmemory.DB{}
	db := &faultyStoreDB{DB: inner, failID: "test-connector-id", setErr: wantErr}
	// Explicit Flush only (fake clock never advanced), so the source's ack and
	// the healthy connector are guaranteed to share one batch.
	persister := NewPersister(logger, db, DefaultPersisterDelayThreshold, 100)
	persister.clock = newFakeClock()

	src, sourceMock := newTestSourceWithPersister(ctx, t, ctrl, persister)
	stream := expectSourceOpen(src, sourceMock)
	sourceMock.EXPECT().LifecycleOnCreated(gomock.Any(), gomock.Any()).
		Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil)
	sourceMock.EXPECT().Teardown(gomock.Any(), gomock.Any()).
		Return(pconnector.SourceTeardownResponse{}, nil)

	is.NoErr(src.Open(ctx))
	// Settle Open's own lifecycle-event persist before arming the failure.
	persister.Flush(ctx)
	is.NoErr(persister.WaitPendingWritesContext(ctx, 5*time.Second))
	db.setArmed.Store(true)

	otherCb, otherErr := collectCallback(t)
	is.NoErr(persister.Persist(ctx, &Instance{ID: "other-conn", Type: TypeDestination}, otherCb))
	is.NoErr(src.Ack(ctx, []opencdc.Position{opencdc.Position("never-durable")}))

	recv := make(chan pconnector.SourceRunRequest, 1)
	go func() {
		req, err := stream.Server().Recv()
		if err == nil {
			recv <- req
		}
	}()

	persister.Flush(ctx)

	select {
	case err := <-src.Errors():
		is.True(cerrors.Is(err, wantErr))
	case req := <-recv:
		t.Fatalf("invariant 1 violated: plugin received ack %q although the batch holding its position failed to commit", req.AckPositions)
	case <-time.After(3 * time.Second):
		t.Fatal("the failed position write was never surfaced via Errors()")
	}

	// The ack stays unreleased: give the delivery goroutine every chance.
	select {
	case req := <-recv:
		t.Fatalf("invariant 1 violated: plugin received ack %q after a failed flush", req.AckPositions)
	case <-time.After(100 * time.Millisecond):
	}
	// Invariant 2: the position was not stored, so a restart resumes from the
	// last durable position (here: none) and re-reads.
	stored, err := NewStore(inner, logger).Get(ctx, src.Instance.ID)
	is.NoErr(err)
	is.Equal(stored.State, nil)
	// The healthy connector sharing the batch was committed.
	is.NoErr(otherErr())

	src.teardownFlushTimeout = 500 * time.Millisecond
	is.NoErr(src.Teardown(ctx))
}

// TestSource_Teardown_FinalFlushStoreErrorIsReturned: the graceful-shutdown
// form. Teardown forces the final flush; if that write fails, the final ack
// must not reach the plugin, Teardown must return the error instead of
// reporting a clean stop, and nothing may hang (nobody reads Errors() during
// teardown, so the source must not block its persist callback on it).
func TestSource_Teardown_FinalFlushStoreErrorIsReturned(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	logger := log.Nop()

	wantErr := cerrors.New("injected store failure")
	db := &faultyStoreDB{DB: &inmemory.DB{}, failID: "test-connector-id", setErr: wantErr}
	persister := NewPersister(logger, db, DefaultPersisterDelayThreshold, 100)
	persister.clock = newFakeClock()

	src, sourceMock := newTestSourceWithPersister(ctx, t, ctrl, persister)
	stream := expectSourceOpen(src, sourceMock)
	sourceMock.EXPECT().LifecycleOnCreated(gomock.Any(), gomock.Any()).
		Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil)
	sourceMock.EXPECT().Teardown(gomock.Any(), gomock.Any()).
		Return(pconnector.SourceTeardownResponse{}, nil)

	is.NoErr(src.Open(ctx))
	persister.Flush(ctx)
	is.NoErr(persister.WaitPendingWritesContext(ctx, 5*time.Second))

	db.setArmed.Store(true)
	is.NoErr(src.Ack(ctx, []opencdc.Position{opencdc.Position("final-pos")}))

	var (
		mu       sync.Mutex
		received []pconnector.SourceRunRequest
	)
	recvDone := make(chan struct{})
	go func() {
		defer close(recvDone)
		for {
			req, err := stream.Server().Recv()
			if err != nil {
				return // stream closed by Teardown
			}
			mu.Lock()
			received = append(received, req)
			mu.Unlock()
		}
	}()

	src.teardownFlushTimeout = 2 * time.Second
	done := make(chan error, 1)
	go func() { done <- src.Teardown(ctx) }()

	select {
	case err := <-done:
		is.True(cerrors.Is(err, wantErr)) // a failed final write is not a clean stop
	case <-time.After(5 * time.Second):
		t.Fatal("Teardown hung on a failed final flush")
	}
	<-recvDone
	mu.Lock()
	defer mu.Unlock()
	is.Equal(len(received), 0) // invariant 1: no ack for the undurable final position

	// The persister must not be wedged either: WaitPendingWrites returns.
	is.NoErr(persister.WaitPendingWritesContext(ctx, 2*time.Second))
}
