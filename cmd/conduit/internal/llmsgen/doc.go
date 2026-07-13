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

// Package main implements the llms.txt/llms-full.txt generator, per
// docs/design-documents/20260712-llms-txt-generation.md. It writes two
// files to the repo root:
//
//   - llms.txt — a concise, llmstxt.org-conformant index (one H1, one
//     blockquote summary, ## sections of links and counts).
//   - llms-full.txt — the same index with every source fully inlined.
//
// Both are generated from four in-repo sources of truth, never
// hand-maintained:
//
//  1. Pipeline config schema — reflected from
//     pkg/provisioning/config/yaml/v2's Configuration/Pipeline/Connector/
//     Processor/DLQ structs, plus v2.Changelog for the version list and
//     v1.LatestVersion for the legacy note.
//  2. Built-in connectors — builtin.DefaultBuiltinConnectors, read via each
//     connector's own NewSpecification() (never through builtin.Registry,
//     which requires a live dispenser and overwrites Specification.Version
//     from runtime/debug.BuildInfo — see connectors.go).
//  3. Error codes — every conduiterr.Code, read through the allcodes
//     blank-import barrel (cmd/conduit/internal/llmsgen/allcodes) so no
//     code-declaring package can be silently unimported.
//  4. MCP tool catalog — cmd/conduit/internal/mcp.Catalog(), the same list
//     NewServer registers tools from.
//
// # Invocation
//
// The //go:generate directive in generate.go is picked up by `go generate
// ./...` (make generate), with zero additional CI wiring: the existing
// validate-generated-files workflow already runs `make generate` and fails
// the build on any resulting git diff, so that job is this generator's
// drift-guard for both llms.txt and llms-full.txt, unchanged.
//
// # Determinism is the whole point
//
// The committed files are also this generator's regression test (CI
// regenerates and diffs them), so non-determinism here is not a cosmetic
// bug — it is a guard that cries wolf on every unrelated PR. Three rules,
// each enforced by a test in llmsgen_test.go:
//
//   - Never read runtime/debug.BuildInfo (empty under `go generate`/`go
//     run`/`go test`, populated only in a built binary — the single most
//     likely source of "green locally, red in CI"). Connector versions
//     come from go.mod (modversion.go) instead.
//   - Sort everything that originates from a Go map: connectors by Name,
//     params by key, error codes by reason (defensively re-sorted even
//     though conduiterr.Codes() already does), MCP tools by Name. Struct
//     fields come from reflection over a fixed slice of types in
//     declaration order, which is not a map and needs no sort.
//   - No timestamps, no absolute paths, no hostname, no Go version — ever.
//     TestNoNondeterministicTokens greps the committed output for the
//     classic offenders.
//
// # Package layout
//
//   - connectors.go — Source #2.
//   - configschema.go — Source #1.
//   - errorcodes.go, sourcescan.go — Source #3, plus the D5 completeness
//     scan (see allcodes's doc for why the scan exists independently of
//     the barrel it verifies).
//   - mcptools.go — Source #4.
//   - modversion.go, reporoot.go — determinism-critical file-derived
//     inputs (go.mod parsing, locating the repo root).
//   - render.go — pure functions from gathered data to file content; no
//     I/O, so these are the golden-file tests' unit under test.
//   - generate.go — the //go:generate directive and the composition root.
package main
