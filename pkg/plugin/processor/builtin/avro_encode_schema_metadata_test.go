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
	"fmt"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/lang"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-commons/schema"
	sdk "github.com/conduitio/conduit-connector-sdk"
	connschema "github.com/conduitio/conduit-connector-sdk/schema"
	sdkproc "github.com/conduitio/conduit-processor-sdk"
	sdkschema "github.com/conduitio/conduit-processor-sdk/schema"
	schemaregistry "github.com/conduitio/conduit-schema-registry"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/processor/egress"
	"github.com/matryer/is"
)

// captureDestination records what reaches a destination's Write, below the
// SDK destination middleware.
type captureDestination struct {
	sdk.UnimplementedDestination
	got []opencdc.Record
}

func (d *captureDestination) Write(_ context.Context, recs []opencdc.Record) (int, error) {
	d.got = append(d.got, recs...)
	return len(recs), nil
}

// TestAvroEncode_SchemaMetadataMatchesEncodedPayload is the regression test for
// avro.encode leaving the source's payload schema subject/version on a record
// whose payload is no longer in that schema (invariant 6). A destination with
// the SDK schema-extraction middleware (Kafka and others) reads that metadata,
// finds raw Avro bytes in the registry wire format and tries to decode them
// again with the source schema, which fails every record.
func TestAvroEncode_SchemaMetadataMatchesEncodedPayload(t *testing.T) {
	const avsc = `{"type":"record","name":"Row","namespace":"conduit.test","fields":[` +
		`{"name":"n","type":"int"},{"name":"s","type":"string"}]}`

	is := is.New(t)
	ctx := context.Background()

	sr, err := schemaregistry.NewSchemaRegistry(&inmemory.DB{})
	is.NoErr(err)
	reg := NewRegistry(log.Nop(), DefaultBuiltinProcessors, sr)
	ctx = contextWithSchemaService(ctx, reg.schemaService)

	p, err := reg.NewProcessor(ctx, plugin.FullName("builtin:avro.encode"), "enc", egress.Policy{})
	is.NoErr(err)
	is.NoErr(p.Configure(ctx, config.Config{
		"schema.strategy":             "autoRegister",
		"schema.autoRegister.subject": "avro-encode-out",
	}))
	is.NoErr(p.Open(ctx))

	// A record as a schema-aware source hands it over: payload encoded with its
	// schema, subject and version in the metadata.
	sch, err := sdkschema.Create(ctx, schema.TypeAvro, "avro-encode-in", []byte(avsc))
	is.NoErr(err)
	raw, err := sch.Marshal(opencdc.StructuredData{"n": 1, "s": "x"})
	is.NoErr(err)
	rec := opencdc.Record{Metadata: opencdc.Metadata{}, Payload: opencdc.Change{After: opencdc.RawData(raw)}}
	schema.AttachPayloadSchemaToRecord(rec, sch)

	out := p.Process(ctx, []opencdc.Record{rec})
	is.Equal(len(out), 1)
	sr1, ok := out[0].(sdkproc.SingleRecord)
	if !ok {
		t.Fatalf("expected SingleRecord, got %T: %+v", out[0], out[0])
	}
	encoded := opencdc.Record(sr1)
	_, isRaw := encoded.Payload.After.(opencdc.RawData)
	is.True(isRaw) // avro.encode produces raw Avro bytes

	// The destination reaches the same registry through the connector SDK.
	// Seed the source schema there, as a real registry would hold it.
	dctx := context.Background()
	_, err = connschema.Create(dctx, schema.TypeAvro, "avro-encode-in", []byte(avsc))
	is.NoErr(err)

	// Whatever the metadata says must not make a destination decode again.
	dest := &captureDestination{}
	mw := (&sdk.DestinationWithSchemaExtraction{
		PayloadEnabled: lang.Ptr(true),
		KeyEnabled:     lang.Ptr(false),
	}).Wrap(dest)
	n, err := mw.Write(dctx, []opencdc.Record{encoded})
	is.NoErr(err) // the destination must not try to decode the already-encoded payload
	is.Equal(n, 1)
	is.Equal(len(dest.got), 1)
	// and the payload reaches the destination as the encoded bytes
	gotRaw, isRaw := dest.got[0].Payload.After.(opencdc.RawData)
	is.True(isRaw)
	is.Equal(gotRaw.Bytes(), encoded.Payload.After.Bytes())
}

// TestAvroEncode_DecodeRoundTrip checks that avro.decode still reads what
// avro.encode wrote once the stale schema metadata is gone.
func TestAvroEncode_DecodeRoundTrip(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	sr, err := schemaregistry.NewSchemaRegistry(&inmemory.DB{})
	is.NoErr(err)
	reg := NewRegistry(log.Nop(), DefaultBuiltinProcessors, sr)
	ctx = contextWithSchemaService(ctx, reg.schemaService)

	enc, err := reg.NewProcessor(ctx, plugin.FullName("builtin:avro.encode"), "enc", egress.Policy{})
	is.NoErr(err)
	is.NoErr(enc.Configure(ctx, config.Config{
		"schema.strategy":             "autoRegister",
		"schema.autoRegister.subject": "avro-roundtrip",
	}))
	is.NoErr(enc.Open(ctx))
	dec, err := reg.NewProcessor(ctx, plugin.FullName("builtin:avro.decode"), "dec", egress.Policy{})
	is.NoErr(err)
	is.NoErr(dec.Configure(ctx, config.Config{}))
	is.NoErr(dec.Open(ctx))

	in := opencdc.Record{Metadata: opencdc.Metadata{}, Payload: opencdc.Change{After: opencdc.StructuredData{"n": 7, "s": "x"}}}
	encOut := enc.Process(ctx, []opencdc.Record{in})
	is.Equal(len(encOut), 1)
	encoded, ok := encOut[0].(sdkproc.SingleRecord)
	is.True(ok)

	decOut := dec.Process(ctx, []opencdc.Record{opencdc.Record(encoded)})
	is.Equal(len(decOut), 1)
	decoded, ok := decOut[0].(sdkproc.SingleRecord)
	if !ok {
		t.Fatalf("expected SingleRecord, got %T: %+v", decOut[0], decOut[0])
	}
	got, ok := decoded.Payload.After.(opencdc.StructuredData)
	is.True(ok)
	is.Equal(got["s"], "x")
	is.Equal(fmt.Sprint(got["n"]), "7")
}

// TestAvroEncode_OtherPayloadPart pins what happens to the part avro.encode did
// not touch. Before and After share the payload schema metadata, so encoding
// one removes the schema association of the other: it reaches the destination
// intact but as schema-less structured data, and the destination does not fail.
func TestAvroEncode_OtherPayloadPart(t *testing.T) {
	const avsc = `{"type":"record","name":"Row","namespace":"conduit.test","fields":[` +
		`{"name":"n","type":"int"},{"name":"s","type":"string"}]}`

	for field, tc := range map[string]struct{ encoded, other string }{
		".Payload.After":  {"After", "Before"},
		".Payload.Before": {"Before", "After"},
	} {
		t.Run(field, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			sr, err := schemaregistry.NewSchemaRegistry(&inmemory.DB{})
			is.NoErr(err)
			reg := NewRegistry(log.Nop(), DefaultBuiltinProcessors, sr)
			ctx = contextWithSchemaService(ctx, reg.schemaService)
			p, err := reg.NewProcessor(ctx, plugin.FullName("builtin:avro.encode"), "enc", egress.Policy{})
			is.NoErr(err)
			is.NoErr(p.Configure(ctx, config.Config{
				"field":                       field,
				"schema.strategy":             "autoRegister",
				"schema.autoRegister.subject": "avro-encode-part-" + tc.encoded,
			}))
			is.NoErr(p.Open(ctx))

			subject := "avro-encode-part-in-" + tc.encoded
			sch, err := sdkschema.Create(ctx, schema.TypeAvro, subject, []byte(avsc))
			is.NoErr(err)
			raw, err := sch.Marshal(opencdc.StructuredData{"n": 1, "s": "x"})
			is.NoErr(err)
			rec := opencdc.Record{Metadata: opencdc.Metadata{}, Payload: opencdc.Change{
				Before: opencdc.RawData(raw), After: opencdc.RawData(raw),
			}}
			schema.AttachPayloadSchemaToRecord(rec, sch)

			out := p.Process(ctx, []opencdc.Record{rec})
			is.Equal(len(out), 1)
			sr1, ok := out[0].(sdkproc.SingleRecord)
			if !ok {
				t.Fatalf("expected SingleRecord, got %T: %+v", out[0], out[0])
			}

			dctx := context.Background()
			_, err = connschema.Create(dctx, schema.TypeAvro, subject, []byte(avsc))
			is.NoErr(err)
			dest := &captureDestination{}
			mw := (&sdk.DestinationWithSchemaExtraction{PayloadEnabled: lang.Ptr(true), KeyEnabled: lang.Ptr(false)}).Wrap(dest)
			_, err = mw.Write(dctx, []opencdc.Record{opencdc.Record(sr1)})
			is.NoErr(err)
			is.Equal(len(dest.got), 1)

			part := func(r opencdc.Record, name string) opencdc.Data {
				if name == "Before" {
					return r.Payload.Before
				}
				return r.Payload.After
			}
			_, encIsRaw := part(dest.got[0], tc.encoded).(opencdc.RawData)
			is.True(encIsRaw) // the encoded part is the wire-format bytes
			other, otherIsStructured := part(dest.got[0], tc.other).(opencdc.StructuredData)
			is.True(otherIsStructured) // the other part is delivered intact, without a schema link
			is.Equal(other["s"], "x")
			_, hasSubject := dest.got[0].Metadata.GetPayloadSchemaSubject()
			is.True(hasSubject != nil) // no schema association remains
		})
	}
}
