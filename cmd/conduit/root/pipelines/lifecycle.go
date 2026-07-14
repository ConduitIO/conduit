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

	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/cmd/conduit/internal/display"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
)

// Lifecycle action names — the values readLifecycleResult's action parameter
// takes, and LifecycleResult.Action's two possible values.
const (
	actionStart = "start"
	actionStop  = "stop"
)

// The 5-value status vocabulary pkg/pipeline.Status exposes internally,
// rendered by pipelineStatusLabel. See that function's doc.
const (
	statusRunning     = "Running"
	statusStopped     = "Stopped"
	statusUserStopped = "UserStopped"
	statusDegraded    = "Degraded"
	statusRecovering  = "Recovering"
)

// LifecycleResult is the shared result shape StartCommand and StopCommand
// return: the pipeline transitioned, which action ran, and its resulting
// status, read back from the server after the transition RPC returns. See
// docs/design-documents/20260712-cli-pipeline-lifecycle-verbs.md §1.
//
// Force is only meaningful for "stop" and is omitted from JSON when false, so
// a "start" result never carries a stray "force":false.
type LifecycleResult struct {
	PipelineID string `json:"pipeline_id"`
	Action     string `json:"action"` // "start" | "stop"
	Force      bool   `json:"force,omitempty"`
	Status     string `json:"status"`

	// protoStatus is the raw read-back status Render uses to match the
	// existing inspect-style lowercase text vocabulary
	// (display.PrintStatusFromProtoString). Unexported: never marshaled, so
	// --json only ever carries the Status field above.
	protoStatus apiv1.Pipeline_Status
}

// readLifecycleResult reads a pipeline's status back via GetPipeline after a
// successful start/stop transition RPC and builds the LifecycleResult both
// commands render. Called only after the transition RPC itself has already
// succeeded — a failure here means the transition took effect but the
// read-back didn't, which is reported as its own error rather than silently
// swallowed (an agent or script must not see a false success).
func readLifecycleResult(ctx context.Context, client *api.Client, id, action string, force bool) (*LifecycleResult, error) {
	resp, err := client.PipelineServiceClient.GetPipeline(ctx, &apiv1.GetPipelineRequest{Id: id})
	if err != nil {
		return nil, cerrors.Errorf("%s succeeded but reading back pipeline %q status failed: %w", action, id, err)
	}

	status := resp.GetPipeline().GetState().GetStatus()
	return &LifecycleResult{
		PipelineID:  id,
		Action:      action,
		Force:       force,
		Status:      pipelineStatusLabel(action, status),
		protoStatus: status,
	}, nil
}

// pipelineStatusLabel renders a lifecycle transition's resulting status using
// the 5-value vocabulary pkg/pipeline.Status exposes internally (Running,
// SystemStopped, UserStopped, Degraded, Recovering) — see
// docs/design-documents/20260704-phase-1-execution-plan.md §3: "the UI can't
// collapse System/UserStopped into one 'Stopped' while the CLI keeps them
// distinct."
//
// The wire Status enum collapses StatusUserStopped/StatusSystemStopped into a
// single Pipeline_STATUS_STOPPED (pkg/http/api/toproto/pipeline.go
// PipelineStatus). State.stopped_reason now distinguishes them directly on the
// wire; switching this label to read it (instead of inferring from the action)
// is a tracked follow-up (issue #2630). Until then: a successful "stop" action's read-back can
// only ever be StatusUserStopped (StatusSystemStopped is set exclusively during
// server-startup reconciliation of pipelines that were running before a restart,
// pkg/pipeline/service.go, never by a StopPipeline RPC succeeding), so inferring
// "UserStopped" from a STATUS_STOPPED read-back right after this command's own
// stop call is correct.
//
// Mirrored (not shared via import) in
// cmd/conduit/internal/mcp/tools_lifecycle.go, since the mcp package cannot
// depend on this root command package — keep any change to one in sync with
// the other.
func pipelineStatusLabel(action string, status apiv1.Pipeline_Status) string {
	switch status {
	case apiv1.Pipeline_STATUS_RUNNING:
		return statusRunning
	case apiv1.Pipeline_STATUS_STOPPED:
		if action == actionStop {
			return statusUserStopped
		}
		return statusStopped
	case apiv1.Pipeline_STATUS_DEGRADED:
		return statusDegraded
	case apiv1.Pipeline_STATUS_RECOVERING:
		return statusRecovering
	case apiv1.Pipeline_STATUS_UNSPECIFIED:
		fallthrough
	default:
		return display.PrintStatusFromProtoString(status.String())
	}
}

// renderLifecycleResult renders the text (non-JSON) form of a LifecycleResult
// as a single human line naming the pipeline and its resulting status,
// through the same display.PrintStatusFromProtoString helper `inspect` uses
// (AC-5) — so the human-readable status vocabulary matches across CLI verbs,
// even though the --json Status field above uses the richer 5-value label.
func renderLifecycleResult(result any) string {
	res, ok := result.(*LifecycleResult)
	if !ok {
		return ""
	}

	buf := new(bytes.Buffer)
	fmt.Fprintf(buf, "Pipeline %s  [%s]\n", res.PipelineID, display.PrintStatusFromProtoString(res.protoStatus.String()))
	return buf.String()
}
