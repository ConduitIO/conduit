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
)

//nolint:govet // a more descriptive example description
func ExampleFilterProcessor() {
	p := newFilter()

	RunExample(p, example{
		Description: `filter out the record`,
		Config:      map[string]string{},
		Have: opencdc.Record{
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key1": "val1"},
			Payload:   opencdc.Change{After: opencdc.StructuredData{"foo": "bar"}, Before: opencdc.StructuredData{"bar": "baz"}},
		},
		Want: sdk.FilterRecord{}})

	// Output:
	// processor filtered record out
}
