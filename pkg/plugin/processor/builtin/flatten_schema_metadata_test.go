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

// TestFlatten_SchemaMetadataAfterReshape runs flatten inside the SDK schema
// middleware, as a pipeline does, on a payload a schema-aware source
// encoded. flatten removes the payload schema metadata, so the middleware
// leaves the flat payload structured and registers no schema extracted from
// it under the source's subject. A nil after-image passes through with its
// metadata, and the middleware encodes the untouched before-image again.
func TestFlatten_SchemaMetadataAfterReshape(t *testing.T) {
	const avsc = `{"type":"record","name":"Row","namespace":"conduit.test","fields":[` +
		`{"name":"id","type":"int"},{"name":"nested","type":{"type":"record","name":"N","fields":[{"name":"s","type":"string"}]}}]}`

	for name, tc := range map[string]struct {
		nilAfter bool
	}{
		"flattened": {},
		"nil after": {nilAfter: true},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			sr, err := schemaregistry.NewSchemaRegistry(&inmemory.DB{})
			is.NoErr(err)
			reg := NewRegistry(log.Nop(), DefaultBuiltinProcessors, sr)
			ctx = contextWithSchemaService(ctx, reg.schemaService)

			p, err := reg.NewProcessor(ctx, plugin.FullName("builtin:flatten"), "flat", egress.Policy{})
			is.NoErr(err)
			is.NoErr(p.Configure(ctx, config.Config{}))
			is.NoErr(p.Open(ctx))

			const subject = "flatten-src"
			sch, err := sdkschema.Create(ctx, schema.TypeAvro, subject, []byte(avsc))
			is.NoErr(err)
			raw, err := sch.Marshal(opencdc.StructuredData{"id": 7, "nested": map[string]any{"s": "x"}})
			is.NoErr(err)
			rec := opencdc.Record{Metadata: opencdc.Metadata{}, Payload: opencdc.Change{Before: opencdc.RawData(raw), After: opencdc.RawData(raw)}}
			if tc.nilAfter {
				rec.Payload.After = nil
			}
			schema.AttachPayloadSchemaToRecord(rec, sch)

			out := p.Process(ctx, []opencdc.Record{rec})
			is.Equal(len(out), 1)
			got, ok := out[0].(sdkproc.SingleRecord)
			if !ok {
				t.Fatalf("expected SingleRecord, got %T: %+v", out[0], out[0])
			}
			_, subjErr := got.Metadata.GetPayloadSchemaSubject()
			if tc.nilAfter {
				is.NoErr(subjErr)                // untouched: the schema link stays
				is.Equal(got.Payload.After, nil) // the delete's after-image stays nil
				_, raw := got.Payload.Before.(opencdc.RawData)
				is.True(raw) // and the before-image is encoded with the source schema again
			} else {
				is.True(subjErr != nil) // the schema link is gone
				after, ok := got.Payload.After.(opencdc.StructuredData)
				is.True(ok)
				is.Equal(after["nested.s"], "x")
			}

			versions, err := sr.SchemaVersionsBySubject(ctx, subject)
			is.NoErr(err)
			is.Equal(versions, []int{1}) // no extracted schema registered
		})
	}
}
