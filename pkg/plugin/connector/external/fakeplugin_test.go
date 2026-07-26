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

package external_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/conduitio/conduit-connector-protocol/pconnector"
	pconnserver "github.com/conduitio/conduit-connector-protocol/pconnector/server"
	v2 "github.com/conduitio/conduit-connector-protocol/pconnector/v2"
	serverv2 "github.com/conduitio/conduit-connector-protocol/pconnector/v2/server"
	connectorv2 "github.com/conduitio/conduit-connector-protocol/proto/connector/v2"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	goplugin "github.com/hashicorp/go-plugin"
	"github.com/matryer/is"
	"google.golang.org/grpc"
)

// fakeSpecifier is a minimal pconnector.SpecifierPlugin used to drive a real
// gRPC server for the tests in this package - it exists to prove the
// Reattach + hard-coded-PluginSet wiring actually carries a real RPC round
// trip, not to test conduit-connector-protocol itself.
type fakeSpecifier struct {
	spec pconnector.Specification
}

func (f *fakeSpecifier) Specify(context.Context, pconnector.SpecifierSpecifyRequest) (pconnector.SpecifierSpecifyResponse, error) {
	return pconnector.SpecifierSpecifyResponse{Specification: f.spec}, nil
}

// fakeSource is a minimal pconnector.SourcePlugin - every method is a no-op
// stub except Configure, which is what the tests in this package exercise.
type fakeSource struct {
	configured chan pconnector.SourceConfigureRequest
}

func (f *fakeSource) Configure(_ context.Context, req pconnector.SourceConfigureRequest) (pconnector.SourceConfigureResponse, error) {
	if f.configured != nil {
		f.configured <- req
	}
	return pconnector.SourceConfigureResponse{}, nil
}

func (f *fakeSource) Open(context.Context, pconnector.SourceOpenRequest) (pconnector.SourceOpenResponse, error) {
	return pconnector.SourceOpenResponse{}, nil
}

func (f *fakeSource) Run(context.Context, pconnector.SourceRunStream) error { return nil }

func (f *fakeSource) Stop(context.Context, pconnector.SourceStopRequest) (pconnector.SourceStopResponse, error) {
	return pconnector.SourceStopResponse{}, nil
}

func (f *fakeSource) Teardown(context.Context, pconnector.SourceTeardownRequest) (pconnector.SourceTeardownResponse, error) {
	return pconnector.SourceTeardownResponse{}, nil
}

func (f *fakeSource) LifecycleOnCreated(context.Context, pconnector.SourceLifecycleOnCreatedRequest) (pconnector.SourceLifecycleOnCreatedResponse, error) {
	return pconnector.SourceLifecycleOnCreatedResponse{}, nil
}

func (f *fakeSource) LifecycleOnUpdated(context.Context, pconnector.SourceLifecycleOnUpdatedRequest) (pconnector.SourceLifecycleOnUpdatedResponse, error) {
	return pconnector.SourceLifecycleOnUpdatedResponse{}, nil
}

func (f *fakeSource) LifecycleOnDeleted(context.Context, pconnector.SourceLifecycleOnDeletedRequest) (pconnector.SourceLifecycleOnDeletedResponse, error) {
	return pconnector.SourceLifecycleOnDeletedResponse{}, nil
}

// fakeDestination is a minimal pconnector.DestinationPlugin - every method is
// a no-op stub except Configure.
type fakeDestination struct {
	configured chan pconnector.DestinationConfigureRequest
}

func (f *fakeDestination) Configure(_ context.Context, req pconnector.DestinationConfigureRequest) (pconnector.DestinationConfigureResponse, error) {
	if f.configured != nil {
		f.configured <- req
	}
	return pconnector.DestinationConfigureResponse{}, nil
}

func (f *fakeDestination) Open(context.Context, pconnector.DestinationOpenRequest) (pconnector.DestinationOpenResponse, error) {
	return pconnector.DestinationOpenResponse{}, nil
}

func (f *fakeDestination) Run(context.Context, pconnector.DestinationRunStream) error { return nil }

func (f *fakeDestination) Stop(context.Context, pconnector.DestinationStopRequest) (pconnector.DestinationStopResponse, error) {
	return pconnector.DestinationStopResponse{}, nil
}

func (f *fakeDestination) Teardown(context.Context, pconnector.DestinationTeardownRequest) (pconnector.DestinationTeardownResponse, error) {
	return pconnector.DestinationTeardownResponse{}, nil
}

func (f *fakeDestination) LifecycleOnCreated(context.Context, pconnector.DestinationLifecycleOnCreatedRequest) (pconnector.DestinationLifecycleOnCreatedResponse, error) {
	return pconnector.DestinationLifecycleOnCreatedResponse{}, nil
}

func (f *fakeDestination) LifecycleOnUpdated(context.Context, pconnector.DestinationLifecycleOnUpdatedRequest) (pconnector.DestinationLifecycleOnUpdatedResponse, error) {
	return pconnector.DestinationLifecycleOnUpdatedResponse{}, nil
}

func (f *fakeDestination) LifecycleOnDeleted(context.Context, pconnector.DestinationLifecycleOnDeletedRequest) (pconnector.DestinationLifecycleOnDeletedResponse, error) {
	return pconnector.DestinationLifecycleOnDeletedResponse{}, nil
}

// fakeServer is a real, listening go-plugin gRPC server standing in for an
// inline_source/inline_destination host process - the same shape
// docs/design-documents/20260707-python-connector-sdk.md's server is, minus
// the handshake-line-on-stdout write a spawned plugin does (this package
// never spawns anything, so nothing reads that line). Built with
// go-plugin's own test-mode Serve (plugin.ServeConfig.Test), the same
// mechanism go-plugin's own test suite (server_test.go) uses to test
// Reattach without an actual subprocess.
type fakeServer struct {
	addr    net.Addr
	closeCh chan struct{}
	cancel  context.CancelFunc
}

// stop shuts the fake server down, tolerating that it may already be
// stopped: every test in this package that completes a terminal Specify/
// Teardown call already causes Dispenser.teardown to Kill its plugin.Client,
// which gracefully closes the gRPC connection - go-plugin's Controller
// service Shutdown RPC - causing this server to call GRPCServer.Stop() on
// itself well before this cleanup ever runs. go-plugin's own server-side
// shutdown-on-context-cancellation path calls the same Stop() again
// independently, and go-plugin's GRPCServer.Stop() is not safe to call
// concurrently with itself (a data race in go-plugin@v1.8.0 itself, not in
// this package). Waiting for closeCh first, and only falling back to
// cancel() if the server has not already exited on its own, avoids
// exercising that race in the (typical, in this package) case where the
// Dispenser already shut it down.
func (s *fakeServer) stop() {
	select {
	case <-s.closeCh:
		return
	case <-time.After(2 * time.Second):
	}
	s.cancel()
	<-s.closeCh
}

// startFakeServer starts a full v1+v2 conduit-connector-protocol server
// (via pconnector/server.Serve, the same entry point a real connector
// plugin - standalone or external - uses) and returns its listening
// address.
func startFakeServer(t *testing.T, specifier pconnector.SpecifierPlugin, source pconnector.SourcePlugin, destination pconnector.DestinationPlugin) *fakeServer {
	t.Helper()
	is := is.New(t)

	// go-plugin's VersionedPlugins mechanism picks which version's PluginSet
	// a *spawned* server actually registers on its grpc.Server by reading
	// PLUGIN_PROTOCOL_VERSIONS from its own environment (server.go's
	// protocolVersion, matched against client.go:642, which sets it on
	// cmd.Env before spawning) - that negotiation is carried entirely by the
	// spawn path's environment, not by anything Reattach participates in.
	// An external connector process is never spawned by Conduit, so nothing
	// sets this for it automatically; a real external-connector host process
	// serving multiple versions needs to set it out-of-band (or simply only
	// ever serve one version). This test pins it explicitly to reproduce
	// that same deterministic selection, rather than falling through to
	// go-plugin's documented fallback ("return the lowest version") and
	// silently exercising v1 instead of the v2 this package defaults to.
	t.Setenv("PLUGIN_PROTOCOL_VERSIONS", "2")

	ctx, cancel := context.WithCancel(context.Background())
	reattachCh := make(chan *goplugin.ReattachConfig, 1)
	closeCh := make(chan struct{})

	go func() {
		_ = pconnserver.Serve(
			func() pconnector.SpecifierPlugin { return specifier },
			func() pconnector.SourcePlugin { return source },
			func() pconnector.DestinationPlugin { return destination },
			pconnserver.WithDebug(ctx, reattachCh, closeCh),
		)
	}()

	var cfg *goplugin.ReattachConfig
	select {
	case cfg = <-reattachCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for fake server to start")
	}
	is.True(cfg != nil)

	srv := &fakeServer{addr: cfg.Addr, closeCh: closeCh, cancel: cancel}
	t.Cleanup(srv.stop)
	return srv
}

// startFakeV2OnlyServer starts a go-plugin server that ONLY implements the
// pconnector v2 gRPC services (no v1) - used to prove Dispenser.Validate's
// version-mismatch classification against a server that genuinely doesn't
// speak the version a Dispenser hard-coded, as opposed to one that is merely
// unreachable. pconnector/server.Serve always registers both v1 and v2
// (matching a real connector plugin, which supports both), so a v1-only or
// v2-only server has to be built directly against go-plugin.
func startFakeV2OnlyServer(t *testing.T, specifier pconnector.SpecifierPlugin) *fakeServer {
	t.Helper()
	is := is.New(t)

	ctx, cancel := context.WithCancel(context.Background())
	reattachCh := make(chan *goplugin.ReattachConfig, 1)
	closeCh := make(chan struct{})

	plugins := goplugin.PluginSet{
		"specifier": &v2OnlySpecifierPlugin{impl: specifier},
	}

	go goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: pconnector.HandshakeConfig,
		VersionedPlugins: map[int]goplugin.PluginSet{
			v2.Version: plugins,
		},
		GRPCServer: goplugin.DefaultGRPCServer,
		Test: &goplugin.ServeTestConfig{
			Context:          ctx,
			ReattachConfigCh: reattachCh,
			CloseCh:          closeCh,
		},
	})

	var cfg *goplugin.ReattachConfig
	select {
	case cfg = <-reattachCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for fake v2-only server to start")
	}
	is.True(cfg != nil)

	srv := &fakeServer{addr: cfg.Addr, closeCh: closeCh, cancel: cancel}
	t.Cleanup(srv.stop)
	return srv
}

// v2OnlySpecifierPlugin registers only the v2 specifier gRPC service - it
// deliberately does not implement source/destination or v1, so a Dispenser
// hard-coded to a different ProtocolVersion dialing it gets a real
// "service/method unknown" gRPC error, not a hand-rolled stand-in for one.
type v2OnlySpecifierPlugin struct {
	goplugin.NetRPCUnsupportedPlugin
	impl pconnector.SpecifierPlugin
}

var (
	_ goplugin.Plugin     = (*v2OnlySpecifierPlugin)(nil)
	_ goplugin.GRPCPlugin = (*v2OnlySpecifierPlugin)(nil)
)

// GRPCClient always errors; this fake only implements the server half.
func (p *v2OnlySpecifierPlugin) GRPCClient(context.Context, *goplugin.GRPCBroker, *grpc.ClientConn) (any, error) {
	return nil, cerrors.New("v2OnlySpecifierPlugin only implements a gRPC server")
}

func (p *v2OnlySpecifierPlugin) GRPCServer(_ *goplugin.GRPCBroker, s *grpc.Server) error {
	connectorv2.RegisterSpecifierPluginServer(s, serverv2.NewSpecifierPluginServer(p.impl))
	return nil
}
