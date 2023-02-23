# Stream Inspector

## Goals

Make troubleshooting pipelines easier by making it possible to inspect the data which is flowing through pipeline.

## Requirements

1. All pipeline components (accessible "from the outside", i.e. sources, processors and destinations) should be
   inspectable.
    - For v1, it's enough that destinations are inspectable.
2. A source inspector should show the records coming out of the respective source.
3. A destination inspector should show the records coming into the respective destination.
4. A processor has two inspectors: one for the incoming records and one for the transformed records).
5. The inspector should provide the relevant records about the component being inspected, from the
   time the inspection started. Historical data is not required.
6. The inspector should have a minimal footprint on the resource usage (CPU, memory etc.)
7. The inspector should have an insignificant impact on a pipeline's performance (ideally, no impact at all).
8. The inspector data should be available through:
    - the HTTP API,
    - the gRPC API and
    - the UI.
9. Multiple users should be able to inspect the same pipeline component at the same time. Every user's session is
   independent.
10. The inspector should stop publishing the data to a client, if the client appears to be idle/crashed.

**Important** :information_source:
In case a stream inspection is slower than the pipeline being inspected, it's possible that some records will be dropped
from the inspector. This is discussed more in the section "Blocking vs. non-blocking" below.

## Implementation

Here we discuss two aspects of the implementation: internals (i.e. how to actually get the records from the inspectable
pipeline components) and the API (i.e. how to deliver the inspected records to a client while providing a good user
experience).

### Blocking vs. non-blocking

It's possible that a stream inspector won't be able to catch up with a pipeline, e.g. in the case of high velocity sources.

To handle this, we have two options:

#### Option 1: Make the stream inspector blocking

In this option, the stream inspector would slow down the pipeline until it catches up. The goal is make sure all records
are inspected.

Advantages:

1. Having complete data when troubleshooting.
2. This would make it possible to inject messages into a pipeline in the future.

Disadvantages:

1. Pipeline performance is affected.
2. The implementation becomes more complex.

#### Option 2: Make the stream inspector non-blocking

In this option, the stream inspector would not slow the pipeline. Some records from the pipeline won't be shown in the
stream inspector at all due to this.

Advantages:

1. Pipeline performance is not affected.
2. Simpler implementation.

Disadvantages:

1. Not having complete data when troubleshooting.

#### Chosen option

The chosen option is a non-blocking stream inspector for following reasons:

A blocking stream inspector would fall apart if we inspect a stream that's processing 10s of thousands of messages a
second.

Also, the concept of "stream surgery" (inserting messages into a pipeline or modifying existing ones) may feel
intuitively valuable, in practice it might not be due to volume of messages.

The pattern we're working towards is one where we enable the user to easily discover the cause of a breakage (e.g.
bad data or bad transform) and allow them to easily write a processor that corrects the issue and re-introduces the
data back into the stream.

### Push based vs. pull based

Implementations will generally use one of two approaches: pull based and push based.

- In a **pull based** implementation, a client (HTTP client, gRPC client, UI) would be periodically checking for
  inspection data.
  - A client would need to store the "position" of the previous inspection, to get newer data.
  - This would require inspection state management in Conduit for each session.
  - Inspection data would come in with a delay.
- In a **push based** implementation, Conduit would be pushing the inspection data to a client (HTTP client, gRPC
  client, UI).
  - A client would not need to store the "position" of the previous inspection, to get newer data.
  - Inspection state management in Conduit would be minimal.
  - Inspection data would come in almost real-time.
  - Inspecting the data may require additional tools (depends on the actual implementation)

From what we know so far, **the push based approach has more advantages and is easier to work with, so that's the
approach chosen here**. Concrete implementation options are discussed below.

For context: gRPC is the main API for Conduit. The HTTP API is generated using [grpc-gateway](https://github.com/grpc-ecosystem/grpc-gateway).

### API

Inspecting a pipeline component is triggered with a gRPC/HTTP API request. If the pipeline component cannot be inspected
for any reason (e.g. inspection not supported, component not found), an error is returned.

For any given pipeline component, only one gRPC/HTTP API method is required, one which starts an inspection (i.e.
sending of data to a client).

Three methods are required in total:

1. One to inspect a connector (there's a single endpoint for source and destination connectors)
2. One to inspect records coming into a processor
3. One to inspect records coming out of a processor

### Delivery options

Here we'll discuss options for delivering the stream of inspection data from the gRPC/HTTP API to a client.

#### Option 1: WebSockets

Conduit provides a streaming endpoint. The stream is exposed using the WebSockets API.

grpc-gateway doesn't support WebSockets (see [this](https://github.com/grpc-ecosystem/grpc-gateway/issues/168)). There's
an open-source proxy for it though, available [here](https://github.com/tmc/grpc-websocket-proxy/).

#### Option 2: Pure gRPC

Conduit provides a streaming endpoint. The stream is consumed as such, i.e. a gRPC endpoint. A JavaScript implementation
of gRPC for browser clients exists (called [grpc-web](https://github.com/grpc/grpc-web)). While that would mean no
changes in Conduit itself, it would require a lot of changes in the UI.

#### Option 3: Server-sent events

[Server-sent events](https://html.spec.whatwg.org/#server-sent-events) enable servers to push events to clients. Unlike
WebSockets, the communication is unidirectional.

While server-sent events generally match our requirements, the implementation would not be straightforward because
grpc-gateway doesn't support it, nor do they plan to support it (see [this](https://hackmd.io/@prysmaticlabs/eventstream-api)
and [this](https://github.com/grpc-ecosystem/grpc-gateway/issues/26)).

Also, we want the inspector to be available through the UI, and using WebSockets is a much easier option than server-sent
events.

#### Chosen delivery option

[grpc-websocket-proxy](https://github.com/tmc/grpc-websocket-proxy/) mention in option 1 is relatively popular and is
open-source, so using it is no risk. The other option is much costlier.

### Internals: Exposing the inspection data

Following options exist to expose the inspection data for node. How the data will be used by the API is discussed in
below sections.

#### Option 1: Connectors and processors expose a method

In this option, inspection would be performed at the connector or processor level.

Inspectable pipeline components themselves would expose an `Inspect` method, for example:

```go
// Example for a source, can also be a processor or a destination
func(s Source) Inspect(direction string) chan Record
```

(As a return value, we may use a special `struct` instead of `chan Record` to more easily propagate events, such as
inspection done.)

Advantages:

1. The implementation would be relatively straightforward and not complex.

Disadvantages:

1. Minor changes in the sources, processors and destinations are needed.

#### Option 2: Dedicated inspector nodes

In this option, we'd have a node which would be dedicated for inspecting data coming in and out of a source, processor
or destination node. To inspect a node we would dynamically add a node before or after a node being inspected.

Advantages:

1. All the code related to inspecting nodes would be "concentrated" in one or two node types.
2. Existing nodes don't need to be changed.
3. This makes it possible to inspect any node.
4. Solving this problem would put us into a good position to solve <https://github.com/ConduitIO/conduit/issues/201>.

Disadvantages:

1. Currently, it's not possible to dynamically add a node, which would mean that we need to restart a pipeline to do this,
   and that's not a good user experience.
2. On the other hand, changing the code so that a pipeline's topology can be dynamically changed is a relatively large
   amount of work.

#### Option 3: Add the inspection code to `PubNode`s and `SubNode`s

Advantages:

1. Solves the problem for all nodes. While we're not necessarily interested in all the nodes, solving the problem at
   the `PubNode` level solves the problem for sources and output records for processors at the same time, and solving
   the problem at the `SubNode` level solves the problem for destinations and input records for processors at the same
   time.

Disadvantages:

1. Adding inspection to pub and sub nodes is complex. This complexity is reflected in following:
   - Once we add a method to the `PubNode` and `SubNode` interfaces, we'll need to implement it in all current
     implementations, even if that means only calling a method from an embedded type.
   - `PubNode` and `SubNode` rely on Go channels to publish/subscribe to messages. Automatically sending messages from
     those channels to registered inspectors is non-trivial.

#### Chosen implementation

Option 1, i.e. connectors and processor exposing methods to inspect themselves, is the preferred option given that
options 2 and 3 are relatively complex, and we would risk delivering this feature in scope of the 0.4 release.

## Questions

- Should we rename it to pipeline inspector instead? Pipeline is the term we use in the API, streams are used
  internally.
  - **Answer**: Since the inspector is about inspecting data, and the term stream stands for data whereas pipeline
      stands for the setup/topology, keeping the term "stream inspector" makes sense.
- Is metadata needed (such as time the records were captured)?
  - Examples:
    - The time at which a record entered/left a node.
    - ~~The source connector from which a record originated.~~ Can be found in OpenCDC metadata.
- Should there be a limit on how long a stream inspector can run?
- Should there be a limit on how many records a stream inspector can receive?
  - Pros:
    - Users could mistakenly leave inspector open "forever".
    - This would handle the case where an inspector crashes.
  - Cons:
    - Some users may want to run an inspection over a longer period of time.
    - The inspector will be non-blocking, not accumulating any data in memory, so it running over a longer period of time
    won't consume significant resources.
  - **Answer**: For now, we'll have no limits.
- Are we interested in more than the records? Is there some other data we'd like to see (now or in future)?
  - **Answer**: We didn't find any data not related to records themselves which would be useful in the inspector.
- Should it be possible to specify which data is shown?
  - **Answer**: No, out of scope. It's a matter of data representation on the client side.
- Should the inspector be blocking (if a client is consuming the inspected records slower that then pipeline rate,
then the pipeline would be throttled) or non-blocking?
  - Arguments for blocking:
    - the inspected data will be complete no matter what, making troubleshooting easier
    - if we make it possible to insert records during inspection, it will make implementation easier
  - Arguments against:
    - "inspection" is meant to be "view-only" and not a debugger
    - for high-volume sources this will always result in pipeline being throttled
  - **Answer**: Based on above, the stream inspector should be non-blocking.

## Future work

- Inspector REPL: <https://github.com/ConduitIO/conduit/issues/697>
