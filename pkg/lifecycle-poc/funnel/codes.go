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

var (
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
	CodeDuplicateSourcePosition = conduiterr.Register("pipeline.duplicate_source_position", codes.FailedPrecondition)

	// CodeEmptySourcePosition is returned when a batch about to be acked to the
	// source contains a record with an empty/nil position.
	//
	// connector.Source.Ack persists State.Position = p[len(p)-1] unconditionally,
	// so acking an empty position OVERWRITES the durable source position with
	// nothing: on restart the source resumes from an empty position — a full
	// re-snapshot for Postgres, offset 0 for file/Kafka. That is an invariant-2
	// (monotonic, crash-safe positions) violation.
	//
	// There are two distinct causes, and the suggestion names both because
	// they call for different fixes.
	//
	// The one reachable today is a SOURCE CONNECTOR emitting a record with an
	// empty position, which is then acked or nacked. Nothing in the read path
	// validates positions, so it reaches Worker.Ack/Worker.Nack unaltered. Like
	// CodeDuplicateSourcePosition, that is a connector contract violation —
	// every record must carry a distinct, non-empty position.
	//
	// The second is a processor-chain shape: Batch.SplitRecord gives every piece
	// after the first a nil position, so a sub-batch covering only the tail of a
	// split run collapses to nils. On the ack path this is reachable; on the
	// nack path the run ledger currently withholds split members and forwards
	// the run's own non-nil origPos instead, so the guard there is a contract
	// check rather than a live route.
	CodeEmptySourcePosition = conduiterr.Register("pipeline.empty_source_position", codes.FailedPrecondition)

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
	CodeSplitRunStraddlesFanOut = conduiterr.Register("pipeline.split_run_straddles_fanout", codes.FailedPrecondition)

	// CodeRetryNotConverging is returned when Worker.doTask's RecordFlagRetry
	// self-recursion (see worker.go's retryAttempt) detects that a processor is
	// not making progress on a retried sub-batch, or has exceeded the hard
	// iteration cap that backstops that detection.
	//
	// ProcessorTask.Do marks a sub-batch RecordFlagRetry for exactly those
	// records the processor returned NO result for, then Worker.doTask re-feeds
	// that sub-batch to the same task. If the same records come back unresolved
	// again and again, the recursion is a fixpoint: it would recurse forever,
	// growing the goroutine stack without bound until it hit Go's 1GB limit and
	// crashed the ENTIRE process - not just the offending pipeline - with an
	// unrecoverable fatal error. See #2726.
	//
	// What actually triggers this, and what does NOT: a processor that simply
	// returns FEWER records than it received is NOT this condition. ProcessorTask.Do
	// rejects an empty result and pads exactly len(in)-len(out) entries, so such a
	// processor shrinks the retry group every round and converges normally - a size
	// threshold or a rate-limit path that returns partial results is fine. The
	// reachable trigger is a record that comes back with no result AT ALL: for a
	// standalone (WASM) processor, a ProcessedRecord whose Record oneof is unset,
	// which in practice means a malformed or version-mismatched plugin.
	//
	// This is fixed loudly instead of silently: nothing in the un-converged
	// sub-batch (or anything after it in the same batch - Worker.doTask's tainted
	// loop never advances its cursor past a Retry group it recursed into) is ever
	// acked or nacked before this error is returned, so every record in it is
	// redelivered on restart (invariant 3). No source position is corrupted or
	// skipped (invariant 2).
	CodeRetryNotConverging = conduiterr.Register("pipeline.retry_not_converging", codes.FailedPrecondition)

	// CodeSharedDestinationPoisoned is returned when a worker in an N-source
	// pipeline (slice 3b) tries to enter a shared destination subtree that a
	// PREVIOUS pass already poisoned, after an error left the shared
	// destination's single gRPC ack stream desynchronized. See Worker.doTask's
	// poisoning store/check for the mechanics, and the adversarial-review
	// finding this closes (H2, docs/design-documents/20260801-archv2-multiconnector-nsource.md).
	//
	// This is a Tier-1 invariant-1 backstop, not merely retry hygiene.
	// DestinationTask.Do writes a batch and reads back exactly that many acks
	// off the destination's single gRPC stream, matching each ack to the
	// position it just sent in strict send order; if Do returns early on an
	// Ack() error, whatever acks were still queued behind it are never read by
	// THIS pass. Without this check, the very next worker to acquire the shared
	// boundary's mutex would read those leftover acks as if they were its own -
	// and connector-defined positions are only unique WITHIN a source, so two
	// sources emitting byte-identical position bytes (routine: two file
	// sources, two offset-based connectors, ...) would make validateAcks'
	// byte-for-byte comparison PASS, silently acking records upstream that this
	// worker's own destination write never durably confirmed.
	//
	// Recovering from this requires a full pipeline restart (a fresh Sink, a
	// fresh gRPC stream, and therefore a fresh, unpoisoned TaskNode) - which is
	// exactly what happens once this error propagates through
	// Worker.Do/lifecycle-poc.Service.runPipeline's existing tomb-kill and
	// recovery/degrade classification, so no special-casing is needed beyond
	// returning this error from doTask.
	CodeSharedDestinationPoisoned = conduiterr.Register("pipeline.shared_destination_poisoned", codes.Aborted)
)
