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
	"github.com/conduitio/conduit/pkg/lifecycle-poc/funnel"
	"github.com/conduitio/conduit/pkg/pipeline"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/processor"
	"gopkg.in/tomb.v2"
)

type FailureEvent struct {
	// ID is the ID of the pipeline which failed.
	ID    string
	Error error
}

type FailureHandler func(FailureEvent)

// Service manages pipelines.
type Service struct {
	logger log.CtxLogger

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
	connectors ConnectorService,
	processors ProcessorService,
	connectorPlugins ConnectorPluginService,
	pipelines PipelineService,
	metricsDisabled bool,
) *Service {
	return &Service{
		logger:           logger.WithComponent("lifecycle.Service"),
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
}

// ConnectorService can fetch and create a connector instance.
type ConnectorService interface {
	Get(ctx context.Context, id string) (*connector.Instance, error)
	Create(ctx context.Context, id string, t connector.Type, plugin string, pipelineID string, cfg connector.Config, p connector.ProvisionType) (*connector.Instance, error)
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
		return rp.w.Stop(ctx)
	case true:
		s.logger.Info(ctx).
			Str(log.PipelineIDField, rp.pipeline.ID).
			Any(log.PipelineStatusField, rp.pipeline.GetStatus()).
			Msg("force stopping pipeline")
		rp.t.Kill(pipeline.ErrForceStop)
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

// StopAndWait exists to satisfy the LifecycleService interfaces shared with
// the sibling pkg/lifecycle package (see provisioning.LifecycleService and
// conduit.Runtime's lifecycleService) but is intentionally unimplemented
// here: it always refuses with a CodeStopAndWaitUnsupported error.
//
// This is a deliberate Tier-1 safety guard, not a TODO. The Stop-and-restart
// primitive backing provisioning.Service.ApplyPlanLive (stop-drain-restart on
// the data path) requires a proven quiescence+durability guarantee — see
// pkg/lifecycle.Service.StopAndWait's doc. That guarantee was established by
// auditing pkg/lifecycle's specific Stop/WaitPipeline/Persister interaction
// (docs/design-documents/20260708-live-server-deploy-apply.md, "Review
// outcome & required rework"). This package's funnel.Worker-based Stop has a
// different implementation and has not had the equivalent audit. Silently
// building StopAndWait on top of an unaudited stop path here would let
// ApplyPlanLive apply-to-running under Preview.PipelineArchV2 without the
// safety case the design review actually verified — exactly the class of bug
// blocker 1 found in the original (pre-rework) design. Refusing outright, so
// ApplyPlanLive's error surfaces immediately instead of silently risking
// data loss, is intentional until this architecture's drain semantics get
// the same audit (tracked as the design doc's "Open parity item").
func (s *Service) StopAndWait(context.Context, string) error {
	ce := conduiterr.New(CodeStopAndWaitUnsupported,
		"live apply (stop-drain-restart of a running pipeline) is not supported under the "+
			"experimental Preview.PipelineArchV2 lifecycle service")
	ce.Suggestion = "disable Preview.PipelineArchV2, or stop the pipeline manually before applying changes"
	return ce
}

// ReconfigureProcessor refuses under the experimental Preview.PipelineArchV2
// lifecycle service, mirroring StopAndWait: live in-place apply is not supported
// under this arch. Returning the same coded error means a live apply is cleanly
// refused rather than silently downgraded.
func (s *Service) ReconfigureProcessor(context.Context, string, string) error {
	ce := conduiterr.New(CodeStopAndWaitUnsupported,
		"live in-place apply (processor reconfigure on a running pipeline) is not supported under "+
			"the experimental Preview.PipelineArchV2 lifecycle service")
	ce.Suggestion = "disable Preview.PipelineArchV2, or stop the pipeline manually before applying changes"
	return ce
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
				s.newProcessorMetrics(pl.Config.Name, instance.Plugin),
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
			if cerrors.IsFatalError(err) {
				// we use %+v to get the stack trace too
				if err := s.pipelines.UpdateStatus(ctx, rp.pipeline.ID, pipeline.StatusDegraded, fmt.Sprintf("%+v", err)); err != nil {
					return err
				}
			} else { //nolint:staticcheck // TODO: implement recovery
				// // try to recover the pipeline
				// if recoveryErr := s.recoverPipeline(ctx, rp); recoveryErr != nil {
				// 	s.logger.
				// 		Err(ctx, err).
				// 		Str(log.PipelineIDField, rp.pipeline.ID).
				// 		Msg("pipeline recovery failed")
				//
				// 	if updateErr := s.pipelines.UpdateStatus(ctx, rp.pipeline.ID, pipeline.StatusDegraded, fmt.Sprintf("%+v", recoveryErr)); updateErr != nil {
				// 		return updateErr
				// 	}
				//
				// 	// we assign it to err so it's returned and notified by the cleanup function
				// 	err = recoveryErr
				// } else {
				// 	// recovery was triggered didn't error, so no cleanup
				// 	// this is why we return nil to skip the cleanup below.
				// 	return nil
				// }
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
	)
}

func (s *Service) newProcessorMetrics(pipelineName, plugin string) funnel.ProcessorMetrics {
	if s.metricsDisabled {
		return &funnel.NoOpProcessorMetrics{}
	}

	return funnel.NewProcessorMetrics(pipelineName, plugin)
}

func (s *Service) newDLQMetrics(pipelineName string, plugin string) funnel.ConnectorMetrics {
	if s.metricsDisabled {
		return &funnel.NoOpConnectorMetrics{}
	}

	return funnel.NewDLQMetrics(pipelineName, plugin)
}
