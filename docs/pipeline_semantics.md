# Pipeline semantics

This document describes the inner workings of a Conduit pipeline, its structure, and behavior. It also describes a
Conduit message and its lifecycle as it flows through the pipeline.

## Pipeline structure

A Conduit pipeline is a directed acyclic graph of nodes. Each node runs in its own goroutine and is connected to other
nodes via unbuffered Go channels that can transmit messages. In theory, we could create arbitrarily complex graphs of
nodes, but for the sake of a simpler API we expose the ability to create graphs with the following structure:

![pipeline](https://user-images.githubusercontent.com/8320753/165359385-79d98217-fd7f-478d-a059-121487e2b4a2.svg)

In the diagram above we see 7 sections:

- **Source connectors** - represents the code that communicates with a 3rd party system and continuously fetches records and
  sends them to Conduit (e.g. [kafka connector](https://github.com/conduitio/conduit-connector-kafka)). Every source
  connector is managed by a source node that receives records, wraps them in a message, and sends them downstream to the
  next node. A pipeline requires at least one source connector to be runnable.
- **Source processors** - these processors only receive messages originating at a specific source connector. Source
  processors are created by specifying the corresponding source connector as the parent entity. Processors are not
  required for starting a pipeline.
- **Fan-in node** - this node is essentially a multiplexer that receives messages produced by all source connectors and
  sends them into one output channel. The order of messages coming from different connectors is nondeterministic. A
  fan-in node is automatically created for all pipelines.
- **Pipeline processors** - these processors receive all messages that flow through the pipeline, regardless of the source
  or destination. Pipeline processors are created by specifying the pipeline as the parent entity. Processors are not
  required for starting a pipeline.
- **Fan-out node** - this node is the counterpart to the fan-in node and acts as a demultiplexer that sends messages coming
  from a single input to multiple downstream nodes (one for each destination). The fan-out node does not buffer
  messages, instead, it waits for a message to be sent to all downstream nodes before processing the next message (
  see [backpressure](#Backpressure)). A fan-out node is automatically created for all pipelines.
- **Destination processors** - these processors receive only messages that are meant to be sent to a specific destination
  connector. Destination processors are created by specifying the corresponding destination connector as the parent
  entity. Processors are not required for starting a pipeline.
- **Destination connectors** - represents the code that communicates with a 3rd party system and continuously receives
  records from Conduit and writes them to the destination (
  e.g. [kafka connector](https://github.com/conduitio/conduit-connector-kafka)). Every destination connector is managed
  by a destination node that receives records and sends them to the connector. A pipeline requires at least one
  destination connector to be runnable.

There are additional internal nodes that Conduit adds to each pipeline. These are inconsequential for the purpose of
this document (e.g. nodes for gathering metrics, nodes for managing acknowledgments, etc.).

## Message

A message wraps a record and manages the records lifecycle as it flows through the pipeline. A message is created in the
source node when it receives a record from the source connector. A pointer to the message is passed between nodes until
it reaches a destination node. Nodes are only allowed to hold a reference to a single message at a time, meaning that
they need to pass the message to the next node before taking another message¹. This also means there is no explicit
buffer in Conduit, a pipeline can only hold as many open messages as there are nodes in the pipeline.

¹This might change in the future if we decide to add support for multi-message transforms.

### Message states

*TBD, need to rethink if our current message states make sense.*

### Message state change handlers

A pipeline node can choose to register state change handlers on a message that will be called when the message state
changes. This can be used to register a nack handler which is notified if a message is negatively acknowledged and route
it to another node that sends that message to a dead letter queue.

## Semantics

### Messages are delivered in order

Since messages are passed between nodes in channels and a node only processes one message at a time it is guaranteed
that messages will flow through the Conduit pipeline in the same order as they are received by the source.

There are two caveats:

- If a pipeline contains multiple source connectors then the order of two messages coming from different connectors is
  nondeterministic. Messages coming from the same source connector are still guaranteed to retain their order.
- If a dead letter queue is configured then negatively acknowledged messages will be removed from the stream while the
  pipeline will be kept running, thus impacting the order of messages.

The order guarantee only holds inside of Conduit. Once a message reaches a destination connector it is allowed to buffer
messages and batch write them to 3rd party systems. Normally the connector would retain the order, although we can't
vouch for badly written connectors that don't follow this behavior.

### Messages are delivered at least once

In course of a pipeline run, it is guaranteed that any message produced by a source connector will be delivered to a
destination connector at most once. A message won't be delivered if it gets filtered out or a node encounters an error
while processing it or a message that was produced before it.

Between pipeline restarts, it is guaranteed that any message that is processed successfully by all nodes and not
filtered out will be delivered to a destination connector at least once. Multiple deliveries can occur in pipelines with
multiple destinations that stopped because of a negatively acknowledged record or pipelines, where a destination
negatively acknowledged a record and processed more messages after that. For this reason, we strongly recommend
implementing the write operation of a destination connector in an idempotent way (if possible).

### Acks are delivered in order

Conduit ensures that acknowledgments are sent to the source connector in the exact same order as records produced by the
connector. This guarantee still holds, even if a destination connector acknowledges records in a different order or if a
later record gets filtered out faster than an earlier record managed to get written to all destinations.

### Acks are delivered at most once

Acknowledgments are sent back to the source connector at most once. This means that if a message gets negatively
acknowledged and was not successfully pushed to a dead letter queue, the acknowledgment for that message won't be
delivered to the source connector. Acknowledgments of all messages produced after this message also won't be delivered
to the source connector, otherwise the order delivery guarantee would be violated. The absence of an acknowledgment
after the source connector teardown is initiated can be interpreted as a negative acknowledgment.

### Backpressure

The usage of unbuffered channels between nodes and the absence of explicit buffers results in backpressure. This means
that the speed of the destination connector dictates the speed of the whole pipeline.

There is an implicit buffer that needs to be filled up before backpressure takes effect. The buffer is equal to the
number of nodes in a pipeline - similar to how a longer garden hose holds more water, a longer Conduit pipeline can hold
more messages. Note that in the case of standalone connectors, Conduit is using gRPC streams to communicate with
connectors which internally buffers requests before sending them over the wire, thus creating another implicit buffer (
we are aware of this issue: [#211](https://github.com/ConduitIO/conduit/issues/211)).

If there are multiple destinations the fanout node won't fetch a new message from the upstream node until the current
message was successfully sent to all downstream nodes (this doesn't mean the message was necessarily processed, just
that all downstream nodes received the message). As a consequence, the speed of the pipeline will be throttled to
accommodate the abilities of the slowest destination connector.

### Dead letter queue

Messages that get negatively acknowledged can be rerouted to another destination called a dead letter queue (DLQ) where
they are stored and can be reprocessed at a later point in time after manual intervention. If rerouting is set up and
the message successfully reaches the DLQ the message will be acked instead. The user has the freedom to configure a DLQ
that simply logs a warning and drops messages to achieve "at most once" delivery guarantees.

### Pipeline stop

A pipeline can be stopped in two ways - either it's stopped gracefully or forcefully.

- A graceful stop is initiated either by Conduit shutting down or by the user requesting the pipeline to stop. Only the
  source connector nodes will receive the signal to stop running. The source nodes will stop running and close their
  outgoing channels, notifying the downstream nodes that there will be no more messages. This behavior propagates down
  the pipeline until the last node stops running, draining any open messages in the process.
- A forceful stop is initiated when a node stops running because it experienced an unrecoverable error. In that case,
  the context that is shared by all nodes will get canceled, signaling to all nodes simultaneously that they should stop
  running as soon as possible. Messages that are in the pipeline won't be drained, instead, they are dropped and will be
  requested from the source again once the pipeline is restarted. The error returned from the first node that failed
  will be stored in the pipeline and can be retrieved through the API.
