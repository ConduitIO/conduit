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

package connector_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/connector/mock"
	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

func TestPersister_EmptyFlushDoesNothing(t *testing.T) {
	ctx := context.Background()
	persister, store, _ := initPersisterTest(
		gomock.NewController(t),
		time.Millisecond*100,
		2,
	)

	persister.Flush(ctx)
	persister.Wait()

	got, err := store.GetAll(ctx)
	assert.Ok(t, err)
	assert.Equal(t, 0, len(got))
}

func TestPersister_PersistFlushesAfterDelayThreshold(t *testing.T) {
	ctx := context.Background()
	delayThreshold := time.Millisecond * 100

	persister, store, builder := initPersisterTest(
		gomock.NewController(t),
		delayThreshold,
		2,
	)

	destination := builder.NewDestinationMock(uuid.NewString(), connector.Config{})
	callbackCalled := make(chan struct{})
	persistAt := time.Now()
	persister.Persist(ctx, destination, func(err error) {
		if err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}
		close(callbackCalled)
	})

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

	conn, err := store.Get(ctx, destination.ID())
	assert.Ok(t, err)
	assert.Equal(t, destination, conn)
}

func TestPersister_PersistFlushesAfterBundleCountThreshold(t *testing.T) {
	ctx := context.Background()
	bundleCountThreshold := 50

	persister, store, builder := initPersisterTest(
		gomock.NewController(t),
		time.Second,
		bundleCountThreshold,
	)

	allCallbacksCalled := make(chan struct{})
	var wgCallbacks sync.WaitGroup
	wgCallbacks.Add(bundleCountThreshold / 2)
	go func() {
		wgCallbacks.Wait()
		close(allCallbacksCalled)
	}()

	for i := 0; i < bundleCountThreshold/2; i++ {
		destination := builder.NewDestinationMock(uuid.NewString(), connector.Config{})
		persister.Persist(ctx, destination, func(err error) {
			t.Fatal("expected callback to be overwritten!")
		})
		// second persist will overwrite first callback
		persister.Persist(ctx, destination, func(err error) {
			if err != nil {
				t.Fatalf("expected nil error, got: %v", err)
			}
			wgCallbacks.Done()
		})
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
	assert.Ok(t, err)
	assert.Equal(t, bundleCountThreshold/2, len(conns))
}

func TestPersister_FlushStoresRightAway(t *testing.T) {
	ctx := context.Background()
	persister, store, builder := initPersisterTest(
		gomock.NewController(t),
		time.Millisecond*100,
		2,
	)

	destination := builder.NewDestinationMock(uuid.NewString(), connector.Config{})
	callbackCalled := make(chan struct{})
	timeAtPersist := time.Now()
	persister.Persist(ctx, destination, func(err error) {
		if err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}
		close(callbackCalled)
	})

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

	conn, err := store.Get(ctx, destination.ID())
	assert.Ok(t, err)
	assert.Equal(t, destination, conn)
}

func TestPersister_WaitsForOpenConnectorsAndFlush(t *testing.T) {
	ctx := context.Background()
	persister, _, builder := initPersisterTest(
		gomock.NewController(t),
		time.Millisecond*100,
		2,
	)

	destination := builder.NewDestinationMock(uuid.NewString(), connector.Config{})
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
		persister.Persist(ctx, destination, func(err error) {})
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
	ctrl *gomock.Controller,
	delayThreshold time.Duration,
	bundleCountThreshold int,
) (*connector.Persister, *connector.Store, mock.Builder) {
	logger := log.New(zerolog.Nop())
	db := &inmemory.DB{}
	builder := mock.Builder{Ctrl: ctrl, SkipValidate: true}

	persister := connector.NewPersister(logger, db, delayThreshold, bundleCountThreshold)
	return persister, connector.NewStore(db, logger, builder), builder
}
