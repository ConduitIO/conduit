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
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

// latencyDestination simulates a destination connector with a fixed per-record
// write cost. perRecord=20µs models a ~50k records/s destination: the write of
// one 3000-record batch blocks for 60ms. The benchmark pairs it with a
// simulated source-read cost to show that the synchronous flush serializes
// read+write while the pipelined flusher overlaps them.
type latencyDestination struct {
	perRecord time.Duration

	mu      sync.Mutex
	written int
	pending []connector.DestinationAck
}

func newLatencyDestination(perRecord time.Duration) *latencyDestination {
	return &latencyDestination{perRecord: perRecord}
}

func (d *latencyDestination) ID() string                     { return "bench" }
func (d *latencyDestination) Open(context.Context) error     { return nil }
func (d *latencyDestination) Teardown(context.Context) error { return nil }
func (d *latencyDestination) Errors() <-chan error           { return make(chan error) }

func (d *latencyDestination) Write(_ context.Context, recs []opencdc.Record) error {
	// The destination's synchronous, per-record cost.
	time.Sleep(time.Duration(len(recs)) * d.perRecord)

	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range recs {
		d.written++
		d.pending = append(d.pending, connector.DestinationAck{Position: r.Position})
	}
	return nil
}

func (d *latencyDestination) Ack(context.Context) ([]connector.DestinationAck, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	acks := d.pending
	d.pending = nil
	return acks, nil
}

func (d *latencyDestination) received() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.written
}

// benchmarkDestinationTaskPipelined feeds `records` records through a
// batchSize batching destination task, simulating a source read (readCost) per
// batch of records before handing them to doBuffered. pipelined toggles the
// read-ahead flusher, so the two variants differ ONLY in whether the flush
// overlaps the next source read — the apples-to-apples comparison this change
// is about.
func benchmarkDestinationTaskPipelined(b *testing.B, pipelined bool) {
	ctx := context.Background()

	// ~50k records/s destination: 20µs per record.
	const perRecord = 20 * time.Microsecond
	const batchSize = 3000
	const records = 30_000 // ten full batches
	// Simulated source read per batch. 15ms models the ~20% serialization gap
	// the synchronous flush costs: the source fills its ~2-batch read-ahead
	// window and then stalls for the full flush duration (60ms), so each batch
	// serializes read (15ms) + write (60ms) ≈ 40k records/s, while the
	// pipelined flusher overlaps them and stays destination-bound at ~50k/s.
	const readCost = 15 * time.Millisecond

	recs := randomRecords(records)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		dest := newLatencyDestination(perRecord)
		task := NewBatchingDestinationTask("bench", dest, log.Nop(), NoOpConnectorMetrics{}, batchSize, 0)
		if pipelined {
			task.SetPipelined()
			if err := task.Open(ctx); err != nil {
				b.Fatal(err)
			}
		}

		for i := 0; i < records; i += batchSize {
			// Simulate the source read of one batch.
			time.Sleep(readCost)
			end := min(i+batchSize, records)
			for _, rec := range recs[i:end] {
				err := task.doBuffered(ctx, NewBatch([]opencdc.Record{rec}), newRecordingAckNacker(&eventLog{}))
				if err != nil && !isBuffered(err) {
					b.Fatal(err)
				}
			}
		}
		if err := task.Close(ctx); err != nil {
			b.Fatal(err)
		}
		if got := dest.received(); got != records {
			b.Fatalf("delivered %d of %d records", got, records)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(records)*float64(b.N)/b.Elapsed().Seconds(), "records/s")
}

// BenchmarkDestinationTask_DoBuffered_Synchronous is the pre-pipelining
// behavior: the flush (write+ack) runs on the benchmark goroutine, so each
// batch serializes read and write.
func BenchmarkDestinationTask_DoBuffered_Synchronous(b *testing.B) {
	benchmarkDestinationTaskPipelined(b, false)
}

// BenchmarkDestinationTask_DoBuffered_Pipelined is the read-ahead behavior: the
// flush runs on the flusher goroutine, so the source read overlaps the
// destination write.
func BenchmarkDestinationTask_DoBuffered_Pipelined(b *testing.B) {
	benchmarkDestinationTaskPipelined(b, true)
}
