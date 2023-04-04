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
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/standalone/v1/internal/fromproto"
	"github.com/conduitio/conduit/pkg/plugin/standalone/v1/internal/toproto"
	"github.com/conduitio/conduit/pkg/record"
	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
)

type GRPCDestinationPlugin struct {
	goplugin.NetRPCUnsupportedPlugin
}

var _ goplugin.Plugin = (*GRPCDestinationPlugin)(nil)

func (p *GRPCDestinationPlugin) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, cc *grpc.ClientConn) (interface{}, error) {
	return &destinationPluginClient{grpcClient: connectorv1.NewDestinationPluginClient(cc)}, nil
}

// GRPCServer always returns an error; we're only implementing the client half
// of the interface.
func (p *GRPCDestinationPlugin) GRPCServer(*goplugin.GRPCBroker, *grpc.Server) error {
	return cerrors.New("this package only implements gRPC clients")
}

type destinationPluginClient struct {
	grpcClient connectorv1.DestinationPluginClient
	stream     connectorv1.DestinationPlugin_RunClient
}

var _ plugin.DestinationPlugin = (*destinationPluginClient)(nil)

func (s *destinationPluginClient) Configure(ctx context.Context, cfg map[string]string) error {
	protoReq := toproto.DestinationConfigureRequest(cfg)
	_, err := s.grpcClient.Configure(ctx, protoReq)
	if err != nil {
		return unwrapGRPCError(err)
	}
	return nil
}

func (s *destinationPluginClient) Start(ctx context.Context) error {
	protoReq := toproto.DestinationStartRequest()
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

func (s *destinationPluginClient) Write(_ context.Context, r record.Record) error {
	if s.stream == nil {
		return plugin.ErrStreamNotOpen
	}

	protoReq, err := toproto.DestinationRunRequest(r)
	if err != nil {
		return err
	}

	err = s.stream.Send(protoReq)
	if err != nil {
		if err == io.EOF {
			// stream was gracefully closed
			return plugin.ErrStreamNotOpen
		}
		return unwrapGRPCError(err)
	}
	return nil
}

func (s *destinationPluginClient) Ack(_ context.Context) (record.Position, error) {
	if s.stream == nil {
		return nil, plugin.ErrStreamNotOpen
	}

	resp, err := s.stream.Recv()
	if err != nil {
		if err == io.EOF {
			return nil, plugin.ErrStreamNotOpen
		}
		return nil, unwrapGRPCError(err)
	}

	position, reason := fromproto.DestinationRunResponse(resp)
	if reason != "" {
		return position, cerrors.New(reason)
	}

	return position, nil
}

func (s *destinationPluginClient) Stop(ctx context.Context, lastPosition record.Position) error {
	if s.stream == nil {
		return plugin.ErrStreamNotOpen
	}

	protoReq := toproto.DestinationStopRequest(lastPosition)
	_, err := s.grpcClient.Stop(ctx, protoReq)
	if err != nil {
		return unwrapGRPCError(err)
	}
	return nil
}

func (s *destinationPluginClient) Teardown(ctx context.Context) error {
	protoReq := toproto.DestinationTeardownRequest()
	_, err := s.grpcClient.Teardown(ctx, protoReq)
	if err != nil {
		return unwrapGRPCError(err)
	}
	return nil
}

func (s *destinationPluginClient) LifecycleOnCreated(ctx context.Context, cfg map[string]string) error {
	protoReq := toproto.DestinationLifecycleOnCreatedRequest(cfg)
	_, err := s.grpcClient.LifecycleOnCreated(ctx, protoReq)
	if err != nil {
		return unwrapGRPCError(err)
	}
	return nil
}
func (s *destinationPluginClient) LifecycleOnUpdated(ctx context.Context, cfgBefore map[string]string, cfgAfter map[string]string) error {
	protoReq := toproto.DestinationLifecycleOnUpdatedRequest(cfgBefore, cfgAfter)
	_, err := s.grpcClient.LifecycleOnUpdated(ctx, protoReq)
	if err != nil {
		return unwrapGRPCError(err)
	}
	return nil
}
func (s *destinationPluginClient) LifecycleOnDeleted(ctx context.Context, cfg map[string]string) error {
	protoReq := toproto.DestinationLifecycleOnDeletedRequest(cfg)
	_, err := s.grpcClient.LifecycleOnDeleted(ctx, protoReq)
	if err != nil {
		return unwrapGRPCError(err)
	}
	return nil
}
