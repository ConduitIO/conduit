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
	"io"
	"path/filepath"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal/deploy"
	"github.com/conduitio/conduit/cmd/conduit/internal/ui"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/provisioning"
	"github.com/conduitio/ecdysis"
)

var (
	_ cecdysis.CommandWithResult = (*ApplyCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*ApplyCommand)(nil)
	_ ecdysis.CommandWithFlags   = (*ApplyCommand)(nil)
	_ ecdysis.CommandWithArgs    = (*ApplyCommand)(nil)
	_ ecdysis.CommandWithConfig  = (*ApplyCommand)(nil)
)

// ApplyArgs holds ApplyCommand's positional argument.
type ApplyArgs struct {
	// Path is a single .yml/.yaml pipeline config file — see DeployArgs.Path.
	Path string
}

// ApplyFlags holds ApplyCommand's flags. See DeployFlags for why
// conduit.Config is embedded.
type ApplyFlags struct {
	conduit.Config

	PlanHash string `long:"plan-hash" usage:"the hash from 'conduit pipelines deploy' to apply; required unless --yes"`
	Yes      bool   `long:"yes" short:"y" usage:"apply the freshly recomputed plan directly, without requiring --plan-hash"`
	NoColor  bool   `long:"no-color" usage:"disable colored/glyph output even on a color-capable terminal"`
}

// ApplyCommand implements `conduit pipelines apply <file>`: recompute the
// Diff, require it to match the presented --plan-hash exactly (refusing with
// provisioning.plan_stale otherwise — see pkg/provisioning/plan.go), then
// execute it. It never runs a plan the caller did not, by hash, approve.
//
// Tier-1 safety (AC-13): apply refuses outright — rather than silently
// mutating — a pipeline it believes is currently running (see ApplyPlan's
// Invariant-7 guard); see deploy.NewLocalService's doc for exactly what
// "believes" can and cannot detect in this standalone CLI form.
type ApplyCommand struct {
	args  ApplyArgs
	flags ApplyFlags

	renderer   *ui.Renderer
	newService serviceFactory
}

func (c *ApplyCommand) Usage() string { return "apply <file>" }

func (c *ApplyCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Apply the changes needed to deploy a pipeline config file",
		Long: `Recomputes the plan for <file> and executes it, but only if --plan-hash matches the
freshly recomputed plan's hash exactly — a mismatch (the file or the live pipeline changed since
'conduit pipelines deploy' computed that hash) is refused with provisioning.plan_stale (exit 2),
never partially applied. Use --yes to skip presenting a hash and apply whatever the current plan
computes to instead.

Applying a change classified 'restart' requires the pipeline to already be stopped; apply refuses
outright rather than silently disrupt a running pipeline (invariant 7) — stop it first, then
re-run apply.

Idempotent: applying an already-applied config computes an empty plan and does nothing (exit 0).`,
		Example: "conduit pipelines apply orders.yaml --plan-hash 9f3a2c...\n" +
			"conduit pipelines apply orders.yaml --yes --json",
	}
}

func (c *ApplyCommand) Flags() []ecdysis.Flag {
	flags := ecdysis.BuildFlags(&c.flags)
	c.flags.Config = storeConfigDefaults(flags)
	return flags
}

func (c *ApplyCommand) Args(args []string) error {
	if len(args) == 0 {
		return cerrors.Errorf("requires a path to a pipeline config file")
	}
	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
	}
	c.args.Path = args[0]
	return nil
}

func (c *ApplyCommand) Config() ecdysis.Config {
	path := filepath.Dir(c.flags.ConduitCfg.Path)
	return ecdysis.Config{
		EnvPrefix:     "CONDUIT",
		Parsed:        &c.flags.Config,
		Path:          c.flags.ConduitCfg.Path,
		DefaultValues: conduit.DefaultConfigWithBasePath(path),
	}
}

func (c *ApplyCommand) ResultCommand() string { return "pipelines.apply" }

func (c *ApplyCommand) ExecuteWithResult(ctx context.Context) (cecdysis.Outcome, error) {
	c.renderer = ui.NewRenderer(rendererOutput(ctx), c.flags.NoColor)

	if c.flags.PlanHash == "" && !c.flags.Yes {
		ce := conduiterr.New(conduiterr.CodeInvalidArgument,
			"apply requires --plan-hash (from 'conduit pipelines deploy'), or --yes to apply the freshly recomputed plan directly")
		ce.Suggestion = "run 'conduit pipelines deploy " + c.args.Path + "' first, review the plan, then pass its hash"
		return cecdysis.Outcome{}, ce
	}

	desired, err := deploy.ParseSinglePipeline(ctx, c.args.Path)
	if err != nil {
		return cecdysis.Outcome{}, err // AC-12
	}

	newService := c.newService
	if newService == nil {
		newService = deploy.NewLocalService
	}
	svc, closeSvc, err := newService(ctx, c.flags.Config)
	if err != nil {
		return cecdysis.Outcome{}, err
	}
	defer closeSvc() //nolint:errcheck // best-effort cleanup; nothing left to report to if this fails

	hash := c.flags.PlanHash
	if hash == "" {
		// --yes with no --plan-hash: apply whatever the current plan is,
		// binding to its own freshly computed hash (still goes through the
		// same ApplyPlan hash check — there is no separate unchecked path).
		fresh, err := svc.Plan(ctx, desired)
		if err != nil {
			return cecdysis.Outcome{}, err
		}
		hash = fresh.Hash
	}

	diff, err := svc.ApplyPlan(ctx, desired, hash)
	if err != nil {
		return cecdysis.Outcome{}, err // AC-7 (stale, exit 2) / AC-13 (running, exit 2) surface here
	}

	return cecdysis.Outcome{
		OK:      true,
		Summary: deploy.Summarize(diff),
		Result:  deploy.Result{PipelineID: diff.PipelineID, Hash: diff.Hash, Changes: diff.Changes, Applied: true},
	}, nil
}

func (c *ApplyCommand) Render(outcome cecdysis.Outcome) string {
	result, _ := outcome.Result.(deploy.Result)
	diff := provisioning.Diff{PipelineID: result.PipelineID, Hash: result.Hash, Changes: result.Changes}

	if c.renderer == nil {
		c.renderer = ui.NewRenderer(io.Discard, true)
	}
	return deploy.Render(c.renderer, diff, result.Applied)
}
