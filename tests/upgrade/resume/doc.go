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

// Package resume is the N to N+1 resume gate for default source batching
// (docs/design-documents/20261027-default-source-batching.md, "Upgrade,
// rollback and versioning"). It starts a real conduit binary built from a
// BASE ref (the release or commit before the change), lets it deliver part of
// a file through a file source and a file destination, stops it (SIGTERM and
// SIGKILL variants), then starts the binary built from this tree on the same
// database and output. The new binary reads the source position the old one
// wrote with the new, batched file connector and must deliver the rest of the
// input with no gap (invariants 2 and 3), and with no duplicates after a
// graceful stop. After a SIGKILL it may replay, within the persister window.
//
// Both pipeline engines run, because the classic engine is still the default.
//
// The test needs a base ref, so it is skipped unless CONDUIT_UPGRADE_BASE_REF
// is set (a tag, branch or commit of this repository):
//
//	CONDUIT_UPGRADE_BASE_REF=v0.21.2 go test ./tests/upgrade/resume/
//
// It builds both binaries (a minute or two each), so it is not part of
// "make test". The upgrade gate is not a required CI check yet (Process
// maturity table in CLAUDE.md).
package resume
