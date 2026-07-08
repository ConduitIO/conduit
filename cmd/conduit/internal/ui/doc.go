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

// Package ui is the single place that owns human-output presentation rules
// for the v0.17 "CLI as product" commands (`pipelines validate|lint|dry-run`,
// `doctor`, `connector|processor new`), per
// docs/design-documents/20260707-cli-output-conventions.md §2. Every command
// that renders glyphs, color, or a located finding goes through this
// package instead of hand-rolling its own — the conventions doc calls out
// doctor's `└`-line vs validate's inline pointer as exactly the drift this
// package exists to prevent.
//
// # What lives here
//
//   - Renderer: detects whether to use Unicode glyphs + ANSI color, or the
//     ASCII fallback, from TTY status, --no-color, and NO_COLOR — once, at
//     construction — so callers never re-implement that detection.
//   - RenderFinding: the canonical located-finding layout (glyph + code +
//     configPath on line 1, message indented 4, "→ suggestion" indented 4,
//     no trailing "└" line) shared by every command that reports findings.
//
// This package is a rendering helper, not a policy engine: it does not know
// about check.Status, ConduitError, or any domain type. Callers translate
// their own domain status into a glyph via Renderer and pass plain strings
// to RenderFinding.
package ui
