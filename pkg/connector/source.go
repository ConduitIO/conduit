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
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
)

// DefaultTeardownFlushTimeout bounds how long Source.Teardown will wait
// (Persister.WaitPendingWritesContext) for the final forced flush and its
// deferred ack (Approach A, docs/design-documents/
// 20260723-source-ack-persist-ordering-fix.md) before proceeding with
// teardown regardless. Deliberately shorter than a typical Kubernetes
// terminationGracePeriodSeconds (commonly 30s): if this wait itself took the
// full grace period, a slow flush could cause the process to be SIGKILLed
// before Teardown's own bounded-wait fallback ever got to run, which would
// be strictly worse than proceeding early. See Teardown's doc comment for
// the failure mode this bound exists for.
const DefaultTeardownFlushTimeout = 10 * time.Second

// Deferred-ack delivery retry policy (Approach A2, docs/design-documents/
// 20260728-snapshot-handoff-deferred-ack-deadlock.md). A snapshot-gating
// source (e.g. Postgres) blocks its own progress until the snapshot-boundary
// ack is delivered, so a transient stream.Send failure for a deferred ack must
// be RETRIED until it succeeds, not dropped — a dropped boundary ack is a
// permanent handoff deadlock + silent post-snapshot CDC loss (invariant 3),
// not the benign no-op the pre-A2 code assumed. These constants bound that
// retry so a genuinely broken stream (not a transient blip) is escalated
// loudly via errs instead of retried forever. They are overridable per source
// via the deferredAck* fields below for deterministic tests (mirroring
// teardownFlushTimeout).
const (
	// DefaultDeferredAckMaxRetries bounds how many times the per-source
	// ack-delivery goroutine retries a transient stream.Send failure to a
	// running plugin before escalating the error via errs.
	DefaultDeferredAckMaxRetries = 12
	// DefaultDeferredAckBackoffInitial is the first inter-retry backoff; it
	// doubles each attempt, capped at DefaultDeferredAckBackoffCap.
	DefaultDeferredAckBackoffInitial = 10 * time.Millisecond
	// DefaultDeferredAckBackoffCap caps the exponential backoff between
	// deferred-ack retries.
	DefaultDeferredAckBackoffCap = 500 * time.Millisecond
)

type Source struct {
	Instance *Instance

	dispenser connectorPlugin.Dispenser
	plugin    connectorPlugin.SourcePlugin

	// errs is used to signal the node that the connector experienced an error
	// when it was processing something asynchronously (e.g. persisting state).
	errs chan error

	// stream is the stream used to exchange records and acks with the
	// source plugin.
	stream pconnector.SourceRunStreamClient

	// stopStream is a function that closes the context of the stream
	stopStream context.CancelFunc

	// streamCtx is the context that stopStream cancels — the single context
	// shared by both directions of the plugin stream (see run). The deferred-
	// ack delivery goroutine observes streamCtx.Done() as the authoritative
	// "the stream is being torn down" signal: once it is done, any stream.Send
	// resolves to ctx.Canceled rather than an actual send, so the goroutine
	// stops retrying and drops (benign — the position is already durable). It
	// is set once in run, before deliverDeferredAcks is started, so the
	// goroutine reads it race-free.
	streamCtx context.Context //nolint:containedctx // mirrors the stream's own contained ctx; read-only in the delivery goroutine

	// wg tracks the number of in flight calls to the connectorPlugin.
	wg sync.WaitGroup

	// ackMu guards pendingAcks, nextAckSeq and durableAckSeq below. It is
	// deliberately separate from Instance's RWMutex: onPersistFlushed runs
	// asynchronously (invoked from connector.Persister's flush callback,
	// see persister.go's callbackWg) and must be able to send the deferred
	// plugin-ack — which needs preparePluginCall's Instance.RLock — without
	// itself holding a lock that could be held by a concurrent caller
	// blocked waiting on this same flush (see Teardown, which forces and
	// awaits a flush without holding Instance's lock for exactly this
	// reason).
	ackMu sync.Mutex
	// pendingAcks is a FIFO queue, in the exact order Ack was called, of
	// positions whose durable persistence has been requested but not yet
	// confirmed. See Ack and onPersistFlushed.
	pendingAcks []pendingAck
	// nextAckSeq is a purely engine-internal, monotonically increasing
	// counter assigned to each Ack call — NOT derived from the opaque
	// connector Position bytes, which Source cannot generically parse or
	// compare (a Position's structure, e.g. per-partition offsets, is
	// entirely connector-defined). It is what lets onPersistFlushed
	// determine, without understanding Position's contents, which queued
	// acks a given durable flush covers, while still preserving invariant 4
	// (per-partition — here, per-connector — ordering): acks are always
	// delivered to the plugin in the exact order Ack assigned them a seq.
	nextAckSeq uint64
	// durableAckSeq is the highest seq confirmed durable so far. It only
	// ever advances (see onPersistFlushed) even if flush confirmations
	// arrive out of order.
	durableAckSeq uint64

	// deferredAckQueue is the FIFO hand-off from onPersistFlushed (which runs
	// in connector.Persister's shared callbackWg goroutine) to the dedicated
	// per-source delivery goroutine (deliverDeferredAcks). onPersistFlushed
	// appends the durable positions it drains here, in the exact order it
	// drains them, and returns fast — it never performs the (retryable,
	// possibly-slow) stream.Send itself, so a slow plugin no longer blocks the
	// process-wide flush cycle (Approach A2 narrows the blast radius #2680
	// flagged). Guarded by ackMu; each element is one Ack call's positions.
	deferredAckQueue [][]opencdc.Position
	// deferredAckClosed is set by Teardown (under ackMu) once it has begun
	// draining the delivery goroutine. After it is set, onPersistFlushed stops
	// enqueuing (a straggler flush's positions are already durable, so dropping
	// its ack during teardown is benign — restart re-delivers). Guarded by
	// ackMu.
	deferredAckClosed bool

	// deferredAckSignal wakes deliverDeferredAcks when new work is enqueued (or
	// when Teardown sets deferredAckClosed). Buffered (size 1) and sent to
	// non-blockingly, so a producer under ackMu never blocks on it; a single
	// buffered slot is sufficient because the goroutine always drains the
	// entire queue on each wakeup.
	deferredAckSignal chan struct{}
	// deliveryDone is closed by deliverDeferredAcks when it exits, so Teardown
	// can join it and guarantee no goroutine leak.
	deliveryDone chan struct{}
	// tearingDown is set true at the very start of Teardown, before any wait.
	// Once set, deliverDeferredAcks still RETRIES a transient send failure (to
	// deliver the final durable ack, invariant 7) but never ESCALATES an
	// exhausted retry via errs — nothing reads errs during teardown, so an
	// escalation there would block the goroutine forever. While it is false
	// (plugin genuinely running), an exhausted retry escalates loudly, which
	// is safe because the node is reading errs. See deliverOneAck.
	tearingDown atomic.Bool

	// teardownFlushTimeout overrides DefaultTeardownFlushTimeout for
	// Teardown's bounded flush wait. Zero (the production default — this
	// field is only ever set by tests) means "use
	// DefaultTeardownFlushTimeout"; see Teardown.
	teardownFlushTimeout time.Duration

	// deferredAckMaxRetries and deferredAckBackoffCap override
	// DefaultDeferredAckMaxRetries / DefaultDeferredAckBackoffCap for the
	// deferred-ack delivery goroutine's retry policy. Zero (the production
	// default — these fields are only ever set by tests) means "use the
	// Default* constant"; see deliverOneAck.
	deferredAckMaxRetries int
	deferredAckBackoffCap time.Duration
}

// pendingAck is one Source.Ack call's positions, queued until the resulting
// state write is confirmed durably flushed by the persister. See the
// Source.ackMu field doc.
type pendingAck struct {
	seq       uint64
	positions []opencdc.Position
}

type SourceState struct {
	Position opencdc.Position
}

func (s *Source) ID() string {
	return s.Instance.ID
}

func (s *Source) Errors() <-chan error {
	return s.errs
}

func (s *Source) Open(ctx context.Context) (err error) {
	s.Instance.Lock()
	defer s.Instance.Unlock()
	if s.Instance.connector != nil {
		// this shouldn't actually happen, it indicates a problem elsewhere
		return cerrors.New("another instance of the connector is already running")
	}

	s.Instance.logger.Debug(ctx).Msg("dispensing source connector plugin")
	s.plugin, err = s.dispenser.DispenseSource()
	if err != nil {
		return err
	}

	defer func() {
		// ensure the plugin gets torn down if something bad happens
		if err != nil {
			_, tdErr := s.plugin.Teardown(ctx, pconnector.SourceTeardownRequest{})
			if tdErr != nil {
				s.Instance.logger.Err(ctx, tdErr).Msg("could not tear down source connector plugin")
			}
			s.plugin = nil
		}
	}()

	err = s.configure(ctx)
	if err != nil {
		return err
	}

	lifecycleEventTriggered, err := s.triggerLifecycleEvent(ctx, s.Instance.LastActiveConfig.Settings, s.Instance.Config.Settings)
	if err != nil {
		return err
	}

	if lifecycleEventTriggered {
		// when a lifecycle event is successfully triggered we consider the config active
		s.Instance.LastActiveConfig = s.Instance.Config
		// persist connector in the next batch to store last active config
		err := s.Instance.persister.Persist(ctx, s.Instance, func(err error) {
			if err != nil {
				s.errs <- err
			}
		})
		if err != nil {
			return err
		}
	}

	err = s.open(ctx)
	if err != nil {
		return err
	}

	err = s.run(ctx)
	if err != nil {
		return err
	}

	s.Instance.logger.Info(ctx).Msg("source connector plugin successfully started")

	s.Instance.connector = s
	s.Instance.persister.ConnectorStarted()

	// Start the dedicated per-source deferred-ack delivery goroutine (Approach
	// A2). It is started last, after every fallible step of Open has succeeded
	// (run, the last such step, has already set stream/streamCtx), so a failed
	// Open never leaks it. Teardown drains and joins it. See
	// deliverDeferredAcks and docs/design-documents/
	// 20260728-snapshot-handoff-deferred-ack-deadlock.md.
	s.deferredAckSignal = make(chan struct{}, 1)
	s.deliveryDone = make(chan struct{})
	go s.deliverDeferredAcks()

	return nil
}

func (s *Source) Stop(ctx context.Context) (opencdc.Position, error) {
	cleanup, err := s.preparePluginCall()
	defer cleanup()
	if err != nil {
		return nil, err
	}

	s.Instance.logger.Debug(ctx).Msg("sending stop signal to source connector plugin")
	resp, err := s.plugin.Stop(ctx, pconnector.SourceStopRequest{})
	if err != nil {
		return nil, cerrors.Errorf("could not stop source plugin: %w", err)
	}

	s.Instance.logger.Info(ctx).
		Bytes(log.RecordPositionField, resp.LastPosition).
		Msg("source connector plugin successfully responded to stop signal")
	return resp.LastPosition, nil
}

// Teardown closes the source's stream and plugin. Invariant 7 (graceful
// shutdown must not drop the final ack): before actually tearing the plugin
// down, this forces any position write still sitting in the persister's
// debounce batch to flush now, and waits for that flush's deferred plugin-ack
// (Ack/onPersistFlushed, Approach A) to actually be SENT — while the stream
// is still fully open in both directions. Without this, a graceful shutdown
// could tear down the plugin between "position flushed" and "plugin acked"
// (lifecycle.Service.StopAndWait's own WaitPersisted call happens only after
// the pipeline's nodes — including this Teardown — have already run),
// silently dropping the final ack even though the crash-path ordering fix
// prevents the equivalent data-loss bug on a kill -9. See
// docs/design-documents/20260723-source-ack-persist-ordering-fix.md,
// "Graceful shutdown (invariant 7)".
//
// Under Approach A2 (docs/design-documents/
// 20260728-snapshot-handoff-deferred-ack-deadlock.md) the actual stream.Send
// of each deferred ack is performed by the dedicated per-source delivery
// goroutine (deliverDeferredAcks), not inline in onPersistFlushed. So the
// flush-and-wait below only guarantees the final durable positions are
// ENQUEUED; the additional waitDeliveryDrain step (also before stopStream,
// also bounded by the same budget) is what guarantees they are actually SENT
// before the stream is closed. tearingDown is set first so that, from here on,
// the delivery goroutine still retries transient sends but never escalates via
// errs (nothing reads errs during teardown).
//
// Critically, this flush-and-wait must happen BEFORE stopStream, not after:
// stopStream cancels the one context shared by both directions of the
// plugin stream (see run's context.WithCancel and, for the in-memory
// transport, pkg/plugin/connector/builtin/stream.go's inMemoryStream — a
// real gRPC stream's client context works the same way). A deferred ack's
// stream.Send racing an already-canceled context resolves to ctx.Err() far
// more often than an actual send (a permanently-ready select case beats one
// that depends on a concurrent receiver), so sending after stopStream would
// silently and near-deterministically drop the final ack instead of
// delivering it — the exact bug this reordering exists to avoid. Read
// unblocking (stopStream's other job, for a source blocked waiting for a
// record that will never come) is deliberately delayed until after the
// flush instead.
//
// Failure mode: graceful shutdown racing a stuck/slow flush. Before this
// fix, Teardown never touched the persister at all, so a wait here is new
// exposure: an unbounded wait would let a stalled disk or a badger
// compaction pause hang graceful shutdown indefinitely, trading the sev-0
// ack-before-persist bug for a possible-hang-on-shutdown bug. The wait below
// is therefore bounded (Persister.WaitPendingWritesContext, honoring both
// ctx and DefaultTeardownFlushTimeout) — on timeout or ctx cancellation,
// Teardown logs a warning and proceeds with teardown anyway rather than
// blocking forever. That fallback is safe, not merely convenient: the
// SIGKILL chaos suite (tests/chaos, TestSIGKILL_PruningUpstream_NoGap /
// TestSIGKILL_DurableUpstream_NoGap) already proves the crash path never
// produces a gap, so a forced teardown here — before the deferred ack was
// confirmed sent — degrades to exactly that same, already-proven-safe
// outcome: at worst a benign duplicate on the next run (the position may
// not have reached disk yet, so a restart simply re-delivers it), never a
// gap.
func (s *Source) Teardown(ctx context.Context) error {
	// Signal the deferred-ack delivery goroutine that we are tearing down,
	// before any wait below. From here on it will still retry a transient send
	// (to deliver the final durable ack during the bounded drain) but will
	// never escalate an exhausted retry via errs — nothing reads errs during
	// teardown, so an escalation there would deadlock the goroutine. See the
	// tearingDown field doc and deliverOneAck.
	s.tearingDown.Store(true)

	s.Instance.Lock()
	if s.plugin == nil {
		s.Instance.Unlock()
		return plugin.ErrPluginNotRunning
	}
	s.Instance.Unlock()

	// Deliberately not holding s.Instance's lock across this wait:
	// onPersistFlushed's deferred ack needs preparePluginCall's
	// Instance.RLock to succeed while s.plugin is still considered running
	// (checked again below), which would deadlock against an exclusive
	// lock held here. funnel.Worker's own stop sequencing (processingLock +
	// the stop flag, see worker.go's Stop) already guarantees no new Ack
	// call can start once a Teardown has begun, so this window introduces
	// no new concurrent-Ack risk.
	timeout := s.teardownFlushTimeout
	if timeout <= 0 {
		timeout = DefaultTeardownFlushTimeout
	}
	// Both the flush wait and the delivery-goroutine drain below share one
	// bound (DefaultTeardownFlushTimeout): they must complete, in total,
	// before we cancel the stream and tear the plugin down. deadline anchors
	// that single budget.
	deadline := time.Now().Add(timeout)
	s.Instance.persister.Flush(ctx)
	if err := s.Instance.persister.WaitPendingWritesContext(ctx, timeout); err != nil {
		// Bounded-wait fallback (see doc comment above): proceed with
		// teardown rather than hang. The deferred ack for whatever is still
		// in flight may not have been sent, but the position is either
		// already durable (benign duplicate) or not yet durable (the
		// plugin was never told to commit past it either, so nothing is
		// lost) — never a gap, by the same reasoning the SIGKILL chaos
		// suite already establishes for a hard crash, which this bounded
		// wait is strictly safer than.
		s.Instance.logger.Warn(ctx).Err(err).
			Dur("timeout", timeout).
			Msg("timed out waiting for the final flush/deferred ack before tearing down source connector plugin; proceeding with teardown anyway (safe: at worst a benign duplicate on restart, never a gap — see Teardown's doc comment)")
	}

	// The forced flush above has run its onPersistFlushed callbacks (waited on
	// by WaitPendingWritesContext), so the final durable positions are now
	// enqueued for delivery. Close the queue and drain the delivery goroutine
	// so the FINAL ack is actually SENT before we cancel the stream below —
	// bounded by whatever remains of the same budget. On timeout we proceed:
	// stopStream then cancels the stream and the goroutine finishes fast,
	// degrading any still-undelivered ack to the already-proven-safe benign
	// duplicate on restart (never a gap), exactly as the flush-wait fallback
	// above. This is Approach A2's move of #2680's bounded-drain from the
	// persister callback onto the delivery goroutine.
	s.ackMu.Lock()
	s.deferredAckClosed = true
	s.ackMu.Unlock()
	s.signalDelivery()
	s.waitDeliveryDrain(ctx, time.Until(deadline))

	s.Instance.Lock()
	if s.plugin == nil {
		// Another Teardown call already finished while this one was
		// unlocked above. Should not happen given funnel.Worker's own
		// teardownMu serialization, but Teardown is a public method on an
		// exported type and must stay safe if ever called concurrently.
		s.Instance.Unlock()
		return plugin.ErrPluginNotRunning
	}

	s.Instance.logger.Debug(ctx).Msg("closing stream")
	// close stream — only now that every deferred ack pending at the start
	// of this call has already been sent (see doc comment above).
	if s.stopStream != nil {
		s.stopStream()
	}
	s.Instance.Unlock()

	// Join the delivery goroutine before waiting on in-flight plugin calls and
	// tearing the plugin down. If the bounded drain above already saw it exit,
	// this returns immediately; if the drain timed out, stopStream just
	// canceled streamCtx, so the goroutine's in-flight/queued sends now fail
	// fast (ctx.Canceled) and its retry backoff aborts — it exits promptly.
	// This guarantees no goroutine leak and that no deferred-ack send races
	// the plugin.Teardown below (which nils s.plugin).
	if s.deliveryDone != nil {
		<-s.deliveryDone
	}

	// wait for any calls to the plugin to stop running (e.g. Stop, or a Read
	// that was blocked waiting for a record that will never come, now
	// unblocked by the stopStream call above)
	s.wg.Wait()

	s.Instance.Lock()
	defer s.Instance.Unlock()
	if s.plugin == nil {
		return plugin.ErrPluginNotRunning
	}

	s.Instance.logger.Debug(ctx).Msg("tearing down source connector plugin")
	_, err := s.plugin.Teardown(ctx, pconnector.SourceTeardownRequest{})

	s.plugin = nil
	s.Instance.connector = nil
	s.Instance.persister.ConnectorStopped()

	if err != nil {
		return cerrors.Errorf("could not tear down source connector plugin: %w", err)
	}

	s.Instance.logger.Info(ctx).Msg("source connector plugin successfully torn down")
	return nil
}

func (s *Source) Read(ctx context.Context) ([]opencdc.Record, error) {
	cleanup, err := s.preparePluginCall()
	defer cleanup()
	if err != nil {
		return nil, err
	}

	if s.stream == nil {
		return nil, cerrors.Errorf("source stream not open: %w", connectorPlugin.ErrStreamNotOpen)
	}

	resp, err := s.stream.Recv()
	if err != nil {
		return nil, err
	}

	now := strconv.FormatInt(time.Now().UnixNano(), 10)
	for _, r := range resp.Records {
		s.sanitizeRecord(&r, now)
	}

	s.Instance.inspector.Send(ctx, resp.Records)
	return resp.Records, nil
}

// Ack acknowledges that the records at the given positions were fully
// processed downstream (or routed to the DLQ, from the caller's point of
// view — see funnel.Worker.Ack/Nack). It does not send the ack to the plugin
// synchronously.
//
// Invariant 1: ack only after the resulting position is durably persisted.
// The plugin ack (stream.Send, driven from onPersistFlushed) is deferred
// until the persister confirms this exact call's resulting state write has
// been durably flushed. This is Approach A from
// docs/design-documents/20260723-source-ack-persist-ordering-fix.md: a
// plugin's own upstream commit — e.g. a Postgres replication slot's
// confirmed_flush_lsn advance, which frees WAL for recycling — must never be
// triggered before Conduit's own crash-recoverable record of that position
// exists on disk, or a crash in between loses the position while the
// upstream has already discarded the data (sev-0, see
// docs/postmortems/20260723-source-ack-persist-ordering.md). Do not
// reintroduce a synchronous stream.Send here without re-reading that design
// doc.
//
// The persister's debounce/batching (persister.go's DefaultPersisterDelayThreshold
// / DefaultPersisterBundleCountThreshold) is unchanged by this — durability
// still lands on the same schedule it always did. What changes is that the
// plugin only learns about it once it's true, which delays a pruning
// upstream's WAL/log retention release by up to one debounce interval —
// bounded, tunable, and the entire trade this fix makes (see the design
// doc's Decision section).
func (s *Source) Ack(ctx context.Context, p []opencdc.Position) error {
	cleanup, err := s.preparePluginCall()
	defer cleanup()
	if err != nil {
		return err
	}

	if s.stream == nil {
		return cerrors.Errorf("source stream not open: %w", connectorPlugin.ErrStreamNotOpen)
	}

	// lock as we are updating the state and leave it locked so the persister
	// can safely prepare the connector before it stores it
	s.Instance.Lock()
	defer s.Instance.Unlock()
	s.Instance.State = SourceState{Position: p[len(p)-1]}

	// Invariant 4 (per-partition/per-connector ordering): queue this ack
	// under the same lock used to update state and register the persist
	// call, so pendingAcks is always populated in exactly the order Ack is
	// called — see the seq field doc for why a purely-internal counter,
	// not the opaque Position, is what onPersistFlushed uses to know what
	// it may safely release to the plugin.
	s.ackMu.Lock()
	s.nextAckSeq++
	seq := s.nextAckSeq
	s.pendingAcks = append(s.pendingAcks, pendingAck{seq: seq, positions: p})
	s.ackMu.Unlock()

	err = s.Instance.persister.Persist(ctx, s.Instance, func(err error) {
		s.onPersistFlushed(seq, err)
	})
	if err != nil {
		return cerrors.Errorf("failed to persist source connector: %w", err)
	}

	return nil
}

// onPersistFlushed is invoked by connector.Persister once the state write
// registered by the Ack call that produced sequence number seq has either
// been durably committed (err == nil) or failed (err != nil). It is called
// from within a goroutine that persister.go's flushNow's callbackWg tracks,
// so any deferred plugin-ack this method sends is awaited by
// Persister.WaitPendingWrites — see that method's doc for why that matters
// for graceful shutdown (invariant 7).
//
// seq need not be the exact Ack call whose PersistCallback the persister
// happened to retain (Persister.Persist's batch map keeps only the LAST
// callback registered for a connector before a flush runs, see persister.go)
// — since Source.Ack always writes the connector's cumulative, monotonically
// advancing SourceState.Position, whichever flush actually lands durably
// necessarily covers every seq up to (and including) the one it was
// registered for. Flush confirmations can also arrive out of order (case:
// a later-registered flush's transaction happens to finish before an
// earlier one still in flight); durableAckSeq only ever advances forward
// and pendingAcks is drained from its head up to whatever the current
// durableAckSeq permits, so out-of-order arrival can only make this method
// a safe no-op for a seq already covered by a previous call — never a
// double-send and never a gap.
func (s *Source) onPersistFlushed(seq uint64, err error) {
	if err != nil {
		// Durability failed: propagate so the runtime can fail the
		// connector/pipeline, exactly as before this fix. Invariant 1: never
		// ack the plugin for a write that did not durably land — the queued
		// positions stay queued; there is nothing safe to send, and this
		// connector is on its way down regardless.
		s.errs <- err
		return
	}

	appended := false
	s.ackMu.Lock()
	if seq > s.durableAckSeq {
		s.durableAckSeq = seq
	}
	i := 0
	for ; i < len(s.pendingAcks) && s.pendingAcks[i].seq <= s.durableAckSeq; i++ {
		// Approach A2: hand the durable positions to the dedicated per-source
		// delivery goroutine (deliverDeferredAcks) in FIFO order instead of
		// sending them to the plugin inline here. Appending under ackMu keeps
		// enqueue order identical to drain order (invariant 4) and returns
		// fast — this callback runs in connector.Persister's shared callbackWg
		// goroutine, so it must not block on a (retryable, possibly-slow)
		// stream.Send (that would widen the process-wide flush blast radius
		// #2680 flagged). If Teardown has already begun draining
		// (deferredAckClosed), the positions are still durable, so dropping
		// their ack now is benign — restart re-delivers.
		if !s.deferredAckClosed {
			s.deferredAckQueue = append(s.deferredAckQueue, s.pendingAcks[i].positions)
			appended = true
		}
	}
	s.pendingAcks = s.pendingAcks[i:]
	s.ackMu.Unlock()

	if appended {
		s.signalDelivery()
	}
}

// signalDelivery wakes the deferred-ack delivery goroutine without blocking.
// deferredAckSignal is buffered (size 1); a non-blocking send is sufficient
// because deliverDeferredAcks always drains the whole queue on each wakeup, so
// at most one pending wakeup ever needs to be latched.
func (s *Source) signalDelivery() {
	select {
	case s.deferredAckSignal <- struct{}{}:
	default:
	}
}

// deliverDeferredAcks is the dedicated per-source ack-delivery goroutine
// (Approach A2, docs/design-documents/
// 20260728-snapshot-handoff-deferred-ack-deadlock.md). It reads the FIFO
// deferredAckQueue that onPersistFlushed populates and delivers each durable
// position set to the plugin, in order, retrying transient failures while the
// plugin is running (see deliverOneAck). It exists so a snapshot-gating source
// (e.g. Postgres), which blocks its own progress until the snapshot-boundary
// ack is delivered, can never lose that ack to a transient send failure — the
// pre-A2 code logged-and-dropped it, which for such a source is a permanent
// handoff deadlock + silent post-snapshot CDC loss (invariant 3).
//
// It is started in Open (after run has set stream/streamCtx) and exits once
// Teardown has set deferredAckClosed and it has drained everything queued
// before that point. On exit it closes deliveryDone, which Teardown joins to
// guarantee no leak.
func (s *Source) deliverDeferredAcks() {
	defer close(s.deliveryDone)
	for {
		s.ackMu.Lock()
		queue := s.deferredAckQueue
		s.deferredAckQueue = nil
		closed := s.deferredAckClosed
		s.ackMu.Unlock()

		for _, positions := range queue {
			s.deliverOneAck(positions)
		}

		if len(queue) > 0 {
			// We may have raced a producer that enqueued more while we were
			// delivering; re-check the queue before parking.
			continue
		}
		if closed {
			// Teardown has closed the queue and we have drained everything
			// enqueued before it did (onPersistFlushed stops enqueuing once
			// deferredAckClosed is set, all under ackMu, so there is nothing
			// left to come). Exit.
			return
		}
		// Nothing to do; park until a producer (or Teardown) signals. The
		// signal is latched (buffered size 1) and sent after the enqueue/close
		// under ackMu, so this can never miss a wakeup.
		<-s.deferredAckSignal
	}
}

// deliverOneAck delivers one previously-queued Ack call's positions to the
// plugin now that the resulting position is known durable.
//
// Invariant 3 (at-least-once) at a snapshot→CDC handoff: while the plugin is
// running, a transient stream.Send failure is RETRIED with bounded backoff
// until it succeeds — it is NOT dropped. A snapshot-gating source emits no
// further records until it receives the snapshot-boundary ack, so there is no
// "next ack" to carry a dropped one; dropping it deadlocks the handoff and
// silently loses all post-snapshot CDC. If the retries exhaust while the
// plugin is genuinely running (a broken stream, not teardown), the error is
// escalated via errs — safe, because the node is reading errs while running;
// a loud failure of an already-broken connector is correct.
//
// During teardown the calculus flips back to #2680's proven-safe behavior: the
// position is already durable, so a failed/undelivered ack is a benign
// duplicate on restart, never a gap. So once teardown has begun (tearingDown
// set, or streamCtx canceled by stopStream), an exhausted/failed send is
// dropped and NEVER escalated — escalating during teardown would block on the
// unbuffered errs channel that nothing is reading, a self-inflicted deadlock.
// The stream must stay open during Teardown's bounded drain (Teardown cancels
// streamCtx only after that drain) precisely so these final sends can succeed.
func (s *Source) deliverOneAck(positions []opencdc.Position) {
	attempt := 0
	for {
		cleanup, err := s.preparePluginCall()
		if err != nil {
			// Plugin already torn down; benign (position durable).
			cleanup()
			return
		}
		if s.stream == nil {
			cleanup()
			return
		}
		sendErr := s.stream.Send(pconnector.SourceRunRequest{AckPositions: positions})
		cleanup()
		if sendErr == nil {
			return // delivered
		}

		// If the stream is already being torn down, every further send will
		// resolve to ctx.Canceled the same way; stop retrying and drop
		// (benign — the position is durable, restart re-delivers).
		if s.streamTornDown() {
			return
		}

		attempt++
		if attempt >= s.maxDeferredAckRetries() {
			// Retries exhausted. Escalate only if the plugin is genuinely
			// running (not tearing down): during teardown, dropping is benign
			// and escalating would deadlock on errs.
			if !s.tearingDown.Load() {
				s.Instance.logger.Warn(context.Background()).Err(sendErr).
					Msg("exhausted retries delivering deferred ack to a running source connector plugin; escalating (stream appears broken)")
				s.escalateDeferredAckFailure(sendErr)
			}
			return
		}

		s.Instance.logger.Debug(context.Background()).Err(sendErr).
			Int("attempt", attempt).
			Msg("transient failure delivering deferred ack to running source connector plugin; retrying")
		if !s.backoffDeferredAck(attempt) {
			// Backoff aborted because the stream was torn down; drop (benign).
			return
		}
	}
}

// streamTornDown reports whether the plugin stream's context has been canceled
// (by stopStream in Teardown or run's error path). Once it has, a stream.Send
// resolves to ctx.Canceled rather than an actual delivery, so the delivery
// goroutine treats this as "teardown, stop retrying, drop benign".
func (s *Source) streamTornDown() bool {
	if s.streamCtx == nil {
		return false
	}
	select {
	case <-s.streamCtx.Done():
		return true
	default:
		return false
	}
}

// escalateDeferredAckFailure surfaces a deferred-ack delivery failure to the
// node via errs. It selects on streamCtx.Done() so that if teardown begins
// while it is trying to escalate (the node having stopped reading errs), it
// abandons the escalation instead of blocking forever — keeping the delivery
// goroutine leak-free.
func (s *Source) escalateDeferredAckFailure(err error) {
	if s.streamCtx == nil {
		s.errs <- err
		return
	}
	select {
	case s.errs <- err:
	case <-s.streamCtx.Done():
	}
}

// maxDeferredAckRetries returns the per-source retry bound, honoring a
// test-only override (see the deferredAckMaxRetries field).
func (s *Source) maxDeferredAckRetries() int {
	if s.deferredAckMaxRetries > 0 {
		return s.deferredAckMaxRetries
	}
	return DefaultDeferredAckMaxRetries
}

// backoffDeferredAck sleeps the exponential backoff for the given retry
// attempt (capped, honoring the deferredAckBackoffCap test override), aborting
// early if the stream is torn down. It returns false if the sleep was aborted
// (the caller should stop retrying), true if it elapsed normally.
func (s *Source) backoffDeferredAck(attempt int) bool {
	backoffCap := s.deferredAckBackoffCap
	if backoffCap <= 0 {
		backoffCap = DefaultDeferredAckBackoffCap
	}
	d := backoffCap
	if attempt >= 1 {
		if shift := attempt - 1; shift < 62 {
			if candidate := DefaultDeferredAckBackoffInitial << uint(shift); candidate > 0 && candidate < backoffCap {
				d = candidate
			}
		}
	}

	timer := time.NewTimer(d)
	defer timer.Stop()
	if s.streamCtx == nil {
		<-timer.C
		return true
	}
	select {
	case <-timer.C:
		return true
	case <-s.streamCtx.Done():
		return false
	}
}

// waitDeliveryDrain waits, bounded by timeout, for the deferred-ack delivery
// goroutine to finish delivering everything queued before Teardown closed the
// queue and to exit. On timeout it logs and returns so Teardown can proceed
// (stopStream then makes the goroutine finish fast; any still-undelivered ack
// degrades to a benign duplicate on restart, never a gap).
func (s *Source) waitDeliveryDrain(ctx context.Context, timeout time.Duration) {
	if s.deliveryDone == nil {
		return
	}
	if timeout < 0 {
		timeout = 0
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.deliveryDone:
	case <-ctx.Done():
		s.warnDeliveryDrainTimedOut(ctx)
	case <-timer.C:
		// Double-check it didn't just finish to avoid a spurious warning when
		// the goroutine exits at almost exactly the deadline.
		select {
		case <-s.deliveryDone:
		default:
			s.warnDeliveryDrainTimedOut(ctx)
		}
	}
}

func (s *Source) warnDeliveryDrainTimedOut(ctx context.Context) {
	s.Instance.logger.Warn(ctx).
		Msg("gave up draining pending deferred acks within the teardown budget; proceeding with teardown (safe: at worst a benign duplicate on restart, never a gap — see Teardown's doc comment)")
}

func (s *Source) OnDelete(ctx context.Context) (err error) {
	if s.Instance.LastActiveConfig.Settings == nil {
		return nil // the connector was never started, nothing to trigger
	}

	s.Instance.Lock()
	defer s.Instance.Unlock()

	s.Instance.logger.Debug(ctx).Msg("dispensing source connector plugin")
	s.plugin, err = s.dispenser.DispenseSource()
	if err != nil {
		return err
	}

	_, err = s.triggerLifecycleEvent(ctx, s.Instance.LastActiveConfig.Settings, nil)

	// call teardown to close plugin regardless of the error
	_, tdErr := s.plugin.Teardown(ctx, pconnector.SourceTeardownRequest{})

	s.plugin = nil

	err = cerrors.LogOrReplace(err, tdErr, func() {
		s.Instance.logger.Err(ctx, tdErr).Msg("could not tear down source connector plugin")
	})
	if err != nil {
		return cerrors.Errorf("could not trigger lifecycle event: %w", err)
	}

	return nil
}

// preparePluginCall makes sure the plugin is running and registers a new plugin
// call in the wait group. The returned function should be called in a deferred
// statement to signal the plugin call is over.
func (s *Source) preparePluginCall() (func(), error) {
	s.Instance.RLock()
	defer s.Instance.RUnlock()
	if s.plugin == nil {
		return func() { /* do nothing */ }, plugin.ErrPluginNotRunning
	}
	// increase wait group so Teardown knows a call to the plugin is running
	s.wg.Add(1)
	return s.wg.Done, nil
}

// state returns the SourceState for this connector.
func (s *Source) state() SourceState {
	if s.Instance.State != nil {
		return s.Instance.State.(SourceState)
	}
	return SourceState{}
}

func (s *Source) configure(ctx context.Context) error {
	s.Instance.logger.Trace(ctx).Msg("configuring source connector plugin")
	_, err := s.plugin.Configure(ctx, pconnector.SourceConfigureRequest{Config: s.Instance.Config.Settings})
	if err != nil {
		return cerrors.Errorf("could not configure source connector plugin: %w", err)
	}
	return nil
}

func (s *Source) open(ctx context.Context) error {
	s.Instance.logger.Trace(ctx).Msg("opening source connector plugin")
	_, err := s.plugin.Open(ctx, pconnector.SourceOpenRequest{
		Position: s.state().Position,
	})
	if err != nil {
		return cerrors.Errorf("could not open source connector plugin: %w", err)
	}
	return nil
}

func (s *Source) run(ctx context.Context) error {
	s.Instance.logger.Trace(ctx).Msg("running source connector plugin")
	ctx, stopStream := context.WithCancel(ctx)
	stream := s.plugin.NewStream()
	err := s.plugin.Run(ctx, stream)
	if err != nil {
		stopStream()
		return cerrors.Errorf("could not run source connector plugin: %w", err)
	}
	s.stream = stream.Client()
	s.stopStream = stopStream
	s.streamCtx = ctx
	return nil
}

func (s *Source) triggerLifecycleEvent(ctx context.Context, oldConfig, newConfig map[string]string) (ok bool, err error) {
	if s.isEqual(oldConfig, newConfig) {
		return false, nil // nothing to do, last active config is the same as current one
	}

	defer func() {
		// Older connectors that predate the lifecycle methods return an
		// "Unimplemented" gRPC status, which the protocol client unwraps into
		// pconnector.ErrUnimplemented (a distinct sentinel from plugin.ErrUnimplemented,
		// despite the identical message). Match pconnector's sentinel so we stay
		// backwards compatible instead of fatally erroring. See issue #1999.
		if cerrors.Is(err, pconnector.ErrUnimplemented) {
			s.Instance.logger.Trace(ctx).Msg("lifecycle events not implemented on source connector plugin (it's probably an older connector)")
			err = nil // ignore error to stay backwards compatible
		}
	}()

	switch {
	// created
	case oldConfig == nil && newConfig != nil:
		s.Instance.logger.Trace(ctx).Msg("triggering lifecycle event \"created\" on source connector plugin")
		_, err := s.plugin.LifecycleOnCreated(ctx, pconnector.SourceLifecycleOnCreatedRequest{Config: newConfig})
		if err != nil {
			return false, cerrors.Errorf("error while triggering lifecycle event \"created\": %w", err)
		}
		return true, nil

	// updated
	case oldConfig != nil && newConfig != nil:
		s.Instance.logger.Trace(ctx).Msg("triggering lifecycle event \"updated\" on source connector plugin")
		_, err := s.plugin.LifecycleOnUpdated(ctx, pconnector.SourceLifecycleOnUpdatedRequest{
			ConfigBefore: oldConfig,
			ConfigAfter:  newConfig,
		})
		if err != nil {
			return false, cerrors.Errorf("error while triggering lifecycle event \"updated\": %w", err)
		}
		return true, nil

	// deleted
	case oldConfig != nil && newConfig == nil:
		s.Instance.logger.Trace(ctx).Msg("triggering lifecycle event \"deleted\" on source connector plugin")
		_, err := s.plugin.LifecycleOnDeleted(ctx, pconnector.SourceLifecycleOnDeletedRequest{Config: oldConfig})
		if err != nil {
			return false, cerrors.Errorf("error while triggering lifecycle event \"deleted\": %w", err)
		}
		return true, nil

	// default should never happen
	default:
		// oldConfig/newConfig are connector settings and routinely carry
		// secrets (DB urls with embedded passwords, SASL credentials, access
		// keys). log.RedactAll redacts every value until per-parameter
		// sensitivity metadata exists - see pkg/foundation/log/redact.go.
		s.Instance.logger.Warn(ctx).
			Any("oldConfig", log.RedactAll(oldConfig)).
			Any("newConfig", log.RedactAll(newConfig)).
			Msg("unexpected combination of old and new config")
		// don't return an error when no event was triggered, strictly speaking
		// the action did not fail
		return false, nil
	}
}

func (s *Source) sanitizeRecord(r *opencdc.Record, now string) {
	if r.Key == nil {
		r.Key = opencdc.RawData{}
	}
	if r.Payload.Before == nil {
		r.Payload.Before = opencdc.RawData{}
	}
	if r.Payload.After == nil {
		r.Payload.After = opencdc.RawData{}
	}
	if r.Metadata == nil {
		r.Metadata = opencdc.Metadata{
			opencdc.MetadataReadAt:                   now,
			opencdc.MetadataConduitSourceConnectorID: s.Instance.ID,
		}
	} else {
		if r.Metadata[opencdc.MetadataReadAt] == "" {
			r.Metadata[opencdc.MetadataReadAt] = now
		}
		if r.Metadata[opencdc.MetadataConduitSourceConnectorID] == "" {
			r.Metadata[opencdc.MetadataConduitSourceConnectorID] = s.Instance.ID
		}
	}
}

func (*Source) isEqual(cfg1, cfg2 map[string]string) bool {
	if len(cfg1) != len(cfg2) {
		return false
	}
	for k, v := range cfg1 {
		if w, ok := cfg2[k]; !ok || v != w {
			return false
		}
	}
	return (cfg1 != nil) == (cfg2 != nil)
}
