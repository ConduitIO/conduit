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

// Package atomicfile writes a whole file's contents durably and atomically:
// a crash at any point leaves either the previous complete content or the
// new complete content, never a torn write (Invariant 5 — "state and
// checkpoint writes are atomic").
//
// It was promoted out of cmd/conduit/internal/repair/apply.go (the CLI
// `repair` command's write step) once a second real call site — the
// connector registry's install manifest (pkg/registry.SaveManifest) —
// justified sharing it: both write a small, whole-file JSON/YAML document
// that must never be seen half-written by a concurrent reader or a crash.
// This is not speculative generality; it is the same helper, the same
// invariant, now with two call sites instead of one.
package atomicfile
