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

package repair

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/conduitio/conduit/cmd/conduit/internal/validate"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/yaml/v3"
	json "github.com/goccy/go-json"
)

// Plan is Collect/CollectContent's result: every fixable finding in the
// config, classified, plus a Hash binding this exact Plan — Apply refuses to
// run unless the caller presents this Hash (design doc §4.1, mirroring
// provisioning.Diff.Hash / ApplyPlan's hash check exactly).
type Plan struct {
	// Path is the file path Collect was called with; empty for a
	// CollectContent call (MCP content-in — no server-side path is ever
	// echoed back to an agent, matching withTempConfigFile's own rule
	// elsewhere in this codebase).
	Path string `json:"path,omitempty"`
	// Hash is a digest of the source bytes plus the ordered Fixes — ANY
	// byte-for-byte change to the file (or, since Fixes is included, a
	// change to the fixes computed from it) changes the hash. See
	// computeHash.
	Hash string `json:"hash"`
	// Fixes is every finding in the config that carries a machine
	// -appliable conduiterr.Fix, classified. A config with zero fixable
	// findings still returns a valid (empty-Fixes) Plan and a nil error —
	// "already clean" is not itself an error (design doc §5.1, AC-21).
	Fixes []ProposedFix `json:"fixes"`
}

// ProposedFix is one fixable finding, ready to render as a diff line (design
// doc §4.1). ConfigPath and Fix.ConfigPath are always identical: ConfigPath
// exists purely so a --json consumer doesn't have to reach into Fix for the
// single most common field. (The two are NOT always identical to the
// underlying validate.Finding.ConfigPath a rename-class warning carries —
// see documentPath's doc for why: repair always uses the Fix's own
// ConfigPath, which is unambiguous and directly walkable, never the
// finding's.)
type ProposedFix struct {
	Code       string         `json:"code"`
	Message    string         `json:"message"`
	ConfigPath string         `json:"configPath"`
	Fix        conduiterr.Fix `json:"fix"`
	Class      FixClass       `json:"class"`
	// Before/After are single-line, human-readable renderings of the
	// current and proposed values — never a connector Settings value (see
	// classify's doc: any settings-adjacent fix is always FixClassDataPath,
	// and repair never even reaches this rendering step for one, since
	// building Before/After only happens for fixes the v1 producer set
	// actually emits, none of which touch settings).
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

// Collect runs the offline validate/lint engine (validate.RunWithOptions
// with Warnings enabled — the same engine `conduit pipelines lint` uses,
// which is a strict superset of what `validate` collects) over path,
// gathers every Finding that carries a non-nil Fix, classifies each, and
// returns a Plan. Pure read: no file writes, no store access (AC-6, AC-7).
func Collect(ctx context.Context, path string) (Plan, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		ce := conduiterr.Wrap(conduiterr.CodeInvalidArgument, "could not read pipeline config file: "+err.Error(), err)
		ce.Suggestion = "check that the file exists and is readable"
		return Plan{}, ce
	}
	return collect(ctx, path, path, raw)
}

// CollectContent is Collect's content-in variant for MCP (mirrors
// cmd/conduit/internal/mcp's withTempConfigFile pattern): same engine, YAML
// content instead of a server-side path. The returned Plan.Path is always
// empty — no server path is ever echoed back to an agent.
func CollectContent(ctx context.Context, content string) (Plan, error) {
	raw := []byte(content)
	dir, err := os.MkdirTemp("", "conduit-repair-*")
	if err != nil {
		return Plan{}, conduiterr.Wrap(conduiterr.CodeInternal, "could not create a temporary directory for the pipeline config", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	// path is built from a fresh os.MkdirTemp directory plus a fixed literal
	// filename — never from content or any other caller-supplied string —
	// so there is no path-traversal surface here despite content itself
	// being untrusted (an MCP agent's YAML); gosec's taint analysis flags
	// this purely because CollectContent is an exported entry point.
	path := filepath.Join(dir, "pipeline.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil { //nolint:gosec // path is MkdirTemp+literal, not derived from content
		return Plan{}, conduiterr.Wrap(conduiterr.CodeInternal, "could not write the temporary pipeline config file", err)
	}

	return collect(ctx, path, "", raw)
}

// collect is Collect/CollectContent's shared body. validatePath is always a
// real file on disk (Collect's own path, or CollectContent's temp file) —
// validate.RunWithOptions needs one; displayPath is what the returned
// Plan.Path is set to (empty for CollectContent).
func collect(ctx context.Context, validatePath, displayPath string, raw []byte) (Plan, error) {
	report, err := validate.RunWithOptions(ctx, validatePath, validate.Options{Warnings: true})
	if err != nil {
		return Plan{}, err
	}
	if len(report.Files) != 1 {
		// Can only happen for a directory input with zero or multiple
		// resolved files — validate.Run tolerates that; repair does not
		// (doc.go's v1 scope boundary).
		ce := conduiterr.New(conduiterr.CodeInvalidArgument, "repair requires exactly one pipeline config file, not a directory")
		ce.Suggestion = "pass the path to one .yml/.yaml file"
		return Plan{}, ce
	}
	fr := report.Files[0]
	if len(fr.Pipelines) == 0 {
		// Zero pipelines almost always means the file didn't parse at all
		// (a genuine YAML/version error, not "zero pipelines defined") —
		// surface the ACTUAL parse finding's message/suggestion here rather
		// than a generic "wrong pipeline count", which would otherwise mask
		// what's really wrong (adversarial self-review finding, see the PR
		// description).
		if len(fr.Findings) > 0 {
			f := fr.Findings[0]
			ce := conduiterr.New(conduiterr.CodeInvalidArgument, "repair could not read a pipeline from "+validatePath+": "+f.Message)
			ce.ConfigPath = f.ConfigPath
			ce.Suggestion = f.Suggestion
			if ce.Suggestion == "" {
				ce.Suggestion = "run 'conduit pipelines validate " + validatePath + "' for the full finding"
			}
			return Plan{}, ce
		}
		ce := conduiterr.New(conduiterr.CodeInvalidArgument, "repair found no pipeline definition in "+validatePath)
		ce.Suggestion = "run 'conduit pipelines validate " + validatePath + "' to see why"
		return Plan{}, ce
	}
	if len(fr.Pipelines) > 1 {
		ce := conduiterr.New(conduiterr.CodeInvalidArgument,
			"repair requires exactly one pipeline definition per file (found "+strconv.Itoa(len(fr.Pipelines))+")")
		ce.Suggestion = "split the file into one pipeline per file — repair, like deploy/apply, edits a single pipeline document"
		return Plan{}, ce
	}

	doc, err := parseDoc(raw)
	if err != nil {
		return Plan{}, err
	}
	if _, ok := singlePipelineNode(doc); !ok {
		ce := conduiterr.New(conduiterr.CodeInvalidArgument,
			"repair only supports a version-2.x pipeline config file with a single pipeline document")
		ce.Suggestion = `set "version" to "2.2" (or run 'conduit pipelines validate' first) — v1 (map-keyed) configs are not supported for automated editing`
		return Plan{}, ce
	}
	root := documentRoot(doc)

	var fixes []ProposedFix
	for _, f := range fr.Findings {
		if f.Fix == nil {
			continue
		}
		fixes = append(fixes, buildProposedFix(root, f))
	}
	sort.Slice(fixes, func(i, j int) bool { return fixes[i].ConfigPath < fixes[j].ConfigPath })

	return Plan{
		Path:  displayPath,
		Hash:  computeHash(raw, fixes),
		Fixes: fixes,
	}, nil
}

// buildProposedFix converts one Finding carrying a non-nil Fix into a
// ProposedFix: classified, with Before/After rendered from the current YAML
// tree when the target resolves (it may legitimately not — e.g. a producer
// bug, or content Collect was handed that doesn't quite match what the
// finding was computed against — in which case Before/After are left empty
// rather than guessed; Apply's own re-navigation is the actual correctness
// gate, not this rendering step).
//
// Secrets guard (design doc §9, failure mode 7): no v1 producer targets a
// connector's settings, so every fix this codebase actually emits today is
// classified FixClassSafe/FixClassRestart and Before/After is never a
// secret. But that is true only because of what the current producer set
// happens to do — it is not enforced anywhere else. This function is the
// one place ProposedFix.Before/After get populated at all, so it is where
// the invariant is enforced structurally: a FixClassDataPath fix NEVER gets
// a rendered Before/After, even if some future producer targeted
// `settings` directly, closing the gap before it could ever leak a
// credential into a --json/MCP response or a rendered diff.
func buildProposedFix(root *yaml.Node, f validate.Finding) ProposedFix {
	class := classify(f.Fix.ConfigPath)
	pf := ProposedFix{
		Code:       f.Code,
		Message:    f.Message,
		ConfigPath: f.Fix.ConfigPath,
		Fix:        *f.Fix,
		Class:      class,
	}
	if class == FixClassDataPath {
		return pf
	}

	segs := documentPath(f.Code, f.Fix.ConfigPath)
	if before, ok := valueAt(root, segs); ok {
		pf.Before = before
	}
	pf.After = f.Fix.Value
	return pf
}

// computeHash returns a deterministic digest of raw (the source bytes) and
// fixes (in Plan.Fixes' own sorted order) — see Plan.Hash's doc. Marshal
// cannot fail for ProposedFix (a plain struct of strings and a nested plain
// struct); a failure here would be a bug in this package, matching
// pkg/provisioning/plan.go's own computeHash panic rationale.
func computeHash(raw []byte, fixes []ProposedFix) string {
	h := sha256.New()
	h.Write(raw)
	b, err := json.Marshal(fixes)
	if err != nil {
		panic(cerrors.Errorf("repair: could not marshal fixes for hashing: %w", err))
	}
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}
