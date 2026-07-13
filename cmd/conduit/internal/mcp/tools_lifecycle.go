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

package mcp

import (
	"context"
	"fmt"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Lifecycle action names — mirrors
// cmd/conduit/root/pipelines.actionStart/actionStop.
const (
	actionStart = "start"
	actionStop  = "stop"
)

// The 5-value status vocabulary pkg/pipeline.Status exposes internally,
// rendered by pipelineStatusLabel — mirrors
// cmd/conduit/root/pipelines.statusRunning et al.
const (
	statusRunning     = "Running"
	statusStopped     = "Stopped"
	statusUserStopped = "UserStopped"
	statusDegraded    = "Degraded"
	statusRecovering  = "Recovering"
)

// LifecycleResult mirrors cmd/conduit/root/pipelines.LifecycleResult's
// fields exactly — the same shape the CLI start/stop commands return, so an
// agent and a human get identical result data for the identical RPC (design
// doc 20260712-cli-pipeline-lifecycle-verbs.md §2: "same verbs the CLI
// uses").
type LifecycleResult struct {
	PipelineID string `json:"pipeline_id"`
	Action     string `json:"action"` // "start" | "stop"
	Force      bool   `json:"force,omitempty"`
	Status     string `json:"status"`
}

// StartArgs is the start tool's input.
type StartArgs struct {
	PipelineID string `json:"pipelineId" jsonschema:"the ID of a pipeline registered in a running Conduit to start"`
}

// start implements the start (write) tool: transitions a stopped pipeline to
// Running via the live server's StartPipeline RPC — the same engine
// `conduit pipelines start` calls, through the same client seam `inspect`
// uses (inspectClient). Only registered when the server was started with
// --allow-mutations (design doc §4). Like inspect, it requires
// Config.APIAddress and has no offline/local-store fallback: starting a
// pipeline only has meaning against a process that stays up (design doc §3).
func (s *server) start(ctx context.Context, _ *sdkmcp.CallToolRequest, in StartArgs) (*sdkmcp.CallToolResult, Result[LifecycleResult], error) {
	if in.PipelineID == "" {
		ce := conduiterr.New(conduiterr.CodeInvalidArgument, "pipelineId is required")
		return toolErr[LifecycleResult](ce)
	}
	if ce := requireAPIAddress(s.cfg.APIAddress, ToolStart); ce != nil {
		return toolErr[LifecycleResult](ce)
	}

	client, err := s.cfg.newAPIClient(ctx, s.cfg.APIAddress)
	if err != nil {
		return toolErr[LifecycleResult](mapGRPCErr(err))
	}
	defer client.Close() // best-effort cleanup; nothing left to report to if this fails

	if err := client.StartPipeline(ctx, in.PipelineID); err != nil {
		return toolErr[LifecycleResult](mapGRPCErr(fmt.Errorf("failed to start pipeline %q: %w", in.PipelineID, err)))
	}

	result, err := readLifecycleStatus(ctx, client, in.PipelineID, actionStart, false)
	if err != nil {
		return toolErr[LifecycleResult](mapGRPCErr(err))
	}
	return toolOK(true, result, nil)
}

// StopArgs is the stop tool's input. Force mirrors the CLI's --force flag —
// see StopFlags' doc in cmd/conduit/root/pipelines/stop.go for why a forced
// stop is safe (not a data-loss escape hatch) despite skipping the graceful
// drain.
type StopArgs struct {
	PipelineID string `json:"pipelineId" jsonschema:"the ID of a pipeline registered in a running Conduit to stop"`
	Force      bool   `json:"force,omitempty" jsonschema:"skip graceful drain and stop immediately; may leave in-flight records un-drained (safe: positions are crash-safe and delivery is at-least-once, so this behaves like a crash, not data loss)"`
}

// stop implements the stop (write) tool: transitions a running pipeline to
// UserStopped via the live server's StopPipeline RPC — graceful drain by
// default, or immediate with force:true. Same engine, client seam, and
// --allow-mutations/--api-address gating as start.
func (s *server) stop(ctx context.Context, _ *sdkmcp.CallToolRequest, in StopArgs) (*sdkmcp.CallToolResult, Result[LifecycleResult], error) {
	if in.PipelineID == "" {
		ce := conduiterr.New(conduiterr.CodeInvalidArgument, "pipelineId is required")
		return toolErr[LifecycleResult](ce)
	}
	if ce := requireAPIAddress(s.cfg.APIAddress, ToolStop); ce != nil {
		return toolErr[LifecycleResult](ce)
	}

	client, err := s.cfg.newAPIClient(ctx, s.cfg.APIAddress)
	if err != nil {
		return toolErr[LifecycleResult](mapGRPCErr(err))
	}
	defer client.Close() // best-effort cleanup; nothing left to report to if this fails

	if err := client.StopPipeline(ctx, in.PipelineID, in.Force); err != nil {
		return toolErr[LifecycleResult](mapGRPCErr(fmt.Errorf("failed to stop pipeline %q: %w", in.PipelineID, err)))
	}

	result, err := readLifecycleStatus(ctx, client, in.PipelineID, actionStop, in.Force)
	if err != nil {
		return toolErr[LifecycleResult](mapGRPCErr(err))
	}
	return toolOK(true, result, nil)
}

// readLifecycleStatus reads a pipeline's status back via GetPipeline after a
// successful start/stop transition RPC and labels it with the 5-value status
// vocabulary pkg/pipeline.Status exposes internally — mirrors
// cmd/conduit/root/pipelines.readLifecycleResult exactly (see
// pipelineStatusLabel's doc below for why this is safe to call, not a race).
// A read-back failure is its own error, not a silently-swallowed one: the
// transition already took effect, so a false success here would tell the
// agent nothing happened when it did.
func readLifecycleStatus(ctx context.Context, client inspectClient, id, action string, force bool) (LifecycleResult, error) {
	pipeline, err := client.GetPipeline(ctx, id)
	if err != nil {
		return LifecycleResult{}, fmt.Errorf("%s succeeded but reading back pipeline %q status failed: %w", action, id, err)
	}
	return LifecycleResult{
		PipelineID: id,
		Action:     action,
		Force:      force,
		Status:     pipelineStatusLabel(action, pipeline.GetState().GetStatus()),
	}, nil
}

// pipelineStatusLabel mirrors
// cmd/conduit/root/pipelines.pipelineStatusLabel exactly — duplicated, not
// imported, because cmd/conduit/internal/mcp cannot depend on
// cmd/conduit/root/pipelines (root packages wire commands together and sit
// above the engine packages in the dependency graph; see doc.go). Keep any
// change to one in sync with the other; see that function's doc for the full
// rationale on why a "stop" action's collapsed STATUS_STOPPED read-back is
// unambiguously "UserStopped".
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
		return status.String()
	}
}
