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

//go:generate mockgen -destination=mock/connector.go -package=mock -mock_names=ConnectorOrchestrator=ConnectorOrchestrator . ConnectorOrchestrator
//go:generate mockgen -destination=mock/connector_service.go -package=mock -mock_names=ConnectorService_InspectConnectorServer=ConnectorService_InspectConnectorServer github.com/conduitio/conduit/proto/gen/api/v1 ConnectorService_InspectConnectorServer
package api

import (
	"context"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/conduitio/conduit/pkg/web/api/fromproto"
	"github.com/conduitio/conduit/pkg/web/api/status"
	"github.com/conduitio/conduit/pkg/web/api/toproto"
	apiv1 "github.com/conduitio/conduit/proto/gen/api/v1"
	"google.golang.org/grpc"
)

type ConnectorOrchestrator interface {
	Create(ctx context.Context, t connector.Type, config connector.Config) (connector.Connector, error)
	List(ctx context.Context) map[string]connector.Connector
	Get(ctx context.Context, id string) (connector.Connector, error)
	Delete(ctx context.Context, id string) error
	Update(ctx context.Context, id string, config connector.Config) (connector.Connector, error)
	Validate(ctx context.Context, t connector.Type, config connector.Config) error
	Inspect(ctx context.Context, id string) (chan record.Record, error)
}

type ConnectorAPIv1 struct {
	apiv1.UnimplementedConnectorServiceServer
	cs ConnectorOrchestrator
}

func NewConnectorAPIv1(cs ConnectorOrchestrator) *ConnectorAPIv1 {
	return &ConnectorAPIv1{cs: cs}
}

func (c *ConnectorAPIv1) Register(srv *grpc.Server) {
	apiv1.RegisterConnectorServiceServer(srv, c)
}

func (c *ConnectorAPIv1) ListConnectors(
	ctx context.Context,
	req *apiv1.ListConnectorsRequest,
) (*apiv1.ListConnectorsResponse, error) {
	// TODO: Implement filtering and limiting.
	list := c.cs.List(ctx)
	var clist []*apiv1.Connector
	for _, v := range list {
		if req.PipelineId == "" || req.PipelineId == v.Config().PipelineID {
			clist = append(clist, toproto.Connector(v))
		}
	}

	return &apiv1.ListConnectorsResponse{Connectors: clist}, nil
}

func (c *ConnectorAPIv1) InspectConnector(req *apiv1.InspectConnectorRequest, server apiv1.ConnectorService_InspectConnectorServer) error {
	if req.Id == "" {
		return status.ConnectorError(cerrors.ErrEmptyID)
	}

	records, err := c.cs.Inspect(server.Context(), req.Id)
	if err != nil {
		return status.ConnectorError(cerrors.Errorf("failed to get connector by ID %v: %w", req.Id, err))
	}

	for rec := range records {
		recProto, err2 := toproto.Record(rec)
		if err2 != nil {
			return cerrors.Errorf("failed converting record: %w", err2)
		}

		err2 = server.Send(&apiv1.InspectConnectorResponse{
			Record: recProto,
		})
		if err2 != nil {
			return cerrors.Errorf("failed sending record: %w", err2)
		}
	}

	return cerrors.New("records channel closed")
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
	pr, err := c.cs.Get(ctx, req.Id)
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
	created, err := c.cs.Create(
		ctx,
		fromproto.ConnectorType(req.Type),
		fromproto.ConnectorConfig(req.Config, req.Plugin, req.PipelineId, nil),
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

	old, err := c.cs.Get(ctx, req.Id)
	if err != nil {
		return nil, status.ConnectorError(cerrors.Errorf("failed to get connector by ID: %w", err))
	}

	config := fromproto.ConnectorConfig(
		req.Config,
		old.Config().Plugin,
		old.Config().PipelineID,
		old.Config().ProcessorIDs,
	)

	updated, err := c.cs.Update(ctx, req.Id, config)

	if err != nil {
		return nil, status.ConnectorError(cerrors.Errorf("failed to update connector: %w", err))
	}

	co := toproto.Connector(updated)

	return &apiv1.UpdateConnectorResponse{Connector: co}, nil
}

func (c *ConnectorAPIv1) DeleteConnector(ctx context.Context, req *apiv1.DeleteConnectorRequest) (*apiv1.DeleteConnectorResponse, error) {
	err := c.cs.Delete(ctx, req.Id)

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
	err := c.cs.Validate(
		ctx,
		fromproto.ConnectorType(req.Type),
		fromproto.ConnectorConfig(req.Config, req.Plugin, "", nil),
	)

	if err != nil {
		return nil, status.ConnectorError(err)
	}

	return &apiv1.ValidateConnectorResponse{}, nil
}
