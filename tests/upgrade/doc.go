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

// Package upgrade contains the batch-shape upgrade-coverage gate: v0.20
// flips Preview.PipelineArchV2 to default, so every existing user's
// pipelines change engines on upgrade. v1 (pkg/lifecycle/stream) acks one
// record at a time (see source_acker.go's registerAckHandler:
// `Source.Ack(ctx, []opencdc.Position{msg.Record.Position})`); v2
// (pkg/lifecycle-poc/funnel) acks a whole batch at once (worker.go's Ack:
// `w.Source.Ack(ctx, originalBatch.positions)`), and
// pkg/connector/source.go's Source.Ack persists only p[len(p)-1] - the last
// position in whatever slice it's given, regardless of engine.
//
// Coarse (batch) checkpointing introduces no NEW failure mode by itself: v1
// depends on exactly the same self-sufficient-position contract (each Ack
// call must cover a position such that everything up to and including it is
// truly durable), so this is not a v2 regression. What batching DOES do is
// amplify the blast radius of every batch-accounting bug: where v1 can only
// ever lose (skip past) the ONE record whose own Ack call is wrong, v2 can
// advance the durable position past an entire unhandled batch remainder in
// a single bad Ack call. That amplification, not any single new failure
// mode, is why this gate exists and why it is organized around batch SHAPES
// rather than a happy-path record count.
//
// This is not theoretical. Every one of these was a real, merged bug in
// this engine, each found by adversarial review rather than by a test:
//
//   - #2722 - a sub-batch span-accounting overshoot in Worker.doTaskAttempt
//     (worker.go) skipped records AND advanced the persisted position past
//     them: "silent, permanent record loss with no error and no gap in the
//     position sequence" (the fix commit's own words). Triggered by a task
//     GROWING a sub-batch via Batch.SplitRecord inside a RecordFlagRetry
//     recursion - see TestV2Combo_RetryThenSplit.
//   - #2723/#2730 - a split run's head could be acked before its tail was
//     even delivered, if a later task or fan-out resolved the pieces out of
//     order. Fixed by run_ledger.go's runAckNacker, which withholds a split
//     run's original position from the parent acker until every member is
//     terminal - see TestV2Split_ResumeCorrectness and
//     TestV2Combo_SplitFanOut.
//   - #2726 - unbounded retry recursion for a non-converging processor;
//     bounded by worker.go's maxRetryAttempts/maxRetryStall.
//   - #2728/#2729 - forward-iterating marking loops in
//     ProcessorTask.markBatchRecords (processor.go) and
//     DestinationTask.markBatchRecords (destination.go) shifted indices
//     mid-pass as an earlier mutation changed the active-record set,
//     misattributing a nack to the wrong physical record - in the worst
//     case acking a destination-failed record as a success.
//
// A happy-path upgrade matrix - "run N records through both engines, assert
// they all arrive" - would have sailed straight through every one of them.
// That is why this gate is organized around batch shapes, not record counts:
// filter (some records filtered mid-batch), retry (a processor returning
// fewer records than it received), DLQ-nack (a record nacked and routed to
// the DLQ, or halting the pipeline if the DLQ is disabled), split (a
// processor emitting sdk.MultiRecord), fan-out (M destinations - v2 only;
// see shapes_v1_test.go for why sdk.MultiRecord, and therefore both split
// and fan-out, is fatal on v1 by design), and combinations of the above.
//
// For each shape: drive one designed batch through the engine, and assert
// (a) every record is delivered at least once - gaps forbidden, duplicates
// allowed - and (b) the persisted source position never advances past a
// record that was not durably handled. Where a shape leaves records
// unhandled (the DLQ-halt cases), a modeled restart (sourceHarness.restart)
// proves the source genuinely resumes from the persisted position and
// redelivers exactly what was never handled - never skipping it, per
// seqPlugin's genuine (non-generator) resume semantics.
//
// # Non-vacuity
//
// This suite's ability to catch the bugs it exists for was verified by hand
// during development (not something this package re-runs in CI - that would
// mean shipping the bug on the green path):
//
//  1. Re-introduced #2722's span-overshoot in
//     pkg/lifecycle-poc/funnel/worker.go's doTaskAttempt (reverted the
//     captured-span fix back to `idx += len(subBatch.positions)`, i.e.
//     advancing by the POST-call, possibly-grown sub-batch length instead of
//     the span captured before the call). TestV2Combo_RetryThenSplit went
//     RED. Reverted.
//  2. Reverted run_ledger.go's runAckNacker withholding (made
//     newRunAckNacker's Ack/Nack forward every record to the parent
//     immediately, bypassing the run-completion vote entirely - the #2723
//     fix undone). TestV2Combo_SplitFanOut went RED. Reverted.
//
// See the PR description for the verbatim failure output from both runs.
//
// # Why not conduit-connector-generator
//
// generator's Source.Open discards its resume position argument entirely,
// so any resume assertion built on it passes vacuously regardless of what
// the engine actually did. This suite uses seqPlugin (plugin.go) instead: a
// small, purpose-built, in-process pconnector.SourcePlugin that genuinely
// honors its resume position (Open seeks to the record immediately after
// it), wrapped in a REAL pkg/connector.Source backed by a real on-disk
// badger DB and connector.Persister - the actual production durability path
// under test, not a stand-in for it.
package upgrade
