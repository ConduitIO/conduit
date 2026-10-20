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

// Package valuecorrectness checks that the values Conduit writes are the
// values it was asked to write, when several pipelines share one process.
//
// It exists because of conduit-commons#305. The Avro codec kept one encoder
// cache for the whole process, keyed by a schema string in which a reference
// to a named type is only its name. Two pipelines whose schemas defined the
// same type name differently encoded with each other's definitions. Every
// record was written and acked, nothing was logged, and the bytes were valid
// Avro, so no existing test noticed: they all ran one schema per process and
// checked that encoding succeeded, not what it produced.
//
// The test runs a real Conduit runtime (in-memory store, built-in schema
// registry, built-in connectors and processors) with several pipelines whose
// registered schemas overlap: the same full names for a record, an enum and a
// decimal fixed type, with different contents, referenced by name inside
// arrays. Pipelines are started one at a time, so the first one always warms
// any shared cache before the next one encodes, which makes a cross-pipeline
// leak deterministic instead of a race.
//
// For every record written it checks two things against expectations that do
// not depend on any state inside the runtime:
//   - the bytes: the Confluent wire header names the schema ID the registry
//     assigned to that pipeline's subject and version, and the Avro body is
//     byte-for-byte what a fresh codec config, which has only ever seen that
//     one schema, produces for the expected value;
//   - the values: decoding the body with that schema, again on a fresh config,
//     gives back the expected value.
//
// A second test (fanout_test.go) covers conduit#3151 / conduit-commons#336:
// one source into three destinations, on the classic and the arch-v2 engine,
// where two destinations edit nested arrays and maps of their record in place
// with custom.javascript and the third has no processor. Each destination's
// output is compared with the exact value it must carry, so an
// opencdc.Record.Clone that shares nested data between fan-out branches shows
// as one branch writing another's edit.
//
// Invariant 6 (schema handling never silently mangles data) is what this
// package guards. It runs nightly and on data-path pull requests through
// .github/workflows/value-correctness.yml, and is skipped elsewhere unless
// CONDUIT_VALUE_CORRECTNESS=1 is set.
package valuecorrectness
