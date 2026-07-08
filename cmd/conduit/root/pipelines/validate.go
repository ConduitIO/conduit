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
	_ cecdysis.CommandWithResult = (*ValidateCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*ValidateCommand)(nil)
	_ ecdysis.CommandWithFlags   = (*ValidateCommand)(nil)
	_ ecdysis.CommandWithArgs    = (*ValidateCommand)(nil)
)

// ValidateArgs holds ValidateCommand's positional argument.
type ValidateArgs struct {
	// Path is a single .yml/.yaml pipeline config file, or a directory of
	// them (not recursed).
	Path string
}

// ValidateFlags holds ValidateCommand's flags. There is deliberately no
// --strict flag here (CLI output conventions §3): validate is errors-only,
// so there is nothing for --strict to escalate; that flag belongs to the
// future `lint`/`dry-run` verbs, which also report advisory warnings.
type ValidateFlags struct {
	Quiet   bool `long:"quiet" short:"q" usage:"suppress passing/OK lines and progress chrome; print only failures and the summary"`
	NoColor bool `long:"no-color" usage:"disable colored/glyph output even on a color-capable terminal"`
}

// ValidateCommand implements `conduit pipelines validate <path>`: offline,
// static verification of one or more pipeline config files. See
// docs/design-documents/20260707-cli-pipeline-validate.md and
// cmd/conduit/internal/validate for the engine this is a thin cobra shell
// over.
type ValidateCommand struct {
	args  ValidateArgs
	flags ValidateFlags

	// renderer is constructed in ExecuteWithResult (where the real output
	// stream and --no-color are available via the cobra command in ctx) and
	// used by Render, which cecdysis.CommandWithResultDecorator calls
	// afterwards on the same command value with no ctx of its own.
	renderer *ui.Renderer
}

func (c *ValidateCommand) Usage() string { return "validate <path>" }

func (c *ValidateCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Validate one or more pipeline configuration files",
		Long: `Runs the same offline parse, enrich, and validate steps 'conduit run' uses to
load pipeline configuration files, without starting Conduit or dialing its API — this command
never requires (or contacts) a running Conduit instance.

<path> is a single .yml/.yaml file, or a directory of them (not recursed into
subdirectories). Every problem found is reported — a bad file, or a bad pipeline within a
file, never stops the rest from being checked. Exits 0 if every pipeline is valid, 2
otherwise.`,
		Example: "conduit pipelines validate pipelines/orders.yaml\n" +
			"conduit pipelines validate ./pipelines --json\n" +
			"conduit pipeline validate ./pipelines --quiet",
	}
}

func (c *ValidateCommand) Flags() []ecdysis.Flag {
	return ecdysis.BuildFlags(&c.flags)
}

func (c *ValidateCommand) Args(args []string) error {
	if len(args) == 0 {
		return cerrors.Errorf("requires a path to a pipeline config file or directory")
	}
	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
	}
	c.args.Path = args[0]
	return nil
}

func (c *ValidateCommand) ResultCommand() string { return "pipelines.validate" }

// ExecuteWithResult runs the offline validate engine and translates its
// Report into the shared cecdysis.Outcome envelope. A non-nil error return
// here is reserved for a HARD failure — path itself couldn't be resolved at
// all (e.g. it doesn't exist) — never for findings within files that did
// resolve; see validate.Run's doc.
func (c *ValidateCommand) ExecuteWithResult(ctx context.Context) (cecdysis.Outcome, error) {
	c.renderer = ui.NewRenderer(rendererOutput(ctx), c.flags.NoColor)

	report, err := validate.Run(ctx, c.args.Path)
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
		// See CLI output conventions §4 and cecdysis.Outcome.ExitErr's doc:
		// a validation run that finds problems is still OK:false/error:null
		// in the rendered envelope — ExitErr is a side channel purely for
		// the process exit code, not part of what gets rendered.
		if exitErr, ok := validate.ExitError(report.Files); ok {
			outcome.ExitErr = exitErr
		}
	}
	return outcome, nil
}

// Render returns the human-readable rendering of a validate run: one line
// per file (✓/✗ <path>), every finding under a failing file rendered via
// ui.RenderFinding, and a summary line.
func (c *ValidateCommand) Render(outcome cecdysis.Outcome) string {
	summary, _ := outcome.Summary.(validate.Summary)
	result, _ := outcome.Result.(validate.Result)

	if c.renderer == nil {
		// Defensive only: cecdysis.CommandWithResultDecorator always calls
		// ExecuteWithResult (which sets c.renderer unconditionally, as its
		// first statement) before ever calling Render on the same command
		// value, so this is unreached in the real command pipeline. It
		// exists so a test that constructs a ValidateCommand and calls
		// Render directly, without going through ExecuteWithResult first,
		// gets plain ASCII output instead of a nil-pointer panic.
		c.renderer = ui.NewRenderer(io.Discard, true)
	}

	var b strings.Builder
	for _, f := range result.Files {
		if f.OK {
			if !c.flags.Quiet {
				fmt.Fprintf(&b, "%s %s\n", c.renderer.Glyph(ui.StatusPass), f.Path)
			}
			continue
		}

		fmt.Fprintf(&b, "%s %s\n", c.renderer.Glyph(ui.StatusFail), f.Path)
		for _, finding := range f.Findings {
			ui.RenderFinding(&b, c.renderer.Glyph(ui.StatusFail), finding.Code, finding.ConfigPath, finding.Message, finding.Suggestion)
		}
		fmt.Fprintln(&b)
	}

	passed := 0
	for _, f := range result.Files {
		if f.OK {
			passed++
		}
	}
	fmt.Fprintf(&b, "Summary: %d files · %d passed · %d failed · %d problems\n",
		summary.Files, passed, summary.Files-passed, summary.Errors)

	if summary.Errors > 0 {
		fmt.Fprintln(&b, "Fix the ✗ items above, then re-run.")
	}

	return b.String()
}

// rendererOutput returns the cobra command's stdout stream so ui.NewRenderer
// can detect whether it's a color-capable TTY, falling back to io.Discard
// (never a terminal, so ui.NewRenderer's plain-ASCII path) if ctx carries no
// cobra command — defensive for direct engine/unit-test callers that don't
// go through the full ecdysis command pipeline.
func rendererOutput(ctx context.Context) io.Writer {
	if cmd := ecdysis.CobraCmdFromContext(ctx); cmd != nil {
		return cmd.OutOrStdout()
	}
	return io.Discard
}
