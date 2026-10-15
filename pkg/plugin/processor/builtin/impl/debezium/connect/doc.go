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

// Package connect is a Go model of the Kafka Connect data API (schemas and
// values) and a byte-exact port of the Kafka Connect JsonConverter, as far as
// the Debezium-compatible output mode needs them.
//
// It exists because the Debezium-compatible output mode
// (docs/design-documents/20261012-debezium-compatible-output.md) must produce
// the same bytes a Kafka Connect worker produces for the same Connect data.
// The golden files under tests/debezium/testdata/golden, captured from real
// Debezium, are the specification; the tests in this package compare against
// them byte for byte.
//
// Core types:
//
//   - [Schema] is a Connect schema: a physical [Type], optionality, name,
//     version, doc, ordered parameters, a default, and the child schemas of
//     arrays, maps and structs. Field and parameter order is preserved
//     everywhere, because the converter output and the Schema Registry schema
//     identity depend on it.
//   - Values are a closed set of Go types (see [FromGo]): int64 for every
//     integer type, float32, float64, bool, string, []byte, []any for arrays,
//     []MapEntry for maps, *Struct for structs and [Decimal] for the Connect
//     Decimal logical type.
//   - [JSONConverter] serializes a schema and value as JsonConverter does.
//
// Invariants:
//
//   - Invariant 6 (no silent mangling): [FromGo] never coerces a value across
//     types, never rounds and never truncates. A value that does not fit its
//     schema is an error with a stable code (see codes.go), never a best
//     effort.
//   - Order: struct fields, map entries, array elements and schema parameters
//     are written in the order they were given, never in Go map order.
//
// The package has no dependency on the record model (opencdc) or on any
// connector; the envelope builder in the parent package maps records onto it.
package connect
