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
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/conduitio/ecdysis"
	json "github.com/goccy/go-json"
	"github.com/matryer/is"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/pkg/conduit/exitcode"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	httpapi "github.com/conduitio/conduit/pkg/http/api"
	"github.com/conduitio/conduit/pkg/pipeline"
)

// runningOrchestrator refuses every Start with pipeline.running, the way the
// lifecycle services do for a pipeline that already has a run.
type runningOrchestrator struct {
	httpapi.PipelineOrchestrator
}

func (runningOrchestrator) Start(_ context.Context, id string) error {
	err := conduiterr.Wrap(
		pipeline.CodePipelineRunning,
		fmt.Sprintf("can't start pipeline %s: %s", id, pipeline.ErrPipelineRunning),
		pipeline.ErrPipelineRunning,
	)
	err.Suggestion = "the pipeline is already running or starting; stop it first if you need to restart it"
	return err
}

// TestStartCommand_JSONRefusalIsJSON reproduces the gap #2989 reported: with
// --json, a refused start printed cobra's plain "Error: rpc error: ..." text
// and nothing on stdout. It runs the real decorator chain against a real gRPC
// server and checks that stdout is one JSON object carrying the stable code,
// the server's message and its suggestion, that stderr stays empty, and that
// the exit code is unchanged (2, FailedPrecondition).
func TestStartCommand_JSONRefusalIsJSON(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	is.NoErr(err)
	srv := grpc.NewServer()
	healthgrpc.RegisterHealthServer(srv, health.NewServer())
	httpapi.NewPipelineAPIv1(runningOrchestrator{}, nil, false).Register(srv)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)
	// The client decorator reads api.grpc.address from ./conduit.yaml.
	dir := t.TempDir()
	is.NoErr(os.WriteFile(filepath.Join(dir, "conduit.yaml"),
		[]byte("api:\n  grpc:\n    address: "+ln.Addr().String()+"\n"), 0o600))
	t.Chdir(dir)

	run := func(args ...string) (string, string, error) {
		e := ecdysis.New(ecdysis.WithDecorators(cecdysis.CommandWithExecuteWithClientResultDecorator{}))
		cmd := e.MustBuildCobraCommand(&StartCommand{})
		cmd.SilenceUsage = true // as cmd/conduit/cli sets on the root
		var stdout, stderr bytes.Buffer
		cmd.SetOut(&stdout)
		cmd.SetErr(&stderr)
		cmd.SetArgs(args)
		err := cmd.ExecuteContext(ctx)
		return stdout.String(), stderr.String(), err
	}

	stdout, stderr, err := run("orders", "--json")
	is.True(err != nil)
	is.Equal(exitcode.ExitCode(err), exitcode.Validation) // exit 2, unchanged
	is.Equal(stderr, "")                                  // no duplicate "Error:" line

	var got cecdysis.ClientResultError
	is.NoErr(json.Unmarshal([]byte(stdout), &got))
	is.True(got.Error != nil)
	is.Equal(got.Error.Code, "pipeline.running")
	is.Equal(got.Error.Message, "can't start pipeline orders: pipeline is running")
	is.Equal(got.Error.Suggestion, "the pipeline is already running or starting; stop it first if you need to restart it")

	// Without --json the human path is unchanged: nothing on stdout, cobra
	// prints the error on stderr.
	stdout, stderr, err = run("orders")
	is.True(err != nil)
	is.Equal(stdout, "")
	is.True(stderr != "")
}
