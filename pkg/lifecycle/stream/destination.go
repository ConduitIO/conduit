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

//go:generate mockgen -typed -destination=mock/destination.go -package=mock -mock_names=Destination=Destination . Destination

package stream

import (
	"context"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
)

// DestinationNode wraps a Destination connector and implements the Sub node interface
type DestinationNode struct {
	Name           string
	Destination    Destination
	ConnectorTimer metrics.Timer

	base   pubSubNodeBase
	logger log.CtxLogger

	connectorCtxCancel context.CancelFunc
}

type Destination interface {
	ID() string
	Open(context.Context) error
	Write(context.Context, []opencdc.Record) error
	Ack(context.Context) ([]connector.DestinationAck, error)
	Stop(context.Context, opencdc.Position) error
	Teardown(context.Context) error
	Errors() <-chan error
}

func (n *DestinationNode) ID() string {
	return n.Name
}

func (n *DestinationNode) Run(ctx context.Context) (err error) {
	// first prepare the trigger to get the cleanup function which will close
	// the outgoing channel and stop downstream nodes
	trigger, cleanup, err := n.base.Trigger(ctx, n.logger, n.Destination.Errors())
	if err != nil {
		return err
	}
	defer cleanup()

	// start a fresh connector context to make sure the connector is running
	// until this method returns
	var connectorCtx context.Context
	connectorCtx, n.connectorCtxCancel = context.WithCancel(context.Background())
	defer n.connectorCtxCancel()

	var (
		// lastPosition stores the position of the last successfully processed record
		lastPosition opencdc.Position
		// openMsgTracker tracks open messages until they are acked or nacked
		openMsgTracker OpenMessagesTracker
	)

	// open connector, this means we actually start the plugin process
	err = n.Destination.Open(connectorCtx)
	if err != nil {
		return cerrors.Errorf("could not open destination connector: %w", err)
	}
	defer func() {
		stopErr := n.Destination.Stop(connectorCtx, lastPosition)
		err = cerrors.LogOrReplace(err, stopErr, func() {
			n.logger.Err(ctx, stopErr).Msg("could not stop destination connector")
		})

		n.logger.Trace(ctx).Msg("waiting for open messages")
		openMsgTracker.Wait()

		// teardown will kill the plugin process
		tdErr := n.Destination.Teardown(connectorCtx)
		err = cerrors.LogOrReplace(err, tdErr, func() {
			n.logger.Err(ctx, tdErr).Msg("could not tear down destination connector")
		})
	}()

	for {
		msg, err := trigger()
		if err != nil || msg == nil {
			return err
		}
		if msg.filtered {
			n.logger.Info(ctx).Str(log.MessageIDField, msg.ID()).
				Msg("message marked as filtered, sending directly to next node")
			err = n.base.Send(ctx, n.logger, msg)
			if err != nil {
				return msg.Nack(err, n.ID())
			}
			continue
		}

		n.logger.Trace(msg.Ctx).Msg("writing record to destination connector")

		writeTime := time.Now()
		err = n.Destination.Write(msg.Ctx, []opencdc.Record{msg.Record})
		if err != nil {
			// An error in Write is a fatal error, we probably won't be able to
			// process any further messages because there is a problem in the
			// communication with the plugin. We need to nack the message to not
			// leave it open and then return the error to stop the pipeline.
			_ = msg.Nack(err, n.ID())
			return cerrors.Errorf("error writing to destination: %w", err)
		}
		n.ConnectorTimer.Update(time.Since(writeTime))

		openMsgTracker.Add(msg)
		lastPosition = msg.Record.Position

		err = n.base.Send(ctx, n.logger, msg)
		if err != nil {
			return msg.Nack(err, n.ID())
		}
	}
}

func (n *DestinationNode) ForceStop(ctx context.Context) {
	n.logger.Warn(ctx).Msg("force stopping destination connector")
	n.connectorCtxCancel()
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
