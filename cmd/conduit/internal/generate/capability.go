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

import "github.com/conduitio/conduit/pkg/provisioning/config"

// capabilityProcessors maps a required-capability tag (as used in a
// Request's Expect.RequiredCapabilities) to the set of builtin processor
// plugin names that satisfy it. Sourced by hand from
// pkg/plugin/processor/builtin.DefaultBuiltinProcessors's keys at the time
// this package was written — never a second, independently-maintained
// processor list; if a builtin processor is renamed or removed, this map
// needs a matching update (there is no structural link enforcing it, since
// importing the builtin processor registry here would pull plugin runtime
// dependencies into a package that only ever handles YAML text — a cost not
// worth paying for a compile-time guarantee testdata already exercises).
//
// A capability tag NOT present in this map is intentionally
// unsatisfiable (hasCapability returns false for it) rather than matching
// everything or nothing gracefully — an eval fixture typo'ing a capability
// name should show up as a permanent semantic-match failure, not silently
// pass.
var capabilityProcessors = map[string]map[string]bool{
	"filter":              {"filter": true},
	"mask":                {"field.exclude": true},
	"rename":              {"field.rename": true},
	"set":                 {"field.set": true},
	"convert":             {"field.convert": true},
	"json-encode":         {"json.encode": true},
	"json-decode":         {"json.decode": true},
	"avro-encode":         {"avro.encode": true},
	"avro-decode":         {"avro.decode": true},
	"base64-encode":       {"base64.encode": true},
	"base64-decode":       {"base64.decode": true},
	"unwrap-debezium":     {"unwrap.debezium": true},
	"unwrap-kafkaconnect": {"unwrap.kafkaconnect": true},
	"unwrap-opencdc":      {"unwrap.opencdc": true},
	"split":               {"split": true},
	"clone":               {"clone": true},
	"webhook":             {"webhook.http": true},
	"embed":               {"openai.embed": true, "cohere.embed": true},
	"textgen":             {"openai.textgen": true, "cohere.command": true, "ollama.request": true},
}

// hasCapability reports whether any processor in procs (pipeline-level or
// attached to a connector — allProcessors flattens both) is one of the
// builtin plugins capabilityProcessors registers for tag. An unknown tag
// (see the map's doc comment) always returns false.
func hasCapability(procs []config.Processor, tag string) bool {
	plugins, ok := capabilityProcessors[tag]
	if !ok {
		return false
	}
	for _, p := range procs {
		if plugins[p.Plugin] {
			return true
		}
	}
	return false
}
