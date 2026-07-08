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

// Package doctorcheck is `conduit doctor`'s check SET: the concrete
// pkg/conduit/check.Check implementations that answer "would `conduit run`
// succeed here?" (config resolution/validation, database reachability, API
// address availability, plugin directories, the built-in plugin registry,
// and whether a running engine is reachable), per
// docs/design-documents/20260707-cli-doctor.md and the ADR
// docs/architecture-decision-records/20260707-check-engine-shared-by-doctor-and-scaffold.md.
//
// # Invariants
//
//   - This package MUST NOT import cobra or ecdysis (directly or
//     transitively through its own imports), same discipline as
//     pkg/conduit/check itself. That is what lets a test in this package
//     call DefaultChecks and check.Run with zero CLI machinery — the same
//     property a future MCP `doctor` tool needs to wrap this check set
//     1:1 without dragging in cobra (ADR, "a future MCP doctor tool wraps
//     doctor's check set via the same engine"). cmd/conduit/root/doctor
//     is the cobra-facing wrapper that imports this package, not the other
//     way around.
//   - Every check here is non-destructive: it does not leave files,
//     directories, or other state behind after it runs, even on the
//     "everything is fine" path (see storeReachableCheck's doc for the one
//     check that has to actively work at this, since opening a database is
//     inherently a side-effecting operation).
//   - The only check that runs external code is standaloneCompatCheck
//     (--deep only), and it does so in a subprocess with its own timeout —
//     see that file's doc for why an in-process recover() is not enough.
package doctorcheck
