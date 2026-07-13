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
	"os"
	"strings"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal/repair"
	"github.com/conduitio/conduit/cmd/conduit/internal/ui"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/ecdysis"
)

var (
	_ cecdysis.CommandWithResult = (*RepairCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*RepairCommand)(nil)
	_ ecdysis.CommandWithFlags   = (*RepairCommand)(nil)
	_ ecdysis.CommandWithArgs    = (*RepairCommand)(nil)
)

// RepairArgs holds RepairCommand's positional argument.
type RepairArgs struct {
	// Path is a single .yml/.yaml pipeline config file containing exactly
	// one pipeline document — the same single-pipeline scope deploy/apply
	// already impose (see cmd/conduit/internal/repair's doc for why).
	Path string
}

// RepairFlags holds RepairCommand's flags (design doc §5.1).
type RepairFlags struct {
	Apply    bool     `long:"apply" usage:"apply the plan instead of only previewing it — safe fixes only, unless --fix selects others"`
	PlanHash string   `long:"plan-hash" usage:"the hash from a prior 'repair' read to apply; required with --apply unless --yes"`
	Yes      bool     `long:"yes" short:"y" usage:"with --apply, apply the freshly recomputed plan directly, without requiring --plan-hash"`
	Fix      []string `long:"fix" usage:"apply only the fix(es) at this configPath; repeatable. Default (with --apply): every safe fix"`
	Escalate bool     `long:"escalate" usage:"permit applying an explicitly --fix-selected data-path-adjacent fix (ack/position/checkpoint-adjacent config) — human-only; never available to an agent via MCP"`
	NoColor  bool     `long:"no-color" usage:"disable colored/glyph output even on a color-capable terminal"`
}

// RepairCommand implements `conduit pipelines repair <file>`: collects every
// machine-appliable Fix a validate/lint run finds (cmd/conduit/internal/repair.Collect),
// renders it as a diff with a plan hash (read, the default), and — only with
// --apply and a matching hash — applies the safe subset (or an explicit
// --fix selection) and atomically rewrites the file. See
// docs/design-documents/20260712-repair-command.md.
//
// Tier-1 safety (AC-14): a data-path-adjacent fix named via --fix without
// --escalate is refused — the whole run, nothing written — with
// repair.data_path_fix_refused. This CLI-level check exists because
// repair.Apply itself treats a refused data-path fix as a per-fix skip
// (procedural, like every other engine call in this codebase) so that MCP's
// repair_apply tool — which never sets Escalate — can report the same skip
// as part of a normal, successful result (AC-15) instead of a failed tool
// call. The CLI and MCP therefore see IDENTICAL engine behavior; they only
// differ in how they react to an explicitly-requested fix coming back
// skipped for this reason — see repair.ApplyInput.Escalate's doc.
type RepairCommand struct {
	args  RepairArgs
	flags RepairFlags

	renderer *ui.Renderer
}

func (c *RepairCommand) Usage() string { return "repair <file>" }

func (c *RepairCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Preview (and optionally apply) machine-appliable fixes for a pipeline config file",
		Long: `Collects every finding 'conduit pipelines validate'/'lint' can find a structured, machine
-appliable fix for (a deprecated/renamed field, an unambiguous invalid /status value, a negative
processor /workers, an over-long /description) and shows the proposed edit as a diff, with a plan
hash. Mutates nothing on its own.

Pass --apply (with --plan-hash from the read step, or --yes to bind to a freshly recomputed plan)
to apply the safe fixes — restart-class and data-path-adjacent fixes are never applied by default;
select one explicitly with --fix <configPath>. A data-path-adjacent fix (connector settings, a
connector's own plugin/type, DLQ config, any id field) additionally requires --escalate — the
human-only Tier-1 override; there is no equivalent for the MCP repair_apply tool.

repair edits the config FILE only — it never touches the pipeline store or a running pipeline.
Getting a repaired file into a running engine is still 'conduit pipelines deploy'/'apply', which
keeps its own plan-hash and running-pipeline guard.`,
		Example: "conduit pipelines repair orders.yaml\n" +
			"conduit pipelines repair orders.yaml --json\n" +
			"conduit pipelines repair orders.yaml --apply --plan-hash 9f3a2c...\n" +
			"conduit pipelines repair orders.yaml --apply --yes\n" +
			"conduit pipelines repair orders.yaml --apply --yes --fix /processors/0/workers",
	}
}

func (c *RepairCommand) Flags() []ecdysis.Flag {
	return ecdysis.BuildFlags(&c.flags)
}

func (c *RepairCommand) Args(args []string) error {
	if len(args) == 0 {
		return cerrors.Errorf("requires a path to a pipeline config file")
	}
	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
	}
	c.args.Path = args[0]
	return nil
}

func (c *RepairCommand) ResultCommand() string { return "pipelines.repair" }

func (c *RepairCommand) ExecuteWithResult(ctx context.Context) (cecdysis.Outcome, error) {
	c.renderer = ui.NewRenderer(rendererOutput(ctx), c.flags.NoColor)

	plan, err := repair.Collect(ctx, c.args.Path)
	if err != nil {
		return cecdysis.Outcome{}, err
	}

	if !c.flags.Apply {
		// Read mode: exit 0 whether or not there are fixes to offer (design
		// doc §5.1) — this is procedural (like deploy), not evaluative.
		return cecdysis.Outcome{
			OK:      true,
			Summary: repair.SummarizePlan(plan),
			Result:  plan,
		}, nil
	}

	hash := c.flags.PlanHash
	if hash == "" {
		if !c.flags.Yes {
			ce := conduiterr.New(conduiterr.CodeInvalidArgument,
				"repair --apply requires --plan-hash (from a prior 'conduit pipelines repair' read), or --yes to apply the freshly recomputed plan directly")
			ce.Suggestion = "run 'conduit pipelines repair " + c.args.Path + "' first, review the plan, then pass its hash"
			return cecdysis.Outcome{}, ce
		}
		hash = plan.Hash
	}

	res, err := repair.Apply(ctx, repair.ApplyInput{
		Path:     c.args.Path,
		Hash:     hash,
		Select:   c.flags.Fix,
		Escalate: c.flags.Escalate,
	})
	if err != nil {
		return cecdysis.Outcome{}, err
	}

	// AC-14: an EXPLICITLY --fix-selected data-path fix that came back
	// skipped (no --escalate) refuses the whole run — see RepairCommand's
	// doc for why this check lives here, at the CLI layer, rather than in
	// the engine.
	if len(c.flags.Fix) > 0 {
		if bad, ok := firstDataPathRefusal(res.Fixes); ok {
			ce := conduiterr.New(repair.CodeDataPathFixRefused,
				fmt.Sprintf("fix at %q is data-path-adjacent (ack/position/checkpoint-adjacent config) and requires --escalate", bad))
			ce.ConfigPath = bad
			ce.Suggestion = "re-run with --escalate to apply this fix (human-only — never available via MCP), or drop it from --fix"
			return cecdysis.Outcome{}, ce
		}
	}

	appliedAny := false
	for _, f := range res.Fixes {
		if f.Outcome == repair.FixOutcomeApplied {
			appliedAny = true
			break
		}
	}
	if appliedAny {
		perm := os.FileMode(0o644)
		if info, statErr := os.Stat(c.args.Path); statErr == nil {
			perm = info.Mode().Perm()
		}
		if err := repair.WriteFileAtomic(c.args.Path, []byte(res.Content), perm); err != nil {
			return cecdysis.Outcome{}, conduiterr.Wrap(conduiterr.CodeInternal, "could not write the repaired pipeline config file", err)
		}
	}

	return cecdysis.Outcome{
		OK:      true,
		Summary: repair.SummarizeResult(res),
		Result:  res,
	}, nil
}

func (c *RepairCommand) Render(outcome cecdysis.Outcome) string {
	if c.renderer == nil {
		c.renderer = ui.NewRenderer(io.Discard, true)
	}

	switch result := outcome.Result.(type) {
	case repair.Plan:
		return renderPlan(c.renderer, result)
	case repair.Result:
		return renderApplyResult(c.renderer, result)
	default:
		return ""
	}
}

// firstDataPathRefusal returns the ConfigPath of the first fix skipped for
// repair.CodeDataPathFixRefused, if any.
func firstDataPathRefusal(fixes []repair.AppliedFix) (string, bool) {
	for _, f := range fixes {
		if f.Outcome == repair.FixOutcomeSkipped && f.Reason == repair.CodeDataPathFixRefused.Reason() {
			return f.ConfigPath, true
		}
	}
	return "", false
}

func renderPlan(r *ui.Renderer, plan repair.Plan) string {
	var b strings.Builder
	if len(plan.Fixes) == 0 {
		fmt.Fprintf(&b, "No machine-appliable fixes for %q — already clean.\n", plan.Path)
		return b.String()
	}

	fmt.Fprintf(&b, "Proposed fixes for %q (hash %s):\n", plan.Path, shortHash(plan.Hash))
	for _, f := range plan.Fixes {
		fmt.Fprintf(&b, "  %s [%-9s] %-24s %s -> %s\n",
			r.DiffGlyph(diffGlyphForClass(f.Class)), string(f.Class), f.ConfigPath, renderValue(f.Before), renderValue(f.After))
		fmt.Fprintf(&b, "      %s: %s\n", f.Code, f.Message)
	}
	fmt.Fprintf(&b, "\nRun:  conduit pipelines repair <file> --apply --plan-hash %s\n", plan.Hash)
	return b.String()
}

func renderApplyResult(r *ui.Renderer, res repair.Result) string {
	var b strings.Builder
	applied, skipped := 0, 0
	for _, f := range res.Fixes {
		glyph := r.Glyph(ui.StatusPass)
		if f.Outcome == repair.FixOutcomeSkipped {
			glyph = r.Glyph(ui.StatusWarn)
			skipped++
		} else {
			applied++
		}
		fmt.Fprintf(&b, "  %s %-9s %s", glyph, string(f.Outcome), f.ConfigPath)
		if f.Reason != "" {
			fmt.Fprintf(&b, " (%s)", f.Reason)
		}
		fmt.Fprintln(&b)
	}
	fmt.Fprintf(&b, "\n%d applied, %d skipped.\n", applied, skipped)
	if applied > 0 {
		fmt.Fprintf(&b, "%q was updated.\n", res.Path)
	}
	return b.String()
}

func renderValue(v string) string {
	if v == "" {
		return "<empty>"
	}
	return v
}

func diffGlyphForClass(c repair.FixClass) ui.DiffAction {
	switch c {
	case repair.FixClassSafe:
		return ui.DiffUpdate
	case repair.FixClassRestart:
		return ui.DiffUpdate
	case repair.FixClassDataPath:
		return ui.DiffDelete
	default:
		return ui.DiffUpdate
	}
}

// shortHash mirrors deploy/render.go's own shortHash — display-only, never
// used as the actual comparison token.
func shortHash(hash string) string {
	const n = 8
	if len(hash) <= n {
		return hash
	}
	return hash[:n]
}
