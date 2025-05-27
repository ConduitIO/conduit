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
	"path/filepath"
	"regexp"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit-connector-protocol/pconnector/server"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/matryer/is"
)

// These constants need to match the constants in pkg/plugin/connector/standalone/test/testplugin/main.go
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
	pluginPath, err := filepath.Abs(path.Join(testPluginDir, "testplugin.sh"))
	if err != nil {
		panic(err)
	}

	return blueprint{
		FullName: plugin.FullName(fmt.Sprintf("standalone:%v@%v", testPluginName, testPluginVersion)),
		Path:     pluginPath,
		Specification: pconnector.Specification{
			Name:        testPluginName,
			Summary:     testPluginSummary,
			Description: testPluginDescription,
			Version:     testPluginVersion,
			Author:      testPluginAuthor,
			SourceParams: map[string]config.Parameter{
				testPluginSourceParam1: {
					Default:     testPluginSourceParam1Default,
					Type:        0, // no type
					Description: testPluginSourceParam1Description,
					Validations: []config.Validation{
						config.ValidationRequired{},
						config.ValidationInclusion{List: []string{"one", "two"}},
					},
				},
				testPluginSourceParam2: {
					Default:     testPluginSourceParam2Default,
					Type:        config.ParameterTypeInt,
					Description: testPluginSourceParam2Description,
					Validations: []config.Validation{
						config.ValidationExclusion{List: []string{"3", "4"}},
						config.ValidationGreaterThan{V: 1},
					},
				},
			},
			DestinationParams: map[string]config.Parameter{
				testPluginDestinationParam1: {
					Default:     testPluginDestinationParam1Default,
					Type:        config.ParameterTypeInt,
					Description: testPluginDestinationParam1Description,
					Validations: []config.Validation{
						config.ValidationLessThan{V: 10},
						config.ValidationRegex{Regex: regexp.MustCompile("[1-9]")},
					},
				},
				testPluginDestinationParam2: {
					Default:     testPluginDestinationParam2Default,
					Type:        config.ParameterTypeDuration,
					Description: testPluginDestinationParam2Description,
					Validations: nil,
				},
			},
		},
	}
}

func TestRegistry_loadPlugins(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	r := NewRegistry(log.Test(t), testPluginDir)
	got := r.loadPlugins(ctx)
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
	ctx := context.Background()

	r := NewRegistry(log.Nop(), testPluginDir)
	r.Init(ctx, ":12345", server.DefaultMaxReceiveRecordSize)

	got := r.List()
	bp := testPluginBlueprint()
	want := map[plugin.FullName]pconnector.Specification{
		bp.FullName: bp.Specification,
	}
	is.Equal(got, want)
}
