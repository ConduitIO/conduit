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

package conduiterr

import "google.golang.org/grpc/codes"

// This file is additive only (a new file — no edits to conduiterr's existing
// code) by design: a parallel change is touching this package's codes and
// status.go, so a shared registration point needs to avoid colliding with it
// and expect to rebase. See pkg/conduit/exitcode's package doc and the
// deterministic exit-code ADR (docs/architecture-decision-records) for the
// full rationale.

// CodeUnavailable is raised when Conduit itself cannot reach a required
// external dependency it needs to start or operate: the CLI can't reach a
// running server, the configured database can't be opened/dialed, or a
// listen address is already bound. It is deliberately generic (unlike the
// per-domain codes elsewhere in the registry) because these are
// environment-of-execution failures, not domain business errors — the
// caller's remediation is "check the environment", not "fix a config field".
//
// It maps to the gRPC Unavailable category, which pkg/conduit/exitcode's
// classifier treats as an environment failure (process exit code 3),
// distinct from validation errors (exit 2) and internal/runtime errors
// (exit 1).
var CodeUnavailable = Register("common.unavailable", codes.Unavailable)
