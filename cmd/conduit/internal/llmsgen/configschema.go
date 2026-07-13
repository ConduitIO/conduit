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
	"reflect"
	"sort"
	"strconv"
	"strings"

	v1 "github.com/conduitio/conduit/pkg/provisioning/config/yaml/v1"
	v2 "github.com/conduitio/conduit/pkg/provisioning/config/yaml/v2"
)

// configField is one exported struct field, reflection-derived.
type configField struct {
	GoField  string
	YAMLKey  string
	GoType   string
	Required bool
}

// configStruct is one v2 schema struct's fields, in declaration order.
type configStruct struct {
	Name   string
	Fields []configField
}

// configSchema is the rendering-ready shape of Source #1 (pipeline config
// schema).
type configSchema struct {
	LatestVersion string // v2.LatestVersion, e.g. "2.2"
	Versions      []string
	Structs       []configStruct
	LegacyVersion string // v1.LatestVersion, e.g. "1.1"
}

// gatherConfigSchema reflects over the v2 config structs — Configuration,
// Pipeline, Connector, Processor, DLQ, in that fixed declaration order —
// rather than hand-listing their fields, so a new field can't be added to
// any of them without appearing in the generated output (design doc D2).
func gatherConfigSchema() configSchema {
	return configSchema{
		LatestVersion: v2.LatestVersion,
		Versions:      sortedChangelogVersions(),
		Structs: []configStruct{
			reflectStruct("Configuration", reflect.TypeOf(v2.Configuration{})),
			reflectStruct("Pipeline", reflect.TypeOf(v2.Pipeline{})),
			reflectStruct("Connector", reflect.TypeOf(v2.Connector{})),
			reflectStruct("Processor", reflect.TypeOf(v2.Processor{})),
			reflectStruct("DLQ", reflect.TypeOf(v2.DLQ{})),
		},
		LegacyVersion: v1.LatestVersion,
	}
}

// sortedChangelogVersions returns v2.Changelog's version keys in ascending
// semver order. v2.Changelog is a map (internal.Changelog) — iterating it
// directly would be nondeterministic (design doc D3), and its type lives in
// an internal package this generator cannot import (pkg/provisioning/
// config/yaml/internal is only importable from that tree), so this ranges
// over the exported map value without ever naming its type.
func sortedChangelogVersions() []string {
	versions := make([]string, 0, len(v2.Changelog))
	for version := range v2.Changelog {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool { return versionLess(versions[i], versions[j]) })
	return versions
}

// versionLess compares two dotted numeric versions ("2.10" > "2.9")
// numerically per segment, not lexicographically. It is intentionally
// narrow (no pre-release/build-metadata handling) — every version this
// generator ever sorts is a plain "<major>.<minor>" pipeline-config
// version.
func versionLess(a, b string) bool {
	pa := strings.Split(a, ".")
	pb := strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, _ := strconv.Atoi(pa[i])
		nb, _ := strconv.Atoi(pb[i])
		if na != nb {
			return na < nb
		}
	}
	return len(pa) < len(pb)
}

// reflectStruct walks t's exported fields in declaration order — reflect's
// NumField/Field(i) always iterate in source order, so unlike a map this
// needs no sort for determinism (design doc D3).
func reflectStruct(name string, t reflect.Type) configStruct {
	fields := make([]configField, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		fields = append(fields, configField{
			GoField: f.Name,
			YAMLKey: yamlKey(f),
			GoType:  friendlyType(f.Type),
			// Required is a documented convention, not a source-of-truth
			// tag (none of these structs carry one): a pointer field
			// (*int, e.g. DLQ.WindowSize) is optional, everything else is
			// always present (holding its zero value if unset). Purely a
			// function of the field's Go type, so still deterministic and
			// source-derived.
			Required: f.Type.Kind() != reflect.Pointer,
		})
	}
	return configStruct{Name: name, Fields: fields}
}

// yamlKey returns f's `yaml:"..."` tag key, or f.Name if untagged.
func yamlKey(f reflect.StructField) string {
	tag := f.Tag.Get("yaml")
	if tag == "" {
		return f.Name
	}
	key, _, _ := strings.Cut(tag, ",")
	return key
}

// friendlyType renders t's type name for the generated doc, stripped of
// the "v2." package qualifier reflect.Type.String() would otherwise
// include for sibling types (e.g. "[]v2.Connector" -> "[]Connector") —
// every struct this generator reflects over lives in package v2, so the
// qualifier is redundant noise for a reader.
func friendlyType(t reflect.Type) string {
	return strings.ReplaceAll(t.String(), "v2.", "")
}
