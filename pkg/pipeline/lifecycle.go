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
	"strings"
	"sync"
	"time"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/measure"
	"github.com/conduitio/conduit/pkg/foundation/multierror"
	"github.com/conduitio/conduit/pkg/pipeline/stream"
	"github.com/conduitio/conduit/pkg/processor"
	"gopkg.in/tomb.v2"
)

// ConnectorFetcher can fetch a connector instance.
type ConnectorFetcher interface {
	Get(ctx context.Context, id string) (connector.Connector, error)
}

// ProcessorFetcher can fetch a processor instance.
type ProcessorFetcher interface {
	Get(ctx context.Context, id string) (*processor.Instance, error)
}

// Start builds and starts a pipeline instance.
func (s *Service) Start(
	ctx context.Context,
	connFetcher ConnectorFetcher,
	procFetcher ProcessorFetcher,
	pipelineID string,
) error {
	pl, err := s.Get(ctx, pipelineID)
	if err != nil {
		return err
	}
	if pl.Status == StatusRunning {
		return cerrors.Errorf("can't start pipeline %s: %w", pl.ID, ErrPipelineRunning)
	}

	s.logger.Debug(ctx).Str(log.PipelineIDField, pl.ID).Msg("starting pipeline")

	s.logger.Trace(ctx).Str(log.PipelineIDField, pl.ID).Msg("building nodes")
	nodes, err := s.buildNodes(ctx, connFetcher, procFetcher, pl)
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
// Stop function.
func (s *Service) Stop(ctx context.Context, pipelineID string) error {
	pl, err := s.Get(ctx, pipelineID)
	if err != nil {
		return err
	}
	return s.stopWithReason(ctx, pl, nil)
}

func (s *Service) stopWithReason(ctx context.Context, pl *Instance, reason error) error {
	if pl.Status != StatusRunning {
		return cerrors.Errorf("can't stop pipeline with status %q: %w", pl.Status, ErrPipelineNotRunning)
	}

	s.logger.Debug(ctx).Str(log.PipelineIDField, pl.ID).Msg("stopping pipeline")
	var err error
	for _, n := range pl.n {
		if node, ok := n.(stream.StoppableNode); ok {
			// stop all pub nodes
			s.logger.Trace(ctx).Str(log.NodeIDField, n.ID()).Msg("stopping node")
			stopErr := node.Stop(ctx, reason)
			if stopErr != nil {
				s.logger.Err(ctx, stopErr).Str(log.NodeIDField, n.ID()).Msg("stop failed")
				err = multierror.Append(err, stopErr)
			}
		}
	}
	return err
}

// StopAll will ask all the pipelines to stop gracefully
// (i.e. that existing messages get processed but not new messages get produced).
func (s *Service) StopAll(ctx context.Context, reason error) {
	for _, pl := range s.instances {
		if pl.Status != StatusRunning {
			continue
		}
		err := s.stopWithReason(ctx, pl, reason)
		if err != nil {
			s.logger.Warn(ctx).
				Err(err).
				Str(log.PipelineIDField, pl.ID).
				Msg("could not stop pipeline")
		}
	}
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
	var err error
	for _, pl := range s.instances {
		plErr := pl.Wait()
		if plErr != nil {
			err = multierror.Append(err, plErr)
		}
	}
	return err
}

// buildsNodes will build and connect all nodes configured in the pipeline.
func (s *Service) buildNodes(
	ctx context.Context,
	connFetcher ConnectorFetcher,
	procFetcher ProcessorFetcher,
	pl *Instance,
) ([]stream.Node, error) {
	// setup many to many channels
	fanIn := stream.FaninNode{Name: "fanin"}
	fanOut := stream.FanoutNode{Name: "fanout"}

	sourceNodes, err := s.buildSourceNodes(ctx, connFetcher, procFetcher, pl, &fanIn)
	if err != nil {
		return nil, cerrors.Errorf("could not build source nodes: %w", err)
	}
	if len(sourceNodes) == 0 {
		return nil, cerrors.New("can't build pipeline without any source connectors")
	}

	processorNodes, err := s.buildProcessorNodes(ctx, procFetcher, pl, pl.ProcessorIDs, &fanIn, &fanOut)
	if err != nil {
		return nil, cerrors.Errorf("could not build processor nodes: %w", err)
	}

	destinationNodes, err := s.buildDestinationNodes(ctx, connFetcher, procFetcher, pl, &fanOut)
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
	procFetcher ProcessorFetcher,
	pl *Instance,
	processorIDs []string,
	first stream.PubNode,
	last stream.SubNode,
) ([]stream.Node, error) {
	var nodes []stream.Node

	prev := first
	for _, procID := range processorIDs {
		proc, err := procFetcher.Get(ctx, procID)
		if err != nil {
			return nil, cerrors.Errorf("could not fetch processor: %w", err)
		}

		node := stream.ProcessorNode{
			Name:           proc.ID,
			Processor:      proc.Processor,
			ProcessorTimer: measure.ProcessorExecutionDurationTimer.WithValues(pl.Config.Name, proc.Type),
		}
		node.Sub(prev.Pub())
		prev = &node

		nodes = append(nodes, &node)
	}

	last.Sub(prev.Pub())
	return nodes, nil
}

func (s *Service) buildSourceAckerNode(
	src connector.Source,
) *stream.SourceAckerNode {
	return &stream.SourceAckerNode{
		Name:   src.ID() + "-acker",
		Source: src,
	}
}

func (s *Service) buildSourceNodes(
	ctx context.Context,
	connFetcher ConnectorFetcher,
	procFetcher ProcessorFetcher,
	pl *Instance,
	next stream.SubNode,
) ([]stream.Node, error) {
	var nodes []stream.Node

	for _, connID := range pl.ConnectorIDs {
		instance, err := connFetcher.Get(ctx, connID)
		if err != nil {
			return nil, cerrors.Errorf("could not fetch connector: %w", err)
		}

		if instance.Type() != connector.TypeSource {
			continue // skip any connector that's not a source
		}

		sourceNode := stream.SourceNode{
			Name:   instance.ID(),
			Source: instance.(connector.Source),
			PipelineTimer: measure.PipelineExecutionDurationTimer.WithValues(
				pl.Config.Name,
			),
		}
		ackerNode := s.buildSourceAckerNode(instance.(connector.Source))
		ackerNode.Sub(sourceNode.Pub())
		metricsNode := s.buildMetricsNode(pl, instance)
		metricsNode.Sub(ackerNode.Pub())

		procNodes, err := s.buildProcessorNodes(ctx, procFetcher, pl, instance.Config().ProcessorIDs, metricsNode, next)
		if err != nil {
			return nil, cerrors.Errorf("could not build processor nodes for connector %s: %w", instance.ID(), err)
		}

		nodes = append(nodes, &sourceNode, ackerNode, metricsNode)
		nodes = append(nodes, procNodes...)
	}

	return nodes, nil
}

func (s *Service) buildMetricsNode(
	pl *Instance,
	conn connector.Connector,
) *stream.MetricsNode {
	return &stream.MetricsNode{
		Name: conn.ID() + "-metrics",
		BytesHistogram: measure.ConnectorBytesHistogram.WithValues(
			pl.Config.Name,
			conn.Config().Plugin,
			strings.ToLower(conn.Type().String()),
		),
	}
}

func (s *Service) buildDestinationAckerNode(
	dest connector.Destination,
) *stream.DestinationAckerNode {
	return &stream.DestinationAckerNode{
		Name:        dest.ID() + "-acker",
		Destination: dest,
	}
}

func (s *Service) buildDestinationNodes(
	ctx context.Context,
	connFetcher ConnectorFetcher,
	procFetcher ProcessorFetcher,
	pl *Instance,
	prev stream.PubNode,
) ([]stream.Node, error) {
	var nodes []stream.Node

	for _, connID := range pl.ConnectorIDs {
		instance, err := connFetcher.Get(ctx, connID)
		if err != nil {
			return nil, cerrors.Errorf("could not fetch connector: %w", err)
		}

		if instance.Type() != connector.TypeDestination {
			continue // skip any connector that's not a destination
		}

		ackerNode := s.buildDestinationAckerNode(instance.(connector.Destination))
		destinationNode := stream.DestinationNode{
			Name:        instance.ID(),
			Destination: instance.(connector.Destination),
			ConnectorTimer: measure.ConnectorExecutionDurationTimer.WithValues(
				pl.Config.Name,
				instance.Config().Plugin,
				strings.ToLower(instance.Type().String()),
			),
		}
		metricsNode := s.buildMetricsNode(pl, instance)
		destinationNode.Sub(metricsNode.Pub())
		ackerNode.Sub(destinationNode.Pub())

		connNodes, err := s.buildProcessorNodes(ctx, procFetcher, pl, instance.Config().ProcessorIDs, prev, metricsNode)
		if err != nil {
			return nil, cerrors.Errorf("could not build processor nodes for connector %s: %w", instance.ID(), err)
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

	// nodesTomb is responsible for running nodes only
	nodesTomb := &tomb.Tomb{}
	var nodesWg sync.WaitGroup
	for id := range pl.n {
		nodesWg.Add(1)
		node := pl.n[id]

		nodesTomb.Go(func() (errOut error) {
			// If any of the nodes stops, the nodesTomb will be put into a dying state
			// and ctx will be cancelled.
			// This way, the other nodes will be notified that they need to stop too.
			//nolint: staticcheck // nil used to use the default (parent provided via WithContext)
			ctx := nodesTomb.Context(nil)
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
				// other nodes stop gracefully. The tomb should still return the
				// error to signal the reason to the cleanup function, that's
				// why we start another goroutine that will take care of
				// returning the error when all nodes are stopped.
				nodesTomb.Go(func() error {
					nodesWg.Wait()
					return err
				})
				return nil
			}
			if err != nil {
				return cerrors.Errorf("node %s stopped with error: %w", node.ID(), err)
			}
			return nil
		})
	}

	measure.PipelinesGauge.WithValues(strings.ToLower(pl.Status.String())).Dec()
	pl.Status = StatusRunning
	pl.Error = ""
	measure.PipelinesGauge.WithValues(strings.ToLower(pl.Status.String())).Inc()

	err := s.store.Set(ctx, pl.ID, pl)
	if err != nil {
		return cerrors.Errorf("pipeline not updated: %w", err)
	}

	// We're using different tombs for the pipeline and the nodes,
	// since we need to be sure that all nodes are stopped,
	// before declaring the pipeline as stopped.
	pl.t = &tomb.Tomb{}
	pl.t.Go(func() error {
		//nolint: staticcheck // nil used to use the default (parent provided via WithContext)
		ctx := pl.t.Context(nil)
		err := nodesTomb.Wait()

		measure.PipelinesGauge.WithValues(strings.ToLower(pl.Status.String())).Dec()

		switch {
		case cerrors.Is(err, ErrGracefulShutdown):
			// not an actual error, it was a graceful shutdown, that is expected
			err = nil
			pl.Status = StatusSystemStopped
		case err != nil:
			pl.Status = StatusDegraded
			// we use %+v to get the stack trace too
			pl.Error = fmt.Sprintf("%+v", err)
		default:
			pl.Status = StatusUserStopped
		}

		s.logger.
			Err(ctx, err).
			Str(log.PipelineIDField, pl.ID).
			Msg("pipeline stopped")

		// It's important to update the metrics before we handle the error from s.Store.Set() (if any),
		// since the source of the truth is the actual pipeline (stored in memory).
		measure.PipelinesGauge.WithValues(strings.ToLower(pl.Status.String())).Inc()

		storeErr := s.store.Set(ctx, pl.ID, pl)
		if storeErr != nil {
			return cerrors.Errorf("pipeline not updated: %w", storeErr)
		}

		return err
	})

	return nil
}
