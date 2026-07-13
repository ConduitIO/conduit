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

// Package repair is the engine behind `conduit pipelines repair` and the MCP
// `repair`/`repair_apply` tools (see
// docs/design-documents/20260712-repair-command.md). It is the single code
// path both shells call — neither the CLI command nor the MCP tool contains
// repair logic of its own, matching cmd/conduit/internal/deploy and
// cmd/conduit/internal/validate's own "engine that thin cobra/MCP shells
// call" shape (CLAUDE.md's MCP rule: "Tools mirror CLI verbs... Same code
// paths as the CLI — no divergent logic").
//
// # What repair is
//
// A ConduitError can carry a structured, machine-appliable Fix
// (pkg/foundation/cerrors/conduiterr.Fix — {ConfigPath, Op, Value}) alongside
// its human Suggestion. repair collects every Finding a validate/lint run
// produces that carries a non-nil Fix (see Collect), and can apply a chosen
// subset of them to the config file's YAML.
//
// # Invariant: repair edits a config FILE, never the store or a running
// pipeline
//
// This is the single most important safety property of this package.
// Collect/CollectContent are pure reads: they run the offline
// validate/lint engine and parse the file's own bytes into a YAML node tree
// purely to render a diff; neither opens a database, dials a Conduit
// server, or touches a running pipeline. Apply re-validates its inputs and
// performs the edits on an in-memory YAML node tree, returning the repaired
// bytes; it does not write to disk itself (see Apply's doc) and, like
// Collect, never opens a store or a running pipeline. The only path from a
// repaired file into a live engine is `deploy`/`apply`, which keeps its own
// plan-hash and Invariant-7 running-pipeline guard — repair does not
// duplicate, bypass, or shortcut that gate.
//
// # Invariant: default-deny classification
//
// Every proposed fix is classified (see Classify) against a JSON-pointer
// pattern of ack/position/checkpoint-adjacent config: connector `settings`,
// a connector's own `plugin`/`type`, any DLQ config, and any `id` field.
// A ConfigPath the classifier does not recognize is treated as data-path
// (FixClassDataPath) — refused for auto-apply — never as safe. This is
// deliberately conservative: an omission in the deny-list is an invariant
// risk (a data-path fix silently auto-applied), so the default runs the
// other way. See classify.go.
//
// # v1 scope
//
// Only a small, deliberately scoped starter set of error/warning sites
// populate a Fix today (design doc §6): a deprecated/renamed field
// (processor `type` -> `plugin`), an unambiguous `/status` enum
// normalization, a negative processor `/workers` clamped to 1, and an
// over-long `/description` truncation. Everything else keeps its human
// Suggestion and carries a nil Fix, exactly as before this package existed
// — repair reports "no machine-appliable fix for this finding" for those,
// honestly, rather than fabricating one. Widening this set (e.g. a plugin
// "did-you-mean" fix, or an ID-rename fix) is future, separate work — see
// the design doc's §6 "explicitly out of scope" table for why each is
// deferred.
//
// # v1 scope: single-pipeline v2 files only
//
// repair operates on exactly one file containing exactly one v2-format
// pipeline document — the same constraint deploy/apply already impose (see
// deploy.ParseSinglePipeline). This is a deliberate, documented
// scope-narrowing, not an oversight: the comment-preserving YAML node
// editor (yamlnode.go) locates a fix's target field by walking a
// JSON-pointer-shaped path from the document root, and a v1 (map-keyed,
// non-deterministically-ordered) config or a multi-pipeline/multi-document
// file would need a materially more complex addressing scheme for no
// benefit to the v1 fix set, which never touches those shapes. A file
// repair cannot edit losslessly is refused with a clear, coded error
// (config.parse_error) rather than a silent comment-stripping rewrite — the
// documented fallback from the design doc's §10 schedule-risk note.
package repair
