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

package enginetest_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/internal/enginetest"
	"github.com/matryer/is"
)

func TestSource_BatchesAndRunsDry(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	s := enginetest.NewSource(enginetest.SourceConfig{ID: "s", Total: 5, BatchSize: 2, EOFWhenDry: true})
	is.NoErr(s.Open(ctx))

	var sizes []int
	for {
		recs, err := s.Read(ctx)
		if err != nil {
			is.Equal(err, io.EOF)
			break
		}
		sizes = append(sizes, len(recs))
	}
	is.Equal(sizes, []int{2, 2, 1})
	is.Equal(s.Produced(), 5)
}

func TestSource_OpenTwiceFails(t *testing.T) {
	is := is.New(t)
	s := enginetest.NewSource(enginetest.SourceConfig{})
	is.NoErr(s.Open(context.Background()))
	is.True(s.Open(context.Background()) != nil)
}

func TestSource_TeardownReleasesStalledRead(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	s := enginetest.NewSource(enginetest.SourceConfig{})
	s.ReadGate.Close()

	errc := make(chan error, 1)
	go func() { _, err := s.Read(ctx); errc <- err }()
	is.NoErr(s.WaitReadCalls(ctx, 1))
	is.NoErr(s.Teardown(ctx))
	is.Equal(<-errc, io.EOF) // closed stream, as a real source reports
}

func TestSource_LedgerFlagsAckBeforeDurable(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	l := enginetest.NewLedger().Require("d")
	s := enginetest.NewSource(enginetest.SourceConfig{ID: "s", Ledger: l})
	d := enginetest.NewDestination(enginetest.DestinationConfig{ID: "d", Ledger: l})
	d.AckGate.Close() // irrelevant to durability: Write flushes immediately
	recs, err := s.Read(ctx)
	is.NoErr(err)

	is.NoErr(s.Ack(ctx, []opencdc.Position{recs[0].Position}))
	is.Equal(len(l.Violations()), 1) // acked before any destination saw it

	l2 := enginetest.NewLedger().Require("d")
	d2 := enginetest.NewDestination(enginetest.DestinationConfig{ID: "d", Ledger: l2})
	s2 := enginetest.NewSource(enginetest.SourceConfig{ID: "s", Ledger: l2})
	recs2, _ := s2.Read(ctx)
	is.NoErr(d2.Write(ctx, recs2))

	// Flushed inside Write but the engine has not been handed the ack: an
	// engine that acked the source now would be acking early.
	is.NoErr(s2.Ack(ctx, []opencdc.Position{recs2[0].Position}))
	is.Equal(len(l2.Violations()), 1)

	l3 := enginetest.NewLedger().Require("d")
	d3 := enginetest.NewDestination(enginetest.DestinationConfig{ID: "d", Ledger: l3})
	s3 := enginetest.NewSource(enginetest.SourceConfig{ID: "s", Ledger: l3})
	recs3, _ := s3.Read(ctx)
	is.NoErr(d3.Write(ctx, recs3))
	_, err = d3.Ack(ctx) // the engine is told
	is.NoErr(err)
	is.NoErr(s3.Ack(ctx, []opencdc.Position{recs3[0].Position}))
	is.Equal(l3.Violations(), []string(nil))
}

func TestDestination_BatchingFlushesOnSizeOnly(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	src := enginetest.NewSource(enginetest.SourceConfig{ID: "s", Total: 3})
	d := enginetest.NewDestination(enginetest.DestinationConfig{Batching: enginetest.Batching{Size: 3}})

	for range 2 {
		recs, _ := src.Read(ctx)
		is.NoErr(d.Write(ctx, recs))
	}
	is.Equal(d.Pending(), 2) // held: no delay configured, batch not full
	is.Equal(d.DeliveredCount(), 0)

	recs, _ := src.Read(ctx)
	is.NoErr(d.Write(ctx, recs))
	is.Equal(d.Pending(), 0)
	acks, err := d.Ack(ctx)
	is.NoErr(err)
	is.Equal(len(acks), 3)
}

func TestDestination_BatchingFlushesOnDelay(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	src := enginetest.NewSource(enginetest.SourceConfig{ID: "s"})
	d := enginetest.NewDestination(enginetest.DestinationConfig{Batching: enginetest.Batching{Size: 100, Delay: time.Millisecond}})

	recs, _ := src.Read(ctx)
	is.NoErr(d.Write(ctx, recs))
	acks, err := d.Ack(ctx) // blocks until the timer flushes
	is.NoErr(err)
	is.Equal(len(acks), 1)
}

func TestDestination_VerdictNacksAndAckChunking(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	src := enginetest.NewSource(enginetest.SourceConfig{ID: "s", Total: 4, BatchSize: 4})
	d := enginetest.NewDestination(enginetest.DestinationConfig{
		AckChunk: 3,
		Verdict: func(r opencdc.Record) error {
			if string(r.Position) == "s/1" {
				return io.ErrUnexpectedEOF
			}
			return nil
		},
	})
	recs, _ := src.Read(ctx)
	is.NoErr(d.Write(ctx, recs))

	a1, _ := d.Ack(ctx)
	a2, _ := d.Ack(ctx)
	is.Equal(len(a1), 3)
	is.Equal(len(a2), 1)
	is.True(a1[1].Error != nil)
	is.Equal(len(d.Delivered()), 3)
	is.Equal(len(d.Nacked()), 1)
}

func TestDestination_CrashFailsPendingAndFutureCalls(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	d := enginetest.NewDestination(enginetest.DestinationConfig{})
	is.NoErr(d.Open(ctx))

	errc := make(chan error, 1)
	go func() { _, err := d.Ack(ctx); errc <- err }()
	d.Crash(nil)
	is.Equal(<-errc, enginetest.ErrCrashed)
	is.Equal(d.Write(ctx, nil), enginetest.ErrCrashed)
	is.Equal(<-d.Errors(), enginetest.ErrCrashed)
}

func TestDestination_AckPumpDeliversThroughChannel(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	src := enginetest.NewSource(enginetest.SourceConfig{ID: "s", Total: 2})
	d := enginetest.NewDestination(enginetest.DestinationConfig{AckPump: true, AckBuffer: 2})
	is.NoErr(d.Open(ctx))
	defer d.Teardown(ctx) //nolint:errcheck // test cleanup

	for range 2 {
		recs, _ := src.Read(ctx)
		is.NoErr(d.Write(ctx, recs))
	}
	a1, err := d.Ack(ctx)
	is.NoErr(err)
	a2, err := d.Ack(ctx)
	is.NoErr(err)
	is.Equal(len(a1)+len(a2), 2)
}

func TestLedger_WithoutRequireFailsLoudly(t *testing.T) {
	is := is.New(t)
	l := enginetest.NewLedger()
	s := enginetest.NewSource(enginetest.SourceConfig{ID: "s", Ledger: l})
	recs, _ := s.Read(context.Background())
	is.NoErr(s.Ack(context.Background(), []opencdc.Position{recs[0].Position}))
	is.Equal(len(l.Violations()), 1)
}

func TestLedger_FailedClosesOnAckBeforeDurable(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	l := enginetest.NewLedger().Require("d")
	s := enginetest.NewSource(enginetest.SourceConfig{ID: "s", Ledger: l})
	recs, _ := s.Read(ctx)

	select {
	case <-l.Failed():
		t.Fatal("Failed closed before any violation")
	default:
	}
	is.NoErr(s.Ack(ctx, []opencdc.Position{recs[0].Position}))
	select {
	case <-l.Failed():
	default:
		t.Fatal("Failed did not close on an ack before the record was durable")
	}
}

// TestShapes_CarryProductionMetadata is the regression test for the defect that
// halved the sensitivity of `make bench-engine` and the blocking benchi
// regression gate built on it: the preset shapes set only
// conduit.source.connector.id, so every record the kit produced was missing
// opencdc.readAt, which Conduit's own sources always set
// (connector.Source.sanitizeRecord). Both engines look that key up once per
// record on the ack path and fall back when it is absent, and the absent-key
// fallback in conduit-commons' Metadata.GetReadAt builds an error with
// fmt.Errorf: 44.6% of all objects the kit allocated were that discarded
// error. The kit measured error formatting production never executes.
//
// Any Shape the kit ships must therefore carry the metadata a production record
// carries, with a readAt that parses.
func TestShapes_CarryProductionMetadata(t *testing.T) {
	shapes := map[string]enginetest.Shape{
		"StructuredShape": enginetest.StructuredShape,
		"RawShape":        enginetest.RawShape(128),
	}

	for name, shape := range shapes {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			rec := shape("src1", 7)

			is.Equal(rec.Metadata[opencdc.MetadataConduitSourceConnectorID], "src1")

			readAt, err := rec.Metadata.GetReadAt()
			is.NoErr(err) // every kit record must carry a parseable opencdc.readAt
			is.True(!readAt.IsZero())
			// Sanity-check the magnitude: a unix timestamp in nanoseconds, not
			// seconds or milliseconds. The shapes capture it at process start,
			// so it is recent but not necessarily now.
			is.True(readAt.After(time.Now().Add(-time.Hour)))
			is.True(readAt.Before(time.Now().Add(time.Minute)))
		})
	}
}

// TestSource_RecordsCarryReadAt covers the same property through the fake
// source, which is how every engine test and benchmark actually gets records.
func TestSource_RecordsCarryReadAt(t *testing.T) {
	is := is.New(t)
	s := enginetest.NewSource(enginetest.SourceConfig{ID: "s", Total: 4, BatchSize: 4})
	is.NoErr(s.Open(context.Background()))

	recs, err := s.Read(context.Background())
	is.NoErr(err)
	is.Equal(len(recs), 4)
	for _, rec := range recs {
		_, err := rec.Metadata.GetReadAt()
		is.NoErr(err)
	}
}
