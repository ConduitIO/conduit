// Copyright © 2026 Meroxa, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package lifecycle

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// CodeStopAndWaitTimeout is returned by StopAndWait when the bounded drain
// (DefaultStopAndWaitTimeout, or a tighter caller-supplied ctx deadline)
// elapses before the pipeline finished stopping, draining, or persisting. See
// StopAndWait's doc ("O2: bounding the drain") for why this is safe to
// surface rather than silently retrying or force-killing: nothing is torn
// down on this path, so the pipeline is left exactly as benign-duplicate-safe
// as it was before the call — never a silent gap.
//
// This supersedes the former CodeStopAndWaitUnsupported (removed alongside
// this package's StopAndWait/ReconfigureProcessor refusal guards — see
// docs/design-documents/20260731-archv2-drain-reconfigure.md, AC-7): that
// code meant "this arch cannot do this at all"; this one means "this specific
// attempt did not complete in time," which is the only refusal shape left
// once the funnel drain audit (same design doc, §3.1) established the
// underlying Stop/WaitPipeline/Persister interaction is safe to build
// StopAndWait on top of.
var (
	CodeStopAndWaitTimeout = conduiterr.Register("lifecycle_v2.stop_and_wait_timeout", codes.DeadlineExceeded)

	// CodePartialGracefulStopEscalated is returned by Stop/StopAndWait for an
	// N-source pipeline (slice 3b of the arch-v2 multi-connector epic) when a
	// graceful stop arms some sources within the deadline but not others. See
	// stopRunnablePipeline's doc and the adversarial-review finding this closes
	// (H1, docs/design-documents/20260801-archv2-multiconnector-nsource.md).
	//
	// Leaving that state unresolved would strand the pipeline reporting
	// StatusRunning indefinitely: the still-unarmed worker(s) keep reading,
	// workersWg never drains, and runPipeline's cleanup goroutine (the only
	// thing that ever writes a terminal status) never runs - invisible to an
	// operator except as an error return that may go to a disconnected caller.
	// Instead the pipeline's tomb is force-killed with this code so every
	// still-unarmed worker's next context check observes cancellation and
	// unwinds, guaranteeing the pipeline reaches a terminal (Degraded) status
	// instead of a silent, half-stopped one. At-least-once is preserved: this
	// never acks anything, so an interrupted worker's in-flight, unacked batch
	// simply replays on the pipeline's next start (invariant 3).
	CodePartialGracefulStopEscalated = conduiterr.Register("lifecycle_v2.partial_graceful_stop_escalated", codes.Aborted)
)

// CodeArchV2DestinationBatchingUnsupported is returned by Start, and by the
// live-apply preflight (PreflightPipelineConfig), when a pipeline would run on
// the preview pipeline architecture v2 (--preview.pipeline-arch-v2) and the
// dead-letter-queue connector sets sdk.batch.size above 1 (#2979) or
// sdk.batch.delay above 0 (#3321).
//
// Regular destinations are no longer refused by this code: the funnel now
// batches destination writes itself using the destination's
// effective sdk.batch.size/sdk.batch.delay, from the pipeline config or the
// connector spec's default (see
// docs/design-documents/20261017-funnel-destination-batching.md and
// 20261103-archv2-destination-batch-timing.md), and configures the SDK plugin
// with "0" for them, so a destination batch can no longer withhold its acks
// and stall the stop-and-wait loop. The DLQ is not wired through that path, so
// an explicit SDK batch on it still withholds acks and is refused. A DLQ
// connector that batches only by its spec default (S3) is configured "0" and
// writes each nack synchronously.
//
// With a fan-out (one source, several destinations), a record another
// destination nacks is handled only once a batching destination has also
// voted for it, that is when that destination's batch flushes (#3323): the
// DLQ write, and with the default DLQ (window-size 1, window-nack-threshold 0)
// the pipeline failure, wait for that flush. With sdk.batch.size and no
// sdk.batch.delay on a quiet source that wait has no bound until the next
// records fill the batch or the pipeline is stopped (#3231), and the pipeline
// reports running meanwhile. Set sdk.batch.delay to bound it. See
// docs/operations/archv2-destination-batching.md.
//
// The v2 destination task writes a batch and then waits for every ack of it
// before the next read. With sdk.batch.size > 1 the SDK destination only
// enqueues on Write and acks when its batch flushes: when it reaches
// sdk.batch.size, or sdk.batch.delay after its first record. When a destination
// batch is larger than the records per write, acks are withheld, and with the
// default sdk.batch.delay of 0 nothing flushes by time, so the pipeline stops
// delivering while still reporting Running. With a delay it delivers, but one
// flush per write (one measurement: 83 rec/s against 56.7k rec/s on the
// default engine, with one-record reads and a 10ms delay). The classic engine
// reads acks on their own goroutine and is not affected.
//
// The guard is conservative: any sdk.batch.size above 1 is refused, even where
// a particular write size would happen to align with the batch, and so is any
// sdk.batch.delay above 0, because the SDK batches on a delay alone with a
// size of its own (1000 in v0.17.2), which withholds acks the same way. Size 0
// or 1 with no delay is safe. Values that do not parse (strconv.Atoi,
// time.ParseDuration) are left to the plugin. Refusing turns a silent liveness failure into an actionable error. It
// fires before any connector is opened, so nothing is read and nothing is
// acked (invariants 1 and 3). The SDK trims keys and values before
// reading them, so the guard does too.
//
// The error names every offending connector; ConfigPath and Fix address the
// first one, as a JSON pointer into the pipeline config document
// (/dead-letter-queue/settings/sdk.batch.size). Remove sdk.batch.size (and
// sdk.batch.delay) from the dead-letter queue, or run without
// --preview.pipeline-arch-v2 if the pipeline does not need record fan-out. See
// docs/operations/archv2-destination-batching.md.
//
// A pipeline created through the API has no config document, so for it the
// error carries no ConfigPath and no Fix; the message still names the
// connector id and name and the setting.
var CodeArchV2DestinationBatchingUnsupported = conduiterr.Register("pipeline.archv2_destination_batching_unsupported", codes.FailedPrecondition)

// CodeArchV2DestinationBatchingNotLiteral is returned by Start, and by the
// live-apply preflight (PreflightPipelineConfig), when a pipeline would run on
// the preview pipeline architecture v2 (--preview.pipeline-arch-v2) and a
// destination or the dead-letter-queue sets sdk.batch.size or sdk.batch.delay
// to a value that is not a literal: it contains "$", such as an environment
// reference (${S3_BATCH}). `conduit pipelines validate` and `conduit doctor`
// report the same finding offline (config.SDKBatchNonLiteral is shared).
//
// v2 batches a destination itself with its effective sdk.batch.* settings and
// configures the plugin "0" for each key that would turn the plugin's own
// batcher on (docs/design-documents/20261103-archv2-destination-batch-timing.md,
// Decision 2). The engine decides that from the stored settings, and ${VAR}
// references are expanded only for the plugin. A reference therefore reads as
// unset to the engine (which adopts the connector default) while the plugin
// gets the expanded value and batches behind the engine: each engine flush
// waits up to the plugin's delay more, or, with a plugin size above the
// engine's and no plugin delay, the plugin holds the acks of a flush until
// records the engine never sends fill its batch, and the pipeline stalls while
// reporting Running. On the dead-letter queue a reference would also slip past
// CodeArchV2DestinationBatchingUnsupported. No record is lost either way (acks
// are only withheld), but liveness is, so the value is refused before any
// connector is built or opened: nothing is read and nothing is acked
// (invariants 1 and 3).
//
// Set sdk.batch.size and sdk.batch.delay to literal values (for example 1000
// and 1s). The classic engine passes the settings to the plugin and is not
// affected. ConfigPath addresses the first offending setting as a JSON pointer
// into the pipeline config document (/connectors/1/settings/sdk.batch.size or
// /dead-letter-queue/settings/sdk.batch.delay); a pipeline created through the
// API has no config document, so the error then carries no ConfigPath and the
// message alone names the connector and the setting. There is no structured
// Fix: the right literal is the operator's choice. See
// docs/operations/archv2-destination-batching.md.
var CodeArchV2DestinationBatchingNotLiteral = conduiterr.Register("pipeline.archv2_destination_batching_not_literal", codes.FailedPrecondition)

// CodeArchV2DestinationBatchUnboundedWait is logged as a warning when a
// pipeline starts on the preview engine (--preview.pipeline-arch-v2) with a
// destination that batches by sdk.batch.size alone, with no sdk.batch.delay.
// It is not an error: the pipeline starts and runs.
//
// The engine batches such a destination itself and flushes a partial batch
// only when it fills, when the pipeline stops, or, with sdk.batch.delay, when
// the delay has passed since its first record (the flush timer). Without the
// delay nothing bounds the wait: on a source that goes quiet the last records
// are not written, and not acknowledged upstream, until more records arrive
// or the pipeline stops. That matches the connector SDK's own batching. No
// data is at risk (nothing is acknowledged before it is written), but the
// source position does not move (on Postgres, WAL is retained). On a pipeline
// with several destinations, a record another destination nacks is routed to
// the dead-letter queue, or with the default dead-letter queue fails the
// pipeline, only once this destination has flushed it too, so that waits
// without bound as well. Set sdk.batch.delay on the destination (for example
// 1s) to bound it. See docs/operations/archv2-destination-batching.md.
var CodeArchV2DestinationBatchUnboundedWait = conduiterr.Register("pipeline.archv2_destination_batch_unbounded_wait", codes.FailedPrecondition)
