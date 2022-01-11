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
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
)

// This SpecificationPlugin is where we enforce the fulfillment of the
// Specifier to the Specifications Service in the RPC protocol.
type SpecificationPlugin struct {
	plugin.NetRPCUnsupportedPlugin
	Impl Specifier
}

func (sp *SpecificationPlugin) GRPCServer(broker *plugin.GRPCBroker, s *grpc.Server) error {
	// bind the grpc server to our specServer and specification plugin.
	proto.RegisterSpecificationsServer(s, &specServer{
		impl: sp.Impl,
	})
	return nil
}

func (sp *SpecificationPlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return &specClient{
		client: proto.NewSpecificationsClient(c),
	}, nil
}

// specServer is the implementation of the GRPC server (running in the plugin)
type specServer struct {
	proto.UnimplementedSpecificationsServer
	impl Specifier
}

func (sp *specServer) Specify(ctx context.Context, req *proto.Empty) (*proto.Specification, error) {
	spec, err := sp.impl.Specify()
	if err != nil {
		return nil, cerrors.Errorf("failed to get proto specification: %w", err)
	}

	ps := &proto.Specification{
		Summary:         spec.Summary,
		Version:         spec.Version,
		Author:          spec.Author,
		Description:     spec.Description,
		DestinationSpec: map[string]*proto.Parameter{},
		SourceSpec:      map[string]*proto.Parameter{},
	}

	for k, p := range spec.SourceParams {
		ps.SourceSpec[k] = &proto.Parameter{
			Default:     p.Default,
			Required:    p.Required,
			Description: p.Description,
		}
	}

	for k, p := range spec.DestinationParams {
		ps.DestinationSpec[k] = &proto.Parameter{
			Default:     p.Default,
			Required:    p.Required,
			Description: p.Description,
		}
	}

	return ps, nil
}

type specClient struct {
	client proto.SpecificationsClient
}

// bind the specClient to our Specifier interface
var _ Specifier = (*specClient)(nil)

func (sc *specClient) Specify() (Specification, error) {
	ps, err := sc.client.Specify(context.Background(), &proto.Empty{})
	if err != nil {
		return Specification{}, cerrors.Errorf("spec client failed to get spec: %w", err)
	}

	s := Specification{
		Summary:           ps.Summary,
		Version:           ps.Version,
		Author:            ps.Author,
		Description:       ps.Description,
		DestinationParams: map[string]Parameter{},
		SourceParams:      map[string]Parameter{},
	}

	for k, p := range ps.SourceSpec {
		s.SourceParams[k] = Parameter{
			Default:     p.Default,
			Description: p.Description,
			Required:    p.Required,
		}
	}

	for k, p := range ps.DestinationSpec {
		s.DestinationParams[k] = Parameter{
			Default:     p.Default,
			Description: p.Description,
			Required:    p.Required,
		}
	}

	return s, nil
}
