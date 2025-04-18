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

package base64

import (
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"
)

func ExampleEncodeProcessor_rawData() {
	p := NewEncodeProcessor(log.Nop())
	exampleutil.RunExample(p, exampleutil.Example{
		Summary: "Encode record key to base64",
		Description: `TThis example takes a record containing raw data in
` + "`.Key`" + ` and converts it into a base64 encoded string.`,
		Config: config.Config{
			"field": ".Key",
		},
		Have: opencdc.Record{
			Position:  opencdc.Position("test-position"),
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key1": "val1"},
			Key:       opencdc.RawData("test-key"),
			Payload: opencdc.Change{
				After: opencdc.StructuredData{
					"foo": "bar",
				},
			},
		},
		Want: sdk.SingleRecord{
			Position:  opencdc.Position("test-position"),
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key1": "val1"},
			Key:       opencdc.RawData("dGVzdC1rZXk="),
			Payload: opencdc.Change{
				After: opencdc.StructuredData{
					"foo": "bar",
				},
			},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,14 +1,14 @@
	//  {
	//    "position": "dGVzdC1wb3NpdGlvbg==",
	//    "operation": "create",
	//    "metadata": {
	//      "key1": "val1"
	//    },
	// -  "key": "test-key",
	// +  "key": "dGVzdC1rZXk=",
	//    "payload": {
	//      "before": null,
	//      "after": {
	//        "foo": "bar"
	//      }
	//    }
	//  }
}

func ExampleEncodeProcessor_stringField() {
	p := NewEncodeProcessor(log.Nop())
	exampleutil.RunExample(p, exampleutil.Example{
		Summary: "Encode nested value to base64",
		Description: `This example takes a record containing a string in
` + "`.Payload.Before.foo`" + ` and converts it into a base64 encoded string.`,
		Config: config.Config{
			"field": ".Payload.After.foo",
		},
		Have: opencdc.Record{
			Position:  opencdc.Position("test-position"),
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key1": "val1"},
			Key:       opencdc.RawData("test-key"),
			Payload: opencdc.Change{
				After: opencdc.StructuredData{
					"foo": "bar",
				},
			},
		},
		Want: sdk.SingleRecord{
			Position:  opencdc.Position("test-position"),
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key1": "val1"},
			Key:       opencdc.RawData("test-key"),
			Payload: opencdc.Change{
				After: opencdc.StructuredData{
					"foo": "YmFy",
				},
			},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,14 +1,14 @@
	//  {
	//    "position": "dGVzdC1wb3NpdGlvbg==",
	//    "operation": "create",
	//    "metadata": {
	//      "key1": "val1"
	//    },
	//    "key": "test-key",
	//    "payload": {
	//      "before": null,
	//      "after": {
	// -      "foo": "bar"
	// +      "foo": "YmFy"
	//      }
	//    }
	//  }
}
