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

package scaffold

import (
	"context"
	"go/build"
	"path/filepath"
	"strings"

	"github.com/conduitio/conduit/pkg/conduit/check"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// minGoVersion is the minimum Go toolchain version a scaffolded module can
// build with — matches the "go 1.25.0" directive both templates' go.mod
// carry at the pinned snapshot ref (see package template). Bump this in
// lockstep whenever the snapshot is resynced to a newer template that
// raises its own go.mod floor.
const minGoVersion = "1.25.0"

// preflightChecks returns this command's toolchain preflight: the checks
// the design doc calls out by name (Go on PATH at the minimum version, git
// on PATH, GOPATH/bin writable), composed from the shared pkg/conduit/check
// engine's generic constructors rather than hand-rolled — per the ADR
// resolving the doctor/scaffold check-engine boundary, scaffold supplies its
// own check *set*, reusing check's engine and generic constructors, not a
// forked copy of doctor.
//
// docker is deliberately not checked here: the design doc lists it as
// warn-only for a future acceptance-test workflow this command doesn't run
// yet (docker compose integration tests are a `make test-integration`
// concern for the scaffolded repo itself, not something `connector new`
// needs to have working before it can write files).
func preflightChecks() []check.Check {
	gopathBin := filepath.Join(goPath(), "bin")
	return []check.Check{
		check.BinaryOnPath("go", minGoVersion),
		check.BinaryOnPath("git", ""),
		check.DirWritable(gopathBin, ""),
	}
}

// goPath returns the effective GOPATH the same way the go tool itself
// would (respecting the GOPATH environment variable, falling back to
// go/build's computed default of $HOME/go) — go/build.Default.GOPATH does
// this resolution already, so shelling out to `go env GOPATH` would be
// redundant.
func goPath() string {
	return build.Default.GOPATH
}

// preflight runs preflightChecks and, if any failed, returns a single
// *conduiterr.ConduitError (CodeToolchainUnavailable) summarizing every
// failure. It deliberately does not delegate to check.Report.ExitCode's
// per-check classification: every check in this set is a toolchain/
// environment question ("can I build a new connector here?" — see the ADR),
// so the whole preflight fails or passes as one bucket-3 environment
// failure, not a mix of buckets depending on which generic check
// constructor happened to backfill which Code (check.DirWritable, for
// instance, defaults to a validation-class Code for its more common caller,
// doctor, where an unwritable configured directory is a config problem —
// that default is wrong for scaffold's specific "your Go install is broken"
// case, so this function overrides it rather than trusting Report.ExitCode).
func preflight(ctx context.Context) (check.Report, error) {
	report := check.Run(ctx, preflightChecks())
	if report.Summary.Failed == 0 {
		return report, nil
	}

	var failed []string
	var suggestions []string
	for _, r := range report.Checks {
		if r.Status != check.StatusFail {
			continue
		}
		failed = append(failed, r.Message)
		if r.Suggestion != "" {
			suggestions = append(suggestions, r.Suggestion)
		}
	}

	ce := conduiterr.New(CodeToolchainUnavailable, "toolchain preflight failed: "+strings.Join(failed, "; "))
	if len(suggestions) > 0 {
		ce.Suggestion = strings.Join(suggestions, "; ")
	}
	return report, ce
}
