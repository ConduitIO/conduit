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

package funnel

import (
	"fmt"
	"slices"

	"github.com/conduitio/conduit-commons/opencdc"
)

type Batch struct {
	records        []opencdc.Record
	recordStatuses []RecordStatus
	positions      []opencdc.Position

	// skipCount is updated any time a record is marked as skipped, to make it
	// easier to construct the set of active records.
	skipCount int

	// If a batch is tainted it means that parts need to be either nacked or
	// retried. Such a batch needs to be split into multiple batches, each
	// containing only records with the same status (skipped counts as acked).
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
		skipCount:      0,
		tainted:        false,
	}
}

func (b *Batch) Ack(i int, j ...int) {
	b.setFlag(RecordFlagAck, nil, i, j...)
}

func (b *Batch) Nack(err error, i int, j ...int) {
	b.setFlag(RecordFlagNack, err, i, j...)
	b.tainted = true
}

func (b *Batch) Retry(i int, j ...int) {
	b.setFlag(RecordFlagRetry, nil, i, j...)
	b.tainted = true
}

func (b *Batch) Skip(i int, j ...int) {
	b.setFlag(RecordFlagSkip, nil, i, j...)
	end := i + 1
	if len(j) == 1 {
		end = j[0]
	}
	b.skipCount += end - i
}

func (b *Batch) setFlag(f RecordFlag, err error, i int, j ...int) {
	switch len(j) {
	case 0:
		b.recordStatuses[i].Flag = f
		b.recordStatuses[i].Error = err
	case 1:
		if i >= j[0] {
			panic(fmt.Sprintf("invalid range (%d >= %d)", i, j[0]))
		}
		for k := i; k < j[0]; k++ {
			b.recordStatuses[k].Flag = f
			b.recordStatuses[k].Error = err
		}
	default:
		panic(fmt.Sprintf("too many arguments (%d)", len(j)))
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
		tainted:        b.tainted,
		skipCount:      b.skipCount,
	}
}

func (b *Batch) sub(from, to int) *Batch {
	skipCount := 0
	for _, status := range b.recordStatuses[from:to] {
		if status.Flag == RecordFlagSkip {
			skipCount++
		}
	}
	return &Batch{
		records:        b.records[from:to],
		recordStatuses: b.recordStatuses[from:to],
		positions:      b.positions[from:to],
		skipCount:      skipCount,
		tainted:        false,
	}
}

// ActiveRecords returns the records that are not skipped.
func (b *Batch) ActiveRecords() []opencdc.Record {
	if b.skipCount == 0 {
		return b.records
	}
	active := make([]opencdc.Record, 0, len(b.records)-b.skipCount)
	for i, r := range b.records {
		if b.recordStatuses[i].Flag != RecordFlagSkip {
			active = append(active, r)
		}
	}
	return active
}

type RecordStatus struct {
	Flag  RecordFlag
	Error error
}

type RecordFlag int

const (
	RecordFlagAck RecordFlag = iota
	RecordFlagNack
	RecordFlagRetry
	RecordFlagSkip
)
