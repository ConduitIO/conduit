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

// Package check is the neutral diagnostic engine shared by `conduit doctor`
// and the `connector|processor new` scaffold preflight (see
// docs/architecture-decision-records/20260707-check-engine-shared-by-doctor-and-scaffold.md).
// The two commands' check SETS are disjoint by design — doctor asks "would
// `conduit run` succeed here?" (config, store, API address, plugins),
// scaffolding asks "can I build a new connector here?" (Go/git/docker on
// PATH) — but they share this package's mechanism: the Check interface, the
// CheckResult/Report/Status types, the panic-isolating runner, exit-code
// aggregation, and a few generic, reusable check constructors.
//
// # Invariants
//
//   - This package MUST NOT import cobra or ecdysis, and MUST NOT call
//     os.Exit. It is consumed by CLI commands, but it is not itself a CLI
//     concern — that keeps it usable from a future MCP doctor tool and from
//     tests without dragging in command-line machinery.
//   - Run isolates a panicking Check: recover() converts it into a Fail
//     CheckResult (Code: internal.error) rather than crashing the calling
//     process. This does not extend to goroutines a Check spawns itself and
//     does not join back before Run returns — see Run's doc for the limit.
//   - Report.ExitCode aggregates every Fail result to a single process exit
//     code by synthesizing a *conduiterr.ConduitError per failing result and
//     taking the worst (max) exitcode.ExitCode bucket across all of them
//     (environment 3 > validation 2 > runtime 1 > ok 0). This is new
//     aggregation logic owned by this package — it is NOT free reuse of
//     exitcode.ExitCode's single-error classifier (which only sees the first
//     error in a chain), and it deliberately does not use cerrors.Join,
//     which would report only the first error's exit code, not the worst.
package check
