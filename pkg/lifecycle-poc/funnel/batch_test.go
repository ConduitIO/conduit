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
	"slices"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/matryer/is"
)

func TestNewBatch(t *testing.T) {
	is := is.New(t)
	records := randomRecords(2)
	batch := NewBatch(slices.Clone(records))

	is.Equal(batch.records, records)
	is.Equal(batch.recordStatuses, []RecordStatus{{Flag: RecordFlagAck}, {Flag: RecordFlagAck}})
	is.Equal(batch.positions, []opencdc.Position{opencdc.Position("0"), opencdc.Position("1")})
	is.Equal(batch.splitRecords, nil)
	is.True(!batch.tainted)
	is.Equal(0, batch.filterCount)
}

func TestBatch_Ack(t *testing.T) {
	is := is.New(t)
	records := randomRecords(2)
	batch := NewBatch(slices.Clone(records))

	batch.Retry(0)
	is.Equal(batch.recordStatuses[0].Flag, RecordFlagRetry)

	batch.Ack(0)
	is.Equal(batch.recordStatuses[0].Flag, RecordFlagAck)
}

func TestBatch_Nack(t *testing.T) {
	is := is.New(t)
	records := randomRecords(2)
	batch := NewBatch(slices.Clone(records))

	is.Equal(batch.recordStatuses[0].Flag, RecordFlagAck)
	is.True(!batch.tainted)

	wantErr := cerrors.New("test error")
	batch.Nack(0, wantErr)

	is.Equal(batch.recordStatuses[0].Flag, RecordFlagNack)
	is.Equal(batch.recordStatuses[0].Error, wantErr)
	is.True(batch.tainted)
}

func TestBatch_Retry(t *testing.T) {
	is := is.New(t)
	records := randomRecords(2)
	batch := NewBatch(slices.Clone(records))

	is.Equal(batch.recordStatuses[0].Flag, RecordFlagAck)
	is.True(!batch.tainted)

	batch.Retry(0)

	is.Equal(batch.recordStatuses[0].Flag, RecordFlagRetry)
	is.True(batch.tainted)
}

func TestBatch_Filter(t *testing.T) {
	is := is.New(t)
	records := randomRecords(2)
	batch := NewBatch(slices.Clone(records))

	is.Equal(batch.recordStatuses[0].Flag, RecordFlagAck)
	is.Equal(batch.filterCount, 0)
	batch.Filter(0)
	is.Equal(batch.recordStatuses[0].Flag, RecordFlagFilter)
	is.Equal(batch.filterCount, 1)
}

func TestBatch_SetRecords_Simple(t *testing.T) {
	is := is.New(t)
	records := randomRecords(2)
	batch := NewBatch(slices.Clone(records))

	newRecords := randomRecords(2)

	is.Equal(batch.records, records)
	// Overwrite all
	batch.SetRecords(0, newRecords)
	is.Equal(batch.records, newRecords)
	// Overwrite one
	batch.SetRecords(1, records[:1])
	is.Equal(batch.records, []opencdc.Record{newRecords[0], records[0]})
}

func TestBatch_SetRecords_Filtered(t *testing.T) {
	is := is.New(t)
	records := randomRecords(3)
	batch := NewBatch(slices.Clone(records))
	batch.Filter(1)

	newRecords := randomRecords(2)

	is.Equal(batch.records, records)
	// Overwrite all
	batch.SetRecords(0, newRecords)
	is.Equal(batch.ActiveRecords(), newRecords)
	// Overwrite one
	batch.SetRecords(1, records[:1])
	is.Equal(batch.ActiveRecords(), []opencdc.Record{newRecords[0], records[0]})
}

func TestBatch_SplitRecord(t *testing.T) {
	is := is.New(t)
	records := randomRecords(2)
	batch := NewBatch(slices.Clone(records))

	splitRecords := randomRecords(2)
	batch.SplitRecord(1, splitRecords)

	is.Equal(len(batch.records), 3)
	is.Equal(batch.records[1:], splitRecords)
	is.True(batch.positions[2] == nil)
	pos := records[1].Position.String()
	is.Equal(batch.splitRecords[pos], records[1])
}

func TestBatch_Nack_SplitRecord(t *testing.T) {
	is := is.New(t)
	records := randomRecords(2)
	batch := NewBatch(slices.Clone(records))

	splitRecords := randomRecords(2)
	batch.SplitRecord(1, splitRecords)

	wantErr := cerrors.New("test error")
	batch.Nack(1, wantErr) // Nack a split record

	is.Equal(batch.recordStatuses[1].Flag, RecordFlagNack)
	is.Equal(batch.recordStatuses[1].Error, wantErr)
	is.Equal(batch.recordStatuses[2].Flag, RecordFlagNack)
	is.Equal(batch.recordStatuses[2].Error, wantErr)
}

func TestBatch_HasActiveRecords(t *testing.T) {
	is := is.New(t)
	records := []opencdc.Record{{}, {}}
	batch := NewBatch(slices.Clone(records))

	is.True(batch.HasActiveRecords())
	batch.Filter(0)
	is.True(batch.HasActiveRecords())
	batch.Filter(0)
	is.True(!batch.HasActiveRecords())
}

func TestBatch_ActiveRecords(t *testing.T) {
	is := is.New(t)
	records := []opencdc.Record{
		{Position: opencdc.Position("foo")},
		{Position: opencdc.Position("bar")},
	}
	batch := NewBatch(slices.Clone(records))

	is.Equal(batch.ActiveRecords(), records)
	batch.Filter(0)
	is.Equal(batch.ActiveRecords(), records[1:])
}

func TestBatch_ActiveRecordIndices(t *testing.T) {
	is := is.New(t)
	records := []opencdc.Record{{}, {}}
	batch := NewBatch(slices.Clone(records))

	is.Equal(batch.activeRecordIndices(), nil)
	batch.Filter(0)
	is.Equal(batch.activeRecordIndices(), []int{1})
}

func TestBatch_OriginalBatch(t *testing.T) {
	is := is.New(t)
	records := randomRecords(1)
	batch := NewBatch(slices.Clone(records))
	wantOriginal := batch.clone()

	splitRecords := []opencdc.Record{{}, {}}
	batch.SplitRecord(0, splitRecords)

	gotOriginal := batch.originalBatch()
	is.Equal(wantOriginal, gotOriginal)
}

func TestBatch_Clone(t *testing.T) {
	is := is.New(t)
	records := []opencdc.Record{
		{Position: opencdc.Position("pos1")},
		{Position: opencdc.Position("pos2")},
		{Position: opencdc.Position("pos3")},
	}
	batch := NewBatch(slices.Clone(records))

	clonedBatch := batch.clone()
	is.Equal(batch.records, clonedBatch.records)
	is.Equal(batch.recordStatuses, clonedBatch.recordStatuses)
	is.Equal(batch.positions, clonedBatch.positions)
	is.Equal(batch.filterCount, clonedBatch.filterCount)
	is.Equal(batch.tainted, clonedBatch.tainted)

	// Filtering, nacking, splitting should affect the cloned batch
	nackErr := cerrors.New("test error")
	clonedBatch.Filter(0) // NB: indices are -1 from this point on
	clonedBatch.Nack(0, nackErr)
	clonedBatch.SplitRecord(1, []opencdc.Record{{}, {}})
	clonedBatch.Retry(2)

	is.True(clonedBatch.tainted)
	is.True(clonedBatch.filterCount == 1)
	is.Equal(clonedBatch.records, []opencdc.Record{
		{Position: opencdc.Position("pos1")},
		{Position: opencdc.Position("pos2")},
		{},
		{},
	})
	is.Equal(clonedBatch.recordStatuses, []RecordStatus{
		{Flag: RecordFlagFilter},
		{Flag: RecordFlagNack, Error: nackErr},
		{Flag: RecordFlagAck},
		{Flag: RecordFlagRetry},
	})
	is.Equal(clonedBatch.positions, []opencdc.Position{
		opencdc.Position("pos1"),
		opencdc.Position("pos2"),
		opencdc.Position("pos3"),
		nil,
	})
	is.Equal(clonedBatch.splitRecords, map[string]opencdc.Record{
		"pos3": records[2],
	})

	// It shouldn't affect the original batch
	is.True(!batch.tainted)
	is.True(batch.filterCount == 0)
	is.Equal(batch.records, records)
	is.Equal(batch.recordStatuses, []RecordStatus{
		{Flag: RecordFlagAck},
		{Flag: RecordFlagAck},
		{Flag: RecordFlagAck},
	})
	is.Equal(batch.positions, []opencdc.Position{
		opencdc.Position("pos1"),
		opencdc.Position("pos2"),
		opencdc.Position("pos3"),
	})
	is.Equal(batch.splitRecords, nil)
}
