// Copyright © 2024 Meroxa, Inc.
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

//go:build !integration

package avro

import (
	"context"
	"fmt"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"
	"github.com/conduitio/conduit/pkg/schemaregistry"
	"github.com/conduitio/conduit/pkg/schemaregistry/schemaregistrytest"
	"github.com/twmb/franz-go/pkg/sr"
)

//nolint:govet // a more descriptive example description
func ExampleEncodeProcessor_autoRegister() {
	url, cleanup := schemaregistrytest.ExampleSchemaRegistryURL("ExampleEncodeProcessor_autoRegister", 54322)
	defer cleanup()

	client, err := schemaregistry.NewClient(log.Nop(), sr.URLs(url))
	if err != nil {
		panic(fmt.Sprintf("failed to create schema registry client: %v", err))
	}

	p := NewEncodeProcessor(log.Nop()).(*encodeProcessor)
	p.SetSchemaRegistry(client)

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: "Auto-register schema",
		Description: `This example shows the usage of the ` + "`avro.encode`" + ` processor
with the ` + "`autoRegister`" + ` schema strategy. The processor encodes the record's
` + "`.Payload.After`" + ` field using the schema that is extracted from the data
and registered on the fly under the subject ` + "`example-autoRegister`" + `.`,
		Config: config.Config{
			"schema.strategy":             "autoRegister",
			"schema.autoRegister.subject": "example-autoRegister",
		},
		Have: opencdc.Record{
			Position:  opencdc.Position("test-position"),
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key1": "val1"},
			Payload: opencdc.Change{
				After: opencdc.StructuredData{
					"myString": "bar",
					"myInt":    1,
					"myFloat":  2.3,
					"myMap": map[string]any{
						"foo": true,
						"bar": 2.2,
					},
					"myStruct": opencdc.StructuredData{
						"foo": 1,
						"bar": false,
					},
				},
			},
		},
		Want: sdk.SingleRecord{
			Position:  opencdc.Position("test-position"),
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key1": "val1"},
			Payload: opencdc.Change{
				After: opencdc.RawData([]byte{0, 0, 0, 0, 1, 102, 102, 102, 102, 102, 102, 2, 64, 2, 154, 153, 153, 153, 153, 153, 1, 64, 1, 6, 98, 97, 114, 0, 2}),
			},
		}})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,24 +1,12 @@
	//  {
	//    "position": "dGVzdC1wb3NpdGlvbg==",
	//    "operation": "create",
	//    "metadata": {
	//      "key1": "val1"
	//    },
	//    "key": null,
	//    "payload": {
	//      "before": null,
	// -    "after": {
	// -      "myFloat": 2.3,
	// -      "myInt": 1,
	// -      "myMap": {
	// -        "bar": 2.2,
	// -        "foo": true
	// -      },
	// -      "myString": "bar",
	// -      "myStruct": {
	// -        "bar": false,
	// -        "foo": 1
	// -      }
	// -    }
	// +    "after": "\u0000\u0000\u0000\u0000\u0001ffffff\u0002@\u0002\ufffd\ufffd\ufffd\ufffd\ufffd\ufffd\u0001@\u0001\u0006bar\u0000\u0002"
	//    }
	//  }
}

//nolint:govet // a more descriptive example description
func ExampleEncodeProcessor_preRegistered() {
	url, cleanup := schemaregistrytest.ExampleSchemaRegistryURL("ExampleEncodeProcessor_preRegistered", 54322)
	defer cleanup()

	client, err := schemaregistry.NewClient(log.Nop(), sr.URLs(url))
	if err != nil {
		panic(fmt.Sprintf("failed to create schema registry client: %v", err))
	}

	_, err = client.CreateSchema(context.Background(), "example-preRegistered", sr.Schema{
		Type: sr.TypeAvro,
		Schema: `
{
  "type":"record",
  "name":"record",
  "fields":[
    {"name":"myString","type":"string"},
    {"name":"myInt","type":"int"}
  ]
}`,
	})
	if err != nil {
		panic(fmt.Sprintf("failed to create schema: %v", err))
	}

	p := NewEncodeProcessor(log.Nop()).(*encodeProcessor)
	p.SetSchemaRegistry(client)

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: "Pre-register schema",
		Description: `This example shows the usage of the ` + "`avro.encode`" + ` processor
with the ` + "`preRegistered`" + ` schema strategy. When using this strategy, the
schema has to be manually pre-registered. In this example we use the following schema:

` + "```json" + `
{
  "type":"record",
  "name":"record",
  "fields":[
    {"name":"myString","type":"string"},
    {"name":"myInt","type":"int"}
  ]
}
` + "```" + `

The processor encodes the record's` + "`.Key`" + ` field using the above schema.`,
		Config: config.Config{
			"schema.strategy":              "preRegistered",
			"schema.preRegistered.subject": "example-preRegistered",
			"schema.preRegistered.version": "1",
			"field":                        ".Key",
		},
		Have: opencdc.Record{
			Position:  opencdc.Position("test-position"),
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key1": "val1"},
			Key: opencdc.StructuredData{
				"myString": "bar",
				"myInt":    1,
			},
		},
		Want: sdk.SingleRecord{
			Position:  opencdc.Position("test-position"),
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key1": "val1"},
			Key:       opencdc.RawData([]byte{0, 0, 0, 0, 1, 6, 98, 97, 114, 2}),
		}})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,15 +1,12 @@
	//  {
	//    "position": "dGVzdC1wb3NpdGlvbg==",
	//    "operation": "create",
	//    "metadata": {
	//      "key1": "val1"
	//    },
	// -  "key": {
	// -    "myInt": 1,
	// -    "myString": "bar"
	// -  },
	// +  "key": "\u0000\u0000\u0000\u0000\u0001\u0006bar\u0002",
	//    "payload": {
	//      "before": null,
	//      "after": null
	//    }
	//  }
}
