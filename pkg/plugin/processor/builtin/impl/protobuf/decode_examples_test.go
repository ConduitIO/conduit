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

package protobuf

import (
	"context"
	"fmt"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"
	"github.com/conduitio/conduit/pkg/schemaregistry"
	"github.com/conduitio/conduit/pkg/schemaregistry/schemaregistrytest"
	"github.com/twmb/franz-go/pkg/sr"
)

func TestDecodeProcessorExample(t *testing.T) {
	// Issue #2534: use an OS-assigned port (0) instead of a hardcoded one to
	// avoid flakes when the port is already in use. The port/URL is not
	// captured in the generated example spec, only Summary/Description/Config/
	// Have/Want are, so this is safe to change.
	url, cleanup := schemaregistrytest.ExampleSchemaRegistryURL("TestDecodeProcessorExample", 0)
	t.Cleanup(cleanup)

	client, err := schemaregistry.NewClient(log.Nop(), sr.URLs(url))
	if err != nil {
		panic(fmt.Sprintf("failed to create schema registry client: %v", err))
	}

	subjSchema, err := client.CreateSchema(context.Background(), "example-decode", sr.Schema{
		Type:   sr.TypeProtobuf,
		Schema: `syntax = "proto3"; message record { string myString = 1; int32 myInt = 2; }`,
	})
	if err != nil {
		panic(fmt.Sprintf("failed to create schema: %v", err))
	}

	// Confluent wire: magic byte + the schema ID this registry actually
	// assigned + empty message index, then the protobuf payload for
	// record{myString:"bar", myInt:1}. Never hardcode the ID: under a shuffled
	// test run another example may have registered schema 1 first, so 1 would
	// resolve to the wrong (avro) schema.
	key, err := (&sr.ConfluentHeader{}).AppendEncode(nil, subjSchema.ID, []int{0})
	if err != nil {
		panic(fmt.Sprintf("failed to encode header: %v", err))
	}
	key = append(key, 0x0A, 0x03, 'b', 'a', 'r', 0x10, 0x01)

	p := NewDecodeProcessor(log.Nop())
	p.SetSchemaRegistry(client)

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: "Decode a record field in Confluent Protobuf format",
		Description: `This example shows the usage of the ` + "`protobuf.decode`" + ` processor.
The processor decodes the record's` + "`.Key`" + ` field using the schema that is downloaded from the
schema registry and needs to exist under the subject` + "`example-decode`" + `. In this example we use
the following schema:

` + "```proto" + `
syntax = "proto3";
message record {
  string myString = 1;
  int32 myInt = 2;
}
` + "```",
		Config: config.Config{
			"field": ".Key",
		},
		Have: opencdc.Record{
			Position:  opencdc.Position("test-position"),
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key1": "val1"},
			Key:       opencdc.RawData(key),
		},
		Want: sdk.SingleRecord{
			Position:  opencdc.Position("test-position"),
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key1": "val1"},
			Key: opencdc.StructuredData{
				"myString": "bar",
				"myInt":    int32(1),
			},
		},
	})

	// The decoded result is asserted above via RunExample's Want comparison.
	// There is deliberately no "Output:" comment: the raw Confluent key in the
	// "before" snapshot embeds the schema registry's auto-assigned ID, which is
	// not deterministic under -shuffle=on, so printing it would flake.
}
