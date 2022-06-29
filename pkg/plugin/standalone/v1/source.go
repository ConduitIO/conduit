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

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/multierror"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/standalone/v1/internal/fromproto"
	"github.com/conduitio/conduit/pkg/plugin/standalone/v1/internal/toproto"
	"github.com/conduitio/conduit/pkg/record"
	goplugin "github.com/hashicorp/go-plugin"
	connectorv1 "go.buf.build/library/go-grpc/conduitio/conduit-connector-protocol/connector/v1"
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
func (p *GRPCSourcePlugin) GRPCServer(broker *goplugin.GRPCBroker, s *grpc.Server) error {
	return cerrors.New("this package only implements gRPC clients")
}

type sourcePluginClient struct {
	grpcClient connectorv1.SourcePluginClient
	stream     connectorv1.SourcePlugin_RunClient
}

var _ plugin.SourcePlugin = (*sourcePluginClient)(nil)

func (s *sourcePluginClient) Configure(ctx context.Context, cfg map[string]string) error {
	protoReq, err := toproto.SourceConfigureRequest(cfg)
	if err != nil {
		return err
	}
	protoResp, err := s.grpcClient.Configure(ctx, protoReq)
	if err != nil {
		return unwrapGRPCError(err)
	}
	_ = protoResp // response is empty
	return nil
}

func (s *sourcePluginClient) Start(ctx context.Context, p record.Position) error {
	protoReq, err := toproto.SourceStartRequest(p)
	if err != nil {
		return err
	}
	protoResp, err := s.grpcClient.Start(ctx, protoReq)
	if err != nil {
		return unwrapGRPCError(err)
	}
	_ = protoResp // response is empty

	s.stream, err = s.grpcClient.Run(ctx)
	if err != nil {
		return unwrapGRPCError(err)
	}

	return nil
}

func (s *sourcePluginClient) Read(ctx context.Context) (record.Record, error) {
	if s.stream == nil {
		return record.Record{}, plugin.ErrStreamNotOpen
	}

	protoResp, err := s.stream.Recv()
	if err != nil {
		if err == io.EOF {
			return record.Record{}, plugin.ErrStreamNotOpen
		}
		return record.Record{}, unwrapGRPCError(err)
	}
	goResp, err := fromproto.SourceRunResponse(protoResp)
	if err != nil {
		return record.Record{}, err
	}
	return goResp, nil
}

func (s *sourcePluginClient) Ack(ctx context.Context, p record.Position) error {
	if s.stream == nil {
		return plugin.ErrStreamNotOpen
	}

	protoReq, err := toproto.SourceRunRequest(p)
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

func (s *sourcePluginClient) Stop(ctx context.Context) (record.Position, error) {
	if s.stream == nil {
		return nil, plugin.ErrStreamNotOpen
	}

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
	var errOut error

	if s.stream != nil {
		err := s.stream.CloseSend()
		if err != nil {
			errOut = multierror.Append(errOut, unwrapGRPCError(err))
		}
	}

	protoReq := toproto.SourceTeardownRequest()
	protoResp, err := s.grpcClient.Teardown(ctx, protoReq)
	if err != nil {
		errOut = multierror.Append(errOut, unwrapGRPCError(err))
	}
	_ = protoResp // response is empty

	return errOut
}
