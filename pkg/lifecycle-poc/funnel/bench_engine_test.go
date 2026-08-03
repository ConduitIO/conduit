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

// In-process engine benchmarks.
//
// These exist because the Docker/benchi harness could not answer the question
// it was built for — see benchi/METHODOLOGY.md. Two defects made every number
// it produced unusable: benchi's msg-rate comes from Conduit's own metrics, and
// the two engines do not instrument the same event (v1 observes in an ack
// handler, arch-v2 observes at both source read and destination ack); and the
// measured effects were smaller than the harness's own A/A noise floor (v1
// against ITSELF reported +13.0% / -5.0% at 30s runs).
//
// This file avoids both problems by construction:
//
//   - No metrics are consulted. b.N and wall time are the measurement.
//   - No Docker, no container scheduling, no destination I/O.
//   - No gomock. An earlier attempt used the package's gomock-based
//     generatorSource/printerDestination helpers and the profile came back
//     dominated by reflect.Value.call and gomock.DoAndReturn — over 50%
//     cumulative — because at batch=1 there are N mocked calls per record. The
//     fakes below are plain structs so what is timed is the engine.
//
// What this measures is arch-v2's PER-PASS cost as a function of batch size:
// one doTask call is one pass. It does NOT compare v1 to v2 — v1 has no
// equivalent entry point to drive this way, and cross-engine comparison is what
// needs dedicated hardware and an A/A control.
package funnel

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
)

// benchSource is a Source with no mocking framework in the path: Read hands
// back a pre-built batch and Ack is a no-op counter.
type benchSource struct {
	id        string
	batch     []opencdc.Record
	acked     int
	readCalls int
}

func newBenchSource(id string, batchSize int) *benchSource {
	recs := make([]opencdc.Record, batchSize)
	for i := range recs {
		recs[i] = opencdc.Record{
			Position: opencdc.Position(strconv.Itoa(i)),
			Metadata: opencdc.Metadata{opencdc.MetadataConduitSourceConnectorID: id},
			Payload: opencdc.Change{
				After: opencdc.StructuredData{"id": i, "name": "benchmark-record"},
			},
		}
	}
	return &benchSource{id: id, batch: recs}
}

func (s *benchSource) ID() string                     { return s.id }
func (s *benchSource) Open(context.Context) error     { return nil }
func (s *benchSource) Teardown(context.Context) error { return nil }
func (s *benchSource) Errors() <-chan error           { return nil }

func (s *benchSource) Read(context.Context) ([]opencdc.Record, error) {
	s.readCalls++
	// Fresh positions each pass so the run ledger and posIndex behave as they
	// would with a real source, rather than seeing repeats.
	out := make([]opencdc.Record, len(s.batch))
	for i := range s.batch {
		r := s.batch[i]
		r.Position = opencdc.Position(strconv.Itoa(s.readCalls*len(s.batch) + i))
		out[i] = r
	}
	return out, nil
}

func (s *benchSource) Ack(_ context.Context, p []opencdc.Position) error {
	s.acked += len(p)
	return nil
}

// benchDestination accepts everything and acks it immediately.
type benchDestination struct {
	id      string
	pending []opencdc.Position
}

func (d *benchDestination) ID() string                     { return d.id }
func (d *benchDestination) Open(context.Context) error     { return nil }
func (d *benchDestination) Teardown(context.Context) error { return nil }
func (d *benchDestination) Errors() <-chan error           { return nil }

func (d *benchDestination) Write(_ context.Context, recs []opencdc.Record) error {
	for _, r := range recs {
		d.pending = append(d.pending, r.Position)
	}
	return nil
}

func (d *benchDestination) Ack(context.Context) ([]connector.DestinationAck, error) {
	acks := make([]connector.DestinationAck, len(d.pending))
	for i, p := range d.pending {
		acks[i] = connector.DestinationAck{Position: p}
	}
	d.pending = d.pending[:0]
	return acks, nil
}

// benchWorker builds a worker over batchSize-record reads and destCount
// destinations, optionally marking the destinations as a shared boundary (the
// N-source serialization point).
func benchWorker(b *testing.B, batchSize, destCount int, shared bool) *Worker {
	b.Helper()
	logger := log.Nop()

	src := newBenchSource("bench-src", batchSize)
	srcTask := NewSourceTask("bench-src", src, logger, &NoOpConnectorMetrics{})
	srcNode := &TaskNode{Task: srcTask}

	for i := range destCount {
		id := fmt.Sprintf("bench-dst-%d", i)
		dt := NewDestinationTask(id, &benchDestination{id: id}, logger, &NoOpConnectorMetrics{})
		n := &TaskNode{Task: dt}
		if shared {
			n.MarkSharedBoundary()
		}
		srcNode.Next = append(srcNode.Next, n)
	}

	dlq := NewDLQ("bench-dlq", &benchDestination{id: "bench-dlq"}, logger, &NoOpConnectorMetrics{}, 1, 0)

	w, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
	if err != nil {
		b.Fatal(err)
	}
	if err := w.Open(context.Background()); err != nil {
		b.Fatal(err)
	}
	return w
}

// BenchmarkEnginePass reports ns/record for one doTask pass at a range of batch
// sizes. If arch-v2's cost is dominated by fixed per-pass work — the hypothesis
// behind the default-config regression, since sdk.batch.size defaults to 0 and
// the SDK then reads batches of ONE — ns/record should fall steeply as batch
// size rises and then plateau.
func BenchmarkEnginePass(b *testing.B) {
	for _, batchSize := range []int{1, 10, 100, 1000} {
		b.Run(fmt.Sprintf("batch%d", batchSize), func(b *testing.B) {
			w := benchWorker(b, batchSize, 1, false)
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := w.doTask(ctx, w.FirstTask, &Batch{}, newRunAckNacker(w)); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*batchSize), "ns/record")
		})
	}
}

// BenchmarkEngineFanOut isolates what M destinations cost per pass, and what
// the shared-boundary mutex adds on top. The mutex is the mechanism that
// serializes N sources into one destination subtree, so this is the closest
// in-process proxy for the N×M shape.
func BenchmarkEngineFanOut(b *testing.B) {
	for _, destCount := range []int{1, 2, 4} {
		for _, shared := range []bool{false, true} {
			name := fmt.Sprintf("dest%d/plain", destCount)
			if shared {
				name = fmt.Sprintf("dest%d/sharedmu", destCount)
			}
			b.Run(name, func(b *testing.B) {
				const batchSize = 100
				w := benchWorker(b, batchSize, destCount, shared)
				ctx := context.Background()
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					if err := w.doTask(ctx, w.FirstTask, &Batch{}, newRunAckNacker(w)); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*batchSize), "ns/record")
			})
		}
	}
}
