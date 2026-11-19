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

package orchestrator

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// failingRemoveConnector is a real pipeline.Service whose RemoveConnector
// fails, so ConnectorOrchestrator.Delete fails after the connector record was
// deleted and has to roll back.
type failingRemoveConnector struct {
	*pipeline.Service
	err error
}

// fakeDispenserFetcher returns dispenser for any plugin.
type fakeDispenserFetcher struct {
	dispenser connectorPlugin.Dispenser
}

func (f fakeDispenserFetcher) NewDispenser(log.CtxLogger, string, string) (connectorPlugin.Dispenser, error) {
	return f.dispenser, nil
}

func (f failingRemoveConnector) RemoveConnector(context.Context, string, string) (*pipeline.Instance, error) {
	return nil, f.err
}

// TestConnectorOrchestrator_Delete_RollbackRestoresInstance is the #3137
// regression test. When a step after connectors.Delete failed, the rollback
// used to call connectors.Create with the old ID, type, plugin and config.
// The transaction was discarded, so the store kept the original record, but
// in memory the connector became a fresh Instance until the next restart.
//
// The rollback must put back the same instance, position included. What
// happens next depends on whether Delete already ran the plugin's delete
// hook:
//
//   - The hook did not run (the connector never started, so there was no
//     "deleted" event to deliver): the connector starts normally.
//   - The hook ran: the plugin may have released what the position points
//     into. The Postgres connector drops its replication slot; resuming would
//     recreate the slot at the current WAL and skip the gap, and clearing the
//     position is no safer (a Kafka source without one starts at the end).
//     The connector fails closed: building it for a start returns
//     connector.delete_hook_ran, and the position stays intact (invariant 3).
func TestConnectorOrchestrator_Delete_RollbackRestoresInstance(t *testing.T) {
	testCases := []struct {
		name    string
		started bool // the connector has a LastActiveConfig, so the hook runs
	}{
		{name: "delete hook did not run, starts normally", started: false},
		{name: "delete hook ran, start fails closed", started: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			logger := log.Nop()
			db := &inmemory.DB{}
			ctrl := gomock.NewController(t)

			plsReal := pipeline.NewService(logger, db)
			conns := connector.NewService(logger, db, nil)

			pl, err := plsReal.Create(ctx, uuid.NewString(), pipeline.Config{Name: "p-" + uuid.NewString()}, pipeline.ProvisionTypeAPI)
			is.NoErr(err)
			pl.SetStatus(pipeline.StatusUserStopped)

			cfg := connector.Config{Name: "src", Settings: map[string]string{"table": "orders"}}
			conn, err := conns.Create(ctx, uuid.NewString(), connector.TypeSource, "test-plugin", pl.ID, cfg, connector.ProvisionTypeAPI)
			is.NoErr(err)
			_, err = plsReal.AddConnector(ctx, pl.ID, conn.ID)
			is.NoErr(err)

			// The connector has a source position (and, if it started, a
			// last active config). SetState persists the whole instance.
			if tc.started {
				conn.LastActiveConfig = cfg
			}
			position := connector.SourceState{Position: opencdc.Position("orders:42")}
			_, err = conns.SetState(ctx, conn.ID, position)
			is.NoErr(err)

			dispenser := pmock.NewDispenser(ctrl)
			if tc.started {
				// connectors.Delete runs the hook: the plugin is dispensed and
				// told the connector is deleted.
				src := pmock.NewSourcePlugin(ctrl)
				src.EXPECT().LifecycleOnDeleted(gomock.Any(), gomock.Any()).Return(pconnector.SourceLifecycleOnDeletedResponse{}, nil)
				src.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.SourceTeardownResponse{}, nil)
				dispenser.EXPECT().DispenseSource().Return(src, nil)
			}

			_, _, procsMock, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)
			if tc.started {
				// A connector that never started created nothing outside
				// Conduit, so the delete dispenses no plugin at all (#3058).
				connPluginMock.EXPECT().NewDispenser(gomock.Any(), conn.Plugin, conn.ID).Return(dispenser, nil)
			}
			expectMutation(t, lifecycleMock, pl.ID, true)

			wantErr := cerrors.New("could not remove the connector from the pipeline")
			pls := failingRemoveConnector{Service: plsReal, err: wantErr}
			orc := NewOrchestrator(db, logger, pls, conns, procsMock, connPluginMock, procPluginMock, lifecycleMock)

			err = orc.Connectors.Delete(ctx, conn.ID)
			is.True(cerrors.Is(err, wantErr))

			got, err := conns.Get(ctx, conn.ID)
			is.NoErr(err)
			is.True(got == conn) // the same instance, not a fresh one
			is.True(got.CreatedAt.Equal(conn.CreatedAt))
			is.Equal(got.State, position) // never cleared
			if tc.started {
				is.Equal(got.LastActiveConfig, cfg)
			}

			// The next start builds the connector (lifecycle calls Connector).
			c, err := got.Connector(ctx, fakeDispenserFetcher{dispenser: pmock.NewDispenser(ctrl)})
			if tc.started {
				is.True(c == nil)
				ce, ok := conduiterr.Get(err)
				is.True(ok)
				is.Equal(ce.Code.Reason(), connector.CodeConnectorDeleteHookRan.Reason())
				is.Equal(got.State, position)
			} else {
				is.NoErr(err)
				is.True(c != nil)
			}

			// The discarded transaction kept the record: a restart still has
			// the connector, with its position. The fail-closed mark is in
			// memory only, so a restart loads it unmarked (see the PR's
			// failure-mode analysis).
			restarted := connector.NewService(logger, db, nil)
			is.NoErr(restarted.Init(ctx))
			stored, err := restarted.Get(ctx, conn.ID)
			is.NoErr(err)
			is.Equal(stored.Config, conn.Config)
			is.Equal(stored.PipelineID, conn.PipelineID)

			// The pipeline still lists the connector (the AddConnector rollback).
			gotPl, err := plsReal.Get(ctx, pl.ID)
			is.NoErr(err)
			is.Equal(gotPl.ConnectorIDs, []string{conn.ID})
		})
	}
}

// errDiscardedTxn stands in for badger's ErrDiscardedTxn.
var errDiscardedTxn = cerrors.New("transaction has been discarded, please create a new one")

// commitFailDB is a database.DB that mimics the two badger behaviours
// inmemory.DB does not have, and which together turn a failed commit into a
// panic:
//
//   - Commit discards the transaction even when it returns an error
//     (badger/v4 txn.go: `defer txn.Discard()` at the top of Commit).
//   - A write issued through a discarded transaction fails with
//     ErrDiscardedTxn instead of being staged (badger/v4 txn.go modify()).
//
// inmemory.DB's Txn.Discard is a no-op and its set never fails, so a rollback
// callback that writes through an already-committed transaction succeeds
// there and a test built on it passes whether or not the bug is fixed.
type commitFailDB struct {
	mu        sync.Mutex
	values    map[string][]byte
	commitErr error // when set, Commit fails (and still discards)
}

var _ database.DB = (*commitFailDB)(nil)

func newCommitFailDB() *commitFailDB {
	return &commitFailDB{values: make(map[string][]byte)}
}

// failCommit makes every subsequent Commit fail with err.
func (d *commitFailDB) failCommit(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.commitErr = err
}

func (d *commitFailDB) NewTransaction(ctx context.Context, _ bool) (database.Transaction, context.Context, error) {
	t := &commitFailTxn{db: d, changes: make(map[string][]byte)}
	return t, database.ContextWithTransaction(ctx, t), nil
}

func (d *commitFailDB) Close() error               { return nil }
func (d *commitFailDB) Ping(context.Context) error { return nil }

func (d *commitFailDB) Set(ctx context.Context, key string, value []byte) error {
	if txn := d.txn(ctx); txn != nil {
		return txn.set(key, value)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.write(key, value)
	return nil
}

func (d *commitFailDB) Get(ctx context.Context, key string) ([]byte, error) {
	if txn := d.txn(ctx); txn != nil {
		return txn.get(key)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.read(key)
}

func (d *commitFailDB) GetKeys(ctx context.Context, prefix string) ([]string, error) {
	if txn := d.txn(ctx); txn != nil && txn.isDiscarded() {
		return nil, errDiscardedTxn
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var keys []string
	for k := range d.values {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (d *commitFailDB) txn(ctx context.Context) *commitFailTxn {
	t, ok := database.TransactionFromContext(ctx).(*commitFailTxn)
	if !ok {
		return nil
	}
	return t
}

// write stores or deletes key. It must be called with d.mu held.
func (d *commitFailDB) write(key string, value []byte) {
	if value == nil {
		delete(d.values, key)
		return
	}
	d.values[key] = value
}

// read returns key's value. It must be called with d.mu held.
func (d *commitFailDB) read(key string) ([]byte, error) {
	v, ok := d.values[key]
	if !ok {
		return nil, database.ErrKeyNotExist
	}
	return v, nil
}

type commitFailTxn struct {
	db *commitFailDB

	mu        sync.Mutex
	changes   map[string][]byte
	deletes   map[string]bool
	discarded bool
}

var _ database.Transaction = (*commitFailTxn)(nil)

func (t *commitFailTxn) Commit() error {
	// badger discards the transaction whatever Commit returns.
	defer t.Discard()

	t.db.mu.Lock()
	defer t.db.mu.Unlock()
	if t.db.commitErr != nil {
		return t.db.commitErr
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, v := range t.changes {
		t.db.write(k, v)
	}
	for k := range t.deletes {
		t.db.write(k, nil)
	}
	return nil
}

func (t *commitFailTxn) Discard() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.discarded = true
}

func (t *commitFailTxn) isDiscarded() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.discarded
}

func (t *commitFailTxn) set(key string, value []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.discarded {
		return errDiscardedTxn
	}
	if value == nil {
		if t.deletes == nil {
			t.deletes = make(map[string]bool)
		}
		t.deletes[key] = true
		delete(t.changes, key)
		return nil
	}
	t.changes[key] = value
	delete(t.deletes, key)
	return nil
}

func (t *commitFailTxn) get(key string) ([]byte, error) {
	t.mu.Lock()
	if t.discarded {
		t.mu.Unlock()
		return nil, errDiscardedTxn
	}
	if t.deletes[key] {
		t.mu.Unlock()
		return nil, database.ErrKeyNotExist
	}
	if v, ok := t.changes[key]; ok {
		t.mu.Unlock()
		return v, nil
	}
	t.mu.Unlock()

	t.db.mu.Lock()
	defer t.db.mu.Unlock()
	return t.db.read(key)
}

// TestConnectorOrchestrator_Delete_CommitFailureRollsBack covers the rollback
// path that runs after txn.Commit() itself fails. It is not the same path as a
// failing RemoveConnector: by the time it runs, the transaction is gone.
//
// rollback.R.Execute runs callbacks LIFO, so the pipeline rollback runs first
// and txn.Discard last. Commit has already discarded the transaction, so a
// rollback callback that issues a store write through ctx's transaction gets
// ErrDiscardedTxn back; the callback returns the error and MustExecute turns it
// into a panic inside a deferred call — an ordinary API connector delete whose
// commit fails would take down the process.
//
// The rollback must restore the pipeline's connector list in memory only and
// leave the store to txn.Discard, the way connectors.Restore already does.
func TestConnectorOrchestrator_Delete_CommitFailureRollsBack(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := newCommitFailDB()

	pls := pipeline.NewService(logger, db)
	conns := connector.NewService(logger, db, nil)

	pl, err := pls.Create(ctx, uuid.NewString(), pipeline.Config{Name: "p-" + uuid.NewString()}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)
	pl.SetStatus(pipeline.StatusUserStopped)

	cfg := connector.Config{Name: "src", Settings: map[string]string{"table": "orders"}}
	conn, err := conns.Create(ctx, uuid.NewString(), connector.TypeSource, "test-plugin", pl.ID, cfg, connector.ProvisionTypeAPI)
	is.NoErr(err)
	// A second connector, so the restored list has to keep its order and not
	// just its length.
	other, err := conns.Create(ctx, uuid.NewString(), connector.TypeDestination, "test-plugin", pl.ID, cfg, connector.ProvisionTypeAPI)
	is.NoErr(err)
	_, err = pls.AddConnector(ctx, pl.ID, conn.ID)
	is.NoErr(err)
	_, err = pls.AddConnector(ctx, pl.ID, other.ID)
	is.NoErr(err)

	position := connector.SourceState{Position: opencdc.Position("orders:42")}
	_, err = conns.SetState(ctx, conn.ID, position)
	is.NoErr(err)

	wantIDs := []string{conn.ID, other.ID}
	wantUpdatedAt := pl.UpdatedAt

	_, _, procsMock, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)
	// The connector never started, so Instance.Close takes its never-started
	// shortcut (#3369) and dispenses no plugin at all: there is no running
	// plugin to tear down and no delete hook to run. No NewDispenser call is
	// expected, which is what keeps a connector with a missing plugin binary
	// deletable.
	expectMutation(t, lifecycleMock, pl.ID, true)

	wantErr := cerrors.New("commit refused: conflict")
	db.failCommit(wantErr)

	orc := NewOrchestrator(db, logger, pls, conns, procsMock, connPluginMock, procPluginMock, lifecycleMock)

	err = orc.Connectors.Delete(ctx, conn.ID)
	is.True(cerrors.Is(err, wantErr))

	// The connector is back in memory, the same instance, position intact.
	got, err := conns.Get(ctx, conn.ID)
	is.NoErr(err)
	is.True(got == conn)
	is.Equal(got.State, position)

	// The pipeline's connector list is back, in order, and so is UpdatedAt.
	gotPl, err := pls.Get(ctx, pl.ID)
	is.NoErr(err)
	is.Equal(gotPl.ConnectorIDs, wantIDs)
	is.True(gotPl.UpdatedAt.Equal(wantUpdatedAt))

	// The discarded transaction left the store as it was: a restart finds the
	// connector and a pipeline that still lists it.
	restartedConns := connector.NewService(logger, db, nil)
	is.NoErr(restartedConns.Init(ctx))
	storedConn, err := restartedConns.Get(ctx, conn.ID)
	is.NoErr(err)
	is.Equal(storedConn.PipelineID, pl.ID)

	restartedPls := pipeline.NewService(logger, db)
	is.NoErr(restartedPls.Init(ctx))
	storedPl, err := restartedPls.Get(ctx, pl.ID)
	is.NoErr(err)
	is.Equal(storedPl.ConnectorIDs, wantIDs)
}
