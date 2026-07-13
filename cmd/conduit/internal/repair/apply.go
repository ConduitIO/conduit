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
	"os"
	"path/filepath"
	"strconv"

	"github.com/conduitio/conduit/cmd/conduit/internal/validate"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/conduitio/yaml/v3"
)

// conduiterr.Fix.Op's closed enum (design doc §3): the applier accepts only
// these three values, never a free-form string.
const (
	opSet    = "set"
	opRemove = "remove"
	opAdd    = "add"
)

// FixOutcome is one selected fix's outcome after an Apply call.
type FixOutcome string

const (
	FixOutcomeApplied FixOutcome = "applied"
	FixOutcomeSkipped FixOutcome = "skipped"
)

// AppliedFix reports what happened to one selected fix.
type AppliedFix struct {
	ConfigPath string     `json:"configPath"`
	Outcome    FixOutcome `json:"outcome"`
	// Reason is set only when Outcome is FixOutcomeSkipped: the stable
	// conduiterr code reason explaining why (repair.data_path_fix_refused,
	// repair.fix_no_longer_applies).
	Reason string `json:"reason,omitempty"`
}

// ApplyInput is Apply's argument. Exactly one of Path/Content must be set —
// Path for the CLI (Apply reads and, on success, the CLI writes back to it
// atomically; Apply itself never writes to disk — see Apply's doc), Content
// for MCP (content-in, matching CollectContent/withTempConfigFile's rule
// elsewhere in this codebase: an MCP agent supplies pipeline config CONTENT,
// never a path on the server host).
type ApplyInput struct {
	Path    string
	Content string

	// Hash is required, from a prior Collect/CollectContent call against
	// the exact same bytes — no "skip the hash" escape hatch (design doc
	// §5.2, mirroring the deploy/apply tool's own stance).
	Hash string

	// Select is the list of ProposedFix.ConfigPath values to apply. Empty
	// means "every FixClassSafe fix in the plan" (AC-17's default) —
	// FixClassRestart and FixClassDataPath fixes are NEVER included in the
	// default selection, regardless of Escalate.
	Select []string

	// Escalate permits applying an explicitly Select-ed FixClassDataPath
	// fix — the human-only Tier-1 override (design doc §4.2). It must be
	// wired ONLY from the CLI's --escalate flag; the MCP repair_apply tool
	// input has no field that can set this (see design doc §5.2's
	// "deliberately no agent-settable escalate field" and AC-15's test).
	// An explicitly Select-ed data-path fix without Escalate comes back in
	// the result as FixOutcomeSkipped with Reason
	// CodeDataPathFixRefused.Reason() — Apply itself never hard-fails for
	// this alone (it is a per-fix policy skip, not a parse/hash failure);
	// it is the CLI shell's job (not this engine's) to treat "I explicitly
	// asked for this fix and it was refused" as the run's hard error
	// (AC-14) — see cmd/conduit/root/pipelines/repair.go. MCP repair_apply
	// surfaces the same skip as part of a normal, successful result
	// (AC-15), since it never sets Escalate and therefore never has a
	// "the human explicitly overrode this" case to enforce.
	Escalate bool
}

// Result is Apply's return value: the repaired content plus a per-fix
// applied/skipped report. Writing REPAIRED content to disk is the caller's
// choice (design doc §4.1) — Apply itself performs no file writes (AC-6):
// the CLI command atomically writes Content back to ApplyInput.Path (see
// WriteFileAtomic) only after Apply returns success; the MCP repair_apply
// tool returns Content directly and writes nothing.
type Result struct {
	Path    string       `json:"path,omitempty"`
	Content string       `json:"content"`
	Fixes   []AppliedFix `json:"fixes"`
}

// Apply re-collects the plan from the current bytes (Path or Content —
// exactly one of ApplyInput's two must be set), verifies the presented Hash
// still matches (else CodePlanStale, AC-9), resolves the selected fix set
// (default: every FixClassSafe fix, AC-17), and performs the edits on an
// in-memory YAML node tree — never touching a store or a running pipeline
// (AC-6, doc.go's central invariant).
//
// Each selected fix is applied independently, in ConfigPath order, against
// the SAME tree the previous fixes in this call already mutated — so a
// fix whose target the current tree no longer has (hand-edited away, a
// fix consumed by an earlier fix in this same batch, or the destination of
// a rename already occupied) is reported FixOutcomeSkipped with
// CodeFixNoLongerApplies, and every OTHER selected fix still applies
// (AC-19's partial-success contract). The write — via the caller — is
// all-or-nothing on the resulting bytes: there is exactly one call to
// marshalDoc, over the tree reflecting every fix that actually applied, so
// there is no such thing as a "half-written" fix.
//
// A structurally invalid fix — an Op outside {set, remove, add}, or a
// Value that cannot be coerced to its target field's known type (the
// v1 fix set's one non-string field, /workers) — aborts the ENTIRE call
// with an internal error and returns before any edit is even attempted
// (AC-3): that is a producer bug, not a runtime condition partial success
// can paper over.
//
// After every edit, Apply re-runs the same validate/lint engine over the
// repaired bytes and refuses to return success if any APPLIED fix's own
// ConfigPath is still flagged by a finding (AC-10) — the safety net that
// makes a bad producer visible in CI rather than in a user's pipeline.
func Apply(ctx context.Context, in ApplyInput) (Result, error) {
	raw, validatePath, displayPath, cleanup, err := resolveApplyInput(in)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()

	fresh, err := collect(ctx, validatePath, displayPath, raw)
	if err != nil {
		return Result{}, err
	}

	if in.Hash == "" {
		ce := conduiterr.New(conduiterr.CodeInvalidArgument, "hash is required (from a prior repair Collect call)")
		ce.Suggestion = "call repair's read step first, review the proposed fixes, then pass its hash to apply"
		return Result{}, ce
	}
	if fresh.Hash != in.Hash {
		ce := conduiterr.New(CodePlanStale,
			"repair plan is stale: the presented hash does not match the current plan hash; "+
				"the file changed since the plan was computed")
		ce.Suggestion = "re-run the repair read step to compute a fresh plan, review it, then apply its hash"
		return Result{}, ce
	}

	selected, err := selectFixes(fresh.Fixes, in.Select)
	if err != nil {
		return Result{}, err
	}
	if len(selected) == 0 {
		ce := conduiterr.New(CodeNoFixesAvailable, "no appliable fixes in the plan")
		ce.Suggestion = "nothing to repair — the config already passes validate for every machine-fixable finding"
		return Result{}, ce
	}

	doc, err := parseDoc(raw)
	if err != nil {
		return Result{}, err
	}
	root := documentRoot(doc)
	if root == nil {
		// Cannot happen: collect() above already required singlePipelineNode
		// to succeed against these exact bytes. Guard anyway rather than
		// panic on a nil deref below.
		return Result{}, conduiterr.New(conduiterr.CodeInternal, "repair: could not re-parse the pipeline config for editing")
	}

	report := make([]AppliedFix, 0, len(selected))
	for _, pf := range selected {
		if pf.Code == "" {
			// selectFixes's marker for "an explicitly selected ConfigPath
			// that has no fix in the fresh plan at all" — never a producer
			// fix, so it never reaches applyOne's op validation (which
			// would otherwise misreport this as an invalid-op producer
			// bug and abort the whole call).
			report = append(report, AppliedFix{ConfigPath: pf.ConfigPath, Outcome: FixOutcomeSkipped, Reason: CodeFixNoLongerApplies.Reason()})
			continue
		}

		if skip, reason := gateFix(pf, in.Escalate); skip {
			report = append(report, AppliedFix{ConfigPath: pf.ConfigPath, Outcome: FixOutcomeSkipped, Reason: reason})
			continue
		}

		applied, err := applyOne(root, pf)
		if err != nil {
			// AC-3: a structurally invalid fix aborts the whole call before
			// any write — never a partial or corrupted result.
			return Result{}, err
		}
		if !applied {
			report = append(report, AppliedFix{ConfigPath: pf.ConfigPath, Outcome: FixOutcomeSkipped, Reason: CodeFixNoLongerApplies.Reason()})
			continue
		}
		report = append(report, AppliedFix{ConfigPath: pf.ConfigPath, Outcome: FixOutcomeApplied})
	}

	out, err := marshalDoc(doc)
	if err != nil {
		return Result{}, err
	}

	if err := revalidate(ctx, report, out); err != nil {
		return Result{}, err
	}

	return Result{Path: displayPath, Content: string(out), Fixes: report}, nil
}

// resolveApplyInput validates ApplyInput's Path/Content exclusivity and
// returns the source bytes plus the path validate.RunWithOptions should run
// against (a temp file for Content). cleanup always removes anything
// resolveApplyInput created — call it unconditionally via defer even on a
// later error.
func resolveApplyInput(in ApplyInput) (raw []byte, validatePath, displayPath string, cleanup func(), err error) {
	noop := func() {}
	switch {
	case in.Path != "" && in.Content != "":
		return nil, "", "", noop, conduiterr.New(conduiterr.CodeInvalidArgument, "repair: exactly one of path or content must be set, not both")
	case in.Path != "":
		b, rerr := os.ReadFile(in.Path)
		if rerr != nil {
			ce := conduiterr.Wrap(conduiterr.CodeInvalidArgument, "could not read pipeline config file: "+rerr.Error(), rerr)
			ce.Suggestion = "check that the file exists and is readable"
			return nil, "", "", noop, ce
		}
		return b, in.Path, in.Path, noop, nil
	case in.Content != "":
		raw := []byte(in.Content)
		dir, derr := os.MkdirTemp("", "conduit-repair-*")
		if derr != nil {
			return nil, "", "", noop, conduiterr.Wrap(conduiterr.CodeInternal, "could not create a temporary directory for the pipeline config", derr)
		}
		path := filepath.Join(dir, "pipeline.yaml")
		if werr := os.WriteFile(path, raw, 0o600); werr != nil {
			_ = os.RemoveAll(dir)
			return nil, "", "", noop, conduiterr.Wrap(conduiterr.CodeInternal, "could not write the temporary pipeline config file", werr)
		}
		return raw, path, "", func() { _ = os.RemoveAll(dir) }, nil
	default:
		return nil, "", "", noop, conduiterr.New(conduiterr.CodeInvalidArgument, "repair: path or content is required")
	}
}

// selectFixes resolves ApplyInput.Select against plan (AC-17's default and
// AC-20's ambiguity guard). Empty select means every FixClassSafe fix.
// Duplicate ConfigPaths across the FIXES BEING APPLIED (not just within
// select) are refused outright with CodeAmbiguousFix — repair never
// silently picks one of two candidates targeting the same field.
func selectFixes(plan []ProposedFix, sel []string) ([]ProposedFix, error) {
	byPath := map[string][]ProposedFix{}
	for _, pf := range plan {
		byPath[pf.ConfigPath] = append(byPath[pf.ConfigPath], pf)
	}
	for path, group := range byPath {
		if len(group) > 1 {
			ce := conduiterr.New(CodeAmbiguousFix, "more than one candidate fix targets "+strconv.Quote(path))
			ce.ConfigPath = path
			ce.Suggestion = "select the exact fix by its full identity is not yet supported; this configPath has ambiguous fixes and cannot be auto-applied"
			return nil, ce
		}
	}

	if len(sel) == 0 {
		var out []ProposedFix
		for _, pf := range plan {
			if pf.Class == FixClassSafe {
				out = append(out, pf)
			}
		}
		return out, nil
	}

	out := make([]ProposedFix, 0, len(sel))
	for _, path := range sel {
		group, ok := byPath[path]
		if !ok || len(group) == 0 {
			// Not a known fix in the current plan at all — reported as a
			// skip (AC-19's "no longer applies" umbrella covers "was never
			// there"), not a hard failure of the whole call. ProposedFix{}
			// with a zero Code is this function's marker for "no fix
			// exists for this selection"; Apply's loop checks Code == ""
			// BEFORE calling applyOne, so this is never misread as a
			// producer emitting an invalid Op.
			out = append(out, ProposedFix{ConfigPath: path})
			continue
		}
		out = append(out, group[0])
	}
	return out, nil
}

// gateFix is Apply's Tier-1 policy check (design doc §4.2), factored out so
// it is directly unit-testable against synthetic ProposedFix values —
// nothing in the v1 producer set actually classifies FixClassDataPath (by
// design; see classify's doc), so exercising this gate end-to-end through
// Collect->Apply would need a producer this codebase deliberately does not
// have yet. skip is true iff pf must not be applied given escalate; reason
// is the stable code to report on the AppliedFix when skip is true.
func gateFix(pf ProposedFix, escalate bool) (skip bool, reason string) {
	if pf.Class == FixClassDataPath && !escalate {
		return true, CodeDataPathFixRefused.Reason()
	}
	return false, ""
}

// applyOne performs pf's edit against root. It returns (applied=false,
// err=nil) for any "not applicable right now" condition (AC-19's
// CodeFixNoLongerApplies umbrella: target missing, rename destination
// already occupied) and a non-nil err only for a structurally invalid fix
// (AC-3) — the distinction the caller uses to decide "skip this one, keep
// going" vs. "abort the whole Apply".
func applyOne(root *yaml.Node, pf ProposedFix) (applied bool, err error) {
	segs := documentPath(pf.Code, pf.Fix.ConfigPath)

	if pf.Code == config.CodeFieldRenamed.Reason() {
		parent, last, ok := navigateParent(root, segs)
		if !ok {
			return false, nil
		}
		return applyRename(parent, last, pf.Fix.Value), nil
	}

	switch pf.Fix.Op {
	case opSet, opRemove, opAdd:
	default:
		return false, conduiterr.New(conduiterr.CodeInternal,
			"repair: fix at "+pf.ConfigPath+" has an invalid op "+strconv.Quote(pf.Fix.Op)+" (want \"set\", \"remove\", or \"add\") — this is a producer bug")
	}

	numeric := isNumericPath(pf.Fix.ConfigPath)
	if numeric && pf.Fix.Op != opRemove {
		if _, cerr := strconv.Atoi(pf.Fix.Value); cerr != nil {
			return false, conduiterr.New(conduiterr.CodeInternal,
				"repair: fix at "+pf.ConfigPath+" has a value that cannot be coerced to the target field's integer type: "+strconv.Quote(pf.Fix.Value))
		}
	}

	parent, last, ok := navigateParent(root, segs)
	if !ok {
		return false, nil
	}

	switch pf.Fix.Op {
	case opSet:
		return applySet(parent, last, pf.Fix.Value, numeric), nil
	case opRemove:
		return applyRemove(parent, last), nil
	case opAdd:
		return applyAdd(parent, last, pf.Fix.Value, numeric), nil
	}
	return false, nil // unreachable — validated above
}

// revalidate re-runs the same validate/lint engine over the repaired bytes
// and fails closed (AC-10) if any APPLIED fix's own ConfigPath is still
// flagged by a finding — meaning the fix did not actually clear the problem
// it claimed to. This never runs against disk state the caller could see
// (a fresh temp file, cleaned up before returning) and never causes a
// partial write: Apply's caller only ever sees Content once this passes.
func revalidate(ctx context.Context, applied []AppliedFix, newRaw []byte) error {
	dir, err := os.MkdirTemp("", "conduit-repair-verify-*")
	if err != nil {
		return conduiterr.Wrap(conduiterr.CodeInternal, "could not create a temporary directory to re-validate the repaired config", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	path := filepath.Join(dir, "pipeline.yaml")
	if err := os.WriteFile(path, newRaw, 0o600); err != nil {
		return conduiterr.Wrap(conduiterr.CodeInternal, "could not write the repaired config for re-validation", err)
	}

	report, err := validate.RunWithOptions(ctx, path, validate.Options{Warnings: true})
	if err != nil {
		return conduiterr.Wrap(conduiterr.CodeInternal, "could not re-validate the repaired config", err)
	}
	if len(report.Files) != 1 {
		return conduiterr.New(conduiterr.CodeInternal, "repair: re-validation produced an unexpected report shape")
	}

	stillFlagged := make(map[string]bool, len(report.Files[0].Findings))
	for _, f := range report.Files[0].Findings {
		stillFlagged[f.ConfigPath] = true
	}
	for _, a := range applied {
		if a.Outcome != FixOutcomeApplied {
			continue
		}
		if stillFlagged[a.ConfigPath] {
			return conduiterr.New(conduiterr.CodeInternal,
				"repair: the fix applied at "+a.ConfigPath+" did not clear its finding on re-validation; nothing was written")
		}
	}
	return nil
}

// WriteFileAtomic writes content to path via a temp file in the same
// directory plus rename — Invariant 5's "torn writes on crash must be
// impossible" applied to a config file: a crash between the temp write and
// the rename leaves the original path untouched (AC-11); a crash after the
// rename leaves the new content, fully written, never a half-written file.
// This is the CLI repair command's write step (see ApplyInput/Result's
// doc — Apply itself never writes to disk); MCP repair_apply never calls
// this at all.
func WriteFileAtomic(path string, content []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".repair-*.tmp")
	if err != nil {
		return cerrors.Errorf("could not create a temp file to write %q atomically: %w", path, err)
	}
	tmpPath := tmp.Name()
	// Always attempt to remove the temp file; once Rename succeeds below
	// this is a no-op (the path no longer exists under tmpPath).
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return cerrors.Errorf("could not write to temp file for %q: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return cerrors.Errorf("could not sync temp file for %q: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return cerrors.Errorf("could not close temp file for %q: %w", path, err)
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return cerrors.Errorf("could not set permissions on temp file for %q: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return cerrors.Errorf("could not atomically replace %q: %w", path, err)
	}
	return nil
}
