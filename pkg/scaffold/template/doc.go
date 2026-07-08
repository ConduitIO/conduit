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

// Package template owns the vendored, go:embed'd snapshots of
// ConduitIO/conduit-connector-template and ConduitIO/conduit-processor-template
// (Extract), and the Go port of each template's setup.sh rename step
// (Rewrite).
//
// # Snapshot discipline
//
// Everything under connector/ and processor/ is an exact, unmodified copy of
// the two upstream template repos at the git SHA recorded in ConnectorRef /
// ProcessorRef — right down to keeping their own setup.sh (which Extract's
// caller deletes via Rewrite, mirroring what running setup.sh for real would
// do; it is never executed here). Do not hand-edit files under connector/ or
// processor/: re-sync them from upstream (bump the pinned SHA and refresh
// the tree) instead, so a diff against upstream is always meaningful. A
// machine-driven resync + a nightly CI job that rebuilds from upstream main
// and fails on drift, the <30-min CI-measured matrix job, and the upstream
// Windows-matrix PRs this package's snapshot would need to pass a Windows
// leg are all tracked as follow-up work in
// https://github.com/ConduitIO/conduit/issues/2575 (see also the design
// doc's "Failure modes / dependencies" section) — today the snapshot is
// refreshed by hand.
//
// # Invariant: this package must not be swept up by `go generate ./...` or
// `make generate` at the repository root
//
// connector/ and processor/ each carry their own go.mod, making them
// separate Go modules nested inside this one. The `go` tool's `./...`
// pattern does not descend into a directory containing its own go.mod, so
// neither `go build ./...` nor `go generate ./...` run from the repository
// root ever touches the snapshot's own //go:generate directives (conn-sdk-cli
// specgen, paramgen) or attempts to build cmd/connector or cmd/processor
// inside it. This is what keeps validate-generated-files (which runs `make
// generate` and asserts a clean git diff) green with these files present —
// do not flatten the nested go.mod away, even though it looks unused from
// this module's perspective.
package template
