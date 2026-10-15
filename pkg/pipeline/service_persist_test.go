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

package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

// Regression tests for the error-but-landed outcome that context-honouring
// stores introduce (#2964): a write whose context ends returns an error, yet
// may still be applied. abandoningDB models badger's Set: when ctx ends it
// returns ctx's error, but the write still lands later, and a later write to
// the same key waits for it.
type abandoningDB struct {
	inmemory.DB

	mu sync.Mutex
	// hang is how many of the next Set calls hang until release is closed.
	hang int
	// lag is how long every other Set takes to apply.
	lag     time.Duration
	release chan struct{}
	pending chan struct{} // closed when the last hung write has applied
	sawCtx  []context.Context
}

func newAbandoningDB() *abandoningDB {
	return &abandoningDB{release: make(chan struct{})}
}

func (d *abandoningDB) Set(ctx context.Context, key string, value []byte) error {
	d.mu.Lock()
	d.sawCtx = append(d.sawCtx, ctx)
	hang := d.hang > 0
	if hang {
		d.hang--
	}
	prev := d.pending
	applied := make(chan struct{})
	d.pending = applied
	d.mu.Unlock()

	// Like badger's per-key lock: wait, bounded by ctx, for the previous
	// write to land before starting this one.
	if prev != nil {
		select {
		case <-prev:
		case <-ctx.Done():
			close(applied) // this one never starts
			return ctx.Err()
		}
	}

	go func() {
		defer close(applied)
		if hang {
			<-d.release
		} else {
			time.Sleep(d.lag)
		}
		_ = d.DB.Set(context.Background(), key, value)
	}()

	select {
	case <-applied:
		return nil
	case <-ctx.Done():
		return ctx.Err() // abandoned: the goroutine still applies it
	}
}

func (d *abandoningDB) unblock() {
	d.mu.Lock()
	defer d.mu.Unlock()
	select {
	case <-d.release:
	default:
		close(d.release)
	}
}

// settle waits for every write handed to the DB to land.
func (d *abandoningDB) settle(t *testing.T) {
	t.Helper()
	d.mu.Lock()
	p := d.pending
	d.mu.Unlock()
	if p == nil {
		return
	}
	select {
	case <-p:
	case <-time.After(5 * time.Second):
		t.Fatal("writes did not settle")
	}
}

func storeHas(t *testing.T, db database.DB, id string) bool {
	t.Helper()
	_, err := NewStore(db).Get(context.Background(), id)
	if cerrors.Is(err, database.ErrKeyNotExist) {
		return false
	}
	if err != nil {
		t.Fatalf("store get %q: %v", id, err)
	}
	return true
}

// consistent fails the test if memory and the store disagree on whether
// pipeline id exists.
func consistent(t *testing.T, s *Service, db *abandoningDB, id string) {
	t.Helper()
	db.settle(t)
	_, inMemory := s.instances[id]
	inStore := storeHas(t, db, id)
	if inMemory != inStore {
		t.Fatalf("pipeline %q: in memory %v, in store %v; the next boot would disagree with what the API reported", id, inMemory, inStore)
	}
}

// An API client that disconnects mid-create must not turn a write that lands
// into a reported failure.
func TestService_Create_ClientCancelDoesNotSplitMemoryAndStore(t *testing.T) {
	is := is.New(t)
	db := newAbandoningDB()
	db.lag = 50 * time.Millisecond
	s := NewService(log.Nop(), db)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	_, err := s.Create(ctx, "p1", Config{Name: "p1"}, ProvisionTypeAPI)
	is.NoErr(err)
	consistent(t, s, db, "p1")
}

func TestService_Create_RetryAfterAbandonedWrite(t *testing.T) {
	is := is.New(t)
	db := newAbandoningDB()
	db.hang = 1
	t.Cleanup(db.unblock)
	s := NewService(log.Nop(), db)
	s.configWriteTimeout = 50 * time.Millisecond

	// The first attempt hangs past the bound; release it while the retry
	// waits behind it.
	go func() { time.Sleep(70 * time.Millisecond); db.unblock() }()
	_, err := s.Create(context.Background(), "p1", Config{Name: "p1"}, ProvisionTypeAPI)
	is.NoErr(err)
	is.True(storeHas(t, db, "p1"))
	consistent(t, s, db, "p1")
}

func TestService_Create_UnknownOutcomeHasCode(t *testing.T) {
	is := is.New(t)
	db := newAbandoningDB()
	db.hang = 1
	t.Cleanup(db.unblock)
	s := NewService(log.Nop(), db)
	s.configWriteTimeout = 30 * time.Millisecond

	_, err := s.Create(context.Background(), "p1", Config{Name: "p1"}, ProvisionTypeAPI)
	is.True(err != nil)
	cerr, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(cerr.Code, CodeConfigPersistUnknown)
	is.True(cerr.Suggestion != "")
	_, inMemory := s.instances["p1"]
	is.True(!inMemory) // memory unchanged; the store decides at next boot
}

func TestService_Delete_RetryAfterAbandonedWrite(t *testing.T) {
	is := is.New(t)
	db := newAbandoningDB()
	s := NewService(log.Nop(), db)
	_, err := s.Create(context.Background(), "p1", Config{Name: "p1"}, ProvisionTypeAPI)
	is.NoErr(err)

	db.mu.Lock()
	db.hang = 1
	db.mu.Unlock()
	t.Cleanup(db.unblock)
	s.configWriteTimeout = 50 * time.Millisecond
	go func() { time.Sleep(70 * time.Millisecond); db.unblock() }()

	is.NoErr(s.Delete(context.Background(), "p1"))
	consistent(t, s, db, "p1")
	is.True(!storeHas(t, db, "p1"))
}

func TestService_Delete_ClientCancelDoesNotSplitMemoryAndStore(t *testing.T) {
	is := is.New(t)
	db := newAbandoningDB()
	s := NewService(log.Nop(), db)
	_, err := s.Create(context.Background(), "p1", Config{Name: "p1"}, ProvisionTypeAPI)
	is.NoErr(err)

	db.mu.Lock()
	db.lag = 50 * time.Millisecond
	db.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	is.NoErr(s.Delete(ctx, "p1"))
	consistent(t, s, db, "p1")
}

// Inside a transaction the write is only staged, so it runs on the caller's
// context unchanged: no detaching, no timeout.
func TestService_Create_InTransactionUsesCallerContext(t *testing.T) {
	is := is.New(t)
	db := newAbandoningDB()
	s := NewService(log.Nop(), db)

	txn, ctx, err := db.NewTransaction(context.Background(), true)
	is.NoErr(err)
	defer txn.Discard()
	_, err = s.Create(ctx, "p1", Config{Name: "p1"}, ProvisionTypeAPI)
	is.NoErr(err)

	db.mu.Lock()
	defer db.mu.Unlock()
	is.Equal(len(db.sawCtx), 1)
	is.Equal(db.sawCtx[0], ctx)
}
