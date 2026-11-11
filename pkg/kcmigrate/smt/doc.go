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

// Package smt translates a Kafka Connect connector's single message
// transform (SMT) and predicate chain into Conduit processors, for
// `conduit migrate kafka-connect` and the kc_migrate MCP tool, which share
// this code through package kcmigrate/migrate. The design is
// docs/design-documents/20261110-kafka-connect-smt-pack.md. This package
// does no I/O.
//
// # How it runs
//
// The shared rule data claims every transforms, transforms.*, predicates
// and predicates.* key with the smt emit kind and a placeholder outcome,
// unsupported and blocking. For every class that produces a pipeline,
// migrate calls [Apply] after the class's finishers and before the report's
// entries are taken. Apply groups the keys per alias in transforms= order,
// translates each SMT with a [Translator] from the [Registry], decides every
// claimed key in place, and returns the processors to append and the
// report's transform index. Classes that produce no pipeline never reach
// it; their keys keep the class's outcome and their index stays empty.
//
// # Core types
//
//   - [Translator] and [PredicateTranslator] turn one SMT or predicate
//     into processors or a condition, or refuse it with a [Refusal]. They
//     are registered by class without the $Key/$Value suffix. [Builtin] has
//     none yet: a translator ships with the Kafka Connect oracle output that
//     proves it exact. Package smttest holds fixtures for tests only.
//   - [Chain] is what a translator reads: chain side, schema mode, record
//     shapes and whether each side's converter carries a schema (both from
//     the effective converter, connector over worker), plugin [Capability]
//     rows, the topic expression and whether an unwrap point came earlier.
//   - [Data] holds the capability table and the refusal of every class
//     without a translator (Go tables, not rule data).
//
// # Invariants
//
//   - Completeness: every claimed key gets exactly one outcome. A key Apply
//     leaves undecided is an error, never a dropped entry; an option no
//     translator claims is kc.key_unknown and blocking.
//   - Emit-if-exact: a translator refuses every configuration outside the
//     subset where its processors behave as the SMT does. Every refusal
//     blocks (invariant 6: transforms change data).
//   - Grouping follows Kafka Connect: aliases from transforms= and
//     predicates= (Java trim and \s), keys by alias. Two aliases of which
//     one is a dotted prefix of the other share keys in Kafka Connect, so
//     both are refused. A config-provider reference blocks wherever it is,
//     under an unlisted alias or an unused predicate too.
//   - All or nothing: one refused link means no processor of the chain is
//     written, and every other link's entries name it. Under --accept-gaps
//     the pipeline runs with none of the chain's processors.
//   - Nil sides: NilGuard is true only for a non-nil interface, so a
//     delete's after-image or a tombstone never becomes an empty row
//     (#3147); every Before copy of a value-side processor is nil-guarded.
//   - Schema metadata: before a processor that reshapes a side, the chain
//     removes that side's schema subject and version under the same
//     condition, once per distinct condition, so no widened schema is
//     registered under the source's subject.
//   - Topic: the topic expression is evaluated against the final record, so
//     an SMT whose topic reads what a later SMT writes is refused.
//   - Determinism: output depends only on the keys and values, in
//     transforms= order; permuting input keys changes nothing, and renaming
//     aliases changes only the processor IDs (smt-<alias>).
package smt
