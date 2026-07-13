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
// Read tools — validate, lint, dry_run, doctor, deploy, inspect — are always
// registered; none of them mutate anything (deploy computes a Diff/hash but
// never executes it; see deploy.go).
//
// Write tools — apply, start, stop, scaffold_connector, scaffold_processor —
// are registered only when cfg.AllowMutations is set (design doc §3,
// AC-2/AC-3; start/stop added by 20260712-cli-pipeline-lifecycle-verbs.md
// §4).
//
// Every tool is a thin handler over the exact engine the matching CLI verb
// calls (cmd/conduit/internal/validate, pkg/conduit/check via doctorcheck,
// cmd/conduit/internal/deploy, pkg/scaffold, the API client) — see doc.go.
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
			"running pipeline's live status (requires the server to have been started with --api-address). If " +
			"present, apply/start/stop/scaffold_connector/scaffold_processor mutate the local pipeline store, a " +
			"running pipeline, or the filesystem — apply requires the hash from a prior deploy call and is " +
			"refused on a stale hash or a running pipeline; start/stop require --api-address like inspect and " +
			"are refused on an invalid transition (already running / not running); these tools only appear when " +
			"the operator started conduit mcp with --allow-mutations.",
	})

	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name: ToolValidate,
		Description: "Offline, static validation of a pipeline configuration (errors only, no side effects). " +
			"Same engine as `conduit pipelines validate`.",
	}, s.validate)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name: ToolLint,
		Description: "Everything validate checks, plus the parser's advisory warnings (deprecated/renamed/" +
			"unknown fields, version fallback). Same engine as `conduit pipelines lint`.",
	}, s.lint)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name: ToolDryRun,
		Description: "Everything validate checks, plus the fully-enriched pipeline graph `conduit run` would " +
			"load and a builtin-plugin existence check. Same engine as `conduit pipelines dry-run`.",
	}, s.dryRun)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name: ToolDoctor,
		Description: "Checks whether the local machine and configuration are ready for `conduit run` — offline, " +
			"non-destructive. Same engine as `conduit doctor`.",
	}, s.doctor)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name: ToolDeploy,
		Description: "Computes the diff (create/update/delete per pipeline/connector/processor) needed to " +
			"deploy a pipeline config against its current stored state, and a plan hash to bind a later apply " +
			"call to. Mutates nothing. Same engine as `conduit pipelines deploy`.",
	}, s.deploy)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name: ToolInspect,
		Description: "Reports a running pipeline's live status, per-stage connector summary, and DLQ. Requires " +
			"a running Conduit (conduit mcp --api-address). Same engine as `conduit pipelines inspect`.",
	}, s.inspect)

	if cfg.AllowMutations {
		sdkmcp.AddTool(srv, &sdkmcp.Tool{
			Name: ToolApply,
			Description: "Applies the plan computed by deploy, only if hash still matches the freshly " +
				"recomputed plan (a stale hash is refused, nothing mutated). Refuses to mutate a currently " +
				"-running pipeline. Same engine as `conduit pipelines apply`.",
		}, s.apply)
		sdkmcp.AddTool(srv, &sdkmcp.Tool{
			Name: ToolStart,
			Description: "Starts a pipeline registered in a running Conduit (transitions to Running). " +
				"Requires --api-address, like inspect; no offline fallback. Refused if the pipeline is " +
				"already running (pipeline.running). Same engine as `conduit pipelines start`.",
		}, s.start)
		sdkmcp.AddTool(srv, &sdkmcp.Tool{
			Name: ToolStop,
			Description: "Stops a running pipeline registered in a running Conduit — graceful drain by " +
				"default, or immediate with force:true. Requires --api-address, like inspect; no offline " +
				"fallback. Refused if the pipeline isn't running (pipeline.not_running). Same engine as " +
				"`conduit pipelines stop`.",
		}, s.stop)
		sdkmcp.AddTool(srv, &sdkmcp.Tool{
			Name:        ToolScaffoldConnector,
			Description: "Scaffolds a new Go connector plugin repository (source and/or destination). Same engine as `conduit connector new`.",
		}, s.scaffoldConnector)
		sdkmcp.AddTool(srv, &sdkmcp.Tool{
			Name:        ToolScaffoldProcessor,
			Description: "Scaffolds a new Go (WASM) processor plugin repository. Same engine as `conduit processor new`.",
		}, s.scaffoldProcessor)
	}

	return srv
}
