// Copyright © 2022 Meroxa, Inc.
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
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestPipelineOrchestrator_Start_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	plBefore.SetStatus(pipeline.StatusSystemStopped)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock)

	lifecycleMock.EXPECT().
		Start(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(nil)

	err := orc.Pipelines.Start(ctx, plBefore.ID)
	is.NoErr(err)
}

func TestPipelineOrchestrator_Start_Fail(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	plBefore.SetStatus(pipeline.StatusSystemStopped)

	wantErr := cerrors.New("pipeline doesn't exist")
	lifecycleMock.EXPECT().
		Start(gomock.AssignableToTypeOf(ctxType), gomock.AssignableToTypeOf("")).
		Return(wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock)
	err := orc.Pipelines.Start(ctx, plBefore.ID)
	is.True(cerrors.Is(err, wantErr))
}

func TestPipelineOrchestrator_Stop_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	plBefore.SetStatus(pipeline.StatusRunning)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock)
	lifecycleMock.EXPECT().
		Stop(gomock.AssignableToTypeOf(ctxType), plBefore.ID, false).
		Return(nil)

	err := orc.Pipelines.Stop(ctx, plBefore.ID, false)
	is.NoErr(err)
}

func TestPipelineOrchestrator_Stop_Fail(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	plBefore.SetStatus(pipeline.StatusRunning)

	wantErr := cerrors.New("pipeline doesn't exist")
	lifecycleMock.EXPECT().
		Stop(gomock.AssignableToTypeOf(ctxType), gomock.AssignableToTypeOf(""), true).
		Return(wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock)
	err := orc.Pipelines.Stop(ctx, plBefore.ID, true)
	is.True(cerrors.Is(err, wantErr))
}

func TestPipelineOrchestrator_Update_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:     uuid.NewString(),
		Config: pipeline.Config{Name: "old pipeline"},
	}
	plBefore.SetStatus(pipeline.StatusSystemStopped)

	newConfig := pipeline.Config{Name: "new pipeline"}
	want := &pipeline.Instance{
		ID:     plBefore.ID,
		Config: pipeline.Config{Name: "new pipeline"},
	}
	want.SetStatus(pipeline.StatusSystemStopped)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)
	plsMock.EXPECT().
		Update(gomock.AssignableToTypeOf(ctxType), plBefore.ID, newConfig).
		Return(want, nil)

	got, err := orc.Pipelines.Update(ctx, plBefore.ID, newConfig)
	is.NoErr(err)
	is.Equal(got, want)
}

func TestPipelineOrchestrator_Update_PipelineRunning(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:     uuid.NewString(),
		Config: pipeline.Config{Name: "old pipeline"},
	}
	plBefore.SetStatus(pipeline.StatusRunning)

	newConfig := pipeline.Config{Name: "new pipeline"}

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)

	got, err := orc.Pipelines.Update(ctx, plBefore.ID, newConfig)
	is.Equal(got, nil)
	is.Equal(err, pipeline.ErrPipelineRunning)
}

func TestPipelineOrchestrator_Update_PipelineProvisionedByConfig(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:            uuid.NewString(),
		Config:        pipeline.Config{Name: "old pipeline"},
		ProvisionedBy: pipeline.ProvisionTypeConfig,
	}
	plBefore.SetStatus(pipeline.StatusUserStopped)

	newConfig := pipeline.Config{Name: "new pipeline"}

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)

	got, err := orc.Pipelines.Update(ctx, plBefore.ID, newConfig)
	is.Equal(got, nil)
	is.True(cerrors.Is(err, ErrImmutableProvisionedByConfig)) // expected ErrImmutableProvisionedByConfig
}

func TestPipelineOrchestrator_Delete_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	plBefore.SetStatus(pipeline.StatusSystemStopped)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)
	plsMock.EXPECT().
		Delete(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(nil)

	err := orc.Pipelines.Delete(ctx, plBefore.ID)
	is.NoErr(err)
}

func TestPipelineOrchestrator_Delete_PipelineRunning(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	plBefore.SetStatus(pipeline.StatusRunning)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)

	err := orc.Pipelines.Delete(ctx, plBefore.ID)
	is.Equal(pipeline.ErrPipelineRunning, err)
}

func TestPipelineOrchestrator_Delete_PipelineProvisionedByConfig(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:            uuid.NewString(),
		ProvisionedBy: pipeline.ProvisionTypeConfig,
	}
	plBefore.SetStatus(pipeline.StatusUserStopped)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)

	err := orc.Pipelines.Delete(ctx, plBefore.ID)
	is.True(cerrors.Is(err, ErrImmutableProvisionedByConfig)) // expected ErrImmutableProvisionedByConfig
}

func TestPipelineOrchestrator_Delete_PipelineHasProcessorsAttached(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:           uuid.NewString(),
		ProcessorIDs: []string{uuid.NewString()},
	}
	plBefore.SetStatus(pipeline.StatusSystemStopped)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)

	err := orc.Pipelines.Delete(ctx, plBefore.ID)
	is.Equal(err, ErrPipelineHasProcessorsAttached)
}

func TestPipelineOrchestrator_Delete_PipelineHasConnectorsAttached(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:           uuid.NewString(),
		ConnectorIDs: []string{uuid.NewString()},
	}
	plBefore.SetStatus(pipeline.StatusSystemStopped)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)

	err := orc.Pipelines.Delete(ctx, plBefore.ID)
	is.Equal(err, ErrPipelineHasConnectorsAttached)
}

func TestPipelineOrchestrator_Delete_PipelineDoesntExist(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)

	wantErr := cerrors.New("pipeline doesn't exist")
	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), gomock.AssignableToTypeOf("")).
		Return(nil, wantErr)

	err := orc.Pipelines.Delete(ctx, uuid.NewString())
	is.True(cerrors.Is(err, wantErr))
}

func TestPipelineOrchestrator_UpdateDLQ_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:     uuid.NewString(),
		Config: pipeline.Config{Name: "test-pipeline"},
		DLQ: pipeline.DLQ{
			Plugin:              "old-plugin",
			Settings:            map[string]string{"foo": "bar"},
			WindowSize:          4,
			WindowNackThreshold: 1,
		},
	}
	plBefore.SetStatus(pipeline.StatusSystemStopped)

	newDLQ := pipeline.DLQ{
		Plugin:              "new-plugin",
		Settings:            map[string]string{"baz": "qux"},
		WindowSize:          2,
		WindowNackThreshold: 0,
	}
	want := &pipeline.Instance{
		ID:     plBefore.ID,
		Config: plBefore.Config,
		DLQ:    newDLQ,
	}
	want.SetStatus(plBefore.GetStatus())

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)
	connPluginMock.EXPECT().
		ValidateDestinationConfig(gomock.Any(), newDLQ.Plugin, newDLQ.Settings).
		Return(nil)
	plsMock.EXPECT().
		UpdateDLQ(gomock.AssignableToTypeOf(ctxType), plBefore.ID, newDLQ).
		Return(want, nil)

	got, err := orc.Pipelines.UpdateDLQ(ctx, plBefore.ID, newDLQ)
	is.NoErr(err)
	is.Equal(got, want)
}

func TestPipelineOrchestrator_UpdateDLQ_PipelineRunning(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	plBefore.SetStatus(pipeline.StatusRunning)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)

	got, err := orc.Pipelines.UpdateDLQ(ctx, plBefore.ID, pipeline.DLQ{})
	is.Equal(err, pipeline.ErrPipelineRunning)
	is.Equal(got, nil)
}

func TestPipelineOrchestrator_UpdateDLQ_PipelineProvisionedByConfig(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:            uuid.NewString(),
		ProvisionedBy: pipeline.ProvisionTypeConfig,
	}
	plBefore.SetStatus(pipeline.StatusUserStopped)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)

	got, err := orc.Pipelines.UpdateDLQ(ctx, plBefore.ID, pipeline.DLQ{})
	is.True(cerrors.Is(err, ErrImmutableProvisionedByConfig)) // expected ErrImmutableProvisionedByConfig
	is.Equal(got, nil)
}

func TestConnectorOrchestrator_UpdateDLQ_InvalidConfig(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:     uuid.NewString(),
		Config: pipeline.Config{Name: "test-pipeline"},
	}
	plBefore.SetStatus(pipeline.StatusSystemStopped)

	newDLQ := pipeline.DLQ{
		Plugin:              "new-plugin",
		Settings:            map[string]string{"baz": "qux"},
		WindowSize:          2,
		WindowNackThreshold: 0,
	}
	wantErr := cerrors.New("invalid plugin config")

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock, lifecycleMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)
	connPluginMock.EXPECT().
		ValidateDestinationConfig(gomock.Any(), newDLQ.Plugin, newDLQ.Settings).
		Return(wantErr)

	got, err := orc.Pipelines.UpdateDLQ(ctx, plBefore.ID, newDLQ)
	is.True(cerrors.Is(err, wantErr))
	is.Equal(got, nil)
}
