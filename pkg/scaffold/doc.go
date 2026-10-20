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

// Package scaffold implements the engine behind `conduit connector new` and
// `conduit processor new`, per
// docs/design-documents/20260707-connector-processor-scaffolding.md. It lives
// outside cmd/ so a future MCP `scaffold` tool can call Generate directly,
// the same way the CLI commands do (no divergent logic, per this repo's MCP
// server convention).
//
// # Approach: orchestrate the upstream template, don't reimplement it
//
// Generate does not hand-roll connector/processor boilerplate. It drives the
// canonical ConduitIO/conduit-connector-template and
// conduit-processor-template through the same recipe their own setup.sh
// encodes — rename the module/token placeholders, install the code-gen tool,
// run code generation, verify the result builds, initialize git — except the
// rename step is a Go port (package template's Rewrite), not a shelled-out
// setup.sh, so it works identically (and deterministically) on Windows and
// without a bash dependency.
//
// The template sources are vendored as an embedded, pinned snapshot (package
// template) rather than fetched live: default generation is offline and
// reproducible, and a snapshot pinned to a recorded git SHA cannot drift
// mid-release the way a live "git clone HEAD" fetch could. Keeping the
// snapshot in sync with upstream is a release-time concern (a nightly CI job
// that regenerates from upstream main and fails loudly on drift — see the
// design doc's "Failure modes / dependencies" section); this package only
// consumes whatever snapshot is embedded.
//
// # Languages: Go and Python
//
// Go scaffolds both connectors and processors. Python scaffolds connectors
// only (no Python processor template exists), from a template authored in
// this repository (package template's connector_python snapshot) rather than
// an upstream repo. The Python path skips the Go-specific install/codegen
// steps and verifies the result with `python3 -m compileall` instead of `go
// build`; it depends on the published `conduitio` distribution, so a
// scaffolded connector's build/acceptance run requires that distribution to
// have been released (see docs/design/20261016-python-ga-merged-distribution.md).
//
// # Connector and processor are not symmetric
//
// The two templates' setup.sh scripts diverge after the rename step: the
// connector template's setup.sh installs conn-sdk-cli and runs `make
// generate` (which runs `conn-sdk-cli specgen` to regenerate connector.yaml
// from the Go config structs, then `conn-sdk-cli readmegen -w` to fill in the
// README's parameter tables); the processor template's setup.sh does nothing
// beyond the rename — no install, no generate. Generate does not blindly
// mirror one recipe onto both languages: it always installs and runs the
// language-appropriate generator (conn-sdk-cli for connectors, paramgen for
// processors — see package steps), reasoning that a freshly generated repo
// should always reflect a real, verified run of its own generator rather
// than relying on whatever happened to be pre-committed in the snapshot.
// --skip-generate is the escape hatch when that isn't wanted (e.g. no
// network to install the tool).
//
// # Exit codes
//
// Generate's returned error, when non-nil, is always a
// *conduiterr.ConduitError with one of this package's registered Codes (see
// codes.go), so pkg/conduit/exitcode classifies it into the right process
// exit code bucket: a toolchain preflight failure is bucket 3 (environment —
// "can I build a new connector here?" is fundamentally an environment
// question, see the shared check-engine ADR); a bad --module, an
// unsupported --lang, or an existing destination without --force is bucket 2
// (validation — the fix is the caller's command line); a code-gen or `go
// build` failure once files are on disk is bucket 1 (runtime — something
// went wrong executing an otherwise-valid request).
package scaffold
