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

package api

import (
	"context"
	"sort"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/builtin"
	"github.com/conduitio/conduit/pkg/plugin/standalone"
	"github.com/conduitio/conduit/pkg/web/api/mock"
	"github.com/conduitio/conduit/pkg/web/api/toproto"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/golang/mock/gomock"
	"github.com/rs/zerolog"
)

func TestPluginAPIv1_ListPluginByName(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	psMock := mock.NewPluginOrchestrator(ctrl)
	api := NewPluginAPIv1(psMock)
	logger := log.New(zerolog.Nop())

	pluginService := plugin.NewService(
		builtin.NewRegistry(logger, builtin.DefaultDispenserFactories...),
		standalone.NewRegistry(logger),
	)

	var plist []*apiv1.PluginSpecifications
	plsMap, err := pluginService.List(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// build the mock returned map
	for k, _ := range plsMap {
		spec := plsMap[k]
		plist = append(plist, toproto.Plugin(&spec))
	}

	psMock.EXPECT().
		List(ctx).
		Return(plsMap, nil).
		Times(1)

	got, err := api.ListPlugins(
		ctx,
		&apiv1.ListPluginsRequest{},
	)

	// build the expected response
	var plugins []*apiv1.PluginSpecifications
	for k, _ := range plsMap {
		plugins = append(plugins, &apiv1.PluginSpecifications{
			Name:              plsMap[k].Name,
			Summary:           plsMap[k].Summary,
			Description:       plsMap[k].Description,
			Version:           plsMap[k].Version,
			Author:            plsMap[k].Author,
			SourceParams:      toproto.PluginParamsMap(plsMap[k].SourceParams),
			DestinationParams: toproto.PluginParamsMap(plsMap[k].DestinationParams),
		})
	}

	want := &apiv1.ListPluginsResponse{
		Plugins: plugins,
	}

	assert.Ok(t, err)

	sortPlugins(want.Plugins)
	sortPlugins(got.Plugins)
	assert.Equal(t, want, got)
}

func sortPlugins(p []*apiv1.PluginSpecifications) {
	sort.Slice(p, func(i, j int) bool {
		return p[i].Name < p[j].Name
	})
}
