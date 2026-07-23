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

package pipelines

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal/repair"
	"github.com/conduitio/conduit/cmd/conduit/internal/testutils"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	json "github.com/goccy/go-json"
	"github.com/matryer/is"
)

const repairableYAML = `version: "2.2"
pipelines:
  - id: orders
    status: " Running "
    connectors:
      - id: src
        type: source
        plugin: builtin:generator
    processors:
      - id: proc1
        type: base64.encode
        workers: -2
`

// TestRepairCommand_ReadMode_NeverWrites is AC-7: the default (no --apply)
// run writes nothing to disk and returns a non-empty hash.
func TestRepairCommand_ReadMode_NeverWrites(t *testing.T) {
	is := is.New(t)
	path := writePipelineFile(t, repairableYAML)
	before, err := os.ReadFile(path)
	is.NoErr(err)

	cmd := &RepairCommand{args: RepairArgs{Path: path}}
	outcome, err := cmd.ExecuteWithResult(context.Background())
	is.NoErr(err)
	is.True(outcome.OK)

	plan, ok := outcome.Result.(repair.Plan)
	is.True(ok)
	is.True(plan.Hash != "")
	is.True(len(plan.Fixes) > 0)

	after, err := os.ReadFile(path)
	is.NoErr(err)
	is.Equal(string(before), string(after))

	is.True(cmd.Render(outcome) != "")

	// Family A golden fixture (v0.19 workstream 8 — cli-contract.md §6 AC-3):
	// the envelope assembled from this real Outcome must validate against
	// the shared schema; see cmd/conduit/cli/schema_golden_test.go.
	res := cecdysis.Result{Command: cmd.ResultCommand(), OK: outcome.OK, Summary: outcome.Summary, Result: outcome.Result}
	b, err := json.Marshal(res)
	is.NoErr(err)
	is.NoErr(testutils.ValidateEnvelope(b))
}

// TestRepairCommand_Apply_MissingHashAndYes_Rejected mirrors
// TestApplyCommand_MissingHashAndYes_Rejected (AC-8): --apply requires
// --plan-hash unless --yes, checked before any write.
func TestRepairCommand_Apply_MissingHashAndYes_Rejected(t *testing.T) {
	is := is.New(t)
	path := writePipelineFile(t, repairableYAML)

	cmd := &RepairCommand{args: RepairArgs{Path: path}, flags: RepairFlags{Apply: true}}
	_, err := cmd.ExecuteWithResult(context.Background())
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), conduiterr.CodeInvalidArgument.Reason())
}

// TestRepairCommand_Apply_Yes_WritesSafeFixesOnly is the default-selection
// happy path (AC-17): --apply --yes applies only the safe fixes (the
// rename + status normalization here — /workers is restart-class and is
// left alone) and atomically rewrites the file.
func TestRepairCommand_Apply_Yes_WritesSafeFixesOnly(t *testing.T) {
	is := is.New(t)
	path := writePipelineFile(t, repairableYAML)

	cmd := &RepairCommand{args: RepairArgs{Path: path}, flags: RepairFlags{Apply: true, Yes: true}}
	outcome, err := cmd.ExecuteWithResult(context.Background())
	is.NoErr(err)
	is.True(outcome.OK)

	res, ok := outcome.Result.(repair.Result)
	is.True(ok)

	after, err := os.ReadFile(path)
	is.NoErr(err)
	is.Equal(string(after), res.Content)
	is.True(strings.Contains(string(after), "plugin: base64.encode"))
	is.True(strings.Contains(string(after), "status:"))
	is.True(!strings.Contains(string(after), " Running "))  // normalized, whatever the quoting style
	is.True(strings.Contains(string(after), "workers: -2")) // restart-class, not in default selection

	is.True(cmd.Render(outcome) != "")
}

// TestRepairCommand_Apply_StaleHash_RefusedNoMutation is AC-9: a hand-edited
// file between the read step and --apply is refused with
// repair.plan_stale, and the file on disk is left byte-for-byte unchanged.
func TestRepairCommand_Apply_StaleHash_RefusedNoMutation(t *testing.T) {
	is := is.New(t)
	path := writePipelineFile(t, repairableYAML)

	before, err := os.ReadFile(path)
	is.NoErr(err)

	cmd := &RepairCommand{args: RepairArgs{Path: path}, flags: RepairFlags{Apply: true, PlanHash: "not-the-real-hash"}}
	_, err = cmd.ExecuteWithResult(context.Background())
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), repair.CodePlanStale.Reason())

	after, err := os.ReadFile(path)
	is.NoErr(err)
	is.Equal(string(before), string(after))
}

// TestRepairCommand_Apply_UnknownFixSelection_NoMutation is the "selecting
// an unknown/no-longer-applicable configPath never mutates anything" half
// of AC-19: --fix naming something outside the current plan applies
// nothing and leaves the file untouched (it does not silently fall back to
// "apply the safe defaults instead").
func TestRepairCommand_Apply_UnknownFixSelection_NoMutation(t *testing.T) {
	is := is.New(t)
	path := writePipelineFile(t, repairableYAML)

	before, err := os.ReadFile(path)
	is.NoErr(err)

	plan, err := repair.Collect(context.Background(), path)
	is.NoErr(err)

	cmd := &RepairCommand{
		args: RepairArgs{Path: path},
		flags: RepairFlags{
			Apply:    true,
			PlanHash: plan.Hash,
			Fix:      []string{"/connectors/0/settings/table"}, // not a fix the plan actually offers
		},
	}
	outcome, err := cmd.ExecuteWithResult(context.Background())
	is.NoErr(err) // an unmatched --fix selection is a per-fix skip, not a hard failure

	res, ok := outcome.Result.(repair.Result)
	is.True(ok)
	is.Equal(len(res.Fixes), 1)
	is.Equal(res.Fixes[0].Outcome, repair.FixOutcomeSkipped)
	is.Equal(res.Fixes[0].Reason, repair.CodeFixNoLongerApplies.Reason())

	after, err := os.ReadFile(path)
	is.NoErr(err)
	is.Equal(string(before), string(after)) // nothing applied, nothing written
}

// TestFirstDataPathRefusal is a direct unit test of the CLI-level AC-14
// check (RepairCommand's doc explains why this lives here rather than in
// the engine): the v1 producer set never actually classifies a fix
// FixClassDataPath (see cmd/conduit/internal/repair's classify doc), so
// there is no fixture that can drive this path end-to-end through
// ExecuteWithResult — this proves the detection logic itself directly
// against a synthetic report, the same rationale as the engine's own
// TestGateFix.
func TestFirstDataPathRefusal(t *testing.T) {
	is := is.New(t)

	path, ok := firstDataPathRefusal([]repair.AppliedFix{
		{ConfigPath: "/status", Outcome: repair.FixOutcomeApplied},
		{ConfigPath: "/connectors/0/settings/table", Outcome: repair.FixOutcomeSkipped, Reason: repair.CodeDataPathFixRefused.Reason()},
	})
	is.True(ok)
	is.Equal(path, "/connectors/0/settings/table")

	_, ok = firstDataPathRefusal([]repair.AppliedFix{
		{ConfigPath: "/status", Outcome: repair.FixOutcomeApplied},
	})
	is.True(!ok)
}
