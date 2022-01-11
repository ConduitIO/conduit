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

package plugins

import (
	"context"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugins/proto"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
)

// DestinationPlugin represents a plugin that acts as a destination connector.
type DestinationPlugin struct {
	plugin.NetRPCUnsupportedPlugin
	Impl Destination
}

func (sp *DestinationPlugin) GRPCServer(broker *plugin.GRPCBroker, s *grpc.Server) error {
	proto.RegisterDestinationServer(s, &destinationServer{
		impl: sp.Impl,
	})

	return nil
}

func (sp *DestinationPlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return &destinationClient{
		client: proto.NewDestinationClient(c),
	}, nil
}

// destinationServer is the implementation of the GRPC server (running in the plugin).
type destinationServer struct {
	proto.UnimplementedDestinationServer
	impl Destination
}

func (s *destinationServer) Open(ctx context.Context, cfg *proto.Config) (*proto.Empty, error) {
	err := s.impl.Open(ctx, toInternalConfig(cfg))
	if err != nil {
		return nil, cerrors.Errorf("destination server open: %w", err)
	}
	return &proto.Empty{}, nil
}

func (s *destinationServer) Teardown(ctx context.Context, req *proto.Empty) (*proto.Empty, error) {
	err := s.impl.Teardown()
	if err != nil {
		return nil, cerrors.Errorf("destination server teardown: %w", err)
	}
	return &proto.Empty{}, nil
}

func (s *destinationServer) Validate(ctx context.Context, cfg *proto.Config) (*proto.Empty, error) {
	err := s.impl.Validate(toInternalConfig(cfg))
	if err != nil {
		return nil, cerrors.Errorf("destination server validate: %w", err)
	}
	return &proto.Empty{}, nil
}

func (s *destinationServer) Write(ctx context.Context, pbrecord *proto.Record) (*proto.Position, error) {
	r, err := toInternalRecord(pbrecord)
	if err != nil {
		return nil, cerrors.Errorf("destination server toInternalRecord: %w", err)
	}

	position, err := s.impl.Write(ctx, r)
	if err != nil {
		return nil, cerrors.Errorf("destination server write: %w", err)
	}

	return &proto.Position{Position: position}, err
}

// destinationClient is the implementation of the GRPC client (running in conduit).
type destinationClient struct {
	client proto.DestinationClient
}

var _ Destination = (*destinationClient)(nil)

func (c *destinationClient) Open(ctx context.Context, cfg Config) error {
	pcfg := &proto.Config{}
	setProtoConfig(cfg, pcfg)
	_, err := c.client.Open(ctx, pcfg)
	if err != nil {
		return cerrors.Errorf("destination client open: %w", wrapRecoverableError(err))
	}
	return nil
}

func (c *destinationClient) Teardown() error {
	_, err := c.client.Teardown(context.TODO(), &proto.Empty{})
	if err != nil {
		return cerrors.Errorf("destination client teardown: %w", wrapRecoverableError(err))
	}
	return nil
}

func (c *destinationClient) Validate(cfg Config) error {
	pcfg := &proto.Config{}
	setProtoConfig(cfg, pcfg)
	_, err := c.client.Validate(context.TODO(), pcfg)
	if err != nil {
		return cerrors.Errorf("destination client validate: %w", wrapRecoverableError(err))
	}
	return nil
}

func (c *destinationClient) Write(ctx context.Context, r record.Record) (record.Position, error) {
	pr, err := toProtoRecord(r)
	if err != nil {
		return record.Position{}, cerrors.Errorf("destination client toProtoRecord: %w", err)
	}

	p, err := c.client.Write(ctx, pr)
	if err != nil {
		err = wrapRecoverableError(err)
		return record.Position{}, cerrors.Errorf("destination client write: %w", err)
	}

	return p.Position, nil
}
