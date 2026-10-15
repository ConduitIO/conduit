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
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
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

	// flush is the most recently triggered flush, or nil if none has ever been
	// triggered. Waiters snapshot this pointer under m and then wait on the
	// channels it carries.
	flush *flushState
}

// flushState is one flush generation: the channels reporting when that specific
// flush's store write, and then its callbacks, have completed.
//
// It replaces two reused sync.WaitGroups (flushWg/callbackWg) whose reuse
// panicked the process — see WaitPendingWrites for the full account. A
// WaitGroup forbids a counter-raising Add from running concurrently with a
// Wait, and that rule is unenforceable here: WaitPendingWrites is called by
// connectors on their own goroutines with no coordination with whichever other
// connector happens to trigger a flush at that instant. A per-generation value
// has no such rule — a waiter reads an immutable snapshot, and a new flush
// allocates new channels instead of mutating a counter someone may be waiting
// on.
type flushState struct {
	// writeDone closes when flushNow's store write has finished — the point
	// the old flushWg tracked.
	writeDone chan struct{}
	// callbacksDone closes once every PersistCallback this flush spawned has
	// returned. Distinct from writeDone because flushNow fires callbacks in
	// their own goroutines without waiting for them, so a caller that needs a
	// callback's side effects to have actually happened (not just the store
	// write) — e.g. connector.Source's deferred plugin-ack under Approach A,
	// see source.go's Ack — needs this one. This is what callbackWg tracked.
	callbacksDone chan struct{}
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

// Wait waits for all connectors to stop running and for the last flush
// (including its callbacks — see WaitPendingWrites) to be executed.
func (p *Persister) Wait() {
	p.connWg.Wait()
	p.WaitPendingWrites()
}

// WaitPendingWrites blocks until every flush already triggered (via Flush, the
// bundle-count threshold, the delay timer, or ConnectorStopped) has finished
// writing to the store AND every PersistCallback that flush invoked has
// returned — but, unlike Wait, it does NOT block on connWg (every connector
// across the whole process reaching ConnectorStopped).
//
// This distinction matters for a caller that only wants to know "has this
// pipeline's already-triggered write actually landed durably", not "has every
// connector in the process stopped running": since the persister's batching is
// shared across all pipelines, connWg only reaches zero once every connector
// on every pipeline has stopped, so calling Wait from a single pipeline's
// stop-and-drain path would deadlock for as long as any other pipeline stays
// running. WaitPendingWrites has no such coupling — it only observes the
// current flush generation, which a connector's own ConnectorStopped call already
// increments synchronously (see triggerFlush and flushNow) before that call
// returns. A caller that calls WaitPendingWrites strictly after learning (e.g.
// via a WaitGroup/tomb join) that ConnectorStopped has already been called
// for the connector it cares about is guaranteed to observe that connector's
// flush AND callback complete: the Add(1) calls happened-before the Wait()
// call by construction, and sync.WaitGroup cannot miss a Done that was
// already pending when Wait was entered.
//
// The callbacksDone half of this wait matters specifically for
// connector.Source's Ack (Approach A, see source.go): the plugin-ack it sends
// is deferred to run *inside* the PersistCallback, once the position is
// durably flushed. Waiting only on the store write (as this method did before that
// fix) would let a caller proceed — and a graceful shutdown tear down the
// plugin — after the store write landed but before the plugin was actually
// told about it, reintroducing an invariant-1 gap on the graceful path even
// though the crash path was fixed. See
// docs/design-documents/20260723-source-ack-persist-ordering-fix.md,
// "Graceful shutdown (invariant 7)".
//
// Used by lifecycle.Service.StopAndWait to await durability (invariant 1/3)
// after a pipeline has fully drained, without deadlocking on unrelated running
// pipelines. See docs/design-documents/20260708-live-server-deploy-apply.md,
// "Review outcome & required rework", blocker 1.
func (p *Persister) WaitPendingWrites() {
	// Snapshot the current generation under m, then wait OUTSIDE the lock.
	//
	// The lock must not be held across the wait: WaitPendingWritesContext
	// deliberately returns early on timeout while this goroutine keeps
	// waiting, so holding m here would let a single stuck flush stall every
	// connector's Persist process-wide — trading a crash for a global hang.
	//
	// This replaces `flushWg.Wait(); callbackWg.Wait()`, which read two
	// WaitGroups with no lock while triggerFlush/flushNow concurrently raised
	// them. A WaitGroup Add that lifts the counter off zero during a Wait is
	// the documented reuse hazard, and it does not merely race — it panics
	// with "sync: WaitGroup is reused before previous Wait has returned",
	// killing the process. connector.Service shares ONE Persister across every
	// connector and Source.Teardown calls WaitPendingWritesContext, so any
	// pipeline where one source tears down while another still acks hit it.
	// Confirmed in a shipped binary on ordinary shutdown of a 2-source
	// pipeline: 3/3 runs under arch-v2 (one Worker per source, so all of them
	// tear down at once), 1/3 under v1.
	p.m.Lock()
	st := p.flush
	p.m.Unlock()

	if st == nil {
		return // nothing was ever flushed
	}
	<-st.writeDone
	<-st.callbacksDone
}

// WaitPendingWritesContext behaves like WaitPendingWrites, but returns early
// — without waiting for the flush/callbacks to actually finish — if ctx is
// canceled or timeout elapses, whichever comes first. It returns nil if the
// wait completed normally, or the triggering error (ctx.Err() or
// context.DeadlineExceeded) if it did not.
//
// This exists for a caller like Source.Teardown that must not hang
// indefinitely on a stuck or slow flush (e.g. a disk stall or a badger
// compaction pause) during graceful shutdown: before Approach A
// (docs/design-documents/20260723-source-ack-persist-ordering-fix.md),
// Teardown never waited on the persister at all, so this wait is new
// exposure, not a pre-existing one — an unbounded wait here would trade the
// sev-0 ack-before-persist bug for a possible-hang-on-graceful-shutdown bug,
// which is not an improvement. A bounded, forced-teardown fallback is safe:
// the SIGKILL chaos suite (tests/chaos) already proves the crash path never
// produces a gap, so a caller proceeding with teardown without the deferred
// ack having been confirmed sent is at worst a benign duplicate on the next
// run (the position may not be durably flushed yet, so a restart simply
// re-delivers it), never a gap — see Source.Teardown's doc comment for the
// full failure-mode entry this covers.
//
// Note on the timeout/cancel path: the background goroutine wrapping
// WaitPendingWrites is not itself abortable (sync.WaitGroup has no cancel),
// so it keeps running until the underlying flush actually finishes, even
// after this function has returned early. That goroutine is only leaked for
// as long as the stuck flush is; if the flush eventually completes (the
// common case — a slow disk, not a dead one), the goroutine exits normally.
// A genuinely permanently-stuck flush would leak it permanently, but at that
// point the process has a much bigger problem than one goroutine, and no
// caller-side timeout can fix a store that will never respond.
func (p *Persister) WaitPendingWritesContext(ctx context.Context, timeout time.Duration) error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.WaitPendingWrites()
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return context.DeadlineExceeded
	}
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

	// Wait for any running flusher to finish. This blocks while holding m,
	// exactly as the flushWg.Wait() it replaces did — a flush is serialized
	// against the next one either way.
	if p.flush != nil {
		<-p.flush.writeDone
	}

	// reset callbacks and bundle count
	batch := p.batch
	p.batch = nil
	p.bundleCount = 0

	// Publish this generation BEFORE starting it, under m, so a concurrent
	// WaitPendingWrites either misses it entirely (and is correct — it was
	// called before this flush was triggered) or sees it whole.
	st := &flushState{
		writeDone:     make(chan struct{}),
		callbacksDone: make(chan struct{}),
	}
	p.flush = st
	go p.flushNow(ctx, batch, st)
}

// flushNow will flush the state to the store.
//
// Every connector's callback receives the outcome of its own write: nil if
// its state was committed, an error if it was not. Every path, including a
// failure to open a transaction, calls every callback exactly once and closes
// both of st's channels. See writeBatch for how one connector's failed write
// is kept from failing the rest of the batch (#2930).
func (p *Persister) flushNow(ctx context.Context, batch map[string]persistData, st *flushState) {
	defer close(st.writeDone)
	start := p.clock.Now()

	results := p.writeBatch(ctx, batch)
	failed := 0
	for id := range batch {
		// Invariant 1: a callback gets nil only for a recorded commit. A
		// missing result (writeBatch always records one; this guards a
		// future change to it) fails closed.
		if _, ok := results[id]; !ok {
			results[id] = statePersistError("internal error", cerrors.Errorf("no write outcome recorded for connector %q", id))
		}
		if results[id] != nil {
			failed++
		}
	}

	// Track every callback this flush spawns so WaitPendingWrites can observe
	// not just "the write landed" but "every side effect the write's callback
	// performs has also finished" — see flushState.callbacksDone. The
	// WaitGroup here is local to this flush generation and is never waited on
	// by anyone else, so it cannot be reused underneath a Wait; the closer
	// goroutine below converts it into a channel close, which is what callers
	// actually observe.
	var cbWg sync.WaitGroup
	cbWg.Add(len(batch))
	for id, data := range batch {
		// execute callbacks in go routines to make sure they can't block this function
		go func(cb PersistCallback, err error) {
			defer cbWg.Done()
			cb(err)
		}(data.callback, results[id])
	}
	go func() {
		cbWg.Wait()
		close(st.callbacksDone)
	}()

	p.logger.Debug(ctx).
		Int("count", len(batch)).
		Int("failed", failed).
		Dur(log.DurationField, p.clock.Now().Sub(start)).
		Msg("persisted connectors")
}

// writeBatch stores the connectors in batch and returns, for every connector
// ID in batch, the outcome of that connector's write: nil if its state was
// committed, otherwise an error carrying CodeConnectorStatePersistFailed.
//
// It writes the batch in one transaction. When one connector's write fails,
// it discards that transaction, records the error for that connector only,
// and writes the remaining connectors again in a fresh transaction. It stops
// writing at the first failure instead of trying the rest of the batch in the
// same transaction, because on Postgres (and SQLite for most errors) a failed
// statement aborts the transaction and every later write in it fails too,
// which would blame connectors whose writes were fine. A failed transaction
// is never reused: whether it is still usable depends on the store (badger:
// yes, after ErrTxnTooBig; Postgres: no), so only a fresh transaction gives
// the same guarantee on every store. Every store's Discard drops the
// uncommitted writes (badger discards the txn, Postgres and SQLite roll back),
// so a retried write is never committed twice or half.
//
// A failure that is not one connector's write (opening a transaction, or
// Commit) is not attributable, so every connector still in that attempt gets
// it and nothing is retried.
//
// Each attempt either commits or removes one connector, so a flush makes at
// most one transaction per connector in the batch. That worst case (every
// write failing) costs one fast-failing transaction per connector, instead of
// one transaction that fails everyone.
//
// Invariant 1: a source's PersistCallback releases its deferred upstream ack
// (see Source.onPersistFlushed), so a connector gets nil only when the
// transaction holding its write committed.
// Invariant 2: a position is never reported stored when it was not. A
// connector left out keeps its previously stored position; its next Persist
// writes its later cumulative state. Retries happen inside this flush, which
// triggerFlush serializes against the next one, so an older state can never
// be written over a newer one.
// Invariant 3: every failure reaches the callback of the connector it belongs
// to, never only a log line.
func (p *Persister) writeBatch(ctx context.Context, batch map[string]persistData) map[string]error {
	results := make(map[string]error, len(batch))

	// Write in a stable order, so a failure is reproducible and logs from
	// consecutive attempts are comparable.
	remaining := make([]string, 0, len(batch))
	for id := range batch {
		remaining = append(remaining, id)
	}
	slices.Sort(remaining)

	for attempt := 1; len(remaining) > 0; attempt++ {
		failedID, err := p.writeAttempt(ctx, batch, remaining)
		switch {
		case err == nil:
			for _, id := range remaining {
				results[id] = nil
			}
			return results
		case failedID == "":
			// Not attributable to one connector: everyone still in this
			// attempt failed with it.
			p.logger.Err(ctx, err).
				Int("count", len(remaining)).
				Int("attempt", attempt).
				Msg("failed to persist connector batch; nothing in this attempt was committed and every connector in it is notified")
			for _, id := range remaining {
				results[id] = err
			}
			return results
		default:
			p.logger.Err(ctx, err).
				Str(log.ConnectorIDField, failedID).
				Int("remaining", len(remaining)-1).
				Int("attempt", attempt).
				Msg("failed to persist connector state; retrying the rest of the batch without it in a new transaction")
			results[failedID] = err
			remaining = slices.DeleteFunc(remaining, func(id string) bool { return id == failedID })
		}
	}
	return results
}

// writeAttempt writes the connectors in ids, in order, in one new transaction
// and commits it. It returns a nil error only if the commit succeeded. If a
// connector's write fails it stops there, discards the transaction and
// returns that connector's ID with the error. If opening or committing the
// transaction fails it returns an empty ID. On any error nothing from this
// attempt has been committed.
func (p *Persister) writeAttempt(ctx context.Context, batch map[string]persistData, ids []string) (failedID string, err error) {
	tx, txCtx, err := p.db.NewTransaction(ctx, true)
	if err != nil {
		return "", statePersistError("failed to create transaction for connector batch", err)
	}
	// Discard after a successful Commit is a no-op (database.Transaction
	// contract); on every error path it is what drops the partial writes.
	defer tx.Discard()

	for _, id := range ids {
		if storeErr := batch[id].storeFunc(txCtx); storeErr != nil {
			return id, statePersistError(
				fmt.Sprintf("failed to store connector %q, its state was not committed", id),
				storeErr,
			)
		}
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return "", statePersistError("failed to commit connector batch", commitErr)
	}
	return "", nil
}

// statePersistError wraps cause as a CodeConnectorStatePersistFailed error.
// The message includes the cause's text because ConduitError.Error() returns
// only its own message, and operators need the store's error (and the
// failing connector IDs) in the one line they see.
func statePersistError(msg string, cause error) error {
	err := conduiterr.Wrap(CodeConnectorStatePersistFailed, msg+": "+cause.Error(), cause)
	err.Suggestion = statePersistSuggestion
	return err
}
