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
func ExampleSetProcessor_setOperation() {
	p := NewSetProcessor()

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: `Sets a record's operation to "update"`,
		Config:  map[string]string{"field": ".Operation", "value": "update"},
		Have:    opencdc.Record{Operation: opencdc.OperationCreate},
		Want:    sdk.SingleRecord{Operation: opencdc.OperationUpdate},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,10 +1,10 @@
	//  {
	//    "position": null,
	// -  "operation": "create",
	// +  "operation": "update",
	//    "metadata": null,
	//    "key": null,
	//    "payload": {
	//      "before": null,
	//      "after": null
	//    }
	//  }
}

//nolint:govet // a more descriptive example description
func ExampleSetProcessor_addField() {
	p := NewSetProcessor()

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: `Processor will create a new field and set its value`,
		Config:  map[string]string{"field": ".Payload.After.foo", "value": "bar"},
		Have: opencdc.Record{Operation: opencdc.OperationSnapshot,
			Key: opencdc.StructuredData{"my-key": "id"},
		},
		Want: sdk.SingleRecord{
			Key:       opencdc.StructuredData{"my-key": "id"},
			Operation: opencdc.OperationSnapshot,
			Payload:   opencdc.Change{After: opencdc.StructuredData{"foo": "bar"}},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,12 +1,14 @@
	//  {
	//    "position": null,
	//    "operation": "snapshot",
	//    "metadata": null,
	//    "key": {
	//      "my-key": "id"
	//    },
	//    "payload": {
	//      "before": null,
	// -    "after": null
	// +    "after": {
	// +      "foo": "bar"
	// +    }
	//    }
	//  }
}

//nolint:govet // a more descriptive example description
func ExampleSetProcessor_template() {
	p := NewSetProcessor()

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: `Set the value under .Payload.After.postgres to true if .Metadata.table="postgres"`,
		Config:  map[string]string{"field": ".Payload.After.postgres", "value": "{{ eq .Metadata.table \"postgres\" }}"},
		Have: opencdc.Record{
			Metadata:  map[string]string{"table": "postgres"},
			Operation: opencdc.OperationSnapshot,
			Payload:   opencdc.Change{After: opencdc.StructuredData{"postgres": "false"}},
		},
		Want: sdk.SingleRecord{
			Metadata:  map[string]string{"table": "postgres"},
			Operation: opencdc.OperationSnapshot,
			Payload:   opencdc.Change{After: opencdc.StructuredData{"postgres": "true"}},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,14 +1,14 @@
	//  {
	//    "position": null,
	//    "operation": "snapshot",
	//    "metadata": {
	//      "table": "postgres"
	//    },
	//    "key": null,
	//    "payload": {
	//      "before": null,
	//      "after": {
	// -      "postgres": "false"
	// +      "postgres": "true"
	//      }
	//    }
	//  }
}
