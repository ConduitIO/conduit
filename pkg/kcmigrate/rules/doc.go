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

// Package rules classifies Kafka Connect config keys with rule data: one
// YAML file per connector class (scope source or sink), one for the keys
// every connector shares (scope shared), and one for worker properties
// (scope worker). The format is specified in
// docs/design-documents/20261014-migrate-kafka-connect-mapping-and-harness.md,
// "Rule data". A rule is data; what an emit kind produces is code.
//
// # Matching
//
// A key is looked up in its class's file, then in the shared file. Within a
// file an exact rule wins over a prefix rule, the longest prefix wins over a
// shorter one, and a prefix wins over a regex. Two regexes that match one
// probe key are a load error, so the outcome never depends on rule order.
// Regexes are RE2.
//
// # Invariants
//
//   - Completeness: [Engine.Connector] and [Engine.Worker] return exactly one
//     entry per input key occurrence, in input order. A key no rule claims is
//     kc.key_unknown and blocking; every key of an unknown class is
//     kc.connector_class_unsupported and blocking. The earlier occurrence of
//     a duplicated key is kc.key_duplicate (the last value wins).
//   - A rule with per-value outcomes ends in an "otherwise" branch that is
//     unsupported and blocking, so an unlisted value is never accepted, and a
//     config-provider reference as such a value is unsupported and blocking,
//     because it cannot be evaluated offline.
//   - Rule data that breaks the report contract (a missing reason, fix or
//     emit kind, an unregistered code) fails [Load]. Load errors are build
//     errors: the production rule files are loaded by a test.
//   - Values are compared, never copied into an outcome.
package rules
