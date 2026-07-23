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
	"path/filepath"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal/deploy"
	"github.com/conduitio/conduit/cmd/conduit/internal/testutils"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/provisioning"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	json "github.com/goccy/go-json"
	"github.com/matryer/is"
)

// fakePlanApplier is a deploy.PlanApplier test double: it hands back
// canned Diffs/errors without touching a database, so the CLI commands can
// be tested against the exact same AC scenarios pkg/provisioning/plan_test.go
// covers at the engine level, without re-deriving them through a real store.
type fakePlanApplier struct {
	planDiff  provisioning.Diff
	planErr   error
	applyDiff provisioning.Diff
	applyErr  error

	// gotHash records the hash ApplyPlan was called with, so a test can
	// assert the command passed through exactly the hash it was given (or
	// the one it recomputed) — never a hard-coded/mismatched one.
	gotHash string
	applied bool
}

func (f *fakePlanApplier) Plan(context.Context, config.Pipeline) (provisioning.Diff, error) {
	return f.planDiff, f.planErr
}

func (f *fakePlanApplier) ApplyPlan(_ context.Context, _ config.Pipeline, hash string) (provisioning.Diff, error) {
	f.gotHash = hash
	f.applied = true
	return f.applyDiff, f.applyErr
}

func writePipelineFile(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "pipeline.yaml")
	is.New(t).NoErr(os.WriteFile(path, []byte(contents), 0o600))
	return path
}

const validPipelineYAML = `version: 2.2
pipelines:
  - id: orders
    name: orders
    connectors:
      - id: src
        type: source
        plugin: builtin:generator
`

// TestDeployCommand_RendersPlan_NoSideEffects is the regression test for
// AC-1/AC-2-style rendering plus deploy's core contract: it never calls
// ApplyPlan (fakePlanApplier.applied stays false).
func TestDeployCommand_RendersPlan_NoSideEffects(t *testing.T) {
	is := is.New(t)
	path := writePipelineFile(t, validPipelineYAML)

	wantDiff := provisioning.Diff{
		PipelineID: "orders",
		Hash:       "deadbeef",
		Changes: []provisioning.Change{
			{Resource: provisioning.ResourcePipeline, ID: "orders", Action: provisioning.ChangeActionCreate, Effect: provisioning.EffectInPlace, Code: "provisioning.pipeline.create"},
		},
	}
	fake := &fakePlanApplier{planDiff: wantDiff}

	cmd := &DeployCommand{
		args: DeployArgs{Path: path},
		newService: func(context.Context, conduit.Config) (deploy.PlanApplier, func() error, error) {
			return fake, func() error { return nil }, nil
		},
	}

	outcome, err := cmd.ExecuteWithResult(context.Background())
	is.NoErr(err)
	is.True(outcome.OK)
	is.True(!fake.applied) // deploy must never call ApplyPlan

	result, ok := outcome.Result.(deploy.Result)
	is.True(ok)
	is.Equal(result.Hash, "deadbeef")
	is.Equal(result.PipelineID, "orders")
	is.True(!result.Applied)

	rendered := cmd.Render(outcome)
	is.True(rendered != "")

	// Family A golden fixture (v0.19 workstream 8 — cli-contract.md §6 AC-3):
	// the envelope assembled from this real Outcome must validate against
	// the shared schema; see cmd/conduit/cli/schema_golden_test.go.
	res := cecdysis.Result{Command: cmd.ResultCommand(), OK: outcome.OK, Summary: outcome.Summary, Result: outcome.Result}
	b, err := json.Marshal(res)
	is.NoErr(err)
	is.NoErr(testutils.ValidateEnvelope(b))
}

// TestDeployCommand_InvalidFile_HardFailure is the regression test for
// AC-12: an unparseable/invalid file is rejected before any Plan call.
// newService is left nil — if ExecuteWithResult ever reached it (a
// regression that skipped the parse/validate gate), the nil call would
// panic and fail the test loudly rather than silently passing.
func TestDeployCommand_InvalidFile_HardFailure(t *testing.T) {
	is := is.New(t)
	path := writePipelineFile(t, "not: [valid, pipeline, yaml structure\n")

	cmd := &DeployCommand{args: DeployArgs{Path: path}}

	_, err := cmd.ExecuteWithResult(context.Background())
	is.True(err != nil)
}

// TestDeployCommand_Apply_RunsApplyPlan is the regression test for the
// --apply --yes convenience path: it must call ApplyPlan with the hash Plan
// computed, and mark the result Applied.
func TestDeployCommand_Apply_RunsApplyPlan(t *testing.T) {
	is := is.New(t)
	path := writePipelineFile(t, validPipelineYAML)

	planned := provisioning.Diff{PipelineID: "orders", Hash: "h1", Changes: []provisioning.Change{
		{Resource: provisioning.ResourcePipeline, ID: "orders", Action: provisioning.ChangeActionCreate, Effect: provisioning.EffectInPlace},
	}}
	applied := planned // ApplyPlan echoes the same diff back once executed
	fake := &fakePlanApplier{planDiff: planned, applyDiff: applied}

	cmd := &DeployCommand{
		args:  DeployArgs{Path: path},
		flags: DeployFlags{Apply: true, Yes: true},
		newService: func(context.Context, conduit.Config) (deploy.PlanApplier, func() error, error) {
			return fake, func() error { return nil }, nil
		},
	}

	outcome, err := cmd.ExecuteWithResult(context.Background())
	is.NoErr(err)
	is.True(fake.applied)
	is.Equal(fake.gotHash, "h1")

	result, ok := outcome.Result.(deploy.Result)
	is.True(ok)
	is.True(result.Applied)
}

// TestDeployCommand_PlanStale_SurfacesError checks that a coded error from
// Plan (e.g. a real Service wrapping a corrupted-state error) propagates as
// the command's hard-failure error rather than being swallowed.
func TestDeployCommand_PlanStale_SurfacesError(t *testing.T) {
	is := is.New(t)
	path := writePipelineFile(t, validPipelineYAML)

	wantErr := conduiterr.New(provisioning.CodePlanStale, "boom")
	fake := &fakePlanApplier{planErr: wantErr}

	cmd := &DeployCommand{
		args: DeployArgs{Path: path},
		newService: func(context.Context, conduit.Config) (deploy.PlanApplier, func() error, error) {
			return fake, func() error { return nil }, nil
		},
	}

	_, err := cmd.ExecuteWithResult(context.Background())
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), provisioning.CodePlanStale.Reason())
}
