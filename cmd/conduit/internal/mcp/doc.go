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

// Package mcp is the adapter layer behind `conduit mcp` (see
// docs/design-documents/20260708-mcp-server.md): it registers Conduit's
// operations as MCP tools that are 1:1 with the CLI verbs and call the exact
// same cobra-free engines — the "three faces" rule (human CLI, agent MCP,
// embedder library are renderings of one operation set). No business logic
// lives here; every tool handler is (a) decode typed args, (b) call the
// shared engine (cmd/conduit/internal/validate, pkg/conduit/check,
// cmd/conduit/internal/deploy, pkg/scaffold, the API client), (c) map the
// result/error to the MCP CallToolResult shape via toolOK/toolErr.
//
// # Read/write separation
//
// Read tools (validate, lint, dry_run, doctor, deploy, inspect) are always
// registered by NewServer. Write tools (apply, scaffold_connector,
// scaffold_processor) are registered only when Config.AllowMutations is set
// — an operator/process-level flag on `conduit mcp`, never a tool argument
// an agent could pass. See NewServer's doc and the design doc §3.
//
// # Content-in, never a server path
//
// validate/lint/dry_run/deploy take the pipeline config as a `config`
// content string, never a filesystem path on the server host — accepting an
// agent-supplied path would let an agent read/validate arbitrary host
// files. The adapter writes the content to a private (0600) temp file inside
// a fresh, single-use directory (withTempConfigFile) for the path-based
// engines, and always removes that directory afterward, including on every
// error path.
//
// # Structured results
//
// Every tool returns Result[T] as CallToolResult.StructuredContent: the
// wrapped engine's own report/diff/result type verbatim under Result, plus
// (on a domain error) an ErrorDetail carrying the conduiterr code, message,
// suggestion, and structured fix — the same remediation data the CLI's
// --json envelope gives a human. See result.go.
package mcp
