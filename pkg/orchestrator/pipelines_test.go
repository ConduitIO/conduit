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

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	pmock "github.com/conduitio/conduit/pkg/plugin/mock"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
	"github.com/matryer/is"
)

func TestPipelineOrchestrator_Start_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}
	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)

	plsMock.EXPECT().
		Start(gomock.AssignableToTypeOf(ctxType), orc.Pipelines.connectors, orc.Pipelines.processors, orc.Pipelines.plugins, plBefore.ID).
		Return(nil)

	err := orc.Pipelines.Start(ctx, plBefore.ID)
	is.NoErr(err)
}

func TestPipelineOrchestrator_Start_Fail(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}

	wantErr := cerrors.New("pipeline doesn't exist")
	plsMock.EXPECT().
		Start(gomock.AssignableToTypeOf(ctxType), consMock, procsMock, pluginMock, gomock.AssignableToTypeOf("")).
		Return(wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	err := orc.Pipelines.Start(ctx, plBefore.ID)
	is.True(err != nil)
}

func TestPipelineOrchestrator_Stop_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusRunning,
	}

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	plsMock.EXPECT().
		Stop(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(nil)

	err := orc.Pipelines.Stop(ctx, plBefore.ID)
	is.NoErr(err)
}

func TestPipelineOrchestrator_Stop_Fail(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusRunning,
	}

	wantErr := cerrors.New("pipeline doesn't exist")
	plsMock.EXPECT().
		Stop(gomock.AssignableToTypeOf(ctxType), gomock.AssignableToTypeOf("")).
		Return(wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	err := orc.Pipelines.Stop(ctx, plBefore.ID)
	is.True(err != nil)
}

func TestPipelineOrchestrator_Update_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
		Config: pipeline.Config{Name: "old pipeline"},
	}
	newConfig := pipeline.Config{Name: "new pipeline"}
	want := &pipeline.Instance{
		ID:     plBefore.ID,
		Status: pipeline.StatusSystemStopped,
		Config: pipeline.Config{Name: "new pipeline"},
	}

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)
	plsMock.EXPECT().
		Update(gomock.AssignableToTypeOf(ctxType), plBefore.ID, newConfig).
		Return(want, nil)

	got, err := orc.Pipelines.Update(ctx, plBefore.ID, newConfig)
	is.Equal(got, want)
	is.NoErr(err)
}

func TestPipelineOrchestrator_Update_PipelineRunning(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusRunning,
		Config: pipeline.Config{Name: "old pipeline"},
	}
	newConfig := pipeline.Config{Name: "new pipeline"}

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)

	got, err := orc.Pipelines.Update(ctx, plBefore.ID, newConfig)
	is.Equal(got, nil)
	is.True(err != nil)
	is.Equal(pipeline.ErrPipelineRunning, err)
}

func TestPipelineOrchestrator_Update_PipelineProvisionedByConfig(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:            uuid.NewString(),
		Status:        pipeline.StatusUserStopped,
		Config:        pipeline.Config{Name: "old pipeline"},
		ProvisionedBy: pipeline.ProvisionTypeConfig,
	}
	newConfig := pipeline.Config{Name: "new pipeline"}

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)

	got, err := orc.Pipelines.Update(ctx, plBefore.ID, newConfig)
	is.Equal(got, nil)
	is.True(err != nil)
	is.True(cerrors.Is(err, ErrImmutableProvisionedByConfig)) // expected ErrImmutableProvisionedByConfig
}

func TestPipelineOrchestrator_Delete_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
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
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusRunning,
	}

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)

	err := orc.Pipelines.Delete(ctx, plBefore.ID)
	is.True(err != nil)
	is.Equal(pipeline.ErrPipelineRunning, err)
}

func TestPipelineOrchestrator_Delete_PipelineProvisionedByConfig(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:            uuid.NewString(),
		Status:        pipeline.StatusUserStopped,
		ProvisionedBy: pipeline.ProvisionTypeConfig,
	}

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)

	err := orc.Pipelines.Delete(ctx, plBefore.ID)
	is.True(err != nil)
	is.True(cerrors.Is(err, ErrImmutableProvisionedByConfig)) // expected ErrImmutableProvisionedByConfig
}

func TestPipelineOrchestrator_Delete_PipelineHasProcessorsAttached(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:           uuid.NewString(),
		Status:       pipeline.StatusSystemStopped,
		ProcessorIDs: []string{uuid.NewString()},
	}

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)

	err := orc.Pipelines.Delete(ctx, plBefore.ID)
	is.True(err != nil)
	is.Equal(ErrPipelineHasProcessorsAttached, err)
}

func TestPipelineOrchestrator_Delete_PipelineHasConnectorsAttached(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:           uuid.NewString(),
		Status:       pipeline.StatusSystemStopped,
		ConnectorIDs: []string{uuid.NewString()},
	}

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)

	err := orc.Pipelines.Delete(ctx, plBefore.ID)
	is.True(err != nil)
	is.Equal(ErrPipelineHasConnectorsAttached, err)
}

func TestPipelineOrchestrator_Delete_PipelineDoesntExist(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	wantErr := cerrors.New("pipeline doesn't exist")
	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), gomock.AssignableToTypeOf("")).
		Return(nil, wantErr)

	err := orc.Pipelines.Delete(ctx, uuid.NewString())
	is.True(err != nil)
}

func TestPipelineOrchestrator_UpdateDLQ_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	pluginDispenser := pmock.NewDispenser(ctrl)

	plBefore := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
		Config: pipeline.Config{Name: "test-pipeline"},
		DLQ: pipeline.DLQ{
			Plugin:              "old-plugin",
			Settings:            map[string]string{"foo": "bar"},
			WindowSize:          4,
			WindowNackThreshold: 1,
		},
	}
	newDLQ := pipeline.DLQ{
		Plugin:              "new-plugin",
		Settings:            map[string]string{"baz": "qux"},
		WindowSize:          2,
		WindowNackThreshold: 0,
	}
	want := &pipeline.Instance{
		ID:     plBefore.ID,
		Status: pipeline.StatusSystemStopped,
		Config: pipeline.Config{Name: "test-pipeline"},
		DLQ:    newDLQ,
	}

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)
	pluginMock.EXPECT().
		NewDispenser(gomock.Any(), newDLQ.Plugin).
		Return(pluginDispenser, nil)
	pluginMock.EXPECT().
		ValidateDestinationConfig(gomock.Any(), pluginDispenser, newDLQ.Settings).
		Return(nil)
	plsMock.EXPECT().
		UpdateDLQ(gomock.AssignableToTypeOf(ctxType), plBefore.ID, newDLQ).
		Return(want, nil)

	got, err := orc.Pipelines.UpdateDLQ(ctx, plBefore.ID, newDLQ)
	is.Equal(got, want)
	is.NoErr(err)
}

func TestPipelineOrchestrator_UpdateDLQ_PipelineRunning(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusRunning,
	}

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)

	got, err := orc.Pipelines.UpdateDLQ(ctx, plBefore.ID, pipeline.DLQ{})
	is.Equal(got, nil)
	is.True(err != nil)
	is.Equal(pipeline.ErrPipelineRunning, err)
}

func TestPipelineOrchestrator_UpdateDLQ_PipelineProvisionedByConfig(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:            uuid.NewString(),
		Status:        pipeline.StatusUserStopped,
		ProvisionedBy: pipeline.ProvisionTypeConfig,
	}

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)

	got, err := orc.Pipelines.UpdateDLQ(ctx, plBefore.ID, pipeline.DLQ{})
	is.Equal(got, nil)
	is.True(err != nil)
	is.True(cerrors.Is(err, ErrImmutableProvisionedByConfig)) // expected ErrImmutableProvisionedByConfig
}

func TestConnectorOrchestrator_UpdateDLQ_InvalidConfig(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	pluginDispenser := pmock.NewDispenser(ctrl)

	plBefore := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
		Config: pipeline.Config{Name: "test-pipeline"},
	}
	newDLQ := pipeline.DLQ{
		Plugin:              "new-plugin",
		Settings:            map[string]string{"baz": "qux"},
		WindowSize:          2,
		WindowNackThreshold: 0,
	}
	wantErr := cerrors.New("invalid plugin config")

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)
	pluginMock.EXPECT().
		NewDispenser(gomock.Any(), newDLQ.Plugin).
		Return(pluginDispenser, nil)
	pluginMock.EXPECT().
		ValidateDestinationConfig(gomock.Any(), pluginDispenser, newDLQ.Settings).
		Return(wantErr)

	got, err := orc.Pipelines.UpdateDLQ(ctx, plBefore.ID, newDLQ)
	is.Equal(got, nil)
	is.True(cerrors.Is(err, wantErr))
}
