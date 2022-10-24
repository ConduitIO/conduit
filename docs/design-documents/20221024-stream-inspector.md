# Stream Inspector

## Goals
Make troubleshooting pipelines easier by making it possible to inspect the data which is flowing through pipeline. 

## Requirements
1. All pipeline components (accessible "from the outside", i.e. sources, processors and destinations) should be inspectable.
   * For v1, it's enough that destinations are inspectable.
2. The inspector data needs to include:
   * the records coming out of the source connectors
   * the records coming into the processors and the transformed record
   * the records coming into the destination connectors
3. The inspector should provide the data about the component being inspected, from the time the inspection started. 
Historical data is not required.
4. The inspector should have a minimal footprint on the resource usage (CPU, memory etc.)
5. It should be possible to specify which data is shown. 
6. The inspector data should be available through the HTTP API and the UI.

## Implementation

## Questions
* Should we rename it to pipeline inspector instead? Pipeline is the term we use in the API, streams are used internally.
  * Cons: We cannot call it CSI (Conduit's Stream Inspector) anymore.
* Is metadata needed (such as time the data was captured)?

## Future work
* Inspector REPL 