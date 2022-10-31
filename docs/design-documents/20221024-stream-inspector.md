# Stream Inspector

## Goals

Make troubleshooting pipelines easier by making it possible to inspect the data which is flowing through pipeline.

## Requirements

1. All pipeline components (accessible "from the outside", i.e. sources, processors and destinations) should be
   inspectable.
    * For v1, it's enough that destinations are inspectable.
2. A source inspector should show the records coming out of the respective source.
3. A destination inspector should show the records coming into the respective destination.
4. A processor has two inspectors: one for the incoming records and one for the transformed records).
5. The inspector should provide the relevant records about the component being inspected, from the 
   time the inspection started. Historical data is not required.
6. The inspector should have a minimal footprint on the resource usage (CPU, memory etc.)
7. The inspector should have an insignificant impact on a pipeline's performance (ideally, no impact at all).
8. The inspector data should be available through:
    * the HTTP API,
    * the gRPC API and
    * the UI.
9. Multiple users should be able to inspect the same pipeline component at the same time. Every user's session is
   independent.
10. The inspector should stop publishing the data to a client, if the client appears to be idle/crashed.

## Implementation

### Push based vs. pull based
Implementations will generally use one of two approaches: pull based and push based.

* In a **pull based** implementation, a client (HTTP client, gRPC client, UI) would be periodically checking for
  inspection data.
  * A client would need to store the "position" of the previous inspection, to get newer data.
  * This would require inspection state management in Conduit for each session.
  * Inspection data would come in with a delay.
* In a **push based** implementation, Conduit would be pushing the inspection data to a client (HTTP client, gRPC
  client, UI).
  * A client would not need to store the "position" of the previous inspection, to get newer data.
  * Inspection state management in Conduit would be minimal.
  * Inspection data would come in almost real-time.
  * Inspecting the data may require additional tools (depends on the actual implementation)

From what we know so far, **the push based approach has more advantages and is easier to work with, so that's the 
approach chosen here**. Concrete implementation options are discussed below.

For context: gRPC is the main API for Conduit. The HTTP API is generated using [grpc-gateway](https://github.com/grpc-ecosystem/grpc-gateway).

### Exposing the inspection data
Following options exist to expose the inspection data for node. How the data will be used by the API is discussed in
below sections.

#### Option 1: Inspectable nodes expose a method
Inspectable pipeline components themselves should expose an `Inspect` method, for example:
```go
// Example for a source, can also be a processor or a destination
func(s Source) Inspect(direction string) chan Record
```
(As a return value, we may use a special `struct` instead of `chan Record` to more easily propagate events, such as
inspection done.)

#### Option 2: Dedicated inspector nodes
TBD

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

### Option 3: Server-sent events 
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

## Questions

* Should we rename it to pipeline inspector instead? Pipeline is the term we use in the API, streams are used
  internally.
    * **Answer**: Since the inspector is about inspecting data, and the term stream stands for data whereas pipeline
      stands for the setup/topology, keeping the term "stream inspector" makes sense.
* Is metadata needed (such as time the records were captured)?
  * Examples: 
    * The time at which a record entered/left a node.
    * ~~The source connector from which a record originated.~~ Can be found in OpenCDC metadata.
* Should there be a limit on how long a stream inspector can run?
* Should there be a limit on how many records a stream inspector can receive?
  * Pros: 
    * Users could mistakenly leave inspector open "forever".
    * This would handle the case where an inspector crashes.
  * Cons:
    * Some users may want to run an inspection over a longer period of time.
    * The inspector will be non-blocking, not accumulating any data in memory, so it running over a longer period of time
    won't consume significant resources.
  * **Answer**: For now, we'll have no limits.
* Are we interested in more than the records? Is there some other data we'd like to see (now or in future)? 
  * **Answer**: We didn't find any data not related to records themselves which would be useful in the inspector.
* Should it be possible to specify which data is shown?
  * **Answer**: No, out of scope. It's a matter of data representation on the client side.
* Should the inspector be blocking (if a client is consuming the inspected records slower that then pipeline rate,
then the pipeline would be throttled) or non-blocking?
  * Arguments for blocking: 
    * the inspected data will be complete no matter what, making troubleshooting easier
    * if we make it possible to insert records during inspection, it will make implementation easier
  * Arguments against: 
    * "inspection" is meant to be "view-only" and not a debugger
    * for high-volume sources this will always result in pipeline being throttled 
  * **Answer**: Based on above, the stream inspector should be non-blocking.

## Future work

* Inspector REPL: https://github.com/ConduitIO/conduit/issues/697