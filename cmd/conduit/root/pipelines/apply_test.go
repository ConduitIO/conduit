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
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal/deploy"
	"github.com/conduitio/conduit/cmd/conduit/internal/testutils"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/provisioning"
	json "github.com/goccy/go-json"
	"github.com/matryer/is"
)

// TestApplyCommand_MatchingHash_Executes is the regression test for AC-6: a
// matching --plan-hash actually calls ApplyPlan with that exact hash.
func TestApplyCommand_MatchingHash_Executes(t *testing.T) {
	is := is.New(t)
	path := writePipelineFile(t, validPipelineYAML)

	applied := provisioning.Diff{PipelineID: "orders", Hash: "h1", Changes: []provisioning.Change{
		{Resource: provisioning.ResourcePipeline, ID: "orders", Action: provisioning.ChangeActionCreate, Effect: provisioning.EffectInPlace},
	}}
	fake := &fakePlanApplier{applyDiff: applied}

	cmd := &ApplyCommand{
		args:  ApplyArgs{Path: path},
		flags: ApplyFlags{PlanHash: "h1"},
		newService: func(context.Context, conduit.Config) (deploy.PlanApplier, func() error, error) {
			return fake, func() error { return nil }, nil
		},
	}

	outcome, err := cmd.ExecuteWithResult(context.Background())
	is.NoErr(err)
	is.True(fake.applied)
	is.Equal(fake.gotHash, "h1")
	is.True(outcome.OK)

	result, ok := outcome.Result.(deploy.Result)
	is.True(ok)
	is.True(result.Applied)
}

// TestApplyCommand_StaleHash_RefusedNoMutation is the regression test for
// AC-7: a stale hash surfaces provisioning.plan_stale as a hard failure
// (exit 2 via pkg/conduit/exitcode's FailedPrecondition mapping); the fake
// still records that ApplyPlan was invoked with the caller's hash (that's
// exactly what ApplyPlan itself is responsible for refusing on — the safety
// property lives in pkg/provisioning, verified there; this test pins that
// the CLI surfaces that refusal rather than swallowing or retrying it).
func TestApplyCommand_StaleHash_RefusedNoMutation(t *testing.T) {
	is := is.New(t)
	path := writePipelineFile(t, validPipelineYAML)

	wantErr := conduiterr.New(provisioning.CodePlanStale, "plan is stale")
	fake := &fakePlanApplier{applyErr: wantErr}

	cmd := &ApplyCommand{
		args:  ApplyArgs{Path: path},
		flags: ApplyFlags{PlanHash: "stale-hash"},
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

// TestApplyCommand_RunningPipeline_Refused is the regression test for
// AC-13: the CLI surfaces provisioning.pipeline_running as a hard failure
// rather than treating it as a success.
func TestApplyCommand_RunningPipeline_Refused(t *testing.T) {
	is := is.New(t)
	path := writePipelineFile(t, validPipelineYAML)

	wantErr := conduiterr.New(provisioning.CodePipelineRunning, "pipeline is running")
	fake := &fakePlanApplier{applyErr: wantErr}

	cmd := &ApplyCommand{
		args:  ApplyArgs{Path: path},
		flags: ApplyFlags{PlanHash: "h1"},
		newService: func(context.Context, conduit.Config) (deploy.PlanApplier, func() error, error) {
			return fake, func() error { return nil }, nil
		},
	}

	_, err := cmd.ExecuteWithResult(context.Background())
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), provisioning.CodePipelineRunning.Reason())
}

// TestApplyCommand_MissingHashAndYes_Rejected is the regression test for the
// design doc's flag contract: apply requires --plan-hash unless --yes is
// set, checked before ever touching the filesystem/service — no fake is
// wired at all, so a regression that skipped this gate would panic on a nil
// newService instead of silently passing.
func TestApplyCommand_MissingHashAndYes_Rejected(t *testing.T) {
	is := is.New(t)

	cmd := &ApplyCommand{args: ApplyArgs{Path: "irrelevant.yaml"}}

	_, err := cmd.ExecuteWithResult(context.Background())
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), conduiterr.CodeInvalidArgument.Reason())
}

// TestApplyCommand_Idempotent_EmptyDiff is the regression test for AC-8: an
// empty Diff from ApplyPlan (nothing to do) is still OK:true / Applied:true,
// with no error.
func TestApplyCommand_Idempotent_EmptyDiff(t *testing.T) {
	is := is.New(t)
	path := writePipelineFile(t, validPipelineYAML)

	fake := &fakePlanApplier{applyDiff: provisioning.Diff{PipelineID: "orders", Hash: "h1"}}

	cmd := &ApplyCommand{
		args:  ApplyArgs{Path: path},
		flags: ApplyFlags{PlanHash: "h1"},
		newService: func(context.Context, conduit.Config) (deploy.PlanApplier, func() error, error) {
			return fake, func() error { return nil }, nil
		},
	}

	outcome, err := cmd.ExecuteWithResult(context.Background())
	is.NoErr(err)
	is.True(outcome.OK)

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

// TestApplyCommand_Yes_RecomputesHash is the regression test for the --yes
// convenience path (no --plan-hash): apply must call Plan first to get a
// fresh hash, then ApplyPlan with that exact hash — never an empty/zero one.
func TestApplyCommand_Yes_RecomputesHash(t *testing.T) {
	is := is.New(t)
	path := writePipelineFile(t, validPipelineYAML)

	fake := &fakePlanApplier{
		planDiff:  provisioning.Diff{PipelineID: "orders", Hash: "fresh-hash"},
		applyDiff: provisioning.Diff{PipelineID: "orders", Hash: "fresh-hash"},
	}

	cmd := &ApplyCommand{
		args:  ApplyArgs{Path: path},
		flags: ApplyFlags{Yes: true},
		newService: func(context.Context, conduit.Config) (deploy.PlanApplier, func() error, error) {
			return fake, func() error { return nil }, nil
		},
	}

	_, err := cmd.ExecuteWithResult(context.Background())
	is.NoErr(err)
	is.Equal(fake.gotHash, "fresh-hash")
}
