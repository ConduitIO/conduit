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

package generate

import (
	"context"
	"fmt"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	yamlparser "github.com/conduitio/conduit/pkg/provisioning/config/yaml"
)

// SemanticResult is one candidate's outcome against a Request's Expect: did
// the actual source/destination connector category and processor set match
// what the request asked for, independent of whether the candidate also
// passed validate.Run. A candidate can validate cleanly and still fail
// here — wrong connector, swapped direction, or a dropped filter are all
// schema-valid mistakes (the DX-review failure mode design doc
// 20260722-conduit-generate.md exists to answer).
type SemanticResult struct {
	Match bool
	// Issues names every mismatch found, in Conduit's own vocabulary
	// (expected vs. found category, or a named missing capability) — never
	// a bare "no match", so a human (or a future retry-feedback loop, per
	// the design doc §3) has something concrete to act on.
	Issues []string
}

// scoreSemantic parses candidate with the SAME YAML parser validate.Run
// uses (never a second, hand-rolled parser) and checks it against expect:
//
//   - source/destination connector category (the builtin plugin's bare
//     name, e.g. "postgres" out of "builtin:postgres") matches, when expect
//     names one;
//   - every tag in expect.RequiredCapabilities resolves to at least one
//     matching processor (pipeline-level or attached to either connector) —
//     capability.go's hasCapability.
//
// A parse failure or an empty pipeline document is reported as a
// SemanticResult with Match false and a descriptive Issue, not a fatal
// error — the caller (ScoreRun) always gets a judgeable result for every
// candidate that was actually provided.
func scoreSemantic(ctx context.Context, expect Expect, candidate string) SemanticResult {
	pipelines, err := yamlparser.NewParser(log.Nop()).Parse(ctx, strings.NewReader(candidate))
	if len(pipelines) == 0 {
		return SemanticResult{
			Match:  false,
			Issues: []string{fmt.Sprintf("candidate did not parse into a judgeable pipeline: %v", errOrNoPipelines(err))},
		}
	}

	// generate's own non-goal (design doc, Goals/Non-goals: "one NL string
	// in, one pipeline candidate out") means a candidate is exactly one
	// pipeline; a document with more than one is judged on the first, and
	// that itself is flagged as an issue since it's not what a well-formed
	// candidate should ever produce. A partial parse (pipelines recovered
	// alongside a non-nil err, per yaml.Parser's "return whatever parsed
	// successfully" contract) is still judged on what did parse, with the
	// error surfaced as an additional issue rather than discarded.
	p := pipelines[0]
	var issues []string
	if err != nil {
		issues = append(issues, fmt.Sprintf("candidate had parse errors: %v", err))
	}
	if len(pipelines) > 1 {
		issues = append(issues, fmt.Sprintf("candidate contains %d pipelines, expected exactly 1", len(pipelines)))
	}

	sourceCat, destCat := connectorCategories(p)
	if expect.SourceCategory != "" && sourceCat != expect.SourceCategory {
		issues = append(issues, fmt.Sprintf("expected source category %q, found %q", expect.SourceCategory, orNone(sourceCat)))
	}
	if expect.DestinationCategory != "" && destCat != expect.DestinationCategory {
		issues = append(issues, fmt.Sprintf("expected destination category %q, found %q", expect.DestinationCategory, orNone(destCat)))
	}

	procs := allProcessors(p)
	for _, tag := range expect.RequiredCapabilities {
		if !hasCapability(procs, tag) {
			issues = append(issues, fmt.Sprintf("missing required capability %q (no matching processor present)", tag))
		}
	}

	return SemanticResult{Match: len(issues) == 0, Issues: issues}
}

// connectorCategories returns the builtin category (bare plugin name) of
// p's source and destination connector. A pipeline with more than one
// connector in the same role, or a non-builtin/unresolvable plugin
// reference, yields an empty category for that role — a mismatch against
// any non-empty Expect, and never a fabricated category by guessing which
// of several same-role connectors was "the" one the request meant.
func connectorCategories(p config.Pipeline) (source, destination string) {
	var sourceSeen, destSeen bool
	for _, c := range p.Connectors {
		cat := builtinCategory(c.Plugin)
		switch c.Type {
		case config.TypeSource:
			source, sourceSeen = foldRoleCategory(sourceSeen, cat)
		case config.TypeDestination:
			destination, destSeen = foldRoleCategory(destSeen, cat)
		}
	}
	return source, destination
}

// foldRoleCategory folds one more same-role connector's category into the
// running result for that role: the first one sets it, a second (or later)
// one makes it permanently ambiguous ("") rather than silently keeping
// whichever was seen first — a candidate with two sources (or two
// destinations) is malformed input the checker should never paper over
// with a guess at which one "the" connector was.
func foldRoleCategory(seenBefore bool, cat string) (string, bool) {
	if seenBefore {
		return "", true
	}
	return cat, true
}

// builtinCategory returns the bare plugin name of a builtin plugin
// reference (e.g. "postgres" for "builtin:postgres"), or "" for any
// standalone/"any"-typed reference — this package only ever judges against
// the corpus's grounding, the built-in connector set, per the task brief's
// "grounded in real built-in connectors" requirement.
func builtinCategory(pluginRef string) string {
	fn := plugin.FullName(pluginRef)
	if fn.PluginType() != plugin.PluginTypeBuiltin {
		return ""
	}
	return fn.PluginName()
}

// allProcessors flattens a pipeline's own processors and every connector's
// attached processors into one slice — a required capability may be
// satisfied at either level, and the semantic checker does not care which.
func allProcessors(p config.Pipeline) []config.Processor {
	out := make([]config.Processor, 0, len(p.Processors))
	out = append(out, p.Processors...)
	for _, c := range p.Connectors {
		out = append(out, c.Processors...)
	}
	return out
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func errOrNoPipelines(err error) error {
	if err != nil {
		return err
	}
	return cerrors.New("no pipelines in document")
}
