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

package unwrap

import (
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"
)

func ExampleKafkaConnectProcessor() {
	p := NewKafkaConnectProcessor(log.Nop())

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: "Unwrap a Kafka Connect record",
		Description: `This example shows how to unwrap a Kafka Connect record.

The Kafka Connect record is serialized as a JSON string in the ` + "`.Payload.After`" + ` field (raw data).
The Kafka Connect record's payload will replace the [OpenCDC record](https://conduit.io/docs/using/opencdc-record)'s payload.

We also see how the key is unwrapped too. In this case, the key comes in as structured data.`,
		Config: config.Config{},
		Have: opencdc.Record{
			Position:  opencdc.Position("test position"),
			Operation: opencdc.OperationCreate,
			Metadata: opencdc.Metadata{
				"metadata-key": "metadata-value",
			},
			Key: opencdc.StructuredData{
				"payload": map[string]interface{}{
					"id": 27,
				},
				"schema": map[string]interface{}{},
			},
			Payload: opencdc.Change{
				After: opencdc.RawData(`{
"payload": {
  "description": "test2"
},
"schema": {}
}`),
			},
		},
		Want: sdk.SingleRecord{
			Position:  opencdc.Position("test position"),
			Operation: opencdc.OperationCreate,
			Metadata: opencdc.Metadata{
				"metadata-key": "metadata-value",
			},
			Key: opencdc.StructuredData{"id": 27},
			Payload: opencdc.Change{
				After: opencdc.StructuredData{
					"description": "test2",
				},
			},
		},
	})
	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,17 +1,16 @@
	//  {
	//    "position": "dGVzdCBwb3NpdGlvbg==",
	//    "operation": "create",
	//    "metadata": {
	//      "metadata-key": "metadata-value"
	//    },
	//    "key": {
	// -    "payload": {
	// -      "id": 27
	// +    "id": 27
	// -    },
	// +  },
	// -    "schema": {}
	// -  },
	// -  "payload": {
	// +  "payload": {
	//      "before": null,
	// -    "after": "{\n\"payload\": {\n  \"description\": \"test2\"\n},\n\"schema\": {}\n}"
	// +    "after": {
	// +      "description": "test2"
	// +    }
	//    }
	//  }
}
