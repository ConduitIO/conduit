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
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// Tests for persistErrReporter and the paths around it (#2925, review of
// #2932): a persister callback must never block forever on an errs channel
// nobody reads, because a blocked callback keeps its flush's callbacksDone
// open and hangs every unbounded Persister.WaitPendingWrites.

func newTestDestinationWithPersister(ctx context.Context, t *testing.T, ctrl *gomock.Controller, persister *Persister) (*Destination, *mock.DestinationPlugin) {
	t.Helper()
	is := is.New(t)
	instance := &Instance{
		ID:            "test-destination-id",
		Type:          TypeDestination,
		Config:        Config{Name: "test-name", Settings: map[string]string{"foo": "bar"}},
		PipelineID:    "test-pipeline-id",
		Plugin:        "test-plugin",
		ProvisionedBy: ProvisionTypeAPI,
	}
	instance.Init(log.Nop(), persister)

	destinationMock := mock.NewDestinationPlugin(ctrl)
	pluginDispenser := mock.NewDispenser(ctrl)
	pluginDispenser.EXPECT().DispenseDestination().Return(destinationMock, nil).AnyTimes()

	conn, err := instance.Connector(ctx, fakePluginFetcher{instance.Plugin: pluginDispenser})
	is.NoErr(err)
	dest, ok := conn.(*Destination)
	is.True(ok)
	return dest, destinationMock
}

// newFailingWritePersister returns a persister that only flushes on an
// explicit Flush, over a store that fails every write of connector failID.
func newFailingWritePersister(failID string, wantErr error) *Persister {
	db := &faultyStoreDB{DB: &inmemory.DB{}, failID: failID, setErr: wantErr}
	db.setArmed.Store(true)
	persister := NewPersister(log.Nop(), db, DefaultPersisterDelayThreshold, 100)
	persister.clock = newFakeClock()
	return persister
}

// TestPersister_BatchErrorCarriesStableCode: errors are API. The batch
// failure every callback receives carries a stable code and a suggestion.
func TestPersister_BatchErrorCarriesStableCode(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	wantErr := cerrors.New("injected store failure")
	persister := newFailingWritePersister("other-conn", wantErr)

	cb, errFn := collectCallback(t)
	is.NoErr(persister.Persist(ctx, &Instance{ID: "other-conn", Type: TypeSource}, cb))
	persister.Flush(ctx)
	err := errFn()

	var ce *conduiterr.ConduitError
	is.True(cerrors.As(err, &ce))
	is.Equal(ce.Code, CodeConnectorStatePersistFailed)
	is.True(ce.Suggestion != "")
	is.True(cerrors.Is(err, wantErr))
}

// TestDestination_PersistFailure_NobodyReadingErrs_DoesNotWedgePersister: a
// destination's Open lifecycle-event persist fails, so its callback gets an
// error. Nobody reads Errors() (arch-v2 always, v1 once its node loop has
// ended). The callback must not block forever: Teardown releases it,
// WaitPendingWrites returns, and Teardown reports the failure.
func TestDestination_PersistFailure_NobodyReadingErrs_DoesNotWedgePersister(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	wantErr := cerrors.New("injected store failure")
	persister := newFailingWritePersister("test-destination-id", wantErr)
	dest, destMock := newTestDestinationWithPersister(ctx, t, ctrl, persister)
	_ = expectDestinationOpen(dest, destMock)
	destMock.EXPECT().LifecycleOnCreated(gomock.Any(), gomock.Any()).
		Return(pconnector.DestinationLifecycleOnCreatedResponse{}, nil)
	destMock.EXPECT().Teardown(gomock.Any(), gomock.Any()).
		Return(pconnector.DestinationTeardownResponse{}, nil)

	is.NoErr(dest.Open(ctx)) // registers the lifecycle-event persist
	is.NoErr(persister.Persist(ctx, &Instance{ID: "other-conn", Type: TypeSource}, func(error) {}))
	persister.Flush(ctx)

	// Whether the callback reports before or after Teardown reads the pending
	// error depends on scheduling, so Teardown's return value is not asserted
	// here. What must hold either way: nothing hangs and the error is kept.
	done := make(chan error, 1)
	go func() { done <- dest.Teardown(ctx) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("destination Teardown hung")
	}
	// Before the fix: context deadline exceeded, the callback blocked forever
	// on d.errs and held callbacksDone open.
	is.NoErr(persister.WaitPendingWritesContext(ctx, 2*time.Second))

	dest.pendingMu.Lock()
	defer dest.pendingMu.Unlock()
	is.True(cerrors.Is(dest.pendingPersistErr, wantErr)) // kept (and logged), not lost
}

// TestDestination_Teardown_ReturnsPersistErrorReportedWhileStopping: a
// persist failure reported once errs is no longer read is returned by
// Teardown, not swallowed.
func TestDestination_Teardown_ReturnsPersistErrorReportedWhileStopping(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	persister := newFailingWritePersister("other-conn", cerrors.New("unused"))
	dest, destMock := newTestDestinationWithPersister(ctx, t, ctrl, persister)
	_ = expectDestinationOpen(dest, destMock)
	destMock.EXPECT().LifecycleOnCreated(gomock.Any(), gomock.Any()).
		Return(pconnector.DestinationLifecycleOnCreatedResponse{}, nil)
	destMock.EXPECT().Teardown(gomock.Any(), gomock.Any()).
		Return(pconnector.DestinationTeardownResponse{}, nil)
	is.NoErr(dest.Open(ctx))

	wantErr := cerrors.New("injected store failure")
	dest.persistErrs.stop()
	dest.reportPersistError(wantErr) // must not block: nobody reads errs
	is.True(cerrors.Is(dest.Teardown(ctx), wantErr))
}

// TestConnector_OpenFailsAfterLifecyclePersist_DoesNotWedgePersister: Open
// registers the lifecycle-event persist, then a later step fails. The node
// never calls Teardown after a failed Open, so if that batch fails the
// callback must not wait for a reader that will never come.
func TestConnector_OpenFailsAfterLifecyclePersist_DoesNotWedgePersister(t *testing.T) {
	openErr := cerrors.New("plugin open failed")

	t.Run("source", func(t *testing.T) {
		is := is.New(t)
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		persister := newFailingWritePersister("test-connector-id", cerrors.New("injected store failure"))
		src, srcMock := newTestSourceWithPersister(ctx, t, ctrl, persister)
		srcMock.EXPECT().Configure(gomock.Any(), gomock.Any()).Return(pconnector.SourceConfigureResponse{}, nil)
		srcMock.EXPECT().LifecycleOnCreated(gomock.Any(), gomock.Any()).
			Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil)
		srcMock.EXPECT().Open(gomock.Any(), gomock.Any()).Return(pconnector.SourceOpenResponse{}, openErr)
		srcMock.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.SourceTeardownResponse{}, nil)

		is.True(cerrors.Is(src.Open(ctx), openErr))
		is.NoErr(persister.Persist(ctx, &Instance{ID: "other-conn", Type: TypeSource}, func(error) {}))
		persister.Flush(ctx)
		is.NoErr(persister.WaitPendingWritesContext(ctx, 2*time.Second))
	})

	t.Run("destination", func(t *testing.T) {
		is := is.New(t)
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		persister := newFailingWritePersister("test-destination-id", cerrors.New("injected store failure"))
		dest, destMock := newTestDestinationWithPersister(ctx, t, ctrl, persister)
		destMock.EXPECT().Configure(gomock.Any(), gomock.Any()).Return(pconnector.DestinationConfigureResponse{}, nil)
		destMock.EXPECT().LifecycleOnCreated(gomock.Any(), gomock.Any()).
			Return(pconnector.DestinationLifecycleOnCreatedResponse{}, nil)
		destMock.EXPECT().Open(gomock.Any(), gomock.Any()).Return(pconnector.DestinationOpenResponse{}, openErr)
		destMock.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.DestinationTeardownResponse{}, nil)

		is.True(cerrors.Is(dest.Open(ctx), openErr))
		is.NoErr(persister.Persist(ctx, &Instance{ID: "other-conn", Type: TypeSource}, func(error) {}))
		persister.Flush(ctx)
		is.NoErr(persister.WaitPendingWritesContext(ctx, 2*time.Second))
	})
}

// TestSource_RecoveredPersistFailure_TeardownIsClean: with nobody reading
// Errors() (arch-v2), a failed flush followed by a successful one that
// stores a later position and releases the acks leaves nothing undurable.
// Teardown must then report a clean stop, not the stale error.
func TestSource_RecoveredPersistFailure_TeardownIsClean(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	db := &faultyStoreDB{DB: &inmemory.DB{}, failID: "test-connector-id", setErr: cerrors.New("transient")}
	persister := NewPersister(log.Nop(), db, DefaultPersisterDelayThreshold, 100)
	persister.clock = newFakeClock()

	src, srcMock := newTestSourceWithPersister(ctx, t, ctrl, persister)
	stream := expectSourceOpen(src, srcMock)
	srcMock.EXPECT().LifecycleOnCreated(gomock.Any(), gomock.Any()).
		Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil)
	srcMock.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.SourceTeardownResponse{}, nil)
	is.NoErr(src.Open(ctx))
	persister.Flush(ctx)
	is.NoErr(persister.WaitPendingWritesContext(ctx, 2*time.Second))

	recv := make(chan []opencdc.Position, 2)
	go func() {
		for {
			req, err := stream.Server().Recv()
			if err != nil {
				return
			}
			recv <- req.AckPositions
		}
	}()

	db.setArmed.Store(true)
	is.NoErr(src.Ack(ctx, []opencdc.Position{opencdc.Position("p1")}))
	persister.Flush(ctx) // fails; its callback now waits on errs, which nobody reads

	db.setArmed.Store(false)
	is.NoErr(src.Ack(ctx, []opencdc.Position{opencdc.Position("p2")}))
	persister.Flush(ctx) // succeeds and covers p1 too
	for _, want := range []string{"p1", "p2"} {
		select {
		case got := <-recv:
			is.Equal(string(got[0]), want)
		case <-time.After(3 * time.Second):
			t.Fatalf("ack %s was not released after the later write landed", want)
		}
	}

	is.NoErr(src.Teardown(ctx)) // the failure was superseded: a clean stop
}

// ctxAwareDB fails NewTransaction on a canceled context, as a network store
// (Postgres) would.
type ctxAwareDB struct{ database.DB }

func (d ctxAwareDB) NewTransaction(ctx context.Context, update bool) (database.Transaction, context.Context, error) {
	if err := ctx.Err(); err != nil {
		return nil, ctx, err
	}
	return d.DB.NewTransaction(ctx, update)
}

// TestSource_Teardown_CanceledCtxDoesNotFailSharedBatch: a force-stop
// cancels the connector context Teardown receives. Teardown's forced flush
// must not use it, or on a ctx-aware store the batch fails and takes every
// other connector in it down too.
func TestSource_Teardown_CanceledCtxDoesNotFailSharedBatch(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	persister := NewPersister(log.Nop(), ctxAwareDB{&inmemory.DB{}}, DefaultPersisterDelayThreshold, 100)
	persister.clock = newFakeClock()
	src, srcMock := newTestSourceWithPersister(ctx, t, ctrl, persister)
	_ = expectSourceOpen(src, srcMock)
	srcMock.EXPECT().LifecycleOnCreated(gomock.Any(), gomock.Any()).
		Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil)
	srcMock.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.SourceTeardownResponse{}, nil)
	is.NoErr(src.Open(ctx))
	persister.Flush(ctx)
	is.NoErr(persister.WaitPendingWritesContext(ctx, 2*time.Second))

	otherCb, otherErr := collectCallback(t)
	is.NoErr(persister.Persist(ctx, &Instance{ID: "other-conn", Type: TypeSource}, otherCb))

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_ = src.Teardown(canceled)
	is.NoErr(otherErr()) // the other connector's write landed despite the force-stop
}

// TestSource_PendingPersistError_SupersededOnlyByCoveringFlush pins the
// supersession rule deterministically: a kept persist error is cleared only
// by a durable flush that reached its seq, never by an older one.
func TestSource_PendingPersistError_SupersededOnlyByCoveringFlush(t *testing.T) {
	for _, tc := range []struct {
		name       string
		durableSeq uint64
		wantErr    bool
	}{
		{name: "covering flush clears it", durableSeq: 5, wantErr: false},
		{name: "older flush does not", durableSeq: 4, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			ctrl := gomock.NewController(t)
			src, srcMock := newTestSource(ctx, t, ctrl)
			_ = expectSourceOpen(src, srcMock)
			srcMock.EXPECT().LifecycleOnCreated(gomock.Any(), gomock.Any()).
				Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil)
			srcMock.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.SourceTeardownResponse{}, nil)
			is.NoErr(src.Open(ctx))
			is.NoErr(src.Instance.persister.WaitPendingWritesContext(ctx, 2*time.Second))

			wantErr := cerrors.New("injected store failure")
			src.persistErrs.stop()
			src.reportPersistError(5, wantErr) // nobody reads errs: kept, must not block
			src.onPersistFlushed(tc.durableSeq, nil)

			err := src.Teardown(ctx)
			is.Equal(cerrors.Is(err, wantErr), tc.wantErr)
		})
	}
}
