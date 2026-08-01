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

// CodeSplitRunPartitioned is returned when Worker.subBatchByFlag is about to
// build a sub-batch whose boundary cuts through the middle of a split run
// (see Batch.SplitRecord) — some of the run's pieces inside the proposed
// sub-batch, the rest outside it.
//
// This should never happen: Batch.Retry, Batch.Filter and Batch.Nack all
// propagate their flag across a whole split run (splitRunFlagTier in
// batch.go), specifically so a run always carries one uniform flag and
// subBatchByFlag's same-flag grouping can never cut through the middle of
// it. If this error fires anyway, that guarantee has regressed — most likely
// a new flag-setting code path was added without routing it through the
// tiered propagation.
//
// This is a second line of defense behind that propagation, not a substitute
// for it: it exists because the alternative is the exact bug this whole
// mechanism was added to close (#2723) — a sub-batch covering only the head
// of a split run collapses (via Batch.originalBatch) to the original
// position and gets acked while the rest of the run has gone nowhere,
// violating invariants 1 and 3. Failing loud here, before that ack/nack call
// is ever made, converts a silent data-loss bug into a stopped pipeline with
// a coded, actionable error — the same trade CodeEmptySourcePosition makes
// for the nil-tail half of this shape.
var CodeSplitRunPartitioned = conduiterr.Register("pipeline.split_run_partitioned", codes.Internal)
