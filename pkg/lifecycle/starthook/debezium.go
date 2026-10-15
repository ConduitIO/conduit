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

package starthook

import (
	"fmt"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/plugin"
	"google.golang.org/grpc/codes"
)

// DebeziumEncodePlugin is the plugin name of the processor that encodes
// Debezium change events. Any reference with this name matches, whatever its
// type prefix or version.
const DebeziumEncodePlugin = "debezium.encode"

// CodeDebeziumStandaloneBeforeEncode is returned by Start when a pipeline
// contains a debezium.encode processor and a record could reach it through a
// standalone plugin: a standalone source connector, a standalone (WASM)
// processor that runs before it on the same path, or a debezium.encode that
// itself resolves to a standalone plugin (design
// docs/design-documents/20261012-debezium-compatible-output.md, decision 18).
//
// The plugin protocol carries record values as a protobuf Struct, which turns
// int64 into float64 (exact only up to 2^53) and bytes into base64 strings.
// debezium.encode detects the result and refuses the record with
// debezium.record_schema_mismatch, so the pipeline would fail on its first
// such record instead of encoding it. Refusing at start makes that an
// actionable error before anything is read or acked.
//
// "Before it on the same path" follows execution order: a source's own
// processors, then the pipeline processors, then a destination's own
// processors. For a debezium.encode on a source, only that source and its
// earlier processors count; for one on the pipeline, every source and source
// processor and the earlier pipeline processors; for one on a destination,
// all of those plus the destination's earlier processors. A processor with a
// condition counts too: it may run on any record.
//
// A plugin reference counts as standalone when the plugin services would
// resolve it to one: a "standalone:" prefix, or no prefix and a standalone
// plugin of that name (and version, if given) is installed, since standalone
// plugins take precedence over built-in ones.
//
// The error names every offender. For a pipeline from a config document,
// ConfigPath points at the first one (/connectors/<n>, /processors/<i> or
// /connectors/<n>/processors/<i>); a pipeline created through the API has no
// document, so the error carries no ConfigPath. Fix: use built-in plugins on
// the path into debezium.encode (the Debezium mode needs the built-in Postgres
// source), or take the standalone processors off that path, for example onto
// a destination that does not encode. Moving them after debezium.encode is
// not a fix: they would see encoded bytes, and debezium.encode must be last.
var CodeDebeziumStandaloneBeforeEncode = conduiterr.Register("pipeline.debezium_standalone_before_encode", codes.FailedPrecondition)

type boundaryOffender struct {
	key        string // dedupe key
	label      string
	configPath string
}

func isEncoder(p Processor) bool {
	return plugin.FullName(p.Plugin).PluginName() == DebeziumEncodePlugin
}

// checkStandaloneBeforeEncode enforces decision 18. It resolves no plugin
// unless the pipeline contains debezium.encode.
func checkStandaloneBeforeEncode(t Topology, r Resolver) error {
	var offenders []boundaryOffender
	seen := map[string]bool{}
	hit := false // the current encoder has a standalone plugin upstream
	add := func(o boundaryOffender) {
		hit = true
		if !seen[o.key] {
			seen[o.key] = true
			offenders = append(offenders, o)
		}
	}

	// sourceSide adds the standalone plugins of source s and of its first n
	// processors.
	sourceSide := func(s Connector, n int) {
		if r.Connector(s.Plugin) {
			add(boundaryOffender{
				key:        "c:" + s.ID,
				label:      fmt.Sprintf("source connector %q uses standalone plugin %q", userID(t, "", s.ID), s.Plugin),
				configPath: connectorPointer(s.Index),
			})
		}
		connProcs(s, "source", n, add, t, r)
	}
	allSources := func() {
		for _, s := range t.Sources {
			sourceSide(s, len(s.Processors))
		}
	}
	pipelineProcs := func(n int) {
		for i, p := range t.Processors[:n] {
			if r.Processor(p.Plugin) {
				add(boundaryOffender{
					key:        "p:" + p.ID,
					label:      fmt.Sprintf("pipeline processor %q uses standalone plugin %q", userID(t, "", p.ID), p.Plugin),
					configPath: processorPointer(i),
				})
			}
		}
	}

	// encoders names the encoders that have a standalone plugin upstream.
	var encoders []string
	visit := func(id string, upstream func()) {
		hit = false
		upstream()
		if hit {
			encoders = append(encoders, id)
		}
	}
	// In each upstream, n = i+1 includes the encoder itself: one that
	// resolves to a standalone plugin receives records over the protocol too.
	for _, s := range t.Sources {
		for i, p := range s.Processors {
			if isEncoder(p) {
				visit(userID(t, s.ID, p.ID), func() { sourceSide(s, i+1) })
			}
		}
	}
	for i, p := range t.Processors {
		if isEncoder(p) {
			visit(userID(t, "", p.ID), func() {
				allSources()
				pipelineProcs(i + 1)
			})
		}
	}
	for _, d := range t.Destinations {
		for i, p := range d.Processors {
			if isEncoder(p) {
				visit(userID(t, d.ID, p.ID), func() {
					allSources()
					pipelineProcs(len(t.Processors))
					connProcs(d, "destination", i+1, add, t, r)
				})
			}
		}
	}

	if len(offenders) == 0 {
		return nil
	}
	return errStandaloneBeforeEncode(t, encoders, offenders)
}

// connProcs adds the standalone processors among the first n processors of
// connector c.
func connProcs(c Connector, kind string, n int, add func(boundaryOffender), t Topology, r Resolver) {
	for i, p := range c.Processors[:n] {
		if r.Processor(p.Plugin) {
			add(boundaryOffender{
				key: "p:" + p.ID,
				label: fmt.Sprintf("processor %q on %s connector %q uses standalone plugin %q",
					userID(t, c.ID, p.ID), kind, userID(t, "", c.ID), p.Plugin),
				configPath: connectorProcessorPointer(c.Index, i),
			})
		}
	}
}

// errStandaloneBeforeEncode builds the refusal. encoders are the encoder IDs as
// the user wrote them.
func errStandaloneBeforeEncode(t Topology, encoders []string, offenders []boundaryOffender) error {
	ids := make([]string, len(encoders))
	for i, id := range encoders {
		ids[i] = fmt.Sprintf("%q", id)
	}
	parts := make([]string, len(offenders))
	for i, o := range offenders {
		parts[i] = o.label
		if t.FromConfig {
			parts[i] += " (" + o.configPath + ")"
		}
	}
	ce := conduiterr.New(
		CodeDebeziumStandaloneBeforeEncode,
		fmt.Sprintf(
			"can't start pipeline %s: records reach %s processor %s through a standalone plugin: %s; "+
				"the plugin protocol turns int64 values into floats and bytes into base64, "+
				"so %s would refuse those records",
			t.PipelineID, DebeziumEncodePlugin, strings.Join(ids, ", "), strings.Join(parts, "; "), DebeziumEncodePlugin,
		),
	)
	ce.Suggestion = "use built-in plugins on the path into " + DebeziumEncodePlugin +
		" (builtin:<name>; the Debezium mode needs the built-in Postgres source), " +
		"or take the standalone processors off that path, for example onto a destination that does not encode; " +
		"do not move them after " + DebeziumEncodePlugin + ", which must be the last processor"
	if t.FromConfig {
		ce.ConfigPath = offenders[0].configPath
	}
	return ce
}
