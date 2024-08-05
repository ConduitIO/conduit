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

package impl

import (
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"
)

//nolint:govet // we're using a more descriptive name of example
func ExampleErrorProcessor() {
	p := NewErrorProcessor(log.Nop())

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: `Error record with custom error message`,
		Description: `This example shows how to configure the error processor to
return a custom error message for a record using a Go template.`,
		Config: config.Config{
			"message": "custom error message with data from record: {{.Metadata.foo}}",
		},
		Have: opencdc.Record{
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"foo": "bar"},
			Payload:   opencdc.Change{After: opencdc.StructuredData{"foo": "bar"}, Before: opencdc.StructuredData{"bar": "baz"}},
		},
		Want: sdk.ErrorRecord{
			Error: cerrors.New("custom error message with data from record: bar"),
		}})

	// Output:
	// processor returned error: custom error message with data from record: bar
}
