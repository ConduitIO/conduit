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
	"github.com/conduitio/conduit/pkg/conduit"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool name constants — the stable identifiers NewServer registers each
// handler under. Exported so callers that need the catalog's names without
// connecting a client (e.g. a future llms.txt tool-catalog generator, design
// doc §6) have one source of truth instead of duplicating string literals.
const (
	ToolValidate          = "validate"
	ToolLint              = "lint"
	ToolDryRun            = "dry_run"
	ToolDoctor            = "doctor"
	ToolDeploy            = "deploy"
	ToolInspect           = "inspect"
	ToolApply             = "apply"
	ToolStart             = "start"
	ToolStop              = "stop"
	ToolScaffoldConnector = "scaffold_connector"
	ToolScaffoldProcessor = "scaffold_processor"
	ToolRepair            = "repair"
	ToolRepairApply       = "repair_apply"
)

// Config configures NewServer.
type Config struct {
	// AllowMutations gates registration of the write tools: apply,
	// scaffold_connector, scaffold_processor. This is an operator/process
	// -level setting — set once, at `conduit mcp` startup, via
	// --allow-mutations — never a tool argument an agent could pass to flip
	// it (design doc §3: "else the safety gate is theater"). When false, the
	// write tools are entirely absent from the catalog (design doc AC-2),
	// not merely present-but-erroring.
	AllowMutations bool

	// StoreConfig is the conduit.Config deploy/apply/doctor use to open the
	// local provisioning store / evaluate doctor's checks — the same
	// db.*/pipelines.path/api.*/... configuration `conduit pipelines
	// deploy`, `apply`, and `conduit doctor` read from flags/env/config
	// file.
	StoreConfig conduit.Config

	// APIAddress is the gRPC address of a running Conduit the inspect tool
	// dials (conduit mcp --api-address). Empty means inspect always returns
	// a structured common.unavailable error rather than guessing a default
	// address — an MCP server may run with no Conduit instance nearby.
	APIAddress string

	// newDeployService and newAPIClient construct the deploy/apply and
	// inspect engines respectively. Both default to the real
	// implementations in NewServer; overridable only from this package's
	// own tests (unexported — no production caller needs to inject a fake).
	//
	// newDeployService defaults to deploy.NewService (PR2 of #2588), not
	// deploy.NewLocalService directly: it prefers a live Conduit server
	// reachable at StoreConfig.API.GRPC.Address — so the apply tool can reach
	// a genuinely running pipeline via the server's ApplyPipeline RPC
	// (provisioning.Service.ApplyPlanLive) — and falls back to
	// NewLocalService's standalone Badger-only path when no server answers.
	// deploy/inspect/apply's --allow-mutations gate (AllowMutations, above)
	// is unchanged and orthogonal: it decides whether the apply tool is
	// registered at all, not which transport a registered apply tool uses.
	newDeployService func(ctx context.Context, cfg conduit.Config) (deploy.PlanApplier, func() error, error)
	newAPIClient     func(ctx context.Context, address string) (inspectClient, error)
}

// server holds the state every tool handler closes over. It is deliberately
// unexported: NewServer returns the *sdkmcp.Server the go-sdk wants, never
// this type — callers only ever interact with it through the MCP protocol
// (stdio or the streamable-HTTP handler), never by calling Go methods on it
// directly.
type server struct {
	cfg Config
}

// NewServer builds the `conduit mcp` tool catalog over cfg.
//
// Read tools — validate, lint, dry_run, doctor, deploy, inspect, repair —
// are always registered; none of them mutate anything (deploy computes a
// Diff/hash but never executes it, see deploy.go; repair computes a plan
// over CONTENT and returns proposed fixes, never touching a store or a
// running pipeline, see cmd/conduit/internal/repair's doc).
//
// Write tools — apply, start, stop, scaffold_connector, scaffold_processor,
// repair_apply — are registered only when cfg.AllowMutations is set (design
// doc §3, AC-2/AC-3; start/stop added by
// 20260712-cli-pipeline-lifecycle-verbs.md §4; repair_apply added by
// 20260712-repair-command.md §5.2 — even when registered, it never applies
// a data-path-adjacent fix; see tools_repair.go's doc).
//
// Every tool is a thin handler over the exact engine the matching CLI verb
// calls (cmd/conduit/internal/validate, pkg/conduit/check via doctorcheck,
// cmd/conduit/internal/deploy, cmd/conduit/internal/repair, pkg/scaffold,
// the API client) — see doc.go.
func NewServer(cfg Config) *sdkmcp.Server {
	if cfg.newDeployService == nil {
		cfg.newDeployService = deploy.NewService
	}
	if cfg.newAPIClient == nil {
		cfg.newAPIClient = newAPIInspectClient
	}

	s := &server{cfg: cfg}

	impl := &sdkmcp.Implementation{Name: "conduit", Version: conduit.Version(false)}
	srv := sdkmcp.NewServer(impl, &sdkmcp.ServerOptions{
		Instructions: "Conduit pipeline tools. validate/lint/dry_run/deploy check and preview a pipeline " +
			"configuration offline (content-in: pass the pipeline YAML as `config`, never a server file path). " +
			"doctor checks whether the local machine/configuration is ready for `conduit run`. inspect reads a " +
			"running pipeline's live status (requires the server to have been started with --api-address). " +
			"repair scans a configuration for machine-appliable fixes and returns them classified " +
			"(safe/restart/data_path) with a plan hash; repair_apply, when present, applies the safe ones " +
			"(or an explicit `select`) but NEVER a data_path fix — those require the human-only CLI --escalate " +
			"path. If present, apply/start/stop/scaffold_connector/scaffold_processor/repair_apply mutate the " +
			"local pipeline store, a running pipeline, or the filesystem — apply requires the hash from a prior " +
			"deploy call and is refused on a stale hash or a running pipeline; start/stop require --api-address " +
			"like inspect and are refused on an invalid transition (already running / not running); these tools " +
			"(plus repair_apply) only appear when " +
			"the operator started conduit mcp with --allow-mutations.",
	})

	sdkmcp.AddTool(srv, tool(ToolValidate), s.validate)
	sdkmcp.AddTool(srv, tool(ToolLint), s.lint)
	sdkmcp.AddTool(srv, tool(ToolDryRun), s.dryRun)
	sdkmcp.AddTool(srv, tool(ToolDoctor), s.doctor)
	sdkmcp.AddTool(srv, tool(ToolDeploy), s.deploy)
	sdkmcp.AddTool(srv, tool(ToolInspect), s.inspect)
	sdkmcp.AddTool(srv, tool(ToolRepair), s.repair)

	if cfg.AllowMutations {
		sdkmcp.AddTool(srv, tool(ToolApply), s.apply)
		sdkmcp.AddTool(srv, tool(ToolStart), s.start)
		sdkmcp.AddTool(srv, tool(ToolStop), s.stop)
		sdkmcp.AddTool(srv, tool(ToolScaffoldConnector), s.scaffoldConnector)
		sdkmcp.AddTool(srv, tool(ToolScaffoldProcessor), s.scaffoldProcessor)
		sdkmcp.AddTool(srv, tool(ToolRepairApply), s.repairApply)
	}

	return srv
}

// tool looks up name in Catalog() and returns the sdkmcp.Tool literal
// AddTool registers it under. Catalog is the single source of truth for
// name + description (D4 of the llms.txt generation design doc): a tool's
// description is written once, here indirectly via catalog.go, and both
// NewServer and the llms.txt generator read it from the same place. tool
// panics if name isn't in the catalog — a programming error (a Tool* const
// used here without a matching catalog.go entry), not a runtime condition,
// so failing loudly at server construction is correct.
func tool(name string) *sdkmcp.Tool {
	for _, info := range catalog {
		if info.Name == name {
			return &sdkmcp.Tool{Name: info.Name, Description: info.Description}
		}
	}
	panic("mcp: tool " + name + " has no Catalog() entry — add one in catalog.go")
}
