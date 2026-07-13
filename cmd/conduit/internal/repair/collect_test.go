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
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/internal/validate"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/matryer/is"
)

// TestCollect_Rename is AC-1's golden test for the v1 starter set's item #1
// (deprecated processor "type" -> "plugin", both pipeline-level and
// connector-nested): the Fix is correct, classified safe, and identical
// whether the fix comes from the pipeline-level or the connector-nested
// processor.
func TestCollect_Rename(t *testing.T) {
	is := is.New(t)

	plan, err := Collect(context.Background(), "testdata/rename.yaml")
	is.NoErr(err)
	is.Equal(len(plan.Fixes), 2) // pipeline-level + connector-nested processor

	for _, f := range plan.Fixes {
		is.Equal(f.Code, "config.field_renamed")
		is.Equal(f.Class, FixClassSafe)
		is.Equal(f.Fix.Op, "remove")
		is.Equal(f.Fix.Value, "plugin")
		is.True(strings.HasSuffix(f.Fix.ConfigPath, "/type"))
		is.Equal(f.ConfigPath, f.Fix.ConfigPath)
	}
}

// TestCollect_Status is AC-1's golden test for item #2: an unambiguous
// case/whitespace-normalizable /status value gets a Fix; a genuinely
// ambiguous one does not (§6: "otherwise no fix").
func TestCollect_Status(t *testing.T) {
	is := is.New(t)

	plan, err := Collect(context.Background(), "testdata/status.yaml")
	is.NoErr(err)
	is.Equal(len(plan.Fixes), 1)
	f := plan.Fixes[0]
	is.Equal(f.Code, "config.field_invalid")
	is.Equal(f.ConfigPath, "/status")
	is.Equal(f.Class, FixClassSafe)
	is.Equal(f.Fix.Op, "set")
	is.Equal(f.Fix.Value, "running")

	plan, err = Collect(context.Background(), "testdata/status-ambiguous.yaml")
	is.NoErr(err)
	is.Equal(len(plan.Fixes), 0) // "bogus" has no deterministic canonical target
}

// TestCollect_Workers is AC-1's golden test for item #3: a negative
// /workers gets Fix{Op:"set", Value:"1"}, classified restart (not safe).
func TestCollect_Workers(t *testing.T) {
	is := is.New(t)

	plan, err := Collect(context.Background(), "testdata/workers.yaml")
	is.NoErr(err)
	is.Equal(len(plan.Fixes), 1)
	f := plan.Fixes[0]
	is.Equal(f.Code, "config.field_invalid")
	is.True(strings.HasSuffix(f.ConfigPath, "/workers"))
	is.Equal(f.Class, FixClassRestart)
	is.Equal(f.Fix.Op, "set")
	is.Equal(f.Fix.Value, "1")
}

// TestCollect_Description is AC-1's golden test for item #4: an over-long
// /description gets Fix{Op:"set", Value:<truncated>}, classified safe. The
// fixture is generated in-test (8192 is pipeline.DescriptionLengthLimit —
// too long to hand-write as a static YAML fixture).
func TestCollect_Description(t *testing.T) {
	is := is.New(t)

	long := strings.Repeat("d", pipeline.DescriptionLengthLimit+100)
	src := "version: \"2.2\"\npipelines:\n  - id: repair-demo\n    status: running\n    description: \"" + long + "\"\n" +
		"    connectors:\n      - id: src\n        type: source\n        plugin: builtin:generator\n"

	dir := t.TempDir()
	path := filepath.Join(dir, "pipeline.yaml")
	is.NoErr(os.WriteFile(path, []byte(src), 0o600))

	plan, err := Collect(context.Background(), path)
	is.NoErr(err)
	is.Equal(len(plan.Fixes), 1)
	f := plan.Fixes[0]
	is.Equal(f.Code, "config.field_too_long")
	is.Equal(f.ConfigPath, "/description")
	is.Equal(f.Class, FixClassSafe)
	is.Equal(f.Fix.Op, "set")
	is.Equal(len(f.Fix.Value), pipeline.DescriptionLengthLimit)
	is.Equal(f.Fix.Value, long[:pipeline.DescriptionLengthLimit])
}

// TestCollect_Clean is AC-21's read-mode half: a file with no fixable
// findings returns a valid, empty-Fixes Plan and a nil error — not an
// error.
func TestCollect_Clean(t *testing.T) {
	is := is.New(t)

	plan, err := Collect(context.Background(), "testdata/clean.yaml")
	is.NoErr(err)
	is.Equal(len(plan.Fixes), 0)
	is.True(plan.Hash != "")
}

// TestCollect_CollectContent_Identical is AC-4: Collect and CollectContent
// return byte-identical Plan.Fixes/Plan.Hash for the same config supplied
// as a file vs. as content.
func TestCollect_CollectContent_Identical(t *testing.T) {
	is := is.New(t)

	raw, err := os.ReadFile("testdata/rename.yaml")
	is.NoErr(err)

	fromFile, err := Collect(context.Background(), "testdata/rename.yaml")
	is.NoErr(err)

	fromContent, err := CollectContent(context.Background(), string(raw))
	is.NoErr(err)

	is.Equal(fromFile.Hash, fromContent.Hash)
	is.Equal(len(fromFile.Fixes), len(fromContent.Fixes))
	for i := range fromFile.Fixes {
		is.Equal(fromFile.Fixes[i], fromContent.Fixes[i])
	}
	is.Equal(fromContent.Path, "") // never echoes a server-side path back
}

// TestCollect_RejectsMultiPipeline is the v1 scope-boundary guard (doc.go):
// a file with more than one pipeline document is refused with a clear,
// coded error rather than repair silently picking one.
func TestCollect_RejectsMultiPipeline(t *testing.T) {
	is := is.New(t)

	const src = `version: "2.2"
pipelines:
  - id: p1
    status: running
  - id: p2
    status: running
`
	dir := t.TempDir()
	path := filepath.Join(dir, "pipeline.yaml")
	is.NoErr(os.WriteFile(path, []byte(src), 0o600))

	_, err := Collect(context.Background(), path)
	is.True(err != nil)
}

// TestCollect_RejectsMultiDocument is the regression test for review B1: a
// multi-document ('---'-separated) file whose first document holds exactly one
// pipeline and whose trailing documents contribute zero pipelines slips past the
// pipeline-count guard (len(Pipelines)==1). Without the document-level check in
// parseDoc, marshalDoc re-emits only the first document, so --apply would
// silently drop the rest — config data loss. Collect must reject it.
func TestCollect_RejectsMultiDocument(t *testing.T) {
	is := is.New(t)

	const src = `version: "2.2"
pipelines:
  - id: p1
    status: RUNNING
---
version: "2.2"
pipelines: []
`
	dir := t.TempDir()
	path := filepath.Join(dir, "pipeline.yaml")
	is.NoErr(os.WriteFile(path, []byte(src), 0o600))

	_, err := Collect(context.Background(), path)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.True(strings.Contains(ce.Message, "single YAML document"))
}

// TestCollect_UnparseableFile_SurfacesParseError is a regression test
// (adversarial self-review finding): a file that fails to parse entirely
// resolves to zero pipelines, which — before this fix — collect()
// misreported as "repair requires exactly one pipeline definition per file
// (found 0)", masking the actual YAML syntax error. It must instead surface
// the real parse finding's own message.
func TestCollect_UnparseableFile_SurfacesParseError(t *testing.T) {
	is := is.New(t)

	const broken = "version: \"2.2\"\npipelines: [this is not valid yaml structure for pipelines\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "pipeline.yaml")
	is.NoErr(os.WriteFile(path, []byte(broken), 0o600))

	_, err := Collect(context.Background(), path)
	is.True(err != nil)
	is.True(!strings.Contains(err.Error(), "found 0")) // the misleading message must be gone
}

// TestBuildProposedFix_DataPathFix_NeverRendersBeforeAfter is the secrets
// guard from buildProposedFix's doc (design doc §9, failure mode 7): a
// FixClassDataPath fix never gets a rendered Before/After, even though its
// raw Fix is still carried through. No real v1 producer emits a data_path
// fix (see classify's doc), so this is exercised directly against a
// synthetic Finding, the same rationale as TestGateFix.
func TestBuildProposedFix_DataPathFix_NeverRendersBeforeAfter(t *testing.T) {
	is := is.New(t)

	doc, err := parseDoc([]byte("pipelines:\n  - connectors:\n      - settings:\n          apiKey: super-secret\n"))
	is.NoErr(err)
	root := documentRoot(doc)

	pf := buildProposedFix(root, validate.Finding{
		Code:       "config.field_invalid",
		Message:    "hypothetical future finding",
		ConfigPath: "/connectors/0/settings/apiKey",
		Fix:        &conduiterr.Fix{ConfigPath: "/connectors/0/settings/apiKey", Op: "set", Value: "super-secret-replacement"},
	})

	is.Equal(pf.Class, FixClassDataPath)
	is.Equal(pf.Before, "")
	is.Equal(pf.After, "")
	// The raw Fix itself (not a rendered diff) is still carried through —
	// Apply needs it; it's the human/--json diff surface this guards.
	is.Equal(pf.Fix.Value, "super-secret-replacement")
}

// TestCollect_RejectsV1Config is the v1 scope-boundary guard for the
// map-keyed v1 config format (doc.go: "v1 configs are not supported for
// automated editing").
func TestCollect_RejectsV1Config(t *testing.T) {
	is := is.New(t)

	const src = `version: "1.1"
pipelines:
  p1:
    status: running
`
	dir := t.TempDir()
	path := filepath.Join(dir, "pipeline.yaml")
	is.NoErr(os.WriteFile(path, []byte(src), 0o600))

	_, err := Collect(context.Background(), path)
	is.True(err != nil)
}
