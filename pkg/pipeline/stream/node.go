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

	"github.com/conduitio/conduit/pkg/foundation/log"
)

// Node represents a single node in a pipeline that knows how to process
// messages flowing through a pipeline.
type Node interface {
	// ID returns the identifier of this Node. Each Node in a pipeline must be
	// uniquely identified by the ID.
	ID() string

	// Run first verifies if the Node is set up correctly and either returns a
	// descriptive error or starts processing messages. Processing should stop
	// as soon as the supplied context is done. If an error occurs while
	// processing messages, the processing should stop and the error should be
	// returned. If processing stopped because the context was canceled, the
	// function should return ctx.Err(). All nodes that are part of the same
	// pipeline will receive the same context in Run and as soon as one node
	// returns an error the context will be canceled.
	// Run has different responsibilities, depending on the node type:
	//  * PubNode has to start producing new messages into the outgoing channel.
	//    The context supplied to Run has to be attached to all messages. Each
	//    message will be either acked or nacked by downstream nodes, it's the
	//    responsibility of PubNode to handle these acks/nacks if applicable.
	//    The outgoing channel has to be closed when Run returns, regardless of
	//    the return value.
	//  * SubNode has to start listening to messages sent to the incoming
	//    channel. It has to use the context supplied in the message for calls
	//    to other functions (imagine the message context as a request context).
	//    It is the responsibility of SubNode to ack or nack a message if it's
	//    processed correctly or with an error. If the incoming channel is
	//    closed, then Run should stop and return nil.
	//  * PubSubNode has to start listening to incoming messages, process them
	//    and forward them to the outgoing channel. The node should not ack/nack
	//    forwarded messages. If a message is dropped and not forwarded to the
	//    outgoing channel (i.e. filters), the message should be acked. If an
	//    error is encountered while processing the message, the message has to
	//    be nacked and Run should return with an error. If the incoming channel
	//	  is closed, then Run should stop and return nil. The outgoing channel
	//    has to be closed when Run returns, regardless of the return value.
	//    The incoming message pointers need to be forwarded, as upstream nodes
	//    could be waiting for acks/nacks on that exact pointer. If the node
	//    forwards a new message (not the exact pointer it received), then it
	//    needs to forward any acks/nacks to the original message pointer.
	Run(ctx context.Context) error
}

// PubNode represents a node at the start of a pipeline, which pushes new
// messages to downstream nodes.
type PubNode interface {
	Node

	// Pub returns the outgoing channel, that can be used to connect downstream
	// nodes to PubNode. It is the responsibility of PubNode to close this
	// channel when it stops running (see Node.Run). Pub needs to be called
	// before running a PubNode, otherwise Node.Run should return an error.
	Pub() <-chan *Message
}

// SubNode represents a node at the end of a pipeline, which listens to incoming
// messages from upstream nodes.
type SubNode interface {
	Node

	// Sub sets the incoming channel, that is used to listen to new messages.
	// Node.Run should listen to messages coming from this channel until the
	// channel is closed. Sub needs to be called before running a SubNode,
	// otherwise Node.Run should return an error.
	Sub(in <-chan *Message)
}

// PubSubNode represents a node in the middle of a pipeline, located between two
// nodes. It listens to incoming messages from the incoming channel, processes
// them and forwards them to the outgoing channel.
type PubSubNode interface {
	PubNode
	SubNode
}

type StoppableNode interface {
	Node

	// Stop signals a running StopNode that it should gracefully shutdown. It
	// should stop producing new messages, wait to receive acks/nacks for any
	// in-flight messages, close the outgoing channel and return from Node.Run.
	// Stop should return right away, not waiting for the node to actually stop
	// running. If the node is not running the function does not do anything.
	// The reason supplied to Stop will be returned by Node.Run.
	Stop(ctx context.Context, reason error) error
}

type ForceStoppableNode interface {
	Node

	// ForceStop signals a running ForceStoppableNode that it should stop
	// running as soon as it can, even if that means that it doesn't properly
	// clean up after itself. This method is a last resort in case something
	// goes wrong and the pipeline gets stuck (e.g. a connector plugin is not
	// responding).
	ForceStop(ctx context.Context)
}

// LoggingNode is a node which expects a logger.
type LoggingNode interface {
	Node

	// SetLogger sets the logger used by the node for logging.
	SetLogger(log.CtxLogger)
}
