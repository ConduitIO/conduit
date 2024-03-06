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

	processorSdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/web/api/status"
	"github.com/conduitio/conduit/pkg/web/api/toproto"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"google.golang.org/grpc"
)

//go:generate mockgen -destination=mock/connector_plugin.go -package=mock -mock_names=ConnectorPluginOrchestrator=ConnectorPluginOrchestrator . ConnectorPluginOrchestrator
//go:generate mockgen -destination=mock/processor_plugin.go -package=mock -mock_names=ProcessorPluginOrchestrator=ProcessorPluginOrchestrator . ProcessorPluginOrchestrator

// ConnectorPluginOrchestrator defines a CRUD interface that manages connector plugins.
type ConnectorPluginOrchestrator interface {
	// List will return all connector plugins' specs.
	List(ctx context.Context) (map[string]connectorPlugin.Specification, error)
}

// ProcessorPluginOrchestrator defines a CRUD interface that manages processor plugins.
type ProcessorPluginOrchestrator interface {
	// List will return all processor plugins' specs.
	List(ctx context.Context) (map[string]processorSdk.Specification, error)
}

type PluginAPIv1 struct {
	apiv1.UnimplementedPluginServiceServer
	connectorPluginOrchestrator ConnectorPluginOrchestrator
	processorPluginOrchestrator ProcessorPluginOrchestrator
}

func NewPluginAPIv1(
	cpo ConnectorPluginOrchestrator,
	ppo ProcessorPluginOrchestrator,
) *PluginAPIv1 {
	return &PluginAPIv1{
		connectorPluginOrchestrator: cpo,
		processorPluginOrchestrator: ppo,
	}
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

func (p *PluginAPIv1) ListConnectorPlugins(
	ctx context.Context,
	req *apiv1.ListConnectorPluginsRequest,
) (*apiv1.ListConnectorPluginsResponse, error) {
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
	var plist []*apiv1.ConnectorPluginSpecifications

	for name, v := range mp {
		if nameFilter != nil && !nameFilter.MatchString(name) {
			continue // don't add to result list, filter didn't match
		}
		plist = append(plist, toproto.ConnectorPluginSpecifications(name, v))
	}

	return &apiv1.ListConnectorPluginsResponse{Plugins: plist}, nil
}

func (p *PluginAPIv1) ListProcessorPlugins(
	ctx context.Context,
	req *apiv1.ListProcessorPluginsRequest,
) (*apiv1.ListProcessorPluginsResponse, error) {
	var nameFilter *regexp.Regexp
	if req.GetName() != "" {
		var err error
		nameFilter, err = regexp.Compile("^" + req.GetName() + "$")
		if err != nil {
			return nil, status.PluginError(cerrors.New("invalid name regex"))
		}
	}

	mp, err := p.processorPluginOrchestrator.List(ctx)
	if err != nil {
		return nil, status.PluginError(err)
	}
	var plist []*apiv1.ProcessorPluginSpecifications

	for name, v := range mp {
		if nameFilter != nil && !nameFilter.MatchString(name) {
			continue // don't add to result list, filter didn't match
		}
		plist = append(plist, toproto.ProcessorPluginSpecifications(name, v))
	}

	return &apiv1.ListProcessorPluginsResponse{Plugins: plist}, nil
}
