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

package provisioning

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/lifecycle"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// processorUpdateFixture returns an old/desired pair whose only difference is a
// single pipeline-level processor's settings — a live-swappable change (see
// Change.liveSwappable), so the diff is LiveEligible and ApplyPlanLive applies it
// in place rather than via a restart.
func processorUpdateFixture() (old, desired config.Pipeline) {
	old = config.Pipeline{
		ID:  "p1",
		DLQ: dlqFixture(),
		Connectors: []config.Connector{
			{ID: "p1:conn:src", Type: config.TypeSource, Plugin: "builtin:postgres", Settings: map[string]string{"table": "users"}},
		},
		Processors: []config.Processor{
			{ID: "p1:proc:1", Plugin: "builtin:field.set", Settings: map[string]string{"field": ".Key", "value": "a"}},
		},
	}
	desired = old
	desired.Processors = []config.Processor{
		{ID: "p1:proc:1", Plugin: "builtin:field.set", Settings: map[string]string{"field": ".Key", "value": "b"}},
	}
	return old, desired
}

// TestApplyPlanLive_ProcessorUpdate_AppliesInPlace_NoRestart proves the payoff of
// the live-in-place path: a processor config change on a running pipeline commits
// the new config and swaps the live node via ReconfigureProcessor — with NO
// StopAndWait and NO Start (no restart). The absence of those two expectations is
// the assertion: gomock fails the test if the restart path runs.
func TestApplyPlanLive_ProcessorUpdate_AppliesInPlace_NoRestart(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, procSrv, _, lifecycleSrv := newTestService(ctrl, log.Nop())

	old, desired := processorUpdateFixture()
	expectExportRunning(pipSrv, connSrv, procSrv, old)

	procSrv.EXPECT().Update(gomock.Any(), "p1:proc:1", "builtin:field.set", gomock.Any()).
		Return(&processor.Instance{ID: "p1:proc:1"}, nil)
	lifecycleSrv.EXPECT().ReconfigureProcessor(gomock.Any(), "p1", "p1:proc:1").Return(nil)

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	is.True(diff.LiveEligible()) // sanity: a processor-only update is live-eligible

	applied, err := srv.ApplyPlanLive(ctx, desired, diff.Hash, true) // authorized
	is.NoErr(err)
	is.Equal(applied.Hash, diff.Hash)
}

// TestApplyPlanLive_ProcessorUpdate_InPlace_CompletesDespiteCallerCancel proves
// the in-place apply detaches from the caller's context once it starts committing
// live state (review MINOR-1): cancelling the caller context mid-apply must not
// abandon the swap (which would report a failure the apply actually landed) — the
// apply runs to a consistent end on a non-cancelled context and reports success.
func TestApplyPlanLive_ProcessorUpdate_InPlace_CompletesDespiteCallerCancel(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, procSrv, _, lifecycleSrv := newTestService(ctrl, log.Nop())

	old, desired := processorUpdateFixture()
	expectExportRunning(pipSrv, connSrv, procSrv, old)

	// The forward import runs on the detached context; cancel the caller context
	// as a side-effect to prove the rest of the apply proceeds regardless.
	procSrv.EXPECT().Update(gomock.Any(), "p1:proc:1", "builtin:field.set", gomock.Any()).
		DoAndReturn(func(importCtx context.Context, _, _ string, _ processor.Config) (*processor.Instance, error) {
			cancel()
			is.NoErr(importCtx.Err()) // detached: not cancelled by the caller
			return &processor.Instance{ID: "p1:proc:1"}, nil
		})
	lifecycleSrv.EXPECT().ReconfigureProcessor(gomock.Any(), "p1", "p1:proc:1").
		DoAndReturn(func(swapCtx context.Context, _, _ string) error {
			is.NoErr(swapCtx.Err()) // swap runs on the detached context too
			return nil
		})

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	applied, err := srv.ApplyPlanLive(ctx, desired, diff.Hash, true)
	is.NoErr(err)
	is.Equal(applied.Hash, diff.Hash)
}

// TestApplyPlanLive_ProcessorUpdate_NotLiveReconfigurable_FallsBackToRestart:
// when the live node can't be swapped (ReconfigureProcessor reports the processor
// isn't live-reconfigurable, e.g. it's parallelized), the config is already
// committed, so ApplyPlanLive falls back to a full StopAndWait -> import -> Start.
func TestApplyPlanLive_ProcessorUpdate_NotLiveReconfigurable_FallsBackToRestart(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, procSrv, _, lifecycleSrv := newTestService(ctrl, log.Nop())

	old, desired := processorUpdateFixture()
	expectExportRunning(pipSrv, connSrv, procSrv, old)

	// procSrv.Update is called by applyInPlace's import and again by the restart
	// path's import (the stateless mock Export keeps returning old, so both
	// imports recompute the same action — in production the second is a no-op).
	procSrv.EXPECT().Update(gomock.Any(), "p1:proc:1", "builtin:field.set", gomock.Any()).
		Return(&processor.Instance{ID: "p1:proc:1"}, nil).Times(2)
	lifecycleSrv.EXPECT().ReconfigureProcessor(gomock.Any(), "p1", "p1:proc:1").
		Return(lifecycle.ErrProcessorNotLiveReconfigurable)

	var order []string
	lifecycleSrv.EXPECT().StopAndWait(gomock.Any(), "p1").DoAndReturn(func(context.Context, string) error {
		order = append(order, "stop")
		return nil
	})
	lifecycleSrv.EXPECT().Start(gomock.Any(), "p1").DoAndReturn(func(context.Context, string) error {
		order = append(order, "start")
		return nil
	})

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)

	_, err = srv.ApplyPlanLive(ctx, desired, diff.Hash, true)
	is.NoErr(err)
	is.Equal(order, []string{"stop", "start"}) // fell back to a restart
}

// TestApplyPlanLive_ProcessorUpdate_OpenFails_RollsBack: when the new processor
// fails to open, the old processor keeps running (open-before-teardown). The
// in-place apply rolls back — restoring the old config to the store — and returns
// the error, without ever restarting the pipeline.
func TestApplyPlanLive_ProcessorUpdate_OpenFails_RollsBack(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, procSrv, _, lifecycleSrv := newTestService(ctrl, log.Nop())

	old, desired := processorUpdateFixture()
	expectExportRunning(pipSrv, connSrv, procSrv, old)

	// Forward import commits the new config (one Update). The rollback then calls
	// transactionalImport(old); against the stateless mock Export (which keeps
	// returning old) that diffs old-vs-old = empty, so no second Update is
	// recorded here — in production, where the store reflects the forward import,
	// the rollback re-imports old and reverses it.
	procSrv.EXPECT().Update(gomock.Any(), "p1:proc:1", "builtin:field.set", gomock.Any()).
		Return(&processor.Instance{ID: "p1:proc:1"}, nil)
	openErr := cerrors.New("new processor failed to open")
	lifecycleSrv.EXPECT().ReconfigureProcessor(gomock.Any(), "p1", "p1:proc:1").Return(openErr)
	// No StopAndWait / Start: a failed in-place swap never restarts the pipeline.

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)

	_, err = srv.ApplyPlanLive(ctx, desired, diff.Hash, true)
	is.True(err != nil)
	is.True(cerrors.Is(err, openErr))
}
