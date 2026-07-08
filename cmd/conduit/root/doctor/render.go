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

package doctor

import (
	"fmt"
	"os"
	"strings"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal/ui"
	"github.com/conduitio/conduit/pkg/conduit/check"
)

// categoryOrder is the fixed display order for Render's grouping — matches
// the design doc's mockup (Config, Store, Network, Plugins, Engine). Any
// category not in this list (shouldn't happen with doctor's own check set,
// but a defensive fallback for a caller-supplied one) is appended after,
// in first-seen order.
var categoryOrder = []string{"config", "store", check.CategoryNetwork, "plugins", "engine"}

var categoryTitles = map[string]string{
	"config":              "Config",
	"store":               "Store",
	check.CategoryNetwork: "Network",
	"plugins":             "Plugins",
	"engine":              "Engine",
}

// Render implements cecdysis.CommandWithResult: the human-readable rendering
// of a doctor run, grouped by category, using the shared ui package for
// glyph/color selection and the shared ui.RenderFinding layout for each
// check's line — per the CLI output conventions §2, doctor does not
// hand-roll its own line format (the doc's own review flagged doctor's
// original `└`-line mockup as exactly the drift this replaces).
//
// ui.RenderFinding's second line-1 argument is documented as a
// conduiterr-style "code". doctor passes the check's Name there instead:
// unlike a validate-style finding, doctor's Code is often empty on a Pass
// (see check.CheckResult's doc, "optional on Pass"), so using it here would
// leave passing lines with no identifying label at all — Name is always
// populated and is what --check filters on, so it's the more useful
// per-line identifier for this command specifically. The layout itself
// (glyph, then identifier + configPath on line 1, message and suggestion
// indented below) is unchanged from every other command using
// ui.RenderFinding.
func (c *DoctorCommand) Render(outcome cecdysis.Outcome) string {
	// Render is only ever called by the decorator with the Outcome
	// ExecuteWithResult just returned, so this type assertion never fails
	// in practice; the ", _" form still degrades to an empty report instead
	// of panicking if that assumption is ever wrong.
	res, _ := outcome.Result.(doctorResult)

	out := c.out
	if out == nil {
		out = os.Stdout
	}
	r := ui.NewRenderer(out, c.flags.NoColor)

	var buf strings.Builder
	fmt.Fprintln(&buf, "Conduit doctor — checking your environment")
	fmt.Fprintln(&buf)

	for _, cat := range orderedCategories(res.Checks) {
		title := categoryTitles[cat]
		if title == "" {
			title = strings.ToUpper(cat[:1]) + cat[1:]
		}
		fmt.Fprintln(&buf, title)

		for _, cr := range res.Checks {
			if resultCategory(cr) != cat {
				continue
			}
			if c.flags.Quiet && cr.Status == check.StatusPass {
				continue
			}
			glyph := r.Glyph(toUIStatus(cr.Status))
			ui.RenderFinding(&buf, glyph, cr.Name, cr.ConfigPath, cr.Message, cr.Suggestion)
		}
	}

	// outcome.Summary is the check.Summary ExecuteWithResult computed from
	// the same check.Report res.Checks came from (see doctor.go) — reused
	// directly rather than re-tallied from res.Checks, so Render can never
	// drift from the counts the --json envelope reports.
	summary, _ := outcome.Summary.(check.Summary)
	fmt.Fprintf(&buf, "\nSummary: %d passed · %d warnings · %d failed\n", summary.Passed, summary.Warned, summary.Failed)
	if summary.Failed > 0 {
		fmt.Fprintf(&buf, "%s items failed. Fix them, then re-run `conduit doctor`.\n", r.Glyph(ui.StatusFail))
	}

	return buf.String()
}

func resultCategory(cr check.CheckResult) string {
	if cr.Category == "" {
		return "other"
	}
	return cr.Category
}

// orderedCategories returns the distinct categories present in results,
// starting with categoryOrder's fixed sequence (for whichever of those are
// actually present), followed by any others in first-seen order.
func orderedCategories(results []check.CheckResult) []string {
	present := map[string]bool{}
	var firstSeen []string
	for _, cr := range results {
		cat := resultCategory(cr)
		if !present[cat] {
			present[cat] = true
			firstSeen = append(firstSeen, cat)
		}
	}

	var ordered []string
	seen := map[string]bool{}
	for _, cat := range categoryOrder {
		if present[cat] {
			ordered = append(ordered, cat)
			seen[cat] = true
		}
	}
	for _, cat := range firstSeen {
		if !seen[cat] {
			ordered = append(ordered, cat)
		}
	}
	return ordered
}

func toUIStatus(s check.Status) ui.Status {
	switch s {
	case check.StatusPass:
		return ui.StatusPass
	case check.StatusWarn:
		return ui.StatusWarn
	case check.StatusFail:
		return ui.StatusFail
	default:
		return ui.StatusFail
	}
}
