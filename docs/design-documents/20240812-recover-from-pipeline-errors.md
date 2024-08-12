# Recover from Pipeline Errors

## Introduction

### Goals

The primary goal of this design document is to introduce a robust error-handling
mechanism within Conduit to ensure the reliability and resilience of data
pipelines. The objectives include:

- **Error Classification:** Distinguish between transient (recoverable) and
  fatal (non-recoverable) errors.
- **Automatic Recovery:** Implement a mechanism to automatically restart
  pipelines in the event of transient errors with configurable backoff
  strategies.
- **Pipeline State Management:** Introduce new pipeline states to clearly
  indicate the status of recovery processes.
- **Configurable Backoff Strategy:** Allow users to configure backoff parameters
  to suit different production environments.

## Background

Conduit's engine is currently very strict, stopping the pipeline and putting it
into a degraded state whenever an error occurs. This approach is not optimal for
production environments where transient errors, such as connectivity issues, may
cause frequent disruptions. To enhance reliability, Conduit should include a
mechanism to automatically restart failing pipelines, leveraging the fact that
Conduit already tracks acknowledged record positions, making restarts a safe
operation with no risk of data loss (assuming connectors are implemented
correctly).

## Error classification

### Fatal errors

Fatal errors are defined as non-recoverable issues that require manual
intervention. When a fatal error occurs, the pipeline will be stopped
permanently, and no further attempts will be made to restart it. This approach
ensures that critical issues are addressed promptly and prevents Conduit from
entering an endless loop of restarts.

### Transient errors

Transient errors are recoverable issues that do not require manual intervention.
Examples include temporary network failures or timeouts. For such errors,
Conduit will automatically attempt to restart the pipeline using a backoff delay
strategy.

- **Default Behavior:** By default, all errors are considered transient and
  recoverable. A connector developer must explicitly mark an error as fatal if
  it should cause the pipeline to stop permanently.

## Recovery mechanism

### Backoff delay strategy

To manage transient errors and prevent constant pipeline restarts, Conduit will
implement a backoff delay strategy. This strategy introduces a waiting period
before attempting to restart the pipeline, allowing time for issues to resolve
themselves. The backoff parameters will be configurable to allow customization
based on specific production needs.

- **Configurable Parameters:**
  - **Minimum Delay Before Restart:** Default: 1 second
  - **Maximum Delay Before Restart:** Default: 1 minute
  - **Backoff Factor:** Default: 2
  - **Maximum Number of Retries:** Default: Infinite
  - **Delay After Which Fail Count is Reset:** Default: 5 minutes (or on first
    successfully end-to-end processed record)

This results in a default delay progression of 1s, 2s, 4s, 8s, 16s, 32s, 1m,...,
balancing the need for recovery time and minimizing downtime.

### Consecutive failures and permanent stop

To avoid endless restarts, Conduit will include an option to permanently stop
the pipeline after a configurable number of consecutive failures. Once the
pipeline successfully processes at least one record or runs without encountering
an error for a predefined time frame, the count of consecutive failures will
reset.

## Pipeline state management

A new pipeline state, `recovering`, will be introduced to indicate that a
pipeline has experienced a fault and is in the process of recovering. This state
will help differentiate between an actively running pipeline and one that is in
a restart loop.

- **State Transition:**
  - `running` → `recovering` → `running` (after pipeline runs without failure
    for the specified duration or a record is processed successfully end-to-end)
  - `running` → `recovering` → `degraded` (if max retries reached or fatal error
    occurs)

This approach will improve the visibility of pipeline status and assist in
debugging and monitoring.

## Record deduplication

Restarting a failed pipeline can result in some records being re-delivered, as
some records might have already been processed by the destination, but the
acknowledgments might not have reached the source before the failure occurred.
For that case, we should add a mechanism in front of the destination which
tracks records that have been successfully processed by the destination, but the
acknowledgment wasn't processed by the source yet. If such a record is
encountered after restarting the pipeline, Conduit should acknowledge it without
sending it to the destination.

## Audit log

If a pipeline is automatically recovered, a user needs to check the Conduit logs
to see the cause and what actually happened. Ideally that information would be
retrievable through the API and would be attached to the pipeline itself so a
user could get insights into how a pipeline is behaving.

To add this feature, we would need to track what is happening on a pipeline and
log any important events so we can return them to the user. When done properly,
this could serve as an audit log of everything a pipeline went through (
provisioning, starting, fault, recovery, stopping, deprovisioning etc.).

## Implementation plan

We will tackle this feature in separate phases, to make sure that the most
valuable parts are delivered right away, while the non-critical improvements are
added later.

### Phase 1

In this phase we will deliver the most critical functionality,
the [recovery mechanism](#Recovery-mechanism)
and [pipeline state management](#Pipeline-state-management). This is the minimum
we need to implement to provide value to our users, while only changing the
Conduit internals.

This phase includes the following tasks:

- Implement recovery mechanism as described in the topic above including the
  backoff retry.
- When a pipeline is restarting it should be in the `recovering` state
- Add the introduced `recovering` state to the API definition
- Make the backoff retry default parameters configurable _globally_ (in
  conduit.yml)

### Phase 2

This phase includes changes in the connector SDK and gives the connectors the
ability to circumvent the recovery mechanism when it makes sense to do so
using [error classification](#Error-classification).

The tasks in this phase are:

- Change the connector SDK to format error messages as JSON and include an error
  code in the error. Document the behavior.
- Conduit should parse errors coming from connectors as JSON. If they are valid
  JSON and include the error code field, that should be used to determine if an
  error is fatal or not.
  - Backwards compatibility with older connectors must be maintained, so plain
    text errors must still be accepted.
- Define error codes (or ranges, like HTTP errors) and define which ranges are
  transient or fatal.
- Add utilities in the connector SDK to create fatal or transient errors.
  - This should work even if errors are wrapped before being returned.
- On a fatal error, Conduit should skip the recovery mechanism and move the
  pipeline state into `degraded` right away.

### Phase 3

In this phase we will sand off any remaining sharp edges and add more visibility
and control to the user. This
includes [record deduplication](#Record-deduplication) and
the [audit log](#audit-log), which are bigger topics and will require a separate
design document.

## Open questions

- **Should failed processors trigger a retry, or should the pipeline be
  considered failed immediately?**
  - I am leaning towards making processor faults fatal by default, as most of
    the processors are stateless and always behave the same. Processors, which
    connect to a 3rd party resource and can experience transient errors, should
    implement retrials internally (like the `webhook.http` processor). Once we
    implement [error classification](#Error-classification), processors should
    be allowed to return transient errors which should be treated as such.
- **How does this functionality interact with a DLQ, especially with a nack
  threshold?**
  - There is a potential risk that restarting a pipeline with a nack threshold
    DLQ could result in records being continuously sent to the DLQ until the
    threshold is reached, triggering a restart loop. Since some records might be
    re-delivered, the same records could land in the DLQ multiple times. I
    propose documenting this edge case for now and tackling the solution as part
    of [record deduplication](#Record-deduplication).
    As for the nack threshold - if that threshold is reached it should emit a
    fatal error and put the pipeline into a `degraded` state without triggering
    the recovery mechanism, otherwise the nack threshold would lose its purpose.
