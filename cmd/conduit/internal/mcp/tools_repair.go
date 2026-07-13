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

	"github.com/conduitio/conduit/cmd/conduit/internal/repair"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// RepairArgs is the repair (read) tool's input: the pipeline configuration
// YAML CONTENT to scan for machine-appliable fixes.
type RepairArgs struct {
	Config string `json:"config" jsonschema:"the pipeline configuration YAML content to scan for machine-appliable fixes (a single pipeline document)"`
}

// repair implements the repair (read) tool: run the same
// cmd/conduit/internal/repair.CollectContent engine `conduit pipelines
// repair` uses over the content, and return the proposed fixes, their
// safety classification, and a plan hash. Mutates nothing — parity with
// the always-on validate/lint/dry_run/deploy tools. Same engine as
// `conduit pipelines repair`.
func (s *server) repair(ctx context.Context, _ *sdkmcp.CallToolRequest, in RepairArgs) (*sdkmcp.CallToolResult, Result[repair.Plan], error) {
	plan, err := repair.CollectContent(ctx, in.Config)
	if err != nil {
		return toolErr[repair.Plan](err)
	}
	return toolOK(true, plan, repair.SummarizePlan(plan))
}

// RepairApplyArgs is the repair_apply tool's input: the same content the
// repair tool was called with, the plan Hash from repair's result, and an
// optional Select of specific configPaths to apply.
//
// There is deliberately NO Escalate field here (design doc §5.2, AC-15):
// repair.ApplyInput.Escalate — the human-only Tier-1 override that permits
// applying a FixClassDataPath fix — is reachable only from the CLI's
// --escalate flag (cmd/conduit/root/pipelines/repair.go). An agent that
// explicitly Selects a data-path-adjacent fix's configPath still gets it
// back as FixOutcomeSkipped with repair.CodeDataPathFixRefused in the
// result's Fixes report — repair_apply NEVER applies one, and there is no
// schema field an agent could set to force it (mirrors AllowMutations
// being process-set only, server.go's Config doc).
type RepairApplyArgs struct {
	Config string `json:"config" jsonschema:"the pipeline configuration YAML content to repair (must match what the repair tool scanned)"`
	// Hash is schema-optional (like ApplyArgs.Hash) so the handler's own
	// check below gives an actionable conduiterr-mapped error rather than
	// the SDK's generic schema-validation error.
	Hash   string   `json:"hash,omitempty" jsonschema:"the plan hash from the repair tool's result; repair_apply refuses to run unless this matches the freshly recomputed plan hash exactly"`
	Select []string `json:"select,omitempty" jsonschema:"apply only the fix(es) at these configPaths; omit to apply every safe fix (the default)"`
}

// repairApply implements the repair_apply (write) tool: recompute the plan
// from Config and apply it only if Hash matches the freshly recomputed
// plan's hash exactly (design doc §5.2, mirroring the apply tool's own
// stance — no "skip the hash" escape hatch for MCP). Applies FixClassSafe
// fixes by default (or the Select-ed subset); a FixClassDataPath fix is
// never applied — see RepairApplyArgs' doc. Only registered when the
// server was started with --allow-mutations. Same engine as `conduit
// pipelines repair --apply`.
func (s *server) repairApply(ctx context.Context, _ *sdkmcp.CallToolRequest, in RepairApplyArgs) (*sdkmcp.CallToolResult, Result[repair.Result], error) {
	if in.Hash == "" {
		ce := conduiterr.New(conduiterr.CodeInvalidArgument, "hash is required (from the repair tool's result)")
		ce.Suggestion = "call the repair tool first, review its proposed fixes, then pass its hash to repair_apply"
		return toolErr[repair.Result](ce)
	}

	res, err := repair.Apply(ctx, repair.ApplyInput{
		Content: in.Config,
		Hash:    in.Hash,
		Select:  in.Select,
		// Escalate is always false here — see RepairApplyArgs' doc.
	})
	if err != nil {
		return toolErr[repair.Result](err)
	}
	return toolOK(true, res, repair.SummarizeResult(res))
}
