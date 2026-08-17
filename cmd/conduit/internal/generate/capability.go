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
// Capability tags are a shared vocabulary: the eval corpus names them in
// requiredCapabilities, intent.go extracts them from a prompt, and this file
// maps them to the builtin processors that satisfy them. They are declared
// once here so those three uses cannot drift into three spellings.
// procFilter is the builtin PROCESSOR PLUGIN named "filter". It shares its
// spelling with capFilter (the capability tag) and nothing else: one names a
// plugin the engine can load, the other names an intent the corpus can ask
// for. They are separate constants so a rename of either cannot silently
// rewrite the other.
const procFilter = "filter"

const (
	capFilter             = "filter"
	capMask               = "mask"
	capRename             = "rename"
	capSet                = "set"
	capConvert            = "convert"
	capJSONEncode         = "json-encode"
	capJSONDecode         = "json-decode"
	capAvroEncode         = "avro-encode"
	capAvroDecode         = "avro-decode"
	capBase64Encode       = "base64-encode"
	capBase64Decode       = "base64-decode"
	capUnwrapDebezium     = "unwrap-debezium"
	capUnwrapKafkaconnect = "unwrap-kafkaconnect"
	capUnwrapOpencdc      = "unwrap-opencdc"
	capSplit              = "split"
	capClone              = "clone"
	capWebhook            = "webhook"
	capEmbed              = "embed"
	capTextgen            = "textgen"
)

var capabilityProcessors = map[string]map[string]bool{
	capFilter:             {procFilter: true},
	capMask:               {"field.exclude": true},
	capRename:             {"field.rename": true},
	capSet:                {"field.set": true},
	capConvert:            {"field.convert": true},
	capJSONEncode:         {"json.encode": true},
	capJSONDecode:         {"json.decode": true},
	capAvroEncode:         {"avro.encode": true},
	capAvroDecode:         {"avro.decode": true},
	capBase64Encode:       {"base64.encode": true},
	capBase64Decode:       {"base64.decode": true},
	capUnwrapDebezium:     {"unwrap.debezium": true},
	capUnwrapKafkaconnect: {"unwrap.kafkaconnect": true},
	capUnwrapOpencdc:      {"unwrap.opencdc": true},
	capSplit:              {"split": true},
	capClone:              {"clone": true},
	capWebhook:            {"webhook.http": true},
	capEmbed:              {"openai.embed": true, "cohere.embed": true},
	capTextgen:            {"openai.textgen": true, "cohere.command": true, "ollama.request": true},
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
