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
	positions      []opencdc.Position

	// filterCount is updated any time a record is marked as filtered, to make it
	// easier to construct the set of active records.
	filterCount int

	// If a batch is tainted it means that parts need to be either nacked or
	// retried. Such a batch needs to be split into multiple batches, each
	// containing only records with the same status (filtered counts as acked).
	tainted bool
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
// provided records.
func (b *Batch) SetRecords(i int, recs []opencdc.Record) {
	copy(b.records[i:], recs)
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
	for _, status := range b.recordStatuses[from:to] {
		if status.Flag == RecordFlagFilter {
			filterCount++
		}
	}
	return &Batch{
		records:        b.records[from:to],
		recordStatuses: b.recordStatuses[from:to],
		positions:      b.positions[from:to],
		filterCount:    filterCount,
		tainted:        false,
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
