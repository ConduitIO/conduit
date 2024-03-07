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

package standalonev1

import (
	"context"
	"io"

	connectorv1 "github.com/conduitio/conduit-connector-protocol/proto/connector/v1"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/plugin/connector/standalone/v1/internal/fromproto"
	"github.com/conduitio/conduit/pkg/plugin/connector/standalone/v1/internal/toproto"
	"github.com/conduitio/conduit/pkg/record"
	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
)

type GRPCSourcePlugin struct {
	goplugin.NetRPCUnsupportedPlugin
}

var _ goplugin.Plugin = (*GRPCSourcePlugin)(nil)

func (p *GRPCSourcePlugin) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, cc *grpc.ClientConn) (interface{}, error) {
	return &sourcePluginClient{grpcClient: connectorv1.NewSourcePluginClient(cc)}, nil
}

// GRPCServer always returns an error; we're only implementing the client half
// of the interface.
func (p *GRPCSourcePlugin) GRPCServer(*goplugin.GRPCBroker, *grpc.Server) error {
	return cerrors.New("this package only implements gRPC clients")
}

type sourcePluginClient struct {
	grpcClient connectorv1.SourcePluginClient
	stream     connectorv1.SourcePlugin_RunClient
}

var _ connector.SourcePlugin = (*sourcePluginClient)(nil)

func (s *sourcePluginClient) Configure(ctx context.Context, cfg map[string]string) error {
	protoReq := toproto.SourceConfigureRequest(cfg)
	_, err := s.grpcClient.Configure(ctx, protoReq)
	if err != nil {
		return unwrapGRPCError(err)
	}
	return nil
}

func (s *sourcePluginClient) Start(ctx context.Context, p record.Position) error {
	protoReq := toproto.SourceStartRequest(p)
	_, err := s.grpcClient.Start(ctx, protoReq)
	if err != nil {
		return unwrapGRPCError(err)
	}

	s.stream, err = s.grpcClient.Run(ctx)
	if err != nil {
		return unwrapGRPCError(err)
	}

	return nil
}

func (s *sourcePluginClient) Read(context.Context) (record.Record, error) {
	if s.stream == nil {
		return record.Record{}, connector.ErrStreamNotOpen
	}

	protoResp, err := s.stream.Recv()
	if err != nil {
		if err == io.EOF {
			return record.Record{}, connector.ErrStreamNotOpen
		}
		return record.Record{}, unwrapGRPCError(err)
	}
	goResp, err := fromproto.SourceRunResponse(protoResp)
	if err != nil {
		return record.Record{}, err
	}
	return goResp, nil
}

func (s *sourcePluginClient) Ack(_ context.Context, p record.Position) error {
	if s.stream == nil {
		return connector.ErrStreamNotOpen
	}

	protoReq := toproto.SourceRunRequest(p)
	err := s.stream.Send(protoReq)
	if err != nil {
		if err == io.EOF {
			// stream was gracefully closed
			return connector.ErrStreamNotOpen
		}
		return unwrapGRPCError(err)
	}
	return nil
}

func (s *sourcePluginClient) Stop(ctx context.Context) (record.Position, error) {
	protoReq := toproto.SourceStopRequest()
	protoResp, err := s.grpcClient.Stop(ctx, protoReq)
	if err != nil {
		return nil, unwrapGRPCError(err)
	}
	goResp, err := fromproto.SourceStopResponse(protoResp)
	if err != nil {
		return nil, err
	}
	return goResp, nil
}

func (s *sourcePluginClient) Teardown(ctx context.Context) error {
	protoReq := toproto.SourceTeardownRequest()
	_, err := s.grpcClient.Teardown(ctx, protoReq)
	if err != nil {
		return unwrapGRPCError(err)
	}
	return nil
}

func (s *sourcePluginClient) LifecycleOnCreated(ctx context.Context, cfg map[string]string) error {
	protoReq := toproto.SourceLifecycleOnCreatedRequest(cfg)
	_, err := s.grpcClient.LifecycleOnCreated(ctx, protoReq)
	if err != nil {
		return unwrapGRPCError(err)
	}
	return nil
}
func (s *sourcePluginClient) LifecycleOnUpdated(ctx context.Context, cfgBefore map[string]string, cfgAfter map[string]string) error {
	protoReq := toproto.SourceLifecycleOnUpdatedRequest(cfgBefore, cfgAfter)
	_, err := s.grpcClient.LifecycleOnUpdated(ctx, protoReq)
	if err != nil {
		return unwrapGRPCError(err)
	}
	return nil
}
func (s *sourcePluginClient) LifecycleOnDeleted(ctx context.Context, cfg map[string]string) error {
	protoReq := toproto.SourceLifecycleOnDeletedRequest(cfg)
	_, err := s.grpcClient.LifecycleOnDeleted(ctx, protoReq)
	if err != nil {
		return unwrapGRPCError(err)
	}
	return nil
}
