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
// `conduit kc lint` and `conduit kc diff`. It describes the outcome of a
// migration as a compatibility [Report].
//
// The designs are docs/design-documents/20261012-migrate-kafka-connect.md
// (the command surface) and 20261014-migrate-kafka-connect-mapping-and-harness.md
// (the report contract, rule data and corpus), which takes precedence where
// the two differ.
//
// # Core types
//
//   - [Report] is the compatibility report, the `result` of the CLI's --json
//     envelope and the content of report.json. Its JSON form is a public
//     contract described by the schema embedded in package kcmigrate/schema.
//   - [Entry] is the outcome for one input key occurrence, [Note] a
//     connector-level difference with no input key.
//   - [Origin] locates a key in the input: file and line/column, or a JSON
//     pointer. It never carries a value.
//
// # Invariants
//
//   - Completeness: a report has one entry per input key occurrence, so the
//     multiset of input keys equals the multiset of entry keys. Notes are
//     outside that multiset.
//   - Every approximated or unsupported entry has a code and a reason; every
//     unsupported or blocking entry, and every blocking note, has a fix.
//     [Report.Validate] enforces this for producers.
//   - Determinism: [MarshalReport] depends only on the report's content.
//
// This package does no I/O: no file system, network or database access.
package kcmigrate
