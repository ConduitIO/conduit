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

func ExampleDecodeProcessor() {
	url, cleanup := schemaregistrytest.ExampleSchemaRegistryURL("ExampleDecodeProcessor", 54322)
	defer cleanup()

	client, err := schemaregistry.NewClient(log.Nop(), sr.URLs(url))
	if err != nil {
		panic(fmt.Sprintf("failed to create schema registry client: %v", err))
	}

	_, err = client.CreateSchema(context.Background(), "example-decode", sr.Schema{
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

	p := NewDecodeProcessor(log.Nop())
	p.SetSchemaRegistry(client)

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: "Decode a record field in Avro format",
		Description: `This example shows the usage of the ` + "`avro.decode`" + ` processor.
The processor decodes the record's` + "`.Key`" + ` field using the schema that is
downloaded from the schema registry and needs to exist under the subject` + "`example-decode`" + `.
In this example we use the following schema:

` + "```json" + `
{
  "type":"record",
  "name":"record",
  "fields":[
    {"name":"myString","type":"string"},
    {"name":"myInt","type":"int"}
  ]
}
` + "```",
		Config: config.Config{
			"field": ".Key",
		},
		Have: opencdc.Record{
			Position:  opencdc.Position("test-position"),
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key1": "val1"},
			Key:       opencdc.RawData([]byte{0, 0, 0, 0, 1, 6, 98, 97, 114, 2}),
		},
		Want: sdk.SingleRecord{
			Position:  opencdc.Position("test-position"),
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key1": "val1"},
			Key: opencdc.StructuredData{
				"myString": "bar",
				"myInt":    1,
			},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,12 +1,15 @@
	//  {
	//    "position": "dGVzdC1wb3NpdGlvbg==",
	//    "operation": "create",
	//    "metadata": {
	//      "key1": "val1"
	//    },
	// -  "key": "\u0000\u0000\u0000\u0000\u0001\u0006bar\u0002",
	// +  "key": {
	// +    "myInt": 1,
	// +    "myString": "bar"
	// +  },
	//    "payload": {
	//      "before": null,
	//      "after": null
	//    }
	//  }
}
