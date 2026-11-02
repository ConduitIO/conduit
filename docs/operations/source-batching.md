# Source batching by default

Since `conduit-connector-sdk` v0.18, a source that leaves `sdk.batch.size` and `sdk.batch.delay` unset reads in
batches of up to 100 records (adaptive drain, no timer). This page covers what to check when a pipeline
behaves differently after the upgrade, and what the `sdk.batch.*` codes mean. Postgres and S3 sources keep
`sdk.batch.size: 0` and `sdk.batch.delay: 0` and are not affected by the default.

To switch a source back to one record per read, set both `sdk.batch.size: "0"` and `sdk.batch.delay: "0"` in
the source connector's settings. Check the plugin's default with `conduit connector-plugins describe
<plugin>`, and the effective values in the
connector's startup log line (the SDK logs size, delay, mode and read path at open).

## Latency panels or alerts moved after the upgrade

### Symptom

`conduit_pipeline_execution_duration_seconds` (p50, p99) is higher than before the upgrade on a pipeline whose
source is generator, file or kafka. Throughput is the same or better. A latency alert fires.

### Diagnosis

The metric is measured from the record's `opencdc.readAt`, which the SDK sets when the connector builds the
record. It now includes time in the SDK's read-ahead and in a batch waiting for the engine. For a dense
source a record waits behind up to about two batches (around 200 records) instead of one. This is expected
and bounded by queueing, not by a timer: adaptive drain never waits to fill a batch.

It is not expected if the source is sparse (a few records per second) and latency rose by a fixed amount. Then
check that the connector is using `sdk.batch.delay: 0`. A non-zero delay is a fixed collection window and adds
up to that delay for the first record of every batch.

`conduit_connector_execution_duration_seconds` for the destination shows whether the write itself got slower.

### Remediation

- If the shift is only queueing on a dense source, re-baseline the dashboard and alert thresholds.
- If latency matters more than throughput for this pipeline, opt out (both settings to 0).
- If you set `sdk.batch.delay` above 0 yourself, lower it or set it to 0.

## Memory grows on a source with large records

### Symptom

The Conduit process RSS rises after the upgrade with a kafka, file or generator source that carries large
records.

### Diagnosis

A source can hold about three batches (read-ahead, the batch being collected, one in flight), so around
`3 x sdk.batch.size` records. At 100 and a few KB per record that is under a megabyte. With records of
hundreds of KB or more it is tens of MB per source. Kafka also holds up to about 200 more fetched,
unacked records than before.

### Remediation

Lower `sdk.batch.size` to a value that fits your record size, or set both settings to 0.

## A destination fails with a message size error

### Symptom

A standalone (non-built-in) destination fails every write with a gRPC "received message larger than max"
error, or a record is nacked with `pipeline.record_exceeds_max_receive_size`.

### Diagnosis

The standalone plugin's gRPC server rejects an engine-to-plugin request over
`connectors.max-receive-record-size` (4 MiB by default). On arch-v2 the engine splits a write by estimated wire
size so a 100-record batch does not exceed it. The nack with `pipeline.record_exceeds_max_receive_size` means a
single record is over the limit by itself. The classic engine writes one record at a time. If the gRPC error
appears on arch-v2, the engine's estimate was too low for those records: report it with a record size.

### Remediation

Raise `connectors.max-receive-record-size` (the plugin must accept it too), shrink the record upstream with a
processor, or route it to the dead-letter queue. Setting the source's batch settings to 0 also avoids large
multi-record writes.

## `sdk.batch.drain_semantics_pending` in a connector log

### Symptom

A source connector logs a WARN with `code=sdk.batch.drain_semantics_pending` at startup.

### Diagnosis

The pipeline sets `sdk.batch.size` above 0 and leaves `sdk.batch.delay` unset or `0`. Today that waits until the
batch is full, with no deadline. A sparse source can hold records unsent and unacked for as long as it takes
to fill a batch. In a later SDK release (two minor releases after v0.18) the same settings will mean "return
what is already queued". Nothing is lost either way; this is a notice that the meaning will change.

### Remediation

- To keep full batches: set `sdk.batch.delay` above 0. That is a fixed collection window and fixes the
  unbounded wait too.
- To take the future behavior now: remove `sdk.batch.size` (the default drain applies) or accept the change
  when you upgrade the SDK.
- To turn batching off: set both settings to 0.

## `sdk.batch.unbounded_wait` in a connector log

### Symptom

A WARN with `code=sdk.batch.unbounded_wait`, or a pipeline start or validation error with that code.

### Diagnosis

The effective settings wait for a full batch with no deadline. This is either an explicit `sdk.batch.size`
above 0 with `sdk.batch.delay: 0`, or a connector whose own spec has a non-zero delay default where the
pipeline sets `sdk.batch.delay: 0`. A sparse source holds records indefinitely.

### Remediation

Set `sdk.batch.delay` above 0, or set `sdk.batch.size` to 0 to turn batching off.

## `sdk.batch.size_defaulted` in a connector log

### Symptom

A WARN with `code=sdk.batch.size_defaulted`.

### Diagnosis

`sdk.batch.delay` is set and `sdk.batch.size` is 0. A batch needs a size, so the SDK substitutes a bounded one
(1000). A source that used to batch without an upper bound here is now capped.

### Remediation

Set `sdk.batch.size` explicitly.

## Reading these codes from Conduit

When one of these conditions is returned as an error from `Configure`, Conduit finds the code in the message
as a delimited token and returns it as the error code, with the SDK's message unchanged, the config path
(`/settings/sdk.batch.delay` or `/settings/sdk.batch.size`, relative to the connector) and a suggested fix.
Agents and `--json` output should match the code, not the text. SDKs older than v0.18 emit no codes.

Design: `docs/design-documents/20261027-default-source-batching.md`.
