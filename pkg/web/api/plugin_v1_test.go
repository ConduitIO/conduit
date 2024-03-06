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

	"github.com/conduitio/conduit-commons/config"
	processorSdk "github.com/conduitio/conduit-processor-sdk"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/web/api/mock"
	"github.com/conduitio/conduit/pkg/web/api/toproto"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// Deprecated: testing the old plugin API.
func TestPluginAPIv1_ListPluginByName(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	cpoMock := mock.NewConnectorPluginOrchestrator(ctrl)
	api := NewPluginAPIv1(cpoMock, nil)

	names := []string{"do-not-want-this-plugin", "want-p1", "want-p2", "skip", "another-skipped"}

	plsMap := make(map[string]connectorPlugin.Specification)
	pls := make([]connectorPlugin.Specification, 0)

	for _, name := range names {
		ps := connectorPlugin.Specification{
			Name:        name,
			Description: "desc",
			Version:     "v1.0",
			Author:      "Aaron",
			SourceParams: map[string]connectorPlugin.Parameter{
				"param": {
					Type: connectorPlugin.ParameterTypeString,
					Validations: []connectorPlugin.Validation{{
						Type: connectorPlugin.ValidationTypeRequired,
					}},
				},
			},
			DestinationParams: map[string]connectorPlugin.Parameter{},
		}
		pls = append(pls, ps)
		plsMap[name] = ps
	}

	cpoMock.EXPECT().
		List(ctx).
		Return(plsMap, nil).
		Times(1)

	want := &apiv1.ListPluginsResponse{
		Plugins: []*apiv1.PluginSpecifications{
			{
				Name:              pls[1].Name,
				Description:       pls[1].Description,
				Summary:           pls[1].Summary,
				Author:            pls[1].Author,
				Version:           pls[1].Version,
				SourceParams:      toproto.PluginParamsMap(pls[1].SourceParams),
				DestinationParams: toproto.PluginParamsMap(pls[1].DestinationParams),
			},
			{
				Name:              pls[2].Name,
				Description:       pls[2].Description,
				Summary:           pls[2].Summary,
				Author:            pls[2].Author,
				Version:           pls[2].Version,
				SourceParams:      toproto.PluginParamsMap(pls[2].SourceParams),
				DestinationParams: toproto.PluginParamsMap(pls[2].DestinationParams),
			},
		},
	}

	got, err := api.ListPlugins(
		ctx,
		&apiv1.ListPluginsRequest{Name: "want-.*"},
	)

	is.NoErr(err)

	sortPlugins := func(p []*apiv1.PluginSpecifications) {
		sort.Slice(p, func(i, j int) bool {
			return p[i].Name < p[j].Name
		})
	}

	sortPlugins(want.Plugins)
	sortPlugins(got.Plugins)
	is.Equal(want, got)
}

func TestPluginAPIv1_ListConnectorPluginsByName(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	cpoMock := mock.NewConnectorPluginOrchestrator(ctrl)
	api := NewPluginAPIv1(cpoMock, nil)

	names := []string{"do-not-want-this-plugin", "want-p1", "want-p2", "skip", "another-skipped"}

	plsMap := make(map[string]connectorPlugin.Specification)
	pls := make([]connectorPlugin.Specification, 0)

	for _, name := range names {
		ps := connectorPlugin.Specification{
			Name:        name,
			Description: "desc",
			Version:     "v1.0",
			Author:      "Aaron",
			SourceParams: map[string]connectorPlugin.Parameter{
				"param": {
					Type: connectorPlugin.ParameterTypeString,
					Validations: []connectorPlugin.Validation{{
						Type: connectorPlugin.ValidationTypeRequired,
					}},
				},
			},
			DestinationParams: map[string]connectorPlugin.Parameter{},
		}
		pls = append(pls, ps)
		plsMap[name] = ps
	}

	cpoMock.EXPECT().
		List(ctx).
		Return(plsMap, nil).
		Times(1)

	want := &apiv1.ListConnectorPluginsResponse{
		Plugins: []*apiv1.ConnectorPluginSpecifications{
			toproto.ConnectorPluginSpecifications(pls[1].Name, pls[1]),
			toproto.ConnectorPluginSpecifications(pls[2].Name, pls[2]),
		},
	}

	got, err := api.ListConnectorPlugins(
		ctx,
		&apiv1.ListConnectorPluginsRequest{Name: "want-.*"},
	)

	is.NoErr(err)

	sortPlugins := func(p []*apiv1.ConnectorPluginSpecifications) {
		sort.Slice(p, func(i, j int) bool {
			return p[i].Name < p[j].Name
		})
	}

	sortPlugins(want.Plugins)
	sortPlugins(got.Plugins)
	is.Equal(want, got)
}

func TestPluginAPIv1_ListProcessorPluginsByName(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	ppoMock := mock.NewProcessorPluginOrchestrator(ctrl)
	api := NewPluginAPIv1(nil, ppoMock)

	names := []string{"do-not-want-this-plugin", "want-p1", "want-p2", "skip", "another-skipped"}

	plsMap := make(map[string]processorSdk.Specification)
	pls := make([]processorSdk.Specification, 0)

	for _, name := range names {
		ps := processorSdk.Specification{
			Name:        name,
			Description: "desc",
			Version:     "v1.0",
			Author:      "Aaron",
			Parameters: map[string]config.Parameter{
				"param": {
					Type: config.ParameterTypeString,
					Validations: []config.Validation{
						config.ValidationRequired{},
					},
				},
			},
		}
		pls = append(pls, ps)
		plsMap[name] = ps
	}

	ppoMock.EXPECT().
		List(ctx).
		Return(plsMap, nil).
		Times(1)

	want := &apiv1.ListProcessorPluginsResponse{
		Plugins: []*apiv1.ProcessorPluginSpecifications{
			toproto.ProcessorPluginSpecifications(pls[1].Name, pls[1]),
			toproto.ProcessorPluginSpecifications(pls[2].Name, pls[2]),
		},
	}

	got, err := api.ListProcessorPlugins(
		ctx,
		&apiv1.ListProcessorPluginsRequest{Name: "want-.*"},
	)

	is.NoErr(err)

	sortPlugins := func(p []*apiv1.ProcessorPluginSpecifications) {
		sort.Slice(p, func(i, j int) bool {
			return p[i].Name < p[j].Name
		})
	}

	sortPlugins(want.Plugins)
	sortPlugins(got.Plugins)
	is.Equal(want, got)
}
