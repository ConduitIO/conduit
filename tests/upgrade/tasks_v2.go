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

package upgrade

import (
	"context"
	"fmt"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/lifecycle-poc/funnel"
)

// filterAtTask marks every record at the given (ActiveRecords-relative,
// 0-based) index as filtered. It is the first task in every pipeline it's
// used in, so ActiveRecords-relative indices equal raw record indices.
type filterAtTask struct {
	id      string
	indices map[int]bool
}

func (t *filterAtTask) ID() string                  { return t.id }
func (t *filterAtTask) Open(context.Context) error  { return nil }
func (t *filterAtTask) Close(context.Context) error { return nil }
func (t *filterAtTask) Do(_ context.Context, b *funnel.Batch) error {
	for i := range b.ActiveRecords() {
		if t.indices[i] {
			b.Filter(i)
		}
	}
	return nil
}

// nackAtTask marks every record at the given index as nacked with a
// synthetic error, routing it through the DLQ (or halting the pipeline,
// depending on the DLQ policy the worker was built with).
type nackAtTask struct {
	id      string
	indices map[int]bool
}

func (t *nackAtTask) ID() string                  { return t.id }
func (t *nackAtTask) Open(context.Context) error  { return nil }
func (t *nackAtTask) Close(context.Context) error { return nil }
func (t *nackAtTask) Do(_ context.Context, b *funnel.Batch) error {
	for i, r := range b.ActiveRecords() {
		if t.indices[i] {
			b.Nack(i, fmt.Errorf("synthetic nack at position %s", r.Position))
		}
	}
	return nil
}

// filterPosTask marks every record whose position is in the given set as
// filtered. Unlike filterAtTask (which indexes into ActiveRecords() at call
// time - only safe as the very first task in a chain), filterPosTask
// resolves by position, so it composes correctly after an earlier task in
// the same chain has already shrunk the active set (e.g. filter+nack
// combinations - see combos_test.go).
type filterPosTask struct {
	id        string
	positions map[string]bool
}

func (t *filterPosTask) ID() string                  { return t.id }
func (t *filterPosTask) Open(context.Context) error  { return nil }
func (t *filterPosTask) Close(context.Context) error { return nil }
func (t *filterPosTask) Do(_ context.Context, b *funnel.Batch) error {
	for i, r := range b.ActiveRecords() {
		if t.positions[string(r.Position)] {
			b.Filter(i)
		}
	}
	return nil
}

// nackPosTask marks every record whose position is in the given set as
// nacked. Position-resolving counterpart to nackAtTask - see filterPosTask's
// doc comment.
type nackPosTask struct {
	id        string
	positions map[string]bool
}

func (t *nackPosTask) ID() string                  { return t.id }
func (t *nackPosTask) Open(context.Context) error  { return nil }
func (t *nackPosTask) Close(context.Context) error { return nil }
func (t *nackPosTask) Do(_ context.Context, b *funnel.Batch) error {
	for i, r := range b.ActiveRecords() {
		if t.positions[string(r.Position)] {
			b.Nack(i, fmt.Errorf("synthetic nack at position %s", r.Position))
		}
	}
	return nil
}

// retryRangeOnceTask models a processor that returns fewer records than it
// received (retry), then converges normally on redelivery: on the FIRST
// pass over the whole batch it marks [from, to) for retry; every later call
// (the retry sub-batch doTask recurses with) leaves the batch untouched,
// which - since Worker.doTaskAttempt's RecordFlagRetry branch pre-marks the
// retried sub-batch Ack before recursing (worker.go) - means it converges to
// a clean ack after exactly one retry round.
type retryRangeOnceTask struct {
	id       string
	from, to int
	seen     bool
}

func (t *retryRangeOnceTask) ID() string                  { return t.id }
func (t *retryRangeOnceTask) Open(context.Context) error  { return nil }
func (t *retryRangeOnceTask) Close(context.Context) error { return nil }
func (t *retryRangeOnceTask) Do(_ context.Context, b *funnel.Batch) error {
	if !t.seen {
		t.seen = true
		b.Retry(t.from, t.to)
	}
	return nil
}

// retryPosOnceTask marks every record whose position is in the given set
// for retry on the FIRST pass over the batch, then converges (leaves the
// retried sub-batch untouched, defaulting to Ack) on the pass doTaskAttempt's
// RecordFlagRetry recursion feeds it. Position-resolving counterpart to
// retryRangeOnceTask - see filterPosTask's doc comment for why that matters
// when this runs after an earlier task in the same chain (e.g. splitAtTask)
// has already changed the batch shape.
type retryPosOnceTask struct {
	id        string
	positions map[string]bool
	seen      bool
}

func (t *retryPosOnceTask) ID() string                  { return t.id }
func (t *retryPosOnceTask) Open(context.Context) error  { return nil }
func (t *retryPosOnceTask) Close(context.Context) error { return nil }
func (t *retryPosOnceTask) Do(_ context.Context, b *funnel.Batch) error {
	if t.seen {
		return nil
	}
	t.seen = true
	for i, r := range b.ActiveRecords() {
		if t.positions[string(r.Position)] {
			b.Retry(i, i+1)
		}
	}
	return nil
}

// retryThenSplitTask marks one record Retry on its first pass, then on the
// retry pass splits that record into several pieces - exactly the shape
// that made Worker.doTaskAttempt's tainted-loop cursor overshoot before
// #2724 (issue #2722): a task GROWING the sub-batch it was handed via
// Batch.SplitRecord, inside a RecordFlagRetry recursion. Ported from
// pkg/lifecycle-poc/funnel/worker_retry_span_test.go's identically-named
// type so this suite reproduces the exact shape through the REAL
// persisted-position path (a real *connector.Source, not a fake
// ackNacker) - see shapes_v2_test.go's TestV2Combo_RetryThenSplit.
type retryThenSplitTask struct {
	id         string
	retryIndex int
	splitInto  int
	seen       bool
}

func (t *retryThenSplitTask) ID() string                  { return t.id }
func (t *retryThenSplitTask) Open(context.Context) error  { return nil }
func (t *retryThenSplitTask) Close(context.Context) error { return nil }
func (t *retryThenSplitTask) Do(_ context.Context, b *funnel.Batch) error {
	if !t.seen {
		t.seen = true
		b.Retry(t.retryIndex, t.retryIndex+1)
		return nil
	}
	active := b.ActiveRecords()
	if len(active) == 1 {
		pieces := splitPieces(active[0], t.splitInto)
		b.SplitRecord(0, pieces)
	}
	return nil
}

// splitAtTask splits the record at the given (ActiveRecords-relative) index
// into `into` pieces, each carrying a distinct Position (the original
// position plus a letter suffix) so delivered pieces are individually
// identifiable in test assertions, while Batch's own (separate) position
// tracking still attributes the whole run back to the one original source
// position - see batch.go's positions/runs field docs.
type splitAtTask struct {
	id    string
	index int
	into  int
}

func (t *splitAtTask) ID() string                  { return t.id }
func (t *splitAtTask) Open(context.Context) error  { return nil }
func (t *splitAtTask) Close(context.Context) error { return nil }
func (t *splitAtTask) Do(_ context.Context, b *funnel.Batch) error {
	active := b.ActiveRecords()
	pieces := splitPieces(active[t.index], t.into)
	b.SplitRecord(t.index, pieces)
	return nil
}

// splitPieces builds `into` distinct copies of orig, each with the original
// position plus a letter suffix (so "2" splits into "2a", "2b", "2c", ...).
func splitPieces(orig opencdc.Record, into int) []opencdc.Record {
	pieces := make([]opencdc.Record, into)
	for i := range pieces {
		piece := orig.Clone()
		piece.Position = append(append(opencdc.Position(nil), orig.Position...), byte('a'+i))
		pieces[i] = piece
	}
	return pieces
}
