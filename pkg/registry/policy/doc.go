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
// Structural enforcement (PR-2): Decide returns a Decision — an opaque
// struct whose only field is unexported, so no package outside policy can
// construct a Decision reporting Allowed() == true without actually calling
// Decide. This is a compiler-enforced guarantee, not a naming convention: a
// caller cannot write "allowed := true" and skip Decide's logic, because
// there is no accessible zero-cost way to fabricate an "allowed" Decision.
// On top of that, a depguard rule in .golangci.yml restricts which files
// may import this package at all (only pkg/registry/install.go and this
// package's own files/tests) — the two together mean "Decide is the only
// skip point" is enforced structurally, not just documented.
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
