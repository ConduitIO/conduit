// Copyright © 2024 Meroxa, Inc.
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

// Package lifecycle contains the logic to manage the lifecycle of pipelines.
// It is responsible for starting, stopping and managing pipelines.
package lifecycle

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/conduitio/conduit-commons/csync"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/measure"
	lifecyclev1 "github.com/conduitio/conduit/pkg/lifecycle"
	"github.com/conduitio/conduit/pkg/lifecycle-poc/funnel"
	"github.com/conduitio/conduit/pkg/pipeline"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/jpillora/backoff"
	"gopkg.in/tomb.v2"
)

// errGracefulShutdownDuringRecovery is an internal sentinel returned by
// StartWithBackoff when a graceful shutdown (StopAll) began while the pipeline
// was parked in the recovery backoff wait. It is never surfaced to callers: the
// cleanup goroutine maps it to a terminal StatusSystemStopped (a graceful stop,
// not a degraded failure) — see runPipeline's recovery arm. Invariant 7: a
// shutdown must finalize the pipeline, not race the shutdown with a restart.
var errGracefulShutdownDuringRecovery = cerrors.New("graceful shutdown during recovery backoff")

type FailureEvent struct {
	// ID is the ID of the pipeline which failed.
	ID    string
	Error error
}

type FailureHandler func(FailureEvent)

// Service manages pipelines.
type Service struct {
	logger log.CtxLogger

	// errRecoveryCfg configures the bounded-backoff auto-recovery loop that
	// restarts a pipeline after a transient (non-fatal) error. Shared with the
	// v1 lifecycle service via the lifecycle.ErrRecoveryCfg type (pure config,
	// no lifecycle coupling — see the arch-v2 recovery-port design). Must be
	// non-nil: buildRunnablePipeline reads it to seed each pipeline's backoff.
	errRecoveryCfg *lifecyclev1.ErrRecoveryCfg

	pipelines  PipelineService
	connectors ConnectorService

	processors       ProcessorService
	connectorPlugins ConnectorPluginService

	handlers         []FailureHandler
	runningPipelines *csync.Map[string, *runnablePipeline]

	// terminalErrors holds the terminal error of a pipeline after it has stopped
	// and been removed from runningPipelines, so WaitPipeline can still report it
	// to a caller that races the pipeline's own cleanup goroutine. Written before
	// the runningPipelines entry is deleted; cleared when the pipeline is started
	// again. Ports the fix applied to the sibling pkg/lifecycle package for the
	// same WaitPipeline lookup-after-delete race — see
	// docs/design-documents/20260706-forceful-stop-test-determinism.md.
	terminalErrors *csync.Map[string, error]

	isGracefulShutdown atomic.Bool
	metricsDisabled    bool
}

// NewService initializes and returns a lifecycle.Service.
func NewService(
	logger log.CtxLogger,
	errRecoveryCfg *lifecyclev1.ErrRecoveryCfg,
	connectors ConnectorService,
	processors ProcessorService,
	connectorPlugins ConnectorPluginService,
	pipelines PipelineService,
	metricsDisabled bool,
) *Service {
	return &Service{
		logger:           logger.WithComponent("lifecycle.Service"),
		errRecoveryCfg:   errRecoveryCfg,
		connectors:       connectors,
		processors:       processors,
		connectorPlugins: connectorPlugins,
		pipelines:        pipelines,
		runningPipelines: csync.NewMap[string, *runnablePipeline](),
		terminalErrors:   csync.NewMap[string, error](),
		metricsDisabled:  metricsDisabled,
	}
}

type runnablePipeline struct {
	pipeline *pipeline.Instance
	w        *funnel.Worker
	t        *tomb.Tomb

	// backoff and recoveryAttempts hold the auto-recovery state. backoff is
	// seeded per build; recoveryAttempts is carried across restarts by Start so
	// MaxRetries actually bounds the retry loop (a reset every restart would make
	// the ceiling unreachable). recoveryAttempts is a pointer so the shared
	// counter survives the rp swap on restart. Mirrors pkg/lifecycle.
	backoff          *backoff.Backoff
	recoveryAttempts *atomic.Int64

	// intentionalStop is set by stopRunnablePipeline's graceful-stop branch,
	// before it calls rp.w.Stop, to mark this run as one an operator (or
	// provisioning.ApplyPlanLive via StopAndWait) deliberately asked to stop —
	// as opposed to a spontaneous failure. runPipeline's cleanup goroutine
	// checks it alongside isGracefulShutdown: a transient (non-fatal) error
	// that surfaces from the drain itself (e.g. a destination write failing
	// while a batch already in flight when Stop was called finishes unwinding)
	// must finalize as StatusUserStopped, never auto-restart via
	// recoverPipeline. Without this, an operator-initiated Stop(force=false)
	// that happens to race a transient drain error is misclassified as a
	// spontaneous transient failure and the pipeline is auto-restarted out
	// from under the operator that just stopped it — the bug this field
	// fixes.
	//
	// Deliberately a plain (non-pointer) atomic.Bool on rp, NOT carried over to
	// a new runnablePipeline the way backoff/recoveryAttempts are (see Start):
	// an intentional stop must never survive a restart. A fresh rp always
	// starts with intentionalStop false, so a pipeline that recovers and later
	// stops for an unrelated reason gets ordinary recovery semantics again, not
	// a stale "this was user-stopped" marker from a previous run.
	intentionalStop atomic.Bool
}

// ConnectorService can fetch and create a connector instance, and report when
// every position/state write already queued for persistence has been
// durably committed — see WaitPersisted's doc (pkg/connector.Service) and
// StopAndWait, which relies on it to await durability after a pipeline has
// fully drained. Mirrors the sibling pkg/lifecycle.ConnectorService interface
// (O1/O2 parity, see StopAndWait's doc).
type ConnectorService interface {
	Get(ctx context.Context, id string) (*connector.Instance, error)
	Create(ctx context.Context, id string, t connector.Type, plugin string, pipelineID string, cfg connector.Config, p connector.ProvisionType) (*connector.Instance, error)
	WaitPersisted()
}

// ProcessorService can fetch a processor instance and make a runnable processor from it.
type ProcessorService interface {
	Get(ctx context.Context, id string) (*processor.Instance, error)
	MakeRunnableProcessor(ctx context.Context, i *processor.Instance) (*processor.RunnableProcessor, error)
}

// ConnectorPluginService can create a connector plugin dispenser.
type ConnectorPluginService interface {
	NewDispenser(logger log.CtxLogger, name string, connectorID string) (connectorPlugin.Dispenser, error)
}

// PipelineService can fetch, list and update the status of a pipeline instance.
type PipelineService interface {
	Get(ctx context.Context, pipelineID string) (*pipeline.Instance, error)
	List(ctx context.Context) map[string]*pipeline.Instance
	UpdateStatus(ctx context.Context, pipelineID string, status pipeline.Status, errMsg string) error
}

// OnFailure registers a handler for a lifecycle.FailureEvent.
// Only errors which happen after a pipeline has been started
// are being sent.
func (s *Service) OnFailure(handler FailureHandler) {
	s.handlers = append(s.handlers, handler)
}

// Init starts all pipelines that have the StatusSystemStopped.
func (s *Service) Init(
	ctx context.Context,
) error {
	var errs []error
	s.logger.Debug(ctx).Msg("initializing pipelines statuses")

	instances := s.pipelines.List(ctx)
	for _, instance := range instances {
		if instance.GetStatus() == pipeline.StatusSystemStopped {
			err := s.Start(ctx, instance.ID)
			if err != nil {
				// try to start remaining pipelines and gather errors
				errs = append(errs, err)
			}
		}
	}

	return cerrors.Join(errs...)
}

// Start builds and starts a pipeline with the given ID.
// If the pipeline is already running, Start returns ErrPipelineRunning.
func (s *Service) Start(
	ctx context.Context,
	pipelineID string,
) error {
	pl, err := s.pipelines.Get(ctx, pipelineID)
	if err != nil {
		return err
	}

	if pl.GetStatus() == pipeline.StatusRunning {
		return cerrors.Errorf("can't start pipeline %s: %w", pl.ID, pipeline.ErrPipelineRunning)
	}

	s.logger.Debug(ctx).Str(log.PipelineIDField, pl.ID).Msg("starting pipeline")
	s.logger.Trace(ctx).Str(log.PipelineIDField, pl.ID).Msg("building tasks")

	rp, err := s.buildRunnablePipeline(ctx, pl)
	if err != nil {
		return cerrors.Errorf("could not build tasks for pipeline %s: %w", pl.ID, err)
	}

	// If this pipeline was already running (i.e. this Start is a recovery
	// restart driven by StartWithBackoff), carry its backoff state onto the new
	// runnablePipeline. Without this, every restart resets the attempt counter
	// and MaxRetries would never bite — an unbounded restart loop. Mirrors
	// pkg/lifecycle.Service.Start.
	if oldRp, ok := s.runningPipelines.Get(pipelineID); ok {
		rp.backoff = oldRp.backoff
		rp.recoveryAttempts = oldRp.recoveryAttempts
	}

	// A new run supersedes any terminal error recorded by a previous run of this
	// pipeline, so a later WaitPipeline can't return a stale result.
	s.terminalErrors.Delete(pipelineID)

	s.logger.Trace(ctx).Str(log.PipelineIDField, pl.ID).Msg("running pipeline")

	if err := s.runPipeline(rp); err != nil {
		return cerrors.Errorf("failed to run pipeline %s: %w", pl.ID, err)
	}
	s.logger.Info(ctx).Str(log.PipelineIDField, pl.ID).Msg("pipeline started")

	s.runningPipelines.Set(pl.ID, rp)

	return nil
}

// Stop will attempt to gracefully stop a given pipeline by calling each worker's
// Stop method. If the force flag is set to true, the pipeline will be stopped
// forcefully by cancelling the context.
func (s *Service) Stop(ctx context.Context, pipelineID string, force bool) error {
	rp, ok := s.runningPipelines.Get(pipelineID)

	if !ok {
		return cerrors.Errorf("pipeline %s is not running: %w", pipelineID, pipeline.ErrPipelineNotRunning)
	}

	if rp.pipeline.GetStatus() != pipeline.StatusRunning && rp.pipeline.GetStatus() != pipeline.StatusRecovering {
		return cerrors.Errorf("can't stop pipeline with status %q: %w", rp.pipeline.GetStatus(), pipeline.ErrPipelineNotRunning)
	}

	return s.stopRunnablePipeline(ctx, rp, force)
}

// StopAll will ask all the running pipelines to stop gracefully
// (i.e. that existing messages get processed but not new messages get produced).
func (s *Service) StopAll(ctx context.Context, force bool) error {
	// Set graceful shutdown flag to true, so pipelines know the system triggered the stop.
	s.isGracefulShutdown.Store(true)

	l := s.runningPipelines.Len()
	if l == 0 {
		return nil
	}

	switch force {
	case false:
		s.logger.Info(ctx).Msgf("stopping %d pipelines gracefully", l)
	case true:
		s.logger.Info(ctx).Msgf("stopping %d pipelines forcefully", l)
	}

	var errs []error
	for _, rp := range s.runningPipelines.All() {
		if rp.pipeline.GetStatus() != pipeline.StatusRunning && rp.pipeline.GetStatus() != pipeline.StatusRecovering {
			continue
		}
		errs = append(errs, s.stopRunnablePipeline(ctx, rp, force))
	}
	return cerrors.Join(errs...)
}

func (s *Service) stopRunnablePipeline(ctx context.Context, rp *runnablePipeline, force bool) error {
	switch force {
	case false:
		s.logger.Info(ctx).
			Str(log.PipelineIDField, rp.pipeline.ID).
			Any(log.PipelineStatusField, rp.pipeline.GetStatus()).
			Msg("gracefully stopping pipeline")

		// Invariant 3/7: mark this run as an intentional (operator-initiated)
		// stop BEFORE calling rp.w.Stop, so that if the drain itself surfaces a
		// transient (non-fatal) error — e.g. a batch already in flight when
		// Stop was called finishes unwinding with a destination write failure —
		// runPipeline's cleanup goroutine (see the intentionalStop check there)
		// finalizes it as StatusUserStopped instead of misreading it as a
		// spontaneous failure and auto-restarting via recoverPipeline. See the
		// intentionalStop field doc.
		rp.intentionalStop.Store(true)
		err := rp.w.Stop(ctx)
		if err != nil {
			// rp.w.Stop failed outright (e.g. it never got past acquiring the
			// processing lock — see the O2 drain bound in StopAndWait, which
			// passes a deadline-bound ctx here). w.stop was therefore never
			// actually set on the worker: the pipeline is still genuinely
			// running, unattended, exactly as before this call. Clear the
			// marker so a LATER, unrelated transient error is still eligible
			// for ordinary auto-recovery instead of being permanently (and
			// incorrectly) treated as an already-completed user stop.
			rp.intentionalStop.Store(false)
		}
		return err
	case true:
		s.logger.Info(ctx).
			Str(log.PipelineIDField, rp.pipeline.ID).
			Any(log.PipelineStatusField, rp.pipeline.GetStatus()).
			Msg("force stopping pipeline")
		// Invariant 3/7: a user force-stop is a deliberate terminal action, not a
		// transient failure. Tag it fatal (matching v1's stopForceful,
		// pkg/lifecycle/service.go) so the cleanup goroutine's IsFatalError check
		// (see the switch on rp.t.Err() below) classifies it as terminal and error
		// recovery — once wired in — never auto-restarts a pipeline the user
		// explicitly stopped.
		rp.t.Kill(cerrors.FatalError(pipeline.ErrForceStop))
		return nil
	}
	panic("unreachable")
}

// Wait blocks until all pipelines are stopped or until the timeout is reached.
// Returns:
//
// (1) nil if all the pipelines are gracefully stopped,
//
// (2) an error, if the pipelines could not have been gracefully stopped,
//
// (3) context.DeadlineExceeded if the pipelines were not stopped within the given timeout.
func (s *Service) Wait(timeout time.Duration) error {
	gracefullyStopped := make(chan struct{})
	var err error
	go func() {
		defer close(gracefullyStopped)
		err = s.waitInternal()
	}()

	select {
	case <-gracefullyStopped:
		return err
	case <-time.After(timeout):
		return context.DeadlineExceeded
	}
}

// waitInternal blocks until all pipelines are stopped and returns an error if any of
// the pipelines failed to stop gracefully.
func (s *Service) waitInternal() error {
	var errs []error

	// copy pipelines to keep the map unlocked while we iterate it
	pipelines := s.runningPipelines.Copy()

	for _, rp := range pipelines.All() {
		if rp.t == nil {
			continue
		}
		err := rp.t.Wait()
		if err != nil {
			errs = append(errs, cerrors.Errorf("pipeline %s: %w", rp.pipeline.ID, err))
		}
	}
	return cerrors.Join(errs...)
}

// WaitPipeline blocks until the pipeline with the given ID is stopped, and
// returns the pipeline's terminal error (nil on a graceful stop).
//
// It is safe to call before, during, or after the pipeline's own cleanup: while
// the pipeline is running it waits on the tomb; if the pipeline has already
// stopped and removed itself from runningPipelines, it returns the recorded
// terminal error instead of a false nil. Returns nil for an ID that never ran.
//
// Without this fallback there is a time-of-check/time-of-use race: the cleanup
// goroutine (runPipeline) can call runningPipelines.Delete(id) between this
// method's lookup and return, in which case a naive "!ok -> return nil" drops
// the terminal error the caller was waiting for. See
// docs/design-documents/20260706-forceful-stop-test-determinism.md, which
// diagnosed and fixed the identical bug in the sibling pkg/lifecycle package.
func (s *Service) WaitPipeline(id string) error {
	p, ok := s.runningPipelines.Get(id)
	if ok && p.t != nil {
		return p.t.Wait()
	}
	// The pipeline already cleaned itself up (or never started under this ID).
	// terminalErrors is written before the runningPipelines entry is deleted, so
	// if the pipeline ran and stopped, its terminal error is here — recovering
	// the result the lookup above would otherwise have lost to the cleanup race.
	if err, ok := s.terminalErrors.Get(id); ok {
		return err
	}
	return nil
}

// DefaultStopAndWaitTimeout bounds the end-to-end StopAndWait sequence (Stop +
// drain-wait + persistence-wait) — see StopAndWait's doc, "O2: bounding the
// drain". A wedged destination (Write that never returns) would otherwise
// hold the pipeline's processingLock forever, so acquireProcessingLock's own
// ctx (threaded through Stop -> funnel.Worker.Stop) never gets a deadline
// unless StopAndWait supplies one — this constant is that deadline. Chosen
// generously (well above a typical destination write timeout) so a merely
// slow — not actually wedged — destination isn't spuriously aborted; see
// docs/design-documents/20260731-archv2-drain-reconfigure.md, "O2".
const DefaultStopAndWaitTimeout = 30 * time.Second

// StopAndWait gracefully stops the pipeline with the given ID and blocks until
// it has reached full quiescence (every worker goroutine has exited — see
// WaitPipeline) AND every connector position/state write that drain triggered
// has been durably flushed to the store (see connectors.WaitPersisted). It
// ports pkg/lifecycle.Service.StopAndWait's contract to the funnel/arch-v2
// lifecycle — see that method's doc for the full invariant-1/3 rationale
// (never let a caller mutate/restart a pipeline whose drain or flush hasn't
// actually completed) and
// docs/design-documents/20260731-archv2-drain-reconfigure.md for the audit
// (§3.1, "the funnel drain audit") that establishes this package's specific
// Stop/WaitPipeline/Persister interaction gives the same guarantee:
//
//   - funnel.Worker's processingLock (acquired by Worker.Stop, held by the
//     first/source task for the lifetime of a batch) guarantees no batch is
//     mid-flight the instant Stop's lock acquisition succeeds — quiescence.
//   - A batch that was read but never finished processing before the stop
//     signal (worker.go's doTask, "stop signal received just before starting
//     to process next batch") is thrown away WITHOUT acking: the source's
//     position is never advanced past it, so a restart re-reads it — a benign
//     duplicate, never a gap (invariants 1/3).
//   - connector.Source.Teardown (called from Worker.Stop, tearDownSource)
//     forces the persister to flush and waits (bounded by
//     connector.DefaultTeardownFlushTimeout) for the deferred ack to drain —
//     durability for whatever WAS acked.
//   - WaitPipeline (the tomb join) and connectors.WaitPersisted (the
//     persister's pending-write barrier) are the pipeline-wide barriers that
//     let a caller observe both of the above have actually completed, not just
//     been triggered.
//
// O2 (bounding the drain): unlike pkg/lifecycle's StopAndWait, this method
// bounds the entire sequence — DefaultStopAndWaitTimeout, or a tighter
// deadline already set on ctx, whichever is sooner — because a wedged
// destination Write blocks the batch that holds processingLock forever, which
// would otherwise hang Stop (and thus StopAndWait, and thus
// provisioning.Service.ApplyPlanLive) indefinitely. On timeout this returns a
// CodeStopAndWaitTimeout error and does NOT force-kill anything: whichever
// step timed out (Stop, the drain wait, or the persistence wait) leaves the
// pipeline in the exact state connector.Source.Teardown's own bounded-wait
// fallback already established as safe (source.go's Teardown doc) — at worst
// a benign duplicate on a later restart, never a gap. If Stop itself times
// out, the worker's internal stop flag was never actually set (see
// stopRunnablePipeline's rollback of intentionalStop on that path), so the
// pipeline is simply still running, unattended, exactly as it was before this
// call — safe to retry.
//
// StopAndWait requires the pipeline to already be running (it delegates to
// Stop, which returns pipeline.ErrPipelineNotRunning-coded errors otherwise)
// and only ever stops gracefully.
func (s *Service) StopAndWait(ctx context.Context, pipelineID string) error {
	deadline := time.Now().Add(DefaultStopAndWaitTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d // honor a tighter caller-supplied deadline
	}
	stopCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	if err := s.Stop(stopCtx, pipelineID, false); err != nil {
		if cerrors.Is(err, context.DeadlineExceeded) {
			return s.stopAndWaitTimeoutErr(pipelineID, "stop", err)
		}
		return cerrors.Errorf("could not stop pipeline %s: %w", pipelineID, err)
	}

	if err := waitBounded(time.Until(deadline), func() error { return s.WaitPipeline(pipelineID) }); err != nil {
		if cerrors.Is(err, context.DeadlineExceeded) {
			return s.stopAndWaitTimeoutErr(pipelineID, "drain", err)
		}
		return cerrors.Errorf("pipeline %s did not stop gracefully: %w", pipelineID, err)
	}

	// Invariant 1/3: do not return — and thus do not let a caller mutate or
	// tear down this pipeline's connectors — until every position/state write
	// the drain above already triggered is durably persisted.
	if err := waitBounded(time.Until(deadline), func() error { s.connectors.WaitPersisted(); return nil }); err != nil {
		return s.stopAndWaitTimeoutErr(pipelineID, "persist", err)
	}

	return nil
}

// stopAndWaitTimeoutErr builds the coded, actionable error StopAndWait returns
// when the bounded drain (O2) elapses during the named phase ("stop", "drain",
// or "persist").
func (s *Service) stopAndWaitTimeoutErr(pipelineID, phase string, cause error) error {
	ce := conduiterr.Wrap(CodeStopAndWaitTimeout, fmt.Sprintf(
		"timed out waiting for pipeline %q to %s within %s; the pipeline was not force-stopped and is left exactly as it was — safe to retry",
		pipelineID, phase, DefaultStopAndWaitTimeout,
	), cause)
	ce.Suggestion = "check the destination/DLQ for a stuck write, then retry; this never drops or duplicates a record beyond the normal at-least-once contract"
	return ce
}

// waitBounded runs fn in a goroutine and returns its result, or
// context.DeadlineExceeded if timeout elapses first. fn's goroutine is not
// itself canceled on timeout (mirrors Persister.WaitPendingWritesContext's own
// doc on this point) — if it eventually completes, its result is simply
// discarded once the caller has already returned.
func waitBounded(timeout time.Duration, fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()

	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return context.DeadlineExceeded
	}
}

// ReconfigureProcessor always returns lifecyclev1.ErrProcessorNotLiveReconfigurable
// under the experimental Preview.PipelineArchV2 lifecycle service (O1): unlike
// pkg/lifecycle, this arch has no live in-place hot-swap capability at all yet
// (no equivalent of stream.ProcessorNode.Reconfigure), so every reconfigure
// request is, structurally, "not live-reconfigurable" — the caller must fall
// back to a restart.
//
// Reusing the v1 sentinel (rather than a v2-specific one) is deliberate: the
// only caller, provisioning.Service.applyInPlace, already matches
// cerrors.Is(err, lifecycle.ErrProcessorNotLiveReconfigurable) to decide
// whether to fall back to StopAndWait+Start — reusing it here means
// applyInPlace needs no arch-v2-specific branch, and the package coupling
// already exists (this file already imports lifecyclev1 for ErrRecoveryCfg).
func (s *Service) ReconfigureProcessor(_ context.Context, pipelineID, processorID string) error {
	return cerrors.Errorf("%w: processor %q in pipeline %q (Preview.PipelineArchV2 has no live in-place reconfigure yet)",
		lifecyclev1.ErrProcessorNotLiveReconfigurable, processorID, pipelineID)
}

// buildRunnablePipeline will build and connect all tasks configured in the pipeline.
func (s *Service) buildRunnablePipeline(
	ctx context.Context,
	pl *pipeline.Instance,
) (*runnablePipeline, error) {
	pipelineLogger := s.logger
	pipelineLogger.Logger = pipelineLogger.Logger.With().Str(log.PipelineIDField, pl.ID).Logger()

	srcTasks, err := s.buildSourceTasks(ctx, pl, pipelineLogger)
	if err != nil {
		return nil, cerrors.Errorf("failed to build source tasks: %w", err)
	}
	if len(srcTasks) == 0 {
		return nil, cerrors.New("can't build pipeline without any source connectors")
	}

	destTasks, err := s.buildDestinationTasks(ctx, pl, pipelineLogger)
	if err != nil {
		return nil, cerrors.Errorf("failed to build destination tasks: %w", err)
	}
	if len(destTasks) == 0 {
		return nil, cerrors.New("can't build pipeline without any destination connectors")
	}

	procTasks, err := s.buildProcessorTasks(ctx, pl, pl.ProcessorIDs, pipelineLogger)
	if err != nil {
		return nil, cerrors.Errorf("failed to build pipeline processor tasks: %w", err)
	}

	dlq, err := s.buildDLQ(ctx, pl, pipelineLogger)
	if err != nil {
		return nil, cerrors.Errorf("failed to build DLQ: %w", err)
	}

	taskNodes, err := s.buildTaskNodes(srcTasks, procTasks, destTasks)
	if err != nil {
		return nil, cerrors.Errorf("failed to build task nodes: %w", err)
	}

	// TODO(multi-connector): when we have multiple connectors we will have more than one task node
	taskNode := taskNodes[0]

	// log the tasks and order for debugging purposes
	taskTypes := make([]string, 0)
	for task := range taskNode.Tasks() {
		taskTypes = append(taskTypes, fmt.Sprintf("%s(%T)", task.ID(), task))
	}
	pipelineLogger.Info(ctx).Any("tasks", taskTypes).Msg("pipeline tasks")

	worker, err := funnel.NewWorker(
		taskNode,
		dlq,
		pipelineLogger,
		measure.PipelineExecutionDurationTimer.WithValues(pl.Config.Name),
	)
	if err != nil {
		return nil, cerrors.Errorf("failed to create worker: %w", err)
	}
	return &runnablePipeline{
		pipeline: pl,
		w:        worker,
		// Seed a fresh backoff and attempt counter. Start carries these onto the
		// next runnablePipeline across a recovery restart. Mirrors
		// pkg/lifecycle.buildRunnablePipeline; the backoff parameters come from
		// the shared lifecycle.ErrRecoveryCfg (equivalent to its toBackoff()).
		backoff: &backoff.Backoff{
			Min:    s.errRecoveryCfg.MinDelay,
			Max:    s.errRecoveryCfg.MaxDelay,
			Factor: float64(s.errRecoveryCfg.BackoffFactor),
			Jitter: true,
		},
		recoveryAttempts: &atomic.Int64{},
	}, nil
}

// buildTaskNodes takes the source, processor and destination tasks and builds
// a task node graph. The returned slice contains the first task nodes in every
// branch of the graph. The other task nodes are connected to the first task node
// in their branch.
func (s *Service) buildTaskNodes(
	srcTasks [][]funnel.Task,
	procTasks []funnel.Task,
	destTasks [][]funnel.Task,
) ([]*funnel.TaskNode, error) {
	// TODO(multi-connector): when we have multiple connectors this will not be as straight forward
	srcTasksBranch := srcTasks[0]   // we only support one source connector for now
	destTasksBranch := destTasks[0] // we only support one destination connector for now

	taskNode := &funnel.TaskNode{Task: srcTasksBranch[0]}
	for _, task := range srcTasksBranch[1:] {
		err := taskNode.AppendToEnd(&funnel.TaskNode{Task: task})
		if err != nil {
			return nil, cerrors.Errorf("failed to append task to task node list: %w", err)
		}
	}
	for _, task := range procTasks {
		err := taskNode.AppendToEnd(&funnel.TaskNode{Task: task})
		if err != nil {
			return nil, cerrors.Errorf("failed to append task to task node list: %w", err)
		}
	}
	for _, task := range destTasksBranch {
		err := taskNode.AppendToEnd(&funnel.TaskNode{Task: task})
		if err != nil {
			return nil, cerrors.Errorf("failed to append task to task node list: %w", err)
		}
	}

	return []*funnel.TaskNode{taskNode}, nil
}

func (s *Service) buildSourceTasks(
	ctx context.Context,
	pl *pipeline.Instance,
	logger log.CtxLogger,
) ([][]funnel.Task, error) {
	var tasks [][]funnel.Task

	for _, connID := range pl.ConnectorIDs {
		instance, err := s.connectors.Get(ctx, connID)
		if err != nil {
			return nil, cerrors.Errorf("could not fetch connector: %w", err)
		}

		if instance.Type != connector.TypeSource {
			continue // skip any connector that's not a source
		}

		if len(tasks) > 0 {
			// TODO(multi-connector): remove check
			return nil, cerrors.New("pipelines with multiple source connectors currently not supported, please disable the experimental feature flag")
		}

		src, err := instance.Connector(ctx, s.connectorPlugins)
		if err != nil {
			return nil, err
		}

		srcTask := funnel.NewSourceTask(
			instance.ID,
			src.(*connector.Source),
			logger,
			s.newConnectorMetrics(pl.Config.Name, instance),
		)

		// Add processor tasks
		procTasks, err := s.buildProcessorTasks(ctx, pl, instance.ProcessorIDs, logger)
		if err != nil {
			return nil, cerrors.Errorf("failed to build source processor tasks: %w", err)
		}

		// Build the slice of tasks for this source
		srcTasks := make([]funnel.Task, 0)
		srcTasks = append(srcTasks, srcTask)
		srcTasks = append(srcTasks, procTasks...)
		tasks = append(tasks, srcTasks)
	}

	return tasks, nil
}

func (s *Service) buildDestinationTasks(
	ctx context.Context,
	pl *pipeline.Instance,
	logger log.CtxLogger,
) ([][]funnel.Task, error) {
	var tasks [][]funnel.Task

	for _, connID := range pl.ConnectorIDs {
		instance, err := s.connectors.Get(ctx, connID)
		if err != nil {
			return nil, cerrors.Errorf("could not fetch connector: %w", err)
		}

		if instance.Type != connector.TypeDestination {
			continue // skip any connector that's not a destination
		}

		if len(tasks) > 0 {
			// TODO(multi-connector): remove check
			return nil, cerrors.New("pipelines with multiple destination connectors currently not supported, please disable the experimental feature flag")
		}

		dest, err := instance.Connector(ctx, s.connectorPlugins)
		if err != nil {
			return nil, err
		}

		destTask := funnel.NewDestinationTask(
			instance.ID,
			dest.(*connector.Destination),
			logger,
			s.newConnectorMetrics(pl.Config.Name, instance),
		)

		// Add processor tasks
		procTasks, err := s.buildProcessorTasks(ctx, pl, instance.ProcessorIDs, logger)
		if err != nil {
			return nil, cerrors.Errorf("failed to build destination processor tasks: %w", err)
		}

		// Build the slice of tasks for this destination
		destTasks := make([]funnel.Task, 0)
		destTasks = append(destTasks, destTask)
		destTasks = append(destTasks, procTasks...)
		tasks = append(tasks, destTasks)
	}

	return tasks, nil
}

func (s *Service) buildProcessorTasks(
	ctx context.Context,
	pl *pipeline.Instance,
	processorIDs []string,
	logger log.CtxLogger,
) ([]funnel.Task, error) {
	var tasks []funnel.Task

	for _, procID := range processorIDs {
		instance, err := s.processors.Get(ctx, procID)
		if err != nil {
			return nil, cerrors.Errorf("could not fetch processor: %w", err)
		}

		runnableProc, err := s.processors.MakeRunnableProcessor(ctx, instance)
		if err != nil {
			return nil, err
		}

		tasks = append(
			tasks,
			funnel.NewProcessorTask(
				instance.ID,
				runnableProc,
				logger,
				s.newProcessorMetrics(pl.Config.Name, instance.Plugin, instance.ID),
			),
		)
	}

	return tasks, nil
}

func (s *Service) buildDLQ(
	ctx context.Context,
	pl *pipeline.Instance,
	logger log.CtxLogger,
) (*funnel.DLQ, error) {
	conn, err := s.connectors.Create(
		ctx,
		pl.ID+"-dlq",
		connector.TypeDestination,
		pl.DLQ.Plugin,
		pl.ID,
		connector.Config{
			Name:     pl.ID + "-dlq",
			Settings: pl.DLQ.Settings,
		},
		connector.ProvisionTypeDLQ, // the provision type ensures the connector won't be persisted
	)
	if err != nil {
		return nil, cerrors.Errorf("failed to create DLQ destination: %w", err)
	}

	dest, err := conn.Connector(ctx, s.connectorPlugins)
	if err != nil {
		return nil, err
	}

	return funnel.NewDLQ(
		"dlq",
		dest.(*connector.Destination),
		logger,
		s.newDLQMetrics(pl.Config.Name, conn.Plugin),
		pl.DLQ.WindowSize,
		pl.DLQ.WindowNackThreshold,
	), nil
}

func (s *Service) runPipeline(rp *runnablePipeline) error {
	if rp.t != nil && rp.t.Alive() {
		return pipeline.ErrPipelineRunning
	}

	// the tomb is responsible for running goroutines related to the pipeline
	rp.t = &tomb.Tomb{}
	ctx := rp.t.Context(nil) //nolint:staticcheck // this is the correct usage of tomb

	err := rp.w.Open(ctx)
	if err != nil {
		return cerrors.Errorf("failed to open worker: %w", err)
	}

	var workersWg sync.WaitGroup

	// startupDone is closed once the initial "running" status write below has
	// fully completed. The cleanup goroutine waits on it before writing its own
	// terminal status to the same *pipeline.Instance.
	//
	// pipeline.Service.UpdateStatus is not safe to call concurrently for the same
	// ID: SetStatus is lock-guarded, but the errMsg field write and the store's
	// JSON-encode of the whole instance for persistence are not. With the mocks
	// used in tests there is no real I/O delay, so the worker can run to
	// completion and the cleanup goroutine can reach its own UpdateStatus call
	// while the initial UpdateStatus(StatusRunning) call below is still in
	// flight, corrupting whichever field loses the race and, worst case,
	// clobbering a correct terminal status back to "running" — confirmed by
	// repro: `-race -shuffle=on -count=1500` under CPU load caught the two
	// UpdateStatus calls racing on the same struct.
	//
	// Both t.Go calls below stay adjacent (nothing slow between them) so
	// tomb.alive reaches 2 before either goroutine can possibly finish; ordering
	// the UpdateStatus calls via this channel instead of via t.Go call order
	// avoids a second bug that surfaced when this was first tried by
	// interleaving a synchronous UpdateStatus between the two t.Go calls: if the
	// worker finishes before that call returns, tomb.alive can hit 0 before the
	// cleanup goroutine is even registered, and the later t.Go panics with
	// "tomb.Go called after all goroutines terminated".
	startupDone := make(chan struct{})

	// TODO(multi-connector): when we have multiple connectors spawn a worker for each source
	workersWg.Add(1)
	rp.t.Go(func() error {
		defer workersWg.Done()

		doErr := rp.w.Do(ctx)
		s.logger.Err(ctx, doErr).Str(log.PipelineIDField, rp.pipeline.ID).Msg("pipeline worker stopped")

		closeErr := rp.w.Close(context.Background())
		err := cerrors.Join(doErr, closeErr)
		if err != nil {
			err = cerrors.Errorf("worker stopped with error: %w", err)
			// Record the reason on the tomb synchronously, before returning (and
			// thus before the deferred workersWg.Done() above fires). Without
			// this, tomb.v2 only records a t.Go'd function's return value in its
			// *own* post-return bookkeeping (t.run, after f() returns) — which
			// races the cleanup goroutine below waking from workersWg.Wait() and
			// reading rp.t.Err(). Losing that race makes the cleanup goroutine
			// observe tomb.ErrStillAlive for a pipeline that actually died with a
			// fatal error, misreporting it as gracefully stopped (status
			// UserStopped/SystemStopped instead of Degraded, dropping the error
			// entirely) — confirmed by repro under `-race -count=500`. Kill is
			// idempotent and safe to call here: t.run's own kill(err) call after
			// this function returns is then a no-op (reason already set).
			rp.t.Kill(err)
			return err
		}

		return nil
	})

	rp.t.Go(func() error {
		// Use fresh context for cleanup function, otherwise the updated status
		// will potentially fail to be stored.
		ctx := context.Background()

		workersWg.Wait()
		// Wait for the initial StatusRunning write below to fully finish before
		// this goroutine writes its own terminal status to the same
		// *pipeline.Instance. See the comment on startupDone above.
		<-startupDone
		err := rp.t.Err()

		switch err {
		case tomb.ErrStillAlive:
			// not an actual error, the pipeline stopped gracefully
			err = nil
			var status pipeline.Status
			if s.isGracefulShutdown.Load() {
				// it was triggered by a graceful shutdown of Conduit
				status = pipeline.StatusSystemStopped
			} else {
				// it was manually triggered by a user
				status = pipeline.StatusUserStopped
			}
			if err := s.pipelines.UpdateStatus(ctx, rp.pipeline.ID, status, ""); err != nil {
				return err
			}
		default:
			switch {
			case cerrors.IsFatalError(err):
				// Invariant 3/7: a fatal terminal error (including a user
				// force-stop, which stopRunnablePipeline fatal-tags) is never
				// auto-recovered — it degrades. we use %+v to get the stack trace too.
				if err := s.pipelines.UpdateStatus(ctx, rp.pipeline.ID, pipeline.StatusDegraded, fmt.Sprintf("%+v", err)); err != nil {
					return err
				}
			case s.isGracefulShutdown.Load():
				// Transient error that fired while Conduit is already shutting
				// down: do not start a recovery loop that would race the
				// shutdown (invariant 7). Finalize as a system stop. This is a
				// deliberate v2 improvement over v1, which does not gate its
				// error arm on graceful shutdown — flagged in the PR.
				err = nil
				if updateErr := s.pipelines.UpdateStatus(ctx, rp.pipeline.ID, pipeline.StatusSystemStopped, ""); updateErr != nil {
					return updateErr
				}
			case rp.intentionalStop.Load():
				// Invariant 3/7: an operator (or provisioning.ApplyPlanLive via
				// StopAndWait) deliberately asked THIS pipeline to stop — see
				// stopRunnablePipeline, which sets intentionalStop before
				// calling rp.w.Stop. A transient (non-fatal) error surfacing
				// from that deliberate drain must never be misread as a
				// spontaneous failure needing recovery: auto-restarting here
				// would restart a pipeline the operator just stopped, exactly
				// the race the recovery port introduced (O3). Finalize as
				// StatusUserStopped, mirroring the tomb.ErrStillAlive branch's
				// clean-stop status, but via this arm because the drain itself
				// returned an error instead of returning cleanly.
				err = nil
				if updateErr := s.pipelines.UpdateStatus(ctx, rp.pipeline.ID, pipeline.StatusUserStopped, ""); updateErr != nil {
					return updateErr
				}
			default:
				// Transient (non-fatal) error: attempt bounded-backoff recovery.
				recoveryErr := s.recoverPipeline(ctx, rp)
				switch {
				case recoveryErr == nil:
					// Recovery restarted the pipeline (or an external Start
					// already replaced the running entry). The live run now owns
					// terminal cleanup, so return early WITHOUT running the
					// cleanup tail below — deleting the runningPipelines entry
					// here would delete the new run's entry. Mirrors v1's
					// return nil (pkg/lifecycle/service.go). This early return is
					// also what lets StartWithBackoff's "am I still the live
					// pipeline" guard observe a concurrent restart during the
					// backoff wait: the old entry must stay in runningPipelines
					// until Start swaps in the new one.
					return nil
				case cerrors.Is(recoveryErr, errGracefulShutdownDuringRecovery):
					// A graceful shutdown began while we were parked in the
					// backoff wait. Finalize as a system stop, not a degraded
					// failure, and run the cleanup tail so the entry is removed.
					err = nil
					if updateErr := s.pipelines.UpdateStatus(ctx, rp.pipeline.ID, pipeline.StatusSystemStopped, ""); updateErr != nil {
						return updateErr
					}
				default:
					// Recovery is exhausted (MaxRetries) or itself errored.
					s.logger.
						Err(ctx, err).
						Str(log.PipelineIDField, rp.pipeline.ID).
						Msg("pipeline recovery failed")

					if updateErr := s.pipelines.UpdateStatus(ctx, rp.pipeline.ID, pipeline.StatusDegraded, fmt.Sprintf("%+v", recoveryErr)); updateErr != nil {
						return updateErr
					}
					// assign so it's the terminal error recorded and notified below.
					err = recoveryErr
				}
			}
		}

		s.logger.
			Err(ctx, err).
			Str(log.PipelineIDField, rp.pipeline.ID).
			Msg("pipeline stopped")

		// Record the terminal error before removing the pipeline from
		// runningPipelines, so a WaitPipeline caller that races this cleanup still
		// sees the result instead of a false nil (ordering matters: set before
		// delete leaves no window where neither is observable).
		s.terminalErrors.Set(rp.pipeline.ID, err)

		// confirmed that all nodes stopped, we can now remove the pipeline from the running pipelines
		s.runningPipelines.Delete(rp.pipeline.ID)

		s.notify(rp.pipeline.ID, err)
		return err
	})

	// Both goroutines are now registered (tomb.alive holds them alive regardless
	// of how fast either finishes), so it's now safe to make the potentially slow
	// UpdateStatus call and then release the cleanup goroutine to make its own.
	// close(startupDone) unconditionally, including on error, so the cleanup
	// goroutine (already blocked on it) is never left hanging.
	err = s.pipelines.UpdateStatus(ctx, rp.pipeline.ID, pipeline.StatusRunning, "")
	close(startupDone)
	return err
}

// recoverPipeline attempts to recover a pipeline that stopped with a transient
// (non-fatal) error. It marks the pipeline StatusRecovering and hands off to
// StartWithBackoff, which waits out the backoff and restarts the pipeline.
//
// Restart-from-position correctness (invariants 1 & 3) is a connector/persister
// property, not a lifecycle one: by the time this runs, the cleanup goroutine
// has already joined the worker goroutine (workersWg.Wait in runPipeline), and
// that goroutine unconditionally ran Worker.Close → Source.Teardown, which
// forces a persister flush and waits for pending writes (pkg/connector/source.go
// Teardown). So every position the old worker durably acked is persisted before
// this restart builds a fresh worker whose Source.Open resumes from that
// position: no acked record is re-read as un-acked, and no un-acked record is
// skipped. The restart re-reads and re-processes anything not yet durably acked
// (at-least-once).
func (s *Service) recoverPipeline(ctx context.Context, rp *runnablePipeline) error {
	s.logger.Trace(ctx).Str(log.PipelineIDField, rp.pipeline.ID).Msg("recovering pipeline")
	if !s.metricsDisabled {
		measure.PipelineRecoveringCount.WithValues(rp.pipeline.Config.Name).Inc()
	}

	err := s.pipelines.UpdateStatus(ctx, rp.pipeline.ID, pipeline.StatusRecovering, "")
	if err != nil {
		return err
	}

	// Exit the goroutine and attempt to restart the pipeline.
	return s.StartWithBackoff(ctx, rp)
}

// StartWithBackoff waits out the recovery backoff for rp, then restarts the
// pipeline. It bounds the number of restarts via ErrRecoveryCfg.MaxRetries
// (InfiniteRetriesErrRecovery disables the bound), returning a fatal
// ErrPipelineCannotRecover once the bound is exceeded so the caller degrades the
// pipeline. Ported from pkg/lifecycle.Service.StartWithBackoff.
//
// Return contract (interpreted by runPipeline's recovery arm):
//   - nil: the pipeline was restarted, or an external Start already replaced the
//     running entry while we waited — either way the live run owns terminal
//     cleanup and the caller must NOT run its cleanup tail.
//   - errGracefulShutdownDuringRecovery: a graceful shutdown began during the
//     backoff wait; the caller finalizes a system stop instead of restarting.
//   - any other error: a fatal recovery failure (MaxRetries exhausted) or a
//     Start error; the caller degrades the pipeline.
func (s *Service) StartWithBackoff(ctx context.Context, rp *runnablePipeline) error {
	// Increment number of recovery attempts. recoveryAttempts is shared across
	// restarts (carried over in Start), so this bounds the whole retry sequence,
	// not a single restart.
	attempt := rp.recoveryAttempts.Add(1)

	if s.errRecoveryCfg.MaxRetries != lifecyclev1.InfiniteRetriesErrRecovery && attempt > s.errRecoveryCfg.MaxRetries {
		return cerrors.FatalError(cerrors.Errorf("failed to recover pipeline %s after %d attempts: %w", rp.pipeline.ID, attempt, pipeline.ErrPipelineCannotRecover))
	}

	duration := rp.backoff.ForAttempt(float64(attempt))
	s.logger.Info(ctx).
		Str(log.PipelineIDField, rp.pipeline.ID).
		Dur(log.DurationField, duration).
		Int64(log.AttemptField, attempt).
		Msg("restarting with backoff")

	// Retry-window reset: decrement the attempt counter after the backoff plus a
	// stable window, so a pipeline that recovers and stays healthy past the
	// window effectively resets its backoff, while sustained flapping within the
	// window accumulates toward MaxRetries. Ported verbatim from v1; note v1
	// implements only this timer-based reset, not the per-successful-record reset
	// the design doc also mentions (documented, not shipped).
	time.AfterFunc(duration+s.errRecoveryCfg.MaxRetriesWindow, func() {
		s.logger.Debug(ctx).
			Str(log.PipelineIDField, rp.pipeline.ID).
			Dur(log.DurationField, duration).
			Int64(log.AttemptField, attempt).
			Msg("decreasing recovery attempts")
		rp.recoveryAttempts.Add(-1) // Decrement the number of attempts after delay.
	})

	// This results in a default delay progression of 1s, 2s, 4s, 8s, 16s, [...],
	// 10m, 10m,... balancing recovery time against downtime.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(duration):
	}

	// The user may have stopped or restarted the pipeline while we were waiting.
	// If the live entry is no longer this rp, an external Start already replaced
	// it — that run owns cleanup, so return nil and do not restart.
	actualRp, ok := s.runningPipelines.Get(rp.pipeline.ID)
	if !ok || actualRp != rp {
		return nil
	}

	// If a graceful shutdown began while we waited, do not restart — finalize a
	// system stop instead (invariant 7). Checked after the guard so a legitimate
	// concurrent restart still wins.
	if s.isGracefulShutdown.Load() {
		return errGracefulShutdownDuringRecovery
	}

	return s.Start(ctx, rp.pipeline.ID)
}

// notify notifies all registered FailureHandlers about an error.
func (s *Service) notify(pipelineID string, err error) {
	if err == nil {
		return
	}
	e := FailureEvent{
		ID:    pipelineID,
		Error: err,
	}
	for _, handler := range s.handlers {
		handler(e)
	}
}

func (s *Service) newConnectorMetrics(pipelineName string, instance *connector.Instance) funnel.ConnectorMetrics {
	if s.metricsDisabled {
		return &funnel.NoOpConnectorMetrics{}
	}

	return funnel.NewConnectorMetrics(
		pipelineName,
		instance.Plugin,
		instance.Type,
		instance.ID,
	)
}

func (s *Service) newProcessorMetrics(pipelineName, plugin, componentID string) funnel.ProcessorMetrics {
	if s.metricsDisabled {
		return &funnel.NoOpProcessorMetrics{}
	}

	return funnel.NewProcessorMetrics(pipelineName, plugin, componentID)
}

func (s *Service) newDLQMetrics(pipelineName string, plugin string) funnel.ConnectorMetrics {
	if s.metricsDisabled {
		return &funnel.NoOpConnectorMetrics{}
	}

	return funnel.NewDLQMetrics(pipelineName, plugin)
}
