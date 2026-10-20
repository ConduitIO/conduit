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

// Package kcmigrate is the engine behind `conduit migrate kafka-connect`,
// `conduit kc lint` and `conduit kc diff`. It reads Kafka Connect worker and
// connector configs into an intermediate representation (IR) and describes
// the outcome of a migration as a compatibility [Report].
//
// The designs are docs/design-documents/20261012-migrate-kafka-connect.md
// (the command surface) and 20261014-migrate-kafka-connect-mapping-and-harness.md
// (the report contract, rule data and corpus), which takes precedence where
// the two differ.
//
// # Core types
//
//   - [Input] is one named piece of content. The name is a label used in
//     report origins; this package never opens it, so the CLI and the MCP
//     tools share one code path (content in, content out).
//   - [Connector] and [Worker] are the IR. A connector's config is an ordered
//     list of [KV], one per occurrence of a key in the input, each with the
//     [Origin] (file and line/column, or JSON pointer) it came from.
//   - [Value] holds an input value. Its String, Format, GoString and JSON
//     forms are redacted, so a value can only reach output through an
//     explicit [Value.Reveal] call.
//   - [Report] is the compatibility report, the `result` of the CLI's --json
//     envelope and the content of report.json. Its JSON form is a public
//     contract described by the schema embedded in package kcmigrate/schema.
//     [Entry] is the outcome for one input key occurrence, [Note] a
//     connector-level difference with no input key.
//
// # Invariants
//
//   - Completeness: parsers never drop a key. Every occurrence of every key
//     in a connector config becomes one [KV], duplicates included, in input
//     order, and a report has one entry per KV, so the multiset of input keys
//     equals the multiset of entry keys. A parser that cannot represent an
//     input fails with a coded error instead of skipping part of it. Notes
//     are outside that multiset.
//   - No secrets in output: values are redacted by default (see [Value]);
//     parse errors name locations and keys, never values.
//   - Every approximated or unsupported entry has a code and a reason; every
//     unsupported or blocking entry, and every blocking note, has a fix.
//     [Report.Validate] enforces this for producers.
//   - Determinism: parsing and [MarshalReport] depend only on the input bytes
//     and names; no map iteration order, clock or host reaches output.
//
// # What counts as an input key (pinned for the migration harness, #3071)
//
// An input key is one occurrence of a connector config key: a key of a
// .properties file, a member of "config" in the REST form
// ({"name": ..., "config": {...}}), a member of a bare config map, or a
// member of "info.config" in a REST export. Each gets exactly one entry.
//
// The members around the config are not config keys and get no entry: the
// REST form's top-level "name", "type", "tasks" and "initial_state", and a
// REST export's map key, "info.name", "info.type", "info.tasks" and
// "status". They set the connector's name and state, which the report shows
// as connectors[].name and in pipelineStatus. They are still accounted for:
// a wrapper member the parser does not know fails the input
// (migrate.input_unrecognized), and so does a top-level name that differs
// from the config's "name", as Kafka Connect itself rejects it.
//
// Differences with no input key are connector-level notes, outside the
// multiset. kc.converter_unknown is such a note: when neither the connector
// nor the --worker file sets key.converter or value.converter, the wire
// format is unknown, so the note is blocking (one note per missing
// converter), with the fix "pass --worker or set the converter on the
// connector".
//
// The MCP tool kc_migrate returns this report as "result" of the same
// envelope the CLI prints, and the generated files as "files" next to it, at
// the top level, so "result" is the same document as report.json.
//
// This package does no I/O: no file system, network or database access.
package kcmigrate
