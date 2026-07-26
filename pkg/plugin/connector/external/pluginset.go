// Copyright © 2026 Meroxa, Inc.
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

package external

import (
	"context"

	v1 "github.com/conduitio/conduit-connector-protocol/pconnector/v1"              //nolint:staticcheck // v1 is used for backwards compatibility
	clientv1 "github.com/conduitio/conduit-connector-protocol/pconnector/v1/client" //nolint:staticcheck // v1 is used for backwards compatibility
	v2 "github.com/conduitio/conduit-connector-protocol/pconnector/v2"
	clientv2 "github.com/conduitio/conduit-connector-protocol/pconnector/v2/client"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
)

// ProtocolVersion identifies which conduit-connector-protocol (pconnector)
// wire-protocol version a Dispenser speaks.
//
// Unlike standalone.Dispenser (which builds its plugin.Client via
// conduit-connector-protocol's pconnector/client.New and hands go-plugin a
// VersionedPlugins map keyed by every supported version, letting the
// spawn-path handshake negotiate which one is actually used),
// plugin.ClientConfig.Reattach carries no such negotiation: go-plugin's
// reattach() (client.go:972-1026 in github.com/hashicorp/go-plugin@v1.8.0)
// never calls checkProtoVersion and never touches VersionedPlugins at all.
// A Dispenser must therefore pick one ProtocolVersion up front and hard-code
// its gRPC stub set into plugin.ClientConfig.Plugins (singular, not
// VersionedPlugins) - see initClient in dispenser.go. Validate then confirms,
// via a live Specify round trip, that the dialed server actually implements
// the version this Dispenser hard-coded; go-plugin itself performs no such
// check on this path.
type ProtocolVersion int

const (
	// ProtocolVersionV1 hard-codes the pconnector v1 gRPC stub set.
	ProtocolVersionV1 ProtocolVersion = ProtocolVersion(v1.Version)
	// ProtocolVersionV2 hard-codes the pconnector v2 gRPC stub set. This is
	// the default a Dispenser uses unless overridden with WithProtocolVersion.
	ProtocolVersionV2 ProtocolVersion = ProtocolVersion(v2.Version)
)

// pluginSet returns the goplugin.PluginSet for the given hard-coded protocol
// version, built from conduit-connector-protocol's exported v1/v2 client
// constructors (clientv1.NewSpecifierPluginClient and friends) - the same
// constructors pconnector/client.New uses for the spawn path. The
// plugin.Plugin/plugin.GRPCPlugin wiring below (grpcPlugin) is duplicated
// from that package's own (unexported) newPluginSet/grpcPlugin: it is only
// reachable there through client.New's Cmd/spawn construction, and this
// package needs to set plugin.ClientConfig.Plugins directly instead. See
// pconnector/client/client.go in conduit-connector-protocol for the original.
func pluginSet(v ProtocolVersion) (goplugin.PluginSet, error) {
	switch v {
	case ProtocolVersionV1:
		return goplugin.PluginSet{
			"specifier":   newGRPCPlugin(clientv1.NewSpecifierPluginClient),
			"source":      newGRPCPlugin(clientv1.NewSourcePluginClient),
			"destination": newGRPCPlugin(clientv1.NewDestinationPluginClient),
		}, nil
	case ProtocolVersionV2:
		return goplugin.PluginSet{
			"specifier":   newGRPCPlugin(clientv2.NewSpecifierPluginClient),
			"source":      newGRPCPlugin(clientv2.NewSourcePluginClient),
			"destination": newGRPCPlugin(clientv2.NewDestinationPluginClient),
		}, nil
	default:
		return nil, cerrors.Errorf("unsupported pconnector protocol version %d", v)
	}
}

// grpcPlugin adapts one of conduit-connector-protocol's NewXPluginClient
// constructors into a goplugin.Plugin/goplugin.GRPCPlugin, so it can be
// placed directly into a plugin.ClientConfig.Plugins set. It only implements
// the client half (GRPCServer always errors) - this package dials existing
// servers, it never serves.
type grpcPlugin[T any] struct {
	goplugin.NetRPCUnsupportedPlugin
	newPluginClient func(cc *grpc.ClientConn) T
}

func newGRPCPlugin[T any](newPluginClient func(cc *grpc.ClientConn) T) *grpcPlugin[T] {
	return &grpcPlugin[T]{newPluginClient: newPluginClient}
}

var (
	_ goplugin.Plugin     = (*grpcPlugin[any])(nil)
	_ goplugin.GRPCPlugin = (*grpcPlugin[any])(nil)
)

func (p *grpcPlugin[T]) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, cc *grpc.ClientConn) (any, error) {
	return p.newPluginClient(cc), nil
}

// GRPCServer always returns an error; this package only implements gRPC
// clients (it dials a server, it never stands one up).
func (p *grpcPlugin[T]) GRPCServer(*goplugin.GRPCBroker, *grpc.Server) error {
	return cerrors.New("pkg/plugin/connector/external only implements gRPC clients")
}
