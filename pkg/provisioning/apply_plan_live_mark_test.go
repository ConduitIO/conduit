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

// Regression tests for #2961 on the provisioning side: ApplyPlanLive wrote
// config (the import) while an external Start could still be admitted, so a
// run could be built from a half-imported pipeline. Each test fires an
// external Start from inside the import itself, which is exactly the gap, so
// there is no scheduling luck and no sleep (except the bounded retry test,
// whose delay is the production retry delay).

import (
	"context"
	"sync"
	"testing"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/provisioning/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// markLifecycle models the lifecycle's admission rule that provisioning
// depends on: Start is refused with ErrPipelineConfigChanging while a mark is
// held, TryLockForMutation is refused while a run exists. The real
// implementation is tested in pkg/lifecycle and pkg/lifecycle-poc.
type markLifecycle struct {
	*mock.LifecycleService

	mu sync.Mutex
	// marks is the number of marks currently held.
	marks int
	// running makes TryLockForMutation refuse, as if a run were admitted.
	running bool
	// startsRefusedBeforeOK makes the first n Starts that meet no mark still
	// fail with ErrPipelineConfigChanging (a concurrent API write).
	startsRefusedBeforeOK int
	// startLog holds the outcome of every Start, in order.
	startLog []error
	// events records "stop", "mark", "unmark", "start" in order.
	events []string
}

func (m *markLifecycle) StopAndWait(context.Context, string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, "stop")
	return nil
}

func (m *markLifecycle) TryLockForMutation(string) (func(), bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return nil, false
	}
	m.marks++
	m.events = append(m.events, "mark")
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			m.marks--
			m.events = append(m.events, "unmark")
		})
	}, true
}

func (m *markLifecycle) Start(context.Context, string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var err error
	switch {
	case m.marks > 0:
		err = pipeline.ErrPipelineConfigChanging
	case m.startsRefusedBeforeOK > 0:
		m.startsRefusedBeforeOK--
		err = pipeline.ErrPipelineConfigChanging
	default:
		m.events = append(m.events, "start")
	}
	m.startLog = append(m.startLog, err)
	return err
}

func (m *markLifecycle) lastStart() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startLog[len(m.startLog)-1]
}

func newMarkService(t *testing.T) (*Service, *mock.PipelineService, *mock.ConnectorService, *mock.ProcessorService, *markLifecycle) {
	t.Helper()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, procSrv, _, lifecycleSrv := newTestService(ctrl, log.Nop())
	ml := &markLifecycle{LifecycleService: lifecycleSrv}
	srv.lifecycleService = ml
	return srv, pipSrv, connSrv, procSrv, ml
}

// A pipeline with no run: the import is covered. An external Start inside it
// is refused, and the mark is gone afterwards.
func TestApplyPlanLive_Mark_NotRunning_StartRefusedInsideImport(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	srv, pipSrv, connSrv, procSrv, ml := newMarkService(t)
	old, desired := settingsUpdateFixture()
	expectExport(pipSrv, connSrv, procSrv, old)

	connSrv.EXPECT().Update(gomock.Any(), "p1:conn:src", "builtin:postgres", gomock.Any()).
		DoAndReturn(func(context.Context, string, string, connector.Config) (*connector.Instance, error) {
			_ = ml.Start(ctx, "p1") // the external Start, inside the import
			return &connector.Instance{ID: "p1:conn:src"}, nil
		})

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	applied, err := srv.ApplyPlanLive(ctx, desired, diff.Hash, false)
	is.NoErr(err)
	is.Equal(applied.AppliedMode, ApplyModeProvisioned)

	is.Equal(len(ml.startLog), 1)
	is.True(cerrors.Is(ml.startLog[0], pipeline.ErrPipelineConfigChanging)) // refused inside the import
	is.Equal(ml.marks, 0)
	is.Equal(ml.events, []string{"mark", "unmark"})
}

// A pipeline with a run that the status has not caught up with: the mark is
// refused, so ApplyPlanLive takes the running branch, which without the
// operator flag refuses and changes nothing.
func TestApplyPlanLive_Mark_NotRunningByStatus_RunExists_TakesRunningBranch(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	srv, pipSrv, connSrv, procSrv, ml := newMarkService(t)
	ml.running = true
	old, desired := settingsUpdateFixture()
	expectExport(pipSrv, connSrv, procSrv, old) // status says not running; no import expectation: it must not run

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	_, err = srv.ApplyPlanLive(ctx, desired, diff.Hash, false)
	var ce *conduiterr.ConduitError
	is.True(cerrors.As(err, &ce))
	is.Equal(ce.Code, CodeLiveApplyUnauthorized)
	is.Equal(ml.marks, 0)
	is.Equal(len(ml.events), 0) // no stop, no mark
}

// The running path: the mark is taken after StopAndWait, an external Start
// inside the import is refused, the mark is released before provisioning's
// own Start, and that Start succeeds.
func TestApplyPlanLive_Mark_Running_StartRefusedInsideImport_OwnStartSucceeds(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	srv, pipSrv, connSrv, procSrv, ml := newMarkService(t)
	old, desired := settingsUpdateFixture()
	expectExportRunning(pipSrv, connSrv, procSrv, old)

	connSrv.EXPECT().Update(gomock.Any(), "p1:conn:src", "builtin:postgres", gomock.Any()).
		DoAndReturn(func(context.Context, string, string, connector.Config) (*connector.Instance, error) {
			_ = ml.Start(ctx, "p1") // the external Start, inside the import
			return &connector.Instance{ID: "p1:conn:src"}, nil
		})

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	applied, err := srv.ApplyPlanLive(ctx, desired, diff.Hash, true)
	is.NoErr(err)
	is.Equal(applied.AppliedMode, ApplyModeRestart)

	is.Equal(len(ml.startLog), 2)
	is.True(cerrors.Is(ml.startLog[0], pipeline.ErrPipelineConfigChanging)) // external Start, inside the import
	is.NoErr(ml.startLog[1])                                                // provisioning's own Start
	is.Equal(ml.marks, 0)
	is.Equal(ml.events, []string{"stop", "mark", "unmark", "start"})
}

// A Start slipped in between StopAndWait and the mark: nothing is imported,
// and the caller is told to retry.
func TestApplyPlanLive_Mark_Running_RunAdmittedAfterStopAndWait_NothingChanged(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	srv, pipSrv, connSrv, procSrv, ml := newMarkService(t)
	old, desired := settingsUpdateFixture()
	expectExportRunning(pipSrv, connSrv, procSrv, old)
	// No Update/Create/Delete expectation: the import must not run, or gomock fails.
	ml.running = true

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	_, err = srv.ApplyPlanLive(ctx, desired, diff.Hash, true)
	is.True(cerrors.Is(err, pipeline.ErrPipelineRunning))
	is.Equal(len(ml.startLog), 0) // and no restart
	is.Equal(ml.marks, 0)
}

// A failed import still releases the mark, and the pipeline is left stopped.
func TestApplyPlanLive_Mark_Running_ImportFails_MarkReleased(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	srv, pipSrv, connSrv, procSrv, ml := newMarkService(t)
	old, desired := settingsUpdateFixture()
	expectExportRunning(pipSrv, connSrv, procSrv, old)
	connSrv.EXPECT().Update(gomock.Any(), "p1:conn:src", "builtin:postgres", gomock.Any()).
		Return(nil, cerrors.New("injected: store write failed"))
	// the import's own rollback restores the old settings
	connSrv.EXPECT().Update(gomock.Any(), "p1:conn:src", "builtin:postgres", gomock.Any()).
		Return(&connector.Instance{ID: "p1:conn:src"}, nil).AnyTimes()

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	_, err = srv.ApplyPlanLive(ctx, desired, diff.Hash, true)
	is.True(err != nil)
	is.Equal(ml.marks, 0)
	is.Equal(len(ml.startLog), 0) // left stopped
}

// provisioning's own Start meets a concurrent write's mark: it retries
// briefly instead of leaving the committed pipeline stopped.
func TestApplyPlanLive_Mark_Running_OwnStartRetriesConfigChanging(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	srv, pipSrv, connSrv, procSrv, ml := newMarkService(t)
	ml.startsRefusedBeforeOK = 2
	old, desired := settingsUpdateFixture()
	expectExportRunning(pipSrv, connSrv, procSrv, old)
	connSrv.EXPECT().Update(gomock.Any(), "p1:conn:src", "builtin:postgres", gomock.Any()).
		Return(&connector.Instance{ID: "p1:conn:src"}, nil)

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	_, err = srv.ApplyPlanLive(ctx, desired, diff.Hash, true)
	is.NoErr(err)
	is.Equal(len(ml.startLog), 3)
	is.NoErr(ml.lastStart())
}

// A mark that outlasts the retries is reported, not swallowed.
func TestApplyPlanLive_Mark_Running_OwnStartGivesUpAfterRetries(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	srv, pipSrv, connSrv, procSrv, ml := newMarkService(t)
	ml.startsRefusedBeforeOK = 1000
	old, desired := settingsUpdateFixture()
	expectExportRunning(pipSrv, connSrv, procSrv, old)
	connSrv.EXPECT().Update(gomock.Any(), "p1:conn:src", "builtin:postgres", gomock.Any()).
		Return(&connector.Instance{ID: "p1:conn:src"}, nil)

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	_, err = srv.ApplyPlanLive(ctx, desired, diff.Hash, true)
	is.True(cerrors.Is(err, pipeline.ErrPipelineConfigChanging))
	is.Equal(len(ml.startLog), startAfterApplyAttempts)
}
