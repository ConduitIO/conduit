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

// SourcePlugin represents a plugin that acts as a source connector.
type SourcePlugin struct {
	plugin.NetRPCUnsupportedPlugin
	Impl Source
}

func (sp *SourcePlugin) GRPCServer(broker *plugin.GRPCBroker, s *grpc.Server) error {
	proto.RegisterSourceServer(s, &sourceServer{
		impl: sp.Impl,
	})

	return nil
}

func (sp *SourcePlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return &sourceClient{
		client: proto.NewSourceClient(c),
	}, nil
}

// sourceServer is the implementation of the GRPC server (running in the plugin).
type sourceServer struct {
	proto.UnimplementedSourceServer
	impl Source
}

func (s *sourceServer) Open(ctx context.Context, cfg *proto.Config) (*proto.Empty, error) {
	err := s.impl.Open(ctx, toInternalConfig(cfg))
	if err != nil {
		return nil, cerrors.Errorf("source server open: %w", err)
	}
	return &proto.Empty{}, nil
}

func (s *sourceServer) Teardown(ctx context.Context, req *proto.Empty) (*proto.Empty, error) {
	err := s.impl.Teardown()
	if err != nil {
		return nil, cerrors.Errorf("source server teardown: %w", err)
	}
	return &proto.Empty{}, nil
}

func (s *sourceServer) Validate(ctx context.Context, cfg *proto.Config) (*proto.Empty, error) {
	err := s.impl.Validate(toInternalConfig(cfg))
	if err != nil {
		return nil, cerrors.Errorf("source server validate: %w", err)
	}
	return &proto.Empty{}, nil
}

func (s *sourceServer) Read(ctx context.Context, req *proto.Position) (*proto.Record, error) {
	rec, err := s.impl.Read(ctx, req.Position)
	if err != nil {
		return nil, cerrors.Errorf("source server read: %w", err)
	}
	r, err := toProtoRecord(rec)
	if err != nil {
		return nil, cerrors.Errorf("source server toProtoRecord: %w", err)
	}
	return r, nil
}

func (s *sourceServer) Ack(ctx context.Context, req *proto.Position) (*proto.Empty, error) {
	err := s.impl.Ack(ctx, req.Position)
	if err != nil {
		return nil, cerrors.Errorf("source server ack: %w", err)
	}
	return &proto.Empty{}, nil
}

// sourceClient is the implementation of the GRPC client (running in conduit).
type sourceClient struct {
	client proto.SourceClient
}

var _ Source = (*sourceClient)(nil)

func (c *sourceClient) Open(ctx context.Context, cfg Config) error {
	pcfg := &proto.Config{}
	setProtoConfig(cfg, pcfg)
	_, err := c.client.Open(ctx, pcfg)
	if err != nil {
		return cerrors.Errorf("source client open: %w", wrapRecoverableError(err))
	}
	return nil
}

func (c *sourceClient) Teardown() error {
	_, err := c.client.Teardown(context.TODO(), &proto.Empty{})
	if err != nil {
		return cerrors.Errorf("source client teardown: %w", wrapRecoverableError(err))
	}
	return nil
}

func (c *sourceClient) Validate(cfg Config) error {
	pcfg := &proto.Config{}
	setProtoConfig(cfg, pcfg)
	_, err := c.client.Validate(context.TODO(), pcfg)
	if err != nil {
		return cerrors.Errorf("source client validate: %w", wrapRecoverableError(err))
	}
	return nil
}

func (c *sourceClient) Read(ctx context.Context, pos record.Position) (record.Record, error) {
	resp, err := c.client.Read(ctx, &proto.Position{Position: pos})
	if err != nil {
		err = wrapRecoverableError(err)
		return record.Record{}, cerrors.Errorf("source client read: %w", err)
	}

	r, err := toInternalRecord(resp)
	if err != nil {
		return record.Record{}, cerrors.Errorf("source client toInternalRecord: %w", err)
	}

	return r, nil
}

func (c *sourceClient) Ack(ctx context.Context, pos record.Position) error {
	_, err := c.client.Ack(ctx, &proto.Position{Position: pos})
	if err != nil {
		err = wrapRecoverableError(err)
		return cerrors.Errorf("source client ack: %w", err)
	}
	return nil
}
