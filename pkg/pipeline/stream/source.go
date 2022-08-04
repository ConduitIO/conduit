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

package stream

import (
	"bytes"
	"context"
	"time"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
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
	Source        connector.Source
	PipelineTimer metrics.Timer

	stopReason error
	base       pubNodeBase
	logger     log.CtxLogger
}

// ID returns a properly formatted SourceNode ID prefixed with `source/`
func (n *SourceNode) ID() string {
	return n.Name
}

func (n *SourceNode) Run(ctx context.Context) (err error) {
	// start a fresh connector context to make sure the connector is running
	// until this method returns
	connectorCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// first open connector, this means we actually start the plugin process
	err = n.Source.Open(connectorCtx)
	if err != nil {
		return cerrors.Errorf("could not open source connector: %w", err)
	}

	// openMsgTracker tracks open messages until they are acked or nacked
	var openMsgTracker OpenMessagesTracker
	defer func() {
		// wait for open messages before tearing down connector
		n.logger.Trace(ctx).Msg("waiting for open messages to be processed")
		openMsgTracker.Wait()
		n.logger.Trace(ctx).Msg("all messages processed, tearing down source")
		tdErr := n.Source.Teardown(connectorCtx)
		err = cerrors.LogOrReplace(err, tdErr, func() {
			n.logger.Err(ctx, tdErr).Msg("could not tear down source connector")
		})
	}()

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
			n.logger.Err(ctx, n.stopReason).Msg("stopping source connector")
			stopPosition, err = n.Source.Stop(ctx)
			if err != nil {
				return cerrors.Errorf("failed to stop source connector: %w", err)
			}

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
			return msg.Nack(err)
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
	n.stopReason = reason
	// InjectControlMessage will inject a message into the stream of messages
	// being processed by SourceNode to let it know when it should stop
	// processing new messages.
	return n.base.InjectControlMessage(ctx, ControlMessageStopSourceNode)
}

func (n *SourceNode) Pub() <-chan *Message {
	return n.base.Pub()
}

func (n *SourceNode) SetLogger(logger log.CtxLogger) {
	n.logger = logger
}
