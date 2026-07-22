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

// Package policy is the --allow-unsigned gate (plan-v2 §6, P1-3).
//
// # The invariant
//
// Decide is the ONLY function in this codebase permitted to return "skip
// verification". No other call site may short-circuit pkg/registry/trust
// directly.
//
// In this PR (PR-0), that is a convention, not a guarantee: Decide returns a
// plain (bool, error), so nothing in the type system stops a caller from
// writing "allowed := true" and skipping Decide entirely. There is no
// structural enforcement yet.
//
// Structural enforcement lands in PR-2 (once the flag/CLI/MCP wiring that
// would give it something real to gate exists), via an unexported
// decision-result type that only Decide can construct, plus a depguard rule
// in .golangci.yml restricting which package may import trust's
// sentinel-skip path. Until PR-2 merges, treat "Decide is the only skip
// point" as a rule contributors must follow, not one the compiler enforces.
//
// # What ships now vs. later (PR-0 vs PR-2)
//
// Decide's logic has ZERO crypto dependency — it is pure boolean logic over
// TTY/env-var/operator-policy/MCP-origin inputs — so it ships fully
// implemented and fully tested in PR-0, per plan-v2 §2.3. Only the CLI
// wiring (the actual --allow-unsigned flag, the interactive TTY
// confirmation prompt, and the MCP tool's JSON schema deliberately omitting
// any allowUnsigned-shaped parameter) waits for PR-2: gating a flag that
// doesn't exist yet has nothing to gate.
package policy
