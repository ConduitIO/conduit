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
	"io"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-commons/semaphore"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

// SourceAckerNode is responsible for handling acknowledgments for messages of
// a specific source and forwarding them to the source in the correct order.
type SourceAckerNode struct {
	Name           string
	Source         Source
	DLQHandlerNode *DLQHandlerNode

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
	defer n.DLQHandlerNode.Done() // notify DLQHandlerNode that we are done

	trigger, cleanup, err := n.base.Trigger(ctx, n.logger, nil)
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
		// let the DLQ node know that a message entered the pipeline
		n.DLQHandlerNode.Add(1)
		// register ack and nack handlers
		n.registerAckHandler(msg, ticket)
		n.registerNackHandler(msg, ticket)

		err = n.base.Send(ctx, n.logger, msg)
		if err != nil {
			// The message could not be forwarded downstream; nack it. Return the
			// send failure itself even if the nack succeeds — otherwise a suppressed
			// closed-stream ack-forward inside Nack (see the handlers below) could
			// mask a genuine base.Send failure and let this node return nil. #1659.
			if nackErr := msg.Nack(err, n.ID()); nackErr != nil {
				return nackErr
			}
			return err
		}
	}
}

// isClosedSourceStream reports whether err is the sentinel a source connector's
// stream returns once it has closed (io.EOF, which plugin.ErrStreamNotOpen
// aliases). Such an error is a consequence of the source having stopped and
// carries no cause of its own, so forwarding an ack/nack to it is a no-op.
func isClosedSourceStream(err error) bool {
	return cerrors.Is(err, io.EOF)
}

func (n *SourceAckerNode) registerAckHandler(msg *Message, ticket semaphore.Ticket) {
	msg.RegisterAckHandler(
		func(msg *Message) (err error) {
			n.logger.Trace(msg.Ctx).Msg("acquiring semaphore for ack")
			lock := n.sem.Acquire(ticket)
			defer func() {
				if err != nil {
					n.fail = true
				}
				n.sem.Release(lock)
				n.DLQHandlerNode.Done()
			}()

			if n.fail {
				n.logger.Trace(msg.Ctx).Msg("blocking forwarding of ack to source connector, because another message failed to be acked/nacked")
				return cerrors.Errorf("another message failed to be acked/nacked")
			}

			n.logger.Trace(msg.Ctx).Msg("forwarding ack to source connector")
			err = n.Source.Ack(msg.Ctx, []opencdc.Position{msg.Record.Position})
			if err != nil {
				if !isClosedSourceStream(err) {
					return cerrors.Errorf("failed to forward ack to source connector: %w", err)
				}
				// The source stream is already closed (io.EOF) — the source
				// connector has stopped, typically because it errored. That io.EOF
				// carries no cause; the source's own Read error is the authoritative
				// pipeline cause (#1659). Suppress this derived error so it can't win
				// the tomb race and mask the real one. Safe under invariant 3:
				// Source.Ack returns before it mutates/persists the position, so the
				// checkpoint never advances past an unacked record, and the record is
				// already durably handled downstream by the time this ack fires.
				n.logger.Debug(msg.Ctx).Msg("source stream already closed, skipping ack forward")
				err = nil
			}

			n.DLQHandlerNode.Ack(msg)
			return nil
		},
	)
}

func (n *SourceAckerNode) registerNackHandler(msg *Message, ticket semaphore.Ticket) {
	msg.RegisterNackHandler(
		func(msg *Message, nackMetadata NackMetadata) (err error) {
			n.logger.Trace(msg.Ctx).Any("nackMetadata", nackMetadata).Msg("acquiring semaphore for nack")
			lock := n.sem.Acquire(ticket)
			defer func() {
				if err != nil {
					n.fail = true
				}
				n.sem.Release(lock)
				n.DLQHandlerNode.Done()
			}()

			if n.fail {
				n.logger.Trace(msg.Ctx).Msg("blocking forwarding of nack to DLQ handler, because another message failed to be acked/nacked")
				return cerrors.Errorf("another message failed to be acked/nacked")
			}

			n.logger.Trace(msg.Ctx).Msg("forwarding nack to DLQ handler")
			err = n.DLQHandlerNode.Nack(msg, nackMetadata)
			if err != nil {
				return cerrors.Errorf("failed to write message to DLQ: %w", err)
			}

			// The nacked record was successfully stored in the DLQ, we consider
			// the record "processed" so we need to ack it in the source.
			err = n.Source.Ack(msg.Ctx, []opencdc.Position{msg.Record.Position})
			if err != nil {
				if !isClosedSourceStream(err) {
					return cerrors.Errorf("failed to forward nack to source connector: %w", err)
				}
				// Same as the ack handler: a closed source stream (io.EOF) is a
				// derived error with no cause; suppress it so the source's real
				// error stays the authoritative pipeline cause (#1659). Safe under
				// invariant 3 (the record is already durably stored in the DLQ, and
				// Source.Ack does not persist a position on this error path).
				n.logger.Debug(msg.Ctx).Msg("source stream already closed, skipping nack forward")
				err = nil
			}

			return nil
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
