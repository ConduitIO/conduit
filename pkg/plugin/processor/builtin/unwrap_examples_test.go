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

package builtin

import (
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/unwrap"
)

//nolint:govet // we're using a more descriptive name of example
func ExampleUnwrapOpenCDC() {
	p := unwrap.NewOpenCDCProcessor(log.Nop())

	RunExample(p, example{
		Description: "",
		Config:      map[string]string{},
		Have: opencdc.Record{
			Position:  opencdc.Position("wrapping position"),
			Key:       opencdc.RawData("wrapping key"),
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{},
			Payload: opencdc.Change{
				Before: nil,
				After: opencdc.StructuredData{
					"position":  opencdc.Position("test-position"),
					"operation": opencdc.OperationUpdate,
					"key": map[string]interface{}{
						"id": "test-key",
					},
					"metadata": opencdc.Metadata{},
					"payload": opencdc.Change{
						After: opencdc.StructuredData{
							"msg":       "string 0e8955b3-7fb5-4dda-8064-e10dc007f00d",
							"sensor_id": 1250383582,
							"triggered": false,
						},
					},
				},
			},
		},
		Want: sdk.SingleRecord{
			Position:  opencdc.Position("wrapping position"),
			Operation: opencdc.OperationUpdate,
			Key: opencdc.StructuredData{
				"id": "test-key",
			},
			Metadata: opencdc.Metadata{},
			Payload: opencdc.Change{
				After: opencdc.StructuredData{
					"msg":       "string 0e8955b3-7fb5-4dda-8064-e10dc007f00d",
					"sensor_id": 1250383582,
					"triggered": false,
				},
			},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,25 +1,16 @@
	//  {
	//    "position": "d3JhcHBpbmcgcG9zaXRpb24=",
	// -  "operation": "create",
	// +  "operation": "update",
	//    "metadata": {},
	// -  "key": "wrapping key",
	// +  "key": {
	// +    "id": "test-key"
	// +  },
	//    "payload": {
	//      "before": null,
	//      "after": {
	// -      "key": {
	// -        "id": "test-key"
	// -      },
	// -      "metadata": {},
	// -      "operation": "update",
	// -      "payload": {
	// -        "before": null,
	// -        "after": {
	// -          "msg": "string 0e8955b3-7fb5-4dda-8064-e10dc007f00d",
	// -          "sensor_id": 1250383582,
	// -          "triggered": false
	// -        }
	// -      },
	// +      "msg": "string 0e8955b3-7fb5-4dda-8064-e10dc007f00d",
	// +      "sensor_id": 1250383582,
	// -      "position": "dGVzdC1wb3NpdGlvbg=="
	// +      "triggered": false
	//      }
	//    }
	//  }
}

//nolint:govet // we're using a more descriptive name of example
func ExampleUnwrapKafkaConnect() {
	p := unwrap.NewKafkaConnectProcessor(log.Nop())

	RunExample(p, example{
		Description: `This example shows how to unwrap a Kafka Connect record.

The Kafka Connect record is serialized as a JSON string in the .Payload.After field (raw data).
The Kafka Connect record's payload will replace the OpenCDC record's payload.

We also see how the key is unwrapped too. In this case, the key comes in as structured data.
`,
		Config: map[string]string{},
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

//nolint:govet // we're using a more descriptive name of example
func ExampleUnwrapDebezium() {
	p := unwrap.NewDebezium(log.Nop())

	RunExample(p, example{
		Description: `This example how to unwrap a Debezium record from a field nested in a record's
.Payload.After field. It additionally shows how the key is unwrapped, and the metadata merged.`,
		Config: map[string]string{
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
	// -      "nested": "{\n\t\t\t\t\t\t \"payload\": {\n\t\t\t\t\t\t   \"after\": {\n\t\t\t\t\t\t\t \"description\": \"test1\",\n\t\t\t\t\t\t\t \"id\": 27\n\t\t\t\t\t\t   },\n\t\t\t\t\t\t   \"before\": null,\n\t\t\t\t\t\t   \"op\": \"c\",\n\t\t\t\t\t\t   \"source\": {\n\t\t\t\t\t\t\t \"opencdc.readAt\": \"1674061777225877000\",\n\t\t\t\t\t\t\t \"opencdc.version\": \"v1\"\n\t\t\t\t\t\t   },\n\t\t\t\t\t\t   \"transaction\": null,\n\t\t\t\t\t\t   \"ts_ms\": 1674061777225\n\t\t\t\t\t\t },\n\t\t\t\t\t\t \"schema\": {} \n\t\t\t\t\t\t}"
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
