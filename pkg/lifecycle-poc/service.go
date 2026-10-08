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
	"crypto/sha256"
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

// errIntentionalStopDuringRecovery is the user-Stop counterpart of
// errGracefulShutdownDuringRecovery: StartWithBackoff returns it when a user
// Stop marked the run intentionalStop while it was parked in the backoff wait.
// The cleanup goroutine maps it to StatusUserStopped (#2901).
var errIntentionalStopDuringRecovery = cerrors.New("user stop during recovery backoff")

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

	// runs counts live runs and holds the shutdown flag (#2901). StopAll
	// begins the shutdown; from then on Start and recovery restarts are
	// refused, and Wait does not return while any run is live. Mirrors
	// pkg/lifecycle. See runTracker.
	runs runTracker

	pipelines  PipelineService
	connectors ConnectorService

	processors       ProcessorService
	connectorPlugins ConnectorPluginService

	handlers         []FailureHandler
	runningPipelines *csync.Map[string, *runnablePipeline]

	// publishMu serializes WRITERS to runningPipelines — the publication in
	// publishRunningPipeline and the compare-and-delete in
	// deleteRunningPipelineIfCurrent — so the read-compare-delete is atomic
	// with respect to a concurrent publication (#2811). csync.Map has no
	// compare-and-swap primitive; without this lock a stale owner's Delete can
	// still land just after another goroutine's Set and erase the newer run.
	// Mirrors pkg/lifecycle.Service.publishMu (#2806).
	//
	// Readers (Get/All/Copy) deliberately do not take it: csync.Map has its own
	// RWMutex for memory safety. It is never held across I/O or a worker
	// operation, so it cannot deadlock against the stop path.
	publishMu sync.Mutex

	// terminalErrors holds the terminal error of a pipeline after it has stopped
	// and been removed from runningPipelines, so WaitPipeline can still report it
	// to a caller that races the pipeline's own cleanup goroutine. Written before
	// the runningPipelines entry is deleted; cleared when the pipeline is started
	// again. Ports the fix applied to the sibling pkg/lifecycle package for the
	// same WaitPipeline lookup-after-delete race — see
	// docs/design-documents/20260706-forceful-stop-test-determinism.md.
	terminalErrors *csync.Map[string, error]

	// testWorkersReleased, if set, is called synchronously from runPipeline
	// immediately after this run's worker goroutines are released (i.e.
	// right after close(registered)) — the earliest instant a worker could
	// possibly do observable work (read/process/ack a record). It exists
	// solely so a test can hold that instant open deterministically, instead
	// of racing it, to prove the #2833 regression: that runningPipelines
	// already points at THIS run before any worker can be released, even on
	// a recovery restart where a dead run's entry would otherwise still be
	// in the map. Mirrors the same "wrap/hook a collaborator instead of
	// sleeping" seam shape as statusRecorder.onUpdate in the test file,
	// applied here because the window under test closes before the
	// PipelineService is ever called, so hooking UpdateStatus cannot observe
	// it.
	//
	// Nil in production — NewService never sets it — so the call site below
	// is a single unlocked nil-check with no synchronization and no
	// observable effect when unset: zero behavior change outside tests.
	// Exported to the package's test files only by being unexported (they
	// share this package) and set directly on a *Service under test; there
	// is no production code path that can set or read it.
	testWorkersReleased func(rp *runnablePipeline)

	// statusWriteTimeout overrides the package's statusWriteTimeout when
	// positive. Zero in production; only this package's tests set it.
	statusWriteTimeout time.Duration

	// starting holds the reservation of every Start that has been admitted
	// and has not yet published its run. Guarded by publishMu. See
	// admission.go.
	starting map[string]*reservation

	// testAtReservation, if set, is called by reserve before it takes
	// publishMu. testAfterBackoffWait, if set, is called by StartWithBackoff
	// once the backoff wait is over, before the restart reserves. They let a
	// test line up an external Start against a pending restart. Nil in
	// production.
	testAtReservation    func(pipelineID string, predecessor *runnablePipeline)
	testAfterBackoffWait func(rp *runnablePipeline)
	// testAfterFinishing, if set, is called by the cleanup goroutine right
	// after it moves the run to phaseFinishing, before it classifies how
	// the run ended. Nil in production.
	testAfterFinishing func(rp *runnablePipeline)

	// testAfterStopSnapshot, if set, is called by the cleanup goroutine right
	// after it has read the run's stop request, before it classifies the run.
	// It lets a test change the request in exactly that window (e.g. a
	// concurrent stop rolling back) to prove the classification uses only
	// what it read. Nil in production, same contract as testWorkersReleased.
	testAfterStopSnapshot func(rp *runnablePipeline)

	// testCompareAndDeleteWindow, if set, is called from
	// deleteRunningPipelineIfCurrent after the identity check has matched and
	// before the Delete: the exact window a concurrent publication must not
	// land in. It lets a test hold that window open and prove publishMu
	// closes it (#2811), instead of racing it. Nil in production, same
	// contract as testWorkersReleased.
	testCompareAndDeleteWindow func()

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
		starting:         make(map[string]*reservation),
		metricsDisabled:  metricsDisabled,
	}
}

type runnablePipeline struct {
	pipeline *pipeline.Instance

	// workers holds one funnel.Worker per source connector (slice 3b of the
	// arch-v2 multi-connector epic: N sources, one worker per source). Each
	// Worker is its own terminal acker bound to its own w.Source (see
	// funnel.Worker's package doc), and Worker.doTask/doNextTask thread the
	// CALLING worker's own acker through the shared sink's TaskNode subtree
	// rather than a separate/shared acker - so as long as (a) there is
	// exactly one Worker per source (enforced here: buildRunnablePipeline
	// builds exactly len(sourceIDs) workers) and (b) no shared downstream
	// component ever calls a source's Ack on behalf of another (upheld by
	// construction, not by a runtime check - see the doc above), cross-source
	// ack contamination is impossible. See
	// docs/design-documents/20260801-archv2-multiconnector-nsource.md.
	workers []*funnel.Worker
	// sourceIDs[i] is the connector ID of workers[i]'s source. Parallel to
	// workers; used for diagnostics only (naming which source a worker's
	// outcome belongs to in logs/errors).
	sourceIDs []string
	// sink owns the shared, destination-side portion of the task graph every
	// worker's own chain converges on: opened exactly once before any worker
	// starts, closed exactly once after every worker has exited
	// (workersWg.Wait() in runPipeline). See funnel.Sink's doc - this is the
	// crux of slice 3b (shared-sink teardown ordering): closing it any
	// earlier would tear down a destination a still-running sibling worker
	// is still writing to.
	sink *funnel.Sink

	t *tomb.Tomb

	// backoff and recoveryAttempts hold the auto-recovery state. backoff is
	// seeded per build; recoveryAttempts is carried across restarts by Start so
	// MaxRetries actually bounds the retry loop (a reset every restart would make
	// the ceiling unreachable). recoveryAttempts is a pointer so the shared
	// counter survives the rp swap on restart. Mirrors pkg/lifecycle.
	backoff          *backoff.Backoff
	recoveryAttempts *atomic.Int64

	// intentionalStop is set by stopRunnablePipeline's graceful-stop branch,
	// before it calls Stop on every worker, to mark this run as one an operator (or
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
	//
	// It is a lock-free mirror of the stop request below: written only under
	// stopMu, together with the rest of the request, and read lock-free by
	// the cleanup goroutine and StartWithBackoff.
	intentionalStop atomic.Bool

	// stopMu guards the stop request: stopGen, stopKind, stopFailedFirst, and
	// writes to intentionalStop. Making a request and rolling one back are
	// each one critical section, so the first request's kind and failed-first
	// snapshot land together, and a rollback cannot erase a request that
	// arrived after the one it undoes (#2912).
	stopMu sync.Mutex
	// stopGen counts stop requests made for this run. A rollback undoes the
	// request only if no other request was made after it, i.e. stopGen still
	// equals the value its own markStopRequested returned.
	stopGen uint64
	// stopKind records who made the first stop request: 0 none,
	// stopKindUser for a user Stop, stopKindSystem for StopAll. The first
	// request wins, so a user-stopped run that a later shutdown also reaches
	// stays UserStopped and is not auto-started on the next boot (#2912 S2,
	// matching pkg/lifecycle's stopSignal).
	stopKind int32
	// stopFailedFirst records whether the run's tomb already carried an error
	// when the first stop request was made. A fatal error that came before
	// the stop still degrades the run; anything after the stop ends it
	// stopped, with the error recorded (#2901).
	stopFailedFirst bool

	// reservation is the admission this run was started under, consumed when
	// the run is published. Nil for a run started without Start (tests).
	reservation *reservation
	// phase, superseded and supersededCh are guarded by Service.publishMu.
	// See admission.go. superseded is set when a Start takes over the
	// pipeline while this run is finishing or waiting out a recovery
	// backoff; supersededCh is closed at the same time to end that wait.
	phase        runPhase
	superseded   bool
	supersededCh chan struct{}
}

// supersede marks rp as taken over by a new Start. publishMu must be held.
func (rp *runnablePipeline) supersede() {
	if rp.superseded {
		return
	}
	rp.superseded = true
	close(rp.supersededChLocked())
}

// supersededChLocked returns the channel closed when rp is superseded.
// publishMu must be held.
func (rp *runnablePipeline) supersededChLocked() chan struct{} {
	if rp.supersededCh == nil {
		rp.supersededCh = make(chan struct{})
	}
	return rp.supersededCh
}

const (
	stopKindUser   int32 = 1
	stopKindSystem int32 = 2
)

// markStopRequested records a stop request for rp. The first request (when
// none is recorded) also records who asked and whether the run had already
// failed. It returns a token for rollbackStopRequest and whether this call
// made the first request.
func (rp *runnablePipeline) markStopRequested(system bool) (token uint64, first bool) {
	failed := rp.t != nil && rp.t.Err() != tomb.ErrStillAlive
	kind := stopKindUser
	if system {
		kind = stopKindSystem
	}

	rp.stopMu.Lock()
	defer rp.stopMu.Unlock()
	if !rp.intentionalStop.Load() {
		first = true
		rp.stopKind = kind
		rp.stopFailedFirst = failed
		rp.intentionalStop.Store(true)
	}
	rp.stopGen++
	return rp.stopGen, first
}

// rollbackStopRequest undoes the request markStopRequested returned token
// for, for a stop that turned out to stop nothing. It does nothing if another
// request was made since: that request still stands, even though it was not
// the first (#2912).
func (rp *runnablePipeline) rollbackStopRequest(token uint64) {
	rp.stopMu.Lock()
	defer rp.stopMu.Unlock()
	if rp.stopGen != token {
		return
	}
	rp.intentionalStop.Store(false)
	rp.stopKind = 0
	rp.stopFailedFirst = false
}

// stopRequest returns the run's recorded stop request.
func (rp *runnablePipeline) stopRequest() (requested bool, kind int32, failedFirst bool) {
	rp.stopMu.Lock()
	defer rp.stopMu.Unlock()
	return rp.intentionalStop.Load(), rp.stopKind, rp.stopFailedFirst
}

// stoppedStatus is the terminal status of a run that was stopped: the kind of
// the first stop request made for it, or, if none reached it, SystemStopped
// during a shutdown and UserStopped otherwise.
func (s *Service) stoppedStatus(rp *runnablePipeline) pipeline.Status {
	_, kind, _ := rp.stopRequest()
	switch kind {
	case stopKindUser:
		return pipeline.StatusUserStopped
	case stopKindSystem:
		return pipeline.StatusSystemStopped
	}
	if s.isGracefulShutdown.Load() || s.runs.shuttingDown() {
		return pipeline.StatusSystemStopped
	}
	return pipeline.StatusUserStopped
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
// If the pipeline has a run that is starting or live, Start returns
// ErrPipelineRunning; the pipeline's status is not consulted (see
// admission.go). A run waiting out a recovery backoff is taken over: its
// pending restart is abandoned. Once StopAll has been called, Start refuses
// with an error coded pipeline.CodeShuttingDown (errors.Is(err,
// pipeline.ErrShuttingDown) holds).
func (s *Service) Start(
	ctx context.Context,
	pipelineID string,
) error {
	return s.startRun(ctx, pipelineID, nil)
}

// startRun is Start, and the recovery restart when predecessor is the run
// waiting out its backoff.
func (s *Service) startRun(ctx context.Context, pipelineID string, predecessor *runnablePipeline) error {
	// Invariant 7: once shutdown has begun no new run starts (#2901). The
	// authoritative check is runPipeline's admission; this one just avoids
	// building a run that would be refused.
	if s.runs.shuttingDown() {
		return errShuttingDown(pipelineID)
	}

	pl, err := s.pipelines.Get(ctx, pipelineID)
	if err != nil {
		return err
	}

	// Invariant 2: admit by run liveness, under publishMu, before building:
	// at most one run per pipeline ID holds a reservation or a live entry.
	res, err := s.reserve(pl.ID, predecessor)
	if err != nil {
		return err
	}
	// A no-op once the publication has consumed the reservation.
	defer s.releaseReservation(pl.ID, res)

	s.logger.Debug(ctx).Str(log.PipelineIDField, pl.ID).Msg("starting pipeline")
	s.logger.Trace(ctx).Str(log.PipelineIDField, pl.ID).Msg("building tasks")

	rp, err := s.buildRunnablePipeline(ctx, pl)
	if err != nil {
		return cerrors.Errorf("could not build tasks for pipeline %s: %w", pl.ID, err)
	}
	rp.reservation = res

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

	// runPipeline publishes rp into runningPipelines itself, at the exact
	// point the run goes live — see the Set call there for why that ordering
	// is load-bearing (#2746) and why this function must not do it after the
	// fact.
	if err := s.runPipeline(rp); err != nil {
		return cerrors.Errorf("failed to run pipeline %s: %w", pl.ID, err)
	}
	s.logger.Info(ctx).Str(log.PipelineIDField, pl.ID).Msg("pipeline started")

	return nil
}

// Stop will attempt to gracefully stop a given pipeline by calling each worker's
// Stop method. If the force flag is set to true, the pipeline will be stopped
// forcefully by cancelling the context.
//
// Stop is admitted by run liveness, not by the pipeline's status (#2899 item
// 2): it reaches a run that is live or waiting out a recovery backoff whatever
// its status shows. A Stop that arrives while a Start is still building the
// run is recorded and applied as soon as the run is published.
func (s *Service) Stop(ctx context.Context, pipelineID string, force bool) error {
	// Invariant 7 (#2901): the stop request is recorded under publishMu, the
	// lock a recovery restart reserves under, so a restart either sees the
	// request and is abandoned, or is already published and is stopped
	// itself. Recording it after the lookup let a restart reserve in between
	// and run on after Stop returned.
	s.publishMu.Lock()
	if res, ok := s.starting[pipelineID]; ok {
		if res.stop == nil {
			res.stop = &pendingStop{}
		}
		res.stop.force = res.stop.force || force
		if res.predecessor != nil {
			// A recovery restart is building. If its build fails, the run
			// it restarts must end stopped, not degraded.
			_, _ = res.predecessor.markStopRequested(false)
		}
		s.publishMu.Unlock()
		return nil
	}
	rp, ok := s.runningPipelines.Get(pipelineID)
	var phase runPhase
	var mark *stopMark
	if ok {
		phase = rp.phase
		token, first := rp.markStopRequested(false)
		mark = &stopMark{token: token, first: first}
	}
	s.publishMu.Unlock()

	if !ok {
		return cerrors.Errorf("pipeline %s is not running: %w", pipelineID, pipeline.ErrPipelineNotRunning)
	}
	if phase != phaseLive {
		// The workers are already dead: recording the stop is the whole
		// job. A run in backoff finishes stopped, with the error it failed
		// with, when its wait ends (#2901); a finishing run that would go on
		// to recover does not. Telling dead workers to stop would only fail.
		return nil
	}

	return s.stopRunnablePipelineMarked(ctx, rp, force, false, mark)
}

// stopMark is a stop request already recorded on a run (markStopRequested's
// result), handed to stopRunnablePipelineMarked so it does not record a
// second one and can still roll this one back.
type stopMark struct {
	token uint64
	first bool
}

// StopAll will ask all the running pipelines to stop gracefully
// (i.e. that existing messages get processed but not new messages get produced).
//
// StopAll is the shutdown path and puts the service into shutdown mode for
// good: from then on Start refuses with pipeline.CodeShuttingDown, no run is
// restarted by recovery, and Wait covers every run that is live (#2901).
func (s *Service) StopAll(ctx context.Context, force bool) error {
	// Set graceful shutdown flag to true, so pipelines know the system
	// triggered the stop. It must be set before beginShutdown below: that
	// wakes recovery backoff waits, and the run they abandon reads this flag
	// (through stoppedStatus) to report SystemStopped.
	s.isGracefulShutdown.Store(true)

	// Invariant 7 (#2901): begin the shutdown before reading
	// runningPipelines below. runPipeline publishes a run and only then reads
	// this flag, so a run is either in the map StopAll iterates or sees the
	// flag and stops itself.
	s.runs.beginShutdown()

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
		// Invariant 7 (#2912 B1): stop every run that is still alive, whatever
		// its status says. A run is published before it announces
		// StatusRunning, so for a moment its entry carries the previous run's
		// status; it read the shutdown flag before it was set, so skipping it
		// here by status would leave it running past Wait.
		if rp.t == nil || !rp.t.Alive() {
			continue
		}
		errs = append(errs, s.stopRunnablePipeline(ctx, rp, force, true))
	}
	return cerrors.Join(errs...)
}

// stopRunnablePipeline stops rp gracefully or forcefully. system is true for
// a shutdown (StopAll), false for a user Stop; see stopKind.
func (s *Service) stopRunnablePipeline(ctx context.Context, rp *runnablePipeline, force, system bool) error {
	return s.stopRunnablePipelineMarked(ctx, rp, force, system, nil)
}

// stopRunnablePipelineMarked is stopRunnablePipeline for a caller that has
// already recorded the stop request (mark), or nil to record it here.
func (s *Service) stopRunnablePipelineMarked(ctx context.Context, rp *runnablePipeline, force, system bool, mark *stopMark) error {
	switch force {
	case false:
		s.logger.Info(ctx).
			Str(log.PipelineIDField, rp.pipeline.ID).
			Any(log.PipelineStatusField, rp.pipeline.GetStatus()).
			Msg("gracefully stopping pipeline")

		// Invariant 3/7: mark this run as an intentional (operator-initiated)
		// stop BEFORE calling Stop on any worker, so that if a drain itself
		// surfaces a transient (non-fatal) error — e.g. a batch already in
		// flight when Stop was called finishes unwinding with a destination
		// write failure — runPipeline's cleanup goroutine (see the
		// intentionalStop check there) finalizes it as StatusUserStopped
		// instead of misreading it as a spontaneous failure and
		// auto-restarting via recoverPipeline. See the intentionalStop field
		// doc.
		var stopToken uint64
		var firstRequest bool
		if mark != nil {
			stopToken, firstRequest = mark.token, mark.first
		} else {
			stopToken, firstRequest = rp.markStopRequested(system)
		}

		// H1 (adversarial review of #2734): every worker's Stop call is
		// dispatched CONCURRENTLY, all against the SAME ctx deadline,
		// instead of sequentially. ctx carries an ABSOLUTE deadline (see
		// StopAndWait's O2 bound), so a sequential loop did not give every
		// worker an equal window to arm: an earlier worker's Stop call could
		// block for a long time (e.g. waiting on its own processingLock,
		// held by its own Do goroutine contending for a SLOW sibling's write
		// on the shared destination via sharedMu - see funnel.Worker.doTask)
		// and, by the time the loop reached a LATER worker, the deadline had
		// already mostly or entirely elapsed - so which workers armed
		// depended on their position in the loop, not on how long they
		// actually needed. Dispatching concurrently gives every worker the
		// same wall-clock window.
		//
		// This does not eliminate partial arming outright - two sources can
		// legitimately need different amounts of time to reach a safe stop
		// point within one bounded deadline - so armed/unarmed status is
		// still gathered below and a genuine partial result is escalated,
		// never left as an ambiguous, silently-stuck state.
		var wg sync.WaitGroup
		var mu sync.Mutex
		var errs []error
		armed := make([]bool, len(rp.workers))

		// F1 (review of the H1 fix): snapshot each worker's stop flag BEFORE
		// dispatching, and count a worker as armed only if THIS call flipped it
		// false->true. Worker.Stopping() reports w.stop, which is worker-owned
		// and can already be true for a reason nothing to do with this Stop —
		// the io.EOF branch sets it when a source exhausts ITSELF. Reading it
		// afterwards conflated "this Stop armed it" with "it stopped itself",
		// so an ordinary slow graceful stop of a live source, alongside a
		// sibling that had exhausted an hour earlier, looked like a partial
		// teardown and escalated to a FATAL kill: the live source's in-flight
		// batch cancelled instead of drained, and Degraded instead of the
		// retriable "nothing armed" outcome. The escalation's premise is
		// "sources torn down BY THIS CALL", so that is what must be measured.
		preArmed := make([]bool, len(rp.workers))
		for i, w := range rp.workers {
			preArmed[i] = w.Stopping()
		}

		wg.Add(len(rp.workers))
		for i, w := range rp.workers {
			go func(i int, w *funnel.Worker) {
				defer wg.Done()
				err := w.Stop(ctx)
				if err != nil {
					mu.Lock()
					errs = append(errs, cerrors.Errorf("source %s: %w", rp.sourceIDs[i], err))
					mu.Unlock()
				}
				// See Worker.Stopping's doc: true iff w.stop was set, which
				// Stop does BEFORE attempting source teardown - so this is
				// accurate even when Stop itself went on to return an error
				// (a wedged/dead source's teardown failing AFTER arming).
				armed[i] = !preArmed[i] && w.Stopping()
			}(i, w)
		}
		wg.Wait()

		// A worker that had ALREADY stopped itself before this call (preArmed —
		// e.g. a source that exhausted via io.EOF) is neither armed-by-us nor
		// failed-to-arm: it is simply gone, and workersWg already accounts for
		// its exit. It must be excluded from BOTH sets. Counting it as armed
		// would be the original F1 bug (a self-exhausted sibling making an
		// ordinary slow stop look partial); counting it as unarmed is the
		// mirror image of the same mistake, and would escalate a perfectly
		// healthy "one source finished, the other stopped cleanly" pipeline to
		// a fatal forced stop.
		var armedSources, unarmedSources []string
		for i, a := range armed {
			if preArmed[i] {
				continue
			}
			if a {
				armedSources = append(armedSources, rp.sourceIDs[i])
			} else {
				unarmedSources = append(unarmedSources, rp.sourceIDs[i])
			}
		}

		switch {
		case len(armedSources) == 0:
			// Nothing armed: every worker's Stop call failed BEFORE setting
			// w.stop (the only such path is acquireProcessingLock losing to
			// ctx - see funnel.Worker.Stop). No source was torn down; every
			// worker is still genuinely running, unattended, exactly as
			// before this call. Clear the marker, when it is this call's own
			// request alone (see below), so a LATER, unrelated transient
			// error is still eligible for ordinary auto-recovery. Mirrors the
			// original single-worker rollback condition ("nothing began
			// stopping"), generalized to "no worker began stopping".
			//
			// Only roll back what this call did (#2912 S3). If every worker
			// was already stopping (preArmed: an earlier Stop armed them, or
			// they exhausted on their own), both lists are empty and this
			// call stopped nothing new; if an earlier request already marked
			// the run, it stands. Clearing it in either case let a second
			// Stop or StopAll erase a user stop, so a later drain error
			// recovered and restarted the pipeline.
			//
			// And only if no other stop request was made in the meantime
			// (#2912): a concurrent Stop or StopAll that recorded its request
			// after this one, and is still waiting to arm, must keep it.
			//
			// Consequence, accepted deliberately (ADR
			// 20261007-stop-requested-never-recovers): once two stop
			// requests have been made, the marker is never cleared, even if
			// both give up before arming. The first's rollback is a no-op
			// because a later request exists, and the second never rolls
			// back because it was not the first. The run then stays marked
			// stopped: a later transient error ends it UserStopped (or
			// SystemStopped) with the error recorded, and it is not
			// recovered. Someone asked twice to stop this run; not
			// restarting it is the safer reading.
			if len(unarmedSources) > 0 && firstRequest {
				rp.rollbackStopRequest(stopToken)
			}
		case len(unarmedSources) > 0:
			// H1 (adversarial review): PARTIAL arming. Some source(s) armed
			// and tore down their connector; other(s) are still reading.
			// Leaving this half-done would strand the pipeline reporting
			// StatusRunning forever: the unarmed workers' Do loops keep
			// running, workersWg never drains, and runPipeline's cleanup
			// goroutine (the only thing that writes a terminal status) never
			// runs. There is no safe "wait longer" option left here - the
			// deadline this Stop call was given has already elapsed for at
			// least one source.
			//
			// Escalate: force-kill the pipeline's tomb. Its ctx (threaded
			// into every worker's Do call - see runPipeline) is canceled, so
			// every still-unarmed worker's blocked Read (or its wait for
			// sharedMu/processingLock) observes cancellation on its next
			// check and unwinds via the context.Canceled path in
			// Worker.doTaskAttempt - guaranteeing workersWg drains once any in-flight shared
			// write completes — sharedMu is a plain mutex with no ctx
			// awareness, so a sibling blocked on it is released by the current
			// holder finishing, not by cancellation and
			// the cleanup goroutine DOES run, reporting a terminal
			// (Degraded) status instead of an invisible half-stopped
			// pipeline. This trades "graceful" for "terminates
			// deterministically", which is the only safe choice once some
			// sources are already gone: at-least-once is still intact -
			// nothing here acks a record, so an interrupted worker's
			// in-flight, unacked batch simply replays on the next start
			// (invariant 3). Genuinely wedged I/O that ignores ctx
			// cancellation entirely is a separate, pre-existing limitation
			// this does not newly introduce (see DefaultStopAndWaitTimeout's
			// doc) - it already applied equally to a single-worker pipeline.
			ce := conduiterr.New(CodePartialGracefulStopEscalated, fmt.Sprintf(
				"graceful stop of pipeline %q partially completed within the deadline: source(s) %v stopped, "+
					"but source(s) %v did not - escalating to a forced stop to avoid a pipeline stuck reporting "+
					"Running with no signal",
				rp.pipeline.ID, armedSources, unarmedSources,
			))
			ce.Suggestion = "check the destination/DLQ and the unarmed source(s) for a stuck write or read; " +
				"the pipeline is being force-stopped and will end stopped with this error recorded - restart it once the " +
				"underlying issue is resolved"
			rp.t.Kill(cerrors.FatalError(ce))
			errs = append(errs, ce)
		default:
			// Every worker armed: full success. Any per-worker Stop errors
			// gathered above are still returned (a source can fail its OWN
			// teardown after arming - see Worker.Stopping's doc - which is
			// reported but needs neither rollback nor escalation), but there
			// is nothing more to do here.
		}
		return cerrors.Join(errs...)
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
		// explicitly stopped. Since #2901 a force stop is recorded as a stop
		// request too, so the run ends UserStopped (SystemStopped from
		// StopAll) with ErrForceStop recorded, rather than Degraded.
		if mark == nil {
			_, _ = rp.markStopRequested(system)
		}
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

	// Invariant 7 (#2901): the snapshot above can miss a run, e.g. a recovery
	// restart published after the snapshot, whose predecessor's tomb dies
	// while the restarted run is still going. The runtime flushes the
	// persister and closes the database right after Wait, so wait until no
	// run is live at all.
	s.runs.wait()

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
	// A run that is still being built is waited for: once its reservation is
	// consumed (published) or released (start failed), the lookups below
	// answer for it.
	s.publishMu.Lock()
	res := s.starting[id]
	s.publishMu.Unlock()
	if res != nil {
		<-res.done
	}

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
// CodeStopAndWaitTimeout error and, for a single-source pipeline (or an
// N-source one where EVERY worker's Stop call times out), does NOT
// force-kill anything: whichever step timed out (Stop, the drain wait, or the
// persistence wait) leaves the pipeline in the exact state
// connector.Source.Teardown's own bounded-wait fallback already established
// as safe (source.go's Teardown doc) — at worst a benign duplicate on a later
// restart, never a gap. If Stop itself times out with NO worker's stop flag
// ever set, the pipeline is simply still running, unattended, exactly as it
// was before this call — safe to retry (stopAndWaitTimeoutErr's "stop" phase).
//
// N-source exception (adversarial-review finding H1): if Stop times out with
// SOME but not all sources' stop flags set, stopRunnablePipeline does NOT
// leave that ambiguous — it force-kills the pipeline's tomb right there
// rather than let it strand reporting StatusRunning forever, and this method
// surfaces that distinctly (stopAndWaitTimeoutErr's "stop-escalated" phase,
// CodePartialGracefulStopEscalated) instead of the plain "safe to retry,
// untouched" story that was true pre-3b when a pipeline had exactly one
// worker. See stopRunnablePipeline's doc for the full escalation rationale.
//
// StopAndWait requires the pipeline to already be running (it delegates to
// Stop, which returns pipeline.ErrPipelineNotRunning-coded errors otherwise)
// and only ever stops gracefully.
// isPartialStopEscalation reports whether err (typically a cerrors.Join tree
// from stopRunnablePipeline) contains a CodePartialGracefulStopEscalated
// anywhere, regardless of how many other coded errors precede it. See the call
// site in StopAndWait for why first-match is not sufficient.
func isPartialStopEscalation(err error) bool {
	if err == nil {
		return false
	}
	if ce, ok := conduiterr.Get(err); ok && ce.Code == CodePartialGracefulStopEscalated {
		return true
	}
	// Walk the rest of the join tree: Get/As stops at the first match, so a
	// coded sibling appearing earlier would otherwise mask the escalation.
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range joined.Unwrap() {
			if isPartialStopEscalation(e) {
				return true
			}
		}
	}
	if u := cerrors.Unwrap(err); u != nil {
		return isPartialStopEscalation(u)
	}
	return false
}

func (s *Service) StopAndWait(ctx context.Context, pipelineID string) error {
	deadline := time.Now().Add(DefaultStopAndWaitTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d // honor a tighter caller-supplied deadline
	}
	stopCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	if err := s.Stop(stopCtx, pipelineID, false); err != nil {
		// H1 compounding fix (adversarial review of #2734): check for the
		// N-source partial-arming escalation BEFORE the generic
		// DeadlineExceeded check below. stopRunnablePipeline already
		// force-killed the pipeline's tomb the moment it detected some but
		// not all sources armed within the deadline - the pipeline is on
		// its way to a terminal (Degraded) status, which is NOT the
		// "untouched, safe to retry" state the generic "stop" phase message
		// describes. Without this check first, the join below (per-worker
		// Stop errors, which for the sources that DIDN'T arm are themselves
		// wrapped ctx.DeadlineExceeded errors) would still satisfy
		// cerrors.Is(err, context.DeadlineExceeded) and fall into that
		// generic branch, surfacing a FALSE "safe to retry" claim for a
		// pipeline that is actually being force-stopped.
		// F3 (review): match the escalation by walking EVERY coded error in the
		// join tree, not just the first. conduiterr.Get is cerrors.As, which
		// returns the first *ConduitError in a pre-order walk — and
		// stopRunnablePipeline appends the per-worker Stop errors (in
		// nondeterministic goroutine order) BEFORE the escalation error. Today
		// every Worker.Stop failure path yields an uncoded error so Get happens
		// to find the escalation, but the moment one of them becomes coded the
		// escalation would be skipped and control would fall through to the
		// generic DeadlineExceeded arm below — printing the exact "still
		// running, safe to retry" text this escalation exists to suppress, for
		// a pipeline that was just force-killed.
		if isPartialStopEscalation(err) {
			return s.stopAndWaitTimeoutErr(pipelineID, "stop-escalated", err)
		}
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
// when the bounded drain (O2) elapses during the named phase ("stop",
// "stop-escalated", "drain", or "persist"). The end-state note is
// phase-specific because the phases leave the pipeline in genuinely different
// states — nothing is ever force-stopped or torn down on "stop"/"drain"/
// "persist", so those three stay at-least-once-safe (never a gap) with the
// pipeline either untouched ("stop") or already draining on its own
// ("drain"/"persist"); "stop-escalated" is the one phase where this call DID
// already force-kill the pipeline (see stopRunnablePipeline's N-source partial-
// arming escalation, adversarial-review finding H1) — that pipeline is headed
// for StatusDegraded, not "safe to retry as if nothing happened". Only the
// "stop" phase leaves the pipeline still running and cleanly retriable; after
// "stop-escalated"/"drain"/"persist" the worker is already stopping (or
// force-stopping), so a literal StopAndWait retry would hit
// ErrPipelineNotRunning (adversarial-review Finding 2).
func (s *Service) stopAndWaitTimeoutErr(pipelineID, phase string, cause error) error {
	var state, suggestion string
	switch phase {
	case "stop":
		state = "the pipeline never began stopping (its worker's stop flag was not set) and is still running, exactly as before this call — safe to retry StopAndWait"
		suggestion = "check the destination/DLQ for a stuck write holding the processing lock, then retry; the pipeline is untouched and at-least-once is intact"
	case "stop-escalated":
		// H1 (adversarial review of #2734): an N-source pipeline where SOME
		// but not all sources armed within the deadline. Unlike the plain
		// "stop" phase, this pipeline is NOT untouched: stopRunnablePipeline
		// already force-killed its tomb the moment it detected the partial
		// result, specifically so it could never be left stuck reporting
		// Running with no signal. Retrying StopAndWait immediately will
		// likely hit ErrPipelineNotRunning as it finishes winding down.
		state = "a partial graceful stop was detected — source(s) already stopped while other(s) were still running — and this call already escalated it to a forced stop; the pipeline is winding down toward StatusDegraded, not untouched"
		suggestion = "do not retry StopAndWait immediately; wait for the pipeline to reach StatusDegraded (poll pipeline status or WaitPipeline), check the source(s)/destination named in the cause for what was stuck, then re-apply"
	default: // "drain" or "persist"
		state = "the pipeline had already begun stopping and will finish draining on its own; the timeout only means quiescence/durability was not confirmed within the bound"
		suggestion = "check the destination/DLQ for a stuck write; do not force-restart — let the pipeline reach a stopped state (it is at-least-once-safe, never a gap), then re-apply"
	}
	// "stop-escalated" reads grammatically as "...within a partial graceful
	// stop", not "...to stop-escalated" — everything else keeps the original
	// "to <phase>" phrasing.
	verb := phase
	if phase == "stop-escalated" {
		verb = "gracefully stop every source"
	}
	ce := conduiterr.Wrap(CodeStopAndWaitTimeout, fmt.Sprintf(
		"timed out waiting for pipeline %q to %s within %s; %s",
		pipelineID, verb, DefaultStopAndWaitTimeout, state,
	), cause)
	ce.Suggestion = suggestion
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

// buildRunnablePipeline will build and connect all tasks configured in the
// pipeline.
//
// Slice 3b of the arch-v2 multi-connector epic: a pipeline may now have N
// source connectors sharing one destination. Each source gets its own
// funnel.Worker (its own per-source prefix: the source task plus that
// source's own connector-specific processors); every worker's prefix is then
// attached, by pointer, to the SAME shared tail (pipeline-level processors +
// destination branch(es), built exactly once by buildSharedTail and owned by
// a single funnel.Sink) — see the funnel.Sink and runnablePipeline.sink field
// docs for why the shared portion needs its own, separate open/close
// lifecycle from any individual worker's.
func (s *Service) buildRunnablePipeline(
	ctx context.Context,
	pl *pipeline.Instance,
) (*runnablePipeline, error) {
	pipelineLogger := s.logger
	pipelineLogger.Logger = pipelineLogger.Logger.With().Str(log.PipelineIDField, pl.ID).Logger()

	srcTaskSets, err := s.buildSourceTasks(ctx, pl, pipelineLogger)
	if err != nil {
		return nil, cerrors.Errorf("failed to build source tasks: %w", err)
	}
	if len(srcTaskSets) == 0 {
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

	sharedRoots, err := s.buildSharedTail(procTasks, destTasks)
	if err != nil {
		return nil, cerrors.Errorf("failed to build shared sink task graph: %w", err)
	}
	sink, err := funnel.NewSink(sharedRoots...)
	if err != nil {
		return nil, cerrors.Errorf("failed to build shared sink: %w", err)
	}

	timer := measure.PipelineExecutionDurationTimer.WithValues(pl.Config.Name)

	workers := make([]*funnel.Worker, 0, len(srcTaskSets))
	sourceIDs := make([]string, 0, len(srcTaskSets))
	for _, srcTaskSet := range srcTaskSets {
		dlq, err := s.buildDLQ(ctx, pl, srcTaskSet.sourceID, pipelineLogger)
		if err != nil {
			return nil, cerrors.Errorf("failed to build DLQ for source %s: %w", srcTaskSet.sourceID, err)
		}

		taskNode := &funnel.TaskNode{Task: srcTaskSet.tasks[0]}
		tail := taskNode
		for _, task := range srcTaskSet.tasks[1:] {
			next := &funnel.TaskNode{Task: task}
			if err := tail.AppendToEnd(next); err != nil {
				return nil, cerrors.Errorf("failed to append task to task node list: %w", err)
			}
			tail = next
		}
		// Attach the SAME shared root(s) — by pointer — to this source's own
		// prefix. N different sources' tail nodes end up with their own Next
		// field pointing at the identical shared TaskNode instances; that's
		// safe (Next is just a slice of pointers, and nothing about
		// attaching it mutates the shared nodes) and is exactly what makes
		// funnel.Worker.doTask's runtime traversal reach the shared
		// destination from every worker while Worker.Open/Close (which walk
		// via the Tasks()/TaskNodes() iterator, not Next directly) stop
		// before it — see TaskNode.MarkSharedBoundary's doc.
		if err := tail.AppendToEnd(sharedRoots...); err != nil {
			return nil, cerrors.Errorf("failed to attach shared sink for source %s: %w", srcTaskSet.sourceID, err)
		}

		// log the tasks and order for debugging purposes. taskNode.Tasks()
		// stops at the shared boundary (see TaskNode.MarkSharedBoundary's
		// doc — a Worker's own Open/Close walk never descends into the
		// shared sink), so without walking sharedRoots separately this line
		// would silently omit the destination and any shared pipeline-level
		// processors from every source's debug output (L4, adversarial
		// review of #2734) — an operator comparing this log against the
		// pipeline config would see a task chain that appears to dead-end
		// before ever reaching a destination. Logged under its own "shared"
		// key, once per source (cheap, and keeps this one line
		// self-contained instead of requiring a second log statement
		// elsewhere to reconstruct the full chain).
		taskTypes := make([]string, 0)
		for task := range taskNode.Tasks() {
			taskTypes = append(taskTypes, fmt.Sprintf("%s(%T)", task.ID(), task))
		}
		sharedTaskTypes := make([]string, 0)
		for _, root := range sharedRoots {
			for task := range root.Tasks() {
				sharedTaskTypes = append(sharedTaskTypes, fmt.Sprintf("%s(%T)", task.ID(), task))
			}
		}
		pipelineLogger.Info(ctx).
			Str("source_id", srcTaskSet.sourceID).
			Any("tasks", taskTypes).
			Any("shared", sharedTaskTypes).
			Msg("pipeline tasks")

		worker, err := funnel.NewWorker(taskNode, dlq, pipelineLogger, timer)
		if err != nil {
			return nil, cerrors.Errorf("failed to create worker for source %s: %w", srcTaskSet.sourceID, err)
		}
		workers = append(workers, worker)
		sourceIDs = append(sourceIDs, srcTaskSet.sourceID)
	}

	return &runnablePipeline{
		pipeline:  pl,
		workers:   workers,
		sourceIDs: sourceIDs,
		sink:      sink,
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

// buildSharedTail builds the TaskNode subtree(s) shared by every source
// worker in the pipeline: the pipeline-level (shared) processors followed by
// the destination branch(es). Built exactly once per pipeline — never once
// per source — and handed to funnel.NewSink, which is what gives it its own
// open-once/close-once lifecycle independent of any individual
// funnel.Worker. See funnel.Sink's doc for the full rationale.
//
// Returns a single root when there is at least one shared pipeline-level
// processor: its own tail already fans out internally to every destination
// branch via one AppendToEnd call (mirroring the pre-3b, single-source
// behavior — see slice 3a's doc on the M-destination fan-out this reuses
// unchanged). Returns len(destTasks) independent roots — one per destination
// branch — when there are no shared processors, since there is then no
// single shared node to serve as that fan-out parent; funnel.Sink treats a
// multi-root case as len(roots) independently-locked shared subtrees, which
// is not just safe but preferable for M>1 (different destinations can then
// proceed without blocking on each other — see funnel.Sink's roots field
// doc).
func (s *Service) buildSharedTail(
	procTasks []funnel.Task,
	destTasks [][]funnel.Task,
) ([]*funnel.TaskNode, error) {
	destBranches := make([]*funnel.TaskNode, len(destTasks))
	for i, destTasksBranch := range destTasks {
		if len(destTasksBranch) == 0 {
			return nil, cerrors.New("(bug) destination branch has no tasks")
		}

		branchNode := &funnel.TaskNode{Task: destTasksBranch[0]}
		tail := branchNode
		for _, task := range destTasksBranch[1:] {
			next := &funnel.TaskNode{Task: task}
			if err := tail.AppendToEnd(next); err != nil {
				return nil, cerrors.Errorf("failed to append task to destination branch: %w", err)
			}
			tail = next
		}
		destBranches[i] = branchNode
	}

	if len(procTasks) == 0 {
		return destBranches, nil
	}

	procRoot := &funnel.TaskNode{Task: procTasks[0]}
	tail := procRoot
	for _, task := range procTasks[1:] {
		next := &funnel.TaskNode{Task: task}
		if err := tail.AppendToEnd(next); err != nil {
			return nil, cerrors.Errorf("failed to append task to task node list: %w", err)
		}
		tail = next
	}
	// A single AppendToEnd call with all M branches: procRoot's tail
	// currently has 0 Next, so this sets its Next to destBranches directly
	// rather than requiring M separate appends (which AppendToEnd can't do
	// once a node already has more than 1 Next — see its doc).
	if err := tail.AppendToEnd(destBranches...); err != nil {
		return nil, cerrors.Errorf("failed to attach destination branches to shared processor chain: %w", err)
	}

	return []*funnel.TaskNode{procRoot}, nil
}

// sourceTaskSet bundles one source connector's task chain (its SourceTask
// plus that connector's own per-connector processors) with the connector ID
// it belongs to. buildRunnablePipeline uses sourceID to build this source's
// dedicated funnel.Worker, its own per-source DLQ (buildDLQ), and to name it
// in diagnostics/errors.
type sourceTaskSet struct {
	sourceID string
	tasks    []funnel.Task
}

// buildSourceTasks builds one sourceTaskSet per source connector in the
// pipeline. Slice 3b of the arch-v2 multi-connector epic: this used to guard
// against (and reject) more than one source connector — that guard is gone.
// Every source connector found gets its own entry; buildRunnablePipeline
// turns each into its own funnel.Worker, which is what makes N sources safe
// (see runnablePipeline.workers' doc on why one worker per source is what
// makes cross-source ack contamination structurally impossible).
func (s *Service) buildSourceTasks(
	ctx context.Context,
	pl *pipeline.Instance,
	logger log.CtxLogger,
) ([]sourceTaskSet, error) {
	var sets []sourceTaskSet

	for _, connID := range pl.ConnectorIDs {
		instance, err := s.connectors.Get(ctx, connID)
		if err != nil {
			return nil, cerrors.Errorf("could not fetch connector: %w", err)
		}

		if instance.Type != connector.TypeSource {
			continue // skip any connector that's not a source
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
		tasks := make([]funnel.Task, 0, 1+len(procTasks))
		tasks = append(tasks, srcTask)
		tasks = append(tasks, procTasks...)
		sets = append(sets, sourceTaskSet{sourceID: instance.ID, tasks: tasks})
	}

	return sets, nil
}

// buildDestinationTasks builds one task branch per destination connector in
// the pipeline. Slice 3a of the arch-v2 multi-connector epic: multiple
// destination connectors are supported here — funnel.Worker fans the batch
// out to every branch this returns and multiAckNacker tracks per-record
// ack/nack outcomes across all of them (see buildTaskNodes and
// funnel.newMultiAckNacker). Multiple SOURCE connectors are a separate,
// later slice — see the guard in buildSourceTasks, which stays in place.
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

		// Build the slice of tasks for this destination.
		//
		// #2736: the destination's own (connector-scoped) processors must run
		// BEFORE the destination task, not after - mirrors pkg/lifecycle's
		// buildDestinationNodes, which chains a destination connector's
		// processor nodes between `prev` and the destination node. Appending
		// destTask first (as this used to do) put the write ahead of the
		// processors in buildSharedTail's chain, so a destination-scoped
		// processor transformed a copy of the record whose output fed only
		// the acker and nowhere else - the destination received the
		// UNTRANSFORMED record. Silent: no error, no warning, just a no-op
		// processor and (for a redaction processor) a data-exposure bug.
		destTasks := make([]funnel.Task, 0, 1+len(procTasks))
		destTasks = append(destTasks, procTasks...)
		destTasks = append(destTasks, destTask)
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

// buildDLQName returns the DLQ connector ID for sourceID within pipeline
// pipelineID, bounded well under connector.IDLengthLimit regardless of how
// long pipelineID or sourceID are.
//
// L1 (adversarial review of #2734): naming used to be
// pipelineID+"-"+sourceID+"-dlq". Provisioned connector IDs are already
// pipelineID+":"+name (see pkg/provisioning/config/enrich.go's
// enrichConnectors), so that format embedded the pipeline ID TWICE — once
// directly, once again inside sourceID — and could push a long-but-
// previously-valid pipeline ID over connector.IDLengthLimit (256), refusing
// to start a pipeline whose own ID was never too long on its own: a
// user-facing regression the user never asked for and can't fix by renaming
// anything they wrote.
//
// Fixed by keying on a short, deterministic hash of sourceID instead of its
// full text: the same sourceID always produces the same hash (stable across
// restarts — important, since buildDLQ runs again on every recovery restart
// and must keep addressing the SAME DLQ connector for a given source),
// collisions across the — typically small — N sources in one pipeline are
// astronomically unlikely (64 bits of SHA-256), and the result's length no
// longer depends on how long the source's own name happens to be.
func buildDLQName(pipelineID, sourceID string) string {
	h := sha256.Sum256([]byte(sourceID))
	return fmt.Sprintf("%s-dlq-%x", pipelineID, h[:8])
}

// buildDLQ builds a per-source DLQ destination connector for sourceID.
//
// Slice 3b of the arch-v2 multi-connector epic: pre-3b, a pipeline had
// exactly one source, so a single DLQ named pl.ID+"-dlq" was unambiguous.
// With N sources, that fixed name would collide the moment a second source
// tried to create a connector with the identical ID — so the DLQ is named
// via buildDLQName instead, giving every source its own, independent DLQ (in
// turn giving every funnel.Worker its own w.DLQ, opened and closed entirely
// within that worker's own Open/Close — never part of the shared sink; see
// funnel.Sink's doc).
//
// This is not a stored-state migration: the DLQ connector is created with
// connector.ProvisionTypeDLQ, which connector.Destination.Open/Teardown
// checks to skip persister.ConnectorStarted/ConnectorStopped — the DLQ
// connector (and therefore its ID) is never written to the connector store,
// so no upgrade path needs to reconcile an old naming scheme against a new
// one; nothing durable ever referenced it.
//
// M1 (adversarial review, documented not redesigned): windowSize/
// windowNackThreshold are per-source, not pipeline-wide. Pre-3b, with
// exactly one source, "halt after 5 nacks" and "halt after 5 nacks from
// THIS source" were the same statement. With N sources, each gets its OWN
// funnel.DLQ and therefore its own independent window (see funnel.DLQ's
// window field) — a pipeline configured with windowNackThreshold: 5 now
// tolerates up to 5 nacks PER SOURCE (5×N pipeline-wide) before any one of
// them halts the pipeline, not 5 total. This is a deliberate choice to keep
// the DLQ genuinely per-source (matching every other per-source DLQ
// property: naming, the destination connector instance, the ack window) over
// introducing a NEW piece of shared mutable state across N worker goroutines
// to preserve the old pipeline-wide count — the latter is a real option (a
// shared *dlqWindow behind its own mutex) but is not attempted here without
// a concrete operator need for it, per the "no speculative generality"
// engineering guideline. Operators who need a pipeline-wide bound today
// should divide their desired total by the number of sources when setting
// windowNackThreshold. See
// docs/design-documents/20260801-archv2-multiconnector-nsource.md, "Per-source
// DLQ window semantics".
//
// M2 (adversarial review, documented not redesigned): N sources' DLQs all
// share pl.DLQ.Settings — the same target, same credentials, same
// everything except the connector ID and window. That is harmless for a
// naturally-concurrent-safe target (builtin:log, most message-queue/object-
// store DLQs), but a DLQ target that is NOT safe for concurrent writers from
// independent connector instances (e.g. a local file DLQ, or anything that
// takes an exclusive lock/handle on Open) will see either interleaved/
// corrupted writes or an Open failure the moment a second source's DLQ
// tries to start. There is no per-source Settings override today — operators
// running N-source pipelines should pick a DLQ plugin known to tolerate
// concurrent independent instances, or avoid file-based DLQs until a
// per-source override exists. See the design doc's failure-modes section.
func (s *Service) buildDLQ(
	ctx context.Context,
	pl *pipeline.Instance,
	sourceID string,
	logger log.CtxLogger,
) (*funnel.DLQ, error) {
	dlqName := buildDLQName(pl.ID, sourceID)

	conn, err := s.connectors.Create(
		ctx,
		dlqName,
		connector.TypeDestination,
		pl.DLQ.Plugin,
		pl.ID,
		connector.Config{
			Name:     dlqName,
			Settings: pl.DLQ.Settings,
		},
		connector.ProvisionTypeDLQ, // the provision type ensures the connector won't be persisted
	)
	if err != nil {
		return nil, cerrors.Errorf("failed to create DLQ destination for source %s: %w", sourceID, err)
	}

	dest, err := conn.Connector(ctx, s.connectorPlugins)
	if err != nil {
		return nil, err
	}

	return funnel.NewDLQ(
		"dlq-"+sourceID,
		dest.(*connector.Destination),
		logger,
		s.newDLQMetrics(pl.Config.Name, conn.Plugin),
		pl.DLQ.WindowSize,
		pl.DLQ.WindowNackThreshold,
	), nil
}

// runPipeline starts every worker in rp.workers plus one cleanup goroutine,
// all registered on rp.t (see the registered/startupDone barriers below —
// unchanged in spirit from the pre-3b single-worker version, just generalized
// to N+1 goroutines instead of 2).
//
// Status aggregation (slice 3b of the arch-v2 multi-connector epic): the
// cleanup goroutine's switch below is UNCHANGED from the pre-3b,
// single-worker version, and that is deliberate — it generalizes to N
// workers for free, because every worker's goroutine feeds the SAME tomb
// (rp.t). Concretely:
//
//   - A worker whose Do() returns nil (a graceful stop, or a source that
//     exhausted its records on its own — see doTask's io.EOF handling) never
//     calls rp.t.Kill. The tomb stays alive and every sibling worker keeps
//     running. The pipeline's status is never touched until workersWg.Wait()
//     unblocks, i.e. it stays Running for as long as ANY worker is still
//     alive — so "some finished, some still running" is simply not yet a
//     terminal state at all.
//   - A worker whose Do()/Close() returns a non-nil error ALWAYS calls
//     rp.t.Kill(err) (fatal or not — see that call site's doc), which cancels
//     ctx for every sibling worker. tomb.v2 records only the FIRST error
//     passed to Kill; every later call (from this same worker's own
//     bookkeeping, or a sibling reacting to the now-canceled ctx) is a no-op.
//     So rp.t.Err() below always reflects "the first reason any one source
//     brought the whole pipeline down" — exactly one error, regardless of N.
//   - {all graceful} -> rp.t.Err() == tomb.ErrStillAlive -> StatusUserStopped/
//     StatusSystemStopped (the tomb.ErrStillAlive case below).
//   - {all fatal} or {some graceful + one fatal} -> rp.t.Err() is that fatal
//     error -> StatusDegraded (cerrors.IsFatalError case below). A fatal
//     error in ANY source degrades the WHOLE pipeline, matching v1.
//   - {some graceful + one transient} or {all transient} -> rp.t.Err() is
//     that transient error -> the recovery path (the innermost default case
//     below), which rebuilds every source's worker and the shared sink from
//     scratch — never a partial-source restart, which is why a transient
//     error in one source legitimately winds down every sibling first.
//   - A failure to close the shared sink itself (sinkCloseErr, computed
//     after workersWg.Wait but before this switch) is folded into err via
//     the same fatal/transient classification, rather than a fifth case.
//
// See docs/design-documents/20260801-archv2-multiconnector-nsource.md,
// "status aggregation", for the full mapping table and rationale.
//
// this shape (see the doc above and doTask's identical existing
// justification in funnel/worker.go) and is exercised by
// TestServiceLifecycle_* in service_test.go plus the N-source tests this
// slice adds; splitting it would trade one clear state machine for several
// correlated ones without reducing real complexity.
//
//nolint:gocyclo // the terminal-status classification switch is inherently
func (s *Service) runPipeline(rp *runnablePipeline) error {
	if rp.t != nil && rp.t.Alive() {
		return pipeline.ErrPipelineRunning
	}

	// Invariant 7 (#2901): count this run as live before it opens anything,
	// and refuse it once shutdown has begun. Wait blocks until every admitted
	// run is released: on the early returns below, which happen before any
	// goroutine is on the tomb, or once the tomb is dead.
	if !s.runs.admit() {
		return errShuttingDown(rp.pipeline.ID)
	}
	released := false
	defer func() {
		if !released {
			s.runs.release()
		}
	}()

	// the tomb is responsible for running goroutines related to the pipeline
	rp.t = &tomb.Tomb{}
	ctx := rp.t.Context(nil) //nolint:staticcheck // this is the correct usage of tomb

	// Invariant (crux of slice 3b, shared-sink teardown ordering): the shared
	// sink is opened exactly once, before any source worker, and — at the
	// other end of this function — closed exactly once, only after every
	// worker has exited (workersWg.Wait below). See funnel.Sink's doc.
	if err := rp.sink.Open(ctx); err != nil {
		return cerrors.Errorf("failed to open shared sink: %w", err)
	}

	// Open every worker. On a failure partway through, roll back every
	// worker already opened (and the sink) before returning — mirrors
	// funnel.Worker.Open's own all-or-nothing rollback.R behavior, just at
	// the pipeline level across N workers.
	opened := make([]*funnel.Worker, 0, len(rp.workers))
	for i, w := range rp.workers {
		if err := w.Open(ctx); err != nil {
			for j := len(opened) - 1; j >= 0; j-- {
				_ = opened[j].Close(context.Background())
			}
			_ = rp.sink.Close(context.Background())
			return cerrors.Errorf("failed to open worker for source %s: %w", rp.sourceIDs[i], err)
		}
		opened = append(opened, w)
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
	// Every t.Go call below (N workers + 1 cleanup goroutine) stays adjacent
	// (nothing slow between them) so tomb.alive reaches N+1 before any
	// goroutine can possibly finish; ordering the UpdateStatus calls via this
	// channel instead of via t.Go call order avoids a second bug that
	// surfaced when this was first tried by interleaving a synchronous
	// UpdateStatus between the t.Go calls: if a worker finishes before that
	// call returns, tomb.alive can hit 0 before the cleanup goroutine is even
	// registered, and a later t.Go panics with "tomb.Go called after all
	// goroutines terminated".
	startupDone := make(chan struct{})

	// registered closes once ALL N+1 t.Go calls below have been made (N
	// worker goroutines plus the cleanup goroutine). Every worker goroutine
	// waits on it before doing any work, which is what actually makes the
	// panic above impossible.
	//
	// Adjacency alone is NOT sufficient, despite what the comment above used
	// to claim: the Go runtime is free to schedule a worker goroutine and run
	// it to completion in the window between t.Go calls. When a worker fails
	// FAST — precisely what a transient-error recovery scenario induces —
	// tomb.alive drops back to 0 before the cleanup goroutine is registered,
	// and a later t.Go panics. Observed in CI on
	// TestServiceLifecycle_Recovery_TransientErrorRecovers under -shuffle (the
	// panic surfaced at this second t.Go, with the connectors never
	// dispensed). With N workers this hazard is worse, not better: any one of
	// N fast-failing workers can trigger it, not just the one. Gating every
	// worker on this channel guarantees alive >= 1 for the entire window, so
	// the tomb cannot die before all N+1 goroutines exist.
	registered := make(chan struct{})

	for i, w := range rp.workers {
		sourceID := rp.sourceIDs[i]
		workersWg.Add(1)
		rp.t.Go(func() error {
			defer workersWg.Done()

			// See `registered` above: must not return before every other
			// worker and the cleanup goroutine are registered on the tomb.
			<-registered

			doErr := w.Do(ctx)
			s.logger.Err(ctx, doErr).
				Str(log.PipelineIDField, rp.pipeline.ID).
				Str(log.ConnectorIDField, sourceID).
				Msg("pipeline source worker stopped")

			// F2 (review of the H2 fix): record the ROOT-CAUSE error on the
			// tomb here, immediately after Do returns and BEFORE the
			// multi-second w.Close below (source teardown waits on a persister
			// flush). tomb.v2 keeps only the FIRST reason, so whoever Kills
			// first defines the pipeline's terminal classification.
			//
			// The H2 poison fix moved that race. Previously a sibling blocked
			// on sharedMu was released only by ctx cancellation, which happens
			// strictly AFTER this worker's Kill — so the root cause always won.
			// Now the deferred sharedMu.Unlock() fires inside doTask, waking
			// the sibling long before this goroutine reaches its Kill; the
			// sibling then refuses entry with the non-fatal
			// CodeSharedDestinationPoisoned and races us to Kill through its
			// own w.Close. If it won, a FATAL root cause (e.g.
			// CodeRetryNotConverging, which #2732 landed precisely to fail
			// loud) would be masked by a non-fatal collateral error — flipping
			// the pipeline out of Degraded and into the recovery arm, to
			// restart against a processor that will never converge, and
			// showing the operator the symptom instead of the cause.
			//
			// Killing before Close closes that window: the sibling still has
			// to unwind and run its own Close before it can Kill.
			if doErr != nil {
				rp.t.Kill(doErr)
			}

			// Invariant (crux of slice 3b): Worker.Close now only tears down
			// THIS worker's own source and its own per-source DLQ — the
			// shared sink is excluded from its tree (see
			// TaskNode.MarkSharedBoundary) and is closed once, separately,
			// by the cleanup goroutine below, only after every worker here
			// has returned. A source that finishes here does not touch (and
			// cannot prematurely tear down) a destination its siblings are
			// still writing to.
			closeErr := w.Close(context.Background())
			err := cerrors.Join(doErr, closeErr)
			if err != nil {
				err = cerrors.Errorf("worker for source %s stopped with error: %w", sourceID, err)
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
				// idempotent and safe to call here (and, with N workers, safe even
				// if two workers race into it concurrently: tomb.v2 only records
				// the FIRST reason, and every later Kill call — from this worker's
				// own t.run bookkeeping or a sibling worker's error — is a no-op).
				//
				// Status aggregation (see this slice's design doc): a worker only
				// reaches this branch on an ACTUAL error. A source that exhausted
				// its records gracefully (io.EOF — see funnel.Worker.doTask) or
				// was asked to Stop returns nil from Do, so THAT worker's goroutine
				// returns nil too and never reaches this Kill call — ctx stays
				// alive for every sibling worker, which is what keeps the pipeline
				// Running while some sources have already finished. A fatal error
				// in any one source Kills the tomb, canceling ctx for every
				// sibling (matches v1: the single tomb fans a Kill to every
				// goroutine's ctx) — ALL workers wind down, and the cleanup
				// goroutine's fatal/transient classification below (unchanged,
				// generalizing automatically to N workers feeding one tomb) then
				// degrades the whole pipeline. A non-fatal (transient) error does
				// the same tomb-wide Kill, but is classified into the recovery
				// path instead of Degraded — recovery here is pipeline-wide
				// (rebuilds every source's worker and the shared sink from
				// scratch), which is why a transient error in ANY source
				// legitimately winds down every worker rather than leaving
				// siblings running against a destination about to be rebuilt.
				rp.t.Kill(err)
				return err
			}

			return nil
		})
	}

	rp.t.Go(func() error {
		// Use fresh context for cleanup function, otherwise the updated status
		// will potentially fail to be stored.
		ctx := context.Background()

		workersWg.Wait()

		// Invariant 1/3 (enforcement site, crux of slice 3b): close the
		// shared sink only now that EVERY worker has exited. See
		// funnel.Sink.Close's doc for the data-loss scenario this ordering
		// prevents (tearing it down while a sibling worker is still writing
		// to it).
		sinkCloseErr := rp.sink.Close(ctx)

		// Wait for the initial StatusRunning write below to fully finish before
		// this goroutine writes its own terminal status to the same
		// *pipeline.Instance. See the comment on startupDone above.
		<-startupDone
		// The workers are dead: from here a Start may take the pipeline
		// over (admission.go). Recovery moves the run to backoff if it is
		// not taken over first.
		s.setPhase(rp, phaseFinishing)
		if s.testAfterFinishing != nil {
			s.testAfterFinishing(rp)
		}

		// Invariant 7: from here the terminal tail (terminal error, map
		// removal, notify) runs whatever the status writes return, and this
		// goroutine returns the run's terminal error, never a status write's
		// (#2899 item 4). See writeStatus.
		err := rp.t.Err()
		// stoppedWithErr is set when the run was stopped (not failed) but
		// still ended with an error: the error is recorded and returned, but
		// OnFailure is not notified (#2901).
		stoppedWithErr := false

		if err == tomb.ErrStillAlive && sinkCloseErr != nil {
			// Every worker exited cleanly (no fatal/transient error anywhere),
			// but tearing down the shared destination itself failed. Fold
			// this in exactly like a worker's own error would be, so the
			// same fatal/transient classification below decides degrade vs.
			// recover instead of this being silently masked as a graceful
			// stop.
			err = cerrors.Errorf("failed to close shared sink: %w", sinkCloseErr)
		} else if sinkCloseErr != nil {
			// The pipeline is already terminal for some other, unrelated
			// reason (a worker's own fatal/transient error, or an
			// intentional stop) — log the sink close failure but don't let
			// it override that classification.
			s.logger.Err(ctx, sinkCloseErr).Str(log.PipelineIDField, rp.pipeline.ID).
				Msg("failed to close shared sink (pipeline already terminal for another reason)")
		}

		switch err {
		case tomb.ErrStillAlive:
			// not an actual error, the pipeline stopped gracefully
			err = nil
			// SystemStopped for a shutdown, UserStopped for a user stop; the
			// first stop request decides (#2912 S2).
			s.writeStatus(ctx, rp, s.stoppedStatus(rp), "")
		default:
			// Only a stop request recorded on this run (with its failed-first
			// snapshot) can turn a fatal error into a stop. The service-wide
			// shutdown flag alone must not: a run that failed fatally on its
			// own and is classified after StopAll set the flag, but before
			// StopAll reached it, is degraded (#2912 S1, as in pkg/lifecycle).
			stopRequested, _, failedFirst := rp.stopRequest()
			if s.testAfterStopSnapshot != nil {
				s.testAfterStopSnapshot(rp)
			}
			switch {
			case cerrors.IsFatalError(err) && (!stopRequested || failedFirst):
				// Invariant 3/7: a fatal error the run hit on its own, before any
				// stop was requested, is never auto-recovered — it degrades.
				// we use %+v to get the stack trace too.
				s.writeStatus(ctx, rp, pipeline.StatusDegraded, fmt.Sprintf("%+v", err))
			case s.isGracefulShutdown.Load():
				// The run ended with an error while Conduit is shutting down: do
				// not start a recovery loop that would race the shutdown
				// (invariant 7). Finalize as a system stop, so it starts again
				// on the next boot, and keep the error (#2901, ADR
				// 20261007-stop-requested-never-recovers): it is recorded on the
				// pipeline and returned by WaitPipeline, but it is not a failure,
				// so OnFailure is not notified.
				s.finishStopped(ctx, rp, s.stoppedStatus(rp), err)
				stoppedWithErr = true
			case stopRequested:
				// Use the snapshot read above, never a live read: a concurrent
				// stop that armed nothing can roll the request back between
				// the two, and a live read would then send a run that was
				// asked to stop into recovery (#2912).
				//
				// Invariant 3/7: an operator (or provisioning.ApplyPlanLive via
				// StopAndWait) deliberately asked THIS pipeline to stop — see
				// stopRunnablePipeline, which records the request before calling
				// Stop on any worker. An error surfacing from that deliberate
				// drain (or a force stop) must never be misread as a spontaneous
				// failure needing recovery: auto-restarting here would restart a
				// pipeline the operator just stopped (O3). Finalize as
				// StatusUserStopped and keep the error (#2901), as above.
				s.finishStopped(ctx, rp, s.stoppedStatus(rp), err)
				stoppedWithErr = true
			default:
				// Transient (non-fatal) error: attempt bounded-backoff recovery.
				recoveryErr := s.recoverPipeline(ctx, rp)
				switch {
				case recoveryErr == nil:
					// Recovery restarted the pipeline. The live run now owns
					// terminal cleanup, so return early WITHOUT running the
					// cleanup tail below — deleting the runningPipelines entry
					// here would delete the new run's entry. Mirrors v1's
					// return nil (pkg/lifecycle/service.go). A Start that took
					// the pipeline over during the backoff is the
					// errRecoverySuperseded case below, not this one.
					return nil
				case cerrors.Is(recoveryErr, errRecoverySuperseded):
					// A Start took the pipeline over while this run was
					// finishing or waiting out its backoff (#2899 item 2). The
					// new run owns the pipeline's status, terminal error and
					// notifications; this run only removes its own entry (a
					// no-op once the new run is published).
					s.deleteRunningPipelineIfCurrent(rp.pipeline.ID, rp)
					return err
				case cerrors.Is(recoveryErr, errGracefulShutdownDuringRecovery):
					// A graceful shutdown began while we were parked in the
					// backoff wait. Finalize as a system stop with the error the
					// run failed with, and run the cleanup tail so the entry is
					// removed.
					s.finishStopped(ctx, rp, s.stoppedStatus(rp), err)
					stoppedWithErr = true
				case cerrors.Is(recoveryErr, errIntentionalStopDuringRecovery):
					// A user Stop arrived while we were parked in the backoff
					// wait (#2901). Same outcome as the intentionalStop arm
					// above: a user stop with the error kept, no restart.
					s.finishStopped(ctx, rp, s.stoppedStatus(rp), err)
					stoppedWithErr = true
				default:
					// Recovery is exhausted (MaxRetries) or itself errored.
					s.logger.
						Err(ctx, err).
						Str(log.PipelineIDField, rp.pipeline.ID).
						Msg("pipeline recovery failed")

					s.writeStatus(ctx, rp, pipeline.StatusDegraded, fmt.Sprintf("%+v", recoveryErr))
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

		// All workers have stopped, so remove this run from runningPipelines —
		// but only if the entry under this ID is still THIS run (#2811).
		// This goroutine can be an OLDER run's cleanup that is unwinding
		// from a nested recovery restart: recoverPipeline -> StartWithBackoff
		// -> Start -> runPipeline(newer) runs synchronously on this
		// goroutine, publishes the newer run and releases its workers before
		// announcing StatusRunning. That announcement no longer fails Start
		// (#2899), but before it did, Start returned an error with the newer
		// run live, this goroutine took the recovery-failed arm above and
		// arrived here. A delete by key would erase the newer run's entry and
		// leave its workers running where no Stop, StopAll, Wait or
		// WaitPipeline can reach them; the compare-and-delete keeps this block
		// safe for any failure that unwinds here with a newer run published.
		//
		// Invariant 7: a run whose workers are live must stay reachable by
		// the stop and wait paths.
		s.deleteRunningPipelineIfCurrent(rp.pipeline.ID, rp)

		if !stoppedWithErr {
			s.notify(rp.pipeline.ID, err)
		}
		return err
	})

	// Publish this run as THE live run for its pipeline ID, here and not in
	// Start (#2746), and — critically — BEFORE close(registered) below
	// releases the worker goroutines (#2833).
	//
	// Invariant 7: whenever a caller can observe the pipeline as running (and,
	// as of #2833, whenever any worker can be observed to be doing work at
	// all), runningPipelines[id] is the run that is actually running. Every
	// public entry point resolves a pipeline through this map — Stop,
	// StopAll, WaitPipeline, StopAndWait (and thus
	// provisioning.ApplyPlanLive) — and StartWithBackoff's "am I still the
	// live pipeline" guard is a pointer comparison against it. A stale entry
	// does not fail loudly; it makes all of them operate on the previous,
	// already-dead run, which is a graceful-shutdown/Stop correctness bug
	// (invariant 7): a Stop that lands in the stale window stops the DEAD
	// run and returns success while the actually-running one keeps consuming
	// and acking records, unsupervised.
	//
	// Start used to Set this AFTER runPipeline returned, i.e. after the
	// UpdateStatus below had already announced StatusRunning. On a recovery
	// restart the old entry is deliberately left in place until the swap (see
	// runPipeline's recovery arm), so in that window the map still pointed at
	// the FAILED run: WaitPipeline joined the dead tomb and returned the
	// pre-recovery error for a pipeline that had just recovered, and Stop
	// stopped the dead run while the recovered one kept going — leaving a
	// pipeline nobody could stop and a persister that never quiesced. #2746/
	// #2812 fixed that window (Set before UpdateStatus) but left a second,
	// narrower one: close(registered) below hands worker goroutines their
	// release signal, and a worker released from <-registered can reach
	// w.Do — reading, processing and acking records — before this goroutine
	// gets back around to the Set call, if Set runs after it. On a recovery
	// restart that is exactly the same stale-map window as #2746, just
	// shrunk to two adjacent statements instead of spanning the UpdateStatus
	// call: a Stop landing between close(registered) and Set still resolves
	// the DEAD run (#2833). Publishing here, before close(registered), closes
	// it: by the time any worker can possibly be released to do anything
	// observable, the map already points at THIS run.
	//
	// This is the correct publish point, and it needs no rollback on error:
	//   - every earlier return in this function (sink Open, worker Open)
	//     fails before any goroutine is on the tomb, so nothing is published;
	//   - from here on the tomb owns termination, and its cleanup goroutine
	//     performs the matching runningPipelines.Delete — including when the
	//     UpdateStatus below fails;
	//   - that cleanup goroutine blocks on startupDone (closed below), so it
	//     can never Delete before this Set, which would strand a live run
	//     outside the map;
	//   - that Delete is a compare-and-delete (#2811), so when this run is a
	//     nested recovery restart, the OUTER run's cleanup can never erase
	//     this run's entry on its way through its own terminal block;
	//   - a failed UpdateStatus below is logged, not returned (#2899), so it
	//     cannot make the caller treat this live run as a failed start.
	pendingStop := s.publishRunningPipeline(rp.pipeline.ID, rp)

	// All N+1 goroutines (every worker plus the cleanup goroutine) are now
	// registered on the tomb, so release the workers: none of them can any
	// longer drive tomb.alive to 0 before the cleanup goroutine exists. This
	// must happen BEFORE the UpdateStatus call below, which is potentially
	// slow — the workers only need the registration barrier and the Set
	// above, not the status write (the cleanup goroutine is the one that
	// waits for that, via startupDone) — and it must happen AFTER the Set
	// above, per the invariant-7 comment there (#2833): a worker released
	// any earlier could act while the map still pointed at a dead run.
	close(registered)

	// testWorkersReleased, if set, lets a test observe/hold this exact
	// instant — workers released, run published, status not yet announced —
	// deterministically. See its doc.
	if s.testWorkersReleased != nil {
		s.testWorkersReleased(rp)
	}

	// Every goroutine of this run is on the tomb, so from here on the
	// admission is released when the tomb is dead, not by the deferred
	// release above.
	released = true
	t := rp.t
	go func() {
		<-t.Dead()
		s.runs.release()
	}()

	// Invariant 7 (#2901): if shutdown began after this run was admitted but
	// before it was published (e.g. a recovery restart that was opening its
	// connectors while StopAll iterated runningPipelines), StopAll could not
	// see it. The publication above happened before this read, and StopAll
	// sets the flag before it reads runningPipelines, so at least one of the
	// two sees the other. Stop it the way StopAll would have.
	if pendingStop != nil {
		// A user Stop arrived while this run was being built (#2899 item 2).
		// Apply it now, as Stop would have.
		if err := s.stopRunnablePipeline(context.Background(), rp, pendingStop.force, false); err != nil {
			s.logger.Warn(ctx).
				Err(err).
				Str(log.PipelineIDField, rp.pipeline.ID).
				Msg("could not stop pipeline that was stopped while starting")
		}
	}

	if s.runs.shuttingDown() {
		if err := s.stopRunnablePipeline(context.Background(), rp, false, true); err != nil {
			s.logger.Warn(ctx).
				Err(err).
				Str(log.PipelineIDField, rp.pipeline.ID).
				Msg("could not stop pipeline that started while shutting down")
		}
	}

	// It's now safe to make the potentially slow UpdateStatus call and then
	// release the cleanup goroutine to make its own. close(startupDone)
	// unconditionally, including on error, so the cleanup goroutine (already
	// blocked on it) is never left hanging.
	//
	// Invariant 7: once a run has goroutines it is registered and owns its
	// cleanup; a status write cannot unpublish it (#2898, #2899). The write
	// is a report, not a gate (design doc
	// 20261007-lifecycle-status-write-failure, rule R1): the workers are
	// running, so Start reports success whatever the write returns. Returning
	// the error used to tell the caller the start failed while the run moved
	// data, and on a recovery restart it sent the failed run's cleanup into
	// the recovery-failed arm, which wrote Degraded over the live restart and
	// notified OnFailure. The context ignores the tomb's cancellation: a run
	// that fails right away must not also fail its Running write and log a
	// spurious persistence failure; its cleanup writes the terminal status
	// after startupDone either way. See announceRunning for the bound.
	s.announceRunning(ctx, rp, startupDone)
	return nil
}

// statusWriteTimeout bounds how long Start waits for a run's StatusRunning
// write. The run is live before the write, so the bound only decides when
// Start returns; it never stops the run. Same value as pkg/lifecycle.
const statusWriteTimeout = 30 * time.Second

// runningWriteTimeout returns the bound on a status write: announceRunning's
// wait and the timeout writeStatus applies.
func (s *Service) runningWriteTimeout() time.Duration {
	if s.statusWriteTimeout > 0 {
		return s.statusWriteTimeout
	}
	return statusWriteTimeout
}

// announceRunning writes StatusRunning for rp, whose workers are running, and
// closes startupDone when the write returns. It returns when the write has
// returned or after statusWriteTimeout, whichever comes first, so a hung
// pipeline store cannot hold Start (or a recovery restart) forever. The write
// carries the same timeout, so backends that honour the context give up too;
// for one that ignores it (badger) the write goroutine finishes on its own,
// and the cleanup goroutine, which waits for startupDone, keeps the tomb and
// Wait from finishing before it does. Mirrors
// pkg/lifecycle.Service.announceRunning.
func (s *Service) announceRunning(ctx context.Context, rp *runnablePipeline, startupDone chan struct{}) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.runningWriteTimeout())
		err := s.pipelines.UpdateStatus(wctx, rp.pipeline.ID, pipeline.StatusRunning, "")
		cancel()
		close(startupDone)
		if err != nil {
			s.runningStatusNotPersisted(ctx, rp, err)
		}
	}()

	timer := time.NewTimer(s.runningWriteTimeout())
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		s.logger.Warn(ctx).
			Str(log.PipelineIDField, rp.pipeline.ID).
			Dur(log.DurationField, s.runningWriteTimeout()).
			Str("code", pipeline.CodeStatusPersistFailed.Reason()).
			Msg("pipeline status write did not return in time; the run is live and Start returns without waiting for it")
	}
}

// runningStatusNotPersisted handles a failed StatusRunning write for a run
// whose workers are already running. It logs the failure and leaves the run
// alone (pipeline.Service has already counted it), with one exception: if the
// pipeline no longer exists, it was deleted under the starting run, and the
// run is stopped as a user stop instead of moving data for a pipeline that is
// gone. Mirrors pkg/lifecycle.Service.runningStatusNotPersisted.
func (s *Service) runningStatusNotPersisted(ctx context.Context, rp *runnablePipeline, err error) {
	if cerrors.Is(err, pipeline.ErrInstanceNotFound) {
		s.logger.Err(ctx, err).
			Str(log.PipelineIDField, rp.pipeline.ID).
			Msg("pipeline was deleted while its run was starting; stopping the run")
		if stopErr := s.stopRunnablePipeline(context.Background(), rp, false, false); stopErr != nil {
			s.logger.Warn(ctx).
				Err(stopErr).
				Str(log.PipelineIDField, rp.pipeline.ID).
				Msg("could not stop the run of a deleted pipeline")
		}
		return
	}
	s.logStatusNotPersisted(ctx, rp.pipeline.ID, pipeline.StatusRunning, err)
}

// writeStatus writes status for rp from its cleanup goroutine. A failed write
// is logged and otherwise ignored: the caller goes on to record the terminal
// error, remove the run from runningPipelines and notify OnFailure whatever
// the write returned (#2899 item 4; design doc
// 20261007-lifecycle-status-write-failure, rule R2). Mirrors
// pkg/lifecycle.Service.writeStatus.
//
// The write is bounded by statusWriteTimeout, so on a backend that honours
// the context a hung store cannot hold the cleanup, and with it Wait, forever.
func (s *Service) writeStatus(ctx context.Context, rp *runnablePipeline, status pipeline.Status, errMsg string) {
	wctx, cancel := context.WithTimeout(ctx, s.runningWriteTimeout())
	defer cancel()
	if err := s.pipelines.UpdateStatus(wctx, rp.pipeline.ID, status, errMsg); err != nil {
		s.logStatusNotPersisted(ctx, rp.pipeline.ID, status, err)
	}
}

// logStatusNotPersisted logs a status write that did not reach the pipeline
// store. The run is unaffected; the stored status, which decides what the
// next boot starts, lags the in-memory one until a later write lands.
func (s *Service) logStatusNotPersisted(ctx context.Context, pipelineID string, status pipeline.Status, err error) {
	s.logger.Warn(ctx).
		Err(err).
		Str(log.PipelineIDField, pipelineID).
		Any(log.PipelineStatusField, status).
		Str("code", pipeline.CodeStatusPersistFailed.Reason()).
		Msg("pipeline status not persisted; the run is unaffected")
}

// publishRunningPipeline makes rp the live run for id in runningPipelines,
// replacing any previous entry. It takes publishMu so it is ordered against
// deleteRunningPipelineIfCurrent.
//
// The run's reservation becomes this entry in the same critical section, so a
// concurrent Start sees one or the other, never neither (invariant 2). It
// returns a Stop that arrived while the run was being built; the caller
// applies it.
func (s *Service) publishRunningPipeline(id string, rp *runnablePipeline) *pendingStop {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	s.runningPipelines.Set(id, rp)
	return s.consumeReservationLocked(rp)
}

// deleteRunningPipelineIfCurrent removes id's entry from runningPipelines only
// if it still holds exactly rp: a compare-and-delete, not a delete by key
// (#2811). A run's cleanup goroutine calls it as its last map write, and by
// then a newer run may own the key (a nested recovery restart whose status
// announcement failed, or an operator Start that landed after a Stop); the
// newer run's entry must survive. publishMu makes the compare and the delete
// one atomic step with respect to publishRunningPipeline. Mirrors
// pkg/lifecycle.Service.deleteRunningPipelineIfCurrent (#2806).
func (s *Service) deleteRunningPipelineIfCurrent(id string, rp *runnablePipeline) {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	// Invariant 7: only the run that owns the entry may remove it.
	if current, ok := s.runningPipelines.Get(id); ok && current == rp {
		if s.testCompareAndDeleteWindow != nil {
			s.testCompareAndDeleteWindow()
		}
		s.runningPipelines.Delete(id)
	}
}

// finishStopped writes the terminal status of a run that was stopped but
// ended with err, keeping err as the pipeline's error message (#2901). The
// caller stores err as the terminal error WaitPipeline returns and does not
// notify OnFailure handlers. Mirrors pkg/lifecycle.Service.finishStopped.
func (s *Service) finishStopped(ctx context.Context, rp *runnablePipeline, status pipeline.Status, err error) {
	s.logger.Warn(ctx).
		Err(err).
		Str(log.PipelineIDField, rp.pipeline.ID).
		Any(log.PipelineStatusField, status).
		Msg("pipeline stopped with an error after a stop was requested; not recovering")
	// we use %+v to get the stack trace too
	s.writeStatus(ctx, rp, status, fmt.Sprintf("%+v", err))
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

	// Invariant 2: a run that a Start has already taken over must not
	// restart the pipeline too.
	if !s.setPhase(rp, phaseBackoff) {
		return errRecoverySuperseded
	}
	// Whatever the recovery returns without restarting, the run is
	// finishing again afterwards.
	defer s.setPhase(rp, phaseFinishing)
	if !s.metricsDisabled {
		measure.PipelineRecoveringCount.WithValues(rp.pipeline.Config.Name).Inc()
	}

	// A failed Recovering write does not stop the recovery: the status is a
	// report (#2899). Before, the store error was returned as the recovery's
	// error, so a status-store blip degraded a pipeline that would have
	// recovered.
	s.writeStatus(ctx, rp, pipeline.StatusRecovering, "")

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
//   - nil: the pipeline was restarted; the new run owns terminal cleanup and
//     the caller must NOT run its cleanup tail.
//   - errRecoverySuperseded: a Start took the pipeline over while we waited;
//     the caller removes its own entry and does nothing else.
//   - errGracefulShutdownDuringRecovery: a graceful shutdown began during the
//     backoff wait; the caller finalizes a system stop instead of restarting.
//   - errIntentionalStopDuringRecovery: a user Stop arrived during the backoff
//     wait; the caller finalizes a user stop instead of restarting.
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
	s.publishMu.Lock()
	superseded := rp.supersededChLocked()
	s.publishMu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.runs.shutdownStarted():
		// Invariant 7 (#2901): end the wait as soon as shutdown begins, so
		// Wait is not held up by a backoff of up to MaxDelay.
		return errGracefulShutdownDuringRecovery
	case <-superseded:
		// A Start took over the pipeline (#2899 item 2).
		return errRecoverySuperseded
	case <-time.After(duration):
	}
	if s.testAfterBackoffWait != nil {
		s.testAfterBackoffWait(rp)
	}

	// If a graceful shutdown began while we waited, do not restart — finalize a
	// system stop instead (invariant 7). Checked after the guard so a legitimate
	// concurrent restart still wins.
	if s.isGracefulShutdown.Load() {
		return errGracefulShutdownDuringRecovery
	}

	// A user Stop on a Recovering pipeline resolves this (dead) run and marks
	// it intentionalStop. Without this check the restart below went ahead
	// anyway, restarting a pipeline the user had just stopped (#2901).
	if rp.intentionalStop.Load() {
		return errIntentionalStopDuringRecovery
	}

	// Invariant 2: the restart reserves like Start does, passing rp, so it
	// is granted only if rp is still the registered run and no Start took
	// over in the meantime. This replaces an unlocked "am I still the live
	// run" check that an external Start could race (#2899 item 2).
	err := s.startRun(ctx, rp.pipeline.ID, rp)
	if cerrors.Is(err, pipeline.ErrShuttingDown) {
		// Shutdown began between the check above and Start's admission
		// (#2901): same outcome as a shutdown during the wait.
		return errGracefulShutdownDuringRecovery
	}
	if err != nil && rp.intentionalStop.Load() {
		// A Stop arrived while the restart was building (it is recorded on
		// rp as well as on the restart's reservation) and the restart then
		// failed: the run ends stopped, not degraded, and does not notify.
		return errIntentionalStopDuringRecovery
	}
	return err
}

// predecessorStopErr is reserve's check for a recovery restart: the run it
// restarts must not have been asked to stop. publishMu must be held, which
// is also where Stop records the request.
func predecessorStopErr(rp *runnablePipeline) error {
	if rp.intentionalStop.Load() {
		return errIntentionalStopDuringRecovery
	}
	return nil
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
