// Copyright © 2025 Meroxa, Inc.
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

package impl

import (
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"
)

func ExampleCloneProcessor_simple() {
	p := NewCloneProcessor(log.Nop())

	exampleutil.RunExample(p, exampleutil.Example{
		Summary:     "Clone record into multiple records",
		Description: "This example takes a record and clones it once, producing 2 records, each containing the same data, except for the metadata field `clone.index`.",
		Config:      config.Config{"count": "1"},
		Have: opencdc.Record{
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"foo": "bar"},
			Key:       opencdc.StructuredData{"id": 123},
			Payload: opencdc.Change{After: opencdc.StructuredData{
				"name": "Alice",
				"age":  30,
			}},
		},
		Want: sdk.MultiRecord{
			opencdc.Record{
				Operation: opencdc.OperationCreate,
				Metadata: map[string]string{
					"foo":         "bar",
					"clone.index": "0",
				},
				Key: opencdc.StructuredData{"id": 123},
				Payload: opencdc.Change{After: opencdc.StructuredData{
					"name": "Alice",
					"age":  30,
				}},
			},
			opencdc.Record{
				Operation: opencdc.OperationCreate,
				Metadata: map[string]string{
					"foo":         "bar",
					"clone.index": "1",
				},
				Key: opencdc.StructuredData{"id": 123},
				Payload: opencdc.Change{After: opencdc.StructuredData{
					"name": "Alice",
					"age":  30,
				}},
			},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,17 +1,38 @@
	// -{
	// +[
	// +  {
	// -  "position": null,
	// +    "position": null,
	// -  "operation": "create",
	// +    "operation": "create",
	// -  "metadata": {
	// +    "metadata": {
	// -    "foo": "bar"
	// +      "clone.index": "0",
	// +      "foo": "bar"
	// +    },
	// +    "key": {
	// +      "id": 123
	// +    },
	// +    "payload": {
	// +      "before": null,
	// +      "after": {
	// +        "age": 30,
	// +        "name": "Alice"
	// +      }
	// +    }
	// +  },
	// +  {
	// +    "position": null,
	// +    "operation": "create",
	// +    "metadata": {
	// +      "clone.index": "1",
	// +      "foo": "bar"
	// -  },
	// +    },
	// -  "key": {
	// +    "key": {
	// -    "id": 123
	// +      "id": 123
	// -  },
	// +    },
	// -  "payload": {
	// +    "payload": {
	// -    "before": null,
	// +      "before": null,
	// -    "after": {
	// +      "after": {
	// -      "age": 30,
	// +        "age": 30,
	// -      "name": "Alice"
	// +        "name": "Alice"
	// -    }
	// +      }
	// -  }
	// +    }
	// -}
	// +  }
	// +]
}
