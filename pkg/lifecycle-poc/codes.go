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
// the preview pipeline architecture v2 (--preview.pipeline-arch-v2) and a
// destination connector, or the dead-letter-queue connector, sets
// sdk.batch.size above 1 (#2979).
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
// a particular write size would happen to align with the batch. Size 0 or 1, or
// sdk.batch.delay on its own, is safe (the SDK flushes synchronously at the
// size). Refusing turns a silent liveness failure into an actionable error. It
// fires before any connector is opened, so nothing is read and nothing is
// acked (invariants 1 and 3). Values that do not parse as an integer are left
// to the plugin's own config validation. The SDK trims keys and values before
// reading them, so the guard does too.
//
// The error names every offending connector; ConfigPath and Fix address the
// first one, as a JSON pointer into the pipeline config document
// (/connectors/<index>/settings/sdk.batch.size or
// /dead-letter-queue/settings/sdk.batch.size). Remove sdk.batch.size (and
// sdk.batch.delay) from the destination, or run without
// --preview.pipeline-arch-v2 if the pipeline does not need record fan-out. See
// docs/operations/archv2-destination-batching.md.
var CodeArchV2DestinationBatchingUnsupported = conduiterr.Register("pipeline.archv2_destination_batching_unsupported", codes.FailedPrecondition)
