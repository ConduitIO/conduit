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

/*
Package stream defines a message and nodes that can be composed into a data
pipeline. Nodes are connected with channels which are used to pass messages
between them.

We distinguish 3 node types: PubNode, PubSubNode and SubNode. A PubNode is at
the start of the pipeline and publishes messages. A PubSubNode sits between two
nodes, it receives messages from one node, processes them and sends them to the
next one. A SubNode is the last node in the pipeline and only receives messages
without sending them to any other node.

A message can have of these statuses:
  Open      The message starts out in an open state and will stay open while
            it's passed around between the nodes.
  Acked     Once a node successfully processes the message (e.g. it is sent to
            the destination or is filtered out by a processor) it is acked.
  Nacked    If some node fails to process the message it can nack the message
            and once it's successfully nacked (e.g. sent to a dead letter queue)
            it becomes nacked.
  Dropped   If a node experiences a non-recoverable error or has to stop running
            without sending the message to the next node (e.g. force stop) it
            can drop the message, then the message status changes to dropped.

In other words, once a node receives a message it has 4 options for how to
handle it: it can either pass it to the next node (message stays open), ack the
message and keep running, nack the message and keep running or drop the message
and stop running. This means that no message will be left in an open status when
the pipeline stops.

Nodes can register functions on the message which will be called when the status
of a message changes. For more information see StatusChangeHandler.

Nodes can implement LoggingNode to receive a logger struct that can be used to
output logs. The node should use the message context to create logs, this way
the logger will automatically attach the message ID as well as the node ID to
the message, making debugging easier.
*/
package stream
