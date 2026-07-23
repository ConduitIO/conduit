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

// Tests for `conduit pipelines init --template` (the vendored template
// gallery, template_gallery.go). These map directly onto
// docs/design-documents/20260723-templates-gallery.md §6's
// acceptance criteria:
//
//   - AC-1: every template scaffolds a working, parseable pipeline —
//     TestGalleryTemplates_ScaffoldParseableYAML.
//   - AC-2: --template list --json conforms to the Family A envelope and
//     enumerates all four with non-empty descriptions —
//     TestTemplateList_JSON_EnvelopeShape.
//   - AC-3 (end-to-end infra assertions) lives in
//     template_gallery_e2e_test.go, not here.
//   - AC-5: zero templates require a non-built-in connector —
//     TestGalleryCatalog_Valid plus the synthetic-catalog rejection tests.
//   - AC-6: a version-pinned mismatch refuses cleanly —
//     TestValidateGalleryTemplateSettings_StaleFixture (synthetic) and
//     TestValidateGalleryTemplateSettings_RealCatalog (regression guard: the
//     real, shipped catalog never mismatches the connectors built into this
//     binary).
//   - AC-7: existing-file handling reuses --force/--dry-run —
//     TestInitCommand_TemplateScaffold_*.
package pipelines

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal/testutils"
	"github.com/conduitio/conduit/pkg/conduit/exitcode"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	yamlparser "github.com/conduitio/conduit/pkg/provisioning/config/yaml"
	"github.com/conduitio/ecdysis"
	json "github.com/goccy/go-json"
	"github.com/matryer/is"
)

// TestGalleryCatalog_Valid proves the real, shipped catalog satisfies every
// structural invariant validateGalleryCatalog enforces — redundant with the
// package-init panic in the narrow sense that a broken catalog would already
// have failed every other test in this package, but this test names the
// specific invariants (AC-5's "zero non-built-in connectors", the "list"
// reservation) so a future regression fails with a pointed message instead
// of "some test in this package panicked at init".
func TestGalleryCatalog_Valid(t *testing.T) {
	is := is.New(t)

	is.NoErr(validateGalleryCatalog(galleryTemplates))
	is.Equal(len(galleryTemplates), 4)

	wantNames := map[string]bool{
		"generator-log": true, "generator-file": true,
		"postgres-s3": true, "postgres-cdc-kafka": true,
	}
	for _, tmpl := range galleryTemplates {
		is.True(wantNames[tmpl.Name])
		is.True(tmpl.Name != templateListSentinel)
		is.True(isBuiltinConnectorName(tmpl.Source))
		is.True(isBuiltinConnectorName(tmpl.Destination))
		is.True(strings.TrimSpace(tmpl.Description) != "")
		is.True(strings.TrimSpace(tmpl.DeliverySemantics) != "")
		is.True(strings.TrimSpace(tmpl.YAML) != "")
	}
}

// TestValidateGalleryCatalog_RejectsReservedName is the edge-case table's
// "a template must never be named 'list'" row (
// docs/design-documents/20260723-templates-gallery.md §7): a
// hand-built catalog containing that name must fail validation rather than
// silently colliding with the --template list sentinel at runtime.
func TestValidateGalleryCatalog_RejectsReservedName(t *testing.T) {
	is := is.New(t)

	bad := []GalleryTemplate{{
		Name: "list", Description: "x", DeliverySemantics: "x",
		Source: "generator", Destination: "log", YAML: "version: \"2.2\"\n",
	}}
	err := validateGalleryCatalog(bad)
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "reserved"))
}

// TestValidateGalleryCatalog_RejectsNonBuiltinConnector is AC-5 enforced
// against a synthetic catalog: a template naming a connector outside
// builtin.DefaultBuiltinConnectors must fail validation, so a future
// template addition can't silently reintroduce the manual-download cliff
// (docs/design-documents/20260723-templates-gallery.md §7's corresponding edge-case row).
func TestValidateGalleryCatalog_RejectsNonBuiltinConnector(t *testing.T) {
	is := is.New(t)

	bad := []GalleryTemplate{{
		Name: "not-builtin", Description: "x", DeliverySemantics: "x",
		Source: "generator", Destination: "not-a-real-connector", YAML: "version: \"2.2\"\n",
	}}
	err := validateGalleryCatalog(bad)
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "not-a-real-connector"))
}

// TestValidateGalleryCatalog_RejectsDuplicateName and
// TestValidateGalleryCatalog_RejectsEmptyYAML round out the structural
// checks with their own pointed assertions.
func TestValidateGalleryCatalog_RejectsDuplicateName(t *testing.T) {
	is := is.New(t)

	dup := []GalleryTemplate{
		{Name: "dup", Description: "x", DeliverySemantics: "x", Source: "generator", Destination: "log", YAML: "v\n"},
		{Name: "dup", Description: "y", DeliverySemantics: "y", Source: "generator", Destination: "file", YAML: "v\n"},
	}
	err := validateGalleryCatalog(dup)
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "duplicate"))
}

func TestValidateGalleryCatalog_RejectsEmptyYAML(t *testing.T) {
	is := is.New(t)

	bad := []GalleryTemplate{{
		Name: "empty-yaml", Description: "x", DeliverySemantics: "x",
		Source: "generator", Destination: "log", YAML: "   ",
	}}
	err := validateGalleryCatalog(bad)
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "empty embedded YAML"))
}

// TestValidateGalleryTemplateSettings_RealCatalog is AC-6's regression
// guard: every REAL, shipped template's settings must resolve cleanly
// against this build's actual connector parameter specs. Unlike a one-off
// "intentionally staled fixture" test, this runs live against the current
// catalog on every CI run, so a template that drifts from a connector
// upgrade (e.g. a renamed parameter) fails here immediately instead of only
// being caught by the end-to-end CI job.
func TestValidateGalleryTemplateSettings_RealCatalog(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	for _, tmpl := range galleryTemplates {
		err := validateGalleryTemplateSettings(ctx, tmpl)
		if err != nil {
			t.Errorf("template %q: %v", tmpl.Name, err)
		}
		is.True(err == nil)
	}
}

// TestValidateGalleryTemplateSettings_StaleFixture is AC-6's synthetic case:
// a template pinning a connector setting key that doesn't exist in this
// build's connector spec must refuse with CodeTemplateVersionMismatch, not
// silently pass through to a pipeline that would only fail later, far away,
// at `conduit run`.
func TestValidateGalleryTemplateSettings_StaleFixture(t *testing.T) {
	is := is.New(t)

	stale := GalleryTemplate{
		Name: "stale", Description: "x", DeliverySemantics: "x",
		Source: "generator", Destination: "log",
		YAML: `version: "2.2"
pipelines:
  - id: stale
    status: running
    name: stale
    connectors:
      - id: src
        type: source
        plugin: "builtin:generator"
        settings:
          format.type: structured
          # this parameter never existed on the generator connector, and
          # (unlike a concrete key under the wildcard "format.options.*")
          # doesn't match any wildcard-suffixed spec key either — a
          # stand-in for a renamed/removed parameter after a connector
          # version bump.
          totallyBogusParameterThatWasRemoved: "true"
      - id: dst
        type: destination
        plugin: "builtin:log"
`,
	}

	err := validateGalleryTemplateSettings(context.Background(), stale)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodeTemplateVersionMismatch.Reason())
}

// TestValidateGalleryTemplateSettings_UnknownConnector covers the sibling
// case: a template referencing a connector plugin that doesn't exist at all
// in this build (as opposed to an unrecognized parameter on a real
// connector).
func TestValidateGalleryTemplateSettings_UnknownConnector(t *testing.T) {
	is := is.New(t)

	bad := GalleryTemplate{
		Name: "unknown-conn", Description: "x", DeliverySemantics: "x",
		Source: "generator", Destination: "log",
		YAML: `version: "2.2"
pipelines:
  - id: unknown-conn
    status: running
    name: unknown-conn
    connectors:
      - id: src
        type: source
        plugin: "builtin:does-not-exist"
      - id: dst
        type: destination
        plugin: "builtin:log"
`,
	}

	err := validateGalleryTemplateSettings(context.Background(), bad)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodeTemplateVersionMismatch.Reason())
}

func newTemplateGalleryEcdysis() *ecdysis.Ecdysis {
	return ecdysis.New(ecdysis.WithDecorators(cecdysis.CommandWithResultDecorator{}))
}

// TestGalleryTemplates_ScaffoldParseableYAML is
// docs/design-documents/20260723-templates-gallery.md §6 AC-1: run
// `pipelines init --template <name>` for all four names against a temp
// --pipelines.path and assert the output YAML parses via the real
// pipeline-config parser (pkg/provisioning/config/yaml), matching what
// `conduit run` itself uses to load pipeline files.
func TestGalleryTemplates_ScaffoldParseableYAML(t *testing.T) {
	for _, tmpl := range galleryTemplates {
		t.Run(tmpl.Name, func(t *testing.T) {
			is := is.New(t)
			dir := t.TempDir()

			cmd := newTemplateGalleryEcdysis().MustBuildCobraCommand(&InitCommand{})
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs([]string{"--pipelines.path=" + dir, "--template=" + tmpl.Name})
			is.NoErr(cmd.Execute())

			path := dir + "/" + tmpl.Name + ".yaml"
			written, err := os.ReadFile(path)
			is.NoErr(err)

			parser := yamlparser.NewParser(log.Nop())
			pipelines, err := parser.Parse(context.Background(), strings.NewReader(string(written)))
			is.NoErr(err)
			is.Equal(len(pipelines), 1)
			is.Equal(pipelines[0].ID, tmpl.Name)
			is.True(len(pipelines[0].Connectors) == 2)
		})
	}
}

// TestTemplateList_JSON_EnvelopeShape is AC-2: --template list --json must
// conform to the Family A envelope and enumerate all four templates with a
// non-empty description each.
func TestTemplateList_JSON_EnvelopeShape(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	cmd := newTemplateGalleryEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--pipelines.path=" + dir, "--template=list", "--json"})
	is.NoErr(cmd.Execute())

	is.NoErr(testutils.ValidateEnvelope(out.Bytes()))

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out.Bytes(), &got))
	is.Equal(got.Command, "pipelines.init")
	is.True(got.OK)
	is.True(got.Error == nil)

	resultBytes, err := json.Marshal(got.Result)
	is.NoErr(err)
	var result TemplateListResult
	is.NoErr(json.Unmarshal(resultBytes, &result))
	is.Equal(len(result.Templates), 4)
	for _, entry := range result.Templates {
		is.True(entry.Name != "")
		is.True(entry.Description != "")
		is.True(entry.Source != "")
		is.True(entry.Destination != "")
		is.True(entry.DeliverySemantics != "")
	}

	// Nothing should have been written to --pipelines.path by list mode.
	entries, err := os.ReadDir(dir)
	is.NoErr(err)
	is.Equal(len(entries), 0)
}

// TestTemplateList_Human covers the non-JSON rendering.
func TestTemplateList_Human(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	cmd := newTemplateGalleryEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--pipelines.path=" + dir, "--template=list"})
	is.NoErr(cmd.Execute())

	got := out.String()
	is.True(strings.Contains(got, "Available vendored pipeline templates"))
	is.True(strings.Contains(got, "generator-log"))
	is.True(strings.Contains(got, "postgres-cdc-kafka"))
}

// TestInitCommand_UnknownTemplate_HardFailure is the edge-case table's
// "unknown --template name" row: a typo must produce a coded refusal
// enumerating the valid names, not silently fall back to the generic demo
// pipeline.
func TestInitCommand_UnknownTemplate_HardFailure(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	cmd := newTemplateGalleryEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--pipelines.path=" + dir, "--template=postgre-s3", "--json"}) // typo

	err := cmd.Execute()
	is.True(err != nil)
	is.Equal(exitcode.ExitCode(err), exitcode.Validation)

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out.Bytes(), &got))
	is.True(!got.OK)
	is.True(got.Error != nil)
	is.Equal(got.Error.Code, CodeUnknownTemplate.Reason())
	for _, name := range []string{"generator-log", "generator-file", "postgres-s3", "postgres-cdc-kafka"} {
		is.True(strings.Contains(got.Error.Suggestion, name))
	}

	entries, derr := os.ReadDir(dir)
	is.NoErr(derr)
	is.Equal(len(entries), 0) // nothing scaffolded on a hard failure
}

// TestInitCommand_TemplateMutuallyExclusiveWithSourceDestination and
// TestInitCommand_TemplateList_MutuallyExclusiveWithSourceDestination cover
// the edge-case table's "--template combined with --source/--destination"
// row, for both a real template name and the "list" sentinel.
func TestInitCommand_TemplateMutuallyExclusiveWithSourceDestination(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	cmd := newTemplateGalleryEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--pipelines.path=" + dir, "--template=generator-log", "--source=postgres", "--json"})

	err := cmd.Execute()
	is.True(err != nil)
	is.Equal(exitcode.ExitCode(err), exitcode.Validation)

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out.Bytes(), &got))
	is.Equal(got.Error.Code, CodeTemplateFlagsExclusive.Reason())
}

func TestInitCommand_TemplateList_MutuallyExclusiveWithSourceDestination(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	cmd := newTemplateGalleryEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--pipelines.path=" + dir, "--template=list", "--destination=s3", "--json"})

	err := cmd.Execute()
	is.True(err != nil)
	is.Equal(exitcode.ExitCode(err), exitcode.Validation)

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out.Bytes(), &got))
	is.Equal(got.Error.Code, CodeTemplateFlagsExclusive.Reason())
}

// TestInitCommand_TemplateScaffold_ExistingFile_RefusesWithoutForce is AC-7:
// the --template path reuses the exact same --force/O_EXCL handling as the
// generic path (writeFile), not a re-implementation.
func TestInitCommand_TemplateScaffold_ExistingFile_RefusesWithoutForce(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	cmd := newTemplateGalleryEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--pipelines.path=" + dir, "--template=generator-log"})
	is.NoErr(cmd.Execute())

	path := dir + "/generator-log.yaml"
	original, err := os.ReadFile(path)
	is.NoErr(err)
	is.True(len(original) > 0)

	cmd2 := newTemplateGalleryEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out2 bytes.Buffer
	cmd2.SetOut(&out2)
	cmd2.SetErr(&out2)
	cmd2.SetArgs([]string{"--pipelines.path=" + dir, "--template=generator-log", "--json"})

	err2 := cmd2.Execute()
	is.True(err2 != nil)
	is.Equal(exitcode.ExitCode(err2), exitcode.Validation)

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out2.Bytes(), &got))
	is.Equal(got.Error.Code, CodeDestinationExists.Reason())
	is.True(strings.Contains(got.Error.Suggestion, "--force"))

	after, err := os.ReadFile(path)
	is.NoErr(err)
	is.Equal(string(original), string(after))
}

func TestInitCommand_TemplateScaffold_ForceOverwrites(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	cmd := newTemplateGalleryEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--pipelines.path=" + dir, "--template=generator-log"})
	is.NoErr(cmd.Execute())

	path := dir + "/generator-log.yaml"
	is.NoErr(os.WriteFile(path, []byte("hand-edited: true\n"), 0o600))

	cmd2 := newTemplateGalleryEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out2 bytes.Buffer
	cmd2.SetOut(&out2)
	cmd2.SetArgs([]string{"--pipelines.path=" + dir, "--template=generator-log", "--force", "--json"})
	is.NoErr(cmd2.Execute())

	after, err := os.ReadFile(path)
	is.NoErr(err)
	is.True(!strings.Contains(string(after), "hand-edited"))
	is.True(strings.Contains(string(after), "generator-log-source"))
}

func TestInitCommand_TemplateScaffold_DryRun_WritesNothing(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	cmd := newTemplateGalleryEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--pipelines.path=" + dir, "--template=postgres-cdc-kafka", "--dry-run"})
	is.NoErr(cmd.Execute())

	entries, err := os.ReadDir(dir)
	is.NoErr(err)
	is.Equal(len(entries), 0)

	got := out.String()
	is.True(strings.Contains(got, "Dry run"))
	is.True(strings.Contains(got, "postgres-cdc-kafka-source"))
}

// TestInitCommand_TemplateScaffold_CustomPipelineName covers the positional
// pipeline-name argument controlling the output filename while the
// embedded YAML's internal id/name stay the template's own canonical value
// (documented behavior — see getPipelineNameForTemplate's doc comment).
func TestInitCommand_TemplateScaffold_CustomPipelineName(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	cmd := newTemplateGalleryEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"my-custom-name", "--pipelines.path=" + dir, "--template=generator-log", "--json"})
	is.NoErr(cmd.Execute())

	_, err := os.ReadFile(dir + "/my-custom-name.yaml")
	is.NoErr(err)

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out.Bytes(), &got))
	resultBytes, err := json.Marshal(got.Result)
	is.NoErr(err)
	var result InitResult
	is.NoErr(json.Unmarshal(resultBytes, &result))
	is.Equal(result.PipelineName, "my-custom-name")
	is.Equal(result.Template, "generator-log")
}

func TestCodeUnknownTemplate_Registered(t *testing.T) {
	is := is.New(t)
	_, ok := conduiterr.LookupCode(CodeUnknownTemplate.Reason())
	is.True(ok)
}

func TestCodeTemplateFlagsExclusive_Registered(t *testing.T) {
	is := is.New(t)
	_, ok := conduiterr.LookupCode(CodeTemplateFlagsExclusive.Reason())
	is.True(ok)
}

func TestCodeTemplateVersionMismatch_Registered(t *testing.T) {
	is := is.New(t)
	_, ok := conduiterr.LookupCode(CodeTemplateVersionMismatch.Reason())
	is.True(ok)
}
