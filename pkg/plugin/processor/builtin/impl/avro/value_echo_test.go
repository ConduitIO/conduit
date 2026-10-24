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

// Errors reach the DLQ, the API and CLI output, so none of them may repeat
// the record value that failed to decode or encode.

func processorError(t *testing.T, out []sdk.ProcessedRecord) error {
	t.Helper()
	if len(out) != 1 {
		t.Fatalf("want 1 record, got %d", len(out))
	}
	er, ok := out[0].(sdk.ErrorRecord)
	if !ok {
		t.Fatalf("want an error record, got %T", out[0])
	}
	return er.Error
}

func TestDecodeProcessor_CodecErrorDoesNotContainTheValue(t *testing.T) {
	is := is.New(t)
	p, id := newDecodeProcessorWithSchema(t, `{"type":"record","name":"r","fields":[{"name":"a","type":"string"}]}`)

	// field a claims 60 bytes and carries a short secret: a real codec failure
	payload := append(zigzag(nil, 60), []byte("customer-SECRET-4711")...)
	err := processorError(t, p.Process(context.Background(), []opencdc.Record{confluentRecord(t, id, payload)}))

	is.True(strings.Contains(err.Error(), "failed to unmarshal data with schema"))
	is.True(strings.Contains(err.Error(), "details not shown"))
	is.True(!strings.Contains(err.Error(), "SECRET"))
	is.True(!schemaregistry.IsAvroLimitError(err))
}

func TestEncodeProcessor_InvalidJSONDoesNotContainTheValue(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		field string
		after any
	}{
		"raw data":  {".Payload.After", opencdc.RawData(`{"a": customer-SECRET-4711}`)},
		"string":    {".Payload.After.foo", map[string]any{"foo": `{"a": customer-SECRET-4711}`}},
		"raw bytes": {".Payload.After.foo", map[string]any{"foo": []byte(`{"a": customer-SECRET-4711}`)}},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			p := NewEncodeProcessor(log.Nop())
			is.NoErr(p.Configure(ctx, config.Config{
				"schema.strategy": "autoRegister", "schema.autoRegister.subject": "s", "field": tc.field,
			}))
			after := tc.after
			if m, ok := after.(map[string]any); ok {
				after = opencdc.StructuredData(m)
			}
			err := processorError(t, p.Process(ctx, []opencdc.Record{{Payload: opencdc.Change{After: after.(opencdc.Data)}}}))
			is.True(strings.Contains(err.Error(), "failed unmarshalling JSON"))
			is.True(strings.Contains(err.Error(), "invalid JSON syntax at offset"))
			is.True(!strings.Contains(err.Error(), "SECRET"))
		})
	}
}

func TestEncoder_CodecErrorDoesNotContainTheValue(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	client, err := schemaregistry.NewClient(log.Nop(), sr.URLs(schemaregistrytest.TestSchemaRegistryURL(t)))
	is.NoErr(err)
	subject := fmt.Sprintf("enc-%d", time.Now().UnixNano())
	_, err = client.CreateSchema(ctx, subject, sr.Schema{Type: sr.TypeAvro, Schema: `{"type":"record","name":"r","fields":[{"name":"a","type":"int"}]}`})
	is.NoErr(err)

	p := NewEncodeProcessor(log.Nop())
	p.SetSchemaRegistry(client)
	is.NoErr(p.Configure(ctx, config.Config{"schema.strategy": "preRegistered", "schema.preRegistered.subject": subject, "schema.preRegistered.version": "1"}))
	is.NoErr(p.Open(ctx))

	rec := opencdc.Record{Payload: opencdc.Change{After: opencdc.StructuredData{"a": "customer-SECRET-4711"}}}
	e := processorError(t, p.Process(ctx, []opencdc.Record{rec}))
	is.True(strings.Contains(e.Error(), "failed to marshal data with schema"))
	is.True(strings.Contains(e.Error(), "details not shown"))
	is.True(!strings.Contains(e.Error(), "SECRET"))
}
