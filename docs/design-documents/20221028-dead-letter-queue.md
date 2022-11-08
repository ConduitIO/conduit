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
  order (this is already assured using
  a [semaphore](https://github.com/ConduitIO/conduit/blob/b6325584d51ec64bc9086faddcdaa2788a5dfa8f/pkg/pipeline/stream/source_acker.go#L35-L37)).
  Note that the order is only preserved for messages coming from the same
  source.
- If a message failed to be sent to the DLQ the nack action should fail and
  stop the pipeline.
- Allow the user to configure the window and number of messages that need to be
  nacked to consider the pipeline broken and trigger a stop. Example: if 25 of
  the last 30 messages are nacked stop the pipeline.
- Messages pushed to the DLQ should contain metadata describing the error and
  the source connector.

### Questions

- **Should we allow the user to attach custom metadata to messages pushed to
  the DLQ?**
  - We decided to add this as future work in the form of allowing users to
    attach processors to a DLQ.

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

The function `DLQHandler.Write` is expected to synchronously write the record
to the DLQ and return `nil` if that action succeeded or an error otherwise. The
synchronous write is needed to ensure the DLQ message actually reached its
destination, otherwise we need to consider the nack failed and need to stop the
pipeline.

### Log connector

We should implement a connector that simply logs records as they come in. The
user should be able to configure which level is used when logging the records.

This is needed because it would be the default connector used for the DLQ.

### DLQ Strategy

We propose to implement a single DLQ strategy that would reroute nacked
messages to a destination connector plugin. The user needs to be able to define
the plugin name and its settings.

The user can also configure a stop window and stop threshold.

- `window_size` defines how many last acks/nacks are monitored in the window
  that controls if the pipeline should stop. 0 disables the window.
- `window_nack_threshold` defines the number of nacks in the window that would
  cause the pipeline to stop.

The default DLQ plugin should be `builtin:log` with a `window_size` of 1 and a
`window_nack_threshold` of 1. This would result in the same behavior as before
the introduction of DLQs - the first nacked message would stop the pipeline.

### API

We need to provide a way to get the current DLQ configuration and to update it.

One option to achieve this is to add two methods to the `PipelineService` and
map them to HTTP endpoints:

```protobuf
message DLQ {
  // plugin is the connector plugin used for storing DLQ records
  // default = builtin:log, configured to log with level WARN
  string plugin = 1;
  // settings are the plugin settings
  map<string, string> settings = 2;

  // window_size defines how many last acks/nacks are monitored in the window
  // that controls if the pipeline should stop (0 disables the window)
  // default = 1
  uint64 window_size = 3;
  // window_nack_threshold defines the number of nacks in the window that would
  // cause the pipeline to stop
  // default = 1
  uint64 window_nack_threshold = 4;
}

service PipelineService {
  rpc GetDLQ(GetDLQRequest) returns (GetDLQResponse) {
    option (google.api.http) = {
      get: "/v1/pipelines/{id}/dlq"
      response_body: "dlq"
    };
  };

  rpc UpdateDLQ(UpdateDLQRequest) returns (UpdateDLQResponse) {
    option (google.api.http) = {
      put: "/v1/pipelines/{id}/dlq"
      body: "dlq"
      response_body: "dlq"
    };
  };
}

message GetDLQRequest {
  string id = 1;
}

message GetDLQResponse {
  DLQ dlq = 1;
}

message UpdateDLQRequest {
  string id = 1;
  DLQ dlq = 2;
}

message UpdateDLQResponse {
  DLQ dlq = 1;
}
```

### Pipeline config file

Example of a DLQ config that logs records and never stops.

```yaml
version: 1.1
pipelines:
  dlq-example:
    connectors:
      [...]
    dlq:
      # disable stop window
      windowSize: 0

      # the next 3 lines explicitly define the log plugin
      # removing this wouldn't change the behavior, it's the default DLQ config
      plugin: builtin:log
      settings:
        level: WARN
```

Example of a DLQ config that writes nacked records to a file and stops if at
least 30 of the last 100 records were nacked.

```yaml
version: 1.1
pipelines:
  dlq-example:
    connectors:
      [...]
    dlq:
      # stop when 30 of the last 100 messages are nacked
      windowSize: 100
      windowNackThreshold: 30

      # define file plugin used for DLQ messages
      plugin: builtin:file
      settings:
        path: ./dlq-example.dlq
```

### Metrics

We need to add metrics for monitoring the DLQ. We intend to add similar metrics
to the ones we have for connectors:

- `conduit_dlq_bytes`
- `conduit_dlq_execution_duration_seconds`

## Future work

- We can give our users even more flexibility by allowing nacked messages to be
  rerouted to other Conduit pipelines. This might not be a very common use case
  though and not worth the work, also it would be hard to expose it to the user
  in a simple way.
- If a nacked message fails to be delivered to a DLQ the pipeline stops with an
  error. We could give our users the option to choose what happens then - do
  they really want to stop the pipeline or ignore the error and carry on? Maybe
  even provide a fallback DLQ.
- The DLQ will currently only be configurable through the API or pipeline
  config file, adding a UI for this feature will be done in the future.
