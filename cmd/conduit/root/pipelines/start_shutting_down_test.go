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

package pipelines

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/matryer/is"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
	grpcstatus "google.golang.org/grpc/status"

	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/pkg/conduit/exitcode"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	httpapi "github.com/conduitio/conduit/pkg/http/api"
	"github.com/conduitio/conduit/pkg/pipeline"
)

// shuttingDownOrchestrator refuses every Start the way pkg/lifecycle (and
// pkg/lifecycle-poc) do once shutdown has begun. Only Start is called; the
// embedded nil interface panics if anything else is.
type shuttingDownOrchestrator struct {
	httpapi.PipelineOrchestrator
}

func (shuttingDownOrchestrator) Start(_ context.Context, id string) error {
	// Same shape as errShuttingDown in pkg/lifecycle/run_tracker.go.
	return conduiterr.Wrap(
		pipeline.CodeShuttingDown,
		fmt.Sprintf("can't start pipeline %s: %s", id, pipeline.ErrShuttingDown),
		pipeline.ErrShuttingDown,
	)
}

// TestStartCommand_ShuttingDownExitCode checks the client side of
// pipeline.shutting_down over a real gRPC connection: the server's
// StartPipeline handler (PipelineAPIv1 + status.PipelineError) turns the
// lifecycle refusal into a status error, the CLI's own client receives it, and
// the CLI's exit-code classifier maps it to exit 3 (Environment), because the
// code is registered as codes.Unavailable.
//
// Against a real `conduit run`, a start sent after SIGTERM almost never gets
// this far: the gRPC server's GracefulStop begins at the same moment as the
// lifecycle's StopAll, so the CLI's health check is refused instead
// (common.unavailable, also exit 3). Both paths exit 3, so a script needs
// only one branch for "Conduit is going away".
func TestStartCommand_ShuttingDownExitCode(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	is.NoErr(err)
	srv := grpc.NewServer()
	healthgrpc.RegisterHealthServer(srv, health.NewServer())
	httpapi.NewPipelineAPIv1(shuttingDownOrchestrator{}, nil, false).Register(srv)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)

	client, err := api.NewClient(ctx, ln.Addr().String())
	is.NoErr(err)
	t.Cleanup(func() { _ = client.Close() })

	cmd := &StartCommand{args: StartArgs{PipelineID: "orders"}}
	_, err = cmd.ExecuteWithClientResult(ctx, client)
	is.True(err != nil)

	st, ok := grpcstatus.FromError(err)
	is.True(ok)
	is.Equal(st.Code(), codes.Unavailable)
	is.Equal(conduiterr.FromStatus(st).Code.Reason(), pipeline.CodeShuttingDown.Reason())

	is.Equal(exitcode.ExitCode(err), exitcode.Environment) // exit 3
}
