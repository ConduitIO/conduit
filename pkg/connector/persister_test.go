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
	"context"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

func TestPersister_EmptyFlushDoesNothing(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	persister, store := initPersisterTest(time.Millisecond*100, 2)

	persister.Flush(ctx)
	persister.Wait()

	got, err := store.GetAll(ctx)
	is.NoErr(err)
	is.Equal(0, len(got))
}

func TestPersister_PersistFlushesAfterDelayThreshold(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	delayThreshold := time.Millisecond * 100

	persister, store := initPersisterTest(delayThreshold, 2)

	conn := &Instance{ID: uuid.NewString(), Type: TypeDestination}
	callbackCalled := make(chan struct{})
	persistAt := time.Now()
	err := persister.Persist(ctx, conn, func(err error) {
		if err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}
		close(callbackCalled)
	})
	is.NoErr(err)

	// we are testing a delay which is not exact, this is the acceptable margin
	maxDelay := delayThreshold + time.Millisecond*10
	select {
	case <-callbackCalled:
		if gotDelay := time.Since(persistAt); gotDelay < delayThreshold || gotDelay > maxDelay {
			t.Fatalf("flush delay should be between %s and %s, actual delay: %s", delayThreshold, maxDelay, gotDelay)
		}
	case <-time.After(maxDelay):
		t.Fatalf("expected callback to be called in a certain time frame")
	}

	got, err := store.Get(ctx, conn.ID)
	is.NoErr(err)
	is.Equal(conn, got)
}

func TestPersister_PersistFlushesAfterBundleCountThreshold(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	bundleCountThreshold := 50

	persister, store := initPersisterTest(time.Second, bundleCountThreshold)

	allCallbacksCalled := make(chan struct{})
	var wgCallbacks sync.WaitGroup
	wgCallbacks.Add(bundleCountThreshold / 2)
	go func() {
		wgCallbacks.Wait()
		close(allCallbacksCalled)
	}()

	for i := 0; i < bundleCountThreshold/2; i++ {
		conn := &Instance{ID: uuid.NewString(), Type: TypeDestination}
		err := persister.Persist(ctx, conn, func(err error) {
			t.Fatal("expected callback to be overwritten!")
		})
		is.NoErr(err)
		// second persist will overwrite first callback
		err = persister.Persist(ctx, conn, func(err error) {
			if err != nil {
				t.Fatalf("expected nil error, got: %v", err)
			}
			wgCallbacks.Done()
		})
		is.NoErr(err)
	}
	lastPersistAt := time.Now()

	// we are testing a delay which is not exact, this is the acceptable margin
	maxDelay := time.Millisecond * 100
	select {
	case <-allCallbacksCalled:
		if gotDelay := time.Since(lastPersistAt); gotDelay > maxDelay {
			t.Fatalf("flush delay should be less than %s, actual delay: %s", maxDelay, gotDelay)
		}
	case <-time.After(maxDelay):
		t.Fatalf("expected callbacks to be called in %s", maxDelay)
	}

	conns, err := store.GetAll(ctx)
	is.NoErr(err)
	is.Equal(bundleCountThreshold/2, len(conns))
}

func TestPersister_FlushStoresRightAway(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	persister, store := initPersisterTest(time.Millisecond*100, 2)

	conn := &Instance{ID: uuid.NewString(), Type: TypeDestination}
	callbackCalled := make(chan struct{})
	timeAtPersist := time.Now()
	err := persister.Persist(ctx, conn, func(err error) {
		if err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}
		close(callbackCalled)
	})
	is.NoErr(err)

	// flush right away
	persister.Flush(ctx)
	persister.Wait()

	// we are testing a delay which is not exact, this is the acceptable margin
	maxDelay := time.Millisecond * 10
	select {
	case <-callbackCalled:
		if gotDelay := time.Since(timeAtPersist); gotDelay > maxDelay {
			t.Fatalf("flush delay should be less than %s, actual delay: %s", maxDelay, gotDelay)
		}
	case <-time.After(maxDelay):
		t.Fatalf("expected callback to be called in a certain time frame")
	}

	got, err := store.Get(ctx, conn.ID)
	is.NoErr(err)
	is.Equal(conn, got)
}

func TestPersister_WaitsForOpenConnectorsAndFlush(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	persister, _ := initPersisterTest(time.Millisecond*100, 2)

	conn := &Instance{ID: uuid.NewString(), Type: TypeDestination}
	persister.ConnectorStarted()
	persister.ConnectorStarted()
	persister.ConnectorStarted()

	timeAtStart := time.Now()
	delay := time.Millisecond * 100
	go func() {
		time.Sleep(delay)
		persister.ConnectorStopped()
		persister.ConnectorStopped()
		// before last stop we persist another change which should be flushed
		// automatically when the connector is stopped
		err := persister.Persist(ctx, conn, func(err error) {})
		is.NoErr(err)
		persister.ConnectorStopped()
	}()

	persister.Wait()

	// we are testing a delay which is not exact, this is the acceptable margin
	maxDelay := delay + time.Millisecond*10
	if gotDelay := time.Since(timeAtStart); gotDelay > maxDelay {
		t.Fatalf("wait delay should be between %s and %s, actual delay: %s", delay, maxDelay, gotDelay)
	}
}

func initPersisterTest(
	delayThreshold time.Duration,
	bundleCountThreshold int,
) (*Persister, *Store) {
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}

	persister := NewPersister(logger, db, delayThreshold, bundleCountThreshold)
	return persister, NewStore(db, logger)
}
