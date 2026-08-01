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
var CodeStopAndWaitTimeout = conduiterr.Register("lifecycle_v2.stop_and_wait_timeout", codes.DeadlineExceeded)

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
var CodePartialGracefulStopEscalated = conduiterr.Register("lifecycle_v2.partial_graceful_stop_escalated", codes.Aborted)
