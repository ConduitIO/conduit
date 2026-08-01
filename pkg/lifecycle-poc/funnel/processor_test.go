// Copyright © 2025 Meroxa, Inc.
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
	"context"
	"slices"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestProcessorTask_Do_Passthrough(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	ctrl := gomock.NewController(t)
	processorMock := NewMockProcessor(ctrl)

	records := randomRecords(10)
	batch := NewBatch(records)
	processorMock.EXPECT().Process(ctx, records).Return(toProcessedRecords(records))

	task := NewProcessorTask("test", processorMock, logger, NoOpProcessorMetrics{})
	err := task.Do(ctx, batch)
	is.NoErr(err)

	is.Equal(batch.ActiveRecords(), records)
	for _, status := range batch.recordStatuses {
		is.Equal(status, RecordStatus{Flag: RecordFlagAck})
	}
}

func TestProcessorTask_Do_BatchWithFilteredRecords(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	ctrl := gomock.NewController(t)
	processorMock := NewMockProcessor(ctrl)

	records := randomRecords(10)
	batch := NewBatch(slices.Clone(records))

	// Records with index 0, 2, 3 and 9 are filtered out
	batch.Filter(9)    // 9
	batch.Filter(2, 4) // 2, 3
	batch.Filter(0)    // 0

	activeRecords := batch.ActiveRecords()
	is.Equal(activeRecords, []opencdc.Record{records[1], records[4], records[5], records[6], records[7], records[8]})

	multiRecord := randomRecords(3)

	wantErr := cerrors.New("error")
	processorMock.EXPECT().Process(ctx, activeRecords).Return(
		toProcessedRecords(
			activeRecords[:5],               // last record (index 8) is not processed and should be retried
			markFiltered(0),                 // index 1 is filtered
			markFiltered(2),                 // index 5 is filtered
			markMultiRecord(3, multiRecord), // index 6 is a multi-record
			markErrored(4, wantErr),         // index 7 is errored
		),
	)

	task := NewProcessorTask("test", processorMock, logger, NoOpProcessorMetrics{})
	err := task.Do(ctx, batch)
	is.NoErr(err)

	is.Equal(batch.ActiveRecords(), []opencdc.Record{records[4], multiRecord[0], multiRecord[1], multiRecord[2], records[7], records[8]})
	is.Equal(batch.recordStatuses, []RecordStatus{
		{Flag: RecordFlagFilter},               // 0
		{Flag: RecordFlagFilter},               // 1
		{Flag: RecordFlagFilter},               // 2
		{Flag: RecordFlagFilter},               // 3
		{Flag: RecordFlagAck},                  // 4
		{Flag: RecordFlagFilter},               // 5
		{Flag: RecordFlagAck},                  // 6 (multi-record 0)
		{Flag: RecordFlagAck},                  // 6 (multi-record 1)
		{Flag: RecordFlagAck},                  // 6 (multi-record 2)
		{Flag: RecordFlagNack, Error: wantErr}, // 7
		{Flag: RecordFlagRetry},                // 8
		{Flag: RecordFlagFilter},               // 9
	})
	is.Equal(batch.splitRecords, map[string]opencdc.Record{
		records[6].Position.String(): records[6],
	})
	is.Equal(batch.filterCount, 6)
}

func TestProcessorTask_Do_MultiRecord(t *testing.T) {
	ctx := context.Background()
	logger := log.Test(t)

	ctrl := gomock.NewController(t)
	processorMock := NewMockProcessor(ctrl)
	task := NewProcessorTask("test", processorMock, logger, NoOpProcessorMetrics{})

	records := randomRecords(5)
	batch := NewBatch(slices.Clone(records))

	t.Run("MultiRecord with 0 records filters the record", func(t *testing.T) {
		is := is.New(t)
		batch := batch.clone()

		processorMock.EXPECT().Process(ctx, batch.records).Return(
			toProcessedRecords(
				batch.records,
				markMultiRecord(0, []opencdc.Record{}),
			),
		)

		err := task.Do(ctx, batch)
		is.NoErr(err)

		is.Equal(batch.ActiveRecords(), []opencdc.Record{records[1], records[2], records[3], records[4]})
		is.Equal(batch.recordStatuses, []RecordStatus{
			{Flag: RecordFlagFilter}, // 0
			{Flag: RecordFlagAck},    // 1
			{Flag: RecordFlagAck},    // 2
			{Flag: RecordFlagAck},    // 3
			{Flag: RecordFlagAck},    // 4
		})
	})

	t.Run("MultiRecord with 1 record sets the record", func(t *testing.T) {
		is := is.New(t)
		batch := batch.clone()

		wantRecord := randomRecords(1)[0]
		processorMock.EXPECT().Process(ctx, batch.records).Return(
			toProcessedRecords(
				batch.records,
				markMultiRecord(0, []opencdc.Record{wantRecord}),
			),
		)

		err := task.Do(ctx, batch)
		is.NoErr(err)

		is.Equal(batch.ActiveRecords(), []opencdc.Record{wantRecord, records[1], records[2], records[3], records[4]})
		is.Equal(batch.recordStatuses, []RecordStatus{
			{Flag: RecordFlagAck}, // 0
			{Flag: RecordFlagAck}, // 1
			{Flag: RecordFlagAck}, // 2
			{Flag: RecordFlagAck}, // 3
			{Flag: RecordFlagAck}, // 4
		})
	})

	t.Run("MultiRecord with >1 records splits the record", func(t *testing.T) {
		is := is.New(t)
		batch := batch.clone()

		wantRecords := randomRecords(2)
		processorMock.EXPECT().Process(ctx, batch.records).Return(
			toProcessedRecords(
				batch.records,
				markMultiRecord(0, wantRecords),
			),
		)

		err := task.Do(ctx, batch)
		is.NoErr(err)

		is.Equal(batch.ActiveRecords(), []opencdc.Record{wantRecords[0], wantRecords[1], records[1], records[2], records[3], records[4]})
		is.Equal(batch.recordStatuses, []RecordStatus{
			{Flag: RecordFlagAck}, // 0 (MultiRecord 0)
			{Flag: RecordFlagAck}, // 0 (MultiRecord 1)
			{Flag: RecordFlagAck}, // 1
			{Flag: RecordFlagAck}, // 2
			{Flag: RecordFlagAck}, // 3
			{Flag: RecordFlagAck}, // 4
		})
	})
}

// TestProcessorTask_Do_MultiRecord_EmptyThenSplit_NoPanic is the direct
// regression test for #2728's shape 1: a processor returns
// [MultiRecord{}, MultiRecord{a,b}] for a 2-record input batch.
//
// markBatchRecords used to mix b.Filter(from+i) and b.SplitRecord(from+i, ...)
// in a single FORWARD pass over the MultiRecord range. Filtering the first
// output record shrank activeRecordIndices() by one before the SECOND output
// record's SplitRecord call resolved its own (now stale) index against it -
// index out of range, panicking the whole process. Iterating end->start (this
// fix) means the split (the higher index) happens before the filter (the
// lower index), so the filter's shrink can never be observed by an index that
// was already resolved and acted on.
func TestProcessorTask_Do_MultiRecord_EmptyThenSplit_NoPanic(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	ctrl := gomock.NewController(t)
	processorMock := NewMockProcessor(ctrl)
	task := NewProcessorTask("test", processorMock, logger, NoOpProcessorMetrics{})

	records := randomRecords(2)
	batch := NewBatch(slices.Clone(records))
	splitInto := randomRecords(2)

	processorMock.EXPECT().Process(ctx, batch.records).Return(
		toProcessedRecords(
			batch.records,
			markMultiRecord(0, []opencdc.Record{}),
			markMultiRecord(1, splitInto),
		),
	)

	err := task.Do(ctx, batch)
	is.NoErr(err) // must not panic

	is.Equal(batch.ActiveRecords(), splitInto)
	is.Equal(batch.recordStatuses, []RecordStatus{
		{Flag: RecordFlagFilter}, // record 0: filtered (empty MultiRecord)
		{Flag: RecordFlagAck},    // record 1, split piece 0
		{Flag: RecordFlagAck},    // record 1, split piece 1
	})
	is.Equal(batch.filterCount, 1)
}

// TestProcessorTask_Do_MultiRecord_SplitThenEmpty_FiltersCorrectRecord is the
// direct regression test for #2728's shape 2: a processor returns
// [MultiRecord{a,b}, MultiRecord{}] for a 2-record input batch.
//
// Forward iteration used to split the first output record BEFORE filtering
// the second, so the filter's index (resolved via activeRecordIndices(),
// which is nil - i.e. "use raw physical indices" - only until the FIRST
// mutation) landed on one of the freshly-inserted split pieces instead of the
// second original record: the record the processor meant to filter stayed Ack
// and was delivered, while an unrelated split piece was silently dropped
// (silent data loss - the confirmed #2728 symptom). Iterating end->start
// filters the second record first (while it is still the raw, un-shifted
// index) and only then splits the first.
func TestProcessorTask_Do_MultiRecord_SplitThenEmpty_FiltersCorrectRecord(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	ctrl := gomock.NewController(t)
	processorMock := NewMockProcessor(ctrl)
	task := NewProcessorTask("test", processorMock, logger, NoOpProcessorMetrics{})

	records := randomRecords(2)
	batch := NewBatch(slices.Clone(records))
	splitInto := randomRecords(2)

	processorMock.EXPECT().Process(ctx, batch.records).Return(
		toProcessedRecords(
			batch.records,
			markMultiRecord(0, splitInto),
			markMultiRecord(1, []opencdc.Record{}),
		),
	)

	err := task.Do(ctx, batch)
	is.NoErr(err)

	// The intended record (records[1]) is the one filtered; both split
	// pieces of records[0] are active and delivered - NOT the buggy
	// "flags=[ack filter ack]" outcome the issue reported.
	is.Equal(batch.ActiveRecords(), splitInto)
	is.Equal(batch.recordStatuses, []RecordStatus{
		{Flag: RecordFlagAck},    // record 0, split piece 0
		{Flag: RecordFlagAck},    // record 0, split piece 1
		{Flag: RecordFlagFilter}, // record 1: filtered (empty MultiRecord)
	})
	is.Equal(batch.filterCount, 1)
	is.Equal(batch.positions, []opencdc.Position{records[0].Position, nil, records[1].Position})
}

// TestProcessorTask_Do_MultiRecord_PreFilteredBatch_MarksCorrectRecords covers
// the shape adversarial review found untested: a batch that ALREADY has
// filtered records when it reaches markBatchRecords.
//
// Both existing #2728 regression tests start from a fresh, unfiltered batch, so
// activeRecordIndices() returns nil on entry and active indices happen to equal
// physical ones. The full-pipeline property test cannot produce this shape
// either — it drives a single processor over a fresh batch. But a two-processor
// chain reaches here with filterCount > 0, which is when the active→physical
// mapping is non-trivial and an index-shift bug actually bites.
//
// Here record 1 is filtered by an earlier stage, then the processor returns
// [empty, MultiRecord{a,b}, empty] for the three REMAINING active records. On
// main the wrong record was split; end→start marking puts every decision on the
// record the processor actually chose.
func TestProcessorTask_Do_MultiRecord_PreFilteredBatch_MarksCorrectRecords(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	ctrl := gomock.NewController(t)
	processorMock := NewMockProcessor(ctrl)
	task := NewProcessorTask("test", processorMock, logger, NoOpProcessorMetrics{})

	records := randomRecords(4)
	batch := NewBatch(slices.Clone(records))

	// An earlier stage filtered record 1: active is now [0, 2, 3].
	batch.Filter(1)
	is.Equal(batch.filterCount, 1)
	is.Equal(len(batch.ActiveRecords()), 3)

	splitInto := randomRecords(2)
	active := slices.Clone(batch.ActiveRecords())
	processorMock.EXPECT().Process(ctx, active).Return(
		toProcessedRecords(
			active,
			markMultiRecord(0, []opencdc.Record{}), // filter active[0] == record 0
			markMultiRecord(1, splitInto),          // split  active[1] == record 2
			markMultiRecord(2, []opencdc.Record{}), // filter active[2] == record 3
		),
	)

	is.NoErr(task.Do(ctx, batch))

	// Record 2 is the one split; records 0 and 3 are the ones filtered. The
	// pre-existing filter on record 1 is untouched.
	is.Equal(batch.ActiveRecords(), splitInto)
	is.Equal(batch.recordStatuses, []RecordStatus{
		{Flag: RecordFlagFilter}, // record 0: filtered by this pass
		{Flag: RecordFlagFilter}, // record 1: filtered earlier, untouched
		{Flag: RecordFlagAck},    // record 2: split piece 0
		{Flag: RecordFlagAck},    // record 2: split piece 1
		{Flag: RecordFlagFilter}, // record 3: filtered by this pass
	})
	is.Equal(batch.filterCount, 3)
	is.Equal(batch.positions, []opencdc.Position{
		records[0].Position, records[1].Position, records[2].Position, nil, records[3].Position,
	})
}
