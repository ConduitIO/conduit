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
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-commons/database/badger"
	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/database/sqlite"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

// Regression tests for #2930 item 1: after #2932 a single connector's failed
// write failed every connector in the persister batch, including connectors
// of other pipelines sharing the process-wide persister. Only the connector
// whose write failed may get the error; the rest must commit and get nil.

// TestPersister_StoreErrorFailsOnlyThatConnector: one connector's write fails
// in a batch of three. The other two are committed in a fresh transaction and
// told so; the failing one gets its own error and nothing of it is stored.
func TestPersister_StoreErrorFailsOnlyThatConnector(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	wantErr := cerrors.New("injected store failure")
	inner := &inmemory.DB{}
	db := &faultyStoreDB{DB: inner, failID: "bad-conn", setErr: wantErr}
	db.setArmed.Store(true)
	persister := NewPersister(logger, db, time.Hour, 100)

	conns := []*Instance{
		{ID: "good-1", Type: TypeSource, State: SourceState{Position: opencdc.Position("p1")}},
		{ID: "bad-conn", Type: TypeSource, State: SourceState{Position: opencdc.Position("p2")}},
		{ID: "good-2", Type: TypeDestination, State: DestinationState{Positions: map[string]opencdc.Position{"s": opencdc.Position("p3")}}},
	}
	waits := map[string]func() error{}
	for _, c := range conns {
		cb, wait := collectCallback(t)
		waits[c.ID] = wait
		is.NoErr(persister.Persist(ctx, c, cb))
	}

	persister.Flush(ctx)
	is.NoErr(persister.WaitPendingWritesContext(ctx, 5*time.Second))

	is.NoErr(waits["good-1"]())                       // committed, told so
	is.NoErr(waits["good-2"]())                       // committed, told so
	is.True(cerrors.Is(waits["bad-conn"](), wantErr)) // only the failing connector gets the error

	store := NewStore(inner, logger)
	got, err := store.GetAll(ctx)
	is.NoErr(err)
	is.Equal(len(got), 2)
	_, err = store.Get(ctx, "bad-conn")
	is.True(cerrors.Is(err, database.ErrKeyNotExist)) // nothing of the failed connector landed
}

// abortingDB models Postgres (and SQLite for most errors): after one
// statement fails, the transaction is aborted, every later statement in it
// fails with "current transaction is aborted", and Commit fails. Writes of
// failID fail while armed.
type abortingDB struct {
	database.DB
	failID string
	armed  atomic.Bool

	mu      sync.Mutex
	aborted map[database.Transaction]bool
	txns    int
}

type abortingTxn struct {
	database.Transaction
	db *abortingDB
}

func (t *abortingTxn) Commit() error {
	t.db.mu.Lock()
	aborted := t.db.aborted[t]
	t.db.mu.Unlock()
	if aborted {
		return cerrors.New("commit on aborted transaction")
	}
	return t.Transaction.Commit()
}

type abortingTxnKey struct{}

func (a *abortingDB) NewTransaction(ctx context.Context, update bool) (database.Transaction, context.Context, error) {
	tx, txCtx, err := a.DB.NewTransaction(ctx, update)
	if err != nil {
		return nil, ctx, err
	}
	wrapped := &abortingTxn{Transaction: tx, db: a}
	a.mu.Lock()
	a.txns++
	a.mu.Unlock()
	return wrapped, context.WithValue(txCtx, abortingTxnKey{}, wrapped), nil
}

func (a *abortingDB) Set(ctx context.Context, key string, value []byte) error {
	tx, _ := ctx.Value(abortingTxnKey{}).(*abortingTxn)
	a.mu.Lock()
	defer a.mu.Unlock()
	if tx != nil && a.aborted[tx] {
		return cerrors.New("current transaction is aborted, commands ignored until end of transaction block")
	}
	if a.armed.Load() && strings.HasSuffix(key, a.failID) {
		if tx != nil {
			a.aborted[tx] = true
		}
		return cerrors.Errorf("write of %s failed", a.failID)
	}
	return a.DB.Set(ctx, key, value)
}

func (a *abortingDB) transactions() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.txns
}

// TestPersister_AbortingStore_OnlyFailingConnectorGetsError: on a store that
// aborts the transaction after a failed statement, the connectors written
// after the failure fail too ("transaction is aborted"). They must not be
// blamed for it: each gets the outcome of its own write in the retry.
func TestPersister_AbortingStore_OnlyFailingConnectorGetsError(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()

	// Run with the failing connector in every position of the write order.
	for _, badID := range []string{"c0", "c1", "c2", "c3"} {
		t.Run(badID, func(t *testing.T) {
			is := is.New(t)
			inner := &inmemory.DB{}
			db := &abortingDB{DB: inner, failID: badID, aborted: map[database.Transaction]bool{}}
			db.armed.Store(true)
			persister := NewPersister(logger, db, time.Hour, 100)

			waits := map[string]func() error{}
			for i := range 4 {
				id := fmt.Sprintf("c%d", i)
				cb, wait := collectCallback(t)
				waits[id] = wait
				is.NoErr(persister.Persist(ctx, &Instance{ID: id, Type: TypeSource, State: SourceState{Position: opencdc.Position(id)}}, cb))
			}
			persister.Flush(ctx)
			is.NoErr(persister.WaitPendingWritesContext(ctx, 5*time.Second))

			for id, wait := range waits {
				err := wait()
				if id == badID {
					is.True(err != nil)
					is.True(strings.Contains(err.Error(), "write of "+badID+" failed")) // its own error, not "aborted"
					continue
				}
				if err != nil {
					t.Fatalf("connector %s was failed by another connector's write: %v", id, err)
				}
			}
			got, err := NewStore(inner, logger).GetAll(ctx)
			is.NoErr(err)
			is.Equal(len(got), 3)
			is.Equal(db.transactions(), 2) // one failed attempt, one retry
		})
	}
}

// TestPersister_AllWritesFailEachGetsOwnError: when every write fails, each
// connector gets the error of its own write, and the flush stays bounded:
// each attempt leaves out the connector that failed it, so the flush ends
// after at most one transaction per connector.
func TestPersister_AllWritesFailEachGetsOwnError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	inner := &inmemory.DB{}
	// Every write fails: failID "" matches every key.
	db := &abortingDB{DB: inner, failID: "", aborted: map[database.Transaction]bool{}}
	db.armed.Store(true)
	persister := NewPersister(logger, db, time.Hour, 100)

	const n = 5
	waits := make([]func() error, n)
	for i := range n {
		cb, wait := collectCallback(t)
		waits[i] = wait
		is.NoErr(persister.Persist(ctx, &Instance{ID: fmt.Sprintf("c%d", i), Type: TypeSource}, cb))
	}
	persister.Flush(ctx)
	is.NoErr(persister.WaitPendingWritesContext(ctx, 5*time.Second))

	for _, wait := range waits {
		err := wait()
		is.True(err != nil)
		is.True(!strings.Contains(err.Error(), "aborted")) // its own write's error, not a side effect of another
	}
	is.Equal(db.transactions(), n) // bounded: one transaction per connector, no more
	got, err := NewStore(inner, logger).GetAll(ctx)
	is.NoErr(err)
	is.Equal(len(got), 0)
}

// TestPersister_CommitErrorFailsEveryRemainingConnector: a failed Commit is
// not attributable to one connector, so every connector in the attempt gets
// it and nothing is retried.
func TestPersister_CommitErrorFailsEveryRemainingConnector(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	inner := &inmemory.DB{}
	db := &abortingDB{DB: inner, failID: "never", aborted: map[database.Transaction]bool{}}
	persister := NewPersister(logger, &commitFailDB{abortingDB: db}, time.Hour, 100)

	cb1, err1 := collectCallback(t)
	cb2, err2 := collectCallback(t)
	is.NoErr(persister.Persist(ctx, &Instance{ID: "c1", Type: TypeSource}, cb1))
	is.NoErr(persister.Persist(ctx, &Instance{ID: "c2", Type: TypeSource}, cb2))
	persister.Flush(ctx)
	is.NoErr(persister.WaitPendingWritesContext(ctx, 5*time.Second))

	is.True(cerrors.Is(err1(), errInjectedCommit))
	is.True(cerrors.Is(err2(), errInjectedCommit))
	is.Equal(db.transactions(), 1) // a commit failure is not retried
}

var errInjectedCommit = cerrors.New("injected commit failure")

type commitFailDB struct{ *abortingDB }

type commitFailTxn struct{ database.Transaction }

func (commitFailTxn) Commit() error { return errInjectedCommit }

func (c *commitFailDB) NewTransaction(ctx context.Context, update bool) (database.Transaction, context.Context, error) {
	tx, txCtx, err := c.abortingDB.NewTransaction(ctx, update)
	if err != nil {
		return nil, ctx, err
	}
	return commitFailTxn{tx}, txCtx, nil
}

// TestPersister_Badger_TxnTooBigFailsOnlyOverflowConnectors runs #2930's
// scenario on the real default store. Sixteen connectors with ~800 KB of
// state each exceed badger's per-transaction limit (~9.6 MB). Each write that
// crosses the limit fails with ErrTxnTooBig and is left out; the retry commits
// the rest. Every callback gets exactly the outcome of its own write: nil
// means the state is stored, an error means it is not.
func TestPersister_Badger_TxnTooBigFailsOnlyOverflowConnectors(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	db, err := badger.New(zerolog.Nop(), t.TempDir())
	is.NoErr(err)
	t.Cleanup(func() { _ = db.Close() })
	persister := NewPersister(logger, db, time.Hour, 100)

	const conns = 16
	pos := opencdc.Position(bytes.Repeat([]byte{'x'}, 600_000))
	waits := make([]func() error, conns)
	for i := range conns {
		cb, wait := collectCallback(t)
		waits[i] = wait
		is.NoErr(persister.Persist(ctx, &Instance{ID: fmt.Sprintf("big-%02d", i), Type: TypeSource, State: SourceState{Position: pos}}, cb))
	}
	persister.Flush(ctx)
	is.NoErr(persister.WaitPendingWritesContext(ctx, 30*time.Second))

	store := NewStore(db, logger)
	var committed, failed int
	for i, wait := range waits {
		id := fmt.Sprintf("big-%02d", i)
		err := wait()
		_, getErr := store.Get(ctx, id)
		if err == nil {
			committed++
			is.NoErr(getErr) // told it landed, and it did
			continue
		}
		failed++
		is.True(strings.Contains(err.Error(), "Txn is too big"))
		is.True(cerrors.Is(getErr, database.ErrKeyNotExist)) // told it failed, and it is not stored
	}
	is.True(committed > 0) // the rest of the batch is no longer failed with the overflow
	is.True(failed > 0)    // the batch really did exceed the limit
	is.Equal(committed+failed, conns)
}

// TestSource_Ack_OtherConnectorStoreErrorStillReleasesAck is the source-level
// form: another connector in the same batch fails its write, the source's own
// write commits, so the source's deferred ack is released and no error is
// reported to it. The other connector is the only one that gets the error.
func TestSource_Ack_OtherConnectorStoreErrorStillReleasesAck(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	logger := log.Nop()

	wantErr := cerrors.New("injected store failure")
	inner := &inmemory.DB{}
	db := &faultyStoreDB{DB: inner, failID: "other-conn", setErr: wantErr}
	db.setArmed.Store(true)
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

	otherCb, otherErr := collectCallback(t)
	is.NoErr(persister.Persist(ctx, &Instance{ID: "other-conn", Type: TypeDestination}, otherCb))
	is.NoErr(src.Ack(ctx, []opencdc.Position{opencdc.Position("durable-pos")}))

	recv := make(chan pconnector.SourceRunRequest, 1)
	go func() {
		req, err := stream.Server().Recv()
		if err == nil {
			recv <- req
		}
	}()

	persister.Flush(ctx)

	select {
	case req := <-recv:
		is.Equal(req.AckPositions, []opencdc.Position{opencdc.Position("durable-pos")})
	case err := <-src.Errors():
		t.Fatalf("source was failed by another connector's write: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("the source's ack was never released although its write committed")
	}
	is.True(cerrors.Is(otherErr(), wantErr)) // the failing connector still gets its error

	stored, err := NewStore(inner, logger).Get(ctx, src.Instance.ID)
	is.NoErr(err)
	is.Equal(stored.State, SourceState{Position: opencdc.Position("durable-pos")})

	src.teardownFlushTimeout = 500 * time.Millisecond
	is.NoErr(src.Teardown(ctx))
}

// TestPersister_SQLite_FailedWriteFailsOnlyThatConnector runs #2930's scenario
// on a real SQLite store. A trigger rejects the write of one connector, once
// with RAISE(ABORT) (the statement fails, the transaction stays open) and once
// with RAISE(ROLLBACK) (SQLite rolls the whole transaction back, as it also
// does by itself for errors like SQLITE_FULL). In both cases only that
// connector gets the error and the others are committed.
func TestPersister_SQLite_FailedWriteFailsOnlyThatConnector(t *testing.T) {
	for _, raise := range []string{"ABORT", "ROLLBACK"} {
		t.Run(raise, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			logger := log.Nop()

			dir := t.TempDir() // sqlite.New takes a directory and creates conduit.db in it
			db, err := sqlite.New(ctx, zerolog.Nop(), dir, "conduit_kv_store")
			is.NoErr(err)
			t.Cleanup(func() { _ = db.Close() })

			raw, err := sql.Open("sqlite", filepath.Join(dir, "conduit.db"))
			is.NoErr(err)
			t.Cleanup(func() { _ = raw.Close() })
			_, err = raw.ExecContext(ctx, fmt.Sprintf(`
				CREATE TRIGGER reject_bad BEFORE INSERT ON conduit_kv_store
				WHEN NEW.key LIKE '%%bad-conn'
				BEGIN SELECT RAISE(%s, 'injected: write of bad-conn rejected'); END`, raise))
			is.NoErr(err)

			persister := NewPersister(logger, db, time.Hour, 100)
			waits := map[string]func() error{}
			for _, id := range []string{"a-good", "bad-conn", "z-good"} {
				cb, wait := collectCallback(t)
				waits[id] = wait
				is.NoErr(persister.Persist(ctx, &Instance{ID: id, Type: TypeSource, State: SourceState{Position: opencdc.Position(id)}}, cb))
			}
			persister.Flush(ctx)
			is.NoErr(persister.WaitPendingWritesContext(ctx, 10*time.Second))

			is.NoErr(waits["a-good"]())
			is.NoErr(waits["z-good"]())
			badErr := waits["bad-conn"]()
			is.True(badErr != nil)
			is.True(strings.Contains(badErr.Error(), "injected: write of bad-conn rejected"))

			got, err := NewStore(db, logger).GetAll(ctx)
			is.NoErr(err)
			is.Equal(len(got), 2)
			is.Equal(got["a-good"].State, SourceState{Position: opencdc.Position("a-good")})
			is.Equal(got["z-good"].State, SourceState{Position: opencdc.Position("z-good")})
		})
	}
}
