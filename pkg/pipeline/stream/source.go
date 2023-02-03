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

//go:generate mockgen -destination=mock/source.go -package=mock -mock_names=Source=Source . Source

package stream

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/csync"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/conduitio/conduit/pkg/record"
)

const (
	ControlMessageStopSourceNode ControlMessageType = "stop-source-node"
)

// SourceNode wraps a Source connector and implements the Pub node interface
type SourceNode struct {
	Name          string
	Source        Source
	PipelineTimer metrics.Timer

	stopReason error
	base       pubNodeBase
	logger     log.CtxLogger

	state              csync.ValueWatcher[nodeState]
	stopOnce           sync.Once
	connectorCtxCancel context.CancelFunc
}

type Source interface {
	ID() string
	Open(context.Context) error
	Read(context.Context) (record.Record, error)
	Ack(context.Context, record.Position) error
	Stop(context.Context) (record.Position, error)
	Teardown(context.Context) error
	Errors() <-chan error
}

// ID returns a properly formatted SourceNode ID prefixed with `source/`
func (n *SourceNode) ID() string {
	return n.Name
}

func (n *SourceNode) Run(ctx context.Context) (err error) {
	defer n.state.Set(nodeStateStopped)

	// first prepare the trigger to get the cleanup function which will close
	// the outgoing channel and stop downstream nodes
	trigger, cleanup, err := n.base.Trigger(
		ctx,
		n.logger,
		n.Source.Errors(),
		func(ctx context.Context) (*Message, error) {
			n.logger.Trace(ctx).Msg("reading record from source connector")
			r, err := n.Source.Read(ctx)
			if err != nil {
				return nil, cerrors.Errorf("error reading from source: %w", err)
			}

			return &Message{Record: r, SourceID: n.Source.ID()}, nil
		},
	)
	if err != nil {
		return err
	}
	defer cleanup()

	// start a fresh connector context to make sure the connector is running
	// until this method returns
	var connectorCtx context.Context
	connectorCtx, n.connectorCtxCancel = context.WithCancel(context.Background())
	defer n.connectorCtxCancel()

	// openMsgTracker tracks open messages until they are acked or nacked
	var openMsgTracker OpenMessagesTracker

	// open connector, this means we actually start the plugin process
	err = n.Source.Open(connectorCtx)
	if err != nil {
		return cerrors.Errorf("could not open source connector: %w", err)
	}
	defer func() {
		// wait for open messages before tearing down connector
		n.logger.Trace(ctx).Msg("waiting for open messages")
		openMsgTracker.Wait()

		tdErr := n.Source.Teardown(connectorCtx)
		err = cerrors.LogOrReplace(err, tdErr, func() {
			n.logger.Err(ctx, tdErr).Msg("could not tear down source connector")
		})
	}()

	n.state.Set(nodeStateRunning)

	var (
		// when source node encounters the record with this position it needs to
		// stop retrieving new records
		stopPosition record.Position
		// last processed position is stored in this position
		lastPosition record.Position
	)

	for {
		msg, err := trigger()
		if err != nil || msg == nil {
			return cerrors.Errorf("source stream was stopped unexpectedly: %w", err)
		}

		if msg.ControlMessageType() == ControlMessageStopSourceNode {
			// this is a control message telling us to stop
			n.logger.Err(ctx, n.stopReason).
				Str(log.RecordPositionField, msg.Record.Position.String()).
				Msg("stopping source node")
			stopPosition = msg.Record.Position

			if bytes.Equal(stopPosition, lastPosition) {
				// we already encountered the record with the last position
				return n.stopReason
			}
			continue
		}

		// track message until it reaches an end state
		openMsgTracker.Add(msg)
		n.registerMetricStatusHandler(msg)

		lastPosition = msg.Record.Position
		err = n.base.Send(ctx, n.logger, msg)
		if err != nil {
			return msg.Nack(err, n.ID())
		}

		if bytes.Equal(stopPosition, lastPosition) {
			// it's the last record that we are supposed to process, stop here
			return n.stopReason
		}
	}
}

func (n *SourceNode) registerMetricStatusHandler(msg *Message) {
	readAt, err := msg.Record.Metadata.GetReadAt()
	if err != nil {
		// if the plugin did not set the field fallback to the time Conduit
		// received the record (now)
		readAt = time.Now()
	}
	msg.RegisterStatusHandler(
		func(*Message, StatusChange) error {
			n.PipelineTimer.Update(time.Since(readAt))
			return nil
		},
	)
}

func (n *SourceNode) Stop(ctx context.Context, reason error) error {
	var err error
	var stopExecuted bool
	n.stopOnce.Do(func() {
		// only execute stop once, more calls won't make a difference
		err = n.stop(ctx, reason)
		stopExecuted = true
	})
	if err != nil {
		// an error happened, allow stop to be executed again
		n.stopOnce = sync.Once{}
	}
	if !stopExecuted {
		n.logger.Warn(ctx).Msg("source connector stop already triggered, " +
			"ignoring second stop request (if the pipeline is stuck, please " +
			"report the issue to the Conduit team)")
	}
	return err
}

func (n *SourceNode) stop(ctx context.Context, reason error) error {
	state, err := n.state.Watch(ctx, csync.WatchValues(nodeStateRunning, nodeStateStopped))
	if err != nil {
		return err
	}
	if state != nodeStateRunning {
		return cerrors.New("source node is not running")
	}

	n.stopReason = reason

	n.logger.Err(ctx, n.stopReason).Msg("stopping source connector")
	stopPosition, err := n.Source.Stop(ctx)
	if err != nil {
		return cerrors.Errorf("failed to stop source connector: %w", err)
	}

	// InjectControlMessage will inject a message into the stream of messages
	// being processed by SourceNode to let it know when it should stop
	// processing new messages.
	return n.base.InjectControlMessage(ctx, ControlMessageStopSourceNode, record.Record{
		Position: stopPosition,
	})
}

func (n *SourceNode) ForceStop(ctx context.Context) {
	n.logger.Warn(ctx).Msg("force stopping source connector")
	n.connectorCtxCancel()
}

func (n *SourceNode) Pub() <-chan *Message {
	return n.base.Pub()
}

func (n *SourceNode) SetLogger(logger log.CtxLogger) {
	n.logger = logger
}
