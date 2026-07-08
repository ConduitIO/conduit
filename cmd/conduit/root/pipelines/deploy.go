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
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal/deploy"
	"github.com/conduitio/conduit/cmd/conduit/internal/ui"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/provisioning"
	"github.com/conduitio/ecdysis"
)

var (
	_ cecdysis.CommandWithResult = (*DeployCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*DeployCommand)(nil)
	_ ecdysis.CommandWithFlags   = (*DeployCommand)(nil)
	_ ecdysis.CommandWithArgs    = (*DeployCommand)(nil)
	_ ecdysis.CommandWithConfig  = (*DeployCommand)(nil)
)

// serviceFactory constructs the deploy.PlanApplier a deploy/apply command run
// uses. It is a package-level type (not a method-local closure) so both
// DeployCommand and ApplyCommand share one seam, and so tests for either can
// inject a fake without a database or plugin runtime — see deploy_test.go /
// apply_test.go.
type serviceFactory func(ctx context.Context, cfg conduit.Config) (deploy.PlanApplier, func() error, error)

// DeployArgs holds DeployCommand's positional argument.
type DeployArgs struct {
	// Path is a single .yml/.yaml pipeline config file (not a directory —
	// deploy/apply operate on exactly one pipeline; see deploy.ParseSinglePipeline).
	Path string
}

// DeployFlags holds DeployCommand's flags. conduit.Config is embedded so
// deploy points at the same store/pipelines-path configuration `conduit run`
// would use (db.*, pipelines.path, ...) via the same flags/env/config file —
// see deploy.NewLocalService's doc for what "the same store" means here.
type DeployFlags struct {
	conduit.Config

	Apply   bool `long:"apply" usage:"compute the plan and apply it in one call (prompts for confirmation unless --yes)"`
	Yes     bool `long:"yes" short:"y" usage:"with --apply, skip the confirmation prompt"`
	Quiet   bool `long:"quiet" short:"q" usage:"suppress the per-change lines; print only the summary"`
	NoColor bool `long:"no-color" usage:"disable colored/glyph output even on a color-capable terminal"`
}

// DeployCommand implements `conduit pipelines deploy <file>`: parse+enrich+
// validate the file (reusing the same offline engine `pipelines validate`
// uses), compute the Diff against the pipeline's current state (Plan, no
// side effects), and render it — emitting the plan hash `apply --plan-hash`
// binds to. See docs/design-documents/20260708-cli-pipeline-deploy-apply.md.
type DeployCommand struct {
	args  DeployArgs
	flags DeployFlags

	renderer *ui.Renderer
	// newService is deploy.NewLocalService by default; tests override it
	// with a fake PlanApplier.
	newService serviceFactory
}

func (c *DeployCommand) Usage() string { return "deploy <file>" }

func (c *DeployCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Preview (and optionally apply) the changes needed to deploy a pipeline config file",
		Long: `Computes the diff between <file> and the pipeline's current state — create/update/delete
per pipeline/connector/processor, each classified in-place (safe on a running pipeline) or restart
(requires stopping it) — without applying anything. Emits a plan hash: run
'conduit pipelines apply <file> --plan-hash <hash>' to execute exactly this plan, or pass --apply to
compute and apply it in one call.

deploy never mutates state on its own — only --apply (or the 'apply' command) does.`,
		Example: "conduit pipelines deploy orders.yaml\n" +
			"conduit pipelines deploy orders.yaml --json\n" +
			"conduit pipelines deploy orders.yaml --apply --yes",
	}
}

func (c *DeployCommand) Flags() []ecdysis.Flag {
	flags := ecdysis.BuildFlags(&c.flags)
	c.flags.Config = storeConfigDefaults(flags)
	return flags
}

func (c *DeployCommand) Args(args []string) error {
	if len(args) == 0 {
		return cerrors.Errorf("requires a path to a pipeline config file")
	}
	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
	}
	c.args.Path = args[0]
	return nil
}

// Config wires DeployFlags.Config (embedded conduit.Config) the same way
// RunCommand does — deploy/apply read the same conduit.yaml / CONDUIT_* env
// / flags as `conduit run` for db.*/pipelines.path, so pointing them at "the
// same store" is just pointing them at the same config.
func (c *DeployCommand) Config() ecdysis.Config {
	path := filepath.Dir(c.flags.ConduitCfg.Path)
	return ecdysis.Config{
		EnvPrefix:     "CONDUIT",
		Parsed:        &c.flags.Config,
		Path:          c.flags.ConduitCfg.Path,
		DefaultValues: conduit.DefaultConfigWithBasePath(path),
	}
}

func (c *DeployCommand) ResultCommand() string { return "pipelines.deploy" }

func (c *DeployCommand) ExecuteWithResult(ctx context.Context) (cecdysis.Outcome, error) {
	c.renderer = ui.NewRenderer(rendererOutput(ctx), c.flags.NoColor)

	desired, err := deploy.ParseSinglePipeline(ctx, c.args.Path)
	if err != nil {
		return cecdysis.Outcome{}, err // AC-12: coded validation failure, before any Plan call
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

	diff, err := svc.Plan(ctx, desired)
	if err != nil {
		return cecdysis.Outcome{}, err
	}

	applied := false
	if c.flags.Apply && !diff.Empty() {
		if !c.flags.Yes {
			ok, cerr := confirm(ctx, fmt.Sprintf("Apply %d change(s) to pipeline %q? [y/N] ", len(diff.Changes), diff.PipelineID))
			if cerr != nil {
				return cecdysis.Outcome{}, cerr
			}
			if !ok {
				return cecdysis.Outcome{}, cerrors.Errorf("apply declined")
			}
		}
		diff, err = svc.ApplyPlan(ctx, desired, diff.Hash)
		if err != nil {
			return cecdysis.Outcome{}, err
		}
		applied = true
	}

	return cecdysis.Outcome{
		OK:      true,
		Summary: deploy.Summarize(diff),
		Result:  deploy.Result{PipelineID: diff.PipelineID, Hash: diff.Hash, Changes: diff.Changes, Applied: applied},
	}, nil
}

func (c *DeployCommand) Render(outcome cecdysis.Outcome) string {
	result, _ := outcome.Result.(deploy.Result)
	diff := provisioning.Diff{PipelineID: result.PipelineID, Hash: result.Hash, Changes: result.Changes}

	if c.renderer == nil {
		c.renderer = ui.NewRenderer(io.Discard, true)
	}
	return deploy.Render(c.renderer, diff, result.Applied)
}

// confirm prints prompt to the running cobra command's output stream and
// reads a line from its input stream, reporting whether the answer was
// affirmative ("y"/"yes", case-insensitive; anything else, including empty
// input/EOF, is a decline — the safe default for a destructive confirmation).
func confirm(ctx context.Context, prompt string) (bool, error) {
	cmd := ecdysis.CobraCmdFromContext(ctx)
	if cmd == nil {
		return false, cerrors.Errorf("cannot prompt for confirmation outside an interactive command run")
	}
	fmt.Fprint(cmd.OutOrStdout(), prompt)

	reader := bufio.NewReader(cmd.InOrStdin())
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, cerrors.Errorf("could not read confirmation: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

// storeConfigDefaults registers pflag defaults for the subset of
// conduit.Config that deploy/apply's standalone service construction
// (deploy.NewLocalService -> conduit.NewRuntime) actually needs — db.*,
// pipelines.path, connectors/processors.path, log.*, schema-registry.type,
// preview.* — and returns the conduit.Config those defaults describe.
//
// This mirrors RunCommand.Flags (cmd/conduit/root/run/run.go) deliberately,
// not by accident: without explicit flags.SetDefault calls here, an unset
// flag's own pflag zero-value (e.g. Log.Level == "") wins over
// ecdysis.Config's DefaultValues layer, so fields conduit.NewRuntime
// dereferences unconditionally (e.g. Log.NewLogger(level, format) parsing an
// empty level) would panic instead of falling back to
// conduit.DefaultConfigWithBasePath's real defaults. Both DeployCommand and
// ApplyCommand call this from Flags() so they can't drift from one another
// or from run's already-correct behavior.
func storeConfigDefaults(flags ecdysis.Flags) conduit.Config {
	currentPath, err := os.Getwd()
	if err != nil {
		panic(cerrors.Errorf("failed to get current working directory: %w", err))
	}
	cfg := conduit.DefaultConfigWithBasePath(currentPath)

	flags.SetDefault("config.path", cfg.ConduitCfg.Path)
	flags.SetDefault("db.type", cfg.DB.Type)
	flags.SetDefault("db.badger.path", cfg.DB.Badger.Path)
	flags.SetDefault("db.postgres.connection-string", cfg.DB.Postgres.ConnectionString)
	flags.SetDefault("db.postgres.table", cfg.DB.Postgres.Table)
	flags.SetDefault("db.sqlite.path", cfg.DB.SQLite.Path)
	flags.SetDefault("db.sqlite.table", cfg.DB.SQLite.Table)
	flags.SetDefault("log.level", cfg.Log.Level)
	flags.SetDefault("log.format", cfg.Log.Format)
	flags.SetDefault("connectors.path", cfg.Connectors.Path)
	flags.SetDefault("connectors.max-receive-record-size", cfg.Connectors.MaxReceiveRecordSize)
	flags.SetDefault("processors.path", cfg.Processors.Path)
	flags.SetDefault("pipelines.path", cfg.Pipelines.Path)
	flags.SetDefault("pipelines.exit-on-degraded", cfg.Pipelines.ExitOnDegraded)
	flags.SetDefault("pipelines.error-recovery.min-delay", cfg.Pipelines.ErrorRecovery.MinDelay)
	flags.SetDefault("pipelines.error-recovery.max-delay", cfg.Pipelines.ErrorRecovery.MaxDelay)
	flags.SetDefault("pipelines.error-recovery.backoff-factor", cfg.Pipelines.ErrorRecovery.BackoffFactor)
	flags.SetDefault("pipelines.error-recovery.max-retries", cfg.Pipelines.ErrorRecovery.MaxRetries)
	flags.SetDefault("pipelines.error-recovery.max-retries-window", cfg.Pipelines.ErrorRecovery.MaxRetriesWindow)
	flags.SetDefault("schema-registry.type", cfg.SchemaRegistry.Type)
	flags.SetDefault("schema-registry.confluent.connection-string", cfg.SchemaRegistry.Confluent.ConnectionString)
	flags.SetDefault("preview.pipeline-arch-v2", cfg.Preview.PipelineArchV2)
	flags.SetDefault("preview.pipeline-arch-v2-disable-metrics", cfg.Preview.PipelineArchV2DisableMetrics)

	return cfg
}
