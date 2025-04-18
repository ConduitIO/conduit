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

func ExampleDebeziumProcessor() {
	p := NewDebeziumProcessor(log.Nop())

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: "Unwrap a Debezium record",
		Description: `This example how to unwrap a Debezium record from a field nested in a record's
` + "`.Payload.After`" + ` field. It additionally shows how the key is unwrapped, and the metadata merged.`,
		Config: config.Config{
			"field": ".Payload.After.nested",
		},
		Have: opencdc.Record{
			Position:  opencdc.Position("test-position"),
			Operation: opencdc.OperationCreate,
			Key:       opencdc.RawData(`{"payload":"27"}`),
			Metadata:  opencdc.Metadata{"metadata-key": "metadata-value"},
			Payload: opencdc.Change{
				After: opencdc.StructuredData{
					"nested": `{
  "payload": {
    "after": {
      "description": "test1",
      "id": 27
    },
    "before": null,
    "op": "c",
    "source": {
      "opencdc.readAt": "1674061777225877000",
      "opencdc.version": "v1"
    },
    "transaction": null,
    "ts_ms": 1674061777225
  },
  "schema": {}
}`,
				},
			},
		},
		Want: sdk.SingleRecord{
			Position:  opencdc.Position("test-position"),
			Key:       opencdc.RawData("27"),
			Operation: opencdc.OperationCreate,
			Metadata: opencdc.Metadata{
				"metadata-key":    "metadata-value",
				"opencdc.readAt":  "1674061777225877000",
				"opencdc.version": "v1",
			},
			Payload: opencdc.Change{
				After: opencdc.StructuredData{
					"description": "test1",
					"id":          float64(27),
				},
			},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,14 +1,17 @@
	//  {
	//    "position": "dGVzdC1wb3NpdGlvbg==",
	//    "operation": "create",
	//    "metadata": {
	// -    "metadata-key": "metadata-value"
	// +    "metadata-key": "metadata-value",
	// -  },
	// -  "key": "{\"payload\":\"27\"}",
	// -  "payload": {
	// -    "before": null,
	// -    "after": {
	// -      "nested": "{\n  \"payload\": {\n    \"after\": {\n      \"description\": \"test1\",\n      \"id\": 27\n    },\n    \"before\": null,\n    \"op\": \"c\",\n    \"source\": {\n      \"opencdc.readAt\": \"1674061777225877000\",\n      \"opencdc.version\": \"v1\"\n    },\n    \"transaction\": null,\n    \"ts_ms\": 1674061777225\n  },\n  \"schema\": {}\n}"
	// +    "opencdc.readAt": "1674061777225877000",
	// +    "opencdc.version": "v1"
	// +  },
	// +  "key": "27",
	// +  "payload": {
	// +    "before": null,
	// +    "after": {
	// +      "description": "test1",
	// +      "id": 27
	//      }
	//    }
	//  }
}
