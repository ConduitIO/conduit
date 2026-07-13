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

package main

import (
	"fmt"
	"sort"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
)

// connectorInfo is the rendering-ready shape of one built-in connector's
// pconnector.Specification.
type connectorInfo struct {
	Name              string
	Summary           string
	Description       string
	Version           string
	Author            string
	SourceParams      []paramInfo
	DestinationParams []paramInfo
}

// paramInfo is one config.Parameter, flattened for rendering.
type paramInfo struct {
	Key         string
	Type        string
	Default     string
	Required    bool
	Description string
	Validations []string
}

// unsetConnectorVersions are the placeholder strings every built-in
// connector's own Specification.Version carries when NewSpecification() is
// called directly (see gatherConnectors's doc). "(devel)" is the literal
// default every conduit-connector-* module hardcodes
// (`var version = "(devel)"`, overwritten only by
// builtin.Registry.Init's debug.BuildInfo path, which this generator
// deliberately does not use — D2/D3).
var unsetConnectorVersions = map[string]bool{
	"":        true,
	"(devel)": true,
}

// gatherConnectors reads every built-in connector's specification directly
// (conn.NewSpecification()) — never through builtin.Registry, which builds
// a dispenser and overwrites Specification.Version from
// runtime/debug.BuildInfo (registry.go's getModuleVersion). BuildInfo.Deps
// is empty under `go generate`/`go run`/`go test`, so reading through the
// registry would make the emitted version nondeterministic across
// invocation contexts — see the design doc's D2 and the "Version caveat"
// in its Context section.
//
// Every built-in connector's own Specification.Version is therefore always
// the literal placeholder "(devel)" when read this way (confirmed against
// all six conduit-connector-* modules pinned in go.mod: each does `var
// version = "(devel)"` and only overwrites it via the registry's build-info
// path this generator bypasses). gatherConnectors replaces that
// placeholder with the module's pinned version from go.mod — a committed,
// deterministic file — via modVersions.
func gatherConnectors(modVersions moduleVersions) ([]connectorInfo, error) {
	out := make([]connectorInfo, 0, len(builtin.DefaultBuiltinConnectors))

	for modulePath, conn := range builtin.DefaultBuiltinConnectors {
		if conn.NewSpecification == nil {
			return nil, fmt.Errorf("connector %s: NewSpecification is nil", modulePath)
		}
		spec := conn.NewSpecification()

		version := spec.Version
		if unsetConnectorVersions[version] {
			v, ok := modVersions[modulePath]
			if !ok {
				return nil, fmt.Errorf(
					"connector %s: Specification.Version is unset (%q) and go.mod has no require line for %s",
					spec.Name, spec.Version, modulePath,
				)
			}
			version = v
		}

		out = append(out, connectorInfo{
			Name:              spec.Name,
			Summary:           spec.Summary,
			Description:       spec.Description,
			Version:           version,
			Author:            spec.Author,
			SourceParams:      gatherParams(spec.SourceParams),
			DestinationParams: gatherParams(spec.DestinationParams),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// gatherParams flattens a config.Parameters map into a slice sorted by key
// — Parameters is a Go map, so iterating it directly would be
// nondeterministic (design doc D3).
func gatherParams(params config.Parameters) []paramInfo {
	if len(params) == 0 {
		return nil
	}

	out := make([]paramInfo, 0, len(params))
	for key, p := range params {
		out = append(out, paramInfo{
			Key:         key,
			Type:        p.Type.String(),
			Default:     p.Default,
			Required:    isRequired(p),
			Description: p.Description,
			Validations: renderValidations(p),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// isRequired reports whether p carries a required validation.
func isRequired(p config.Parameter) bool {
	for _, v := range p.Validations {
		if v.Type() == config.ValidationTypeRequired {
			return true
		}
	}
	return false
}

// renderValidations renders each of p's validations as "type" or
// "type(value)" when the validation carries a value (e.g.
// "greater-than(0)", "inclusion(a,b,c)"). Validations is already a slice
// (declaration order from the connector's YAML spec), not a map, so no
// re-sort is needed for determinism — order is preserved as authored.
func renderValidations(p config.Parameter) []string {
	if len(p.Validations) == 0 {
		return nil
	}
	out := make([]string, 0, len(p.Validations))
	for _, v := range p.Validations {
		if val := v.Value(); val != "" {
			out = append(out, fmt.Sprintf("%s(%s)", v.Type(), val))
		} else {
			out = append(out, v.Type().String())
		}
	}
	return out
}
