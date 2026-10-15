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
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-commons/schema"
	sdk "github.com/conduitio/conduit-processor-sdk"
	sdkschema "github.com/conduitio/conduit-processor-sdk/schema"
	schemaregistry "github.com/conduitio/conduit-schema-registry"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/processor/egress"
	"github.com/matryer/is"
)

// TestBuiltinProcessor_SchemaFidelity is the regression test for built-in
// processors re-encoding records with a schema rebuilt from decoded values
// (conduit-processor-sdk#170, invariant 6). Conduit wraps every built-in
// processor in the SDK's schema middleware, so a processor that does not touch
// the payload, or only writes values that fit its schema, must hand back the
// exact schema the record came in with: date stays date, enum stays enum, int
// stays int, and no new schema version is registered.
func TestBuiltinProcessor_SchemaFidelity(t *testing.T) {
	const avsc = `{"type":"record","name":"Row","namespace":"conduit.test","fields":[` +
		`{"name":"n","type":"int"},` +
		`{"name":"d","type":{"type":"int","logicalType":"date"}},` +
		`{"name":"c","type":{"type":"enum","name":"Color","symbols":["RED","GREEN"]}}]}`

	testCases := []struct {
		name  string
		field string
		value string
		// keepsSchema is false when the processor writes a value that does
		// not fit the schema, which must take the extracted-schema fallback
		// and keep the value.
		keepsSchema bool
	}{
		{"metadata only, payload untouched", ".Metadata.touched", "yes", true},
		{"payload value that fits the enum", ".Payload.After.c", "RED", true},
		{"payload field the schema lacks", ".Payload.After.added", "new", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()

			sr, err := schemaregistry.NewSchemaRegistry(&inmemory.DB{})
			is.NoErr(err)
			reg := NewRegistry(log.Nop(), DefaultBuiltinProcessors, sr)
			p, err := reg.NewProcessor(ctx, plugin.FullName("builtin:field.set"), "fidelity", egress.Policy{})
			is.NoErr(err)
			is.NoErr(p.Configure(ctx, config.Config{"field": tc.field, "value": tc.value}))

			subject := strings.NewReplacer("/", ".", " ", "_", ",", "").Replace(t.Name())
			sch, err := sdkschema.Create(ctx, schema.TypeAvro, subject, []byte(avsc))
			is.NoErr(err)
			raw, err := sch.Marshal(opencdc.StructuredData{
				"n": 1,
				"d": time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC),
				"c": "GREEN",
			})
			is.NoErr(err)
			rec := opencdc.Record{Metadata: opencdc.Metadata{}, Payload: opencdc.Change{After: opencdc.RawData(raw)}}
			schema.AttachPayloadSchemaToRecord(rec, sch)

			out := p.Process(ctx, []opencdc.Record{rec})
			is.Equal(len(out), 1)
			got, ok := out[0].(sdk.SingleRecord)
			if !ok {
				t.Fatalf("expected SingleRecord, got %T: %+v", out[0], out[0])
			}

			version, err := got.Metadata.GetPayloadSchemaVersion()
			is.NoErr(err)
			outSch, err := sdkschema.Get(ctx, subject, version)
			is.NoErr(err)
			var decoded opencdc.StructuredData
			is.NoErr(outSch.Unmarshal(got.Payload.After.Bytes(), &decoded))

			if !tc.keepsSchema {
				is.True(version != sch.Version) // a field the schema lacks must not be dropped into it
				is.Equal(decoded["added"], tc.value)
				return
			}
			if version != sch.Version || string(outSch.Bytes) != avsc {
				t.Fatalf("schema changed for a payload that still fits it:\n in  %s:%d %s\n out %s:%d %s",
					sch.Subject, sch.Version, sch.Bytes, outSch.Subject, outSch.Version, outSch.Bytes)
			}
			if tc.field == ".Metadata.touched" {
				is.Equal(got.Payload.After.Bytes(), raw) // untouched payload keeps its exact bytes
			} else {
				is.Equal(decoded["c"], tc.value)
			}
		})
	}
}
