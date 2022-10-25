# Stream Inspector

## Goals
Make troubleshooting pipelines easier by making it possible to inspect the data which is flowing through pipeline. 

## Requirements
1. All pipeline components (accessible "from the outside", i.e. sources, processors and destinations) should be inspectable.
   * For v1, it's enough that destinations are inspectable.
2. A source inspector should show the records coming out of the respective source.
3. A destination inspector should show the records coming into the respective destination.
4. A processor has two inspectors: one for the incoming data and one for the resulting data (i.e. the transformed records).
5. The inspector should provide the data about the component being inspected, from the time the inspection started. 
Historical data is not required.
6. The inspector should have a minimal footprint on the resource usage (CPU, memory etc.)
7. It should be possible to specify which data is shown. 
8. The inspector data should be available through the HTTP API and the UI.
9. The inspector should stop publishing the data if the client appears to be idle/crashed.

## Implementation

## Questions
* Should we rename it to pipeline inspector instead? Pipeline is the term we use in the API, streams are used internally.
  * Cons: We cannot call it CSI (Conduit's Stream Inspector) anymore.
* Is metadata needed (such as time the data was captured)?

## Future work
* Inspector REPL 