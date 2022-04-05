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
)

// DestinationNode wraps a Destination connector and implements the Sub node interface
type DestinationNode struct {
	Name           string
	Destination    connector.Destination
	ConnectorTimer metrics.Timer
	// AckerNode is responsible for handling acks
	AckerNode *AckerNode

	base   subNodeBase
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
	defer func() {
		// wait for acker node to receive all outstanding acks, time out after
		// 1 minute or right away if the context is already canceled.
		waitCtx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		n.AckerNode.Wait(waitCtx)

		// teardown will kill the plugin process
		tdErr := n.Destination.Teardown(connectorCtx)
		if tdErr != nil {
			if err == nil {
				err = tdErr
			} else {
				// we are already returning an error, just log this error
				n.logger.Err(ctx, err).Msg("could not tear down destination connector")
			}
		}
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

		// first signal ack handler we might receive an ack, since this could
		// already happen before write returns
		err = n.AckerNode.ExpectAck(msg)
		if err != nil {
			return err
		}

		writeTime := time.Now()
		err = n.Destination.Write(msg.Ctx, msg.Record)
		if err != nil {
			n.AckerNode.ForgetAndDrop(msg)
			return cerrors.Errorf("error writing to destination: %w", err)
		}
		n.ConnectorTimer.Update(time.Since(writeTime))
	}
}

// Sub will subscribe this node to an incoming channel.
func (n *DestinationNode) Sub(in <-chan *Message) {
	n.base.Sub(in)
}

// SetLogger sets the logger.
func (n *DestinationNode) SetLogger(logger log.CtxLogger) {
	n.logger = logger
}
