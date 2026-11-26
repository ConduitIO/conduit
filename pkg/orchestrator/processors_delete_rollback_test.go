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

package orchestrator

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	procMock "github.com/conduitio/conduit/pkg/plugin/processor/mock"
	"github.com/conduitio/conduit/pkg/processor"
	procSvcMock "github.com/conduitio/conduit/pkg/processor/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

type failingRemoveProcessorPipeline struct {
	*pipeline.Service
	err error
}

func (f failingRemoveProcessorPipeline) RemoveProcessor(context.Context, string, string) (*pipeline.Instance, error) {
	return nil, f.err
}

type failingRemoveProcessorConnector struct {
	*connector.Service
	err error
}

func (f failingRemoveProcessorConnector) RemoveProcessor(context.Context, string, string) (*connector.Instance, error) {
	return nil, f.err
}

// TestProcessorOrchestrator_Delete_RollbackRestoresInstance is the #3370
// correctness regression test for processors.go:264. When RemoveProcessor fails
// while the transaction is still live, the rollback used to call
// processors.Create, discarding the store write via txn.Discard while leaving
// memory holding a brand-new Instance (new CreatedAt/UpdatedAt, fresh
// inspectors, and an extra plugin dispense/teardown) in place of the original.
//
// The rollback must restore the exact *processor.Instance that was deleted.
func TestProcessorOrchestrator_Delete_RollbackRestoresInstance(t *testing.T) {
	testCases := []struct {
		name       string
		parentType processor.ParentType
	}{
		{name: "pipeline parent", parentType: processor.ParentTypePipeline},
		{name: "connector parent", parentType: processor.ParentTypeConnector},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			logger := log.Nop()
			db := &inmemory.DB{}
			ctrl := gomock.NewController(t)

			plsReal := pipeline.NewService(logger, db)
			connsReal := connector.NewService(logger, db, nil)

			// Dispense is expected ONCE (initial Create); rollback must NOT re-dispense.
			sdkProc := procMock.NewProcessor(ctrl)
			sdkProc.EXPECT().Teardown(gomock.Any()).Return(nil).Times(1)
			reg := procSvcMock.NewPluginService(ctrl)
			reg.EXPECT().NewProcessor(gomock.Any(), "test-processor", gomock.Any(), gomock.Any()).Return(sdkProc, nil).Times(1)

			procs := processor.NewService(logger, db, reg)

			pl, err := plsReal.Create(ctx, uuid.NewString(), pipeline.Config{Name: "p-" + uuid.NewString()}, pipeline.ProvisionTypeAPI)
			is.NoErr(err)
			pl.SetStatus(pipeline.StatusUserStopped)

			var parent processor.Parent
			if tc.parentType == processor.ParentTypePipeline {
				parent = processor.Parent{ID: pl.ID, Type: processor.ParentTypePipeline}
			} else {
				conn, cerr := connsReal.Create(ctx, uuid.NewString(), connector.TypeSource, "test-conn", pl.ID,
					connector.Config{Name: "src"}, connector.ProvisionTypeAPI)
				is.NoErr(cerr)
				parent = processor.Parent{ID: conn.ID, Type: processor.ParentTypeConnector}
			}

			proc, err := procs.Create(
				ctx,
				uuid.NewString(),
				"test-processor",
				parent,
				processor.Config{Settings: map[string]string{"k": "v"}, Workers: 1},
				processor.ProvisionTypeAPI,
				"{{ true }}",
			)
			is.NoErr(err)
			wantCreatedAt := proc.CreatedAt
			wantUpdatedAt := proc.UpdatedAt

			if tc.parentType == processor.ParentTypePipeline {
				_, err = plsReal.AddProcessor(ctx, pl.ID, proc.ID)
				is.NoErr(err)
			} else {
				_, err = connsReal.AddProcessor(ctx, parent.ID, proc.ID)
				is.NoErr(err)
			}

			_, _, _, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)
			expectMutation(t, lifecycleMock, pl.ID, true)

			wantErr := cerrors.New("remove processor failed")
			var plsSvc PipelineService = plsReal
			var connsSvc ConnectorService = connsReal
			if tc.parentType == processor.ParentTypePipeline {
				plsSvc = failingRemoveProcessorPipeline{Service: plsReal, err: wantErr}
			} else {
				connsSvc = failingRemoveProcessorConnector{Service: connsReal, err: wantErr}
			}

			orc := NewOrchestrator(db, logger, plsSvc, connsSvc, procs, connPluginMock, procPluginMock, lifecycleMock)
			err = orc.Processors.Delete(ctx, proc.ID)
			is.True(cerrors.Is(err, wantErr))

			// Memory holds the exact original *processor.Instance, not a fresh copy.
			got, err := procs.Get(ctx, proc.ID)
			is.NoErr(err)
			is.True(got == proc)
			is.True(got.CreatedAt.Equal(wantCreatedAt))
			is.True(got.UpdatedAt.Equal(wantUpdatedAt))

			// Store stayed intact via txn.Discard.
			restarted := processor.NewService(logger, db, procSvcMock.NewPluginService(ctrl))
			is.NoErr(restarted.Init(ctx))
			stored, err := restarted.Get(ctx, proc.ID)
			is.NoErr(err)
			is.True(stored.CreatedAt.Equal(wantCreatedAt))
		})
	}
}

// TestProcessorOrchestrator_Delete_CommitFailureRollsBackProcessor covers the
// #3370 panic class at processors.go:264. When txn.Commit() fails, badger has
// already discarded the transaction, so any store write in a rollback callback
// fails with ErrDiscardedTxn and rollback.R.MustExecute panics.
//
// PipelineService is mocked here so this test isolates the processor rollback
// at processors.go:264 (pipeline/connector RestoreProcessorIDs are tracked as
// step 2 of #3370). Without processor.Service.Restore's transaction guard,
// this test panics inside deferred MustExecute.
func TestProcessorOrchestrator_Delete_CommitFailureRollsBackProcessor(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := newCommitFailDB()
	ctrl := gomock.NewController(t)

	sdkProc := procMock.NewProcessor(ctrl)
	sdkProc.EXPECT().Teardown(gomock.Any()).Return(nil).Times(1)
	reg := procSvcMock.NewPluginService(ctrl)
	reg.EXPECT().NewProcessor(gomock.Any(), "test-processor", gomock.Any(), gomock.Any()).Return(sdkProc, nil).Times(1)

	procs := processor.NewService(logger, db, reg)

	plID := uuid.NewString()
	pl := &pipeline.Instance{ID: plID}
	pl.SetStatus(pipeline.StatusUserStopped)

	proc, err := procs.Create(
		ctx,
		uuid.NewString(),
		"test-processor",
		processor.Parent{ID: plID, Type: processor.ParentTypePipeline},
		processor.Config{Settings: map[string]string{"k": "v"}, Workers: 1},
		processor.ProvisionTypeAPI,
		"{{ true }}",
	)
	is.NoErr(err)

	plsMock, consMock, _, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)
	expectMutation(t, lifecycleMock, plID, true)
	plsMock.EXPECT().Get(gomock.Any(), plID).Return(pl, nil)
	plsMock.EXPECT().RemoveProcessor(gomock.Any(), plID, proc.ID).Return(pl, nil)
	plsMock.EXPECT().AddProcessor(gomock.Any(), plID, proc.ID).Return(pl, nil)

	wantErr := cerrors.New("commit refused: conflict")
	db.failCommit(wantErr)

	orc := NewOrchestrator(db, logger, plsMock, consMock, procs, connPluginMock, procPluginMock, lifecycleMock)
	err = orc.Processors.Delete(ctx, proc.ID)
	is.True(cerrors.Is(err, wantErr))

	// Memory keeps the exact original instance.
	got, err := procs.Get(ctx, proc.ID)
	is.NoErr(err)
	is.True(got == proc)

	// Discarded transaction left the persisted processor untouched.
	restarted := processor.NewService(logger, db, procSvcMock.NewPluginService(ctrl))
	is.NoErr(restarted.Init(ctx))
	stored, err := restarted.Get(ctx, proc.ID)
	is.NoErr(err)
	is.Equal(stored.ID, proc.ID)
	is.True(stored.CreatedAt.Equal(proc.CreatedAt))
}
