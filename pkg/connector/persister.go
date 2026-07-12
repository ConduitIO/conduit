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
	"time"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

const (
	DefaultPersisterDelayThreshold       = time.Second
	DefaultPersisterBundleCountThreshold = 10000
)

// Persister is responsible for persisting connectors and their state when
// certain thresholds are met.
type Persister struct {
	logger log.CtxLogger
	db     database.DB
	store  *Store

	delayThreshold       time.Duration
	bundleCountThreshold int

	// clock abstracts time so tests can control the passage of time
	// deterministically instead of relying on real sleeps and timing
	// tolerances. NewPersister sets this to a realClock; tests in this
	// package may swap it for a fakeClock before exercising delay-based
	// behavior.
	clock clock

	connWg sync.WaitGroup

	// m guards all private variables below it.
	m           sync.Mutex
	bundleCount int
	batch       map[string]persistData
	flushTimer  stoppableTimer
	flushWg     sync.WaitGroup
}

// clock abstracts the two time operations the persister needs in order to
// debounce flushes: reading the current time and scheduling a callback after
// a delay. It exists purely to make the delay-threshold behavior
// deterministically testable; NewPersister always wires up a realClock.
type clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) stoppableTimer
}

// stoppableTimer is the subset of *time.Timer's API the persister relies on.
type stoppableTimer interface {
	// Stop prevents the timer from firing, matching the semantics of
	// *time.Timer.Stop: it returns true if the call stops the timer, false
	// if the timer has already expired or been stopped.
	Stop() bool
}

// realClock is the production clock implementation, backed directly by the
// time package.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) AfterFunc(d time.Duration, f func()) stoppableTimer {
	return time.AfterFunc(d, f)
}

// PersistCallback is a function that's called when a connector is persisted.
type PersistCallback func(error)

type persistData struct {
	callback  PersistCallback
	storeFunc func(context.Context) error
}

// NewPersister creates a new persister that stores data into the supplied
// database when the thresholds are met.
func NewPersister(
	logger log.CtxLogger,
	db database.DB,
	delayThreshold time.Duration,
	bundleCountThreshold int,
) *Persister {
	return &Persister{
		logger: logger.WithComponent("connector.Persister"),
		db:     db,
		// persister should never retrieve data, the store does not need a builder
		store: NewStore(db, logger),

		delayThreshold:       delayThreshold,
		bundleCountThreshold: bundleCountThreshold,

		clock: realClock{},
	}
}

// ConnectorStarted increases the number of connector this persister is
// persisting. As long as at least one connector is started the Wait function
// will block, so connectors have to make sure to call ConnectorStopped.
func (p *Persister) ConnectorStarted() {
	p.connWg.Add(1)
}

// ConnectorStopped triggers one last flush and decreases the number of
// connectors this persister is persisting. Once all connectors are stopped the
// Wait function stops blocking.
func (p *Persister) ConnectorStopped() {
	p.m.Lock()
	defer p.m.Unlock()
	p.triggerFlush(context.Background())
	p.connWg.Done()
}

// Persist signals the persister that a connector state changed and it should be
// persisted with the next batch. This function will collect all changed
// connectors until either the number of detected changes reaches the configured
// threshold or the configured delay is reached (whichever comes first), then
// the connectors are flushed and a new batch starts to be collected.
func (p *Persister) Persist(ctx context.Context, conn *Instance, callback PersistCallback) error {
	p.m.Lock()
	defer p.m.Unlock()

	p.logger.Trace(ctx).
		Str(log.ConnectorIDField, conn.ID).
		Msg("adding connector to next persist batch")
	if p.batch == nil {
		p.batch = make(map[string]persistData)
	}

	storeFunc, err := p.store.PrepareSet(conn.ID, conn)
	if err != nil {
		return cerrors.Errorf("failed to prepare connector for persistence: %w", err)
	}
	p.batch[conn.ID] = persistData{
		callback:  callback,
		storeFunc: storeFunc,
	}
	p.bundleCount++

	if p.bundleCount == p.bundleCountThreshold {
		p.logger.Trace(ctx).Msg("reached bundle count threshold")
		p.triggerFlush(context.Background()) // use a new context because action happens in background
		return nil
	}

	if p.flushTimer == nil {
		p.flushTimer = p.clock.AfterFunc(p.delayThreshold, func() {
			p.Flush(context.Background()) // use a new context because action happens in background
		})
	}
	return nil
}

// Wait waits for all connectors to stop running and for the last flush to be executed.
func (p *Persister) Wait() {
	p.connWg.Wait()
	p.flushWg.Wait()
}

// WaitPendingWrites blocks until every flush already triggered (via Flush, the
// bundle-count threshold, the delay timer, or ConnectorStopped) has finished
// writing to the store — but, unlike Wait, it does NOT block on connWg (every
// connector across the whole process reaching ConnectorStopped).
//
// This distinction matters for a caller that only wants to know "has this
// pipeline's already-triggered write actually landed durably", not "has every
// connector in the process stopped running": since the persister's batching is
// shared across all pipelines, connWg only reaches zero once every connector
// on every pipeline has stopped, so calling Wait from a single pipeline's
// stop-and-drain path would deadlock for as long as any other pipeline stays
// running. WaitPendingWrites has no such coupling — it only observes flushWg,
// which a connector's own ConnectorStopped call already increments
// synchronously (see triggerFlush) before that call returns. A caller that
// calls WaitPendingWrites strictly after learning (e.g. via a WaitGroup/tomb
// join) that ConnectorStopped has already been called for the connector it
// cares about is guaranteed to observe that connector's flush complete: the
// Add(1) happened-before the Wait() call by construction, and sync.WaitGroup
// cannot miss a Done that was already pending when Wait was entered.
//
// Used by lifecycle.Service.StopAndWait to await durability (invariant 1/3)
// after a pipeline has fully drained, without deadlocking on unrelated running
// pipelines. See docs/design-documents/20260708-live-server-deploy-apply.md,
// "Review outcome & required rework", blocker 1.
func (p *Persister) WaitPendingWrites() {
	p.flushWg.Wait()
}

// Flush will trigger a goroutine that persists any in-memory data to the store.
// To wait for the changes to be actually persisted you need to call Wait.
func (p *Persister) Flush(ctx context.Context) {
	p.m.Lock()
	defer p.m.Unlock()
	p.triggerFlush(ctx)
}

// triggerFlush expects to hold the lock already.
func (p *Persister) triggerFlush(ctx context.Context) {
	p.logger.Trace(ctx).Msg("triggering flush")
	if p.flushTimer != nil {
		p.flushTimer.Stop()
		p.flushTimer = nil
	}
	if p.batch == nil {
		return
	}

	// wait for any running flusher to finish
	p.flushWg.Wait()

	// reset callbacks and bundle count
	batch := p.batch
	p.batch = nil
	p.bundleCount = 0

	p.flushWg.Add(1)
	go p.flushNow(ctx, batch)
}

// flushNow will flush the state to the store.
func (p *Persister) flushNow(ctx context.Context, batch map[string]persistData) {
	defer p.flushWg.Done()
	start := p.clock.Now()

	tx, ctx, err := p.db.NewTransaction(ctx, true)
	if err != nil {
		// TODO make sure error is propagated back to the runtime and Conduit shuts down
		p.logger.Err(ctx, err).Msg("error creating new transaction")
		return
	}

	defer tx.Discard()
	for id, data := range batch {
		err := data.storeFunc(ctx)
		if err != nil {
			p.logger.Err(ctx, err).
				Str(log.ConnectorIDField, id).
				Msg("error while saving connector")
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	for _, data := range batch {
		// execute callbacks in go routines to make sure they can't block this function
		go data.callback(err)
	}

	p.logger.Debug(ctx).
		Err(err).
		Int("count", len(batch)).
		Dur(log.DurationField, p.clock.Now().Sub(start)).
		Msg("persisted connectors")
}
