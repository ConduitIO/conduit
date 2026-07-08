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

// LintFlags holds LintCommand's flags. Unlike validate, lint reports advisory
// warnings, so it has a --strict flag to escalate them to failures (CLI output
// conventions §3).
type LintFlags struct {
	Strict  bool `long:"strict" usage:"treat advisory warnings as failures (exit 2)"`
	Quiet   bool `long:"quiet" short:"q" usage:"suppress passing/OK lines and progress chrome; print only warnings, failures, and the summary"`
	NoColor bool `long:"no-color" usage:"disable colored/glyph output even on a color-capable terminal"`
}

// LintCommand implements `conduit pipelines lint <path>`: everything validate
// checks, plus the parser's advisory warnings (deprecated/renamed/unknown
// fields, version fallback). Warnings are advisory — exit 0 unless --strict.
// Offline, like validate. See
// docs/design-documents/20260708-cli-pipeline-inspect-lint-dryrun.md.
type LintCommand struct {
	args  LintArgs
	flags LintFlags

	renderer *ui.Renderer
}

func (c *LintCommand) Usage() string { return "lint <path>" }

func (c *LintCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Lint one or more pipeline configuration files (validate + advisory warnings)",
		Long: `Runs everything 'conduit pipelines validate' checks, and additionally reports the
parser's advisory warnings — deprecated or renamed fields, unrecognized fields, and pipeline
config version fallbacks — located by line and column. Like validate, lint is fully offline and
never contacts a running Conduit instance.

Warnings are advisory: lint exits 0 when a file has only warnings, unless --strict is set, which
escalates warnings to failures (exit 2). Validation errors always fail (exit 2).

See also 'conduit pipelines validate' (errors only) and 'conduit pipelines dry-run' (validate plus
the enriched pipeline graph).`,
		Example: "conduit pipelines lint pipelines/orders.yaml\n" +
			"conduit pipelines lint ./pipelines --strict\n" +
			"conduit pipeline lint ./pipelines --json",
	}
}

func (c *LintCommand) Flags() []ecdysis.Flag { return ecdysis.BuildFlags(&c.flags) }

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

// ExecuteWithResult runs the offline validate engine with warnings enabled and
// translates its Report into the shared cecdysis.Outcome envelope. As with
// validate, a non-nil error return is reserved for a hard failure (path can't
// be resolved at all), never for findings within resolved files.
func (c *LintCommand) ExecuteWithResult(ctx context.Context) (cecdysis.Outcome, error) {
	c.renderer = ui.NewRenderer(rendererOutput(ctx), c.flags.NoColor)

	report, err := validate.RunWithOptions(ctx, c.args.Path, validate.Options{Warnings: true})
	if err != nil {
		return cecdysis.Outcome{}, conduiterr.Wrap(conduiterr.CodeInvalidArgument,
			fmt.Sprintf("could not resolve path %q: %v", c.args.Path, err), err)
	}

	// OK is the envelope verdict: errors always fail; warnings fail only under
	// --strict.
	ok := report.Summary.Errors == 0
	if c.flags.Strict && report.Summary.Warnings > 0 {
		ok = false
	}

	outcome := cecdysis.Outcome{
		OK:      ok,
		Summary: report.Summary,
		Result:  validate.Result{Files: report.Files},
	}
	if !ok {
		if exitErr, has := validate.ExitErrorStrict(report.Files, c.flags.Strict); has {
			outcome.ExitErr = exitErr
		}
	}
	return outcome, nil
}

// Render renders a lint run: ✓ for clean files, ⚠ for warning-only files, ✗ for
// files with errors, each finding under it (errors and warnings), and a summary.
func (c *LintCommand) Render(outcome cecdysis.Outcome) string {
	summary, _ := outcome.Summary.(validate.Summary)
	result, _ := outcome.Result.(validate.Result)

	if c.renderer == nil {
		c.renderer = ui.NewRenderer(io.Discard, true)
	}

	var b strings.Builder
	passed, warned, failed := 0, 0, 0
	for _, f := range result.Files {
		status, glyph := fileStatus(c.renderer, f)
		switch status {
		case ui.StatusPass:
			passed++
			if !c.flags.Quiet {
				fmt.Fprintf(&b, "%s %s\n", glyph, f.Path)
			}
			continue
		case ui.StatusWarn:
			warned++
		case ui.StatusFail:
			failed++
		}

		fmt.Fprintf(&b, "%s %s\n", glyph, f.Path)
		for _, finding := range f.Findings {
			fg := c.renderer.Glyph(ui.StatusFail)
			if finding.Severity == validate.SeverityWarning {
				fg = c.renderer.Glyph(ui.StatusWarn)
			}
			ui.RenderFinding(&b, fg, finding.Code, findingLocation(finding), finding.Message, finding.Suggestion)
		}
		fmt.Fprintln(&b)
	}

	fmt.Fprintf(&b, "Summary: %d files · %d passed · %d warned · %d failed · %d errors · %d warnings\n",
		summary.Files, passed, warned, failed, summary.Errors, summary.Warnings)

	switch {
	case summary.Errors > 0:
		fmt.Fprintln(&b, "Fix the ✗ items above, then re-run.")
	case c.flags.Strict && summary.Warnings > 0:
		fmt.Fprintln(&b, "Warnings failed the run (--strict). Address the ⚠ items above.")
	}

	return b.String()
}

// fileStatus classifies one file report for rendering: fail if it has any
// error, warn if it has only warnings, pass if it has no findings.
func fileStatus(r *ui.Renderer, f validate.FileReport) (ui.Status, string) {
	hasErr := false
	hasWarn := false
	for _, find := range f.Findings {
		switch find.Severity {
		case validate.SeverityError:
			hasErr = true
		case validate.SeverityWarning:
			hasWarn = true
		}
	}
	switch {
	case hasErr:
		return ui.StatusFail, r.Glyph(ui.StatusFail)
	case hasWarn:
		return ui.StatusWarn, r.Glyph(ui.StatusWarn)
	default:
		return ui.StatusPass, r.Glyph(ui.StatusPass)
	}
}

// findingLocation returns the config-path pointer for an error finding, or a
// "line:column" location for a warning (which locates in the source file, not
// by a config-path pointer).
func findingLocation(f validate.Finding) string {
	if f.Severity == validate.SeverityWarning && f.Line > 0 {
		if f.ConfigPath != "" {
			return fmt.Sprintf("%s (line %d:%d)", f.ConfigPath, f.Line, f.Column)
		}
		return fmt.Sprintf("line %d:%d", f.Line, f.Column)
	}
	return f.ConfigPath
}
