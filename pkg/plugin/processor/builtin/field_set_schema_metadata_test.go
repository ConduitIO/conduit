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
	"testing"

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
	"github.com/matryer/is"
)

// TestFieldSet_SchemaMetadataAfterReshape runs field.set inside the SDK schema
// middleware, as a pipeline does, on a payload a schema-aware source encoded.
//
//   - With "from" (or a non-string "type") the processor removes the payload
//     schema metadata, so the middleware leaves the payload structured and
//     registers no schema extracted from the values under the source's subject.
//   - With only "value" (the v0.1.0 behaviour) the metadata is kept, and the
//     middleware registers a new version of the subject, as it always has.
func TestFieldSet_SchemaMetadataAfterReshape(t *testing.T) {
	const avsc = `{"type":"record","name":"Row","namespace":"conduit.test","fields":[` +
		`{"name":"id","type":"int"},{"name":"s","type":"string"}]}`

	for name, tc := range map[string]struct {
		cfg          config.Config
		wantVersions []int
		wantSchema   bool
	}{
		"from":       {config.Config{"field": ".Payload.After.copy", "from": ".Payload.After.id"}, []int{1}, false},
		"typed":      {config.Config{"field": ".Payload.After.n", "value": "1", "type": "int"}, []int{1}, false},
		"value only": {config.Config{"field": ".Payload.After.extra", "value": "x"}, []int{1, 2}, true},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			sr, err := schemaregistry.NewSchemaRegistry(&inmemory.DB{})
			is.NoErr(err)
			reg := NewRegistry(log.Nop(), DefaultBuiltinProcessors, sr)
			ctx = contextWithSchemaService(ctx, reg.schemaService)

			p, err := reg.NewProcessor(ctx, plugin.FullName("builtin:field.set"), "set", egress.Policy{})
			is.NoErr(err)
			is.NoErr(p.Configure(ctx, tc.cfg))
			is.NoErr(p.Open(ctx))

			const subject = "field-set-src"
			sch, err := sdkschema.Create(ctx, schema.TypeAvro, subject, []byte(avsc))
			is.NoErr(err)
			raw, err := sch.Marshal(opencdc.StructuredData{"id": 7, "s": "x"})
			is.NoErr(err)
			rec := opencdc.Record{Metadata: opencdc.Metadata{}, Payload: opencdc.Change{After: opencdc.RawData(raw)}}
			schema.AttachPayloadSchemaToRecord(rec, sch)

			out := p.Process(ctx, []opencdc.Record{rec})
			is.Equal(len(out), 1)
			got, ok := out[0].(sdkproc.SingleRecord)
			if !ok {
				t.Fatalf("expected SingleRecord, got %T: %+v", out[0], out[0])
			}
			_, subjErr := got.Metadata.GetPayloadSchemaSubject()
			is.Equal(subjErr == nil, tc.wantSchema)
			_, structured := got.Payload.After.(opencdc.StructuredData)
			is.Equal(structured, !tc.wantSchema) // without a schema the payload stays structured

			versions, err := sr.SchemaVersionsBySubject(ctx, subject)
			is.NoErr(err)
			is.Equal(versions, tc.wantVersions)
		})
	}
}
