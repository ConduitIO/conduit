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

package builtin

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-commons/schema"
	sdkproc "github.com/conduitio/conduit-processor-sdk"
	sdkschema "github.com/conduitio/conduit-processor-sdk/schema"
	schemaregistry "github.com/conduitio/conduit-schema-registry"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/processor/egress"
	"github.com/goccy/go-json"
	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"
	"github.com/twmb/franz-go/pkg/sr"
)

// smokeSourceSchema is the Avro schema the Postgres source attaches to an
// int4, numeric(14,2) and date column (smoke-20261105, wide table).
const smokeSourceSchema = `{"type":"record","name":"bench_source_0","namespace":"public","fields":[` +
	`{"name":"c00_int","type":"int"},` +
	`{"name":"c02_numeric","type":["null",{"type":"bytes","logicalType":"decimal","precision":14,"scale":2}]},` +
	`{"name":"c06_date","type":{"type":"int","logicalType":"date"}}]}`

// runSmokeChain runs a source-shaped record through field.set (adding __op)
// and avro.encode with the given settings, both with the default processor
// middleware, and returns the field types of the schema behind the wire
// header of the encoded payload.
func runSmokeChain(t *testing.T, fieldSet, avroEncode config.Config) map[string]string {
	t.Helper()
	is := is.New(t)
	ctx := context.Background()

	srv, err := schemaregistry.NewSchemaRegistry(&inmemory.DB{})
	is.NoErr(err)
	reg := NewRegistry(log.Nop(), DefaultBuiltinProcessors, srv)
	ctx = contextWithSchemaService(ctx, reg.schemaService)

	set, err := reg.NewProcessor(ctx, plugin.FullName("builtin:field.set"), "op", egress.Policy{})
	is.NoErr(err)
	is.NoErr(set.Configure(ctx, fieldSet))
	is.NoErr(set.Open(ctx))
	enc, err := reg.NewProcessor(ctx, plugin.FullName("builtin:avro.encode"), "avro", egress.Policy{})
	is.NoErr(err)
	is.NoErr(enc.Configure(ctx, avroEncode))
	is.NoErr(enc.Open(ctx))

	// The record as the Postgres source hands it over: the payload encoded
	// with the source schema, its subject and version in the metadata.
	sch, err := sdkschema.Create(ctx, schema.TypeAvro, "bench_source_0.payload", []byte(smokeSourceSchema))
	is.NoErr(err)
	row := opencdc.StructuredData{
		"c00_int":     int32(32),
		"c02_numeric": big.NewRat(3232, 100),
		"c06_date":    time.Date(2023, 11, 14, 0, 0, 0, 0, time.UTC),
	}
	raw, err := sch.Marshal(row)
	is.NoErr(err)
	rec := opencdc.Record{
		Operation: opencdc.OperationCreate,
		Metadata:  opencdc.Metadata{},
		Payload:   opencdc.Change{After: opencdc.RawData(raw)},
	}
	schema.AttachPayloadSchemaToRecord(rec, sch)

	out := set.Process(ctx, []opencdc.Record{rec})
	is.Equal(len(out), 1)
	afterSet, ok := out[0].(sdkproc.SingleRecord)
	if !ok {
		t.Fatalf("field.set: expected SingleRecord, got %T: %+v", out[0], out[0])
	}
	out = enc.Process(ctx, []opencdc.Record{opencdc.Record(afterSet)})
	is.Equal(len(out), 1)
	encoded, ok := out[0].(sdkproc.SingleRecord)
	if !ok {
		t.Fatalf("avro.encode: expected SingleRecord, got %T: %+v", out[0], out[0])
	}

	var h sr.ConfluentHeader
	id, body, err := h.DecodeID(encoded.Payload.After.Bytes())
	is.NoErr(err)
	writer, err := srv.SchemaByID(ctx, id)
	is.NoErr(err)

	// The values are unchanged whichever strategy wrote them.
	var decoded opencdc.StructuredData
	is.NoErr((schema.Schema{Type: schema.TypeAvro, Bytes: []byte(writer.Schema)}).Unmarshal(body, &decoded))
	is.Equal(decoded["__op"], "c")
	is.Equal(decoded["c02_numeric"].(*big.Rat).Cmp(big.NewRat(3232, 100)), 0)

	var parsed struct {
		Fields []struct {
			Name string          `json:"name"`
			Type json.RawMessage `json:"type"`
		} `json:"fields"`
	}
	is.NoErr(json.Unmarshal([]byte(writer.Schema), &parsed))
	types := map[string]string{}
	for _, f := range parsed.Fields {
		var v any
		is.NoErr(json.Unmarshal(f.Type, &v))
		b, err := json.Marshal(v)
		is.NoErr(err)
		types[f.Name] = string(b)
	}
	return types
}

// TestAvroEncode_FromRecord_KeepsSourceTypes is the regression test for the
// smoke-20261105 finding: Postgres attaches int, decimal(14,2) and date, and
// avro.encode with autoRegister registered long, decimal(31,10) and
// timestamp-micros, so Debezium-shaped consumers could not read the output.
// With fromRecord the schema behind the wire header is the source schema plus
// a nullable __op.
func TestAvroEncode_FromRecord_KeepsSourceTypes(t *testing.T) {
	got := runSmokeChain(t,
		config.Config{
			"field": ".Payload.After.__op",
			"value": "c",
			// Keep the source schema on the record; see the processor
			// description of strategy fromRecord.
			"sdk.schema.encode.payload.enabled": "false",
		},
		config.Config{
			"field":                     ".Payload.After",
			"schema.strategy":           "fromRecord",
			"schema.fromRecord.subject": "cdc.public.bench_source_0-value",
		},
	)
	want := map[string]string{
		"c00_int":     `"int"`,
		"c02_numeric": `["null",{"logicalType":"decimal","precision":14,"scale":2,"type":"bytes"}]`,
		"c06_date":    `{"logicalType":"date","type":"int"}`,
		"__op":        `["null","string"]`,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("writer schema field types (-want +got):\n%s", diff)
	}
}

// TestAvroEncode_AutoRegister_StillInfers pins that the default strategies
// are unchanged by fromRecord: autoRegister on the same chain still infers.
func TestAvroEncode_AutoRegister_StillInfers(t *testing.T) {
	got := runSmokeChain(t,
		config.Config{"field": ".Payload.After.__op", "value": "c"},
		config.Config{
			"field":                       ".Payload.After",
			"schema.strategy":             "autoRegister",
			"schema.autoRegister.subject": "cdc.public.bench_source_0-value",
		},
	)
	is := is.New(t)
	is.Equal(got["c00_int"], `"long"`)
	is.Equal(got["c06_date"], `{"logicalType":"timestamp-micros","type":"long"}`)
	is.Equal(got["c02_numeric"], `["null",{"logicalType":"decimal","precision":31,"scale":10,"type":"bytes"}]`)
}

// TestAvroEncode_FromRecord_AfterShapeChangeWithEncodeMiddleware pins the
// limitation the design doc lists as an open question: when the processor
// before avro.encode changes the shape with its schema-encode middleware on,
// the middleware registers an inferred schema and the record carries that, so
// fromRecord keeps the inferred types. If the processor SDK starts extending
// the carried schema instead, this test fails and the documented requirement
// (sdk.schema.encode.payload.enabled: false) can be dropped.
func TestAvroEncode_FromRecord_AfterShapeChangeWithEncodeMiddleware(t *testing.T) {
	got := runSmokeChain(t,
		config.Config{"field": ".Payload.After.__op", "value": "c"},
		config.Config{
			"field":                     ".Payload.After",
			"schema.strategy":           "fromRecord",
			"schema.fromRecord.subject": "cdc.public.bench_source_0-value",
		},
	)
	is := is.New(t)
	is.Equal(got["c00_int"], `"long"`)
}

// TestAvroEncode_FromRecord_ExcludedFieldAndDelete runs field.exclude and
// avro.encode (fromRecord) over the real middleware chain and the built-in
// registry: a field the pipeline removed is dropped from the writer schema
// (the other fields keep their source types), and a delete record whose After
// is empty passes through avro.encode unchanged instead of failing.
func TestAvroEncode_FromRecord_ExcludedFieldAndDelete(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	srv, err := schemaregistry.NewSchemaRegistry(&inmemory.DB{})
	is.NoErr(err)
	reg := NewRegistry(log.Nop(), DefaultBuiltinProcessors, srv)
	ctx = contextWithSchemaService(ctx, reg.schemaService)

	exclude, err := reg.NewProcessor(ctx, plugin.FullName("builtin:field.exclude"), "exclude", egress.Policy{})
	is.NoErr(err)
	is.NoErr(exclude.Configure(ctx, config.Config{
		"fields":                            ".Payload.After.c06_date",
		"sdk.schema.encode.payload.enabled": "false",
	}))
	is.NoErr(exclude.Open(ctx))
	enc, err := reg.NewProcessor(ctx, plugin.FullName("builtin:avro.encode"), "avro", egress.Policy{})
	is.NoErr(err)
	is.NoErr(enc.Configure(ctx, config.Config{
		"field":                     ".Payload.After",
		"schema.strategy":           "fromRecord",
		"schema.fromRecord.subject": "cdc.public.bench_source_0-value",
	}))
	is.NoErr(enc.Open(ctx))

	sch, err := sdkschema.Create(ctx, schema.TypeAvro, "bench_source_0.payload", []byte(smokeSourceSchema))
	is.NoErr(err)
	raw, err := sch.Marshal(opencdc.StructuredData{
		"c00_int":     int32(32),
		"c02_numeric": big.NewRat(3232, 100),
		"c06_date":    time.Date(2023, 11, 14, 0, 0, 0, 0, time.UTC),
	})
	is.NoErr(err)
	create := opencdc.Record{
		Operation: opencdc.OperationCreate,
		Metadata:  opencdc.Metadata{},
		Payload:   opencdc.Change{After: opencdc.RawData(raw)},
	}
	schema.AttachPayloadSchemaToRecord(create, sch)
	del := opencdc.Record{
		Operation: opencdc.OperationDelete,
		Metadata:  opencdc.Metadata{},
		Payload:   opencdc.Change{Before: opencdc.RawData(raw), After: opencdc.RawData{}},
	}
	schema.AttachPayloadSchemaToRecord(del, sch)

	run := func(p sdkproc.Processor, rec opencdc.Record) opencdc.Record {
		t.Helper()
		out := p.Process(ctx, []opencdc.Record{rec})
		is.Equal(len(out), 1)
		single, ok := out[0].(sdkproc.SingleRecord)
		if !ok {
			t.Fatalf("expected SingleRecord, got %T: %+v", out[0], out[0])
		}
		return opencdc.Record(single)
	}

	// Create: c06_date removed, the rest keeps the source types.
	got := run(enc, run(exclude, create))
	var h sr.ConfluentHeader
	id, body, err := h.DecodeID(got.Payload.After.Bytes())
	is.NoErr(err)
	writer, err := srv.SchemaByID(ctx, id)
	is.NoErr(err)
	var parsed struct {
		Fields []struct {
			Name string `json:"name"`
			Type any    `json:"type"`
		} `json:"fields"`
	}
	is.NoErr(json.Unmarshal([]byte(writer.Schema), &parsed))
	types := map[string]string{}
	for _, f := range parsed.Fields {
		b, err := json.Marshal(f.Type)
		is.NoErr(err)
		types[f.Name] = string(b)
	}
	want := map[string]string{
		"c00_int":     `"int"`,
		"c02_numeric": `["null",{"logicalType":"decimal","precision":14,"scale":2,"type":"bytes"}]`,
	}
	if diff := cmp.Diff(want, types); diff != "" {
		t.Fatalf("writer schema field types (-want +got):\n%s", diff)
	}
	var decoded opencdc.StructuredData
	is.NoErr((schema.Schema{Type: schema.TypeAvro, Bytes: []byte(writer.Schema)}).Unmarshal(body, &decoded))
	is.Equal(len(decoded), 2)
	is.Equal(decoded["c02_numeric"].(*big.Rat).Cmp(big.NewRat(3232, 100)), 0)

	// Delete: the empty After is not encoded, and the payload schema
	// metadata stays (it still describes Before).
	got = run(enc, del)
	is.Equal(got.Payload.After, opencdc.Data(opencdc.RawData{}))
	subject, err := got.Metadata.GetPayloadSchemaSubject()
	is.NoErr(err)
	is.Equal(subject, sch.Subject)
}
