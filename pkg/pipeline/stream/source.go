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
	"sync"
	"time"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/conduitio/conduit/pkg/plugin"
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

	var wgOpenMessages sync.WaitGroup
	defer func() {
		// wait for open messages before tearing down connector
		n.logger.Trace(ctx).Msg("waiting for open messages to be processed")
		wgOpenMessages.Wait()
		n.logger.Trace(ctx).Msg("all messages processed, tearing down source")
		tdErr := n.Source.Teardown(connectorCtx)
		if tdErr != nil {
			if err == nil {
				err = tdErr
			} else {
				// we are already returning an error, just log this error
				n.logger.Err(ctx, err).Msg("could not tear down source connector")
			}
		}
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

			return &Message{Record: r}, nil
		},
	)
	if err != nil {
		return err
	}
	defer cleanup()

	for {
		msg, err := trigger()
		if err != nil || msg == nil {
			if cerrors.Is(err, plugin.ErrStreamNotOpen) {
				// node was stopped gracefully, return stop reason
				return n.stopReason
			}
			return err
		}

		// register another open message
		wgOpenMessages.Add(1)
		msg.RegisterStatusHandler(
			func(msg *Message, change StatusChange) error {
				// this is the last handler to be executed, once this handler is
				// reached we know either the message was successfully acked, nacked
				// or dropped
				defer n.PipelineTimer.Update(time.Since(msg.Record.ReadAt))
				defer wgOpenMessages.Done()
				return nil
			},
		)

		err = n.base.Send(ctx, n.logger, msg)
		if err != nil {
			msg.Drop()
			return err
		}
	}
}

func (n *SourceNode) Stop(reason error) {
	ctx := context.TODO() // TODO get context as parameter
	n.logger.Err(ctx, reason).Msg("stopping source connector")
	n.stopReason = reason
	_ = n.Source.Stop(ctx) // TODO return error
}

func (n *SourceNode) Pub() <-chan *Message {
	return n.base.Pub()
}

func (n *SourceNode) SetLogger(logger log.CtxLogger) {
	n.logger = logger
}
