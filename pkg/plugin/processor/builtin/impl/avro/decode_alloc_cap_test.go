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

package avro

import (
	"context"
	"encoding/binary"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/schemaregistry"
	"github.com/conduitio/conduit/pkg/schemaregistry/schemaregistrytest"
	"github.com/matryer/is"
	"github.com/twmb/franz-go/pkg/sr"
)

// The decoder behind avro.decode (conduit-commons/schema/avro, v0.7.0+)
// caps every array and map at 1,000,000 elements. A payload over the cap
// must come back as an error record, never as a decoded value that was
// truncated to fit (Invariant 6), and a payload that only *declares* a huge
// count must be rejected before memory for that count is allocated.
const avroAllocCap = 1_000_000

// zigzag appends n as an Avro long (zigzag varint).
func zigzag(b []byte, n int64) []byte {
	return binary.AppendUvarint(b, uint64((n<<1)^(n>>63)))
}

// intArrayPayload encodes {"items": [0, 0, ...]} with n elements in one
// block. Each 0 is a single byte, so the payload is an honest n+few bytes.
func intArrayPayload(n int) []byte {
	b := zigzag(nil, int64(n))
	b = append(b, make([]byte, n)...) // n x int 0
	return append(b, 0)               // end of array
}

// mapPayload encodes {"attrs": {"k": 0, ...}} declaring n entries in one
// block. Avro allows repeated keys, so the payload stays small.
func mapPayload(n int) []byte {
	b := zigzag(nil, int64(n))
	for range n {
		b = append(b, 2, 'k', 0) // key "k", value int 0
	}
	return append(b, 0)
}

// newDecodeProcessorWithSchema returns an opened avro.decode processor
// reading .Payload.After, and the schema ID that schema was registered
// under in a schema registry (in-memory, or the real one with
// -tags integration).
func newDecodeProcessorWithSchema(t *testing.T, schema string) (*DecodeProcessor, int) {
	t.Helper()
	is := is.New(t)
	ctx := context.Background()

	client, err := schemaregistry.NewClient(log.Nop(), sr.URLs(schemaregistrytest.TestSchemaRegistryURL(t)))
	is.NoErr(err)
	// Unique subject per run: a fixed subject collides with a previous run
	// against a real registry (see #2883).
	subject := fmt.Sprintf("alloc-cap-%s-%d", strings.ReplaceAll(t.Name(), "/", "-"), time.Now().UnixNano())
	ss, err := client.CreateSchema(ctx, subject, sr.Schema{Type: sr.TypeAvro, Schema: schema})
	is.NoErr(err)

	p := NewDecodeProcessor(log.Nop())
	p.SetSchemaRegistry(client)
	is.NoErr(p.Configure(ctx, config.Config{"field": ".Payload.After"}))
	is.NoErr(p.Open(ctx))
	return p, ss.ID
}

func confluentRecord(t *testing.T, schemaID int, payload []byte) opencdc.Record {
	t.Helper()
	b, err := (&sr.ConfluentHeader{}).AppendEncode(nil, schemaID, nil)
	if err != nil {
		t.Fatalf("encode confluent header: %v", err)
	}
	return opencdc.Record{
		Position:  opencdc.Position("alloc-cap"),
		Operation: opencdc.OperationCreate,
		Payload:   opencdc.Change{After: opencdc.RawData(append(b, payload...))},
	}
}

func processOne(t *testing.T, p *DecodeProcessor, rec opencdc.Record) sdk.ProcessedRecord {
	t.Helper()
	got := p.Process(context.Background(), []opencdc.Record{rec})
	if len(got) != 1 {
		t.Fatalf("expected 1 processed record, got %d", len(got))
	}
	return got[0]
}

// requireCapError asserts got is an error record naming the allocation cap.
func requireCapError(t *testing.T, got sdk.ProcessedRecord, setting string) {
	t.Helper()
	errRec, ok := got.(sdk.ErrorRecord)
	if !ok {
		t.Fatalf("expected sdk.ErrorRecord, got %T (an over-cap payload must not decode)", got)
	}
	if !strings.Contains(errRec.Error.Error(), setting) {
		t.Fatalf("error does not name %s: %v", setting, errRec.Error)
	}
	t.Logf("error: %v", errRec.Error)
}

func TestDecodeProcessor_ArrayAllocationCap(t *testing.T) {
	const schema = `{"type":"record","name":"r","fields":[{"name":"items","type":{"type":"array","items":"int"}}]}`

	t.Run("at cap decodes", func(t *testing.T) {
		is := is.New(t)
		p, id := newDecodeProcessorWithSchema(t, schema)
		got := processOne(t, p, confluentRecord(t, id, intArrayPayload(avroAllocCap)))
		rec, ok := got.(sdk.SingleRecord)
		if !ok {
			t.Fatalf("expected sdk.SingleRecord, got %T: %v", got, got)
		}
		items := rec.Payload.After.(opencdc.StructuredData)["items"].([]any)
		is.Equal(len(items), avroAllocCap)
	})

	t.Run("one over cap is an error, not a truncated array", func(t *testing.T) {
		p, id := newDecodeProcessorWithSchema(t, schema)
		got := processOne(t, p, confluentRecord(t, id, intArrayPayload(avroAllocCap+1)))
		requireCapError(t, got, "MaxSliceAllocSize")
	})

	t.Run("declared count is rejected before allocating", func(t *testing.T) {
		is := is.New(t)
		p, id := newDecodeProcessorWithSchema(t, schema)
		// 6 payload bytes declaring 2^40 elements. Without the cap the
		// decoder would try to size a slice for all of them.
		payload := append(zigzag(nil, 1<<40), 0)

		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		got := processOne(t, p, confluentRecord(t, id, payload))
		runtime.ReadMemStats(&after)

		requireCapError(t, got, "MaxSliceAllocSize")
		allocated := after.TotalAlloc - before.TotalAlloc
		t.Logf("allocated during decode: %d bytes", allocated)
		is.True(allocated < 64<<20) // a few registry round-trip buffers, not 2^40 elements
	})
}

func TestDecodeProcessor_MapAllocationCap(t *testing.T) {
	const schema = `{"type":"record","name":"r","fields":[{"name":"attrs","type":{"type":"map","values":"int"}}]}`

	p, id := newDecodeProcessorWithSchema(t, schema)
	got := processOne(t, p, confluentRecord(t, id, mapPayload(avroAllocCap+1)))
	requireCapError(t, got, "MaxMapAllocSize")
}
