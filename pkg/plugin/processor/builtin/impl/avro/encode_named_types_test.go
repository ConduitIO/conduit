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
	"math/big"
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

// TestEncodeProcessor_SharedTypeNamesAcrossSubjects is the engine-level
// regression test for ConduitIO/conduit-commons#305. It models two pipelines
// in one Conduit process, each with an avro.encode processor using a
// pre-registered subject. Both subjects define named types R, E and D with
// the same names and different contents, and use them by name inside arrays.
// All encoders in one process used to share the codec's encoder cache, keyed
// by a string in which a reference is only its name, so whichever subject
// encoded second wrote its records with the first subject's encoders:
// record fields swapped, enum symbols swapped, decimals at the wrong scale.
// Invariant 6: each record must decode, with its own schema, to the value
// that was encoded.
func TestEncodeProcessor_SharedTypeNamesAcrossSubjects(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	url := schemaregistrytest.TestSchemaRegistryURL(t)
	client, err := schemaregistry.NewClient(log.Nop(), sr.URLs(url))
	is.NoErr(err)

	schema := func(name, rFields, eSymbols, scale string) string {
		return `{"type":"record","name":"` + name + `","fields":[` +
			`{"name":"top_r","type":["null",{"type":"record","name":"R","fields":[` + rFields + `]}]},` +
			`{"name":"top_e","type":["null",{"type":"enum","name":"E","symbols":[` + eSymbols + `]}]},` +
			`{"name":"top_d","type":["null",{"type":"fixed","name":"D","size":8,"logicalType":"decimal","precision":10,"scale":` + scale + `}]},` +
			`{"name":"a","type":{"type":"array","items":"R"}},` +
			`{"name":"e","type":{"type":"array","items":"E"}},` +
			`{"name":"d","type":{"type":"array","items":"D"}}]}`
	}
	subjects := map[string]string{
		"pipeline1-value": schema("one", `{"name":"p","type":"long"},{"name":"q","type":"long"}`, `"X","Y"`, "6"),
		"pipeline2-value": schema("two", `{"name":"q","type":"long"},{"name":"p","type":"long"}`, `"Y","X"`, "2"),
	}
	for subject, s := range subjects {
		_, err := client.CreateSchema(ctx, subject, sr.Schema{Type: sr.TypeAvro, Schema: s})
		is.NoErr(err)
	}

	value := func() opencdc.StructuredData {
		return opencdc.StructuredData{
			"top_r": nil,
			"top_e": nil,
			"top_d": nil,
			"a":     []any{map[string]any{"p": int64(1), "q": int64(2)}},
			"e":     []any{"X"},
			"d":     []any{big.NewRat(3, 2)},
		}
	}

	// The order matters: the first subject to encode used to own the cache.
	for _, subject := range []string{"pipeline1-value", "pipeline2-value"} {
		p := NewEncodeProcessor(log.Nop())
		p.SetSchemaRegistry(client)
		is.NoErr(p.Configure(ctx, config.Config{
			"schema.strategy":              "preRegistered",
			"schema.preRegistered.subject": subject,
			"schema.preRegistered.version": "1",
		}))
		is.NoErr(p.Open(ctx))

		got := p.Process(ctx, []opencdc.Record{{Payload: opencdc.Change{After: value()}}})
		is.Equal(len(got), 1)
		rec, ok := got[0].(sdk.SingleRecord)
		if !ok {
			t.Fatalf("%s: expected a single record, got %#v", subject, got[0])
		}
		encoded := rec.Payload.After.Bytes()

		// Decode with a Serde of this subject's own schema. The first five
		// bytes are the Confluent wire format header.
		serde, err := cavro.Parse([]byte(subjects[subject]))
		is.NoErr(err)
		var decoded opencdc.StructuredData
		is.NoErr(serde.Unmarshal(encoded[5:], &decoded))

		if diff := cmp.Diff(value(), decoded, cmp.Comparer(func(a, b *big.Rat) bool { return a.Cmp(b) == 0 })); diff != "" {
			t.Errorf("%s: record decodes differently with its own schema (-encoded +decoded):\n%s", subject, diff)
		}
		is.NoErr(p.Teardown(ctx))
	}
}
