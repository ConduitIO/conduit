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

// Package validate is the shared, offline engine behind `conduit pipeline
// validate`, `lint`, and `dry-run` (Run, RunLint, RunDryRun respectively),
// per docs/design-documents/20260707-cli-pipeline-validate.md and
// docs/design-documents/20260708-cli-pipeline-inspect-lint-dryrun.md. All
// three run the same parse -> enrich -> validate pipeline
// pkg/provisioning.Service uses at startup (see service.go's
// provisionPipeline), but never dial the Conduit API and never fail fast:
// every finding across every resolved file is collected into one Report,
// which both the human renderer and the --json envelope
// (cmd/conduit/root/pipelines/{validate,lint,dry_run}.go) marshal from the
// same data. `lint` adds advisory parser warnings (SeverityWarning
// Findings with Line/Column); `dry-run` adds those plus the enriched graph
// and, with --resolve-plugins, builtin plugin-ref resolution.
//
// # Invariants this package must hold
//
//   - Offline: Run/RunLint/RunDryRun never construct a gRPC/API client and
//     never touch anything but the local filesystem (and, for
//     RunDryRun's --resolve-plugins, the in-memory builtin plugin
//     registries — see plugins.go, which never spawns a plugin process or
//     dials anything). A command built on this package is safe to run with
//     no Conduit server anywhere nearby.
//   - Collect-all, never fail-fast: a parse failure, a validation error, or a
//     cross-file duplicate ID in one file must not stop findings from being
//     collected for the other files (or the other pipelines in the same
//     file).
//   - Every finding is walked from an UNWRAPPED cerrors.Join via
//     cerrors.ForEach — never from an error that has itself been re-wrapped
//     with "%w" first. cerrors.ForEach only flattens a Join's immediate
//     Unwrap() []error; wrapping it again (e.g. cerrors.Errorf("...: %w",
//     joinedErr)) hides that shape behind a single-error Unwrap() error,
//     and ForEach then treats the whole Join as one finding instead of N.
//     This was flagged in the design doc's review as the top masked-bug
//     risk; findingsFromError and the tests in engine_test.go exist
//     specifically to keep it from regressing.
//   - The warnings-exposure refactor in pkg/provisioning/config/yaml (Warning/
//     Warnings, Parser.ParseWithWarnings) is strictly additive: `conduit
//     run`'s own parsing path (Parser.Parse/ParseConfigurations) is
//     untouched and keeps logging warnings instead of returning them — see
//     TestParser_WarningsExposure_DoesNotChangeRunBehavior in
//     pkg/provisioning/config/yaml/parser_test.go.
package validate
