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

package json

import (
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"
)

//nolint:govet // a more descriptive example description
func ExampleDecodeProcessor_rawKey() {
	p := NewDecodeProcessor(log.Nop())

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: `Decode record key as JSON`,
		Description: `This example takes a record containing a raw JSON string in
` + "`.Key`" + ` and converts it into structured data.`,
		Config: config.Config{"field": ".Key"},
		Have: opencdc.Record{
			Operation: opencdc.OperationCreate,
			Key:       opencdc.RawData(`{"after":{"data":4,"id":3}}`),
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationCreate,
			Key: opencdc.StructuredData{
				"after": map[string]interface{}{"data": float64(4), "id": float64(3)},
			},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,10 +1,15 @@
	//  {
	//    "position": null,
	//    "operation": "create",
	//    "metadata": null,
	// -  "key": "{\"after\":{\"data\":4,\"id\":3}}",
	// +  "key": {
	// +    "after": {
	// +      "data": 4,
	// +      "id": 3
	// +    }
	// +  },
	//    "payload": {
	//      "before": null,
	//      "after": null
	//    }
	//  }
}

//nolint:govet // a more descriptive example description
func ExampleDecodeProcessor_rawPayloadField() {
	p := NewDecodeProcessor(log.Nop())

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: "Decode nested field as JSON",
		Description: `This example takes a record containing a raw JSON string in
` + "`.Payload.Before.foo`" + ` and converts it into a map.`,
		Config: config.Config{"field": ".Payload.Before.foo"},
		Have: opencdc.Record{
			Operation: opencdc.OperationSnapshot,
			Payload: opencdc.Change{
				Before: opencdc.StructuredData{
					"foo": `{"before":{"data":4,"id":3},"baz":"bar"}`,
				},
			},
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationSnapshot,
			Payload: opencdc.Change{
				Before: opencdc.StructuredData{
					"foo": map[string]any{
						"before": map[string]any{"data": float64(4), "id": float64(3)},
						"baz":    "bar",
					},
				},
			},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,12 +1,18 @@
	//  {
	//    "position": null,
	//    "operation": "snapshot",
	//    "metadata": null,
	//    "key": null,
	//    "payload": {
	//      "before": {
	// -      "foo": "{\"before\":{\"data\":4,\"id\":3},\"baz\":\"bar\"}"
	// +      "foo": {
	// +        "baz": "bar",
	// +        "before": {
	// +          "data": 4,
	// +          "id": 3
	// +        }
	// +      }
	//      },
	//      "after": null
	//    }
	//  }
}
