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

func ExampleOpenCDCProcessor() {
	p := NewOpenCDCProcessor(log.Nop())

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: "Unwrap an [OpenCDC record](https://conduit.io/docs/using/opencdc-record)",
		Description: "In this example we use the `unwrap.opencdc` processor to unwrap the [OpenCDC record](https://conduit.io/docs/using/opencdc-record) found in the " +
			"record's `.Payload.After` field.",
		Config: config.Config{},
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
