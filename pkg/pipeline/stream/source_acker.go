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

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/semaphore"
)

// SourceAckerNode is responsible for handling acknowledgments for messages of
// a specific source and forwarding them to the source in the correct order.
type SourceAckerNode struct {
	Name   string
	Source connector.Source

	base   pubSubNodeBase
	logger log.CtxLogger

	// sem ensures acks are sent to the source in the correct order and only one
	// at a time
	sem semaphore.Simple
	// fail is set to true once the first ack/nack fails and we can't guarantee
	// that acks will be delivered in the correct order to the source anymore,
	// at that point we completely stop processing acks/nacks
	fail bool
}

func (n *SourceAckerNode) ID() string {
	return n.Name
}

func (n *SourceAckerNode) Run(ctx context.Context) error {
	trigger, cleanup, err := n.base.Trigger(ctx, n.logger)
	if err != nil {
		return err
	}

	defer cleanup()
	for {
		msg, err := trigger()
		if err != nil || msg == nil {
			return err
		}

		// enqueue message in semaphore
		ticket := n.sem.Enqueue()
		n.registerAckHandler(msg, ticket)
		n.registerNackHandler(msg, ticket)

		err = n.base.Send(ctx, n.logger, msg)
		if err != nil {
			return msg.Nack(err)
		}
	}
}

func (n *SourceAckerNode) registerAckHandler(msg *Message, ticket semaphore.Ticket) {
	msg.RegisterAckHandler(
		func(msg *Message) (err error) {
			defer func() {
				if err != nil {
					n.fail = true
				}
				tmpErr := n.sem.Release(ticket)
				err = cerrors.LogOrReplace(err, tmpErr, func() {
					n.logger.Err(msg.Ctx, tmpErr).Msg("error releasing semaphore ticket for ack")
				})
			}()
			n.logger.Trace(msg.Ctx).Msg("acquiring semaphore for ack")
			err = n.sem.Acquire(ticket)
			if err != nil {
				return cerrors.Errorf("could not acquire semaphore for ack: %w", err)
			}

			if n.fail {
				n.logger.Trace(msg.Ctx).Msg("blocking forwarding of ack to source connector, because another message failed to be acked/nacked")
				return cerrors.Errorf("another message failed to be acked/nacked")
			}

			n.logger.Trace(msg.Ctx).Msg("forwarding ack to source connector")
			return n.Source.Ack(msg.Ctx, msg.Record.Position)
		},
	)
}

func (n *SourceAckerNode) registerNackHandler(msg *Message, ticket semaphore.Ticket) {
	msg.RegisterNackHandler(
		func(msg *Message, reason error) (err error) {
			defer func() {
				if err != nil {
					n.fail = true
				}
				tmpErr := n.sem.Release(ticket)
				err = cerrors.LogOrReplace(err, tmpErr, func() {
					n.logger.Err(msg.Ctx, tmpErr).Msg("error releasing semaphore ticket for nack")
				})
			}()
			n.logger.Trace(msg.Ctx).Msg("acquiring semaphore for nack")
			err = n.sem.Acquire(ticket)
			if err != nil {
				return cerrors.Errorf("could not acquire semaphore for nack: %w", err)
			}

			if n.fail {
				n.logger.Trace(msg.Ctx).Msg("blocking forwarding of nack to DLQ handler, because another message failed to be acked/nacked")
				return cerrors.Errorf("another message failed to be acked/nacked")
			}

			n.logger.Trace(msg.Ctx).Msg("forwarding nack to DLQ handler")
			// TODO implement DLQ and call it here, right now any nacked message
			//  will just stop the pipeline because we don't support DLQs,
			//  don't forget to forward ack to source if the DLQ call succeeds
			//  https://github.com/ConduitIO/conduit/issues/306
			return cerrors.New("no DLQ handler configured")
		},
	)
}

func (n *SourceAckerNode) Sub(in <-chan *Message) {
	n.base.Sub(in)
}

func (n *SourceAckerNode) Pub() <-chan *Message {
	return n.base.Pub()
}

func (n *SourceAckerNode) SetLogger(logger log.CtxLogger) {
	n.logger = logger
}
