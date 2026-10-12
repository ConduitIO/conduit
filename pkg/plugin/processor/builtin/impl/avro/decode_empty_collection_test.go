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
	"fmt"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/schemaregistry"
	"github.com/conduitio/conduit/pkg/schemaregistry/schemaregistrytest"
	"github.com/twmb/franz-go/pkg/sr"
)

// TestDecodeProcessor_EmptyArrayAndMapAreNonNil pins what avro.decode returns
// for an empty Avro array or map: an empty, non-nil []any{} / map[string]any{}.
// This is identical on v0.20.0 (conduit-commons v0.6.0, hamba/avro v2.31.0)
// and on conduit-commons v0.7.0 (iskorotkov/avro), so the codec swap does not
// change it; the test was run against both to establish that. It guards
// against a future codec upgrade changing nil-ness, which would show up in
// JSON output (null vs []) and in `== nil` checks.
func TestDecodeProcessor_EmptyArrayAndMapAreNonNil(t *testing.T) {
	ctx := context.Background()
	client, err := schemaregistry.NewClient(log.Nop(), sr.URLs(schemaregistrytest.TestSchemaRegistryURL(t)))
	if err != nil {
		t.Fatal(err)
	}
	schema := `{"type":"record","name":"EmptyColl","fields":[` +
		`{"name":"items","type":{"type":"array","items":"int"}},` +
		`{"name":"tags","type":{"type":"array","items":"string"}},` +
		`{"name":"m","type":{"type":"map","values":"int"}}]}`
	ss, err := client.CreateSchema(ctx, fmt.Sprintf("empty-coll-%d", time.Now().UnixNano()), sr.Schema{Type: sr.TypeAvro, Schema: schema})
	if err != nil {
		t.Fatal(err)
	}
	p := NewDecodeProcessor(log.Nop())
	p.SetSchemaRegistry(client)
	if err := p.Configure(ctx, config.Config{"field": ".Payload.After"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Open(ctx); err != nil {
		t.Fatal(err)
	}

	hdr, err := (&sr.ConfluentHeader{}).AppendEncode(nil, ss.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := opencdc.Record{
		Position:  opencdc.Position("empty-coll"),
		Operation: opencdc.OperationCreate,
		// Three zero bytes: an empty block ends each of the two arrays and the map.
		Payload: opencdc.Change{After: opencdc.RawData(append(hdr, 0, 0, 0))},
	}
	got := p.Process(ctx, []opencdc.Record{rec})
	single, ok := got[0].(sdk.SingleRecord)
	if !ok {
		t.Fatalf("expected sdk.SingleRecord, got %#v", got[0])
	}
	sd, ok := single.Payload.After.(opencdc.StructuredData)
	if !ok {
		t.Fatalf("expected StructuredData, got %T", single.Payload.After)
	}
	for _, k := range []string{"items", "tags"} {
		s, ok := sd[k].([]any)
		if !ok || s == nil || len(s) != 0 {
			t.Errorf("%s: want empty non-nil []any, got %#v", k, sd[k])
		}
	}
	if m, ok := sd["m"].(map[string]any); !ok || m == nil || len(m) != 0 {
		t.Errorf("m: want empty non-nil map[string]any, got %#v", sd["m"])
	}
}
