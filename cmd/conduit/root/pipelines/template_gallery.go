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

// This file backs `conduit pipelines init --template <name>` (v0.19
// Workstream 3, docs/design-documents/20260723-templates-gallery.md) — a
// small, permanently-maintained, embedded (go:embed) gallery of named,
// runnable pipeline templates. This is
// deliberately a DIFFERENT concept from pipelineTemplate/connectorSpec in
// template.go, which back the generic --source/--destination scaffold path
// by introspecting a builtin connector's Specification at scaffold time: a
// GalleryTemplate is a curated, versioned, FULLY-RENDERED YAML fixture (the
// literal bytes written to disk are the literal bytes committed to this
// repo, and the literal bytes each template's README documents as its
// "runnable example" and each end-to-end CI test parses and runs) — see
//
// docs/design-documents/20260723-templates-gallery.md §4's "corrected precedent" discussion and task (2) of its
// breakdown for why this format was chosen over re-templating connector
// params per invocation.
package pipelines

import (
	"context"
	_ "embed"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	provconfig "github.com/conduitio/conduit/pkg/provisioning/config"
	yamlparser "github.com/conduitio/conduit/pkg/provisioning/config/yaml"
)

// templateListSentinel is the reserved --template value that switches
// `pipelines init` into enumeration mode instead of scaffolding
// (
// docs/design-documents/20260723-templates-gallery.md §4/§7). No embedded template may use this name;
// validateGalleryCatalog enforces that at package init time — a future
// template literally named "list" fails the build (see
// TestGalleryCatalog_Valid), not just a runtime ambiguity.
const templateListSentinel = "list"

// GalleryTemplate is one entry in the embedded template gallery.
type GalleryTemplate struct {
	// Name is the --template flag value that selects this entry. Must be
	// unique across the catalog and must never equal templateListSentinel.
	Name string
	// Description is the one-line summary shown by --template list.
	Description string
	// Source and Destination are the built-in connector names this template
	// configures, as reported by conn.NewSpecification().Name (see
	// isBuiltinConnectorName) — e.g. "generator", "postgres", "s3", "kafka".
	// Both must resolve to a built-in connector (
	// docs/design-documents/20260723-templates-gallery.md §6 AC-5):
	// validateGalleryCatalog fails the build otherwise.
	Source      string
	Destination string
	// DeliverySemantics is the one-line delivery-semantics summary carried
	// in --template list's JSON payload (
	// docs/design-documents/20260723-templates-gallery.md §10). The full
	// explanation lives in the template's README, not here.
	DeliverySemantics string
	// YAML is the literal, embedded pipeline configuration this template
	// scaffolds.
	YAML string
	// AllowedNonBuiltin is a SCOPED exception to the built-in-only invariant
	// above: the plugin names (Specification.Name / connector-plugin name)
	// this ONE template is explicitly permitted to reference as its Source
	// or Destination even though they are not in
	// builtin.DefaultBuiltinConnectors. This is the registry-backed
	// template extension point named (but not built) by
	// docs/design-documents/20260723-templates-gallery.md §7's "a future
	// template author proposes one needing a non-built-in connector" row,
	// exercised for the first time by
	// docs/design-documents/20260724-ai-pipeline-components.md §8's
	// postgres-pgvector-rag template. Nil for every other template: leaving
	// this empty is what keeps AC-5 ("zero templates require a non-built-in
	// connector") a hard, package-init-enforced invariant for the rest of
	// the catalog — validateGalleryCatalog and
	// validateGalleryTemplateConnectors both consult this field per-template
	// rather than relaxing the check globally, so granting the exception to
	// one entry can never silently authorize it for another (see
	// TestValidateGalleryCatalog_RejectsNonBuiltinConnector_ScopedToOneTemplate).
	AllowedNonBuiltin []string
	// Prerequisites are preflight steps (registry plugin installs, offline
	// `--bundle` installs, or a manual build/placement for a local dev build)
	// that must be completed before `conduit run` will succeed against this
	// template's scaffolded YAML. `pipelines init --template <name>`
	// surfaces these directly in its result (both --json and
	// human-readable) rather than only documenting them in the template's
	// README, mirroring `conduit migrate kafka-connect`'s "never silently
	// drop config it can't translate" spirit — applied here as "never emit
	// a bare YAML that fails opaquely on first `conduit run` because a
	// plugin isn't present" (design doc §8). Nil for every template whose
	// Source/Destination are both built in (AllowedNonBuiltin is also nil
	// for those, structurally: a template with prerequisites necessarily
	// has a non-empty AllowedNonBuiltin, though the converse — an allowed
	// non-builtin plugin the user is assumed to already have installed —
	// isn't required to hold).
	Prerequisites []string
}

//go:embed templates/generator-log/pipeline.yaml
var galleryYAMLGeneratorLog string

//go:embed templates/generator-file/pipeline.yaml
var galleryYAMLGeneratorFile string

//go:embed templates/postgres-s3/pipeline.yaml
var galleryYAMLPostgresS3 string

//go:embed templates/postgres-cdc-kafka/pipeline.yaml
var galleryYAMLPostgresCDCKafka string

//go:embed templates/postgres-pgvector-rag/pipeline.yaml
var galleryYAMLPostgresPgvectorRAG string

// Template names (GalleryTemplate.Name / --template values) and built-in
// connector names used more than once below — named so golangci-lint's
// goconst check doesn't flag repeated string literals, and so the E2E test
// file (template_gallery_e2e_test.go) can reference the same names rather
// than re-typing them.
const (
	templateNameGeneratorLog        = "generator-log"
	templateNameGeneratorFile       = "generator-file"
	templateNamePostgresS3          = "postgres-s3"
	templateNamePostgresCDCKafka    = "postgres-cdc-kafka"
	templateNamePostgresPgvectorRAG = "postgres-pgvector-rag"

	connNamePostgres = "postgres"
	connNameS3       = "s3"
	connNameKafka    = "kafka"
	connNameFile     = "file"
	// connNamePgvector is NOT in builtin.DefaultBuiltinConnectors — it is
	// the one registry-installed, non-built-in destination the
	// postgres-pgvector-rag template is scoped-exception-permitted to name
	// (GalleryTemplate.AllowedNonBuiltin).
	connNamePgvector = "pgvector"
)

// galleryCatalogSpec is the MVP template set (
// docs/design-documents/20260723-templates-gallery.md §2/§4): all four
// use only entries in builtin.DefaultBuiltinConnectors, so the "manual
// download cliff" this workstream exists to avoid is structurally
// impossible to hit with this set (validateGalleryCatalog proves it, rather
// than this comment merely asserting it).
func galleryCatalogSpec() []GalleryTemplate {
	return []GalleryTemplate{
		{
			Name: templateNameGeneratorLog,
			Description: "Generate synthetic records and log them to stdout " +
				"— the fastest way to see a pipeline run.",
			Source:      defaultSource,
			Destination: defaultDestination,
			DeliverySemantics: "At-least-once; no persisted position, so a restart starts a " +
				"new synthetic stream rather than replaying anything.",
			YAML: galleryYAMLGeneratorLog,
		},
		{
			Name: templateNameGeneratorFile,
			Description: "Generate synthetic records and append them as newline-delimited " +
				"JSON to a local file.",
			Source:      defaultSource,
			Destination: connNameFile,
			DeliverySemantics: "At-least-once; records are acked only after the file write " +
				"is flushed, and the destination only ever appends.",
			YAML: galleryYAMLGeneratorFile,
		},
		{
			Name: templateNamePostgresS3,
			Description: "Snapshot a Postgres table, then stream ongoing changes, " +
				"landing each record as JSON in an S3 bucket.",
			Source:      connNamePostgres,
			Destination: connNameS3,
			DeliverySemantics: "At-least-once, not exactly-once; cdcMode is \"auto\" " +
				"(falls back to polling if logical replication isn't available).",
			YAML: galleryYAMLPostgresS3,
		},
		{
			Name: templateNamePostgresCDCKafka,
			Description: "Stream Postgres change data capture (logical replication, " +
				"no initial snapshot) straight to a Kafka topic.",
			Source:      connNamePostgres,
			Destination: connNameKafka,
			DeliverySemantics: "At-least-once, not exactly-once; cdcMode is forced to " +
				"\"logrepl\" — refuses to run rather than silently degrading to polling.",
			YAML: galleryYAMLPostgresCDCKafka,
		},
		{
			// postgres-pgvector-rag is the RAG-sync template
			// (docs/design-documents/20260724-ai-pipeline-components.md
			// §8): Postgres CDC -> chunk -> embed -> pgvector. It is the
			// FIRST (and, deliberately, only) entry naming a non-built-in
			// destination — AllowedNonBuiltin below is the scoped
			// exception that makes validateGalleryCatalog and
			// validateGalleryTemplateConnectors permit it without
			// weakening AC-5 for every other template.
			Name: templateNamePostgresPgvectorRAG,
			Description: "Sync a Postgres table into a RAG-ready vector store: chunk each row's text, " +
				"embed it, and upsert the vectors into pgvector.",
			Source:            connNamePostgres,
			Destination:       connNamePgvector,
			AllowedNonBuiltin: []string{connNamePgvector},
			DeliverySemantics: "At-least-once, not exactly-once; a row is only acknowledged upstream " +
				"once pgvector has durably upserted every chunk derived from it. Deletes remove all " +
				"chunks ever derived from a source row (matched by source_key), even across a chunk-count " +
				"change, so no orphaned vectors survive an update or delete.",
			YAML: galleryYAMLPostgresPgvectorRAG,
			// Prerequisites: this template's pipeline.yaml references three
			// plugins none of which are built into this binary (postgres
			// is the only built-in participant). Surfaced directly in
			// `pipelines init --template postgres-pgvector-rag`'s result
			// (init.go's InitResult.Prerequisites / Render) so the command
			// never emits a bare YAML that fails opaquely on first
			// `conduit run` — see this field's doc comment.
			//
			// The pgvector and processor entries below must each state
			// their registry-availability truthfully — see
			// TestGalleryCatalog_PgvectorRAG_PrerequisitesMatchPublishedReality
			// in template_gallery_test.go for the drift guard and the
			// exact condition under which it (and this comment) should be
			// revisited: github.com/conduitio/conduit-connector-pgvector
			// cutting its first tagged release.
			Prerequisites: []string{
				"Run the pipeline with pipeline architecture v2 (`--preview.pipeline-arch-v2`, or " +
					"`preview.pipeline-arch-v2: true` in the config). The chunking processor fans one source " +
					"record into many chunk records, and record fan-out is only supported by architecture v2; " +
					"on the default engine the pipeline fails at the chunk step with an \"unknown record type\" " +
					"error. Architecture v2 is a preview engine — see its status before relying on it in production.",
				"pgvector destination: NOT installable from the registry yet — " +
					"github.com/conduitio/conduit-connector-pgvector has no tagged release, so there is no " +
					"version to pass to `conduit connectors install`. Build it yourself: clone the repo and " +
					"run `go build -o conduit-connector-pgvector ./cmd/connector`, then place the binary under " +
					"the directory --connectors.path points at (defaults to `connectors/` next to " +
					"conduit.yaml). Switch to the registry install once that repo cuts a tagged release.",
				"conduit-processor-ai's chunking (ai.chunk) and embedding (ai.embed) processors ARE " +
					"published to the signed registry — install them with `conduit processor-plugins install " +
					"ai.chunk` and `conduit processor-plugins install ai.embed`. To test an unreleased change " +
					"instead, build from a github.com/conduitio/conduit-processor-ai checkout " +
					"(`GOOS=wasip1 GOARCH=wasm go build -tags wasm -o ai-chunk.wasm ./cmd/chunking`, likewise " +
					"`./cmd/embedding`) and place the .wasm files under --processors.path.",
			},
		},
	}
}

// galleryTemplates is the validated, ready-to-serve catalog. Built once at
// package init time; mustBuildGalleryCatalog panics if the embedded catalog
// is structurally invalid (see validateGalleryCatalog) — a broken vendored
// template is a build-time bug, not something that should surface only when
// a user happens to pick that name.
var galleryTemplates = mustBuildGalleryCatalog()

func mustBuildGalleryCatalog() []GalleryTemplate {
	catalog := galleryCatalogSpec()
	if err := validateGalleryCatalog(catalog); err != nil {
		panic(fmt.Sprintf("pipelines: embedded template gallery is invalid: %v", err))
	}
	return catalog
}

// validateGalleryCatalog enforces the structural invariants every embedded
// template must hold (
// docs/design-documents/20260723-templates-gallery.md §6 AC-5, §7's reserved-name row): unique,
// non-empty names; never the reserved "list" sentinel; a non-empty
// description; source/destination that either resolve to a built-in
// connector OR are named in that specific template's own AllowedNonBuiltin
// (the scoped registry-backed-template exception,
// docs/design-documents/20260724-ai-pipeline-components.md §8 — see
// GalleryTemplate's doc comment); non-empty embedded YAML. Because the
// exception is checked per-template (t.AllowedNonBuiltin), a connector named
// in one template's allowlist grants NO permission to any other template —
// every other entry's empty AllowedNonBuiltin keeps AC-5 exactly as hard as
// before (see
// TestValidateGalleryCatalog_RejectsNonBuiltinConnector_ScopedToOneTemplate).
// This does NOT check individual setting keys against the connector's
// parameter spec — that is validateGalleryTemplateSettings's job, run
// per-template at scaffold time (not at package init, so a synthetic "stale
// fixture" can be asserted against directly in a test without crashing the
// whole test binary via a package-level panic).
func validateGalleryCatalog(catalog []GalleryTemplate) error {
	seen := make(map[string]bool, len(catalog))
	for _, t := range catalog {
		if t.Name == "" {
			return cerrors.Errorf("embedded template gallery: template has an empty name")
		}
		if t.Name == templateListSentinel {
			return cerrors.Errorf(
				"embedded template gallery: template must not be named %q (reserved for --template list)",
				templateListSentinel,
			)
		}
		if seen[t.Name] {
			return cerrors.Errorf("embedded template gallery: duplicate template name %q", t.Name)
		}
		seen[t.Name] = true

		if strings.TrimSpace(t.Description) == "" {
			return cerrors.Errorf("embedded template gallery: template %q has an empty description", t.Name)
		}
		if strings.TrimSpace(t.DeliverySemantics) == "" {
			return cerrors.Errorf("embedded template gallery: template %q has an empty delivery-semantics summary", t.Name)
		}
		if !isBuiltinConnectorName(t.Source) && !slices.Contains(t.AllowedNonBuiltin, t.Source) {
			return cerrors.Errorf(
				"embedded template gallery: template %q: source %q is not a built-in connector "+
					"and is not in this template's AllowedNonBuiltin allowlist",
				t.Name, t.Source)
		}
		if !isBuiltinConnectorName(t.Destination) && !slices.Contains(t.AllowedNonBuiltin, t.Destination) {
			return cerrors.Errorf(
				"embedded template gallery: template %q: destination %q is not a built-in connector "+
					"and is not in this template's AllowedNonBuiltin allowlist",
				t.Name, t.Destination)
		}
		if strings.TrimSpace(t.YAML) == "" {
			return cerrors.Errorf("embedded template gallery: template %q has empty embedded YAML", t.Name)
		}
	}
	return nil
}

// isBuiltinConnectorName reports whether name is the Specification.Name of
// one of builtin.DefaultBuiltinConnectors — the same set getSourceSpec/
// getDestinationSpec resolve --source/--destination against, kept as its
// own helper so validateGalleryCatalog doesn't need an InitCommand receiver.
func isBuiltinConnectorName(name string) bool {
	for _, conn := range builtin.DefaultBuiltinConnectors {
		if conn.NewSpecification().Name == name {
			return true
		}
	}
	return false
}

// lookupGalleryTemplate returns the named template and true, or a zero
// value and false if name isn't in the catalog.
func lookupGalleryTemplate(name string) (GalleryTemplate, bool) {
	for _, t := range galleryTemplates {
		if t.Name == name {
			return t, true
		}
	}
	return GalleryTemplate{}, false
}

// galleryTemplateNames returns every catalog template's name, sorted, for
// use in the unknown-template error's suggestion text.
func galleryTemplateNames() []string {
	names := make([]string, 0, len(galleryTemplates))
	for _, t := range galleryTemplates {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return names
}

// builtinConnectorSpecParams returns the SourceParams (connType == "source")
// or DestinationParams (connType == "destination") of the built-in connector
// named pluginName, or ok=false if no such connector exists in this build.
func builtinConnectorSpecParams(pluginName, connType string) (params config.Parameters, ok bool) {
	for _, conn := range builtin.DefaultBuiltinConnectors {
		specs := conn.NewSpecification()
		if specs.Name != pluginName {
			continue
		}
		switch connType {
		case "source":
			return specs.SourceParams, true
		case "destination":
			return specs.DestinationParams, true
		default:
			return nil, false
		}
	}
	return nil, false
}

// validateGalleryTemplateSettings re-parses tmpl's embedded YAML and checks
// every connector setting key against the CURRENT (build-time) parameter
// spec of the corresponding built-in connector. This is what turns a stale
// template fixture — one authored against an older connector's param shape
// — into an upfront, coded refusal at `pipelines init --template <name>`
// time, instead of a pipeline that scaffolds cleanly but fails far away,
// confusingly, at `conduit run` (
// docs/design-documents/20260723-templates-gallery.md §7's "version-pinned
// mismatch" row, §6 AC-6). Called once per scaffold invocation from
// InitCommand's template path — deliberately NOT from package init, so a
// test can construct a synthetic mismatched template and assert this
// function's error directly (see TestValidateGalleryTemplateSettings in
// template_gallery_test.go) without taking down the whole test binary via a
// package-level panic.
func validateGalleryTemplateSettings(ctx context.Context, tmpl GalleryTemplate) error {
	parser := yamlparser.NewParser(log.Nop())
	pipelineCfgs, err := parser.Parse(ctx, strings.NewReader(tmpl.YAML))
	if err != nil {
		return conduiterr.Wrap(CodeTemplateVersionMismatch,
			fmt.Sprintf("embedded template %q failed to parse", tmpl.Name), err)
	}

	for _, p := range pipelineCfgs {
		if err := validateGalleryTemplateConnectors(tmpl, p.Connectors); err != nil {
			return err
		}
	}
	return nil
}

// configParamRecognized reports whether key is a parameter the connector
// actually declares — either a literal match, or a match against a
// wildcard-suffixed spec key (e.g. the generator connector's arbitrary
// `format.options` map surfaces in its spec as the single wildcard entry
// "format.options.*", covering any concrete key like "format.options.id" or
// "format.options.name" a template sets under that prefix). Without the
// wildcard case, every template using a connector's free-form map-typed
// setting would incorrectly fail this check for every concrete key it sets.
func configParamRecognized(params config.Parameters, key string) bool {
	if _, ok := params[key]; ok {
		return true
	}
	for paramKey := range params {
		prefix, isWildcard := strings.CutSuffix(paramKey, "*")
		if isWildcard && strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// validateGalleryTemplateConnectors checks tmpl's parsed connectors against
// this build's actual connector param specs, with one scoped exception: a
// connector plugin named in tmpl.AllowedNonBuiltin (see GalleryTemplate's doc
// comment and validateGalleryCatalog) skips the builtin-spec lookup entirely
// — builtinConnectorSpecParams would legitimately return not-found for it
// (its spec isn't compiled into this binary at all), so treating that as a
// "does not exist" error would be wrong for a template deliberately
// referencing a registry-installed plugin. The allowlist is consulted
// per-connector against THIS tmpl only, so it grants no permission to any
// other catalog entry.
func validateGalleryTemplateConnectors(tmpl GalleryTemplate, connectors []provconfig.Connector) error {
	for _, c := range connectors {
		pluginName := plugin.FullName(c.Plugin).PluginName()

		if slices.Contains(tmpl.AllowedNonBuiltin, pluginName) {
			// Scoped exception (design doc §8 / GalleryTemplate.AllowedNonBuiltin):
			// this plugin's parameter spec is not compiled into this
			// binary, so there is nothing to validate setting keys
			// against here — a registry-installed plugin's own
			// install/startup validation is where a real mismatch would
			// surface. Do NOT build a parallel spec-fetching subsystem for
			// this; see the task scope note in this package's design doc
			// citation.
			continue
		}

		params, ok := builtinConnectorSpecParams(pluginName, c.Type)
		if !ok {
			ce := conduiterr.New(CodeTemplateVersionMismatch, fmt.Sprintf(
				"embedded template %q references %s connector %q, which does not exist in this build's "+
					"built-in connector set and is not in this template's AllowedNonBuiltin allowlist",
				tmpl.Name, c.Type, pluginName,
			))
			ce.Suggestion = "this is a packaging bug in the vendored template, not a user config error " +
				"— please file an issue against ConduitIO/conduit"
			return ce
		}

		for key := range c.Settings {
			if configParamRecognized(params, key) {
				continue
			}
			ce := conduiterr.New(CodeTemplateVersionMismatch, fmt.Sprintf(
				"embedded template %q sets %s connector %q's parameter %q, which is not recognized by "+
					"the %q connector built into this binary", tmpl.Name, c.Type, c.ID, key, pluginName,
			))
			ce.Suggestion = "the vendored template is pinned against an older connector parameter shape " +
				"than the one built into this binary — this is a packaging bug, not a user config error; " +
				"please file an issue against ConduitIO/conduit"
			return ce
		}
	}
	return nil
}

// TemplateListEntry is one row of `--template list --json`'s result payload
// (docs/design-documents/20260723-templates-gallery.md §10's committed shape).
type TemplateListEntry struct {
	Name              string `json:"name"`
	Description       string `json:"description"`
	Source            string `json:"source"`
	Destination       string `json:"destination"`
	DeliverySemantics string `json:"deliverySemantics"`
}

// TemplateListResult is `pipelines init --template list`'s --json result
// payload: `{"templates": [...]}`.
type TemplateListResult struct {
	Templates []TemplateListEntry `json:"templates"`
}

// TemplateListSummary is `pipelines init --template list`'s --json summary
// payload.
type TemplateListSummary struct {
	// Count is the number of templates in the embedded catalog.
	Count int `json:"count"`
}

// renderTemplateList is the human-readable (non-JSON) rendering of
// `--template list`.
func renderTemplateList(list TemplateListResult) string {
	var b strings.Builder
	b.WriteString("Available vendored pipeline templates:\n\n")
	for _, t := range list.Templates {
		fmt.Fprintf(&b, "  %-20s %s -> %s\n      %s\n\n", t.Name, t.Source, t.Destination, t.Description)
	}
	b.WriteString("Scaffold one with `conduit pipelines init --template <name>`.\n")
	return b.String()
}
