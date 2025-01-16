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
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/http/api/mock"
	"github.com/conduitio/conduit/pkg/http/api/toproto"
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
	api := NewPluginAPIv1(cpoMock)

	names := []string{"do-not-want-this-plugin", "want-p1", "want-p2", "skip", "another-skipped"}

	plsMap := make(map[string]pconnector.Specification)
	pls := make([]pconnector.Specification, 0)

	for _, name := range names {
		ps := pconnector.Specification{
			Name:        name,
			Description: "desc",
			Version:     "v1.0",
			Author:      "Aaron",
			SourceParams: map[string]config.Parameter{
				"param": {
					Type: config.ParameterTypeString,
					Validations: []config.Validation{
						config.ValidationRequired{},
					},
				},
			},
			DestinationParams: map[string]config.Parameter{},
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
