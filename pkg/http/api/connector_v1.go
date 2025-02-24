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

//go:generate mockgen -typed -destination=mock/connector.go -package=mock -mock_names=ConnectorOrchestrator=ConnectorOrchestrator . ConnectorOrchestrator
//go:generate mockgen -typed -destination=mock/connector_service.go -package=mock -mock_names=ConnectorService_InspectConnectorServer=ConnectorService_InspectConnectorServer github.com/conduitio/conduit/proto/api/v1 ConnectorService_InspectConnectorServer
//go:generate mockgen -typed -destination=mock/connector_plugin.go -package=mock -mock_names=ConnectorPluginOrchestrator=ConnectorPluginOrchestrator . ConnectorPluginOrchestrator

package api

import (
	"context"
	"regexp"

	opencdcv1 "github.com/conduitio/conduit-commons/proto/opencdc/v1"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/http/api/fromproto"
	"github.com/conduitio/conduit/pkg/http/api/status"
	"github.com/conduitio/conduit/pkg/http/api/toproto"
	"github.com/conduitio/conduit/pkg/inspector"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"google.golang.org/grpc"
)

type ConnectorOrchestrator interface {
	Create(ctx context.Context, t connector.Type, plugin string, pipelineID string, config connector.Config) (*connector.Instance, error)
	List(ctx context.Context) map[string]*connector.Instance
	Get(ctx context.Context, id string) (*connector.Instance, error)
	Delete(ctx context.Context, id string) error
	Update(ctx context.Context, id string, plugin string, config connector.Config) (*connector.Instance, error)
	Validate(ctx context.Context, t connector.Type, plugin string, config connector.Config) error
	Inspect(ctx context.Context, id string) (*inspector.Session, error)
}

type ConnectorPluginOrchestrator interface {
	// List will return all connector plugins' specs.
	List(ctx context.Context) (map[string]pconnector.Specification, error)
}

type ConnectorAPIv1 struct {
	apiv1.UnimplementedConnectorServiceServer
	connectorOrchestrator       ConnectorOrchestrator
	connectorPluginOrchestrator ConnectorPluginOrchestrator
}

func NewConnectorAPIv1(
	co ConnectorOrchestrator,
	cpo ConnectorPluginOrchestrator,
) *ConnectorAPIv1 {
	return &ConnectorAPIv1{
		connectorOrchestrator:       co,
		connectorPluginOrchestrator: cpo,
	}
}

func (c *ConnectorAPIv1) Register(srv *grpc.Server) {
	apiv1.RegisterConnectorServiceServer(srv, c)
}

func (c *ConnectorAPIv1) ListConnectors(
	ctx context.Context,
	req *apiv1.ListConnectorsRequest,
) (*apiv1.ListConnectorsResponse, error) {
	// TODO: Implement filtering and limiting.
	list := c.connectorOrchestrator.List(ctx)
	var clist []*apiv1.Connector
	for _, v := range list {
		if req.PipelineId == "" || req.PipelineId == v.PipelineID {
			clist = append(clist, toproto.Connector(v))
		}
	}

	return &apiv1.ListConnectorsResponse{Connectors: clist}, nil
}

func (c *ConnectorAPIv1) InspectConnector(req *apiv1.InspectConnectorRequest, server apiv1.ConnectorService_InspectConnectorServer) error {
	if req.Id == "" {
		return status.ConnectorError(cerrors.ErrEmptyID)
	}

	session, err := c.connectorOrchestrator.Inspect(server.Context(), req.Id)
	if err != nil {
		return status.ConnectorError(cerrors.Errorf("failed to inspect connector: %w", err))
	}

	for rec := range session.C {
		recProto := &opencdcv1.Record{}
		err := rec.ToProto(recProto)
		if err != nil {
			return cerrors.Errorf("failed converting record: %w", err)
		}

		err = server.Send(&apiv1.InspectConnectorResponse{
			Record: recProto,
		})
		if err != nil {
			return cerrors.Errorf("failed sending record: %w", err)
		}
	}

	return cerrors.New("inspector session closed")
}

// GetConnector returns a single Connector proto response or an error.
func (c *ConnectorAPIv1) GetConnector(
	ctx context.Context,
	req *apiv1.GetConnectorRequest,
) (*apiv1.GetConnectorResponse, error) {
	if req.Id == "" {
		return nil, status.ConnectorError(cerrors.ErrEmptyID)
	}

	// fetch the connector from the ConnectorOrchestrator
	pr, err := c.connectorOrchestrator.Get(ctx, req.Id)
	if err != nil {
		return nil, status.ConnectorError(cerrors.Errorf("failed to get connector by ID: %w", err))
	}

	resp := toproto.Connector(pr)

	return &apiv1.GetConnectorResponse{Connector: resp}, nil
}

// CreateConnector handles a CreateConnectorRequest, persists it to the Storage
// layer, and then returns the created connector with its assigned ID
func (c *ConnectorAPIv1) CreateConnector(
	ctx context.Context,
	req *apiv1.CreateConnectorRequest,
) (*apiv1.CreateConnectorResponse, error) {
	created, err := c.connectorOrchestrator.Create(
		ctx,
		fromproto.ConnectorType(req.Type),
		req.Plugin,
		req.PipelineId,
		fromproto.ConnectorConfig(req.Config),
	)
	if err != nil {
		return nil, status.ConnectorError(cerrors.Errorf("failed to create connector: %w", err))
	}

	co := toproto.Connector(created)

	return &apiv1.CreateConnectorResponse{Connector: co}, nil
}

func (c *ConnectorAPIv1) UpdateConnector(
	ctx context.Context,
	req *apiv1.UpdateConnectorRequest,
) (*apiv1.UpdateConnectorResponse, error) {
	if req.Id == "" {
		return nil, cerrors.ErrEmptyID
	}

	updated, err := c.connectorOrchestrator.Update(ctx, req.Id, req.Plugin, fromproto.ConnectorConfig(req.Config))
	if err != nil {
		return nil, status.ConnectorError(cerrors.Errorf("failed to update connector: %w", err))
	}

	co := toproto.Connector(updated)

	return &apiv1.UpdateConnectorResponse{Connector: co}, nil
}

func (c *ConnectorAPIv1) DeleteConnector(ctx context.Context, req *apiv1.DeleteConnectorRequest) (*apiv1.DeleteConnectorResponse, error) {
	err := c.connectorOrchestrator.Delete(ctx, req.Id)
	if err != nil {
		return nil, status.ConnectorError(cerrors.Errorf("failed to delete connector: %w", err))
	}

	return &apiv1.DeleteConnectorResponse{}, nil
}

// ValidateConnector validates whether the connector configurations are valid or not
// returns an empty response if valid, an error otherwise
func (c *ConnectorAPIv1) ValidateConnector(
	ctx context.Context,
	req *apiv1.ValidateConnectorRequest,
) (*apiv1.ValidateConnectorResponse, error) {
	err := c.connectorOrchestrator.Validate(
		ctx,
		fromproto.ConnectorType(req.Type),
		req.Plugin,
		fromproto.ConnectorConfig(req.Config),
	)
	if err != nil {
		return nil, status.ConnectorError(err)
	}

	return &apiv1.ValidateConnectorResponse{}, nil
}

func (c *ConnectorAPIv1) ListConnectorPlugins(
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

	mp, err := c.connectorPluginOrchestrator.List(ctx)
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
