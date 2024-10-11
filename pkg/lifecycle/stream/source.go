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

//go:generate mockgen -typed -destination=mock/source.go -package=mock -mock_names=Source=Source . Source

package stream

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/conduitio/conduit-commons/csync"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
)

const (
	ControlMessageStopSourceNode ControlMessageType = "stop-source-node"

	stopHint = "If the pipeline is stuck try waiting a few minutes, existing records could need a while to be flushed. Otherwise try force stopping the pipeline."
)

// SourceNode wraps a Source connector and implements the Pub node interface
type SourceNode struct {
	Name          string
	Source        Source
	PipelineTimer metrics.Timer

	base   pubNodeBase
	logger log.CtxLogger

	state              csync.ValueWatcher[nodeState]
	connectorCtxCancel context.CancelFunc

	// mctx guards access to the connector context
	mctx sync.Mutex

	stop struct {
		sync.Mutex
		position        opencdc.Position
		reason          error
		positionFetched bool
		successful      bool
	}
}

type Source interface {
	ID() string
	Open(context.Context) error
	Read(context.Context) ([]opencdc.Record, error)
	Ack(context.Context, []opencdc.Position) error
	Stop(context.Context) (opencdc.Position, error)
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
		func(ctx context.Context) ([]*Message, error) {
			n.logger.Trace(ctx).Msg("reading record from source connector")
			recs, err := n.Source.Read(ctx)
			if err != nil {
				return nil, cerrors.Errorf("error reading from source: %w", err)
			}

			msgs := make([]*Message, len(recs))
			for i, r := range recs {
				msgs[i] = &Message{Record: r, SourceID: n.Source.ID()}
			}
			return msgs, nil
		},
	)
	if err != nil {
		return err
	}
	defer cleanup()

	// start a fresh connector context to make sure the connector is running
	// until this method returns
	var connectorCtx context.Context

	n.mctx.Lock()
	connectorCtx, n.connectorCtxCancel = context.WithCancel(context.Background())
	n.mctx.Unlock()
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
		stopPosition opencdc.Position
		// last processed position is stored in this position
		lastPosition opencdc.Position
	)

	for {
		msg, err := trigger()
		if err != nil || msg == nil {
			return cerrors.Errorf("source stream was stopped unexpectedly: %w", err)
		}

		if msg.ControlMessageType() == ControlMessageStopSourceNode {
			// this is a control message telling us to stop
			n.logger.Err(ctx, n.stop.reason).
				Str(log.RecordPositionField, msg.Record.Position.String()).
				Msg("stopping source node")
			stopPosition = msg.Record.Position

			if bytes.Equal(stopPosition, lastPosition) {
				// we already encountered the record with the last position
				return n.stop.reason
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
			return n.stop.reason
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
	state, err := n.state.Watch(ctx, csync.WatchValues(nodeStateRunning, nodeStateStopped))
	if err != nil {
		return err
	}
	if state != nodeStateRunning {
		return cerrors.New("source node is not running")
	}

	n.stop.Lock()
	defer n.stop.Unlock()

	if n.stop.successful {
		if n.stop.reason != nil {
			return cerrors.Errorf("stop already triggered (%s), caused by: %w", stopHint, n.stop.reason)
		}
		return cerrors.Errorf("stop already triggered (%s)", stopHint)
	}

	err = n.stopGraceful(ctx, reason)
	if err != nil {
		return err
	}
	n.stop.successful = true

	return nil
}

func (n *SourceNode) stopGraceful(ctx context.Context, reason error) (err error) {
	if !n.stop.positionFetched {
		n.logger.Err(ctx, reason).Msg("stopping source connector")
		stopPosition, err := n.Source.Stop(ctx)
		if err != nil {
			return cerrors.Errorf("failed to stop source connector: %w", err)
		}

		n.stop.positionFetched = true
		n.stop.reason = reason
		n.stop.position = stopPosition
	}

	n.logger.Trace(ctx).
		Str(log.RecordPositionField, n.stop.position.String()).
		Msg("injecting stop source node control message")

	// InjectControlMessage will inject a message into the stream of messages
	// being processed by SourceNode to let it know when it should stop
	// processing new messages.
	return n.base.InjectControlMessage(ctx, ControlMessageStopSourceNode, opencdc.Record{
		Position: n.stop.position,
	})
}

func (n *SourceNode) ForceStop(ctx context.Context) {
	n.logger.Warn(ctx).Msg("force stopping source connector")
	n.mctx.Lock()
	n.connectorCtxCancel()
	n.mctx.Unlock()
}

func (n *SourceNode) Pub() <-chan *Message {
	return n.base.Pub()
}

func (n *SourceNode) SetLogger(logger log.CtxLogger) {
	n.logger = logger
}
