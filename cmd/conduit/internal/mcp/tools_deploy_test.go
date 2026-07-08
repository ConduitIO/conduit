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
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/internal/deploy"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/provisioning"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/matryer/is"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakePlanApplier is a deploy.PlanApplier test double — the same pattern
// cmd/conduit/root/pipelines/deploy_test.go uses — so the MCP deploy/apply
// tools can be tested against canned Diffs/errors without a real database.
type fakePlanApplier struct {
	planDiff  provisioning.Diff
	planErr   error
	applyDiff provisioning.Diff
	applyErr  error

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

func newServerWithFake(fake *fakePlanApplier, allowMutations bool) *sdkmcp.Server {
	return NewServer(Config{
		AllowMutations: allowMutations,
		newLocalService: func(context.Context, conduit.Config) (deploy.PlanApplier, func() error, error) {
			return fake, func() error { return nil }, nil
		},
	})
}

// TestDeploy_ReturnsDiffAndHash_NeverMutates is the AC-4 regression test:
// deploy returns the Diff + hash and never calls ApplyPlan.
func TestDeploy_ReturnsDiffAndHash_NeverMutates(t *testing.T) {
	is := is.New(t)

	wantDiff := provisioning.Diff{
		PipelineID: "orders",
		Hash:       "deadbeefcafe",
		Changes: []provisioning.Change{
			{Resource: provisioning.ResourcePipeline, ID: "orders", Action: provisioning.ChangeActionCreate, Effect: provisioning.EffectInPlace, Code: "provisioning.pipeline.create"},
		},
	}
	fake := &fakePlanApplier{planDiff: wantDiff}
	srv := newServerWithFake(fake, false)
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolDeploy,
		Arguments: map[string]any{"config": validPipelineYAML},
	})
	is.NoErr(err)
	is.True(!res.IsError)
	is.True(!fake.applied) // deploy must never call ApplyPlan

	env := decodeStructuredContent[Result[deploy.Result]](t, res)
	is.True(env.OK)
	is.Equal(env.Result.Hash, wantDiff.Hash)
	is.Equal(env.Result.PipelineID, wantDiff.PipelineID)
	is.Equal(env.Result.Applied, false)
	is.Equal(len(env.Result.Changes), 1)
}

// TestApply_MatchingHash_Mutates is half of AC-5: apply with a matching hash
// calls ApplyPlan with exactly that hash and reports Applied:true.
func TestApply_MatchingHash_Mutates(t *testing.T) {
	is := is.New(t)

	applyDiff := provisioning.Diff{PipelineID: "orders", Hash: "deadbeefcafe"}
	fake := &fakePlanApplier{applyDiff: applyDiff}
	srv := newServerWithFake(fake, true)
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolApply,
		Arguments: map[string]any{"config": validPipelineYAML, "hash": "deadbeefcafe"},
	})
	is.NoErr(err)
	is.True(!res.IsError)
	is.True(fake.applied)
	is.Equal(fake.gotHash, "deadbeefcafe") // the tool passes the caller's hash through unmodified

	env := decodeStructuredContent[Result[deploy.Result]](t, res)
	is.True(env.OK)
	is.Equal(env.Result.Applied, true)
}

// TestApply_StaleHash_RefusesNoMutation is the other half of AC-5: a stale
// hash (ApplyPlan refusing with provisioning.CodePlanStale) surfaces as a
// tool error, and — critically — the fake still reports it was "called"
// (ApplyPlan itself is what refuses; the tool never short-circuits before
// calling it) but the returned Diff.Hash differs from what was presented,
// proving no mutation was accepted. This exercises the actual
// provisioning.CodePlanStale code path end-to-end through the MCP adapter,
// not just a mocked short-circuit.
func TestApply_StaleHash_RefusesNoMutation(t *testing.T) {
	is := is.New(t)

	staleErr := conduiterr.New(provisioning.CodePlanStale,
		`plan for pipeline "orders" is stale: the presented hash "presented" does not match the current plan hash "fresh"`)
	staleErr.Suggestion = "re-run 'conduit pipelines deploy' to compute a fresh plan, review it, then apply its hash"

	fake := &fakePlanApplier{applyErr: staleErr}
	srv := newServerWithFake(fake, true)
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolApply,
		Arguments: map[string]any{"config": validPipelineYAML, "hash": "presented"},
	})
	is.NoErr(err) // protocol succeeds; the domain error rides in the result
	is.True(res.IsError)

	env := decodeStructuredContent[Result[deploy.Result]](t, res)
	is.True(!env.OK)
	is.True(env.Error != nil)
	is.Equal(env.Error.Code, provisioning.CodePlanStale.Reason())
	is.True(env.Error.Suggestion != "") // AC-6: every tool error carries a suggestion
}

// TestApply_MissingHash_RejectedBeforeCallingApplyPlan asserts apply refuses
// a call with no hash at all (an agent that skipped deploy) without ever
// reaching ApplyPlan — the plan-authorized-second-call contract (design doc
// §3) starts with "a hash must be presented", not just "must match".
func TestApply_MissingHash_RejectedBeforeCallingApplyPlan(t *testing.T) {
	is := is.New(t)

	fake := &fakePlanApplier{}
	srv := newServerWithFake(fake, true)
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolApply,
		Arguments: map[string]any{"config": validPipelineYAML},
	})
	is.NoErr(err)
	is.True(res.IsError)
	is.True(!fake.applied)

	env := decodeStructuredContent[Result[deploy.Result]](t, res)
	is.True(env.Error != nil)
	is.True(env.Error.Suggestion != "")
}
