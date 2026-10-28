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
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

// writeRecordingDestination records the record count of every Write.
type writeRecordingDestination struct {
	*fakeDestination
	mu     sync.Mutex
	writes []int
	sizes  []int
}

func (d *writeRecordingDestination) Write(ctx context.Context, recs []opencdc.Record) error {
	size := 0
	for _, r := range recs {
		size += recordWireSize(r)
	}
	d.mu.Lock()
	d.writes = append(d.writes, len(recs))
	d.sizes = append(d.sizes, size)
	d.mu.Unlock()
	return d.fakeDestination.Write(ctx, recs)
}

func bigRecords(n, payload int) []opencdc.Record {
	recs := make([]opencdc.Record, n)
	for i := range recs {
		recs[i] = opencdc.Record{
			Position: opencdc.Position(fmt.Sprintf("p%04d", i)),
			Payload:  opencdc.Change{After: opencdc.RawData(make([]byte, payload))},
		}
	}
	return recs
}

// maxReceive is the receive limit used below: with the 95% margin one Write
// holds 2 records of ~1 KB.
const testMaxReceive = 3000

func newByteCapTask(t *testing.T, batchSize int) (*DestinationTask, *writeRecordingDestination) {
	t.Helper()
	dest := &writeRecordingDestination{fakeDestination: newFakeDestination("d")}
	task := NewBatchingDestinationTask("d", dest, log.Test(t), NoOpConnectorMetrics{}, batchSize, 0)
	task.SetMaxReceiveRecordSize(testMaxReceive)
	return task, dest
}

// TestDestinationTask_Do_SplitsWriteByWireSize: at shipped defaults (no
// batching) a source batch goes straight to one Write. A batch whose estimated
// wire size exceeds the connector's receive limit must be split, with the acks
// kept in source order, instead of failing the whole Write at the plugin.
func TestDestinationTask_Do_SplitsWriteByWireSize(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	task, dest := newByteCapTask(t, 0)

	recs := bigRecords(7, 1000)
	batch := NewBatch(recs)
	is.NoErr(task.Do(ctx, batch))

	limit := writeLimit(testMaxReceive)
	for _, sz := range dest.sizes {
		is.True(sz <= limit) // no Write exceeds the byte budget
	}
	is.True(len(dest.writes) > 1)
	is.Equal(dest.receivedPositions(), bytecapPositions(recs...)) // every record, in order
	is.True(!batch.tainted)                                       // all acked
}

// TestDestinationTask_Do_ChunkNackMapsToRightRecord: a nack in a later chunk is
// attributed to the right record of the batch.
func TestDestinationTask_Do_ChunkNackMapsToRightRecord(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	task, dest := newByteCapTask(t, 0)

	recs := bigRecords(7, 1000)
	dest.setNackErr(recs[5].Position, fmt.Errorf("rejected"))
	batch := NewBatch(recs)
	is.NoErr(task.Do(ctx, batch))

	is.True(batch.tainted)
	for i, st := range batch.recordStatuses {
		if i == 5 {
			is.Equal(st.Flag, RecordFlagNack)
		} else {
			is.Equal(st.Flag, RecordFlagAck)
		}
	}
}

// TestDestinationTask_DoBuffered_FlushesOnBytes: with sdk.batch.size far above
// what fits in one request, the accumulator must flush on bytes, not wait for
// the record count (3000 records of ~1.4 KB exceed a 4 MiB request).
func TestDestinationTask_DoBuffered_FlushesOnBytes(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	task, dest := newByteCapTask(t, 100)

	recs := bigRecords(6, 1000)
	log := &eventLog{}
	for _, r := range recs {
		err := task.doBuffered(ctx, NewBatch([]opencdc.Record{r}), newRecordingAckNacker(log))
		is.True(err == nil || isBuffered(err))
	}
	is.True(len(dest.writes) >= 2) // flushed before 100 records accumulated
	is.NoErr(task.Close(ctx))
	for _, sz := range dest.sizes {
		is.True(sz <= writeLimit(testMaxReceive)) // every Write is within the budget
	}
	is.Equal(dest.receivedPositions(), bytecapPositions(recs...))
}

// TestDestinationTask_DoBuffered_OversizeSourceBatchChunked: one source batch
// larger than the limit reaches flushPending whole and is written in chunks.
func TestDestinationTask_DoBuffered_OversizeSourceBatchChunked(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	task, dest := newByteCapTask(t, 100)

	recs := bigRecords(9, 1000)
	log := &eventLog{}
	err := task.doBuffered(ctx, NewBatch(recs), newRecordingAckNacker(log))
	is.True(err == nil || isBuffered(err))
	is.NoErr(task.Close(ctx))

	for _, sz := range dest.sizes {
		is.True(sz <= writeLimit(testMaxReceive))
	}
	is.True(len(dest.writes) > 1)
	is.Equal(dest.receivedPositions(), bytecapPositions(recs...))
	is.Equal(len(log.snapshot()), 1) // the one source batch acked once, after all chunks
}

// TestRecordWireSize_NumbersCostMoreThanJSON pins why the cap does not reuse the
// JSON estimator: small numbers are 1-2 bytes in JSON and ~11 on the wire.
func TestRecordWireSize_NumbersCostMoreThanJSON(t *testing.T) {
	is := is.New(t)
	nums := make([]any, 200)
	for i := range nums {
		nums[i] = float64(i % 10)
	}
	rec := opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"a": nums}}}
	is.True(recordWireSize(rec) >= 200*11)
}

func bytecapPositions(recs ...opencdc.Record) []opencdc.Position {
	out := make([]opencdc.Position, len(recs))
	for i, r := range recs {
		out[i] = r.Position
	}
	return out
}

// oversizeRecords returns three records whose middle one alone cannot fit in a
// testMaxReceive request.
func oversizeRecords() []opencdc.Record {
	recs := bigRecords(3, 200)
	recs[1].Payload.After = opencdc.RawData(make([]byte, 2*testMaxReceive))
	return recs
}

func assertOversizeNacked(is *is.I, statusErr error, pos opencdc.Position) {
	ce, ok := conduiterr.Get(statusErr)
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodeRecordExceedsMaxReceiveSize.Reason())
	is.True(strings.Contains(ce.Message, "connectors.max-receive-record-size"))
	is.True(strings.Contains(ce.Message, log.KeyHash(pos)))
}

// TestDestinationTask_Do_OversizeRecordIsNackedNotSent: a record that alone
// exceeds the connector's receive limit would fail the whole stream at the plugin
// (RESOURCE_EXHAUSTED, no per-record nack) and, replayed on restart, stall the
// pipeline. It is nacked with a coded error before the Write so the DLQ policy
// decides; the records around it are written and acked as usual.
func TestDestinationTask_Do_OversizeRecordIsNackedNotSent(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	task, dest := newByteCapTask(t, 0)

	recs := oversizeRecords()
	batch := NewBatch(recs)
	is.NoErr(task.Do(ctx, batch))

	is.Equal(dest.receivedPositions(), bytecapPositions(recs[0], recs[2])) // the huge record never reached the plugin
	is.Equal(batch.recordStatuses[0].Flag, RecordFlagAck)
	is.Equal(batch.recordStatuses[1].Flag, RecordFlagNack)
	is.Equal(batch.recordStatuses[2].Flag, RecordFlagAck)
	assertOversizeNacked(is, batch.recordStatuses[1].Error, recs[1].Position)
}

// TestDestinationTask_DoBuffered_OversizeRecordIsNackedNotSent: same on the
// coalescing path, where the record sits inside a flush with its neighbours.
func TestDestinationTask_DoBuffered_OversizeRecordIsNackedNotSent(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	task, dest := newByteCapTask(t, 100)

	recs := oversizeRecords()
	batch := NewBatch(recs)
	alog := &eventLog{}
	err := task.doBuffered(ctx, batch, newRecordingAckNacker(alog))
	is.True(err == nil || isBuffered(err))
	is.NoErr(task.Close(ctx))

	is.Equal(dest.receivedPositions(), bytecapPositions(recs[0], recs[2]))
	is.Equal(batch.recordStatuses[1].Flag, RecordFlagNack)
	assertOversizeNacked(is, batch.recordStatuses[1].Error, recs[1].Position)
	is.Equal(alog.snapshot(), []string{"acker.ack", "acker.nack", "acker.ack"}) // tainted batch: split by flag, in order
}
