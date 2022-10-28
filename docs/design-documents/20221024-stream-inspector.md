# Stream Inspector

## Goals

Make troubleshooting pipelines easier by making it possible to inspect the data which is flowing through pipeline.

## Requirements

1. All pipeline components (accessible "from the outside", i.e. sources, processors and destinations) should be
   inspectable.
    * For v1, it's enough that destinations are inspectable.
2. A source inspector should show the records coming out of the respective source.
3. A destination inspector should show the records coming into the respective destination.
4. A processor has two inspectors: one for the incoming data and one for the resulting data (i.e. the transformed
   records).
5. The inspector should provide the data (i.e. the relevant records) about the component being inspected, from the 
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

### API
Inspecting a pipeline component is triggered with a gRPC/HTTP API request. If the pipeline component cannot be inspected
for any reason (e.g. inspection not supported, component not found), an error is returned.

Inspectable pipeline components themselves should expose an `Inspect` method, for example
```go
func(s Source) Inspect() chan Record 
```
(As a return value, we may use a special `struct` instead of `chan Record` to more easily propagate events, such as 
inspection done.)

The API layer will use this method to get the records it needs to stream to the client which requested an inspection.

For any given pipeline component, only one gRPC/HTTP API method is required, one which starts an inspection (i.e. 
sending of data to a client).

Three methods are required in total:
1. One to inspect a connector (there's a single endpoint for source and destination connectors)
2. One to inspect records coming into a processor
3. One to inspect records coming out of a processor

### Implementation option 1: WebSockets
Conduit provides a streaming endpoint. The stream is exposed using the WebSockets API.

grpc-gateway doesn't support WebSockets (see [this](https://github.com/grpc-ecosystem/grpc-gateway/issues/168)). There's 
an open-source proxy for it though, available [here](https://github.com/tmc/grpc-websocket-proxy/). 

### Implementation option 2: Pure gRPC
Conduit provides a streaming endpoint. The stream is consumed as such, i.e. a gRPC endpoint. A JavaScript implementation
of gRPC for browser clients exists (called [grpc-web](https://github.com/grpc/grpc-web)). While that would mean no 
changes in Conduit itself, it would require a lot of changes in the UI.

### Implementation option 3: Server-sent events 
[Server-sent events](https://html.spec.whatwg.org/#server-sent-events) enable servers to push events to clients. Unlike
WebSockets, the communication is unidirectional.

While server-sent events generally match our requirements, the implementation would not be straightforward because 
grpc-gateway doesn't support it, nor do they plan to support it (see [this](https://hackmd.io/@prysmaticlabs/eventstream-api) 
and [this](https://github.com/grpc-ecosystem/grpc-gateway/issues/26)).

Also, we want the inspector to be available through the UI, and using WebSockets is a much easier option than server-sent 
events.

### Chosen implementation
[grpc-websocket-proxy](https://github.com/tmc/grpc-websocket-proxy/) mention in option 1 is relatively popular and is
open-source, so using it is no risk. The other option is much costlier.

## Questions

* Should we rename it to pipeline inspector instead? Pipeline is the term we use in the API, streams are used
  internally.
    * Cons: We cannot call it CSI (Conduit's Stream Inspector) anymore.
* Is metadata needed (such as time the data was captured)?
* Should there be a limit on how long a stream inspector can run?
* Should there be a limit on how many records a stream inspector can receive?
* Are we interested in more than the records? Is there some other data we'd like to see (now or in future)? 
* Should it be possible to specify which data is shown?
  * Answer: No, out of scope. It's a matter of data representation on the client side.

## Future work

* Inspector REPL 