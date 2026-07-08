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

package deploy

import (
	"fmt"
	"strings"

	"github.com/conduitio/conduit/cmd/conduit/internal/ui"
	"github.com/conduitio/conduit/pkg/provisioning"
)

// Summary is the --json envelope's `summary` payload for both `deploy` and
// `apply`: a rollup of Diff.Changes by action, plus whether applying would
// (or did) require restarting the pipeline.
type Summary struct {
	Create  int  `json:"create"`
	Update  int  `json:"update"`
	Delete  int  `json:"delete"`
	Restart bool `json:"restart"`
}

// Result is the --json envelope's `result` payload for both `deploy` and
// `apply`.
type Result struct {
	PipelineID string                `json:"pipelineID"`
	Hash       string                `json:"hash"`
	Changes    []provisioning.Change `json:"changes"`
	// Applied is true only for `apply` (always false for `deploy`, which
	// never executes anything).
	Applied bool `json:"applied"`
}

// Summarize rolls a Diff up into a Summary.
func Summarize(diff provisioning.Diff) Summary {
	var s Summary
	for _, c := range diff.Changes {
		switch c.Action {
		case provisioning.ChangeActionCreate:
			s.Create++
		case provisioning.ChangeActionUpdate:
			s.Update++
		case provisioning.ChangeActionDelete:
			s.Delete++
		}
		if c.Effect == provisioning.EffectRestart {
			s.Restart = true
		}
	}
	return s
}

// Render returns the human-readable rendering of diff: one line per Change
// (glyph, action, resource, ID, effect, config paths), then a summary line.
// applied indicates whether this Diff was actually executed (apply) or only
// previewed (deploy), which changes the summary line's wording and the
// trailing "Run: ..." hint.
func Render(r *ui.Renderer, diff provisioning.Diff, applied bool) string {
	var b strings.Builder

	if diff.Empty() {
		fmt.Fprintf(&b, "No changes for pipeline %q — already up to date.\n", diff.PipelineID)
		return b.String()
	}

	fmt.Fprintf(&b, "Plan for pipeline %q (hash %s):\n", diff.PipelineID, shortHash(diff.Hash))
	for _, c := range diff.Changes {
		fmt.Fprintf(&b, "  %s %-6s %-9s %-20s %-9s", r.DiffGlyph(diffGlyphFor(c.Action)), string(c.Action), string(c.Resource), c.ID, effectLabel(c.Effect))
		if len(c.ConfigPaths) > 0 {
			fmt.Fprintf(&b, " %s", strings.Join(c.ConfigPaths, ", "))
		}
		fmt.Fprintln(&b)
	}

	sum := Summarize(diff)
	fmt.Fprintf(&b, "\n%d to create, %d to update, %d to delete.\n", sum.Create, sum.Update, sum.Delete)

	if sum.Restart {
		if applied {
			fmt.Fprintf(&b, "Pipeline %q was RESTARTED to apply these changes.\n", diff.PipelineID)
		} else {
			fmt.Fprintf(&b, "Applying will RESTART pipeline %q.\n", diff.PipelineID)
		}
	}
	if !applied {
		fmt.Fprintf(&b, "Run:  conduit pipelines apply <file> --plan-hash %s\n", diff.Hash)
	}

	return b.String()
}

// shortHash returns a short, display-only prefix of a full plan hash — never
// used as the actual token (ApplyPlan/--plan-hash always compare the full
// hash), purely so the human-readable "Plan for pipeline ... (hash ...)"
// line isn't a 64-character wall of hex.
func shortHash(hash string) string {
	const n = 8
	if len(hash) <= n {
		return hash
	}
	return hash[:n]
}

func diffGlyphFor(a provisioning.ChangeAction) ui.DiffAction {
	switch a {
	case provisioning.ChangeActionCreate:
		return ui.DiffCreate
	case provisioning.ChangeActionDelete:
		return ui.DiffDelete
	case provisioning.ChangeActionUpdate:
		return ui.DiffUpdate
	default:
		return ui.DiffUpdate
	}
}

func effectLabel(e provisioning.Effect) string {
	if e == provisioning.EffectRestart {
		return "restart"
	}
	return "in place"
}
