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
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
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
func (s *Source) Teardown(ctx context.Context) error {
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
	s.Instance.persister.Flush(ctx)
	s.Instance.persister.WaitPendingWrites()

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

	var toSend [][]opencdc.Position
	s.ackMu.Lock()
	if seq > s.durableAckSeq {
		s.durableAckSeq = seq
	}
	i := 0
	for ; i < len(s.pendingAcks) && s.pendingAcks[i].seq <= s.durableAckSeq; i++ {
		toSend = append(toSend, s.pendingAcks[i].positions)
	}
	s.pendingAcks = s.pendingAcks[i:]
	s.ackMu.Unlock()

	for _, positions := range toSend {
		s.sendDeferredAck(positions)
	}
}

// sendDeferredAck sends one previously-queued Ack call's positions to the
// plugin now that the resulting position is known durable. Unlike Ack's own
// (pre-Approach-A) synchronous send, a failure here is never escalated via
// errs, on purpose: the position this ack refers to is already durable
// (that's precisely why onPersistFlushed called this), so failing to
// deliver the message itself is always a benign, already-safe no-op, never a
// data-integrity problem — the plugin will learn about it on the next ack it
// does receive (see the design doc's failure-mode analysis, "crash between
// flush-complete and plugin-ack"). This is not merely a style choice: Teardown
// deliberately cancels the stream's context (stopStream) before forcing the
// final flush that can trigger this exact call, specifically so any
// still-batched position gets flushed and acked while the plugin is still
// considered "running" for preparePluginCall's purposes — which means a
// perfectly expected send here is one racing an already-canceled context,
// returning context.Canceled (not io.EOF; see
// pkg/plugin/connector/builtin/stream.go's inMemoryStreamClient.Send and
// equivalents), not a real transport fault. Escalating that via the
// unbuffered errs channel (as a genuine send failure was before this fix)
// would risk this goroutine blocking forever the moment nothing is left
// reading errs during teardown — a self-inflicted deadlock, not a
// correctness improvement.
func (s *Source) sendDeferredAck(positions []opencdc.Position) {
	cleanup, err := s.preparePluginCall()
	defer cleanup()
	if err != nil {
		// Plugin already torn down; benign, see doc comment above.
		return
	}
	if s.stream == nil {
		return
	}

	if sendErr := s.stream.Send(pconnector.SourceRunRequest{AckPositions: positions}); sendErr != nil {
		s.Instance.logger.Debug(context.Background()).Err(sendErr).
			Msg("failed to send deferred ack to source connector plugin; position is already durable, tolerating as benign (see sendDeferredAck's doc comment)")
	}
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
