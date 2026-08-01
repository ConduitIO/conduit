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

package funnel

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// CodeDuplicateSourcePosition is returned when a single batch read from a
// source contains two or more records carrying identical position bytes, and
// that batch is fanned out to multiple destinations.
//
// A position is a record's identity: multiAckNacker keys its per-position
// unanimity tally on it, and connector.Source.Ack advances the persisted
// position from it. Two records sharing one position make those two things
// ambiguous — the tally cannot tell the records apart, so one of them could
// never reach unanimity, and because positions are released to the source
// strictly in order (invariant 4), a single unresolvable slot silently stops
// the WHOLE batch from ever being acked. The pipeline would keep running,
// reading and writing records, while its source position never advanced and
// no error was ever surfaced.
//
// Failing loudly here is deliberate. At-least-once is not violated by the
// stall (nothing is acked, so nothing is lost — the records replay on
// restart), but a silent, unbounded liveness failure is far worse for an
// operator than a clear error naming the offending connector behaviour.
// Emitting duplicate positions within one batch is a connector contract
// violation, so this is a user-actionable error, not an internal assertion.
var CodeDuplicateSourcePosition = conduiterr.Register("pipeline.duplicate_source_position", codes.FailedPrecondition)

// CodeEmptySourcePosition is returned when a batch about to be acked to the
// source contains a record with an empty/nil position.
//
// connector.Source.Ack persists State.Position = p[len(p)-1] unconditionally,
// so acking an empty position OVERWRITES the durable source position with
// nothing: on restart the source resumes from an empty position — a full
// re-snapshot for Postgres, offset 0 for file/Kafka. That is an invariant-2
// (monotonic, crash-safe positions) violation.
//
// Unlike CodeDuplicateSourcePosition, this is usually NOT the source
// connector's fault. Batch.SplitRecord gives every piece after the first a nil
// position, so a sub-batch that covers only the tail of a split run collapses
// to nils — which happens when a later processor returns only part of a split
// run (a Retry/Filter that does not propagate across the run). The suggestion
// therefore points at the processor chain, not the source.
var CodeEmptySourcePosition = conduiterr.Register("pipeline.empty_source_position", codes.FailedPrecondition)

// CodeSplitRunStraddlesFanOut is returned when a batch reaching a
// multi-destination fan-out holds only part of a split run — i.e. a processor
// before the fan-out split a record, and a later pre-fan-out processor resolved
// only some of the resulting pieces (for example by returning fewer records
// than it received, which marks the remainder Retry).
//
// The run-completion ledger (see run_ledger.go) is per-Batch, and Batch.clone
// gives each branch its own copy so branches cannot race or bleed decisions
// into each other. That copy carries the run's whole-run member count, so a
// branch holding only part of the run can never complete it — and neither can
// the parent side. The run's original source position would then be withheld
// forever while later positions ack past it, silently skipping the record on
// restart.
//
// Failing loud is deliberate and restores the behaviour that predates the
// ledger: this shape already failed loud before (via CodeEmptySourcePosition on
// the nil-position tail), and silently withholding would have disarmed that
// backstop. Nothing is acked and nothing is lost — the records are redelivered
// on restart (invariant 3). Joining a run's tally across fan-out branches is
// possible but is deliberately not attempted here without a real use case.
var CodeSplitRunStraddlesFanOut = conduiterr.Register("pipeline.split_run_straddles_fanout", codes.FailedPrecondition)

// CodeRetryNotConverging is returned when Worker.doTask's RecordFlagRetry
// self-recursion (see worker.go's retryAttempt) detects that a processor is
// not making progress on a retried sub-batch, or has exceeded the hard
// iteration cap that backstops that detection.
//
// ProcessorTask.Do marks a sub-batch RecordFlagRetry whenever the processor
// returns fewer records than it received; Worker.doTask re-feeds that exact
// sub-batch to the same task. A processor whose short return is a
// deterministic function of its input (e.g. one that always skips records
// above a size threshold, or a rate-limit path that returns only what it
// could process and is handed the identical batch back) never resolves any
// of those records, so the recursion is a fixpoint: it would recurse forever,
// growing the goroutine stack without bound until it hit Go's 1GB limit and
// crashed the ENTIRE process - not just the offending pipeline - with an
// unrecoverable fatal error. See #2726.
//
// This is fixed loudly instead of silently: nothing in the un-converged
// sub-batch (or anything after it in the same batch - Worker.doTask's tainted
// loop never advances its cursor past a Retry group it recursed into) is ever
// acked or nacked before this error is returned, so every record in it is
// redelivered on restart (invariant 3). No source position is corrupted or
// skipped (invariant 2).
var CodeRetryNotConverging = conduiterr.Register("pipeline.retry_not_converging", codes.FailedPrecondition)
