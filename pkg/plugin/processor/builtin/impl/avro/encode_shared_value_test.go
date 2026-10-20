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

// TestEncodeProcessor_SharedValueEncodedTwice is the engine-level regression
// test for ConduitIO/conduit-commons#342. Serde.Marshal used to wrap union
// values (for example "bar" into {"string":"bar"}) by writing into the
// caller's own maps and slices, so a nested value that another record, or the
// record's Before, still referenced was left wrapped. The schema's array items
// are a union that has a map branch, which makes a wrapped value look like a
// valid map: the second encode used to succeed and write the wrapper in place
// of the value.
//
// Invariant 6: every record must decode, with its own schema, to the value
// that was encoded, and the processor must not modify the record it is given.
func TestEncodeProcessor_SharedValueEncodedTwice(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	url := schemaregistrytest.TestSchemaRegistryURL(t)
	client, err := schemaregistry.NewClient(log.Nop(), sr.URLs(url))
	is.NoErr(err)

	const schema = `{"type":"record","name":"shared342","fields":[
		{"name":"a","type":{"type":"array","items":["null","string",{"type":"map","values":"string"}]}},
		{"name":"m","type":{"type":"map","values":["null","string","long",{"type":"array","items":"string"}]}}]}`
	_, err = client.CreateSchema(ctx, "shared342-value", sr.Schema{Type: sr.TypeAvro, Schema: schema})
	is.NoErr(err)

	p := NewEncodeProcessor(log.Nop())
	p.SetSchemaRegistry(client)
	is.NoErr(p.Configure(ctx, config.Config{
		"schema.strategy":              "preRegistered",
		"schema.preRegistered.subject": "shared342-value",
		"schema.preRegistered.version": "1",
	}))
	is.NoErr(p.Open(ctx))
	t.Cleanup(func() { _ = p.Teardown(ctx) })

	serde, err := cavro.Parse([]byte(schema))
	is.NoErr(err)

	// The nested values are shared by the two records and by Before, the way
	// fan-out branches, a shallow clone, or a connector that retains its data
	// share them.
	sharedArr := []any{"bar", map[string]any{"k": "v"}}
	sharedMap := map[string]any{"x": "s", "y": []any{"t"}}
	want := func() opencdc.StructuredData {
		return opencdc.StructuredData{
			"a": []any{"bar", map[string]any{"k": "v"}},
			"m": map[string]any{"x": "s", "y": []any{"t"}},
		}
	}
	rec := func() opencdc.Record {
		return opencdc.Record{Payload: opencdc.Change{
			Before: opencdc.StructuredData{"a": sharedArr, "m": sharedMap},
			After:  opencdc.StructuredData{"a": sharedArr, "m": sharedMap},
		}}
	}

	for i := range 2 {
		got := p.Process(ctx, []opencdc.Record{rec()})
		is.Equal(len(got), 1)
		out, ok := got[0].(sdk.SingleRecord)
		if !ok {
			t.Errorf("encode %d of a shared value did not produce a record: %#v", i+1, got[0])
			continue
		}
		encoded := out.Payload.After.Bytes()

		// The first five bytes are the Confluent wire format header.
		var decoded opencdc.StructuredData
		is.NoErr(serde.Unmarshal(encoded[5:], &decoded))
		if diff := cmp.Diff(want(), decoded); diff != "" {
			t.Errorf("encode %d: record decodes differently than it was encoded (-want +got):\n%s", i+1, diff)
		}

		// The processor must leave the shared containers as it found them.
		if diff := cmp.Diff(want(), opencdc.StructuredData{"a": sharedArr, "m": sharedMap}); diff != "" {
			t.Errorf("encode %d modified the shared input (-want +got):\n%s", i+1, diff)
		}
	}
}
