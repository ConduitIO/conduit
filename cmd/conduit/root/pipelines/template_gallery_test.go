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
	"path/filepath"
	"regexp"
	"slices"
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

// galleryTemplatePluginNameRE extracts "plugin: \"kind:name\"" references
// from a rendered pipeline.yaml fixture, e.g. `plugin: "standalone:pgvector"`
// -> kind="standalone", name="pgvector". Anchored to the start of the line
// (ignoring leading whitespace, (?m)^\s*) so a commented-out fixture line
// (`# plugin: "standalone:pgvector"`) does NOT match — the `#` prefix means
// "plugin:" is no longer the first token on the line.
var galleryTemplatePluginNameRE = regexp.MustCompile(`(?m)^\s*plugin:\s*"?(builtin|standalone):([A-Za-z0-9_.-]+)"?`)

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
	is.Equal(len(galleryTemplates), 5)

	wantNames := map[string]bool{
		"generator-log": true, "generator-file": true,
		"postgres-s3": true, "postgres-cdc-kafka": true,
		"postgres-pgvector-rag": true,
	}
	for _, tmpl := range galleryTemplates {
		is.True(wantNames[tmpl.Name])
		is.True(tmpl.Name != templateListSentinel)
		// AC-5 ("zero templates require a non-built-in connector") holds
		// for source/destination that are EITHER a built-in connector OR
		// explicitly allowlisted by that same template's own
		// AllowedNonBuiltin — postgres-pgvector-rag is the one entry that
		// exercises the second branch (its Destination is "pgvector",
		// which isBuiltinConnectorName alone would reject).
		is.True(isBuiltinConnectorName(tmpl.Source) || slices.Contains(tmpl.AllowedNonBuiltin, tmpl.Source))
		is.True(isBuiltinConnectorName(tmpl.Destination) || slices.Contains(tmpl.AllowedNonBuiltin, tmpl.Destination))
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

// TestValidateGalleryCatalog_RejectsNonBuiltinConnector_ScopedToOneTemplate
// is the load-bearing safety property of the postgres-pgvector-rag
// exception (GalleryTemplate.AllowedNonBuiltin,
// docs/design-documents/20260724-ai-pipeline-components.md §8): granting
// ONE template permission to reference a non-built-in connector must not
// leak that permission to any OTHER template. This builds a synthetic
// catalog with two entries — one that legitimately allowlists
// "not-a-real-connector" (mirroring the real postgres-pgvector-rag entry's
// shape) and a second, unrelated template that references the SAME
// connector name WITHOUT itself being on any allowlist — and asserts the
// first passes while the second still fails validation. If the exception
// were checked catalog-wide instead of per-template, this test would fail
// (the second entry would wrongly pass once the first legitimized the
// name).
func TestValidateGalleryCatalog_RejectsNonBuiltinConnector_ScopedToOneTemplate(t *testing.T) {
	is := is.New(t)

	catalog := []GalleryTemplate{
		{
			Name: "legitimately-allowlisted", Description: "x", DeliverySemantics: "x",
			Source: "generator", Destination: "not-a-real-connector",
			AllowedNonBuiltin: []string{"not-a-real-connector"},
			YAML:              "version: \"2.2\"\n",
		},
		{
			Name: "not-allowlisted", Description: "x", DeliverySemantics: "x",
			Source: "generator", Destination: "not-a-real-connector",
			// No AllowedNonBuiltin: this template never opted into the
			// exception, even though a sibling entry's allowlist happens
			// to name the exact same connector.
			YAML: "version: \"2.2\"\n",
		},
	}

	err := validateGalleryCatalog(catalog)
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "not-allowlisted"))
	is.True(strings.Contains(err.Error(), "not-a-real-connector"))

	// Isolate: the first entry alone (with its own allowlist) must pass on
	// its own — proving the failure above is really about the SECOND
	// entry's missing allowlist, not some other defect in the fixture.
	is.NoErr(validateGalleryCatalog(catalog[:1]))
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

// TestValidateGalleryTemplateConnectors_AllowedNonBuiltin_SkipsSpecCheck_ScopedToTemplate
// is validateGalleryTemplateConnectors's own half of the scoped-exception
// safety property (the catalog-level half is
// TestValidateGalleryCatalog_RejectsNonBuiltinConnector_ScopedToOneTemplate):
// a connector plugin named in THIS template's AllowedNonBuiltin skips the
// builtin-spec/parameter check entirely — even one setting a nonsense key,
// since there's no compiled-in spec to check it against — while the exact
// same plugin/settings pair on a DIFFERENT template (no allowlist) still
// fails with CodeTemplateVersionMismatch.
func TestValidateGalleryTemplateConnectors_AllowedNonBuiltin_SkipsSpecCheck_ScopedToTemplate(t *testing.T) {
	is := is.New(t)

	yaml := `version: "2.2"
pipelines:
  - id: uses-pgvector
    status: running
    name: uses-pgvector
    connectors:
      - id: src
        type: source
        plugin: "builtin:generator"
      - id: dst
        type: destination
        plugin: "standalone:pgvector"
        settings:
          anySettingAtAll: "not checked against a compiled-in spec"
`

	allowed := GalleryTemplate{
		Name: "allowed", Description: "x", DeliverySemantics: "x",
		Source: "generator", Destination: "pgvector",
		AllowedNonBuiltin: []string{"pgvector"},
		YAML:              yaml,
	}
	is.NoErr(validateGalleryTemplateSettings(context.Background(), allowed))

	notAllowed := GalleryTemplate{
		Name: "not-allowed", Description: "x", DeliverySemantics: "x",
		Source: "generator", Destination: "pgvector",
		// No AllowedNonBuiltin — the identical connector reference must
		// still be refused for THIS template.
		YAML: yaml,
	}
	err := validateGalleryTemplateSettings(context.Background(), notAllowed)
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

// TestInitCommand_TemplateScaffold_PgvectorRAG_EmitsPrerequisites is the
// preflight-note requirement from design doc §8 ("never emit a bare YAML
// that fails opaquely on first `conduit run` because a plugin isn't
// present"): scaffolding postgres-pgvector-rag must surface its
// GalleryTemplate.Prerequisites in BOTH the --json result and the
// human-readable Render output, citing the `conduit
// processor-plugins install ai.chunk`/`ai.embed` commands (installable as of
// issue #2818's fix, still gated on minConduitVersion 0.20.0 — see
// TestGalleryCatalog_PgvectorRAG_PrerequisitesMatchPublishedReality) and
// pointing at a local build as the fallback for pgvector (always) and
// ai.chunk/ai.embed (only on a pre-0.20.0 Conduit).
func TestInitCommand_TemplateScaffold_PgvectorRAG_EmitsPrerequisites(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	cmd := newTemplateGalleryEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--pipelines.path=" + dir, "--template=postgres-pgvector-rag", "--json"})
	is.NoErr(cmd.Execute())

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out.Bytes(), &got))
	is.True(got.OK)

	resultBytes, err := json.Marshal(got.Result)
	is.NoErr(err)
	var result InitResult
	is.NoErr(json.Unmarshal(resultBytes, &result))
	is.True(len(result.Prerequisites) >= 2)

	joined := strings.Join(result.Prerequisites, "\n")
	is.True(strings.Contains(joined, "go build -o conduit-connector-pgvector ./cmd/connector"))
	is.True(strings.Contains(joined, "conduit processor-plugins install ai.chunk"))
	is.True(strings.Contains(joined, "conduit processor-plugins install ai.embed"))
	is.True(strings.Contains(joined, "GOOS=wasip1 GOARCH=wasm"))

	// The human-readable Render path must surface the same note, not just
	// the --json payload.
	cmd2 := newTemplateGalleryEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out2 bytes.Buffer
	cmd2.SetOut(&out2)
	cmd2.SetArgs([]string{"--pipelines.path=" + dir, "--template=postgres-pgvector-rag", "--force"})
	is.NoErr(cmd2.Execute())
	is.True(strings.Contains(out2.String(), "go build -o conduit-connector-pgvector ./cmd/connector"))
}

// TestGalleryCatalog_PgvectorRAG_PrerequisitesMatchPublishedReality is a
// drift guard for a docs-honesty bug that shipped in two opposite directions
// at once: the pgvector prerequisite told users to run `conduit connectors
// install pgvector@<version>`, a command that can never succeed
// (github.com/conduitio/conduit-connector-pgvector has zero git tags and
// zero releases, so there is no version to resolve), while the processor
// prerequisite said the hosted install "goes live once conduit-processor-ai
// publishes signed artifacts" when ai.chunk@0.1.0 and ai.embed@0.1.0 were
// already live in the registry index (verified against
// ConduitIO/conduit-connector-registry@main's index/index.json, version 10,
// 2026-08-21T19:26:17Z: both carry a real artifact + slsaProvenance entry;
// pgvector is entirely absent from the published connectors list).
//
// A prior version of this guard replaced a stale phrase check
// (`strings.Contains(joined, "install "+name)`) that a false claim could
// satisfy just as easily as a true one: the ORIGINAL buggy prose also
// contained the literal text "install ai.chunk" — the lie was in the
// surrounding clause ("goes live once..."), not the command name. That is
// why the guard caught only ONE direction of the bug (a plugin told to
// `connectors install` something unpublished) and never the other (a
// plugin's install command falsely described as working). This version
// asserts the specific facts a reader needs for ai.chunk/ai.embed —
// published, currently blocked, why, and the actual working fallback —
// instead of a substring both the true and false prose would contain.
//
// Be precise about the limit of that, because it is easy to read this guard
// as stronger than it is: every assertion below is a PRESENCE check. It
// catches a fact being removed or reworded away — which is how the original
// bug happened. Detecting a contradicting clause added alongside facts that
// all still hold would mean parsing intent out of English, which a unit
// test cannot do.
//
// UPDATE (issue #2818 fixed): the ai.chunk/ai.embed block below was
// rewritten once `conduit processor-plugins install ai.chunk`/`ai.embed`
// actually succeeded against a real running build (see
// pkg/registry/resolve_realindex_test.go for the resolution-level proof,
// and this package's own manual verification: `conduit processor-plugins
// install ai.chunk --dry-run --json` against the live index). The
// remaining, still-real constraint is minConduitVersion 0.20.0: a v0.19.0
// stable Conduit is correctly refused, a v0.20.0 nightly or later is not.
// `registry.incompatible_version` is still asserted below — it is still
// the code returned for that legitimate refusal, just no longer the code
// returned unconditionally on every build regardless of version.
//
// It is intentionally hand-maintained per plugin rather than table/map-driven:
// this guard's whole job is to catch prose making a false claim about ONE
// specific plugin's install path, which a generic per-name loop (matching
// on a name->bool ground-truth map) cannot distinguish from a differently
// false claim that still satisfies a generic "is it mentioned" check — see
// TestInitCommand_TemplateScaffold_PgvectorRAG_EmitsPrerequisites's history
// for exactly that failure mode. A fourth non-built-in plugin would need
// its own explicit block added below; the generic loop immediately below
// only catches it being silently DROPPED from the prose, not a false claim
// about its specific install path — that gap is accepted, not enforced,
// because there is no offline way for a unit test to query the live
// registry index and confirm what "true" would even mean for an unknown
// future plugin.
//
// REVISIT the pgvector block below the moment
// github.com/conduitio/conduit-connector-pgvector cuts a tagged release
// that ConduitIO/conduit-connector-registry's index.json picks up — at that
// point `conduit connectors install pgvector@<version>` becomes a claim
// this test should REQUIRE, not forbid, and template_gallery.go's
// Prerequisites/README.md's table should switch back to the registry
// install path.
func TestGalleryCatalog_PgvectorRAG_PrerequisitesMatchPublishedReality(t *testing.T) {
	is := is.New(t)

	tmpl, ok := lookupGalleryTemplate(templateNamePostgresPgvectorRAG)
	is.True(ok)

	joined := strings.Join(tmpl.Prerequisites, "\n")

	pluginRefs := galleryTemplatePluginNameRE.FindAllStringSubmatch(tmpl.YAML, -1)
	is.True(len(pluginRefs) > 0) // the regex itself must still match the fixture's plugin lines

	// (1) Generic, name-agnostic check: every non-built-in plugin the shipped
	// YAML references must be named somewhere in the prerequisite prose, so a
	// plugin silently DROPPED from the prose (the other possible drift) fails
	// here too. Run per-plugin via t.Run so a failure names the plugin
	// instead of reporting only a line number.
	for _, m := range pluginRefs {
		kind, name := m[1], m[2]
		if kind == "builtin" {
			continue // built-ins need no install step
		}
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			is.True(strings.Contains(joined, name))
		})
	}

	// (2) pgvector: no tagged release exists anywhere, so a registry install
	// can never succeed — the prose must never claim otherwise. This substring
	// check is a heuristic, not a proof: a truthful sentence that happened to
	// read "do not run `conduit connectors install pgvector`" would also trip
	// it. That is an accepted, narrow false-positive risk given the prose is
	// authored entirely in this repo (template_gallery.go) — not a general
	// claim that substring matching soundly detects "is this a working
	// instruction or a warning."
	t.Run("pgvector_not_registry_installable", func(t *testing.T) {
		is := is.New(t)
		is.True(!strings.Contains(joined, "connectors install pgvector"))
		is.True(strings.Contains(joined, "go build -o conduit-connector-pgvector"))
	})

	// (3) ai.chunk/ai.embed: published to the signed registry (0.1.0 per the
	// live index, see doc comment above) and installable via
	// `conduit processor-plugins install` (#2818's always-false protocol
	// comparison is fixed) — but still genuinely gated on minConduitVersion
	// 0.20.0, so registry.incompatible_version remains the correct code for
	// a too-old running Conduit. Assert the specific facts, not a phrase the
	// old false prose also contained.
	// (4) The template README mirrors this prose for a reader who never runs
	// `pipelines init`. Nothing enforced that mirror before this check, while
	// the PR that introduced it claimed the two "cannot drift apart
	// silently" — README/source drift being the exact bug class this guard
	// exists to catch, one level up. Assert the load-bearing facts appear in
	// both, so correcting one file and forgetting the other fails here.
	//
	// The test's working directory is the package directory, so this relative
	// path is stable under `go test ./...` from anywhere in the repo.
	t.Run("readme_mirrors_the_prerequisites", func(t *testing.T) {
		is := is.New(t)
		readme, err := os.ReadFile(filepath.Join(
			"templates", templateNamePostgresPgvectorRAG, "README.md"))
		is.NoErr(err)
		text := string(readme)

		for _, fact := range []string{
			"0.1.0",                                  // the published processor version
			"minConduitVersion",                      // the real, still-enforced requirement
			"0.20.0",                                 // its actual value
			"registry.incompatible_version",          // the code for a too-old Conduit
			"2818",                                   // the tracking issue, now fixed
			"go build -o conduit-connector-pgvector", // the pgvector fallback
			"GOOS=wasip1 GOARCH=wasm",                // the processor fallback, for pre-0.20.0 builds
			"pipeline.fanout_requires_arch_v2",       // the real arch-v2 error
		} {
			is.True(strings.Contains(text, fact)) // README must state this fact too
		}
		// Same never-claim rule as (2), applied to the README.
		is.True(!strings.Contains(text, "connectors install pgvector`"))
	})

	t.Run("ai.chunk_ai.embed_published_and_installable", func(t *testing.T) {
		is := is.New(t)
		for _, name := range []string{"ai.chunk", "ai.embed"} {
			// The install command is cited as a WORKING instruction now, not
			// a doomed one — but see check (4) below for what must
			// accompany it.
			is.True(strings.Contains(joined, "conduit processor-plugins install "+name))
		}
		is.True(strings.Contains(joined, "0.1.0"))                         // the published version, not just "published"
		is.True(strings.Contains(joined, "minConduitVersion"))             // the real, still-enforced requirement
		is.True(strings.Contains(joined, "0.20.0"))                        // its actual value
		is.True(strings.Contains(joined, "registry.incompatible_version")) // the code for a too-old Conduit
		is.True(strings.Contains(joined, "2818"))                          // the tracking issue, now fixed
		is.True(strings.Contains(joined, "fixed"))                         // must say so, not just cite the issue number
		is.True(strings.Contains(joined, "GOOS=wasip1 GOARCH=wasm"))       // the fallback for pre-0.20.0 builds
		is.True(strings.Contains(joined, "./cmd/chunking"))
		is.True(strings.Contains(joined, "./cmd/embedding"))
		// --bundle hits the identical version algebra offline (bundle.go), so
		// the prose must say so explicitly rather than offer it as an
		// unqualified escape hatch (a positive assertion, not a bare
		// "--bundle" absence check — the fact IS worth stating, just not as
		// a working alternative).
		is.True(strings.Contains(joined, "not a workaround"))
	})
}

// TestInitCommand_TemplateScaffold_BuiltinOnlyTemplate_NoPrerequisites is
// the converse: a built-in-only template (generator-log) must never carry a
// prerequisites note — it's self-sufficient the moment it's scaffolded, and
// the field should stay empty rather than print a vacuous "no steps needed"
// section.
func TestInitCommand_TemplateScaffold_BuiltinOnlyTemplate_NoPrerequisites(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	cmd := newTemplateGalleryEcdysis().MustBuildCobraCommand(&InitCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--pipelines.path=" + dir, "--template=generator-log", "--json"})
	is.NoErr(cmd.Execute())

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out.Bytes(), &got))
	resultBytes, err := json.Marshal(got.Result)
	is.NoErr(err)
	var result InitResult
	is.NoErr(json.Unmarshal(resultBytes, &result))
	is.Equal(len(result.Prerequisites), 0)
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
	is.Equal(len(result.Templates), 5)
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
	for _, name := range []string{
		"generator-log", "generator-file", "postgres-s3", "postgres-cdc-kafka", "postgres-pgvector-rag",
	} {
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
