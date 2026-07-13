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
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/internal/repair"
	json "github.com/goccy/go-json"
	"github.com/matryer/is"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const repairablePipelineYAML = `version: "2.2"
pipelines:
  - id: orders
    status: " Running "
    connectors:
      - id: src
        type: source
        plugin: builtin:generator
    processors:
      - id: proc1
        type: base64.encode
        workers: -2
`

// TestRepair_ReturnsClassifiedFixesAndHash is the repair (read) tool's core
// contract: it returns the proposed fixes, classified, with a hash, and
// mutates nothing (it is always registered, like deploy/validate/lint).
func TestRepair_ReturnsClassifiedFixesAndHash(t *testing.T) {
	is := is.New(t)

	srv := NewServer(Config{AllowMutations: false})
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolRepair,
		Arguments: map[string]any{"config": repairablePipelineYAML},
	})
	is.NoErr(err)
	is.True(!res.IsError)

	env := decodeStructuredContent[Result[repair.Plan]](t, res)
	is.True(env.OK)
	is.True(env.Result.Hash != "")
	is.True(len(env.Result.Fixes) >= 2) // rename + status; workers is restart-class but still proposed
}

// TestRepairApply_NotRegisteredWithoutAllowMutations is AC-16: repair_apply
// is absent from tool discovery (not merely erroring) unless the server was
// started with --allow-mutations — repair (read) is unaffected.
func TestRepairApply_NotRegisteredWithoutAllowMutations(t *testing.T) {
	is := is.New(t)

	srv := NewServer(Config{AllowMutations: false})
	cs := connectTestClient(t, srv)
	names := listToolNames(t, cs)

	is.True(!containsName(names, ToolRepairApply))
	is.True(containsName(names, ToolRepair))
}

// TestRepairApply_AppliesSafeFixesByDefault is AC-17 through the MCP
// transport: a matching hash applies the safe fixes only, and returns the
// repaired content directly (it never writes to any file — MCP is
// content-in/content-out only).
func TestRepairApply_AppliesSafeFixesByDefault(t *testing.T) {
	is := is.New(t)

	srv := NewServer(Config{AllowMutations: true})
	cs := connectTestClient(t, srv)

	readRes, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolRepair,
		Arguments: map[string]any{"config": repairablePipelineYAML},
	})
	is.NoErr(err)
	plan := decodeStructuredContent[Result[repair.Plan]](t, readRes).Result

	applyRes, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolRepairApply,
		Arguments: map[string]any{"config": repairablePipelineYAML, "hash": plan.Hash},
	})
	is.NoErr(err)
	is.True(!applyRes.IsError)

	env := decodeStructuredContent[Result[repair.Result]](t, applyRes)
	is.True(env.OK)
	is.True(env.Result.Content != repairablePipelineYAML) // something changed
	appliedAny := false
	for _, f := range env.Result.Fixes {
		if f.Outcome == repair.FixOutcomeApplied {
			appliedAny = true
		}
	}
	is.True(appliedAny)
}

// TestRepairApply_MissingHash_RejectedBeforeApplying mirrors
// TestApply_MissingHash_RejectedBeforeCallingApplyPlan: repair_apply
// refuses a call with no hash, before ever touching the plan.
func TestRepairApply_MissingHash_RejectedBeforeApplying(t *testing.T) {
	is := is.New(t)

	srv := NewServer(Config{AllowMutations: true})
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolRepairApply,
		Arguments: map[string]any{"config": repairablePipelineYAML},
	})
	is.NoErr(err)
	is.True(res.IsError)

	env := decodeStructuredContent[Result[repair.Result]](t, res)
	is.True(env.Error != nil)
	is.True(env.Error.Suggestion != "")
}

// TestRepairApply_StaleHash_Refused is AC-9 through the MCP transport: a
// hash that does not match the freshly recomputed plan is refused with
// repair.plan_stale.
func TestRepairApply_StaleHash_Refused(t *testing.T) {
	is := is.New(t)

	srv := NewServer(Config{AllowMutations: true})
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolRepairApply,
		Arguments: map[string]any{"config": repairablePipelineYAML, "hash": "not-the-real-hash"},
	})
	is.NoErr(err)
	is.True(res.IsError)

	env := decodeStructuredContent[Result[repair.Result]](t, res)
	is.True(env.Error != nil)
	is.Equal(env.Error.Code, repair.CodePlanStale.Reason())
}

// TestRepairApply_ToolSchema_HasNoEscalateField is AC-15's structural half:
// the repair_apply tool's input schema exposes no field an agent could set
// to force a data-path fix through — Escalate is reachable only via the
// CLI's --escalate flag (repair.ApplyInput.Escalate's doc), never from this
// tool's arguments.
func TestRepairApply_ToolSchema_HasNoEscalateField(t *testing.T) {
	is := is.New(t)

	srv := NewServer(Config{AllowMutations: true})
	cs := connectTestClient(t, srv)

	res, err := cs.ListTools(context.Background(), nil)
	is.NoErr(err)

	var found *sdkmcp.Tool
	for _, tool := range res.Tools {
		if tool.Name == ToolRepairApply {
			found = tool
		}
	}
	is.True(found != nil) // the tool was found

	// Round-trip the whole Tool through JSON and inspect the schema's
	// declared properties generically — InputSchema's concrete client-side
	// type is an SDK implementation detail; the JSON shape on the wire is
	// the actual contract an agent sees.
	b, err := json.Marshal(found)
	is.NoErr(err)
	var decoded struct {
		InputSchema struct {
			Properties map[string]any `json:"properties"`
		} `json:"inputSchema"`
	}
	is.NoErr(json.Unmarshal(b, &decoded))

	_, hasEscalate := decoded.InputSchema.Properties["escalate"]
	is.True(!hasEscalate)
	_, hasConfig := decoded.InputSchema.Properties["config"]
	is.True(hasConfig)
	_, hasHash := decoded.InputSchema.Properties["hash"]
	is.True(hasHash)
	_, hasSelect := decoded.InputSchema.Properties["select"]
	is.True(hasSelect)
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}
