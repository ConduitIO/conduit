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
	"regexp"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/http/api/status"
	"github.com/conduitio/conduit/pkg/http/api/toproto"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"google.golang.org/grpc"
)

type PluginAPIv1 struct {
	apiv1.UnimplementedPluginServiceServer
	connectorPluginOrchestrator ConnectorPluginOrchestrator
}

func NewPluginAPIv1(
	cpo ConnectorPluginOrchestrator,
) *PluginAPIv1 {
	return &PluginAPIv1{connectorPluginOrchestrator: cpo}
}

func (p *PluginAPIv1) Register(srv *grpc.Server) {
	apiv1.RegisterPluginServiceServer(srv, p)
}

// Deprecated: this is here for backwards compatibility with the old plugin API.
// Use ListConnectorPlugins instead.
func (p *PluginAPIv1) ListPlugins(
	ctx context.Context,
	req *apiv1.ListPluginsRequest,
) (*apiv1.ListPluginsResponse, error) {
	var nameFilter *regexp.Regexp
	if req.GetName() != "" {
		var err error
		nameFilter, err = regexp.Compile("^" + req.GetName() + "$")
		if err != nil {
			return nil, status.PluginError(cerrors.New("invalid name regex"))
		}
	}

	mp, err := p.connectorPluginOrchestrator.List(ctx)
	if err != nil {
		return nil, status.PluginError(err)
	}
	var plist []*apiv1.PluginSpecifications

	for name, v := range mp {
		if nameFilter != nil && !nameFilter.MatchString(name) {
			continue // don't add to result list, filter didn't match
		}
		plist = append(plist, toproto.PluginSpecifications(name, v))
	}

	return &apiv1.ListPluginsResponse{Plugins: plist}, nil
}
