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

package mcp

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/conduitio/conduit/cmd/conduit/root/doctor/doctorcheck"
	"github.com/conduitio/conduit/pkg/conduit/check"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// DoctorArgs mirrors `conduit doctor`'s behavioral flags (--deep,
// --require-server, --check).
type DoctorArgs struct {
	Deep          bool     `json:"deep,omitempty" jsonschema:"also run plugins.standalone_compat, which dispenses standalone connector plugin binaries in an isolated subprocess"`
	RequireServer bool     `json:"requireServer,omitempty" jsonschema:"fail (instead of warn) if the Conduit API server isn't reachable"`
	Checks        []string `json:"checks,omitempty" jsonschema:"run only these named check(s); empty runs every check"`
}

// DoctorResult is the doctor tool's Result payload — the same shape the CLI
// `conduit doctor` command's --json envelope emits under "result"
// (cmd/conduit/root/doctor.doctorResult, which is unexported to its
// package — reproduced here as an identical, independent type since both
// wrap the same check.Report.Checks).
type DoctorResult struct {
	Checks []check.CheckResult `json:"checks"`
}

// doctor implements the doctor tool: checks whether the local machine and
// configuration are ready for `conduit run` — offline, non-destructive. Same
// engine as `conduit doctor` (doctorcheck.DefaultChecks + check.Run), proving
// the shared-engine rule (design doc AC-7): given the same conduit.Config and
// Options, this tool and the CLI command run literally the same check.Check
// slice through the same check.Run.
func (s *server) doctor(ctx context.Context, _ *sdkmcp.CallToolRequest, in DoctorArgs) (*sdkmcp.CallToolResult, Result[DoctorResult], error) {
	if err := validateCheckNames(in.Checks, in.Deep); err != nil {
		return toolErr[DoctorResult](err)
	}

	exe, _ := os.Executable() // best-effort; standaloneCompatCheck degrades to a Fail if this is empty and Deep is set.
	opts := doctorcheck.Options{Deep: in.Deep, RequireServer: in.RequireServer, Executable: exe}

	checks := doctorcheck.DefaultChecks(s.cfg.StoreConfig, log.Nop(), opts)
	checks = filterChecks(checks, in.Checks)

	report := check.Run(ctx, checks)
	return toolOK(report.Summary.Failed == 0, DoctorResult{Checks: report.Checks}, report.Summary)
}

// validateCheckNames rejects an unknown Checks name (or a valid name that
// requires Deep the caller didn't also set) — mirrors
// cmd/conduit/root/doctor.DoctorCommand.validateCheckNames exactly (that
// method is unexported to its package).
func validateCheckNames(selected []string, deep bool) error {
	valid := doctorcheck.CheckNames()
	for _, name := range selected {
		if !slices.Contains(valid, name) {
			ce := conduiterr.New(conduiterr.CodeInvalidArgument, fmt.Sprintf("unknown check %q", name))
			ce.Suggestion = "valid checks: " + strings.Join(valid, ", ")
			return ce
		}
		if name == doctorcheck.NamePluginsStandaloneCompat && !deep {
			ce := conduiterr.New(conduiterr.CodeInvalidArgument, fmt.Sprintf("check %q requires deep:true", name))
			ce.Suggestion = "set deep:true, or drop " + name + " from checks"
			return ce
		}
	}
	return nil
}

// filterChecks narrows checks to just the ones named in selected, in checks'
// original order — mirrors cmd/conduit/root/doctor.filterChecks exactly
// (that function is unexported to its package). An empty selected runs
// everything (the common case).
func filterChecks(checks []check.Check, selected []string) []check.Check {
	if len(selected) == 0 {
		return checks
	}
	want := make(map[string]bool, len(selected))
	for _, name := range selected {
		want[name] = true
	}
	filtered := make([]check.Check, 0, len(checks))
	for _, c := range checks {
		if want[c.Name()] {
			filtered = append(filtered, c)
		}
	}
	return filtered
}
