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
// directly. This is enforced structurally where possible (Decide's Context
// is a plain value with no way to construct an "allowed" result except by
// calling Decide) and, from PR-2 (once the flag/CLI/MCP wiring that would
// give the enforcement something real to gate exists), by a depguard rule
// restricting which package may import trust's sentinel-skip path and an
// unexported decision-result type only Decide can construct.
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
