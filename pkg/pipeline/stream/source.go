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

	"github.com/conduitio/conduit/pkg/record"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/conduitio/conduit/pkg/plugins"
	"github.com/jpillora/backoff"
)

// SourceNode wraps a Source connector and implements the Pub node interface
type SourceNode struct {
	Name           string
	Source         connector.Source
	ConnectorTimer metrics.Timer
	PipelineTimer  metrics.Timer

	base   pubNodeBase
	logger log.CtxLogger
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
		wgOpenMessages.Wait()
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

	b := &backoff.Backoff{
		Factor: 2,
		Min:    time.Millisecond * 100,
		Max:    time.Second * 5,
	}
	ticker := time.NewTicker(1) // first tick happens right away
	defer ticker.Stop()

	trigger, cleanup, err := n.base.Trigger(
		ctx,
		n.logger,
		ticker.C,
		n.Source.Errors(),
		func(tick interface{}) (*Message, error) {
			n.logger.Trace(ctx).Msg("reading record from source connector")

			readTime := time.Now().UTC()
			r, err := n.Source.Read(ctx)
			if err != nil {
				return nil, cerrors.Errorf("error reading from source: %w", err)
			}
			if r.Key == nil {
				r.Key = record.RawData{}
			}
			if r.Payload == nil {
				r.Payload = record.RawData{}
			}
			// only record metric if no error occurred, this way we don't record
			// empty reads
			n.ConnectorTimer.Update(time.Since(readTime))
			r.ReadAt = readTime
			r.SourceID = n.ID()
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
			if plugins.IsRecoverableError(err) {
				n.logger.Trace(ctx).Err(err).Msg("backing off because of recoverable error")
				ticker.Reset(b.Duration())
				continue
			}
			return err
		}

		// register another open message
		wgOpenMessages.Add(1)
		msg.RegisterStatusHandler(
			func(msg *Message, change StatusChange, next StatusChangeHandler) error {
				// this is the last handler to be executed, once this handler is
				// reached we know either the message was successfully acked, nacked
				// or dropped
				defer n.PipelineTimer.Update(time.Since(msg.Record.ReadAt))
				defer wgOpenMessages.Done()
				return next(msg, change)
			},
		)

		msg.RegisterAckHandler(
			func(msg *Message, next AckHandler) error {
				n.logger.Trace(msg.Ctx).Msg("forwarding ack to source connector")
				err := n.Source.Ack(msg.Ctx, msg.Record.Position)
				if err != nil {
					return err
				}
				return next(msg)
			},
		)

		err = n.base.Send(ctx, n.logger, msg)
		if err != nil {
			msg.Drop()
			return err
		}

		ticker.Reset(1) // next tick should happen right away
		b.Reset()       // reset backoff retry
	}
}

func (n *SourceNode) Stop(reason error) {
	n.base.Stop(reason)
}

func (n *SourceNode) Pub() <-chan *Message {
	return n.base.Pub()
}

func (n *SourceNode) SetLogger(logger log.CtxLogger) {
	n.logger = logger
}
