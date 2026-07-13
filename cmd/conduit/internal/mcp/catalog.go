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

import "sort"

// ToolInfo describes one MCP tool this package can register: its stable
// name, human description, and whether it mutates local state. NewServer
// and the llms.txt generator (cmd/conduit/internal/llmsgen, design doc
// docs/design-documents/20260712-llms-txt-generation.md §D4) both read
// Catalog() instead of duplicating the tool list, so a tool can never exist
// in the running server but be missing from the generated docs, or
// vice-versa.
type ToolInfo struct {
	// Name is the stable tool identifier, equal to one of the Tool* consts.
	Name string
	// Description is the exact text passed to sdkmcp.Tool.Description at
	// registration.
	Description string
	// Mutates is true for tools registered only when Config.AllowMutations
	// is set (the write tools: apply, scaffold_connector,
	// scaffold_processor). false for the always-registered read tools.
	Mutates bool
}

// catalog is the single source of truth for every MCP tool this package can
// register. Keep it in Name order for readability; Catalog() re-sorts
// defensively so callers never depend on declaration order.
var catalog = []ToolInfo{
	{
		Name: ToolApply,
		Description: "Applies the plan computed by deploy, only if hash still matches the freshly " +
			"recomputed plan (a stale hash is refused, nothing mutated). Refuses to mutate a " +
			"currently-running pipeline. Same engine as `conduit pipelines apply`.",
		Mutates: true,
	},
	{
		Name: ToolDeploy,
		Description: "Computes the diff (create/update/delete per pipeline/connector/processor) needed to " +
			"deploy a pipeline config against its current stored state, and a plan hash to bind a later apply " +
			"call to. Mutates nothing. Same engine as `conduit pipelines deploy`.",
		Mutates: false,
	},
	{
		Name: ToolDoctor,
		Description: "Checks whether the local machine and configuration are ready for `conduit run` — offline, " +
			"non-destructive. Same engine as `conduit doctor`.",
		Mutates: false,
	},
	{
		Name: ToolDryRun,
		Description: "Everything validate checks, plus the fully-enriched pipeline graph `conduit run` would " +
			"load and a builtin-plugin existence check. Same engine as `conduit pipelines dry-run`.",
		Mutates: false,
	},
	{
		Name: ToolInspect,
		Description: "Reports a running pipeline's live status, per-stage connector summary, and DLQ. Requires " +
			"a running Conduit (conduit mcp --api-address). Same engine as `conduit pipelines inspect`.",
		Mutates: false,
	},
	{
		Name: ToolLint,
		Description: "Everything validate checks, plus the parser's advisory warnings (deprecated/renamed/" +
			"unknown fields, version fallback). Same engine as `conduit pipelines lint`.",
		Mutates: false,
	},
	{
		Name:        ToolScaffoldConnector,
		Description: "Scaffolds a new Go connector plugin repository (source and/or destination). Same engine as `conduit connector new`.",
		Mutates:     true,
	},
	{
		Name:        ToolScaffoldProcessor,
		Description: "Scaffolds a new Go (WASM) processor plugin repository. Same engine as `conduit processor new`.",
		Mutates:     true,
	},
	{
		Name: ToolStart,
		Description: "Starts a pipeline registered in a running Conduit (transitions to Running). " +
			"Requires --api-address, like inspect; no offline fallback. Refused if the pipeline is " +
			"already running (pipeline.running). Same engine as `conduit pipelines start`.",
		Mutates: true,
	},
	{
		Name: ToolStop,
		Description: "Stops a running pipeline registered in a running Conduit — graceful drain by " +
			"default, or immediate with force:true. Requires --api-address, like inspect; no offline " +
			"fallback. Refused if the pipeline isn't running (pipeline.not_running). Same engine as " +
			"`conduit pipelines stop`.",
		Mutates: true,
	},
	{
		Name: ToolValidate,
		Description: "Offline, static validation of a pipeline configuration (errors only, no side effects). " +
			"Same engine as `conduit pipelines validate`.",
		Mutates: false,
	},
}

// Catalog returns every MCP tool this package knows how to register, sorted
// by Name. It is the single source of truth NewServer registers from and
// the llms.txt generator reads from — see the ToolInfo doc.
func Catalog() []ToolInfo {
	out := make([]ToolInfo, len(catalog))
	copy(out, catalog)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
