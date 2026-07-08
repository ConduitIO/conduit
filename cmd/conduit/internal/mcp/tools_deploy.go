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

	"github.com/conduitio/conduit/cmd/conduit/internal/deploy"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// DeployArgs is the deploy tool's input: the pipeline configuration YAML
// CONTENT to plan against the pipeline's current stored state.
type DeployArgs struct {
	Config string `json:"config" jsonschema:"the pipeline configuration YAML content to plan (a single pipeline document)"`
}

// deploy implements the deploy (read) tool: parse+enrich+validate the
// content (the same offline engine validate/lint/dry_run use), compute the
// Diff against the pipeline's current stored state (Plan — no side effects),
// and return it with its hash. Mutates nothing; the hash binds a later apply
// call to exactly this plan (design doc §3, AC-4). Same engine as
// `conduit pipelines deploy`.
func (s *server) deploy(ctx context.Context, _ *sdkmcp.CallToolRequest, in DeployArgs) (*sdkmcp.CallToolResult, Result[deploy.Result], error) {
	desired, err := withTempConfigFile(in.Config, func(path string) (config.Pipeline, error) {
		return deploy.ParseSinglePipeline(ctx, path)
	})
	if err != nil {
		return toolErr[deploy.Result](err)
	}

	svc, closeSvc, err := s.cfg.newLocalService(ctx, s.cfg.StoreConfig)
	if err != nil {
		return toolErr[deploy.Result](err)
	}
	defer closeSvc() //nolint:errcheck // best-effort cleanup; nothing left to report to if this fails

	diff, err := svc.Plan(ctx, desired)
	if err != nil {
		return toolErr[deploy.Result](err)
	}

	result := deploy.Result{PipelineID: diff.PipelineID, Hash: diff.Hash, Changes: diff.Changes, Applied: false}
	return toolOK(true, result, deploy.Summarize(diff))
}

// ApplyArgs is the apply tool's input: the same pipeline configuration YAML
// content deploy was called with, plus the plan Hash from deploy's result.
// Hash is required — see apply's doc for why there is deliberately no
// --yes-equivalent "skip the hash" escape hatch for MCP (unlike the CLI's
// --yes flag): an agent must always go through deploy first.
type ApplyArgs struct {
	Config string `json:"config" jsonschema:"the pipeline configuration YAML content to apply (must match what deploy planned)"`
	// Hash is deliberately json:",omitempty" (schema-optional, not
	// SDK-required) even though apply always needs one: the handler's own
	// check below gives a more actionable conduiterr-mapped error
	// (code+suggestion, per AC-6) than the SDK's generic "missing required
	// property" schema-validation error, which carries no ErrorDetail at
	// all (see the design doc's structured-result contract, §4).
	Hash string `json:"hash,omitempty" jsonschema:"the plan hash from the deploy tool's result; apply refuses to run unless this matches the freshly recomputed plan hash exactly"`
}

// apply implements the apply (write) tool: recompute the Diff and execute it
// only if hash matches the freshly recomputed plan's hash exactly — a
// mismatch is refused with provisioning.plan_stale, nothing mutated (design
// doc AC-5). It inherits ApplyPlan's Invariant-7 guard (refuses to mutate a
// pipeline it believes is running) and deploy.NewLocalService's Badger-only
// structural gate (refuses to run at all against a store a live `conduit
// run` might have open) — so this tool cannot mutate a running pipeline,
// with no separate honor-system flag required (design doc §3). Only
// registered when the server was started with --allow-mutations. Same
// engine as `conduit pipelines apply`.
func (s *server) apply(ctx context.Context, _ *sdkmcp.CallToolRequest, in ApplyArgs) (*sdkmcp.CallToolResult, Result[deploy.Result], error) {
	if in.Hash == "" {
		ce := conduiterr.New(conduiterr.CodeInvalidArgument, "hash is required (from the deploy tool's result)")
		ce.Suggestion = "call the deploy tool first, review its diff, then pass its hash to apply"
		return toolErr[deploy.Result](ce)
	}

	desired, err := withTempConfigFile(in.Config, func(path string) (config.Pipeline, error) {
		return deploy.ParseSinglePipeline(ctx, path)
	})
	if err != nil {
		return toolErr[deploy.Result](err)
	}

	svc, closeSvc, err := s.cfg.newLocalService(ctx, s.cfg.StoreConfig)
	if err != nil {
		return toolErr[deploy.Result](err)
	}
	defer closeSvc() //nolint:errcheck // best-effort cleanup; nothing left to report to if this fails

	diff, err := svc.ApplyPlan(ctx, desired, in.Hash)
	if err != nil {
		// AC-5 (stale hash) / Invariant-7 (running pipeline) surface here,
		// via provisioning.CodePlanStale / provisioning.CodePipelineRunning
		// — no mutation happened in either case.
		return toolErr[deploy.Result](err)
	}

	result := deploy.Result{PipelineID: diff.PipelineID, Hash: diff.Hash, Changes: diff.Changes, Applied: true}
	return toolOK(true, result, deploy.Summarize(diff))
}
