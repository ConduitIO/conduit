// Copyright © 2022 Meroxa, Inc.
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

package standalone

import (
	"context"
	"fmt"
	"path"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/matryer/is"
)

// These constants need to match the constants in pkg/plugin/standalone/test/testplugin/main.go
const (
	testPluginDir = "./test"

	testPluginName        = "test-plugin"
	testPluginSummary     = "My test plugin summary"
	testPluginDescription = "My test plugin description"
	testPluginVersion     = "v0.1.0"
	testPluginAuthor      = "test"

	testPluginSourceParam1            = "src.foo"
	testPluginSourceParam1Default     = ""
	testPluginSourceParam1Description = "Required parameter"
	testPluginSourceParam2            = "src.bar"
	testPluginSourceParam2Default     = "bar"
	testPluginSourceParam2Description = "Optional parameter"

	testPluginDestinationParam1            = "dest.foo"
	testPluginDestinationParam1Default     = ""
	testPluginDestinationParam1Description = "Required parameter"
	testPluginDestinationParam2            = "dest.bar"
	testPluginDestinationParam2Default     = "bar"
	testPluginDestinationParam2Description = "Optional parameter"
)

func testPluginBlueprint() blueprint {
	return blueprint{
		fullName: plugin.FullName(fmt.Sprintf("standalone:%v@%v", testPluginName, testPluginVersion)),
		path:     path.Join(testPluginDir, "testplugin.sh"),
		specification: plugin.Specification{
			Name:        testPluginName,
			Summary:     testPluginSummary,
			Description: testPluginDescription,
			Version:     testPluginVersion,
			Author:      testPluginAuthor,
			SourceParams: map[string]plugin.Parameter{
				testPluginSourceParam1: {
					Default:     testPluginSourceParam1Default,
					Type:        "string",
					Description: testPluginSourceParam1Description,
					Validations: []plugin.Validation{
						{Type: plugin.ValidationTypeRequired, Value: ""},
					},
				},
				testPluginSourceParam2: {
					Default:     testPluginSourceParam2Default,
					Type:        "string",
					Description: testPluginSourceParam2Description,
					Validations: nil,
				},
			},
			DestinationParams: map[string]plugin.Parameter{
				testPluginDestinationParam1: {
					Default:     testPluginDestinationParam1Default,
					Type:        "string",
					Description: testPluginDestinationParam1Description,
					Validations: []plugin.Validation{
						{Type: plugin.ValidationTypeRequired, Value: ""},
					},
				},
				testPluginDestinationParam2: {
					Default:     testPluginDestinationParam2Default,
					Type:        "string",
					Description: testPluginDestinationParam2Description,
					Validations: nil,
				},
			},
		},
	}
}

func TestRegistry_loadPlugins(t *testing.T) {
	is := is.New(t)

	r := NewRegistry(log.Nop(), "")
	got := r.loadPlugins(context.Background(), testPluginDir)
	want := map[string]map[string]blueprint{
		testPluginName: {
			testPluginVersion:          testPluginBlueprint(),
			plugin.PluginVersionLatest: testPluginBlueprint(),
		},
	}

	is.Equal(len(got), 1)
	is.Equal(got, want)
}

func TestRegistry_List(t *testing.T) {
	is := is.New(t)

	r := NewRegistry(log.Nop(), testPluginDir)

	got := r.List()
	bp := testPluginBlueprint()
	want := map[plugin.FullName]plugin.Specification{
		bp.fullName: bp.specification,
	}

	is.Equal(got, want)
}
