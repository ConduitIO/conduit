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
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

// conduiterrFix is a tiny test constructor for conduiterr.Fix, purely to
// keep the ProposedFix literals below on one line each.
func conduiterrFix(path, op, value string) conduiterr.Fix {
	return conduiterr.Fix{ConfigPath: path, Op: op, Value: value}
}

// errAsConduitErr reads back a *conduiterr.ConduitError's Code reason for
// assertions below.
func errAsConduitErr(err error) (string, bool) {
	ce, ok := conduiterr.Get(err)
	if !ok {
		return "", false
	}
	return ce.Code.Reason(), true
}

// TestApply_Rename_AppliesAndPreservesComments is AC-10 (re-validation
// passes), AC-12 (comments/unrelated formatting preserved — only the
// targeted lines change), and the core happy path: Collect -> Apply with
// the fresh hash applies every FixClassSafe fix by default.
func TestApply_Rename_AppliesAndPreservesComments(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	plan, err := Collect(ctx, "testdata/rename.yaml")
	is.NoErr(err)
	is.Equal(len(plan.Fixes), 2)

	res, err := Apply(ctx, ApplyInput{Path: "testdata/rename.yaml", Hash: plan.Hash})
	is.NoErr(err)
	is.Equal(len(res.Fixes), 2)
	for _, f := range res.Fixes {
		is.Equal(f.Outcome, FixOutcomeApplied)
	}

	// Both "type" fields became "plugin"; the file no longer has any "type:"
	// processor field, and every comment survives verbatim.
	is.True(!strings.Contains(res.Content, "type: base64"))
	is.True(strings.Contains(res.Content, "plugin: base64.encode"))
	is.True(strings.Contains(res.Content, "plugin: base64.decode"))
	is.True(strings.Contains(res.Content, "# deprecated field, should be renamed to \"plugin\""))
	is.True(strings.Contains(res.Content, "# trailing comment survives"))

	// Re-collecting the repaired content finds no more fixable findings.
	after, err := CollectContent(ctx, res.Content)
	is.NoErr(err)
	is.Equal(len(after.Fixes), 0)

	// Apply never writes to disk itself — the source file is untouched.
	orig, err := os.ReadFile("testdata/rename.yaml")
	is.NoErr(err)
	is.True(!strings.Contains(string(orig), "plugin: base64.encode"))
}

// TestApply_Workers_ClassifiedRestart_NotInDefaultSelection is AC-17: the
// default selection (Select empty) only ever applies FixClassSafe fixes —
// workers.yaml's fix is FixClassRestart, so a default Apply call finds
// nothing to do.
func TestApply_Workers_ClassifiedRestart_NotInDefaultSelection(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	plan, err := Collect(ctx, "testdata/workers.yaml")
	is.NoErr(err)
	is.Equal(len(plan.Fixes), 1)
	is.Equal(plan.Fixes[0].Class, FixClassRestart)

	_, err = Apply(ctx, ApplyInput{Path: "testdata/workers.yaml", Hash: plan.Hash})
	is.True(err != nil) // CodeNoFixesAvailable: nothing safe to apply by default

	// Explicitly selecting the restart fix DOES apply it.
	res, err := Apply(ctx, ApplyInput{Path: "testdata/workers.yaml", Hash: plan.Hash, Select: []string{plan.Fixes[0].ConfigPath}})
	is.NoErr(err)
	is.Equal(len(res.Fixes), 1)
	is.Equal(res.Fixes[0].Outcome, FixOutcomeApplied)
	is.True(strings.Contains(res.Content, "workers: 1"))
	is.True(!strings.Contains(res.Content, "workers: -3"))
}

// TestApply_StaleHash is AC-9: a hash that no longer matches the current
// file is refused with repair.plan_stale, nothing applied.
func TestApply_StaleHash(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	_, err := Apply(ctx, ApplyInput{Path: "testdata/rename.yaml", Hash: "not-a-real-hash"})
	is.True(err != nil)
	ce, ok := errAsConduitErr(err)
	is.True(ok)
	is.Equal(ce, CodePlanStale.Reason())
}

// TestApply_MissingHash is AC-8's analogue for the engine (the CLI's
// --plan-hash/--yes UX sits above this): Hash is required, always.
func TestApply_MissingHash(t *testing.T) {
	is := is.New(t)
	_, err := Apply(context.Background(), ApplyInput{Path: "testdata/rename.yaml"})
	is.True(err != nil)
}

// TestApply_NoFixesAvailable_OnCleanFile is AC-21: --apply against an
// already-clean file is repair.no_fixes_available, not silently OK:true
// with nothing in it.
func TestApply_NoFixesAvailable_OnCleanFile(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	plan, err := Collect(ctx, "testdata/clean.yaml")
	is.NoErr(err)

	_, err = Apply(ctx, ApplyInput{Path: "testdata/clean.yaml", Hash: plan.Hash})
	is.True(err != nil)
	ce, ok := errAsConduitErr(err)
	is.True(ok)
	is.Equal(ce, CodeNoFixesAvailable.Reason())
}

// TestApply_FixNoLongerApplies is AC-19: selecting a configPath the fresh
// plan doesn't actually have a fix for (e.g. it was hand-fixed between
// Collect and this Apply call using a DIFFERENT plan/hash than the one
// bound here — modeled directly by selecting an unknown path alongside a
// real one) skips that one with repair.fix_no_longer_applies while the
// other selected fix still applies.
func TestApply_FixNoLongerApplies_PartialSuccess(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	plan, err := Collect(ctx, "testdata/rename.yaml")
	is.NoErr(err)
	is.Equal(len(plan.Fixes), 2)

	res, err := Apply(ctx, ApplyInput{
		Path: "testdata/rename.yaml",
		Hash: plan.Hash,
		Select: []string{
			plan.Fixes[0].ConfigPath,
			"/pipelines/0/processors/99/type", // does not exist
		},
	})
	is.NoErr(err) // partial success is not a hard failure
	is.Equal(len(res.Fixes), 2)

	byPath := map[string]AppliedFix{}
	for _, f := range res.Fixes {
		byPath[f.ConfigPath] = f
	}
	is.Equal(byPath[plan.Fixes[0].ConfigPath].Outcome, FixOutcomeApplied)
	is.Equal(byPath["/pipelines/0/processors/99/type"].Outcome, FixOutcomeSkipped)
	is.Equal(byPath["/pipelines/0/processors/99/type"].Reason, CodeFixNoLongerApplies.Reason())
}

// TestApply_InvalidOp_AbortsEntireCall is AC-3: a structurally invalid fix
// (Op outside {set,remove,add}) aborts the WHOLE Apply call before any edit
// — never a partial write for this reason.
func TestApply_InvalidOp_AbortsEntireCall(t *testing.T) {
	is := is.New(t)

	root, err := parseDoc([]byte("a: b\n"))
	is.NoErr(err)
	_, err = applyOne(documentRoot(root), ProposedFix{
		Code:       "config.field_invalid",
		ConfigPath: "/a",
		Fix:        conduiterrFix("/a", "rename", "b"),
	})
	is.True(err != nil) // invalid op ("rename" is not set/remove/add)
}

// TestApply_UncoercibleNumericValue_AbortsEntireCall is AC-3's other half:
// a Fix targeting the v1 fix set's one numeric field (/workers) whose Value
// cannot be parsed as an integer aborts the whole call — a producer bug,
// never a silent bad write.
func TestApply_UncoercibleNumericValue_AbortsEntireCall(t *testing.T) {
	is := is.New(t)

	root, err := parseDoc([]byte("workers: 3\n"))
	is.NoErr(err)
	_, err = applyOne(documentRoot(root), ProposedFix{
		Code:       "config.field_invalid",
		ConfigPath: "/workers",
		Fix:        conduiterrFix("/workers", "set", "not-a-number"),
	})
	is.True(err != nil)
}

// TestRevalidate_CatchesAFixThatDidNotClearItsFinding is AC-10: the safety
// net that makes a bad producer visible in CI rather than in a user's
// pipeline. It calls the unexported revalidate directly against bytes that
// STILL have a finding at the ConfigPath a (simulated) "applied" fix
// claimed to clear — every real v1 producer is correct (see
// TestApply_Rename_AppliesAndPreservesComments etc.), so this is the only
// way to exercise the guard without hand-writing a deliberately-buggy
// producer.
func TestRevalidate_CatchesAFixThatDidNotClearItsFinding(t *testing.T) {
	is := is.New(t)

	// status is still invalid ("bogus") after the "fix" — revalidate must
	// refuse, since /status is still flagged.
	const stillBroken = `version: "2.2"
pipelines:
  - id: p1
    status: bogus
    connectors:
      - id: src
        type: source
        plugin: builtin:generator
`
	err := revalidate(context.Background(), []AppliedFix{
		{ConfigPath: "/status", Outcome: FixOutcomeApplied},
	}, []byte(stillBroken))
	is.True(err != nil)
}

// TestApply_AmbiguousFix proves repair never silently picks between two
// candidate fixes for the same ConfigPath (AC-20), by exercising
// selectFixes directly — the v1 producer set cannot itself generate a
// genuine collision, so this is tested at the unit level, same rationale
// as TestGateFix.
func TestApply_AmbiguousFix(t *testing.T) {
	is := is.New(t)

	plan := []ProposedFix{
		{ConfigPath: "/status", Class: FixClassSafe, Fix: conduiterrFix("/status", "set", "running")},
		{ConfigPath: "/status", Class: FixClassSafe, Fix: conduiterrFix("/status", "set", "stopped")},
	}
	_, err := selectFixes(plan, nil)
	is.True(err != nil)
	ce, ok := errAsConduitErr(err)
	is.True(ok)
	is.Equal(ce, CodeAmbiguousFix.Reason())
}

// The atomic-write behavior formerly tested here as WriteFileAtomic moved to
// pkg/foundation/atomicfile (see atomicfile_test.go's
// TestWriteFile_ReplacesContentWholesale /
// TestWriteFile_FailureLeavesOriginalIntact) once a second real call site —
// the connector registry's install manifest — justified promoting it out of
// this package.
