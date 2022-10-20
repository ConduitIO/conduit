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
	"context"
	"time"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/conduitio/conduit/pkg/record"
)

// DestinationNode wraps a Destination connector and implements the Sub node interface
type DestinationNode struct {
	Name           string
	Destination    connector.Destination
	ConnectorTimer metrics.Timer

	base   pubSubNodeBase
	logger log.CtxLogger
}

func (n *DestinationNode) ID() string {
	return n.Name
}

func (n *DestinationNode) Run(ctx context.Context) (err error) {
	// start a fresh connector context to make sure the connector is running
	// until this method returns
	connectorCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// first open connector, this means we actually start the plugin process
	err = n.Destination.Open(connectorCtx)
	if err != nil {
		return cerrors.Errorf("could not open destination connector: %w", err)
	}

	// lastPosition stores the position of the last successfully processed record
	var lastPosition record.Position
	// openMsgTracker tracks open messages until they are acked or nacked
	var openMsgTracker OpenMessagesTracker
	defer func() {
		stopErr := n.Destination.Stop(connectorCtx, lastPosition)
		if stopErr != nil {
			// log this error right away because we're not sure the connector
			// will be able to stop right away, we might block for 1 minute
			// waiting for acks and we don't want the log to be empty
			n.logger.Err(ctx, err).Msg("could not stop destination connector")
			if err == nil {
				err = stopErr
			}
		}

		n.logger.Trace(ctx).Msg("waiting for open messages")
		openMsgTracker.Wait()

		// teardown will kill the plugin process
		tdErr := n.Destination.Teardown(connectorCtx)
		err = cerrors.LogOrReplace(err, tdErr, func() {
			n.logger.Err(ctx, tdErr).Msg("could not tear down destination connector")
		})
	}()

	trigger, cleanup, err := n.base.Trigger(ctx, n.logger, n.Destination.Errors())
	if err != nil {
		return err
	}
	defer cleanup()

	for {
		msg, err := trigger()
		if err != nil || msg == nil {
			return err
		}

		n.logger.Trace(msg.Ctx).Msg("writing record to destination connector")

		writeTime := time.Now()
		err = n.Destination.Write(msg.Ctx, msg.Record)
		if err != nil {
			// An error in Write is a fatal error, we probably won't be able to
			// process any further messages because there is a problem in the
			// communication with the plugin. We need to nack the message to not
			// leave it open and then return the error to stop the pipeline.
			_ = msg.Nack(err)
			return cerrors.Errorf("error writing to destination: %w", err)
		}
		n.ConnectorTimer.Update(time.Since(writeTime))

		openMsgTracker.Add(msg)
		lastPosition = msg.Record.Position

		err = n.base.Send(ctx, n.logger, msg)
		if err != nil {
			return msg.Nack(err)
		}
	}
}

// Sub will subscribe this node to an incoming channel.
func (n *DestinationNode) Sub(in <-chan *Message) {
	n.base.Sub(in)
}

// Pub will return the outgoing channel.
func (n *DestinationNode) Pub() <-chan *Message {
	return n.base.Pub()
}

// SetLogger sets the logger.
func (n *DestinationNode) SetLogger(logger log.CtxLogger) {
	n.logger = logger
}
