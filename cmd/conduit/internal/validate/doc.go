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

// Package validate is the shared, offline engine behind `conduit pipelines
// validate` (and, in a future PR, `lint` and `dry-run`), per
// docs/design-documents/20260707-cli-pipeline-validate.md. It runs the same
// parse -> enrich -> validate pipeline pkg/provisioning.Service uses at
// startup (see service.go's provisionPipeline), but never dials the Conduit
// API and never fails fast: every finding across every resolved file is
// collected into one Report, which both the human renderer and the --json
// envelope (cmd/conduit/root/pipelines/validate.go) marshal from the same
// data.
//
// # Invariants this package must hold
//
//   - Offline: Run never constructs a gRPC/API client and never touches
//     anything but the local filesystem. A command built on this package is
//     safe to run with no Conduit server anywhere nearby.
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
package validate
