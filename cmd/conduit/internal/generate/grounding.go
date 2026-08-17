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
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	procbuiltin "github.com/conduitio/conduit/pkg/plugin/processor/builtin"
)

// Grounding size caps. A grounded prompt is sent on every attempt, so its
// size is a per-invocation cost the user pays in tokens; these bound it
// without hiding the part that matters.
const (
	// MaxOptionalParamsPerRole caps how many OPTIONAL parameters are listed
	// per connector role. Required parameters are never dropped: omitting one
	// would ground the model on a config it cannot legally produce.
	MaxOptionalParamsPerRole = 10
	// MaxParamDescriptionLen truncates a parameter description.
	MaxParamDescriptionLen = 140
)

// ConnectorInfo is one built-in connector as the grounded prompt sees it.
type ConnectorInfo struct {
	// Name is the bare plugin name ("postgres"), referenced in a config as
	// "builtin:postgres".
	Name    string
	Summary string
	// SourceParams/DestinationParams are empty when the connector does not
	// implement that role — which is itself the grounding fact that stops the
	// model proposing, say, a source that only exists as a destination.
	SourceParams      []ParamInfo
	DestinationParams []ParamInfo
}

// ParamInfo is one connector configuration parameter, flattened.
type ParamInfo struct {
	Key         string
	Type        string
	Default     string
	Required    bool
	Description string
}

// BuiltinCatalog reads every built-in connector's specification directly
// (conn.NewSpecification()), the same source cmd/conduit/internal/llmsgen
// reads for llms-full.txt — never a second hand-maintained list, per design
// §9. Reading the specifications rather than the llms-full.txt rendering of
// them keeps the catalog a compiled-in fact: it cannot drift from the binary
// the generated pipeline will actually run on, and it needs no file at
// runtime.
//
// Unlike llmsgen this deliberately does NOT report versions: a grounded
// prompt names connectors, and a version string read outside a build-info
// context is the placeholder "(devel)" (see llmsgen's gatherConnectors) —
// grounding the model on that would teach it to emit a fake version.
//
// The result is sorted by name, so an identical binary always produces an
// identical prompt. A nondeterministic prompt would make every eval number
// unreproducible.
func BuiltinCatalog() []ConnectorInfo {
	out := make([]ConnectorInfo, 0, len(builtin.DefaultBuiltinConnectors))

	for _, conn := range builtin.DefaultBuiltinConnectors {
		if conn.NewSpecification == nil {
			continue
		}
		spec := conn.NewSpecification()
		out = append(out, ConnectorInfo{
			Name:              spec.Name,
			Summary:           spec.Summary,
			SourceParams:      groundedParams(spec.SourceParams),
			DestinationParams: groundedParams(spec.DestinationParams),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// BuiltinConnectorRefs returns every resolvable builtin connector reference —
// full module path AND short name — for validate.Options.BuiltinPlugins.
//
// Derived from the same builtin.DefaultBuiltinConnectors map BuiltinCatalog
// reads, so the set a candidate is CHECKED against can never drift from the
// set the model was GROUNDED on. Grounding on one list and validating against
// another is how a fabricated plugin name reaches a user.
func BuiltinConnectorRefs() map[string]struct{} {
	set := make(map[string]struct{}, len(builtin.DefaultBuiltinConnectors)*2)
	for fullPath := range builtin.DefaultBuiltinConnectors {
		set[fullPath] = struct{}{}
		set[strings.TrimPrefix(path.Base(fullPath), "conduit-connector-")] = struct{}{}
	}
	return set
}

// BuiltinProcessorRefs returns the resolvable builtin processor plugin names
// (e.g. "json.decode", "field.set") for validate.Options.BuiltinProcessors.
// Unlike connectors, these map keys are already the names a config references.
func BuiltinProcessorRefs() map[string]struct{} {
	set := make(map[string]struct{}, len(procbuiltin.DefaultBuiltinProcessors))
	for name := range procbuiltin.DefaultBuiltinProcessors {
		set[name] = struct{}{}
	}
	return set
}

// CatalogNames returns the bare plugin names in the catalog, sorted — the
// allowlist a candidate's plugin references are checked against, and the
// suggestion pool RetryFeedback.WithConnectorSuggestions draws from.
func CatalogNames(cat []ConnectorInfo) []string {
	out := make([]string, 0, len(cat))
	for _, c := range cat {
		out = append(out, c.Name)
	}
	sort.Strings(out)
	return out
}

// groundedParams flattens a config.Parameters map, required parameters first,
// then optional ones capped at MaxOptionalParamsPerRole.
//
// config.Parameters is a Go map, so it is sorted by key before anything else
// touches it: an unsorted iteration would make the prompt — and therefore
// every eval run — nondeterministic.
func groundedParams(params config.Parameters) []ParamInfo {
	if len(params) == 0 {
		return nil
	}

	all := make([]ParamInfo, 0, len(params))
	for key, p := range params {
		all = append(all, ParamInfo{
			Key:         key,
			Type:        p.Type.String(),
			Default:     p.Default,
			Required:    paramRequired(p),
			Description: clipDescription(p.Description),
		})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Key < all[j].Key })

	out := make([]ParamInfo, 0, len(all))
	for _, p := range all {
		if p.Required {
			out = append(out, p)
		}
	}
	optional := 0
	for _, p := range all {
		if p.Required {
			continue
		}
		if optional == MaxOptionalParamsPerRole {
			break
		}
		out = append(out, p)
		optional++
	}
	return out
}

// paramRequired reports whether p carries a required validation.
func paramRequired(p config.Parameter) bool {
	for _, v := range p.Validations {
		if v.Type() == config.ValidationTypeRequired {
			return true
		}
	}
	return false
}

// clipDescription flattens a parameter description to one line and truncates
// it. Descriptions are authored prose with newlines and examples; unclipped
// they dominate the prompt without adding grounding value.
func clipDescription(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= MaxParamDescriptionLen {
		return s
	}
	return s[:MaxParamDescriptionLen] + "…"
}

// BuildSystemPrompt renders the grounding: the config version the parser
// accepts, the rules the output must satisfy, and the connector catalog.
//
// Everything in here is derived from the compiled-in catalog or written
// literally in this function. No part of it is model-influenced and no part
// is read from a file at runtime, so the same binary always grounds the same
// way.
func BuildSystemPrompt(cat []ConnectorInfo) string {
	var b strings.Builder

	b.WriteString("You generate Conduit pipeline configuration files.\n\n")
	b.WriteString("Rules:\n")
	fmt.Fprintf(&b, "- Output ONLY a pipeline config YAML document with version %q. No prose, no explanation.\n", GroundedConfigVersion)
	b.WriteString("- Use ONLY the connectors listed below, referenced as \"builtin:<name>\" (e.g. \"builtin:postgres\").\n")
	b.WriteString("- Never invent a connector, a processor, or a configuration parameter that is not listed.\n")
	b.WriteString("- Set every required parameter of every connector you use.\n")
	b.WriteString("- Never put a real credential in the output. Where a secret is needed, emit the literal placeholder TODO.\n")
	b.WriteString("- A pipeline needs an id, at least one source connector, and at least one destination connector.\n\n")

	b.WriteString("Shape:\n\n")
	b.WriteString(groundingExample)
	b.WriteString("\nAvailable connectors:\n")

	for _, c := range cat {
		b.WriteString("\n- ")
		b.WriteString(c.Name)
		if c.Summary != "" {
			b.WriteString(": ")
			b.WriteString(clipDescription(c.Summary))
		}
		b.WriteString("\n")
		writeRole(&b, "source", c.SourceParams)
		writeRole(&b, "destination", c.DestinationParams)
	}

	return b.String()
}

// writeRole renders one role's parameters, or states plainly that the
// connector does not implement the role — an absence the model must know
// about, since "postgres as a destination" and "log as a source" are exactly
// the kind of plausible-but-wrong pipeline this grounding exists to prevent.
func writeRole(b *strings.Builder, role string, params []ParamInfo) {
	if len(params) == 0 {
		fmt.Fprintf(b, "  %s: not supported\n", role)
		return
	}

	fmt.Fprintf(b, "  %s parameters:\n", role)
	for _, p := range params {
		fmt.Fprintf(b, "    - %s (%s", p.Key, p.Type)
		if p.Required {
			b.WriteString(", required")
		}
		if p.Default != "" {
			fmt.Fprintf(b, ", default %s", p.Default)
		}
		b.WriteString(")")
		if p.Description != "" {
			b.WriteString(": ")
			b.WriteString(p.Description)
		}
		b.WriteString("\n")
	}
}

// GroundedConfigVersion is the pipeline-config version generated candidates
// are told to emit.
const GroundedConfigVersion = "2.2"

// groundingExample is the one worked example in the prompt. It uses only
// built-in connectors that exist in every build (generator, log), so the
// example itself can never reference something the catalog does not offer.
const groundingExample = `version: "2.2"
pipelines:
  - id: example-pipeline
    status: running
    connectors:
      - id: source
        type: source
        plugin: "builtin:generator"
        settings:
          format.type: structured
      - id: destination
        type: destination
        plugin: "builtin:log"
        settings:
          level: info
`
