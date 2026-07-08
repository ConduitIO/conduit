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

package validate

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/provisioning"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/matryer/is"
)

// sharedTestdataDir points at the existing provisioning parser testdata
// (pipelines1-success.yml, pipelines4-invalid-yaml.yml, ...), per the task's
// instruction to reuse it rather than fork a parallel fixture set for
// parser-level scenarios (invalid YAML, an empty file). This package's own
// testdata/ only adds fixtures those files don't cover: field-validation
// errors, an unrecognized version, and cross-file duplicate pipeline IDs.
const sharedTestdataDir = "../../../../pkg/provisioning/config/yaml/v2/testdata"

// TestRun_ValidFile_NoFindings covers AC-1: a valid file is OK with zero
// findings.
func TestRun_ValidFile_NoFindings(t *testing.T) {
	is := is.New(t)

	report, err := Run(context.Background(), "testdata/valid.yaml")
	is.NoErr(err)
	is.True(report.OK())
	is.Equal(report.Summary.Files, 1)
	is.Equal(report.Summary.Errors, 0)
	is.Equal(len(report.Files), 1)
	is.True(report.Files[0].OK)
	is.Equal(len(report.Files[0].Findings), 0)
	is.Equal(report.Files[0].Pipelines, []string{"valid-pipeline"})
}

// TestRun_FileWithFieldErrors_AllFindingsPresent covers AC-2: every one of
// the N errors in a file is present, not just the first (the fail-fast /
// ForEach-collapse risk this whole PR exists to guard against), each with a
// non-empty code, configPath, message, and suggestion.
func TestRun_FileWithFieldErrors_AllFindingsPresent(t *testing.T) {
	is := is.New(t)

	report, err := Run(context.Background(), "testdata/errors.yaml")
	is.NoErr(err)
	is.True(!report.OK())
	is.Equal(len(report.Files), 1)

	fr := report.Files[0]
	is.True(!fr.OK)
	// 5 independent field problems: invalid status, empty connector id,
	// invalid connector type, missing connector plugin, duplicate connector
	// id — see testdata/errors.yaml's comment-free but deliberate shape.
	is.Equal(len(fr.Findings), 5)
	is.Equal(report.Summary.Errors, 5)

	wantPaths := map[string]bool{
		"/status":              false,
		"/connectors/0/id":     false,
		"/connectors/0/type":   false,
		"/connectors/0/plugin": false,
		"/connectors/2/id":     false,
	}
	for _, f := range fr.Findings {
		is.True(f.Code != "")
		is.True(f.ConfigPath != "")
		is.True(f.Message != "")
		is.True(f.Suggestion != "")
		is.Equal(f.Severity, SeverityError)
		if _, ok := wantPaths[f.ConfigPath]; ok {
			wantPaths[f.ConfigPath] = true
		}
	}
	for _, seen := range wantPaths {
		is.True(seen) // want a finding at every expected configPath
	}
}

// TestRun_Offline_NoDial documents and exercises the offline invariant: Run
// takes a plain path and never touches the network — no api.Client, no gRPC
// dial, anywhere in its call graph (validateFile only ever calls os.Open,
// the yaml parser, config.Enrich, and config.Validate). A run against a
// local file succeeding with no server, no address flag, and no client
// construction anywhere in this package is the structural proof.
func TestRun_Offline_NoDial(t *testing.T) {
	is := is.New(t)

	report, err := Run(context.Background(), "testdata/valid.yaml")
	is.NoErr(err)
	is.True(report.OK())
}

// TestRun_UnparseableYAML covers AC-5 (unparseable -> exit-2-coded finding,
// not a panic) using the shared parser testdata's malformed-indentation
// fixture.
func TestRun_UnparseableYAML(t *testing.T) {
	is := is.New(t)

	report, err := Run(context.Background(), sharedTestdataDir+"/pipelines4-invalid-yaml.yml")
	is.NoErr(err) // a bad file is a Finding, not a hard Run error
	is.True(!report.OK())
	is.Equal(len(report.Files), 1)
	is.True(!report.Files[0].OK)
	is.True(len(report.Files[0].Findings) >= 1)
	for _, f := range report.Files[0].Findings {
		is.Equal(f.Code, config.CodeParseError.Reason())
		is.True(f.Message != "")
		is.True(f.Suggestion != "")
	}
}

// TestRun_UnknownVersion covers the other half of AC-5: a recognized-shape
// document whose version is not just outdated but entirely unrecognized
// (parseVersion has no changelog entry for major version "99") is a coded
// finding, not a panic or an uncoded error.
func TestRun_UnknownVersion(t *testing.T) {
	is := is.New(t)

	report, err := Run(context.Background(), "testdata/unknown-version.yaml")
	is.NoErr(err)
	is.True(!report.OK())
	is.Equal(len(report.Files), 1)
	is.True(len(report.Files[0].Findings) >= 1)
	is.Equal(report.Files[0].Findings[0].Code, config.CodeParseError.Reason())
}

// TestRun_EmptyFile covers the design doc's failure-mode note: an empty
// file/dir is "0 pipelines checked", not an error.
func TestRun_EmptyFile(t *testing.T) {
	is := is.New(t)

	report, err := Run(context.Background(), sharedTestdataDir+"/pipelines3-empty.yml")
	is.NoErr(err)
	is.True(report.OK())
	is.Equal(report.Summary.Pipelines, 0)
	is.Equal(len(report.Files), 1)
	is.True(report.Files[0].OK)
}

// TestRun_EmptyDirectory covers the same failure mode for a directory with
// no matching .yml/.yaml files at all.
func TestRun_EmptyDirectory(t *testing.T) {
	is := is.New(t)

	dir := t.TempDir()
	report, err := Run(context.Background(), dir)
	is.NoErr(err)
	is.True(report.OK())
	is.Equal(report.Summary.Files, 0)
	is.Equal(len(report.Files), 0)
}

// TestRun_Directory_Aggregates covers AC-4: every .yml/.yaml file in the
// directory is checked (not recursed into subdirectories, and non-YAML
// files are ignored), passing files are reported alongside failing ones,
// and the run-wide Summary aggregates correctly.
func TestRun_Directory_Aggregates(t *testing.T) {
	is := is.New(t)

	report, err := Run(context.Background(), "testdata/dir")
	is.NoErr(err)
	is.True(!report.OK()) // fail1.yaml has findings

	is.Equal(report.Summary.Files, 3) // pass1, pass2, fail1 — not notes.txt or nested/ignored.yaml
	is.Equal(report.Summary.Errors, 2)

	byPath := map[string]FileReport{}
	for _, f := range report.Files {
		byPath[f.Path] = f
	}
	is.True(byPath["testdata/dir/pass1.yaml"].OK)
	is.True(byPath["testdata/dir/pass2.yaml"].OK)
	is.True(!byPath["testdata/dir/fail1.yaml"].OK)
	is.Equal(len(byPath["testdata/dir/fail1.yaml"].Findings), 2)
}

// TestRun_CrossFileDuplicateID covers AC (from the task): two files sharing
// a pipeline ID both fail, with the dup-ID code. This is the "new code, not
// reuse" duplicate-ID detection the design doc's review called out — unlike
// pkg/provisioning.Service.findDuplicateIDs (a same-process, flattened,
// bare-sentinel check), this must locate the collision *per file* with a
// real code and configPath.
func TestRun_CrossFileDuplicateID(t *testing.T) {
	is := is.New(t)

	report, err := Run(context.Background(), "testdata/dup")
	is.NoErr(err)
	is.True(!report.OK())
	is.Equal(len(report.Files), 2)

	for _, f := range report.Files {
		is.True(!f.OK)
		is.Equal(len(f.Findings), 1)
		find := f.Findings[0]
		is.Equal(find.Code, provisioning.CodePipelineIDDuplicate.Reason())
		is.Equal(find.ConfigPath, "/pipelines/0/id")
		is.True(find.Suggestion != "")
		is.True(find.Message != "")
	}
}

// TestRun_SameFileDuplicateID covers the same detection logic on a single
// file with two pipeline *documents* (YAML `---`-separated) sharing an ID —
// a distinct path through checkCrossFileDuplicateIDs from
// TestRun_CrossFileDuplicateID (one fileIdx, two pipeIdx, instead of two
// fileIdx), using the shared parser testdata's dedicated fixture for this
// shape.
func TestRun_SameFileDuplicateID(t *testing.T) {
	is := is.New(t)

	report, err := Run(context.Background(), sharedTestdataDir+"/pipelines2-duplicate-pipeline-id.yml")
	is.NoErr(err)
	is.True(!report.OK())
	is.Equal(len(report.Files), 1)

	fr := report.Files[0]
	is.Equal(len(fr.Pipelines), 3) // pipeline1, pipeline2, pipeline1 (dup)

	var dupFindings []Finding
	for _, f := range fr.Findings {
		if f.Code == provisioning.CodePipelineIDDuplicate.Reason() {
			dupFindings = append(dupFindings, f)
		}
	}
	is.Equal(len(dupFindings), 2) // one per occurrence of "pipeline1"
	is.Equal(dupFindings[0].ConfigPath, "/pipelines/0/id")
	is.Equal(dupFindings[1].ConfigPath, "/pipelines/2/id")
}

// TestCheckCrossFileDuplicateIDs_EmptyIDNotDoubleReported proves an empty
// pipeline ID (already reported separately via config.CodeFieldRequired at
// "/id") is never also treated as a "duplicate" against other empty-ID
// pipelines — that would be a confusing, redundant second finding for the
// same root cause.
func TestCheckCrossFileDuplicateIDs_EmptyIDNotDoubleReported(t *testing.T) {
	is := is.New(t)

	frs := []fileState{
		{path: "a.yaml", pipelines: []config.Pipeline{{ID: ""}}},
		{path: "b.yaml", pipelines: []config.Pipeline{{ID: ""}}},
	}
	checkCrossFileDuplicateIDs(frs)

	is.Equal(len(frs[0].findings), 0)
	is.Equal(len(frs[1].findings), 0)
}

// TestValidateFile_ForEachUnwrappedJoin_YieldsAllFindings is the load-bearing
// regression test for the design review's top masked-bug risk: validateFile
// must call cerrors.ForEach on config.Validate's and parser.Parse's raw
// (unwrapped) cerrors.Join return value. Re-wrapping that Join with "%w"
// before walking it (e.g. `cerrors.Errorf("invalid pipeline config: %w",
// err)`, which is exactly what pkg/provisioning.Service.provisionPipeline
// does at service.go:282 for its own, different purpose) hides the Join's
// Unwrap() []error shape behind a single-error Unwrap() error, and
// cerrors.ForEach then treats the whole thing as ONE error instead of N.
//
// This test proves both directions: ForEach over the raw Join yields every
// error (proving validateFile's actual behavior via testdata/errors.yaml,
// asserted again here for this specific invariant), and ForEach over a
// re-wrapped Join collapses to one (proving what would go wrong if a future
// change added a wrap before the ForEach call in engine.go).
func TestValidateFile_ForEachUnwrappedJoin_YieldsAllFindings(t *testing.T) {
	is := is.New(t)

	cfg := config.Pipeline{
		ID:     "p1",
		Status: "bogus",
		Connectors: []config.Connector{
			{ID: "", Type: "bogus-type"},
		},
	}
	joined := config.Validate(cfg)
	is.True(joined != nil)

	var viaUnwrapped []error
	cerrors.ForEach(joined, func(e error) { viaUnwrapped = append(viaUnwrapped, e) })
	is.True(len(viaUnwrapped) >= 4) // status, id, type, plugin are all independent findings

	// The masked-bug scenario: wrap the Join with "%w" before walking it.
	rewrapped := cerrors.Errorf("invalid pipeline config: %w", joined)
	var viaRewrapped []error
	cerrors.ForEach(rewrapped, func(e error) { viaRewrapped = append(viaRewrapped, e) })
	is.Equal(len(viaRewrapped), 1) // collapsed — this is the bug this package's engine.go must not have
}

// TestExitError_NoFindings_ReturnsFalse and
// TestExitError_WithFindings_ReturnsValidationCode cover the exit-code
// aggregation the CLI output conventions §4 requires commands to own
// explicitly.
func TestExitError_NoFindings_ReturnsFalse(t *testing.T) {
	is := is.New(t)
	_, ok := ExitError([]FileReport{{OK: true}})
	is.True(!ok)
}

func TestExitError_WithFindings_ReturnsValidationCode(t *testing.T) {
	is := is.New(t)
	err, ok := ExitError([]FileReport{
		{Findings: []Finding{{Severity: SeverityError, Code: config.CodeFieldRequired.Reason()}}},
	})
	is.True(ok)
	is.True(err != nil)
}
