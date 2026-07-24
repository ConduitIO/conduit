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
	"sort"
	"strings"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
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
}

//go:embed templates/generator-log/pipeline.yaml
var galleryYAMLGeneratorLog string

//go:embed templates/generator-file/pipeline.yaml
var galleryYAMLGeneratorFile string

//go:embed templates/postgres-s3/pipeline.yaml
var galleryYAMLPostgresS3 string

//go:embed templates/postgres-cdc-kafka/pipeline.yaml
var galleryYAMLPostgresCDCKafka string

// Template names (GalleryTemplate.Name / --template values) and built-in
// connector names used more than once below — named so golangci-lint's
// goconst check doesn't flag repeated string literals, and so the E2E test
// file (template_gallery_e2e_test.go) can reference the same names rather
// than re-typing them.
const (
	templateNameGeneratorLog     = "generator-log"
	templateNameGeneratorFile    = "generator-file"
	templateNamePostgresS3       = "postgres-s3"
	templateNamePostgresCDCKafka = "postgres-cdc-kafka"

	connNamePostgres = "postgres"
	connNameS3       = "s3"
	connNameKafka    = "kafka"
	connNameFile     = "file"
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
// description; source/destination that both resolve to a built-in
// connector; non-empty embedded YAML. It does NOT check individual setting
// keys against the connector's parameter spec — that is
// validateGalleryTemplateSettings's job, run per-template at scaffold time
// (not at package init, so a synthetic "stale fixture" can be asserted
// against directly in a test without crashing the whole test binary via a
// package-level panic).
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
		if !isBuiltinConnectorName(t.Source) {
			return cerrors.Errorf("embedded template gallery: template %q: source %q is not a built-in connector",
				t.Name, t.Source)
		}
		if !isBuiltinConnectorName(t.Destination) {
			return cerrors.Errorf("embedded template gallery: template %q: destination %q is not a built-in connector",
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
		if err := validateGalleryTemplateConnectors(tmpl.Name, p.Connectors); err != nil {
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

func validateGalleryTemplateConnectors(templateName string, connectors []provconfig.Connector) error {
	for _, c := range connectors {
		pluginName := strings.TrimPrefix(c.Plugin, "builtin:")

		params, ok := builtinConnectorSpecParams(pluginName, c.Type)
		if !ok {
			ce := conduiterr.New(CodeTemplateVersionMismatch, fmt.Sprintf(
				"embedded template %q references %s connector %q, which does not exist in this build's "+
					"built-in connector set", templateName, c.Type, pluginName,
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
					"the %q connector built into this binary", templateName, c.Type, c.ID, key, pluginName,
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
