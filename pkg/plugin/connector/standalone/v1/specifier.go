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

	connectorv1 "github.com/conduitio/conduit-connector-protocol/proto/connector/v1"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/plugin/connector/standalone/v1/internal/fromproto"
	"github.com/conduitio/conduit/pkg/plugin/connector/standalone/v1/internal/toproto"
	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
)

type GRPCSpecifierPlugin struct {
	goplugin.NetRPCUnsupportedPlugin
}

var _ goplugin.Plugin = (*GRPCSpecifierPlugin)(nil)

func (p *GRPCSpecifierPlugin) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, cc *grpc.ClientConn) (interface{}, error) {
	return &specifierPluginClient{grpcClient: connectorv1.NewSpecifierPluginClient(cc)}, nil
}

// GRPCServer always returns an error; we're only implementing the client half
// of the interface.
func (p *GRPCSpecifierPlugin) GRPCServer(*goplugin.GRPCBroker, *grpc.Server) error {
	return cerrors.New("this package only implements gRPC clients")
}

type specifierPluginClient struct {
	grpcClient connectorv1.SpecifierPluginClient
}

var _ connector.SpecifierPlugin = (*specifierPluginClient)(nil)

func (s *specifierPluginClient) Specify() (connector.Specification, error) {
	protoReq := toproto.SpecifierSpecifyRequest()
	protoResp, err := s.grpcClient.Specify(context.Background(), protoReq)
	if err != nil {
		return connector.Specification{}, unwrapGRPCError(err)
	}
	specs, err := fromproto.SpecifierSpecifyResponse(protoResp)
	if err != nil {
		return connector.Specification{}, err
	}
	return specs, nil
}
