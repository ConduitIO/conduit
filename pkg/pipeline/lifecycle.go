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

package pipeline

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
	"github.com/conduitio/conduit/pkg/pipeline/stream"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/processor"
	"gopkg.in/tomb.v2"
)

// ConnectorFetcher can fetch a connector instance.
type ConnectorFetcher interface {
	Get(ctx context.Context, id string) (*connector.Instance, error)
	Create(ctx context.Context, id string, t connector.Type, plugin string, pipelineID string, cfg connector.Config, p connector.ProvisionType) (*connector.Instance, error)
}

// ProcessorService can fetch a processor instance and make a runnable processor from it.
type ProcessorService interface {
	Get(ctx context.Context, id string) (*processor.Instance, error)
	MakeRunnableProcessor(ctx context.Context, i *processor.Instance) (*processor.RunnableProcessor, error)
}

// PluginDispenserFetcher can fetch a plugin.
type PluginDispenserFetcher interface {
	NewDispenser(logger log.CtxLogger, name string, connectorID string) (connectorPlugin.Dispenser, error)
}

// Run runs pipelines that had the running state in store.
func (s *Service) Run(
	ctx context.Context,
	connFetcher ConnectorFetcher,
	procService ProcessorService,
	pluginFetcher PluginDispenserFetcher,
) error {
	var errs []error
	s.logger.Debug(ctx).Msg("initializing pipelines statuses")

	// run pipelines that are in the StatusSystemStopped state
	for _, instance := range s.instances {
		if instance.GetStatus() == StatusSystemStopped {
			err := s.Start(ctx, connFetcher, procService, pluginFetcher, instance.ID)
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
	connFetcher ConnectorFetcher,
	procService ProcessorService,
	pluginFetcher PluginDispenserFetcher,
	pipelineID string,
) error {
	pl, err := s.Get(ctx, pipelineID)
	if err != nil {
		return err
	}
	if pl.GetStatus() == StatusRunning {
		return cerrors.Errorf("can't start pipeline %s: %w", pl.ID, ErrPipelineRunning)
	}

	s.logger.Debug(ctx).Str(log.PipelineIDField, pl.ID).Msg("starting pipeline")

	s.logger.Trace(ctx).Str(log.PipelineIDField, pl.ID).Msg("building nodes")
	nodes, err := s.buildNodes(ctx, connFetcher, procService, pluginFetcher, pl)
	if err != nil {
		return cerrors.Errorf("could not build nodes for pipeline %s: %w", pl.ID, err)
	}

	pl.n = make(map[string]stream.Node)
	for _, node := range nodes {
		pl.n[node.ID()] = node
	}

	s.logger.Trace(ctx).Str(log.PipelineIDField, pl.ID).Msg("running nodes")
	if err := s.runPipeline(ctx, pl); err != nil {
		return cerrors.Errorf("failed to run pipeline %s: %w", pl.ID, err)
	}
	s.logger.Info(ctx).Str(log.PipelineIDField, pl.ID).Msg("pipeline started")

	return nil
}

// Stop will attempt to gracefully stop a given pipeline by calling each node's
// Stop function. If force is set to true the pipeline won't stop gracefully,
// instead the context for all nodes will be canceled which causes them to stop
// running as soon as possible.
func (s *Service) Stop(ctx context.Context, pipelineID string, force bool) error {
	pl, err := s.Get(ctx, pipelineID)
	if err != nil {
		return err
	}

	if pl.GetStatus() != StatusRunning && pl.GetStatus() != StatusRecovering {
		return cerrors.Errorf("can't stop pipeline with status %q: %w", pl.GetStatus(), ErrPipelineNotRunning)
	}

	switch force {
	case false:
		return s.stopGraceful(ctx, pl, nil)
	case true:
		return s.stopForceful(ctx, pl)
	}
	panic("unreachable code")
}

func (s *Service) stopGraceful(ctx context.Context, pl *Instance, reason error) error {
	s.logger.Info(ctx).
		Str(log.PipelineIDField, pl.ID).
		Any(log.PipelineStatusField, pl.GetStatus()).
		Msg("gracefully stopping pipeline")
	var errs []error
	for _, n := range pl.n {
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
	return cerrors.Join(errs...)
}

func (s *Service) stopForceful(ctx context.Context, pl *Instance) error {
	s.logger.Info(ctx).
		Str(log.PipelineIDField, pl.ID).
		Any(log.PipelineStatusField, pl.GetStatus()).
		Msg("force stopping pipeline")
	pl.t.Kill(ErrForceStop)
	for _, n := range pl.n {
		if node, ok := n.(stream.ForceStoppableNode); ok {
			// stop all pub nodes
			s.logger.Trace(ctx).Str(log.NodeIDField, n.ID()).Msg("force stopping node")
			node.ForceStop(ctx)
		}
	}
	return nil
}

// StopAll will ask all the pipelines to stop gracefully
// (i.e. that existing messages get processed but not new messages get produced).
func (s *Service) StopAll(ctx context.Context, reason error) {
	for _, pl := range s.instances {
		if pl.GetStatus() != StatusRunning && pl.GetStatus() != StatusRecovering {
			continue
		}
		err := s.stopGraceful(ctx, pl, reason)
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
		return ErrTimeout
	}
}

// waitInternal blocks until all pipelines are stopped and returns an error if any of
// the pipelines failed to stop gracefully.
func (s *Service) waitInternal() error {
	var errs []error
	for _, pl := range s.instances {
		err := pl.Wait()
		if err != nil {
			errs = append(errs, err)
		}
	}
	return cerrors.Join(errs...)
}

// buildsNodes will build and connect all nodes configured in the pipeline.
func (s *Service) buildNodes(
	ctx context.Context,
	connFetcher ConnectorFetcher,
	procService ProcessorService,
	pluginFetcher PluginDispenserFetcher,
	pl *Instance,
) ([]stream.Node, error) {
	// setup many to many channels
	fanIn := stream.FaninNode{Name: "fanin"}
	fanOut := stream.FanoutNode{Name: "fanout"}

	sourceNodes, err := s.buildSourceNodes(ctx, connFetcher, procService, pluginFetcher, pl, &fanIn)
	if err != nil {
		return nil, cerrors.Errorf("could not build source nodes: %w", err)
	}
	if len(sourceNodes) == 0 {
		return nil, cerrors.New("can't build pipeline without any source connectors")
	}

	processorNodes, err := s.buildProcessorNodes(ctx, procService, pl, pl.ProcessorIDs, &fanIn, &fanOut)
	if err != nil {
		return nil, cerrors.Errorf("could not build processor nodes: %w", err)
	}

	destinationNodes, err := s.buildDestinationNodes(ctx, connFetcher, procService, pluginFetcher, pl, &fanOut)
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

func (s *Service) buildProcessorNodes(
	ctx context.Context,
	procService ProcessorService,
	pl *Instance,
	processorIDs []string,
	first stream.PubNode,
	last stream.SubNode,
) ([]stream.Node, error) {
	var nodes []stream.Node

	prev := first
	for _, procID := range processorIDs {
		instance, err := procService.Get(ctx, procID)
		if err != nil {
			return nil, cerrors.Errorf("could not fetch processor: %w", err)
		}

		runnableProc, err := procService.MakeRunnableProcessor(ctx, instance)
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
	pl *Instance,
	proc *processor.RunnableProcessor,
) *stream.ParallelNode {
	return &stream.ParallelNode{
		Name: proc.ID + "-parallel",
		NewNode: func(i uint64) stream.PubSubNode {
			n := s.buildProcessorNode(pl, proc)
			n.Name = n.Name + "-" + strconv.FormatUint(i, 10) // add suffix to name
			return n
		},
		Workers: proc.Config.Workers,
	}
}

func (s *Service) buildProcessorNode(
	pl *Instance,
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
	connFetcher ConnectorFetcher,
	procService ProcessorService,
	pluginFetcher PluginDispenserFetcher,
	pl *Instance,
	next stream.SubNode,
) ([]stream.Node, error) {
	var nodes []stream.Node

	dlqHandlerNode, err := s.buildDLQHandlerNode(ctx, connFetcher, pluginFetcher, pl)
	if err != nil {
		return nil, err
	}

	for _, connID := range pl.ConnectorIDs {
		instance, err := connFetcher.Get(ctx, connID)
		if err != nil {
			return nil, cerrors.Errorf("could not fetch connector: %w", err)
		}

		if instance.Type != connector.TypeSource {
			continue // skip any connector that's not a source
		}

		src, err := instance.Connector(ctx, pluginFetcher)
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

		procNodes, err := s.buildProcessorNodes(ctx, procService, pl, instance.ProcessorIDs, metricsNode, next)
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
	connFetcher ConnectorFetcher,
	pluginFetcher PluginDispenserFetcher,
	pl *Instance,
) (*stream.DLQHandlerNode, error) {
	conn, err := connFetcher.Create(
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

	dest, err := conn.Connector(ctx, pluginFetcher)
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
	pl *Instance,
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
	connFetcher ConnectorFetcher,
	procService ProcessorService,
	pluginFetcher PluginDispenserFetcher,
	pl *Instance,
	prev stream.PubNode,
) ([]stream.Node, error) {
	var nodes []stream.Node

	for _, connID := range pl.ConnectorIDs {
		instance, err := connFetcher.Get(ctx, connID)
		if err != nil {
			return nil, cerrors.Errorf("could not fetch connector: %w", err)
		}

		if instance.Type != connector.TypeDestination {
			continue // skip any connector that's not a destination
		}

		dest, err := instance.Connector(ctx, pluginFetcher)
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

		connNodes, err := s.buildProcessorNodes(ctx, procService, pl, instance.ProcessorIDs, prev, metricsNode)
		if err != nil {
			return nil, cerrors.Errorf("could not build processor nodes for connector %s: %w", instance.ID, err)
		}

		nodes = append(nodes, connNodes...)
		nodes = append(nodes, metricsNode, &destinationNode, ackerNode)
	}

	return nodes, nil
}

func (s *Service) runPipeline(ctx context.Context, pl *Instance) error {
	if pl.t != nil && pl.t.Alive() {
		return ErrPipelineRunning
	}

	// the tomb is responsible for running goroutines related to the pipeline
	pl.t = &tomb.Tomb{}

	// keep tomb alive until the end of this function, this way we guarantee we
	// can run the cleanup goroutine even if all nodes stop before we get to it
	keepAlive := make(chan struct{})
	pl.t.Go(func() error {
		<-keepAlive
		return nil
	})
	defer close(keepAlive)

	// nodesWg is done once all nodes stop running
	var nodesWg sync.WaitGroup
	var isGracefulShutdown atomic.Bool
	for id := range pl.n {
		nodesWg.Add(1)
		node := pl.n[id]

		pl.t.Go(func() (errOut error) {
			// If any of the nodes stop, the tomb will be put into a dying state
			// and ctx will be cancelled.
			// This way, the other nodes will be notified that they need to stop too.
			//nolint:staticcheck // nil used to use the default (parent provided via WithContext)
			ctx := pl.t.Context(nil)
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
			if cerrors.Is(err, ErrGracefulShutdown) {
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

	measure.PipelinesGauge.WithValues(strings.ToLower(pl.GetStatus().String())).Dec()
	pl.SetStatus(StatusRunning)
	pl.Error = ""
	measure.PipelinesGauge.WithValues(strings.ToLower(pl.GetStatus().String())).Inc()

	err := s.store.Set(ctx, pl.ID, pl)
	if err != nil {
		return cerrors.Errorf("pipeline not updated: %w", err)
	}

	// cleanup function updates the metrics and pipeline status once all nodes
	// stop running
	pl.t.Go(func() error {
		// use fresh context for cleanup function, otherwise the updated status
		// won't be stored
		ctx := context.Background()

		nodesWg.Wait()
		err := pl.t.Err()

		measure.PipelinesGauge.WithValues(strings.ToLower(pl.GetStatus().String())).Dec()

		switch err {
		case tomb.ErrStillAlive:
			// not an actual error, the pipeline stopped gracefully
			err = nil
			if isGracefulShutdown.Load() {
				// it was triggered by a graceful shutdown of Conduit
				pl.SetStatus(StatusSystemStopped)
			} else {
				// it was manually triggered by a user
				pl.SetStatus(StatusUserStopped)
			}
		default:
			pl.SetStatus(StatusDegraded)
			// we use %+v to get the stack trace too
			pl.Error = fmt.Sprintf("%+v", err)
		}

		s.logger.
			Err(ctx, err).
			Str(log.PipelineIDField, pl.ID).
			Msg("pipeline stopped")

		s.notify(pl.ID, err)
		// It's important to update the metrics before we handle the error from s.Store.Set() (if any),
		// since the source of the truth is the actual pipeline (stored in memory).
		measure.PipelinesGauge.WithValues(strings.ToLower(pl.GetStatus().String())).Inc()

		storeErr := s.store.Set(ctx, pl.ID, pl)
		if storeErr != nil {
			return cerrors.Errorf("pipeline not updated: %w", storeErr)
		}

		return err
	})

	return nil
}
