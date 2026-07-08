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
	"path"
	"strings"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal/ui"
	"github.com/conduitio/conduit/cmd/conduit/internal/validate"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	procbuiltin "github.com/conduitio/conduit/pkg/plugin/processor/builtin"
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
	Path string
}

// DryRunFlags holds DryRunCommand's flags.
type DryRunFlags struct {
	ResolvePlugins bool `long:"resolve-plugins" usage:"check that referenced builtin plugins exist (standalone plugins stay advisory)"`
	Quiet          bool `long:"quiet" short:"q" usage:"suppress passing/OK lines and progress chrome; print only problems and the summary"`
	NoColor        bool `long:"no-color" usage:"disable colored/glyph output even on a color-capable terminal"`
}

// DryRunCommand implements `conduit pipelines dry-run <path>`: everything
// validate checks, then reports the fully-enriched pipeline graph (final
// connector/processor IDs, injected DLQ defaults, worker counts) that 'conduit
// run' would actually load, and (with --resolve-plugins, default on) checks
// that referenced builtin plugins exist. Offline, no side effects. See
// docs/design-documents/20260708-cli-pipeline-inspect-lint-dryrun.md.
type DryRunCommand struct {
	args  DryRunArgs
	flags DryRunFlags

	renderer *ui.Renderer
}

func (c *DryRunCommand) Usage() string { return "dry-run <path>" }

func (c *DryRunCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Show the enriched pipeline graph 'conduit run' would load, without running it",
		Long: `Runs everything 'conduit pipelines validate' checks, then reports the fully-enriched
pipeline graph that 'conduit run' would actually load: final connector and processor IDs, injected
dead-letter-queue defaults, and worker counts. With --resolve-plugins (default on) it also checks
that every referenced builtin plugin exists — an unknown builtin fails (exit 2). Standalone plugin
references are left advisory ("not statically verifiable"), never a false failure, since the plugin
binary isn't loaded during an offline dry-run.

Fully offline with no side effects — like validate and lint, it never contacts a running Conduit.

See also 'conduit pipelines validate' (errors only) and 'conduit pipelines lint' (advisory warnings).`,
		Example: "conduit pipelines dry-run pipelines/orders.yaml\n" +
			"conduit pipelines dry-run ./pipelines --resolve-plugins=false\n" +
			"conduit pipeline dry-run ./pipelines --json",
	}
}

func (c *DryRunCommand) Flags() []ecdysis.Flag {
	// --resolve-plugins defaults on. ecdysis.BuildFlags ignores a `default:`
	// struct tag for bools, so set the default explicitly via SetDefault.
	flags := ecdysis.BuildFlags(&c.flags)
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

func (c *DryRunCommand) ResultCommand() string { return "pipelines.dry-run" }

// ExecuteWithResult runs the offline validate engine with enriched-graph
// exposure and (optionally) plugin resolution, translating its Report into the
// shared cecdysis.Outcome envelope.
func (c *DryRunCommand) ExecuteWithResult(ctx context.Context) (cecdysis.Outcome, error) {
	c.renderer = ui.NewRenderer(rendererOutput(ctx), c.flags.NoColor)

	report, err := validate.RunWithOptions(ctx, c.args.Path, validate.Options{
		Enriched:          true,
		ResolvePlugins:    c.flags.ResolvePlugins,
		BuiltinPlugins:    builtinPluginSet(),
		BuiltinProcessors: builtinProcessorSet(),
	})
	if err != nil {
		return cecdysis.Outcome{}, conduiterr.Wrap(conduiterr.CodeInvalidArgument,
			fmt.Sprintf("could not resolve path %q: %v", c.args.Path, err), err)
	}

	outcome := cecdysis.Outcome{
		OK:      report.OK(),
		Summary: report.Summary,
		Result:  validate.Result{Files: report.Files},
	}
	if !report.OK() {
		if exitErr, ok := validate.ExitError(report.Files); ok {
			outcome.ExitErr = exitErr
		}
	}
	return outcome, nil
}

// Render renders a dry-run: per file, the ✗ findings (validation + unresolved
// builtin plugins) if any, then the enriched pipeline graph.
func (c *DryRunCommand) Render(outcome cecdysis.Outcome) string {
	summary, _ := outcome.Summary.(validate.Summary)
	result, _ := outcome.Result.(validate.Result)

	if c.renderer == nil {
		c.renderer = ui.NewRenderer(io.Discard, true)
	}

	var b strings.Builder
	passed := 0
	for _, f := range result.Files {
		if f.OK {
			passed++
			if !c.flags.Quiet {
				fmt.Fprintf(&b, "%s %s\n", c.renderer.Glyph(ui.StatusPass), f.Path)
			}
		} else {
			fmt.Fprintf(&b, "%s %s\n", c.renderer.Glyph(ui.StatusFail), f.Path)
			for _, finding := range f.Findings {
				ui.RenderFinding(&b, c.renderer.Glyph(ui.StatusFail), finding.Code, finding.ConfigPath, finding.Message, finding.Suggestion)
			}
		}

		// The enriched graph: what `conduit run` would actually load.
		for _, p := range f.Enriched {
			fmt.Fprintf(&b, "  pipeline %s\n", p.ID)
			for _, conn := range p.Connectors {
				fmt.Fprintf(&b, "    %-11s %-24s %s\n", conn.Type, conn.ID, conn.Plugin)
			}
			dlq := p.DLQ.Plugin
			if dlq == "" {
				dlq = "(none)"
			}
			fmt.Fprintf(&b, "    DLQ         %s\n", dlq)
		}
		if len(f.Enriched) > 0 {
			fmt.Fprintln(&b)
		}
	}

	fmt.Fprintf(&b, "Summary: %d files · %d passed · %d failed · %d problems\n",
		summary.Files, passed, summary.Files-passed, summary.Errors)
	if summary.Errors > 0 {
		fmt.Fprintln(&b, "Fix the ✗ items above, then re-run.")
	}
	return b.String()
}

// builtinPluginSet returns the set of resolvable builtin connector references,
// as both full module paths (github.com/conduitio/conduit-connector-<name>) and
// their short names (<name>), derived from builtin.DefaultBuiltinConnectors so
// it stays in sync with the actual builtin set. Injected into the validate
// engine so the engine itself stays offline and decoupled from the registry.
func builtinPluginSet() map[string]struct{} {
	set := make(map[string]struct{}, len(builtin.DefaultBuiltinConnectors)*2)
	for fullPath := range builtin.DefaultBuiltinConnectors {
		set[fullPath] = struct{}{}
		set[strings.TrimPrefix(path.Base(fullPath), "conduit-connector-")] = struct{}{}
	}
	return set
}

// builtinProcessorSet returns the set of resolvable builtin processor plugin
// names (e.g. "json.decode", "field.set") — the keys of
// builtin.DefaultBuiltinProcessors, which are already the plugin names a config
// references (unlike connectors, whose keys are full module paths). Injected
// into the validate engine so it stays offline/decoupled from the registry.
func builtinProcessorSet() map[string]struct{} {
	set := make(map[string]struct{}, len(procbuiltin.DefaultBuiltinProcessors))
	for name := range procbuiltin.DefaultBuiltinProcessors {
		set[name] = struct{}{}
	}
	return set
}
