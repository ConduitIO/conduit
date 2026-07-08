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
	"fmt"
	"io"
	"strings"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal/ui"
	"github.com/conduitio/conduit/cmd/conduit/internal/validate"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/ecdysis"
)

var (
	_ cecdysis.CommandWithResult = (*DryRunCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*DryRunCommand)(nil)
	_ ecdysis.CommandWithFlags   = (*DryRunCommand)(nil)
	_ ecdysis.CommandWithArgs    = (*DryRunCommand)(nil)
)

// DryRunArgs holds DryRunCommand's positional argument.
type DryRunArgs struct {
	// Path is a single .yml/.yaml pipeline config file, or a directory of
	// them (not recursed).
	Path string
}

// DryRunFlags holds DryRunCommand's flags. There is deliberately no
// --strict flag here (per the design doc's flag table): dry-run's only
// escalation-worthy problem is an unknown builtin plugin, which is already
// always an error (never advisory) — --resolve-plugins toggles whether that
// check runs at all, not its severity.
type DryRunFlags struct {
	ResolvePlugins bool `long:"resolve-plugins" usage:"resolve connector/processor plugin refs against the builtin registry (default on); unknown builtin refs fail, standalone/unprefixed refs are not statically verifiable and are never flagged"`
	Quiet          bool `long:"quiet" short:"q" usage:"suppress passing/OK lines and progress chrome; print only failures and the summary"`
	NoColor        bool `long:"no-color" usage:"disable colored/glyph output even on a color-capable terminal"`
}

// DryRunCommand implements `conduit pipeline dry-run <path>`: everything
// `pipelines validate` checks, plus a report of the enriched pipeline graph
// (final connector/processor IDs, injected DLQ defaults, worker counts) and,
// by default, resolution of every "builtin:" plugin ref against the
// compiled-in builtin registry. See
// docs/design-documents/20260708-cli-pipeline-inspect-lint-dryrun.md and
// cmd/conduit/internal/validate.RunDryRun for the shared engine this is a
// thin cobra shell over.
type DryRunCommand struct {
	args  DryRunArgs
	flags DryRunFlags

	// renderer is constructed in ExecuteWithResult (where the real output
	// stream and --no-color are available via the cobra command in ctx) and
	// used by Render, which cecdysis.CommandWithResultDecorator calls
	// afterwards on the same command value with no ctx of its own.
	renderer *ui.Renderer
}

func (c *DryRunCommand) Usage() string { return "dry-run <path>" }

func (c *DryRunCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Preview the enriched pipeline graph a config file would provision",
		Long: `Runs the same offline checks as 'conduit pipelines validate' (parse, enrich,
validate — no server, no API dial), then reports the enriched pipeline graph: final
connector/processor IDs (pipelineID:connectorID), injected dead-letter-queue defaults,
and worker counts — exactly what 'conduit run' would provision, without provisioning it.

With --resolve-plugins (default on), every connector/processor plugin ref prefixed
"builtin:" is checked against the compiled-in builtin registry; an unknown one is an
error (exit 2). A "standalone:" or unprefixed plugin ref cannot be statically verified
(it may resolve at runtime against a plugin this command never loads) and is reported as
"unverified", never as a false failure.

<path> is a single .yml/.yaml file, or a directory of them (not recursed into
subdirectories).

See also: 'conduit pipelines validate' (errors only) and 'conduit pipelines lint'
(validate + advisory warnings, with --strict).`,
		Example: "conduit pipelines dry-run pipelines/orders.yaml\n" +
			"conduit pipelines dry-run ./pipelines --resolve-plugins=false\n" +
			"conduit pipeline dry-run ./pipelines --json",
	}
}

func (c *DryRunCommand) Flags() []ecdysis.Flag {
	flags := ecdysis.BuildFlags(&c.flags)
	// ecdysis.BuildFlags has no struct-tag support for a non-zero default,
	// so --resolve-plugins's "default on" (per the design doc's flag table)
	// is set explicitly here rather than relying on the bool field's zero
	// value (false).
	flags.SetDefault("resolve-plugins", true)
	return flags
}

func (c *DryRunCommand) Args(args []string) error {
	if len(args) == 0 {
		return cerrors.Errorf("requires a path to a pipeline config file or directory")
	}
	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
	}
	c.args.Path = args[0]
	return nil
}

func (c *DryRunCommand) ResultCommand() string { return "pipelines.dry_run" }

// ExecuteWithResult runs the offline dry-run engine and translates its
// DryRunReport into the shared cecdysis.Outcome envelope. A non-nil error
// return here is reserved for a HARD failure — path itself couldn't be
// resolved at all (e.g. it doesn't exist) — never for findings within files
// that did resolve; see validate.RunDryRun's doc.
func (c *DryRunCommand) ExecuteWithResult(ctx context.Context) (cecdysis.Outcome, error) {
	c.renderer = ui.NewRenderer(rendererOutput(ctx), c.flags.NoColor)

	report, err := validate.RunDryRun(ctx, c.args.Path, c.flags.ResolvePlugins)
	if err != nil {
		return cecdysis.Outcome{}, conduiterr.Wrap(conduiterr.CodeInvalidArgument,
			fmt.Sprintf("could not resolve path %q: %v", c.args.Path, err), err)
	}

	outcome := cecdysis.Outcome{
		OK:      report.OK(),
		Summary: report.Summary,
		Result:  validate.DryRunResult{Files: report.Files, Enriched: report.Enriched},
	}
	if !report.OK() {
		// See CLI output conventions §4 and cecdysis.Outcome.ExitErr's doc:
		// a dry-run that finds problems is still OK:false/error:null in the
		// rendered envelope — ExitErr is a side channel purely for the
		// process exit code, not part of what gets rendered. dry-run has no
		// --strict, so this is exactly validate's ExitError (errors only —
		// an unverified standalone/unprefixed plugin ref is never a
		// Finding, see plugins.go).
		if exitErr, ok := validate.ExitError(report.Files); ok {
			outcome.ExitErr = exitErr
		}
	}
	return outcome, nil
}

// Render returns the human-readable rendering of a dry-run: one line per
// file (✓/✗), every finding under a failing file, then the enriched graph
// for every pipeline in every file, and a summary line.
func (c *DryRunCommand) Render(outcome cecdysis.Outcome) string {
	summary, _ := outcome.Summary.(validate.Summary)
	result, _ := outcome.Result.(validate.DryRunResult)

	if c.renderer == nil {
		// Defensive only — see ValidateCommand.Render's identical comment:
		// unreached via the real command pipeline, only for a test that
		// calls Render directly.
		c.renderer = ui.NewRenderer(io.Discard, true)
	}

	enrichedByPath := make(map[string][]validate.EnrichedPipeline, len(result.Enriched))
	for _, ef := range result.Enriched {
		enrichedByPath[ef.Path] = ef.Pipelines
	}

	var b strings.Builder
	passed := 0
	for _, f := range result.Files {
		if f.OK {
			passed++
		}

		if f.OK {
			if !c.flags.Quiet {
				fmt.Fprintf(&b, "%s %s\n", c.renderer.Glyph(ui.StatusPass), f.Path)
			}
		} else {
			fmt.Fprintf(&b, "%s %s\n", c.renderer.Glyph(ui.StatusFail), f.Path)
			for _, finding := range f.Findings {
				ui.RenderFinding(&b, c.renderer.Glyph(ui.StatusFail), finding.Code, finding.ConfigPath, finding.Message, finding.Suggestion)
			}
		}

		for _, p := range enrichedByPath[f.Path] {
			renderEnrichedPipeline(&b, p)
		}
		fmt.Fprintln(&b)
	}

	fmt.Fprintf(&b, "Summary: %d files · %d passed · %d failed · %d problems\n",
		summary.Files, passed, summary.Files-passed, summary.Errors)

	if summary.Errors > 0 {
		fmt.Fprintln(&b, "Fix the ✗ items above, then re-run.")
	}

	return b.String()
}

// renderEnrichedPipeline writes one pipeline's enriched view: its final ID,
// each connector (with plugin status, when checked) and its nested
// processors, top-level processors, and the DLQ defaults.
func renderEnrichedPipeline(b *strings.Builder, p validate.EnrichedPipeline) {
	fmt.Fprintf(b, "  pipeline %s\n", p.ID)
	for _, c := range p.Connectors {
		fmt.Fprintf(b, "    connector %-12s %-10s %s%s\n", c.ID, c.Type, c.Plugin, pluginStatusSuffix(c.PluginStatus))
		for _, proc := range c.Processors {
			fmt.Fprintf(b, "      processor %-12s %s (workers: %d)%s\n", proc.ID, proc.Plugin, proc.Workers, pluginStatusSuffix(proc.PluginStatus))
		}
	}
	for _, proc := range p.Processors {
		fmt.Fprintf(b, "    processor %-12s %s (workers: %d)%s\n", proc.ID, proc.Plugin, proc.Workers, pluginStatusSuffix(proc.PluginStatus))
	}
	fmt.Fprintf(b, "    dlq: plugin=%s window-size=%d window-nack-threshold=%d\n", p.DLQ.Plugin, p.DLQ.WindowSize, p.DLQ.WindowNackThreshold)
}

// pluginStatusSuffix renders a trailing " (unverified)"/" (not found)"
// annotation for a checked plugin ref, or nothing at all when status is
// empty (--resolve-plugins was off, or this ref was never checked) or
// "builtin" (the unremarkable, expected case).
func pluginStatusSuffix(status validate.PluginStatus) string {
	switch status {
	case validate.PluginStatusUnverified:
		return " (unverified)"
	case validate.PluginStatusNotFound:
		return " (not found)"
	case validate.PluginStatusBuiltin:
		return ""
	default:
		return ""
	}
}
