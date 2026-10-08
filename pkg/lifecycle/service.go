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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/conduitio/conduit-commons/csync"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/conduitio/conduit/pkg/foundation/metrics/measure"
	"github.com/conduitio/conduit/pkg/lifecycle/stream"
	"github.com/conduitio/conduit/pkg/pipeline"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/jpillora/backoff"
	"gopkg.in/tomb.v2"
)

const InfiniteRetriesErrRecovery = -1

type FailureEvent struct {
	// ID is the ID of the pipeline which failed.
	ID    string
	Error error
}

type FailureHandler func(FailureEvent)

type ErrRecoveryCfg struct {
	MinDelay         time.Duration
	MaxDelay         time.Duration
	BackoffFactor    int
	MaxRetries       int64
	MaxRetriesWindow time.Duration
}

func (e *ErrRecoveryCfg) toBackoff() *backoff.Backoff {
	return &backoff.Backoff{
		Min:    e.MinDelay,
		Max:    e.MaxDelay,
		Factor: float64(e.BackoffFactor),
		Jitter: true,
	}
}

// Service manages pipelines.
type Service struct {
	logger log.CtxLogger

	errRecoveryCfg *ErrRecoveryCfg

	pipelines  PipelineService
	connectors ConnectorService

	processors       ProcessorService
	connectorPlugins ConnectorPluginService

	handlers         []FailureHandler
	runningPipelines *csync.Map[string, *runnablePipeline]

	// publishMu serializes WRITERS to runningPipelines — the publication in
	// runPipeline and every compare-and-delete — so the read-compare-delete
	// in deleteRunningPipelineIfCurrent is atomic with respect to a
	// concurrent publication. csync.Map has no compare-and-swap primitive, so
	// without this a stale owner can still erase a newer run's entry by
	// landing its Delete after another goroutine's Set: measured at 4 in
	// 200,000 races on the unserialized version, which is rare but is exactly
	// the bug class #2806 exists to close, so "rare" is not good enough.
	//
	// Readers (Get/All) deliberately do NOT take this: csync.Map has its own
	// RWMutex for memory safety, and a reader that observes a slightly stale
	// pointer is the pre-existing, acceptable case. This lock exists only to
	// make write-write ordering deterministic.
	//
	// It is never held across I/O or a node operation, so it cannot deadlock
	// against the stop path.
	publishMu sync.Mutex

	// terminalErrors holds the terminal error of a pipeline after it has stopped
	// and been removed from runningPipelines, so WaitPipeline can still report it
	// to a caller that races the pipeline's own cleanup. Written before the
	// runningPipelines entry is deleted; cleared when the pipeline is started
	// again. See docs/design-documents/20260706-forceful-stop-test-determinism.md.
	terminalErrors *csync.Map[string, error]

	// runs counts live runs and holds the shutdown flag (#2901). StopAll
	// begins the shutdown; from then on Start and recovery restarts are
	// refused, and Wait does not return while any run is live. See
	// runTracker.
	runs runTracker

	// testBeforePublish, if set, is called by runPipeline after the run's
	// node goroutines have started and before the run is published to
	// runningPipelines. It lets a test hold that window open, e.g. to begin a
	// shutdown inside it, instead of racing it. Nil in production: NewService
	// never sets it, and only this package's tests do. Same contract as
	// pkg/lifecycle-poc's testWorkersReleased.
	testBeforePublish func(rp *runnablePipeline)

	// statusWriteTimeout overrides the package's statusWriteTimeout when
	// positive. Zero in production; only this package's tests set it.
	statusWriteTimeout time.Duration
}

// NewService initializes and returns a lifecycle.Service.
func NewService(
	logger log.CtxLogger,
	errRecoveryCfg *ErrRecoveryCfg,
	connectors ConnectorService,
	processors ProcessorService,
	connectorPlugins ConnectorPluginService,
	pipelines PipelineService,
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
	}
}

type runnablePipeline struct {
	pipeline         *pipeline.Instance
	n                []stream.Node
	t                *tomb.Tomb
	backoff          *backoff.Backoff
	recoveryAttempts *atomic.Int64

	// stop is fired when a stop is requested for this run: a user Stop
	// (graceful or forced) or StopAll. A run whose stop was requested never
	// enters recovery, and a recovery already waiting out its backoff is
	// abandoned (#2901). Not carried over to a restarted run.
	stop stopSignal
}

// requestStop fires rp's stop signal, recording whether the request is a
// shutdown (system) and whether the run had already failed on its own.
func (rp *runnablePipeline) requestStop(system bool) {
	failedFirst := rp.t != nil && rp.t.Err() != tomb.ErrStillAlive
	rp.stop.fire(system, failedFirst)
}

// ConnectorService can fetch and create a connector instance, and report when
// every position/state write already queued for persistence has been
// durably committed — see WaitPersisted's doc and StopAndWait, which relies
// on it to await durability after a pipeline has fully drained.
type ConnectorService interface {
	Get(ctx context.Context, id string) (*connector.Instance, error)
	Create(ctx context.Context, id string, t connector.Type, plugin string, pipelineID string, cfg connector.Config, p connector.ProvisionType) (*connector.Instance, error)
	WaitPersisted()
}

// ProcessorService can fetch a processor instance and make a runnable processor from it.
type ProcessorService interface {
	Get(ctx context.Context, id string) (*processor.Instance, error)
	MakeRunnableProcessor(ctx context.Context, i *processor.Instance) (*processor.RunnableProcessor, error)
	// MakeRunnableProcessorForReconfigure builds a runnable for an
	// already-running instance without the running guard — for the live in-place
	// reconfigure swap (ReconfigureProcessor). See
	// processor.Service.MakeRunnableProcessorForReconfigure.
	MakeRunnableProcessorForReconfigure(ctx context.Context, i *processor.Instance) (*processor.RunnableProcessor, error)
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
// If the pipeline is already running, Start returns ErrPipelineRunning. Once
// StopAll has been called, Start refuses with an error coded
// pipeline.CodeShuttingDown (errors.Is(err, pipeline.ErrShuttingDown) holds).
func (s *Service) Start(
	ctx context.Context,
	pipelineID string,
) error {
	// Invariant 7: once shutdown has begun no new run starts, so nothing can
	// write positions after the runtime closes the database (#2901). This is
	// the early, cheap refusal; runPipeline's admission is the authoritative
	// one and covers a shutdown that begins while this Start is building.
	if shuttingDown, _ := s.runs.shuttingDown(); shuttingDown {
		return errShuttingDown(pipelineID)
	}

	pl, err := s.pipelines.Get(ctx, pipelineID)
	if err != nil {
		return err
	}

	if pl.GetStatus() == pipeline.StatusRunning {
		// Invariant: errors.Is(err, ErrPipelineRunning) still holds — sentinel
		// wrapped, ConduitError adds the code.
		err := conduiterr.Wrap(
			pipeline.CodePipelineRunning,
			fmt.Sprintf("can't start pipeline %s: %s", pl.ID, pipeline.ErrPipelineRunning),
			pipeline.ErrPipelineRunning,
		)
		err.Suggestion = "the pipeline is already running; stop it first if you need to restart it"
		return err
	}

	s.logger.Debug(ctx).Str(log.PipelineIDField, pl.ID).Msg("starting pipeline")
	s.logger.Trace(ctx).Str(log.PipelineIDField, pl.ID).Msg("building nodes")

	rp, err := s.buildRunnablePipeline(ctx, pl)
	if err != nil {
		return cerrors.Errorf("could not build nodes for pipeline %s: %w", pl.ID, err)
	}

	// We check if the pipeline was previously running and get the backoff configuration from it.
	if oldRp, ok := s.runningPipelines.Get(pipelineID); ok {
		rp.backoff = oldRp.backoff
		rp.recoveryAttempts = oldRp.recoveryAttempts
	}

	// A new run supersedes any terminal error recorded by a previous run of this
	// pipeline, so a later WaitPipeline can't return a stale result.
	s.terminalErrors.Delete(pipelineID)

	s.logger.Trace(ctx).Str(log.PipelineIDField, pl.ID).Msg("running nodes")
	// runPipeline publishes rp into runningPipelines itself, at the point the
	// run actually goes live — see the Set call there for why that ordering
	// is load-bearing (#2806) and why this function must not do it after the
	// fact.
	if err := s.runPipeline(ctx, rp); err != nil {
		return cerrors.Errorf("failed to run pipeline %s: %w", pl.ID, err)
	}
	s.logger.Info(ctx).Str(log.PipelineIDField, pl.ID).Msg("pipeline started")

	return nil
}

// StartWithBackoff starts a pipeline with a backoff.
// It'll check the number of times the pipeline has been restarted and the duration of the backoff.
// When the pipeline has reached out the maximum number of retries, it'll return a fatal error.
//
// It returns errRecoveryAborted without restarting if a stop is requested for
// rp, or the service begins shutting down, before the restart goes live.
// Either one also ends the backoff wait early.
func (s *Service) StartWithBackoff(ctx context.Context, rp *runnablePipeline) error {
	// Invariant 7 (#2901): a run whose stop was requested is never restarted,
	// and nothing is restarted once shutdown has begun.
	if s.recoveryAborted(rp) {
		return errRecoveryAborted
	}

	// Increment number of recovery attempts.
	attempt := rp.recoveryAttempts.Add(1)

	if s.errRecoveryCfg.MaxRetries != InfiniteRetriesErrRecovery && attempt > s.errRecoveryCfg.MaxRetries {
		return cerrors.FatalError(cerrors.Errorf("failed to recover pipeline %s after %d attempts: %w", rp.pipeline.ID, attempt, pipeline.ErrPipelineCannotRecover))
	}

	duration := rp.backoff.ForAttempt(float64(attempt))
	s.logger.Info(ctx).
		Str(log.PipelineIDField, rp.pipeline.ID).
		Dur(log.DurationField, duration).
		Int64(log.AttemptField, attempt).
		Msg("restarting with backoff")

	time.AfterFunc(duration+s.errRecoveryCfg.MaxRetriesWindow, func() {
		s.logger.Debug(ctx).
			Str(log.PipelineIDField, rp.pipeline.ID).
			Dur(log.DurationField, duration).
			Int64(log.AttemptField, attempt).
			Msg("decreasing recovery attempts")
		rp.recoveryAttempts.Add(-1) // Decrement the number of attempts after delay.
	})

	// This results in a default delay progression of 1s, 2s, 4s, 8s, 16s, [...], 10m, 10m,... balancing the need for recovery time and minimizing downtime.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-rp.stop.done():
		return errRecoveryAborted
	case <-s.runs.shutdownStarted():
		return errRecoveryAborted
	case <-time.After(duration):
	}

	// The user may have stopped or restarted the pipeline while we were waiting.
	actualRp, ok := s.runningPipelines.Get(rp.pipeline.ID)
	if !ok || actualRp != rp {
		return nil
	}

	// select picks at random among ready cases, so the timer can win against
	// a stop that arrived at the same moment. Check again.
	if s.recoveryAborted(rp) {
		return errRecoveryAborted
	}

	err := s.Start(ctx, rp.pipeline.ID)
	if cerrors.Is(err, pipeline.ErrShuttingDown) {
		// Shutdown began between the check above and Start's admission.
		return errRecoveryAborted
	}
	return err
}

// errRecoveryAborted is returned by StartWithBackoff when it abandons a
// restart because a stop was requested for the run or the service began
// shutting down (#2901). The cleanup goroutine in runPipeline turns it into a
// terminal status instead of a restart.
var errRecoveryAborted = cerrors.New("recovery aborted: stop requested or shutting down")

// recoveryAborted reports whether a pending recovery restart of rp must be
// abandoned: a stop was requested for rp, or the service is shutting down.
func (s *Service) recoveryAborted(rp *runnablePipeline) bool {
	shuttingDown, _ := s.runs.shuttingDown()
	return shuttingDown || rp.stop.requested()
}

// Stop will attempt to gracefully stop a given pipeline by calling each node's
// Stop function. If force is set to true the pipeline won't stop gracefully,
// instead the context for all nodes will be canceled which causes them to stop
// running as soon as possible.
func (s *Service) Stop(ctx context.Context, pipelineID string, force bool) error {
	rp, ok := s.runningPipelines.Get(pipelineID)

	if !ok {
		// Invariant: errors.Is(err, ErrPipelineNotRunning) still holds — sentinel
		// wrapped, ConduitError adds the code.
		err := conduiterr.Wrap(
			pipeline.CodePipelineNotRunning,
			fmt.Sprintf("pipeline %s is not running: %s", pipelineID, pipeline.ErrPipelineNotRunning),
			pipeline.ErrPipelineNotRunning,
		)
		err.Suggestion = "start the pipeline before trying to stop it"
		return err
	}

	// Read the status once. Reading it separately for each comparison (and
	// again for the message) let a Recovering -> Running transition between
	// the reads refuse a stop of a pipeline that was Recovering, then
	// Running, with "can't stop pipeline with status Running" (#2912 S4).
	if status := rp.pipeline.GetStatus(); status != pipeline.StatusRunning && status != pipeline.StatusRecovering {
		// Invariant: errors.Is(err, ErrPipelineNotRunning) still holds — sentinel
		// wrapped, ConduitError adds the code.
		err := conduiterr.Wrap(
			pipeline.CodePipelineNotRunning,
			fmt.Sprintf("can't stop pipeline with status %q: %s", status, pipeline.ErrPipelineNotRunning),
			pipeline.ErrPipelineNotRunning,
		)
		err.Suggestion = "start the pipeline before trying to stop it"
		return err
	}

	switch force {
	case false:
		return s.stopGraceful(ctx, rp, nil, false)
	case true:
		return s.stopForceful(ctx, rp)
	}
	panic("unreachable code")
}

// stopGraceful asks rp's source nodes to stop with reason and lets the run
// drain. system is true for a shutdown (StopAll), false for a user Stop; it
// decides between SystemStopped and UserStopped when the run ends.
func (s *Service) stopGraceful(ctx context.Context, rp *runnablePipeline, reason error, system bool) error {
	s.logger.Info(ctx).
		Str(log.PipelineIDField, rp.pipeline.ID).
		Any(log.PipelineStatusField, rp.pipeline.GetStatus()).
		Msg("gracefully stopping pipeline")

	// Invariant 7 (#2901): record the stop request before any node is told to
	// stop, so that whatever the drain returns (a node error, or the reason
	// itself echoed back by the source) the cleanup goroutine treats this run
	// as stopped and never restarts it. Also abandons a recovery that is
	// waiting out its backoff for this run.
	alreadyStopping := rp.stop.requested()
	rp.requestStop(system)

	var errs []error
	for _, n := range rp.n {
		if node, ok := n.(stream.StoppableNode); ok {
			// stop all pub nodes
			s.logger.Trace(ctx).Str(log.NodeIDField, n.ID()).Msg("stopping node")
			err := node.Stop(ctx, reason)
			if err != nil {
				// A node refusing a second stop while the first one drains
				// ("stop already triggered") is expected when a stop was
				// already in progress for this run: debug, not error.
				e := s.logger.Err(ctx, err)
				if alreadyStopping {
					e = s.logger.Debug(ctx).Err(err)
				}
				e.Str(log.NodeIDField, n.ID()).Msg("stop failed")
				errs = append(errs, err)
			}
		}
	}

	return cerrors.Join(errs...)
}

func (s *Service) stopForceful(ctx context.Context, rp *runnablePipeline) error {
	s.logger.Info(ctx).
		Str(log.PipelineIDField, rp.pipeline.ID).
		Any(log.PipelineStatusField, rp.pipeline.GetStatus()).
		Msg("force stopping pipeline")

	// Record the stop request first (#2901): the run then ends UserStopped
	// with the force-stop error recorded, never recovers, and a recovery
	// waiting out its backoff for this run is abandoned. The fatal error
	// below is what cancels the nodes' context.
	rp.requestStop(false)

	// Creates a FatalError to prevent the pipeline from recovering.
	rp.t.Kill(cerrors.FatalError(pipeline.ErrForceStop))
	for _, n := range rp.n {
		if node, ok := n.(stream.ForceStoppableNode); ok {
			// stop all pub nodes
			s.logger.Trace(ctx).Str(log.NodeIDField, n.ID()).Msg("force stopping node")
			node.ForceStop(ctx)
		}
	}
	return nil
}

// StopAll will ask all the running pipelines to stop gracefully
// (i.e. that existing messages get processed but not new messages get produced).
//
// StopAll is the shutdown path: it puts the service into shutdown mode, which
// is permanent. From then on Start refuses with pipeline.CodeShuttingDown, no
// run is restarted by recovery, and a run that ends with an error (including
// a non-graceful reason that the source returns as its error) is reported
// SystemStopped with the error recorded instead of being recovered, unless it
// had already failed fatally on its own before the stop (#2901).
func (s *Service) StopAll(ctx context.Context, reason error) {
	// Invariant 7 (#2901): begin the shutdown before reading the running
	// pipelines, under publishMu. runPipeline publishes a run under the same
	// lock and reads the flag in the same critical section, so every run is
	// either in the map iterated below, or sees the flag and stops itself
	// right after publishing. No run can slip past both.
	s.publishMu.Lock()
	s.runs.beginShutdown(reason)
	s.publishMu.Unlock()

	for _, rp := range s.runningPipelines.All() {
		p := rp.pipeline
		// Invariant 7 (#2912 B1): stop every run that is still alive, whatever
		// its status says. A run is published before it announces
		// StatusRunning, so for a moment its entry carries the previous run's
		// status (UserStopped after a Stop, SystemStopped at boot, Degraded,
		// or none for a new pipeline). It read the shutdown flag before it
		// was set, so it will not stop itself; skipping it here by status
		// would leave it running past Wait.
		if rp.t == nil || !rp.t.Alive() {
			continue
		}
		alreadyStopping := rp.stop.requested()
		err := s.stopGraceful(ctx, rp, reason, true)
		if err != nil {
			s.logStopError(ctx, alreadyStopping, err, p.ID, "could not stop pipeline")
		}
	}
	// TODO stop pipelines forcefully after timeout if they are still running
}

// Wait blocks until all pipelines are stopped or until the timeout is reached.
// "All" includes runs that started after Wait was called; after StopAll no new
// run can start, so a nil or error return means no run is live (#2901).
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
			errs = append(errs, err)
		}
	}

	// Invariant 7 (#2901): the snapshot above can miss runs. A recovery
	// restart publishes its run from inside the old run's cleanup goroutine,
	// possibly after the snapshot was taken, and the old tomb dies as soon as
	// that restart returns, while the new run is still going. The runtime
	// flushes the persister and closes the database right after Wait, so
	// returning here with any run live would let it write positions after
	// the close. Wait until no run is live at all.
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

// StopAndWait gracefully stops the pipeline with the given ID and blocks
// until it has reached full quiescence: every node goroutine has exited
// (drain complete — see stream.SourceNode.Run's openMsgTracker.Wait(), which
// keeps the source's Teardown from running until every in-flight message has
// been acked or nacked end-to-end) AND every connector position/state write
// that drain triggered has been durably flushed to the store.
//
// Callers MUST use StopAndWait instead of Stop when they intend to mutate the
// pipeline's stored connectors/processors/config immediately afterward (e.g.
// provisioning.Service.ApplyPlanLive's stop-drain-restart). Stop(ctx, id,
// false) alone only injects a stop-control-message into the source node and
// returns — it does not wait for the drain, let alone the position flush, to
// complete. Racing a mutation against that in-flight drain could tear down or
// reconfigure a connector while old goroutines are still mid-flight,
// corrupting acks or losing the final position write.
//
// Invariant 1 (never ack upstream before the downstream write is durable) and
// invariant 3 (at-least-once: no path may drop a record without delivering or
// DLQ-routing it) are why StopAndWait blocks on both signals rather than just
// the first: WaitPipeline alone proves every record was acked end-to-end, but
// says nothing about whether the resulting position write actually reached
// disk — connector.Persister batches and flushes asynchronously, so without
// the second wait a caller could observe a "stopped" pipeline whose last
// checkpoint is still only in memory, and mutate/restart it into that gap.
// See docs/design-documents/20260708-live-server-deploy-apply.md, "Review
// outcome & required rework", blocker 1, which traced this exact race in the
// original (pre-rework) design.
//
// StopAndWait requires the pipeline to already be running (it delegates to
// Stop, which returns pipeline.ErrPipelineNotRunning-coded errors otherwise)
// and only ever stops gracefully — there is no forceful variant, since a
// forceful stop provides none of the drain/flush guarantees a caller needing
// this primitive is asking for.
func (s *Service) StopAndWait(ctx context.Context, pipelineID string) error {
	if err := s.Stop(ctx, pipelineID, false); err != nil {
		return cerrors.Errorf("could not stop pipeline %s: %w", pipelineID, err)
	}

	if err := s.WaitPipeline(pipelineID); err != nil {
		return cerrors.Errorf("pipeline %s did not stop gracefully: %w", pipelineID, err)
	}

	// Invariant 1/3: do not return — and thus do not let a caller mutate or
	// tear down this pipeline's connectors — until every position/state write
	// the drain above already triggered is durably persisted.
	s.connectors.WaitPersisted()

	return nil
}

// buildsNodes will build new nodes that will be assigned to the pipeline.Instance.
func (s *Service) buildNodes(ctx context.Context, pl *pipeline.Instance) ([]stream.Node, error) {
	// setup many to many channels
	fanIn := stream.FaninNode{Name: "fanin"}
	fanOut := stream.FanoutNode{Name: "fanout"}

	sourceNodes, err := s.buildSourceNodes(ctx, pl, &fanIn)
	if err != nil {
		return nil, cerrors.Errorf("could not build source nodes: %w", err)
	}
	if len(sourceNodes) == 0 {
		return nil, cerrors.New("can't build pipeline without any source connectors")
	}

	processorNodes, err := s.buildProcessorNodes(ctx, pl, pl.ProcessorIDs, &fanIn, &fanOut)
	if err != nil {
		return nil, cerrors.Errorf("could not build processor nodes: %w", err)
	}

	destinationNodes, err := s.buildDestinationNodes(ctx, pl, &fanOut)
	if err != nil {
		return nil, cerrors.Errorf("could not build destination nodes: %w", err)
	}
	if len(destinationNodes) == 0 {
		return nil, cerrors.New("can't build pipeline without any destination connectors")
	}

	// gather nodes and add our fan in and fan out nodes
	nodes := make([]stream.Node, 0, len(processorNodes)+len(sourceNodes)+len(destinationNodes)+2)
	nodes = append(nodes, sourceNodes...)
	nodes = append(nodes, &fanIn)
	nodes = append(nodes, processorNodes...)
	nodes = append(nodes, &fanOut)
	nodes = append(nodes, destinationNodes...)

	// set up logger for all nodes that need it
	nodeLogger := s.logger
	nodeLogger.Logger = nodeLogger.Logger.With().Str(log.PipelineIDField, pl.ID).Logger()
	for _, n := range nodes {
		stream.SetLogger(n, nodeLogger)
	}
	return nodes, nil
}

// buildRunnablePipeline will build and connect all nodes configured in the pipeline.
func (s *Service) buildRunnablePipeline(
	ctx context.Context,
	pl *pipeline.Instance,
) (*runnablePipeline, error) {
	nodes, err := s.buildNodes(ctx, pl)
	if err != nil {
		return nil, err
	}

	return &runnablePipeline{
		pipeline:         pl,
		n:                nodes,
		backoff:          s.errRecoveryCfg.toBackoff(),
		recoveryAttempts: &atomic.Int64{},
	}, nil
}

func (s *Service) buildProcessorNodes(
	ctx context.Context,
	pl *pipeline.Instance,
	processorIDs []string,
	first stream.PubNode,
	last stream.SubNode,
) ([]stream.Node, error) {
	var nodes []stream.Node

	prev := first
	for _, procID := range processorIDs {
		instance, err := s.processors.Get(ctx, procID)
		if err != nil {
			return nil, cerrors.Errorf("could not fetch processor: %w", err)
		}

		runnableProc, err := s.processors.MakeRunnableProcessor(ctx, instance)
		if err != nil {
			return nil, err
		}

		var node stream.PubSubNode
		if instance.Config.Workers > 1 {
			node = s.buildParallelProcessorNode(pl, runnableProc)
		} else {
			node = s.buildProcessorNode(pl, runnableProc)
		}

		node.Sub(prev.Pub())
		prev = node

		nodes = append(nodes, node)
	}

	last.Sub(prev.Pub())
	return nodes, nil
}

func (s *Service) buildParallelProcessorNode(
	pl *pipeline.Instance,
	proc *processor.RunnableProcessor,
) *stream.ParallelNode {
	return &stream.ParallelNode{
		Name: proc.ID + "-parallel",
		NewNode: func(i int) stream.PubSubNode {
			n := s.buildProcessorNode(pl, proc)
			n.Name = n.Name + "-" + strconv.Itoa(i) // add suffix to name
			return n
		},
		Workers: proc.Config.Workers,
	}
}

func (s *Service) buildProcessorNode(
	pl *pipeline.Instance,
	proc *processor.RunnableProcessor,
) *stream.ProcessorNode {
	return &stream.ProcessorNode{
		Name:           proc.ID,
		Processor:      proc,
		ProcessorTimer: measure.ProcessorExecutionDurationTimer.WithValues(pl.Config.Name, proc.Plugin, proc.ID),
	}
}

func (s *Service) buildSourceNodes(
	ctx context.Context,
	pl *pipeline.Instance,
	next stream.SubNode,
) ([]stream.Node, error) {
	var nodes []stream.Node

	dlqHandlerNode, err := s.buildDLQHandlerNode(ctx, pl)
	if err != nil {
		return nil, err
	}

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

		sourceNode := stream.SourceNode{
			Name:   instance.ID,
			Source: src.(*connector.Source),
			PipelineTimer: measure.PipelineExecutionDurationTimer.WithValues(
				pl.Config.Name,
			),
		}
		dlqHandlerNode.Add(1)
		ackerNode := s.buildSourceAckerNode(src.(*connector.Source), dlqHandlerNode)
		ackerNode.Sub(sourceNode.Pub())
		metricsNode := s.buildMetricsNode(pl, instance)
		metricsNode.Sub(ackerNode.Pub())

		procNodes, err := s.buildProcessorNodes(ctx, pl, instance.ProcessorIDs, metricsNode, next)
		if err != nil {
			return nil, cerrors.Errorf("could not build processor nodes for connector %s: %w", instance.ID, err)
		}

		nodes = append(nodes, &sourceNode, ackerNode, metricsNode)
		nodes = append(nodes, procNodes...)
	}

	if len(nodes) != 0 {
		nodes = append(nodes, dlqHandlerNode)
	}
	return nodes, nil
}

func (s *Service) buildSourceAckerNode(
	src *connector.Source,
	dlqHandlerNode *stream.DLQHandlerNode,
) *stream.SourceAckerNode {
	return &stream.SourceAckerNode{
		Name:           src.Instance.ID + "-acker",
		Source:         src,
		DLQHandlerNode: dlqHandlerNode,
	}
}

func (s *Service) buildDLQHandlerNode(
	ctx context.Context,
	pl *pipeline.Instance,
) (*stream.DLQHandlerNode, error) {
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

	return &stream.DLQHandlerNode{
		Name:    conn.ID,
		Handler: &DLQDestination{Destination: dest.(*connector.Destination)},

		WindowSize:          pl.DLQ.WindowSize,
		WindowNackThreshold: pl.DLQ.WindowNackThreshold,

		Timer: measure.DLQExecutionDurationTimer.WithValues(
			pl.Config.Name,
			pl.DLQ.Plugin,
		),
		Histogram: metrics.NewRecordBytesHistogram(
			measure.DLQBytesHistogram.WithValues(
				pl.Config.Name,
				pl.DLQ.Plugin,
			),
		),
	}, nil
}

func (s *Service) buildMetricsNode(
	pl *pipeline.Instance,
	conn *connector.Instance,
) *stream.MetricsNode {
	return &stream.MetricsNode{
		Name: conn.ID + "-metrics",
		Histogram: metrics.NewRecordBytesHistogram(
			measure.ConnectorBytesHistogram.WithValues(
				pl.Config.Name,
				conn.Plugin,
				strings.ToLower(conn.Type.String()),
				conn.ID,
			),
		),
	}
}

func (s *Service) buildDestinationAckerNode(
	dest *connector.Destination,
) *stream.DestinationAckerNode {
	return &stream.DestinationAckerNode{
		Name:        dest.Instance.ID + "-acker",
		Destination: dest,
	}
}

func (s *Service) buildDestinationNodes(
	ctx context.Context,
	pl *pipeline.Instance,
	prev stream.PubNode,
) ([]stream.Node, error) {
	var nodes []stream.Node

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

		ackerNode := s.buildDestinationAckerNode(dest.(*connector.Destination))
		destinationNode := stream.DestinationNode{
			Name:        instance.ID,
			Destination: dest.(*connector.Destination),
			ConnectorTimer: measure.ConnectorExecutionDurationTimer.WithValues(
				pl.Config.Name,
				instance.Plugin,
				strings.ToLower(instance.Type.String()),
				instance.ID,
			),
		}
		metricsNode := s.buildMetricsNode(pl, instance)
		destinationNode.Sub(metricsNode.Pub())
		ackerNode.Sub(destinationNode.Pub())

		connNodes, err := s.buildProcessorNodes(ctx, pl, instance.ProcessorIDs, prev, metricsNode)
		if err != nil {
			return nil, cerrors.Errorf("could not build processor nodes for connector %s: %w", instance.ID, err)
		}

		nodes = append(nodes, connNodes...)
		nodes = append(nodes, metricsNode, &destinationNode, ackerNode)
	}

	return nodes, nil
}

func (s *Service) runPipeline(ctx context.Context, rp *runnablePipeline) error {
	if rp.t != nil && rp.t.Alive() {
		return pipeline.ErrPipelineRunning
	}

	// Invariant 7 (#2901): count this run as live before any of its
	// goroutines exist, and refuse it once shutdown has begun. Wait blocks
	// until every admitted run's tomb is dead.
	if !s.runs.admit() {
		return errShuttingDown(rp.pipeline.ID)
	}

	// the tomb is responsible for running goroutines related to the pipeline
	rp.t = &tomb.Tomb{}

	// keep tomb alive until the end of this function, this way we guarantee we
	// can run the cleanup goroutine even if all nodes stop before we get to it
	keepAlive := make(chan struct{})
	rp.t.Go(func() error {
		<-keepAlive
		return nil
	})
	defer close(keepAlive)

	// Release the admission once every goroutine of this run, including the
	// cleanup goroutine and any recovery restart it runs, has returned. The
	// tomb has a goroutine (keepAlive) from here on, so Dead always closes.
	t := rp.t
	go func() {
		<-t.Dead()
		s.runs.release()
	}()

	// nodesWg is done once all nodes stop running
	var nodesWg sync.WaitGroup
	var isGracefulShutdown atomic.Bool
	for _, node := range rp.n {
		nodesWg.Add(1)

		rp.t.Go(func() (errOut error) {
			// If any of the nodes stop, the tomb will be put into a dying state
			// and ctx will be cancelled.
			// This way, the other nodes will be notified that they need to stop too.
			//nolint:staticcheck // nil used to use the default (parent provided via WithContext)
			ctx := rp.t.Context(nil)
			s.logger.Trace(ctx).Str(log.NodeIDField, node.ID()).Msg("running node")
			defer func() {
				e := s.logger.Trace(ctx)
				if errOut != nil {
					e = s.logger.Err(ctx, errOut) // increase the log level to error
				}
				e.Str(log.NodeIDField, node.ID()).Msg("node stopped")
			}()
			defer nodesWg.Done()

			err := node.Run(ctx)
			if cerrors.Is(err, pipeline.ErrGracefulShutdown) {
				// This node was shutdown because of ErrGracefulShutdown, we
				// need to stop this goroutine without returning an error to let
				// other nodes stop gracefully. We set a boolean that lets the
				// cleanup routine know this was a graceful shutdown in case no
				// other error is returned.
				isGracefulShutdown.Store(true)
				return nil
			}
			if err != nil {
				err = cerrors.Errorf("node %s stopped with error: %w", node.ID(), err)
				// Record the error on the tomb here, synchronously, before
				// the deferred nodesWg.Done() above runs (#2896). tomb.v2
				// only records a t.Go'd function's return value after the
				// function returns, i.e. after Done() and the "node stopped"
				// log write. In that window the cleanup goroutine below can
				// wake from nodesWg.Wait(), read rp.t.Err() as ErrStillAlive
				// and classify a failed pipeline as user-stopped: no
				// Degraded status, no OnFailure handlers, and for a
				// transient error no recovery. Killing first means every
				// node error is visible to rp.t.Err() by the time nodesWg
				// reaches zero.
				//
				// Kill keeps the first non-nil reason, so this does not
				// override an earlier reason (e.g. stopForceful's
				// ErrForceStop or a sibling node's error), and the
				// t.kill(err) tomb.run does after we return is a no-op.
				// pkg/lifecycle-poc's worker goroutine does the same.
				rp.t.Kill(err)
				return err
			}
			return nil
		})
	}

	// startupDone is closed once the StatusRunning write below has returned.
	// The cleanup goroutine waits on it before it writes the terminal status,
	// so the two writes to the same *pipeline.Instance never overlap and the
	// terminal status can never be overwritten by a late Running. Same
	// barrier as pkg/lifecycle-poc's runPipeline.
	startupDone := make(chan struct{})

	// Publish this run as THE live run for its pipeline ID, here and not in
	// Start (#2806, same invariant as pkg/lifecycle-poc's #2746 fix — see
	// that package's runPipeline for the mirrored comment).
	//
	// Invariant established here: at the publication window — from the moment
	// StatusRunning is observable — runningPipelines[id] is the run that is
	// actually running.
	//
	// Deliberately scoped. It is NOT a general claim that the map always
	// tracks the live run: during StartWithBackoff's sleep the map holds the
	// dead pre-recovery run on purpose, for MinDelay..MaxDelay (1s..10m), and
	// Stop admits StatusRecovering (:299). That window is orders of magnitude
	// larger than this one and is a separate, pre-existing bug — see the
	// issue filed alongside this change. Do not read this comment as saying
	// that one is covered. Every public
	// entry point resolves a pipeline through this map — Stop, StopAll,
	// WaitPipeline, StopAndWait (and thus provisioning.ApplyPlanLive) — and
	// StartWithBackoff's "am I still the live pipeline" guard (:270) is a
	// pointer comparison against it. A stale entry does not fail loudly: it
	// makes all of them silently operate on the previous, already-dead run.
	//
	// Start used to Set this AFTER runPipeline returned, i.e. after the
	// UpdateStatus below had already announced StatusRunning. On a recovery
	// restart the old entry is deliberately left in place until the swap
	// (see the recovery arm below), so in that window the map still pointed
	// at the FAILED run. WaitPipeline joined the dead tomb and returned the
	// pre-recovery error for a pipeline that had just recovered.
	//
	// Stop, precisely: it resolves the dead run and returns an error from
	// SourceNode.Stop ("source node is not running", stream/source.go:193-200,
	// since a dead run's source is already stopped) — it does NOT silently
	// report success, and StopAndWait therefore surfaces that error to
	// provisioning.ApplyPlanLive rather than proceeding. The invariant-7
	// violation arrives through StopAll instead: it swallows that error into a
	// log warning (:366-372), runtime then calls ls.Wait(exitTimeout), which
	// resolves instantly off the dead tomb, and shutdown proceeds to quiesce
	// the persister and close the DB while the recovered run is still live.
	s.publishRunningPipeline(rp)

	// Invariant 7: once a run has goroutines it is registered and owns its
	// cleanup; a status write cannot unpublish it (#2898). The cleanup
	// goroutine is registered before the StatusRunning write, so whatever
	// that write returns, the run's terminal status, terminal error, map
	// removal and OnFailure notification are owned by it. The tomb cannot
	// have died yet: keepAlive holds it until this function returns.
	rp.t.Go(func() error {
		return s.cleanupRun(rp, &nodesWg, &isGracefulShutdown, startupDone)
	})

	// The status write is a report, not a gate (design doc
	// 20261007-lifecycle-status-write-failure, rule R1). The nodes are
	// already running, so a failed write must not change what the run does:
	// the in-memory status already says Running, Stop and StopAll reach the
	// run through the entry published above, and Start reports success
	// because the pipeline is running. See announceRunning for how the
	// write is bounded and why it ignores the caller's cancellation.
	s.announceRunning(ctx, rp, startupDone)
	return nil
}

// statusWriteTimeout bounds how long Start waits for a run's StatusRunning
// write. The run is live before the write, so the bound only decides when
// Start returns; it never stops the run.
const statusWriteTimeout = 30 * time.Second

// runningWriteTimeout returns the bound announceRunning applies.
func (s *Service) runningWriteTimeout() time.Duration {
	if s.statusWriteTimeout > 0 {
		return s.statusWriteTimeout
	}
	return statusWriteTimeout
}

// announceRunning writes StatusRunning for rp, whose nodes are running, and
// closes startupDone when the write returns. It returns when the write has
// returned or after statusWriteTimeout, whichever comes first, so a hung
// pipeline store cannot hold Start (or a recovery restart) forever.
//
// The write uses a context that ignores the caller's cancellation (an API
// client that gives up must not fail it on backends that honour the context,
// SQLite and Postgres) and carries the same timeout, so those backends give
// up too. A backend that ignores the context (badger) is still bounded here:
// the write goroutine is left to finish on its own. It cannot outlive the
// run unnoticed: the cleanup goroutine waits for startupDone, so the tomb,
// and with it Wait, does not finish before the write returns.
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
// whose nodes are already running. It logs the failure and leaves the run
// alone (pipeline.Service has already counted it), with one exception: if the
// pipeline no longer exists, it was deleted under the starting run, and the
// run is stopped as a user stop instead of moving data for a pipeline that is
// gone.
func (s *Service) runningStatusNotPersisted(ctx context.Context, rp *runnablePipeline, err error) {
	if cerrors.Is(err, pipeline.ErrInstanceNotFound) {
		s.logger.Err(ctx, err).
			Str(log.PipelineIDField, rp.pipeline.ID).
			Msg("pipeline was deleted while its run was starting; stopping the run")
		// Detached context, as in publishRunningPipeline: ctx can be an API
		// request's.
		alreadyStopping := rp.stop.requested()
		if stopErr := s.stopGraceful(context.Background(), rp, nil, false); stopErr != nil {
			s.logStopError(context.Background(), alreadyStopping, stopErr, rp.pipeline.ID,
				"could not stop the run of a deleted pipeline")
		}
		return
	}
	s.logStatusNotPersisted(ctx, rp.pipeline.ID, pipeline.StatusRunning, err)
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

// cleanupRun is the cleanup goroutine of a run started by runPipeline. It
// waits for every node to stop, classifies how the run ended, writes the
// terminal status, records the terminal error, removes the run from
// runningPipelines (compare-and-delete) and notifies OnFailure handlers for
// real failures. For a transient failure with no stop requested it runs
// recovery instead, synchronously, on this run's tomb.
//
// startupDone is closed by runPipeline once its StatusRunning write has
// returned; the terminal status is written only after that.
func (s *Service) cleanupRun(rp *runnablePipeline, nodesWg *sync.WaitGroup, isGracefulShutdown *atomic.Bool, startupDone <-chan struct{}) error {
	// use fresh context for cleanup function, otherwise the updated status
	// won't be stored
	ctx := context.Background()

	nodesWg.Wait()
	// Never write the terminal status while runPipeline's StatusRunning write
	// is in flight: pipeline.Service.UpdateStatus is not safe to call
	// concurrently for one pipeline, and a Running landing after the terminal
	// status would overwrite it. See startupDone in runPipeline.
	<-startupDone
	err := rp.t.Err()
	// stoppedWithErr is set when the run was stopped (not failed) but
	// still ended with an error. That error is recorded and returned,
	// but it is not a failure: OnFailure is not notified.
	stoppedWithErr := false

	switch err {
	case tomb.ErrStillAlive:
		// not an actual error, the pipeline stopped gracefully
		err = nil
		var status pipeline.Status
		if isGracefulShutdown.Load() {
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
		stopRequested, systemStop, failedFirst := rp.stop.state()
		switch {
		case cerrors.IsFatalError(err) && (!stopRequested || failedFirst):
			// The run failed on its own with a fatal error, before any
			// stop was requested: it is degraded.
			// we use %+v to get the stack trace too
			if err := s.pipelines.UpdateStatus(ctx, rp.pipeline.ID, pipeline.StatusDegraded, fmt.Sprintf("%+v", err)); err != nil {
				return err
			}
		case stopRequested:
			// Invariant 7 (#2901, ADR 20261007-stop-requested-never-recovers):
			// a stop was requested for this run and it ended with an error:
			// a node failed while draining, the source returned StopAll's
			// non-graceful reason, or it was force-stopped. Never recover a
			// run someone asked to stop; that would restart a pipeline the
			// user stopped, or start one during shutdown after Wait stopped
			// looking. The run is stopped, with the error recorded.
			if updateErr := s.finishStopped(ctx, rp, systemStop, err); updateErr != nil {
				return updateErr
			}
			stoppedWithErr = true
		default:
			// try to recover the pipeline
			recoveryErr := s.recoverPipeline(ctx, rp)
			switch {
			case recoveryErr == nil:
				// recovery was triggered didn't error, so no cleanup
				// this is why we return nil to skip the cleanup below.
				return nil
			case cerrors.Is(recoveryErr, errRecoveryAborted):
				// A stop was requested, or shutdown began, while the
				// recovery was pending (#2901). Same outcome as the arm
				// above, with the error the run failed with. A shutdown
				// that ended the wait before StopAll reached this run
				// counts as a system stop.
				fired, system, _ := rp.stop.state()
				if !fired {
					system = true
				}
				if updateErr := s.finishStopped(ctx, rp, system, err); updateErr != nil {
					return updateErr
				}
				stoppedWithErr = true
			default:
				s.logger.
					Err(ctx, err).
					Str(log.PipelineIDField, rp.pipeline.ID).
					Msg("pipeline recovery failed")

				if updateErr := s.pipelines.UpdateStatus(ctx, rp.pipeline.ID, pipeline.StatusDegraded, fmt.Sprintf("%+v", recoveryErr)); updateErr != nil {
					return updateErr
				}

				// we assign it to err so it's returned and notified by the cleanup function
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

	// confirmed that all nodes stopped, we can now remove the pipeline
	// from the running pipelines — but only if the entry under this ID
	// is still THIS run (#2806). This goroutine can itself be the one
	// running synchronously inside an OLDER run's cleanup: recoverPipeline
	// -> StartWithBackoff -> Start runs a nested runPipeline on the
	// calling tomb, not a fresh goroutine. If that nested Start fails
	// after publishing (it no longer fails on a status write, #2898, but
	// a failure after publication must still be safe here), the error
	// propagates back into the OUTER run's cleanup, which falls through
	// to this same terminal block. A blind Delete(rp.pipeline.ID) there
	// would delete the INNER run's freshly-published, still-alive entry —
	// orphaning it, unreachable via Stop/WaitPipeline, exactly the bug
	// class this fix closes. See deleteRunningPipelineIfCurrent.
	s.deleteRunningPipelineIfCurrent(rp.pipeline.ID, rp)

	if !stoppedWithErr {
		s.notify(rp.pipeline.ID, err)
	}
	return err
}

// finishStopped writes the terminal status of a run that was stopped but
// ended with err: SystemStopped for a shutdown, so the pipeline starts again
// on the next boot, UserStopped otherwise. The error is kept as the
// pipeline's error message, logged, and (by the caller) stored as the
// terminal error that WaitPipeline returns. It is not a failure, so the
// caller does not notify OnFailure handlers and exit-on-degraded does not
// trip (#2901).
func (s *Service) finishStopped(ctx context.Context, rp *runnablePipeline, system bool, err error) error {
	status := pipeline.StatusUserStopped
	if system {
		status = pipeline.StatusSystemStopped
	}
	s.logger.Warn(ctx).
		Err(err).
		Str(log.PipelineIDField, rp.pipeline.ID).
		Any(log.PipelineStatusField, status).
		Msg("pipeline stopped with an error after a stop was requested; not recovering")
	// we use %+v to get the stack trace too
	return s.pipelines.UpdateStatus(ctx, rp.pipeline.ID, status, fmt.Sprintf("%+v", err))
}

// logStopError logs a failed stop of a pipeline during shutdown. If a stop
// had already been requested for the run (StopAll and the self-stop at
// publication can both reach the same run, and so can a user Stop followed by
// StopAll), the error is the source refusing a second stop while the first
// one drains, which is expected: it is logged at debug, not as a warning.
func (s *Service) logStopError(ctx context.Context, alreadyStopping bool, err error, pipelineID, msg string) {
	e := s.logger.Warn(ctx)
	if alreadyStopping {
		e = s.logger.Debug(ctx)
		msg += " (a stop was already in progress)"
	}
	e.Err(err).Str(log.PipelineIDField, pipelineID).Msg(msg)
}

// publishRunningPipeline makes rp the live run for its pipeline ID (see the
// comment at its call site in runPipeline for why the timing matters). If
// shutdown has begun by then, it also stops rp, because StopAll may already
// have iterated runningPipelines without seeing it (#2901).
func (s *Service) publishRunningPipeline(rp *runnablePipeline) {
	if s.testBeforePublish != nil {
		s.testBeforePublish(rp)
	}

	s.publishMu.Lock()
	s.runningPipelines.Set(rp.pipeline.ID, rp)
	// Read the shutdown flag in the same critical section as the publication:
	// StopAll sets it under publishMu before it reads runningPipelines, so
	// either StopAll sees this run, or this run sees the flag (#2901).
	shuttingDown, shutdownReason := s.runs.shuttingDown()
	s.publishMu.Unlock()

	if shuttingDown {
		// Shutdown began after this run was admitted but before StopAll could
		// see it (e.g. a recovery restart that was building its nodes while
		// StopAll iterated). Invariant 7: stop it the same way StopAll would
		// have, so Wait's drain covers it. It goes on to report Running and
		// then SystemStopped like any other run StopAll stopped.
		// Detached context (#2912 N1): ctx is the caller's, e.g. an API
		// request, and its cancellation must not leave the run alive until
		// the exit timeout.
		alreadyStopping := rp.stop.requested()
		if err := s.stopGraceful(context.Background(), rp, shutdownReason, true); err != nil {
			s.logStopError(context.Background(), alreadyStopping, err, rp.pipeline.ID,
				"could not stop pipeline that started while shutting down")
		}
	}
}

// deleteRunningPipelineIfCurrent removes id's entry from runningPipelines
// only if it still holds exactly rp — a compare-and-delete rather than a
// delete-by-key (#2806). This is what stops a stale owner (an older run's
// cleanup goroutine) from erasing a newer run's
// published entry, which is the bug class #2806 fixes: a superseded run
// falling through to an unconditional Delete(id) and taking a live run down
// with it.
//
// csync.Map exposes no compare-and-swap primitive, so the read-compare-delete
// is made atomic the only way available: publishMu serializes it against the
// publication in runPipeline, which is the only other writer. Without that
// lock this is a genuine TOCTOU — a concurrent Set(id, newer) landing between
// the Get and the Delete makes a stale owner erase a live run — measured at 4
// occurrences in 200,000 races during review, and it is reachable without any
// recovery chain: an operator Stop leaves the status UserStopped, which admits
// a concurrent Start, whose Set can land inside a departing cleanup's window.
func (s *Service) deleteRunningPipelineIfCurrent(id string, rp *runnablePipeline) {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()

	if current, ok := s.runningPipelines.Get(id); ok && current == rp {
		s.runningPipelines.Delete(id)
	}
}

// recoverPipeline attempts to recover a pipeline that has stopped running.
func (s *Service) recoverPipeline(ctx context.Context, rp *runnablePipeline) error {
	s.logger.Trace(ctx).Str(log.PipelineIDField, rp.pipeline.ID).Msg("recovering pipeline")
	measure.PipelineRecoveringCount.WithValues(rp.pipeline.Config.Name).Inc()

	err := s.pipelines.UpdateStatus(ctx, rp.pipeline.ID, pipeline.StatusRecovering, "")
	if err != nil {
		return err
	}

	// Exit the goroutine and attempt to restart the pipeline
	return s.StartWithBackoff(ctx, rp)
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
