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

package engines_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/internal/enginetest"
	"github.com/conduitio/conduit/pkg/internal/enginetest/engines"
)

// benchShape is one reference pipeline shape for `make bench-engine`.
//
// The shapes follow the ones the arch-v2 graduation work measured on the
// Docker/benchi harness (benchi/staged-engine-proto, benchi/archv2-gate), so a
// local number can be read against an AWS one. What this does NOT replace is
// that harness: it drives fakes in-process, so it measures the engine's own
// cost (ack propagation, scheduling, locking) and none of the connector, gRPC
// or network path. Treat results as a regression signal for engine changes, not
// as a throughput claim. A throughput claim needs benchi (CLAUDE.md).
type benchShape struct {
	name string
	// records is the number of records each source produces per iteration.
	records int
	sources int
	dests   int
	src     enginetest.SourceConfig
	dst     enginetest.DestinationConfig
	// skip lists engines that cannot run the shape, with the reason.
	skip map[string]string
}

var benchShapes = []benchShape{
	{name: "1x1", records: 10_000, sources: 1, dests: 1},
	{name: "2x2", records: 10_000, sources: 2, dests: 2},
	{name: "4x4", records: 10_000, sources: 4, dests: 4},
	{
		name: "1x1-srcbatch100", records: 10_000, sources: 1, dests: 1,
		src: enginetest.SourceConfig{BatchSize: 100},
	},
	{
		// Real elapsed time per ack, so the shape is short. It is what separates
		// an engine that waits for each ack from one that overlaps them.
		name: "1x1-acklatency100us", records: 2_000, sources: 1, dests: 1,
		dst: enginetest.DestinationConfig{AckLatency: 100 * time.Microsecond},
	},
	{
		// Acks delivered through a goroutine and a buffered channel, the shape of
		// a gRPC destination.
		name: "1x1-bufferedacks", records: 10_000, sources: 1, dests: 1,
		dst: enginetest.DestinationConfig{AckPump: true, AckBuffer: 64},
	},
	{
		// SDK-style destination batching with reads that fill a batch. Reads
		// smaller than the batch need a flush timer and are slow on arch-v2 (the
		// engine waits for each write's ack before sending the next); that gap is
		// covered by a skipped test, not by a number here.
		name: "1x1-destbatch100", records: 10_000, sources: 1, dests: 1,
		src: enginetest.SourceConfig{BatchSize: 100},
		dst: enginetest.DestinationConfig{Batching: enginetest.Batching{Size: 100, Delay: 10 * time.Millisecond}},
	},
}

// BenchmarkEngine runs each shape on each engine. One iteration is one full
// pipeline run: start, move records*sources records, stop, tear down. The
// reported records/s is total source records over elapsed wall time, so it
// includes startup and drain; with the default 10k records that is small next
// to the run, and it is the same for both engines.
//
// Run through `make bench-engine`, which sets -count for benchstat.
func BenchmarkEngine(b *testing.B) {
	for _, shape := range benchShapes {
		b.Run(shape.name, func(b *testing.B) {
			for _, eng := range engines.All() {
				b.Run(eng.Name(), func(b *testing.B) {
					if reason, ok := shape.skip[eng.Name()]; ok {
						b.Skip(reason)
					}
					benchEngine(b, eng, shape)
				})
			}
		})
	}
}

func benchEngine(b *testing.B, eng engines.Engine, shape benchShape) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		topo := shape.topology()
		b.StartTimer()

		run, err := eng.Start(ctx, topo)
		if err != nil {
			b.Fatal(err)
		}
		for _, s := range topo.Sources {
			if err := s.WaitAcked(ctx, shape.records); err != nil {
				b.Fatalf("%s did not ack %d records: %v", s.ID(), shape.records, err)
			}
		}
		if err := run.Stop(ctx); err != nil {
			b.Fatal(err)
		}
		if err := run.Wait(ctx); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()

	total := float64(b.N * shape.records * shape.sources)
	b.ReportMetric(total/b.Elapsed().Seconds(), "records/s")
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/total, "ns/record")
}

func (s benchShape) topology() engines.Topology {
	var t engines.Topology
	for i := range s.sources {
		cfg := s.src
		cfg.ID = fmt.Sprintf("src%d", i)
		cfg.Total = s.records
		cfg.Discard = true
		t.Sources = append(t.Sources, enginetest.NewSource(cfg))
	}
	for i := range s.dests {
		cfg := s.dst
		cfg.ID = fmt.Sprintf("dst%d", i)
		cfg.Discard = true
		t.Destinations = append(t.Destinations, enginetest.NewDestination(cfg))
	}
	t.DLQ = func(id string) *enginetest.Destination {
		return enginetest.NewDestination(enginetest.DestinationConfig{ID: "dlq-" + id, Discard: true})
	}
	return t
}
