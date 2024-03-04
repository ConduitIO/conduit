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
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"
)

//nolint:govet // a more descriptive example description
func ExampleRenameProcessor_rename1() {
	p := NewRenameProcessor()

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: `Rename two fields, one under metadata, and one under payload.after`,
		Config:  map[string]string{"mapping": ".Metadata.key1:newKey,.Payload.After.foo:newFoo"},
		Have: opencdc.Record{
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key1": "val1"},
			Payload:   opencdc.Change{After: opencdc.StructuredData{"foo": "bar"}, Before: opencdc.StructuredData{"bar": "baz"}},
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"newKey": "val1"},
			Payload:   opencdc.Change{After: opencdc.StructuredData{"newFoo": "bar"}, Before: opencdc.StructuredData{"bar": "baz"}},
		}})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,16 +1,16 @@
	//  {
	//    "position": null,
	//    "operation": "create",
	//    "metadata": {
	// -    "key1": "val1"
	// +    "newKey": "val1"
	//    },
	//    "key": null,
	//    "payload": {
	//      "before": {
	//        "bar": "baz"
	//      },
	//      "after": {
	// -      "foo": "bar"
	// +      "newFoo": "bar"
	//      }
	//    }
	//  }
}

//nolint:govet // a more descriptive example description
func ExampleRenameProcessor_rename2() {
	p := NewRenameProcessor()

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: `Rename two fields, one under key, and one under payload.before`,
		Config:  map[string]string{"mapping": ".Key.id:table,.Payload.Before.bar:renamedBar"},
		Have: opencdc.Record{
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key1": "val1"},
			Key:       opencdc.StructuredData{"id": "table-name"},
			Payload:   opencdc.Change{After: opencdc.StructuredData{"foo": "bar"}, Before: opencdc.StructuredData{"bar": "baz"}},
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key1": "val1"},
			Key:       opencdc.StructuredData{"table": "table-name"},
			Payload:   opencdc.Change{After: opencdc.StructuredData{"foo": "bar"}, Before: opencdc.StructuredData{"renamedBar": "baz"}},
		}})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,18 +1,18 @@
	//  {
	//    "position": null,
	//    "operation": "create",
	//    "metadata": {
	//      "key1": "val1"
	//    },
	//    "key": {
	// -    "id": "table-name"
	// +    "table": "table-name"
	//    },
	//    "payload": {
	//      "before": {
	// -      "bar": "baz"
	// +      "renamedBar": "baz"
	//      },
	//      "after": {
	//        "foo": "bar"
	//      }
	//    }
	//  }
}
