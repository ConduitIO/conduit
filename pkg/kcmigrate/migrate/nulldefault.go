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

package migrate

import (
	"strings"

	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
)

// debeziumUpstream is the rules' upstream name for the Debezium source
// families.
const debeziumUpstream = "debezium"

// keyReplaceNull is the JsonConverter setting, under a "key." or "value."
// converter prefix, that decides whether a NULL in a column with a default is
// written as the default.
const keyReplaceNull = "replace.null.with.default"

// nullDefaultNotes returns a non-blocking kc.default_differs note for a
// Debezium source connector whose key or value converter does not explicitly
// set replace.null.with.default=false. Kafka Connect's converters default it
// to true, so a NULL in a column that has a default was written as the
// default; Conduit's source writes the NULL. Translating is still right, so
// the note does not block, but a consumer that relied on the default must now
// handle null.
//
// The setting is read the way Kafka Connect configures a converter: from the
// connector's <side>.converter.* keys when the connector names the converter,
// else from the worker's. A side whose converter writes no schema
// (ByteArrayConverter, StringConverter) has no column defaults to apply and
// is skipped; any other converter is treated like JsonConverter, since this
// package models no other converter's behavior.
func nullDefaultNotes(cl rules.Classified, worker []rules.Decision) []kcmigrate.Note {
	if cl.Rules == nil || cl.Rules.Scope != rules.ScopeSource || cl.Rules.Upstream.Name != debeziumUpstream {
		return nil
	}
	var paths []string
	for _, side := range []string{"key", "value"} {
		if replacesNullWithDefault(cl.Decisions, worker, side) {
			paths = append(paths, side+".converter."+keyReplaceNull)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	return []kcmigrate.Note{{
		Code: kcmigrate.CodeDefaultDiffers.Reason(),
		Rule: strings.TrimSuffix(cl.Rules.Name(), ".yaml") + "/notes/replace-null-with-default",
		Reason: "Kafka Connect wrote a column's default in place of NULL, because replace.null.with.default is true by default and " +
			unsetPhrase(paths) + " Conduit writes NULL. Consumers that relied on the default must handle null.",
		Fix: "None needed in Conduit. To compare outputs, set key.converter.replace.null.with.default=false and " +
			"value.converter.replace.null.with.default=false on the Kafka Connect side.",
	}}
}

// replacesNullWithDefault reports whether side's converter would write a
// column default in place of NULL.
func replacesNullWithDefault(connector, worker []rules.Decision, side string) bool {
	prefix := side + ".converter"
	layer := worker
	for _, l := range [][]rules.Decision{connector, worker} {
		if _, ok := lastValue(l, prefix); ok {
			layer = l
			break
		}
	}
	class, _ := lastValue(layer, prefix)
	if class == byteArrayConverter || class == stringConverter {
		return false
	}
	if class == "" {
		// No converter class anywhere (kc.converter_unknown blocks): read the
		// setting from the connector, then the worker, so a lone setting counts.
		for _, l := range [][]rules.Decision{connector, worker} {
			if v, ok := lastValue(l, prefix+"."+keyReplaceNull); ok {
				return !strings.EqualFold(v, "false")
			}
		}
		return true
	}
	v, ok := lastValue(layer, prefix+"."+keyReplaceNull)
	return !ok || !strings.EqualFold(v, "false")
}

// unsetPhrase says which of the setting paths are not false.
func unsetPhrase(paths []string) string {
	if len(paths) == 1 {
		return paths[0] + " is not set to false."
	}
	return "neither " + paths[0] + " nor " + paths[1] + " is set to false."
}
