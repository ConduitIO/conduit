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

package field

import (
	"time"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"
)

//nolint:govet // a more descriptive example description
func ExampleConvertProcessor_stringToInt() {
	p := NewConvertProcessor(log.Nop())

	exampleutil.RunExample(p, exampleutil.Example{
		Summary:     "Convert `string` to `int`",
		Description: "This example takes the string in field `.Key.id` and changes its data type to `int`.",
		Config:      config.Config{"field": ".Key.id", "type": "int"},
		Have: opencdc.Record{
			Operation: opencdc.OperationUpdate,
			Key:       opencdc.StructuredData{"id": "123"},
			Payload:   opencdc.Change{After: opencdc.StructuredData{"foo": "bar"}},
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationUpdate,
			Key:       opencdc.StructuredData{"id": 123},
			Payload:   opencdc.Change{After: opencdc.StructuredData{"foo": "bar"}},
		}})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,14 +1,14 @@
	//  {
	//    "position": null,
	//    "operation": "update",
	//    "metadata": null,
	//    "key": {
	// -    "id": "123"
	// +    "id": 123
	//    },
	//    "payload": {
	//      "before": null,
	//      "after": {
	//        "foo": "bar"
	//      }
	//    }
	//  }
}

//nolint:govet // a more descriptive example description
func ExampleConvertProcessor_intToBool() {
	p := NewConvertProcessor(log.Nop())

	exampleutil.RunExample(p, exampleutil.Example{
		Summary:     "Convert `int` to `bool`",
		Description: "This example takes the `int` in field `.Payload.After.done` and changes its data type to `bool`.",
		Config:      config.Config{"field": ".Payload.After.done", "type": "bool"},
		Have: opencdc.Record{
			Operation: opencdc.OperationUpdate,
			Key:       opencdc.StructuredData{"id": "123"},
			Payload:   opencdc.Change{After: opencdc.StructuredData{"done": 1}},
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationUpdate,
			Key:       opencdc.StructuredData{"id": "123"},
			Payload:   opencdc.Change{After: opencdc.StructuredData{"done": true}},
		}})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,14 +1,14 @@
	//  {
	//    "position": null,
	//    "operation": "update",
	//    "metadata": null,
	//    "key": {
	//      "id": "123"
	//    },
	//    "payload": {
	//      "before": null,
	//      "after": {
	// -      "done": 1
	// +      "done": true
	//      }
	//    }
	//  }
}

//nolint:govet // a more descriptive example description
func ExampleConvertProcessor_floatToString() {
	p := NewConvertProcessor(log.Nop())

	exampleutil.RunExample(p, exampleutil.Example{
		Summary:     "Convert `float` to `string`",
		Description: "This example takes the `float` in field `.Key.id` and changes its data type to `string`.",
		Config:      config.Config{"field": ".Key.id", "type": "string"},
		Have: opencdc.Record{
			Operation: opencdc.OperationUpdate,
			Key:       opencdc.StructuredData{"id": 123.345},
			Payload:   opencdc.Change{After: opencdc.StructuredData{"foo": "bar"}},
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationUpdate,
			Key:       opencdc.StructuredData{"id": "123.345"},
			Payload:   opencdc.Change{After: opencdc.StructuredData{"foo": "bar"}},
		}})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,14 +1,14 @@
	//  {
	//    "position": null,
	//    "operation": "update",
	//    "metadata": null,
	//    "key": {
	// -    "id": 123.345
	// +    "id": "123.345"
	//    },
	//    "payload": {
	//      "before": null,
	//      "after": {
	//        "foo": "bar"
	//      }
	//    }
	//  }
}

//nolint:govet // a more descriptive example description
func ExampleConvertProcessor_intTotime() {
	p := NewConvertProcessor(log.Nop())

	timeObj := time.Date(2024, 1, 2, 12, 34, 56, 123456789, time.UTC)

	exampleutil.RunExample(p, exampleutil.Example{
		Summary:     "Convert `string` to `time`",
		Description: "This example takes an `int` in field `.Payload.After.createdAt` and parses it as a unix timestamp into a `time.Time` value.",
		Config:      config.Config{"field": ".Payload.After.createdAt", "type": "time"},
		Have: opencdc.Record{
			Operation: opencdc.OperationCreate,
			Key:       opencdc.StructuredData{"id": 123.345},
			Payload:   opencdc.Change{After: opencdc.StructuredData{"createdAt": timeObj.UnixNano()}},
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationCreate,
			Key:       opencdc.StructuredData{"id": 123.345},
			Payload:   opencdc.Change{After: opencdc.StructuredData{"createdAt": timeObj}},
		}})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,14 +1,14 @@
	//  {
	//    "position": null,
	//    "operation": "create",
	//    "metadata": null,
	//    "key": {
	//      "id": 123.345
	//    },
	//    "payload": {
	//      "before": null,
	//      "after": {
	// -      "createdAt": 1704198896123456789
	// +      "createdAt": "2024-01-02T12:34:56.123456789Z"
	//      }
	//    }
	//  }
}
