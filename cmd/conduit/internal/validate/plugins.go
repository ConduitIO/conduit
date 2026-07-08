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
	"fmt"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/plugin"
	connbuiltin "github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	procbuiltin "github.com/conduitio/conduit/pkg/plugin/processor/builtin"
	"github.com/conduitio/conduit/pkg/provisioning/config"
)

// builtinConnectorNames is the set of builtin connector plugin names (e.g.
// "postgres", "s3" — the short name a pipeline config's "builtin:<name>"
// plugin ref addresses), computed once at package init from
// connbuiltin.DefaultBuiltinConnectors. Each entry's
// sdk.Connector.NewSpecification() call parses an embedded YAML string
// compiled into the binary (see e.g. conduit-connector-postgres's
// connector.go: `NewSpecification: sdk.YAMLSpecification(specs, version)`)
// — no plugin process, no dial, no schema service — the same call
// cmd/conduit/root/pipelines/init.go already makes to list scaffoldable
// connectors. This, not the full pkg/plugin/connector/builtin.Registry (which
// requires a *connutils.SchemaService and mutates the package-level
// schema.Service on Init), is what keeps RunDryRun's --resolve-plugins
// offline: never a real Registry, just its ingredient map.
var builtinConnectorNames = func() map[string]bool {
	names := make(map[string]bool, len(connbuiltin.DefaultBuiltinConnectors))
	for _, conn := range connbuiltin.DefaultBuiltinConnectors {
		names[conn.NewSpecification().Name] = true
	}
	return names
}()

// builtinProcessorNames is the set of builtin processor plugin names (e.g.
// "json.decode", "filter"). Unlike connectors, DefaultBuiltinProcessors's
// map keys are already the short plugin names, so no specification call is
// needed at all.
var builtinProcessorNames = func() map[string]bool {
	names := make(map[string]bool, len(procbuiltin.DefaultBuiltinProcessors))
	for name := range procbuiltin.DefaultBuiltinProcessors {
		names[name] = true
	}
	return names
}()

// pluginResolution is one connector/processor's --resolve-plugins verdict.
type pluginResolution int

const (
	// pluginResolutionBuiltinOK: an explicit "builtin:<name>" ref whose name
	// is a known builtin.
	pluginResolutionBuiltinOK pluginResolution = iota
	// pluginResolutionBuiltinNotFound: an explicit "builtin:<name>" ref
	// whose name is NOT a known builtin — a real problem, becomes an error
	// Finding.
	pluginResolutionBuiltinNotFound
	// pluginResolutionUnverified: a "standalone:" ref, or an unprefixed
	// ("any") ref. Per pkg/plugin/connector/service.go's newDispenser, an
	// unprefixed ref tries the standalone registry FIRST and only falls
	// back to builtin, so it can't be statically classified as "builtin"
	// without dialing a standalone plugin — never a Finding, per the design
	// doc's failure modes ("--resolve-plugins standalone false-negatives ->
	// scope to builtins").
	pluginResolutionUnverified
)

// resolvePluginRef classifies ref (a connector's or processor's Plugin
// field) against builtinNames (builtinConnectorNames or
// builtinProcessorNames, as appropriate for the caller).
func resolvePluginRef(ref string, builtinNames map[string]bool) pluginResolution {
	fn := plugin.FullName(ref)
	if fn.PluginType() != plugin.PluginTypeBuiltin {
		return pluginResolutionUnverified
	}
	if builtinNames[fn.PluginName()] {
		return pluginResolutionBuiltinOK
	}
	return pluginResolutionBuiltinNotFound
}

// resolvePluginFindings resolves every connector and processor plugin ref in
// an already-enriched pipeline p, returning an error Finding
// (connector.plugin_not_found / processor.plugin_not_found) for each
// "builtin:<name>" ref whose name isn't a known builtin. Standalone/
// unprefixed refs never produce a Finding here (see
// pluginResolutionUnverified) — RunDryRun instead marks them "unverified" on
// the enriched-graph output (see buildEnrichedFiles), which is how AC-6's
// "advisory, not a false fail" surfaces without a Finding that would flip a
// clean run to OK:false.
func resolvePluginFindings(p config.Pipeline) []Finding {
	var findings []Finding
	for i, c := range p.Connectors {
		findings = append(findings, pluginNotFoundFinding(
			c.Plugin, builtinConnectorNames, conduiterr.CodeConnectorPluginNotFound,
			fmt.Sprintf("/connectors/%d/plugin", i), fmt.Sprintf("connector %q", c.ID))...)
		findings = append(findings, processorPluginFindings(c.Processors, fmt.Sprintf("/connectors/%d/processors", i))...)
	}
	findings = append(findings, processorPluginFindings(p.Processors, "/processors")...)
	return findings
}

// processorPluginFindings resolves every processor in procs, whose
// JSON-pointer paths are pathPrefix+"/<index>/plugin" — pathPrefix is
// "/processors" for a pipeline's top-level processors, or
// "/connectors/<i>/processors" for a connector's nested ones (mirroring
// config.validateProcessors's own pathPrefix convention).
func processorPluginFindings(procs []config.Processor, pathPrefix string) []Finding {
	var findings []Finding
	for i, proc := range procs {
		findings = append(findings, pluginNotFoundFinding(
			proc.Plugin, builtinProcessorNames, conduiterr.CodeProcessorPluginNotFound,
			fmt.Sprintf("%s/%d/plugin", pathPrefix, i), fmt.Sprintf("processor %q", proc.ID))...)
	}
	return findings
}

// pluginNotFoundFinding returns a single error Finding if ref is an unknown
// builtin plugin, or nil otherwise (a known builtin, a not-statically-
// verifiable ref, or an empty ref — already reported separately by
// config.Validate's mandatory-field check, so not double-reported here).
func pluginNotFoundFinding(ref string, builtinNames map[string]bool, notFoundCode conduiterr.Code, configPath, subject string) []Finding {
	if ref == "" {
		return nil
	}
	if resolvePluginRef(ref, builtinNames) != pluginResolutionBuiltinNotFound {
		return nil
	}

	name := plugin.FullName(ref).PluginName()
	return []Finding{{
		Severity:   SeverityError,
		Code:       notFoundCode.Reason(),
		Message:    fmt.Sprintf("%s: builtin plugin %q not found", subject, name),
		ConfigPath: configPath,
		Suggestion: fmt.Sprintf("check the plugin name, or drop the %q prefix if %q is meant to resolve as a standalone plugin", plugin.PluginTypeBuiltin+":", name),
	}}
}
