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
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"
)

func ExampleExcludeProcessor_oneField() {
	p := NewExcludeProcessor(log.Nop())

	exampleutil.RunExample(p, exampleutil.Example{
		Summary:     "Exclude all fields in payload",
		Description: "Excluding all fields in `.Payload` results in an empty payload.",
		Config:      config.Config{"fields": ".Payload"},
		Have: opencdc.Record{
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key1": "val1"},
			Payload:   opencdc.Change{After: opencdc.StructuredData{"foo": "bar"}, Before: opencdc.StructuredData{"bar": "baz"}},
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key1": "val1"},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,16 +1,12 @@
	//  {
	//    "position": null,
	//    "operation": "create",
	//    "metadata": {
	//      "key1": "val1"
	//    },
	//    "key": null,
	//    "payload": {
	// -    "before": {
	// -      "bar": "baz"
	// -    },
	// +    "before": null,
	// -    "after": {
	// -      "foo": "bar"
	// -    }
	// +    "after": null
	//    }
	//  }
}

func ExampleExcludeProcessor_multipleFields() {
	p := NewExcludeProcessor(log.Nop())

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: `Exclude multiple fields`,
		Description: `It's possible to exclude multiple fields by providing a
comma-separated list of fields. In this example, we exclude ` + "`.Metadata`" + `,
` + "`.Payload.After.foo`" + ` and ` + "`.Key.key1`" + `.`,
		Config: config.Config{"fields": ".Metadata,.Payload.After.foo,.Key.key1"},
		Have: opencdc.Record{
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"source": "s3"},
			Key:       opencdc.StructuredData{"key1": "val1", "key2": "val2"},
			Payload:   opencdc.Change{After: opencdc.StructuredData{"foo": "bar", "foobar": "baz"}, Before: opencdc.StructuredData{"bar": "baz"}},
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{},
			Key:       opencdc.StructuredData{"key2": "val2"},
			Payload:   opencdc.Change{After: opencdc.StructuredData{"foobar": "baz"}, Before: opencdc.StructuredData{"bar": "baz"}},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,20 +1,16 @@
	//  {
	//    "position": null,
	//    "operation": "create",
	// -  "metadata": {
	// -    "source": "s3"
	// -  },
	// +  "metadata": {},
	// -  "key": {
	// -    "key1": "val1",
	// +  "key": {
	//      "key2": "val2"
	//    },
	//    "payload": {
	//      "before": {
	//        "bar": "baz"
	//      },
	// -    "after": {
	// -      "foo": "bar",
	// +    "after": {
	//        "foobar": "baz"
	//      }
	//    }
	//  }
}
