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

// Settings that stop a converter writing a column's default in place of NULL.
const (
	// keyReplaceNull is JsonConverter's (Kafka Connect 3.5 and later, KIP-581):
	// true by default; false keeps NULL.
	keyReplaceNull = "replace.null.with.default"
	// keyIgnoreDefault is the Confluent Avro, Protobuf and JSON Schema
	// converters' (schema-registry AbstractDataConfig): false by default;
	// true keeps NULL.
	keyIgnoreDefault = "ignore.default.for.nullables"
)

// Confluent converters that take keyIgnoreDefault.
var confluentConverters = map[string]bool{
	"io.confluent.connect.avro.AvroConverter":         true,
	"io.confluent.connect.protobuf.ProtobufConverter": true,
	"io.confluent.connect.json.JsonSchemaConverter":   true,
}

// nullDefaultNotes returns a non-blocking kc.default_differs note for a
// Debezium source connector whose key or value converter may write a
// column's schema default in place of NULL. Conduit's source writes the NULL.
// Translating is still right, so the note does not block, but a consumer
// that relied on the default must now handle null.
//
// Settings are read the way Kafka Connect configures a converter: from the
// connector's <side>.converter.* keys when the connector names the converter,
// else from the worker's. A side whose converter writes no schema
// (ByteArrayConverter, StringConverter) has no defaults to apply and is
// skipped. JsonConverter and the Confluent schema converters are checked
// against their own setting; any other converter is noted without a setting
// name, since this package does not model it.
func nullDefaultNotes(cl rules.Classified, worker []rules.Decision) []kcmigrate.Note {
	if cl.Rules == nil || cl.Rules.Scope != rules.ScopeSource || cl.Rules.Upstream.Name != debeziumUpstream {
		return nil
	}
	var found, fixes []string
	for _, side := range []string{"key", "value"} {
		f, fix := nullDefaultSide(cl.Decisions, worker, side)
		if f != "" {
			found, fixes = append(found, f), append(fixes, fix)
		}
	}
	if len(found) == 0 {
		return nil
	}
	return []kcmigrate.Note{{
		Code: kcmigrate.CodeDefaultDiffers.Reason(),
		Rule: strings.TrimSuffix(cl.Rules.Name(), ".yaml") + "/notes/replace-null-with-default",
		Reason: "Kafka Connect's converters write a column's schema default, when it has one, in place of NULL. Conduit writes NULL. " +
			"Consumers that relied on the default must handle null. " + strings.Join(found, " "),
		Fix: "None needed in Conduit. To compare outputs on the Kafka Connect side: " + strings.Join(fixes, " ") +
			" Transforms in the chain have their own replace.null.with.default setting (Kafka Connect 3.9 and later).",
	}}
}

// nullDefaultSide returns, for side ("key" or "value"), what the report says
// when its converter may write a default in place of NULL and the setting
// that would stop it; both are empty when it does not.
func nullDefaultSide(connector, worker []rules.Decision, side string) (found, fix string) {
	prefix := side + ".converter"
	layer := worker
	for _, l := range [][]rules.Decision{connector, worker} {
		if _, ok := lastValue(l, prefix); ok {
			layer = l
			break
		}
	}
	class, _ := lastValue(layer, prefix)
	switch {
	case class == byteArrayConverter || class == stringConverter:
		return "", ""
	case class == jsonConverter:
		path := prefix + "." + keyReplaceNull
		if v, ok := lastValue(layer, path); ok && strings.EqualFold(v, "false") {
			return "", ""
		}
		return path + " is not set to false (JsonConverter's default is true).",
			"set " + path + "=false (Kafka Connect 3.5 and later)."
	case confluentConverters[class]:
		path := prefix + "." + keyIgnoreDefault
		if v, ok := lastValue(layer, path); ok && strings.EqualFold(v, "true") {
			return "", ""
		}
		return path + " is not set to true (the Confluent converters' default is false).",
			"set " + path + "=true."
	}
	return prefix + " is not a converter this version of migrate models, and it may write defaults in place of NULL.",
		"check the documentation of " + prefix + " for a setting that keeps NULL."
}
