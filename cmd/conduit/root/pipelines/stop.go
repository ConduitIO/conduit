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
	_ cecdysis.CommandWithExecuteWithClientResult = (*StopCommand)(nil)
	_ ecdysis.CommandWithDocs                     = (*StopCommand)(nil)
	_ ecdysis.CommandWithFlags                    = (*StopCommand)(nil)
	_ ecdysis.CommandWithArgs                     = (*StopCommand)(nil)
)

// StopArgs holds StopCommand's positional argument.
type StopArgs struct {
	PipelineID string
}

// StopFlags holds StopCommand's flags.
type StopFlags struct {
	// Force skips the graceful drain (stopGraceful) and stops immediately
	// (stopForceful) via StopPipelineRequest.force. This may leave in-flight
	// records un-drained, but it is not a data-loss escape hatch: positions
	// are crash-safe (invariant 2) and delivery is at-least-once (invariant
	// 3), so a forced stop behaves like a crash — recoverable on the next
	// start, not lossy.
	Force bool `long:"force" usage:"skip graceful drain and stop immediately; may leave in-flight records un-drained (safe: positions are crash-safe and delivery is at-least-once, so this behaves like a crash, not data loss)"`
}

// StopCommand implements `conduit pipelines stop PIPELINE_ID [--force]`:
// transitions a running pipeline registered in a running Conduit to
// UserStopped, via the StopPipeline RPC (server-side:
// pkg/lifecycle.Service.Stop — already shipped; this command is CLI wiring
// only). Like StartCommand it requires a live server and has no
// offline/local-store fallback — see
// docs/design-documents/20260712-cli-pipeline-lifecycle-verbs.md §3.
//
// StopPipeline returns once stopGraceful has asked the pub nodes to stop; the
// drain then completes asynchronously. This command does not block/poll for
// the drain to fully settle — it reports whatever GetPipeline's read-back
// says once the transition RPC returns, so neither a human nor an agent
// should assume "stop" returning means the drain is 100% complete (a --wait
// flag is a deferred follow-up, not implemented here).
type StopCommand struct {
	args  StopArgs
	flags StopFlags
}

func (c *StopCommand) Usage() string { return "stop PIPELINE_ID" }

func (c *StopCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Stop a pipeline registered in a running Conduit",
		Long: `Transitions a running pipeline to UserStopped against a live Conduit server: gracefully
by default (drains in-flight records before returning), or immediately with --force. Requires a
reachable server (conduit run) at the configured gRPC address — there is deliberately no
offline/local-store fallback, since there is nothing running to stop without a live process.

This command does not wait for a graceful drain to fully settle before returning — it reports the
pipeline's status immediately after the transition RPC returns, which may still be transitional.

Stopping an already-stopped pipeline is refused with pipeline.not_running (exit 2); nothing is
mutated — use 'conduit pipelines start' first.`,
		Example: "conduit pipelines stop orders\n" +
			"conduit pipelines stop orders --force\n" +
			"conduit pipelines stop orders --json",
	}
}

func (c *StopCommand) Flags() []ecdysis.Flag { return ecdysis.BuildFlags(&c.flags) }

func (c *StopCommand) Args(args []string) error {
	if len(args) == 0 {
		return cerrors.Errorf("requires a pipeline ID")
	}
	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
	}
	c.args.PipelineID = args[0]
	return nil
}

// ExecuteWithClientResult calls StopPipeline with the --force flag, then
// reads the pipeline's resulting status back via GetPipeline (AC-3/AC-4). A
// failure here — not found, not running, or the server being unreachable —
// is returned as-is: the client-result decorator maps its gRPC status to the
// process exit code and the coded conduiterr the server attached survives
// unchanged (AC-6/8/10).
func (c *StopCommand) ExecuteWithClientResult(ctx context.Context, client *api.Client) (any, error) {
	if _, err := client.PipelineServiceClient.StopPipeline(ctx, &apiv1.StopPipelineRequest{
		Id:    c.args.PipelineID,
		Force: c.flags.Force,
	}); err != nil {
		return nil, cerrors.Errorf("failed to stop pipeline %q: %w", c.args.PipelineID, err)
	}

	return readLifecycleResult(ctx, client, c.args.PipelineID, actionStop, c.flags.Force)
}

func (c *StopCommand) Render(result any) string {
	return renderLifecycleResult(result)
}
