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

package deploy

import (
	"context"
	"net"
	"testing"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/http/api"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/matryer/is"
	"google.golang.org/grpc"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
)

// fakePipelineServer implements just enough of apiv1.PipelineServiceServer
// for NewService's live-server-preference tests: PlanPipeline/ApplyPipeline
// echo back a fixed Diff, so a test can assert RemoteService made a real
// round trip over the wire without needing a real provisioning.Service or
// database behind it.
type fakePipelineServer struct {
	apiv1.UnimplementedPipelineServiceServer
	diff *apiv1.Diff
}

func (f *fakePipelineServer) PlanPipeline(context.Context, *apiv1.PlanPipelineRequest) (*apiv1.PlanPipelineResponse, error) {
	return &apiv1.PlanPipelineResponse{Diff: f.diff}, nil
}

func (f *fakePipelineServer) ApplyPipeline(context.Context, *apiv1.ApplyPipelineRequest) (*apiv1.ApplyPipelineResponse, error) {
	return &apiv1.ApplyPipelineResponse{Diff: f.diff}, nil
}

// startFakeServer starts a real gRPC server (the health service NewClient's
// CheckHealth needs, plus a fake PipelineService) on an OS-assigned loopback
// port, returning its address. The server is stopped via t.Cleanup.
func startFakeServer(t *testing.T, diff *apiv1.Diff) string {
	t.Helper()
	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	srv := grpc.NewServer()
	healthgrpc.RegisterHealthServer(srv, api.NewHealthServer(nil, log.Nop()))
	apiv1.RegisterPipelineServiceServer(srv, &fakePipelineServer{diff: diff})

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	return lis.Addr().String()
}

// TestNewService_PrefersLiveServer is the regression test for AC-8's "uses
// the API when a server is reachable" half: with a fake server listening and
// healthy at cfg.API.GRPC.Address, NewService must return a *RemoteService
// (never fall back to NewLocalService), and that service's Plan call must
// actually reach the fake server — proving this isn't just a type check.
func TestNewService_PrefersLiveServer(t *testing.T) {
	is := is.New(t)

	want := &apiv1.Diff{PipelineId: "p1", Hash: "remote-hash"}
	addr := startFakeServer(t, want)

	var cfg conduit.Config
	cfg.API.GRPC.Address = addr

	svc, cleanup, err := NewService(context.Background(), cfg)
	is.NoErr(err)
	t.Cleanup(func() { _ = cleanup() })

	_, ok := svc.(*RemoteService)
	is.True(ok) // must have preferred the live server, not NewLocalService's PlanApplier

	diff, err := svc.Plan(context.Background(), config.Pipeline{ID: "p1"})
	is.NoErr(err)
	is.Equal(diff.Hash, "remote-hash") // proves the round trip actually reached the fake server
}

// TestNewService_FallsBackToStandalone_WhenNoServerReachable is AC-8's other
// half: with nothing listening at cfg.API.GRPC.Address, NewService must fall
// back to NewLocalService — proven by observing the exact CodeStandaloneUnsafe
// refusal NewLocalService gives for a non-Badger store (see service_test.go's
// TestNewLocalService_RefusesNonBadgerStores), which only NewLocalService's
// code path can produce; RemoteService has no such check.
func TestNewService_FallsBackToStandalone_WhenNoServerReachable(t *testing.T) {
	is := is.New(t)

	// An address nothing listens on: bind an OS-assigned loopback port, then
	// close it immediately, so a dial is refused rather than hanging or
	// (worse) landing on some unrelated service.
	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	is.NoErr(err)
	addr := lis.Addr().String()
	is.NoErr(lis.Close())

	var cfg conduit.Config
	cfg.API.GRPC.Address = addr
	cfg.DB.Type = conduit.DBTypeSQLite // any non-Badger type trips NewLocalService's known refusal

	_, cleanup, err := NewService(context.Background(), cfg)
	t.Cleanup(func() { _ = cleanup() })
	is.True(err != nil)

	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeStandaloneUnsafe) // only NewLocalService's path raises this
}
