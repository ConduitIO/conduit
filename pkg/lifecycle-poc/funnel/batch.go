// Copyright © 2024 Meroxa, Inc.
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

//go:generate stringer -type=RecordFlag -linecomment

package funnel

import (
	"fmt"
	"slices"

	"github.com/conduitio/conduit-commons/opencdc"
)

// Batch represents a batch of records that are processed together. It keeps
// track of the status of each record in the batch, and provides methods to
// update the status of records.
type Batch struct {
	records        []opencdc.Record
	recordStatuses []RecordStatus
	// positions is a slice of positions for the records in the batch. The
	// positions are used to ack or nack records in the source. If a record
	// was split, its position is nil. Split records are always consecutive, so
	// we can use the first record's position to ack or nack the split records.
	positions []opencdc.Position

	// filterCount is updated any time a record is marked as filtered, to make it
	// easier to construct the set of active records.
	filterCount int

	// If a batch is tainted it means that parts need to be either nacked or
	// retried. Such a batch needs to be split into multiple batches, each
	// containing only records with the same status (filtered counts as acked).
	tainted bool

	// splitRecords is a map of split records, where the key is the record
	// position converted to a string. The value is the original record itself.
	// This is used to keep track of the original record when splitting it into
	// multiple records. The position is nil for split records, except the first
	// record, which contains the position of the original record.
	splitRecords map[string]opencdc.Record
}

func NewBatch(records []opencdc.Record) *Batch {
	// Store positions separately, as we need the original positions when acking
	// records in the source, in case a processor tries to change the position.
	positions := make([]opencdc.Position, len(records))
	for i, r := range records {
		positions[i] = r.Position
	}

	return &Batch{
		records:        records,
		recordStatuses: make([]RecordStatus, len(records)),
		positions:      positions,
		filterCount:    0,
		tainted:        false,
	}
}

// Ack marks the record at index i as acked. If a second index is provided, all
// records between i (included) and j (excluded) are marked as acked. If multiple
// indices are provided, the method panics.
// Records are marked as acked by default, so this method is only useful when
// reprocessing records marked to be retried.
func (b *Batch) Ack(i int, j ...int) {
	b.setFlagNoErr(RecordFlagAck, i, j...)
}

// Nack marks the record at index i as nacked. If multiple errors are provided,
// they are assigned to the records starting at index i.
func (b *Batch) Nack(i int, errs ...error) {
	b.setFlagWithErr(RecordFlagNack, i, errs)
	b.tainted = true

	if len(b.splitRecords) > 0 {
		// If the batch contains split records, we need to set the flag for all
		// records in the split record, so they are all marked as nacked.
		for j, pos := range b.positions[i : i+len(errs)] {
			if _, ok := b.splitRecords[pos.String()]; pos != nil && !ok {
				continue // This is not a split record.
			}
			from, to := i, i
			for from > 0 && b.positions[from] == nil {
				// Find the first record that has a position. This is the first
				// record that was split from the original record.
				from--
			}
			for to < len(b.positions) && b.positions[to] == nil {
				// Find the last record that has a position. This is the last
				// record that was split from the original record.
				to++
			}

			// We found a range of split records, we need to mark them all as
			// nacked. Copy the error so it's set for all records in the range.
			splitErrs := make([]error, to-from)
			for k := range splitErrs {
				splitErrs[k] = errs[j]
			}
			b.setFlagWithErr(RecordFlagNack, from, splitErrs)
		}
	}
}

// Retry marks the record at index i to be retried. If a second index is
// provided, all records between i (included) and j (excluded) are marked to be
// retried. If multiple indices are provided, the method panics.
func (b *Batch) Retry(i int, j ...int) {
	b.setFlagNoErr(RecordFlagRetry, i, j...)
	b.tainted = true
}

// Filter marks the record at index i as filtered out. If a second index is
// provided, all records between i (included) and j (excluded) are marked as
// filtered. If multiple indices are provided, the method panics.
func (b *Batch) Filter(i int, j ...int) {
	b.setFlagNoErr(RecordFlagFilter, i, j...)
	end := i + 1
	if len(j) == 1 {
		end = j[0]
	}
	b.filterCount += end - i
}

// SetRecords replaces the records in the batch starting at index i with the
// provided records. If recs contains n records, indices i to i+n-1 are replaced.
func (b *Batch) SetRecords(i int, recs []opencdc.Record) {
	copy(b.records[i:], recs)
}

// SplitRecord splits the record at index i into the provided records. The
// records replace the record at the index i, and the rest of the records are
// shifted to the right.
func (b *Batch) SplitRecord(i int, recs []opencdc.Record) {
	if b.splitRecords == nil {
		b.splitRecords = make(map[string]opencdc.Record)
	}
	if _, ok := b.splitRecords[b.records[i].Position.String()]; b.records[i].Position != nil && !ok {
		// We're splitting an original record, so we need to store it in the
		// split records map. If any of the split records get nacked, we need to
		// send the original to the DLQ.
		b.splitRecords[b.records[i].Position.String()] = b.records[i]
	}

	b.records = append(b.records[:i], append(recs, b.records[i+1:]...)...)

	// Extend the record statuses slice to accommodate the new records. By default
	// the new records are marked as acked. The original record must have been
	// acked, otherwise the processor wouldn't receive it.
	b.recordStatuses = append(
		b.recordStatuses[:i+1],
		append(make([]RecordStatus, len(recs)-1), b.recordStatuses[i+1:]...)...,
	)

	// Extend the positions slice to accommodate the new records. The new records
	// get a nil position, which indicates that they were split from the original
	// record.
	b.positions = append(
		b.positions[:i+1],
		append(make([]opencdc.Position, len(recs)-1), b.positions[i+1:]...)...,
	)
}

func (b *Batch) setFlagNoErr(f RecordFlag, i int, j ...int) {
	switch len(j) {
	case 0:
		b.recordStatuses[i].Flag = f
		b.recordStatuses[i].Error = nil
	case 1:
		if i >= j[0] {
			panic(fmt.Sprintf("invalid range (%d >= %d)", i, j[0]))
		}
		for k := i; k < j[0]; k++ {
			b.recordStatuses[k].Flag = f
			b.recordStatuses[k].Error = nil
		}
	default:
		panic(fmt.Sprintf("too many arguments (%d)", len(j)))
	}
}

func (b *Batch) setFlagWithErr(f RecordFlag, i int, errs []error) {
	for k, err := range errs {
		b.recordStatuses[i+k].Flag = f
		b.recordStatuses[i+k].Error = err
	}
}

func (b *Batch) clone() *Batch {
	records := make([]opencdc.Record, len(b.records))
	for i, r := range b.records {
		records[i] = r.Clone()
	}

	return &Batch{
		records:        records,
		recordStatuses: slices.Clone(b.recordStatuses),
		positions:      b.positions,
		tainted:        b.tainted,
		filterCount:    b.filterCount,
	}
}

func (b *Batch) sub(from, to int) *Batch {
	filterCount := 0
	if b.filterCount > 0 {
		for _, status := range b.recordStatuses[from:to] {
			if status.Flag == RecordFlagFilter {
				filterCount++
			}
		}
	}
	var splitRecords map[string]opencdc.Record
	if len(b.splitRecords) != 0 {
		splitRecords = make(map[string]opencdc.Record)
		for _, pos := range b.positions[from:to] {
			if rec, ok := b.splitRecords[pos.String()]; ok {
				splitRecords[pos.String()] = rec
			}
		}
		if len(splitRecords) == 0 {
			splitRecords = nil
		}
	}

	return &Batch{
		records:        b.records[from:to],
		recordStatuses: b.recordStatuses[from:to],
		positions:      b.positions[from:to],
		filterCount:    filterCount,
		tainted:        false,
		splitRecords:   splitRecords,
	}
}

// HasActiveRecords returns true if the batch has any records that are not
// filtered out.
func (b *Batch) HasActiveRecords() bool {
	return b.filterCount < len(b.records)
}

// ActiveRecords returns the records that are not filtered.
func (b *Batch) ActiveRecords() []opencdc.Record {
	if b.filterCount == 0 {
		return b.records
	}
	if b.filterCount == len(b.records) {
		return nil
	}
	active := make([]opencdc.Record, 0, len(b.records)-b.filterCount)
	for i, r := range b.records {
		if b.recordStatuses[i].Flag != RecordFlagFilter {
			active = append(active, r)
		}
	}
	return active
}

// ActiveRecordIndices returns the indices of the records that are not filtered.
// If no records are filtered, it returns nil, in which case the caller should
// use the original indices of the records. This prevents the need to
// reallocate the slice if no records are filtered.
func (b *Batch) ActiveRecordIndices() []int {
	if b.filterCount == 0 {
		return nil
	}
	active := make([]int, 0, len(b.records)-b.filterCount)
	for i, status := range b.recordStatuses {
		if status.Flag != RecordFlagFilter {
			active = append(active, i)
		}
	}
	return active
}

// originalBatch returns the batch in which any split records are replaced with
// the original records. This is needed when acking or nacking records.
// If the batch does not contain any split records, it returns the original batch.
func (b *Batch) originalBatch() *Batch {
	if len(b.splitRecords) == 0 {
		// No records were split, we can return the original batch.
		return b
	}

	records := make([]opencdc.Record, 0, len(b.records))
	recordStatuses := make([]RecordStatus, 0, len(b.records))
	positions := make([]opencdc.Position, 0, len(b.records))
	for i, pos := range b.positions {
		if pos == nil {
			continue
		}

		records = append(records, b.records[i])
		recordStatuses = append(recordStatuses, b.recordStatuses[i])
		positions = append(positions, pos)
		if rec, ok := b.splitRecords[pos.String()]; ok {
			// If the record was split, we need to replace it with the
			// original record.
			records[len(records)-1] = rec
		}
	}

	filterCount := 0
	if b.filterCount > 0 {
		for _, status := range recordStatuses {
			if status.Flag == RecordFlagFilter {
				filterCount++
			}
		}
	}

	return &Batch{
		records:        records,
		recordStatuses: recordStatuses,
		positions:      positions,
		filterCount:    filterCount,
		tainted:        false,
		splitRecords:   nil,
	}
}

// RecordStatus holds the status of a record in a batch. The flag indicates the
// status of the record, and the error is set if the record was nacked.
type RecordStatus struct {
	Flag  RecordFlag
	Error error
}

type RecordFlag int

const (
	RecordFlagAck    RecordFlag = iota // ack
	RecordFlagNack                     // nack
	RecordFlagRetry                    // retry
	RecordFlagFilter                   // filter
)
