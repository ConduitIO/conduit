// Copyright © 2023 Meroxa, Inc.
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

//go:generate ./test/build-test-processors.sh

package standalone

import (
	"testing"

	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

var testPluginDir = "./test/wasm_processors/"

func TestRegistry_List(t *testing.T) {
	is := is.New(t)

	underTest := NewRegistry(log.Nop(), testPluginDir+"chaos_processor/")
	list := underTest.List()
	is.Equal(1, len(list))
	got, ok := list["standalone:chaos-processor@v1.3.5"]
	is.True(ok) // expected spec for standalone:test-processor@v1.3.5

	param := sdk.Parameter{
		Default:     "success",
		Type:        sdk.ParameterTypeString,
		Description: "prefix",
		Validations: []sdk.Validation{
			{
				Type:  sdk.ValidationTypeInclusion,
				Value: "success,error,panic",
			},
		},
	}
	wantSpec := sdk.Specification{
		Name:        "chaos-processor",
		Summary:     "chaos processor summary",
		Description: "chaos processor description",
		Version:     "v1.3.5",
		Author:      "Meroxa, Inc.",
		Parameters: map[string]sdk.Parameter{
			"configure": param,
			"open":      param,
			"process.prefix": {
				Default:     "",
				Type:        sdk.ParameterTypeString,
				Description: "prefix to be added to the payload's after",
				Validations: []sdk.Validation{
					{
						Type: sdk.ValidationTypeRequired,
					},
				},
			},
			"process":  param,
			"teardown": param,
		},
	}
	is.Equal(
		got,
		&spec{Specification: wantSpec},
	)
}

func TestRegistry_MalformedProcessor(t *testing.T) {
	is := is.New(t)

	underTest := NewRegistry(log.Nop(), testPluginDir+"malformed_processor/")
	list := underTest.List()
	is.Equal(0, len(list))
}

func TestRegistry_SpecifyError(t *testing.T) {
	is := is.New(t)

	underTest := NewRegistry(log.Nop(), testPluginDir+"specify_error/")
	list := underTest.List()
	is.Equal(0, len(list))
}
