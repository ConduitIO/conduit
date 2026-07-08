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
	_ cecdysis.CommandWithResult = (*LintCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*LintCommand)(nil)
	_ ecdysis.CommandWithFlags   = (*LintCommand)(nil)
	_ ecdysis.CommandWithArgs    = (*LintCommand)(nil)
)

// LintArgs holds LintCommand's positional argument.
type LintArgs struct {
	// Path is a single .yml/.yaml pipeline config file, or a directory of
	// them (not recursed).
	Path string
}

// LintFlags holds LintCommand's flags. Strict is lint's one addition over
// `validate`'s flag set (see ValidateFlags's doc for why validate itself has
// no --strict): lint has advisory warnings for --strict to escalate,
// validate does not.
type LintFlags struct {
	Strict  bool `long:"strict" usage:"treat advisory warnings as failures (exit 2 if any warning is found)"`
	Quiet   bool `long:"quiet" short:"q" usage:"suppress passing/OK lines and progress chrome; print only failures, warnings, and the summary"`
	NoColor bool `long:"no-color" usage:"disable colored/glyph output even on a color-capable terminal"`
}

// LintCommand implements `conduit pipeline lint <path>`: everything
// `pipelines validate` checks, plus advisory parser warnings (deprecated/
// renamed/unknown fields, version fallback) surfaced with their source
// line/column. See docs/design-documents/20260708-cli-pipeline-inspect-lint-dryrun.md
// and cmd/conduit/internal/validate.RunLint for the shared engine this is a
// thin cobra shell over.
type LintCommand struct {
	args  LintArgs
	flags LintFlags

	// renderer is constructed in ExecuteWithResult (where the real output
	// stream and --no-color are available via the cobra command in ctx) and
	// used by Render, which cecdysis.CommandWithResultDecorator calls
	// afterwards on the same command value with no ctx of its own.
	renderer *ui.Renderer
}

func (c *LintCommand) Usage() string { return "lint <path>" }

func (c *LintCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Validate one or more pipeline configuration files, plus advisory warnings",
		Long: `Runs the same offline checks as 'conduit pipelines validate' (parse, enrich,
validate — no server, no API dial), plus advisory warnings for deprecated, renamed, or
unknown fields and pipeline-config version fallbacks, each with the source line and column.

<path> is a single .yml/.yaml file, or a directory of them (not recursed into
subdirectories). Every problem found is reported — a bad file, or a bad pipeline within a
file, never stops the rest from being checked.

Warnings are advisory: they exit 0 unless --strict is set, in which case any warning also
exits 2 (same as an error). An error always exits 2, --strict or not.

See also: 'conduit pipelines validate' (errors only, no --strict) and 'conduit pipelines
dry-run' (validate + the enriched pipeline graph + builtin plugin-ref resolution).`,
		Example: "conduit pipelines lint pipelines/orders.yaml\n" +
			"conduit pipelines lint ./pipelines --strict\n" +
			"conduit pipeline lint ./pipelines --json",
	}
}

func (c *LintCommand) Flags() []ecdysis.Flag {
	return ecdysis.BuildFlags(&c.flags)
}

func (c *LintCommand) Args(args []string) error {
	if len(args) == 0 {
		return cerrors.Errorf("requires a path to a pipeline config file or directory")
	}
	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
	}
	c.args.Path = args[0]
	return nil
}

func (c *LintCommand) ResultCommand() string { return "pipelines.lint" }

// ExecuteWithResult runs the offline lint engine and translates its Report
// into the shared cecdysis.Outcome envelope. A non-nil error return here is
// reserved for a HARD failure — path itself couldn't be resolved at all
// (e.g. it doesn't exist) — never for findings within files that did
// resolve; see validate.RunLint's doc.
func (c *LintCommand) ExecuteWithResult(ctx context.Context) (cecdysis.Outcome, error) {
	c.renderer = ui.NewRenderer(rendererOutput(ctx), c.flags.NoColor)

	report, err := validate.RunLint(ctx, c.args.Path)
	if err != nil {
		return cecdysis.Outcome{}, conduiterr.Wrap(conduiterr.CodeInvalidArgument,
			fmt.Sprintf("could not resolve path %q: %v", c.args.Path, err), err)
	}

	// report.OK() is errors-only; under --strict, a warning also makes the
	// run not-OK for the envelope's `ok` field, matching what
	// LintExitError(strict) does for the process exit code (AC-2: "warning
	// promoted to failure").
	ok := report.OK() && (!c.flags.Strict || report.Summary.Warnings == 0)

	outcome := cecdysis.Outcome{
		OK:      ok,
		Summary: report.Summary,
		Result:  validate.Result{Files: report.Files},
	}
	if !ok {
		// See CLI output conventions §4 and cecdysis.Outcome.ExitErr's doc:
		// a lint run that finds problems is still OK:false/error:null in the
		// rendered envelope — ExitErr is a side channel purely for the
		// process exit code, not part of what gets rendered.
		if exitErr, found := validate.LintExitError(report.Files, c.flags.Strict); found {
			outcome.ExitErr = exitErr
		}
	}
	return outcome, nil
}

// Render returns the human-readable rendering of a lint run: one line per
// file (✓ pass / ⚠ warnings-only / ✗ has an error), every finding under a
// non-clean file rendered via ui.RenderFinding with its own severity's
// glyph, and a summary line.
func (c *LintCommand) Render(outcome cecdysis.Outcome) string {
	summary, _ := outcome.Summary.(validate.Summary)
	result, _ := outcome.Result.(validate.Result)

	if c.renderer == nil {
		// Defensive only — see ValidateCommand.Render's identical comment:
		// unreached via the real command pipeline, only for a test that
		// calls Render directly.
		c.renderer = ui.NewRenderer(io.Discard, true)
	}

	var b strings.Builder
	passed, warned, failed := 0, 0, 0
	for _, f := range result.Files {
		status, hasErrors, hasWarnings := fileLintStatus(f)
		switch {
		case hasErrors:
			failed++
		case hasWarnings:
			warned++
		default:
			passed++
		}

		if !hasErrors && !hasWarnings {
			if !c.flags.Quiet {
				fmt.Fprintf(&b, "%s %s\n", c.renderer.Glyph(status), f.Path)
			}
			continue
		}

		fmt.Fprintf(&b, "%s %s\n", c.renderer.Glyph(status), f.Path)
		for _, finding := range f.Findings {
			glyph := c.renderer.Glyph(ui.StatusFail)
			if finding.Severity == validate.SeverityWarning {
				glyph = c.renderer.Glyph(ui.StatusWarn)
			}
			ui.RenderFinding(&b, glyph, finding.Code, findingLocation(finding), finding.Message, finding.Suggestion)
		}
		fmt.Fprintln(&b)
	}

	fmt.Fprintf(&b, "Summary: %d files · %d passed · %d warned · %d failed · %d errors · %d warnings\n",
		summary.Files, passed, warned, failed, summary.Errors, summary.Warnings)

	switch {
	case summary.Errors > 0:
		fmt.Fprintln(&b, "Fix the ✗ items above, then re-run.")
	case summary.Warnings > 0 && c.flags.Strict:
		fmt.Fprintln(&b, "Warnings are failing this run under --strict. Fix the ⚠ items above, then re-run.")
	case summary.Warnings > 0:
		fmt.Fprintln(&b, "Warnings are advisory (exit 0). Re-run with --strict to treat them as failures.")
	}

	return b.String()
}

// fileLintStatus classifies one lint FileReport into the three-way status
// `lint` renders with (pass/warn/fail — validate only ever needs
// pass/fail), plus whether it has any error- or warning-severity finding at
// all (Render uses these to decide whether to print findings and to tally
// the summary counts).
func fileLintStatus(f validate.FileReport) (status ui.Status, hasErrors, hasWarnings bool) {
	for _, finding := range f.Findings {
		switch finding.Severity {
		case validate.SeverityError:
			hasErrors = true
		case validate.SeverityWarning:
			hasWarnings = true
		}
	}
	switch {
	case hasErrors:
		return ui.StatusFail, hasErrors, hasWarnings
	case hasWarnings:
		return ui.StatusWarn, hasErrors, hasWarnings
	default:
		return ui.StatusPass, hasErrors, hasWarnings
	}
}

// findingLocation renders a Finding's location for the human output: a
// config.Validate error has a JSON-pointer ConfigPath; a parser warning
// (lint's SeverityWarning findings) has no ConfigPath, only Line/Column, so
// this falls back to a "line:column" form for those instead of printing
// nothing.
func findingLocation(f validate.Finding) string {
	if f.ConfigPath != "" {
		return f.ConfigPath
	}
	if f.Line > 0 {
		if f.Column > 0 {
			return fmt.Sprintf("line %d, column %d", f.Line, f.Column)
		}
		return fmt.Sprintf("line %d", f.Line)
	}
	return ""
}
