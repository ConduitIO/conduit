# Dead letter queue

## Introduction

In Conduit the concept of a Dead letter queue (DLQ) describes the place where
messages are sent if they are negatively acknowledged (nacked). This can happen
for a number of reasons:

- A node in the pipeline experiences an error while processing a message (e.g.
  processor).
- The destination connector fails to process a message and sends back a nack.
- The pipeline is stopped forcefully causing in-flight messages to be nacked.

### Goals

The purpose of a DLQ is to make a pipeline resilient to errors that might
happen while processing messages.

The design for the DLQ should support the following:

- Allow user to reroute nacked messages to any Conduit connector (builtin or
  standalone).
- A message sent to the DLQ should reflect the contents it had just before the
  error happened. Example: if a message went through 2 processors and failed
  while being processed by a 3rd processor, the DLQ message should reflect any
  transforms done by the first 2 processors.
- Ensure message order is preserved when messages are sent to the DLQ.
  In other words - the order in which messages were produced by the source
  connector should be preserved, even if messages are nacked in a different
  order.
- If a message failed to be sent to the DLQ the nack action should fail and
  stop the pipeline.
- Allow the user to configure the window and percentage of messages that need
  to be nacked before the pipeline is considered broken and stops. Example: if
  90% of the last 30 messages are nacked stop the pipeline.
- Messages pushed to the DLQ should contain metadata describing the error and
  the source connector.

### Questions

- **Should we allow the user to attach custom metadata to messages pushed to
  the DLQ?**

### Background

Currently Conduit pipelines fail fast. If a message fails to be processed for
any reason it is negatively acknowledged which causes the pipeline to stop
running as soon as possible. In reality it's often the case that we don't want
to stop the pipeline because of one bad message, instead we want to store it
somewhere safe and keep the pipeline running. The solution is the support for
dead letter queues or DLQs.

## Implementation

### DLQ Handlers

Internally we already have a place to implement DLQ handlers
([here](https://github.com/ConduitIO/conduit/blob/d19379efc04d20d12ab9c80df82a29fcef7e8afd/pkg/pipeline/stream/source_acker.go#L114-L118)).
We need to decide on the API of the DLQ handler, in this design we propose an
interface with a method that takes a context, a record and an error and returns
an error. This should be all we need to write that record to the DLQ.

```go
type DLQHandler struct {
	func Write(ctx context.Context, r record.Record, reason error) error
}
```

The function `DLQ.Write` is expected to synchronously write the record to the
DLQ and return `nil` if that action succeeded or an error otherwise.

### DLQ Strategies

We propose to implement 3 DLQ strategies:

- `stop`

  Using this strategy would cause a nacked message to stop the pipeline
  immediately without draining remaining messages to the destination. (this is
  the current behavior and should stay the default).

- `ignore`

  This strategy would cause nacked messages to be logged but wouldn't cause the
  pipeline to stop. We can consider making it possible to configure the log
  level used to log the nacked message.

- `plugin`

  This strategy would reroute nacked messages to a destination connector
  plugin. This strategy needs to be able to get additional configuration
  options defining the plugin name and its settings.

### API

The DLQ should be defined as a property of
the [`Pipeline.Config`](https://github.com/ConduitIO/conduit/blob/d19379efc04d20d12ab9c80df82a29fcef7e8afd/proto/api/v1/api.proto#L28)
entity. It's is a config option that can be updated even after the pipeline is
created. We need to provide a way to choose a DLQ strategy and configure it if
necessary.

One option to achieve this is to provide these fields:
```protobuf
message DLQ {
  // strategy is one of [stop,ignore,plugin]
  string strategy = 1;
  // if strategy is plugin, this defines the plugin name
  string plugin = 2;
  // if strategy is plugin, this defines the plugin settings
  map<string, string> settings = 3;
}
```

### Pipeline config file

The config file should mimic the API structure.

```yaml

version: 1.0
pipelines:
  dlq-example:
    connectors:
      [...]
    dlq:
      # strategy is one of [fail,ignore,plugin]
      strategy: plugin
      # if strategy is plugin, this defines the plugin name
      plugin: builtin:file
      # if strategy is plugin, this defines the plugin settings
      settings:
        path: ./dlq-example.dlq
```

## Future work

- We can give our users even more flexibility by allowing nacked messages to be
  rerouted to other Conduit pipelines. This might not be a very common use case
  though and not worth the work, also it would be hard to expose it to the user
  in a simple way.
- If a nacked message fails to be delivered to a DLQ the pipeline stops with an
  error. We could give our users the option to choose what happens then - do
  they really want to stop the pipeline or ignore the error and carry on? Maybe
  even provide a fallback DLQ.
