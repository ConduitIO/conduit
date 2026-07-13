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

	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/conduitio/ecdysis"
)

var (
	_ cecdysis.CommandWithExecuteWithClientResult = (*StartCommand)(nil)
	_ ecdysis.CommandWithDocs                     = (*StartCommand)(nil)
	_ ecdysis.CommandWithArgs                     = (*StartCommand)(nil)
)

// StartArgs holds StartCommand's positional argument.
type StartArgs struct {
	PipelineID string
}

// StartCommand implements `conduit pipelines start PIPELINE_ID`: transitions
// a stopped pipeline registered in a running Conduit to Running, via the
// StartPipeline RPC (server-side: pkg/lifecycle.Service.Start — already
// shipped; this command is CLI wiring only). Mirrors InspectCommand's shape:
// one required positional ID, a live gRPC client, --json for free via the
// client-result decorator.
//
// Unlike deploy/apply, start has no offline/local-store fallback: starting a
// pipeline means running its goroutines inside a live process, which only has
// meaning against a process that stays up (a `conduit run` server) — a
// one-shot CLI invocation that started the pipeline would have it die the
// moment the process exited. See
// docs/design-documents/20260712-cli-pipeline-lifecycle-verbs.md §3.
type StartCommand struct {
	args StartArgs
}

func (c *StartCommand) Usage() string { return "start PIPELINE_ID" }

func (c *StartCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Start a pipeline registered in a running Conduit",
		Long: `Transitions a stopped pipeline to Running against a live Conduit server. Requires a
reachable server (conduit run) at the configured gRPC address — there is deliberately no
offline/local-store fallback, since a started pipeline's goroutines only stay alive inside a
long-running process; see 'conduit pipelines apply' for the offline pipeline-config path.

Starting an already-running pipeline is refused with pipeline.running (exit 2); nothing is
mutated — use 'conduit pipelines stop' first if you need to restart it.`,
		Example: "conduit pipelines start orders\n" +
			"conduit pipelines start orders --json",
	}
}

func (c *StartCommand) Args(args []string) error {
	if len(args) == 0 {
		return cerrors.Errorf("requires a pipeline ID")
	}
	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
	}
	c.args.PipelineID = args[0]
	return nil
}

// ExecuteWithClientResult calls StartPipeline, then reads the pipeline's
// resulting status back via GetPipeline (AC-1/AC-2). A failure here — not
// found, already running, or the server being unreachable — is returned
// as-is: the client-result decorator maps its gRPC status to the process
// exit code (NotFound/FailedPrecondition -> 2, Unavailable -> 3) and the
// coded conduiterr the server attached survives unchanged (AC-6/8/10).
func (c *StartCommand) ExecuteWithClientResult(ctx context.Context, client *api.Client) (any, error) {
	if _, err := client.PipelineServiceClient.StartPipeline(ctx, &apiv1.StartPipelineRequest{
		Id: c.args.PipelineID,
	}); err != nil {
		return nil, cerrors.Errorf("failed to start pipeline %q: %w", c.args.PipelineID, err)
	}

	return readLifecycleResult(ctx, client, c.args.PipelineID, actionStart, false)
}

func (c *StartCommand) Render(result any) string {
	return renderLifecycleResult(result)
}
