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

// Package harness reads the `conduit migrate kafka-connect` corpus under
// tests/migrate/corpus and checks it. The corpus format is fixed by
// docs/design-documents/20261014-migrate-kafka-connect-mapping-and-harness.md
// ("Harness corpus format"); this package is its reader, not its definition.
//
// What it does today, without the migrate command:
//
//   - LoadCorpus parses every case.yaml strictly (unknown fields are errors)
//     and validates the manifest: identity, provenance and license, the
//     command line, the expectations and the declared emits.
//   - InputKeys extracts the key occurrences of every input file with its
//     own parsers (Java properties, Kafka Connect JSON). They are deliberately
//     independent of pkg/kcmigrate: the completeness check compares the
//     migrator's report against these keys, and a check that used the
//     migrator's own parser would agree with any bug in it.
//   - Scrub is the scrub-v1 sanitization check (rule 7): no secret value
//     that is not a placeholder, no hostname outside the documentation
//     domains, no IP outside the documentation ranges.
//   - LoadCertification reads certification.yaml (rule 4).
//
// What it does with a migrator (CLI, MCP, or an in-process adapter that
// implements Migrator):
//
//   - CheckOutput checks one run against the report contract, the
//     never-silently-drop property, the case's expectations and leaks.
//   - CompareRuns (determinism), CompareGoldens (rule 1), ValidateSchema
//     (envelope and report schemas) and CLI.ValidatePipelines.
//   - A surface that does not exist yet returns an error wrapping
//     ErrPending, which callers report as pending, not as a pass.
//
// Invariant: the input-key multiset this package computes is the reference
// side of the never-silently-drop property. Every key occurrence in an input
// file is counted, including duplicates and keys whose value is not a string,
// and comment lines are never counted.
package harness
