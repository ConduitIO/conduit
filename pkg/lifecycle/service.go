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

// Package lifecycle wires up everything under the hood of a Conduit instance
// including metrics, telemetry, logging, and server construction.
// It should only ever interact with the Orchestrator, never individual
// services. All of that responsibility should be left to the Orchestrator.
package lifecycle

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
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

type FailureEvent struct {
	// ID is the ID of the pipeline which failed.
	ID    string
	Error error
}

type FailureHandler func(FailureEvent)

// Service manages pipelines.
type Service struct {
	logger log.CtxLogger

	backoffCfg *backoff.Backoff

	pipelines  PipelineService
	connectors ConnectorService

	processors       ProcessorService
	connectorPlugins ConnectorPluginService

	handlers         []FailureHandler
	runningPipelines map[string]*runnablePipeline
}

// NewService initializes and returns a pipeline Service.
func NewService(
	logger log.CtxLogger,
	backoffCfg *backoff.Backoff,
	connectors ConnectorService,
	processors ProcessorService,
	connectorPlugins ConnectorPluginService,
	pipelines PipelineService,
) *Service {
	return &Service{
		logger:           logger.WithComponent("pipeline.Service"),
		backoffCfg:       backoffCfg,
		connectors:       connectors,
		processors:       processors,
		connectorPlugins: connectorPlugins,
		pipelines:        pipelines,
	}
}

type runnablePipeline struct {
	pipeline *pipeline.Instance
	n        []stream.Node
	t        *tomb.Tomb
}

// ConnectorService can fetch a connector instance.
type ConnectorService interface {
	Get(ctx context.Context, id string) (*connector.Instance, error)
	Create(ctx context.Context, id string, t connector.Type, plugin string, pipelineID string, cfg connector.Config, p connector.ProvisionType) (*connector.Instance, error)
}

// ProcessorService can fetch a processor instance and make a runnable processor from it.
type ProcessorService interface {
	Get(ctx context.Context, id string) (*processor.Instance, error)
	MakeRunnableProcessor(ctx context.Context, i *processor.Instance) (*processor.RunnableProcessor, error)
}

// ConnectorPluginService can fetch a plugin.
type ConnectorPluginService interface {
	NewDispenser(logger log.CtxLogger, name string, connectorID string) (connectorPlugin.Dispenser, error)
}

// PipelineService can fetch a pipeline instance.
type PipelineService interface {
	Get(ctx context.Context, pipelineID string) (*pipeline.Instance, error)
	GetInstances() map[string]*pipeline.Instance
	UpdateStatus(ctx context.Context, pipelineID string, status pipeline.Status, errMsg string) error
}

// OnFailure registers a handler for a pipeline.FailureEvent.
// Only errors which happen after a pipeline has been started
// are being sent.
func (s *Service) OnFailure(handler FailureHandler) {
	s.handlers = append(s.handlers, handler)
}

// Run runs pipelines that had the running state in store.
func (s *Service) Run(
	ctx context.Context,
) error {
	var errs []error
	s.logger.Debug(ctx).Msg("initializing pipelines statuses")

	// run pipelines that are in the StatusSystemStopped state
	instances := s.pipelines.GetInstances()
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

	s.logger.Trace(ctx).Str(log.PipelineIDField, pl.ID).Msg("building nodes")

	rp, err := s.buildRunnablePipeline(ctx, pl)
	if err != nil {
		return cerrors.Errorf("could not build nodes for pipeline %s: %w", pl.ID, err)
	}

	s.logger.Trace(ctx).Str(log.PipelineIDField, pl.ID).Msg("running nodes")
	if err := s.runPipeline(ctx, rp); err != nil {
		return cerrors.Errorf("failed to run pipeline %s: %w", pl.ID, err)
	}
	s.logger.Info(ctx).Str(log.PipelineIDField, pl.ID).Msg("pipeline started")
	s.runningPipelines[pl.ID] = rp

	return nil
}

// Stop will attempt to gracefully stop a given pipeline by calling each node's
// Stop function. If force is set to true the pipeline won't stop gracefully,
// instead the context for all nodes will be canceled which causes them to stop
// running as soon as possible.
func (s *Service) Stop(ctx context.Context, pipelineID string, force bool) error {
	rp, ok := s.runningPipelines[pipelineID]
	if !ok {
		return cerrors.Errorf("pipeline %s is not running: %w", pipelineID, pipeline.ErrPipelineNotRunning)
	}

	if rp.pipeline.GetStatus() != pipeline.StatusRunning && rp.pipeline.GetStatus() != pipeline.StatusRecovering {
		return cerrors.Errorf("can't stop pipeline with status %q: %w", rp.pipeline.GetStatus(), pipeline.ErrPipelineNotRunning)
	}

	switch force {
	case false:
		return s.stopGraceful(ctx, rp, nil)
	case true:
		return s.stopForceful(ctx, rp)
	}
	panic("unreachable code")
}

func (s *Service) stopGraceful(ctx context.Context, rp *runnablePipeline, reason error) error {
	s.logger.Info(ctx).
		Str(log.PipelineIDField, rp.pipeline.ID).
		Any(log.PipelineStatusField, rp.pipeline.GetStatus()).
		Msg("gracefully stopping pipeline")
	var errs []error
	for _, n := range rp.n {
		if node, ok := n.(stream.StoppableNode); ok {
			// stop all pub nodes
			s.logger.Trace(ctx).Str(log.NodeIDField, n.ID()).Msg("stopping node")
			err := node.Stop(ctx, reason)
			if err != nil {
				s.logger.Err(ctx, err).Str(log.NodeIDField, n.ID()).Msg("stop failed")
				errs = append(errs, err)
			}
		}
	}

	if len(errs) == 0 {
		s.runningPipelines[rp.pipeline.ID] = nil
		return nil
	}

	return cerrors.Join(errs...)
}

func (s *Service) stopForceful(ctx context.Context, rp *runnablePipeline) error {
	s.logger.Info(ctx).
		Str(log.PipelineIDField, rp.pipeline.ID).
		Any(log.PipelineStatusField, rp.pipeline.GetStatus()).
		Msg("force stopping pipeline")
	rp.t.Kill(pipeline.ErrForceStop)
	for _, n := range rp.n {
		if node, ok := n.(stream.ForceStoppableNode); ok {
			// stop all pub nodes
			s.logger.Trace(ctx).Str(log.NodeIDField, n.ID()).Msg("force stopping node")
			node.ForceStop(ctx)
		}
	}

	s.runningPipelines[rp.pipeline.ID] = nil
	return nil
}

// StopAll will ask all the pipelines to stop gracefully
// (i.e. that existing messages get processed but not new messages get produced).
func (s *Service) StopAll(ctx context.Context, reason error) {
	instances := s.pipelines.GetInstances()
	for _, pl := range instances {
		rp, ok := s.runningPipelines[pl.ID]
		if !ok || (pl.GetStatus() != pipeline.StatusRunning && pl.GetStatus() != pipeline.StatusRecovering) {
			continue
		}
		err := s.stopGraceful(ctx, rp, reason)
		if err != nil {
			s.logger.Warn(ctx).
				Err(err).
				Str(log.PipelineIDField, pl.ID).
				Msg("could not stop pipeline")
		}
	}
	// TODO stop pipelines forcefully after timeout if they are still running
}

// Wait blocks until all pipelines are stopped or until the timeout is reached.
// Returns:
//
// (1) nil if all the pipelines are gracefully stopped,
//
// (2) an error, if the pipelines could not have been gracefully stopped,
//
// (3) ErrTimeout if the pipelines were not stopped within the given timeout.
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
		return pipeline.ErrTimeout
	}
}

// waitInternal blocks until all pipelines are stopped and returns an error if any of
// the pipelines failed to stop gracefully.
func (s *Service) waitInternal() error {
	var errs []error
	for _, rp := range s.runningPipelines {
		if rp.t == nil {
			continue
		}
		err := rp.t.Wait()
		if err != nil {
			errs = append(errs, err)
		}
	}
	return cerrors.Join(errs...)
}

// buildRunnablePipeline will build and connect all nodes configured in the pipeline.
func (s *Service) buildRunnablePipeline(
	ctx context.Context,
	pl *pipeline.Instance,
) (*runnablePipeline, error) {
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

	return &runnablePipeline{
		pipeline: pl,
		n:        nodes,
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
		ProcessorTimer: measure.ProcessorExecutionDurationTimer.WithValues(pl.Config.Name, proc.Plugin),
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
	// TODO: Handle the tomb outside and after maybe checking the status
	if rp.t != nil && rp.t.Alive() {
		return pipeline.ErrPipelineRunning
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
				return cerrors.Errorf("node %s stopped with error: %w", node.ID(), err)
			}
			return nil
		})
	}

	err := s.pipelines.UpdateStatus(ctx, rp.pipeline.ID, pipeline.StatusRunning, "")
	if err != nil {
		return err
	}

	// cleanup function updates the metrics and pipeline status once all nodes
	// stop running
	rp.t.Go(func() error {
		// use fresh context for cleanup function, otherwise the updated status
		// won't be stored
		ctx := context.Background()

		nodesWg.Wait()
		err := rp.t.Err()

		measure.PipelinesGauge.WithValues(strings.ToLower(rp.pipeline.GetStatus().String())).Dec()

		switch err {
		case tomb.ErrStillAlive:
			// not an actual error, the pipeline stopped gracefully
			err = nil
			if isGracefulShutdown.Load() {
				// it was triggered by a graceful shutdown of Conduit
				err = s.pipelines.UpdateStatus(ctx, rp.pipeline.ID, pipeline.StatusSystemStopped, "")
			} else {
				// it was manually triggered by a user
				err = s.pipelines.UpdateStatus(ctx, rp.pipeline.ID, pipeline.StatusUserStopped, "")
			}
			if err != nil {
				return err
			}
		default:
			if cerrors.IsFatalError(err) {
				// we use %+v to get the stack trace too
				err = s.pipelines.UpdateStatus(ctx, rp.pipeline.ID, pipeline.StatusDegraded, fmt.Sprintf("%+v", err))
				if err != nil {
					return err
				}
			} else {
				err = s.pipelines.UpdateStatus(ctx, rp.pipeline.ID, pipeline.StatusRecovering, "")
				if err != nil {
					return err
				}
			}
		}

		s.logger.
			Err(ctx, err).
			Str(log.PipelineIDField, rp.pipeline.ID).
			Msg("pipeline stopped")

		s.notify(rp.pipeline.ID, err)
		// It's important to update the metrics before we handle the error from s.Store.Set() (if any),
		// since the source of the truth is the actual pipeline (stored in memory).
		measure.PipelinesGauge.WithValues(strings.ToLower(rp.pipeline.GetStatus().String())).Inc()
		return s.pipelines.UpdateStatus(ctx, rp.pipeline.ID, rp.pipeline.GetStatus(), "")
	})
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
