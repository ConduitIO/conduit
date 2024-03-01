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
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/avro"
)

//nolint:govet // we're using a more descriptive name of example
func ExampleEncodeAvro() {
	p := avro.NewEncodeProcessor(log.Nop())

	RunExample(p, example{
		Description: "",
		Config: map[string]string{
			"url":                          "http://localhost",
			"schema.strategy":              "preRegistered",
			"schema.preRegistered.subject": "testsubject",
			"schema.preRegistered.version": "123",
		},
		Have: opencdc.Record{},
		Want: nil,
	})

	// Output:
}
