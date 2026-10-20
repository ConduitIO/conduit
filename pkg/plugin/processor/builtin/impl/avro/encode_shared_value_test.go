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
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	cavro "github.com/conduitio/conduit-commons/schema/avro"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/schemaregistry"
	"github.com/conduitio/conduit/pkg/schemaregistry/schemaregistrytest"
	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"
	"github.com/twmb/franz-go/pkg/sr"
)

// nestedUnionSchema has unions nested in an array and in a map. One branch of
// each union is itself a map or array, which is what makes a wrapped value
// ({"string": "bar"}) indistinguishable from a plain value of another branch.
const nestedUnionSchema = `{"type":"record","name":"nested_unions","fields":[` +
	`{"name":"a","type":{"type":"array","items":["null","string",{"type":"map","values":"string"}]}},` +
	`{"name":"m","type":{"type":"map","values":["null","string",{"type":"array","items":"string"}]}}]}`

func newNestedUnionProcessors(t *testing.T) (*EncodeProcessor, *DecodeProcessor) {
	t.Helper()
	is := is.New(t)
	ctx := context.Background()

	client, err := schemaregistry.NewClient(log.Nop(), sr.URLs(schemaregistrytest.TestSchemaRegistryURL(t)))
	is.NoErr(err)
	_, err = client.CreateSchema(ctx, "nested-unions-value", sr.Schema{Type: sr.TypeAvro, Schema: nestedUnionSchema})
	is.NoErr(err)

	enc := NewEncodeProcessor(log.Nop())
	enc.SetSchemaRegistry(client)
	is.NoErr(enc.Configure(ctx, config.Config{
		"schema.strategy":              "preRegistered",
		"schema.preRegistered.subject": "nested-unions-value",
		"schema.preRegistered.version": "1",
	}))
	is.NoErr(enc.Open(ctx))
	t.Cleanup(func() { _ = enc.Teardown(ctx) })

	dec := NewDecodeProcessor(log.Nop())
	dec.SetSchemaRegistry(client)
	is.NoErr(dec.Configure(ctx, config.Config{"field": ".Payload.After"}))
	is.NoErr(dec.Open(ctx))
	t.Cleanup(func() { _ = dec.Teardown(ctx) })
	return enc, dec
}

func singleRecord(t *testing.T, r sdk.ProcessedRecord) sdk.SingleRecord {
	t.Helper()
	rec, ok := r.(sdk.SingleRecord)
	if !ok {
		t.Fatalf("expected a single record, got %#v", r)
	}
	return rec
}

// TestEncodeProcessor_SharedValueEncodedTwice is the engine-level regression
// test for ConduitIO/conduit-commons#342. Two records carry the same
// StructuredData, as happens when a record is fanned out or when Before and
// After share a nested value. Serde.Marshal used to wrap union values in the
// caller's own maps and slices, so the second encode saw the wrappers: it
// either failed or, when the wrapper was itself a valid branch (here the map
// branch), wrote the wrapper instead of the value.
// Invariant 6: no silent mangling; each record decodes to what was encoded.
func TestEncodeProcessor_SharedValueEncodedTwice(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	enc, _ := newNestedUnionProcessors(t)

	shared := opencdc.StructuredData{
		"a": []any{"bar"},
		"m": map[string]any{"k": "v"},
	}
	want := opencdc.StructuredData{
		"a": []any{"bar"},
		"m": map[string]any{"k": "v"},
	}

	got := enc.Process(ctx, []opencdc.Record{
		{Payload: opencdc.Change{After: shared}},
		{Payload: opencdc.Change{After: shared}},
	})
	is.Equal(len(got), 2)

	serde, err := cavro.Parse([]byte(nestedUnionSchema))
	is.NoErr(err)
	for i, r := range got {
		rec := singleRecord(t, r)
		var decoded opencdc.StructuredData
		is.NoErr(serde.Unmarshal(rec.Payload.After.Bytes()[5:], &decoded))
		if diff := cmp.Diff(want, decoded); diff != "" {
			t.Errorf("record %d decodes differently from what was encoded (-want +got):\n%s", i, diff)
		}
	}
	// The processor must not leave the union wrappers in the caller's value.
	if diff := cmp.Diff(want, shared); diff != "" {
		t.Errorf("encoding modified its input (-want +got):\n%s", diff)
	}
}

// TestDecodeEncode_NestedUnionRoundTrip decodes a record with unions nested in
// an array and a map and encodes the decoded value again. The second encode
// must produce the same bytes as the first (single-entry maps, so entry order
// cannot differ), and a second decode must see the same value.
func TestDecodeEncode_NestedUnionRoundTrip(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	enc, dec := newNestedUnionProcessors(t)

	in := opencdc.StructuredData{
		"a": []any{"bar"},
		"m": map[string]any{"k": "v"},
	}
	first := singleRecord(t, enc.Process(ctx, []opencdc.Record{{Payload: opencdc.Change{After: in}}})[0])

	decoded := singleRecord(t, dec.Process(ctx, []opencdc.Record{opencdc.Record(first)})[0])
	sd, ok := decoded.Payload.After.(opencdc.StructuredData)
	if !ok {
		t.Fatalf("expected StructuredData, got %T", decoded.Payload.After)
	}
	is.Equal(sd["a"], []any{"bar"})

	second := singleRecord(t, enc.Process(ctx, []opencdc.Record{{Payload: opencdc.Change{After: sd}}})[0])
	is.Equal(second.Payload.After.Bytes(), first.Payload.After.Bytes())

	again := singleRecord(t, dec.Process(ctx, []opencdc.Record{opencdc.Record(second)})[0])
	if diff := cmp.Diff(decoded.Payload.After, again.Payload.After); diff != "" {
		t.Errorf("value changed across decode/encode/decode (-first +second):\n%s", diff)
	}
}
