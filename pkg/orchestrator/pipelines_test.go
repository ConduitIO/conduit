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

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
)

func TestPipelineOrchestrator_Start_Success(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}
	orc := NewOrchestrator(db, plsMock, consMock, procsMock)

	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)
	plsMock.EXPECT().
		Start(gomock.AssignableToTypeOf(ctxType), orc.Pipelines.connectors, orc.Pipelines.processors, plBefore).
		Return(nil)

	err := orc.Pipelines.Start(ctx, plBefore.ID)
	assert.Ok(t, err)
}

func TestPipelineOrchestrator_Start_Fail(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}

	wantErr := cerrors.New("pipeline doesn't exist")
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), gomock.AssignableToTypeOf("")).
		Return(nil, wantErr)

	orc := NewOrchestrator(db, plsMock, consMock, procsMock)
	err := orc.Pipelines.Start(ctx, plBefore.ID)
	assert.Error(t, err)
}

func TestPipelineOrchestrator_Stop_Success(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusRunning,
	}

	orc := NewOrchestrator(db, plsMock, consMock, procsMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)
	plsMock.EXPECT().
		Stop(gomock.AssignableToTypeOf(ctxType), plBefore).
		Return(nil)

	err := orc.Pipelines.Stop(ctx, plBefore.ID)
	assert.Ok(t, err)
}

func TestPipelineOrchestrator_Stop_Fail(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusRunning,
	}

	wantErr := cerrors.New("pipeline doesn't exist")
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), gomock.AssignableToTypeOf("")).
		Return(nil, wantErr)

	orc := NewOrchestrator(db, plsMock, consMock, procsMock)
	err := orc.Pipelines.Stop(ctx, plBefore.ID)
	assert.Error(t, err)
}

func TestPipelineOrchestrator_Update_Success(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock := newMockServices(t)

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

	orc := NewOrchestrator(db, plsMock, consMock, procsMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)
	plsMock.EXPECT().
		Update(gomock.AssignableToTypeOf(ctxType), plBefore, newConfig).
		Return(want, nil)

	got, err := orc.Pipelines.Update(ctx, plBefore.ID, newConfig)
	assert.Equal(t, got, want)
	assert.Ok(t, err)
}

func TestPipelineOrchestrator_Update_PipelineRunning(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusRunning,
		Config: pipeline.Config{Name: "old pipeline"},
	}
	newConfig := pipeline.Config{Name: "new pipeline"}

	orc := NewOrchestrator(db, plsMock, consMock, procsMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)

	got, err := orc.Pipelines.Update(ctx, plBefore.ID, newConfig)
	assert.Nil(t, got)
	assert.Error(t, err)
	assert.Equal(t, pipeline.ErrPipelineRunning, err)
}

func TestPipelineOrchestrator_Delete_Success(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}

	orc := NewOrchestrator(db, plsMock, consMock, procsMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)
	plsMock.EXPECT().
		Delete(gomock.AssignableToTypeOf(ctxType), plBefore).
		Return(nil)

	err := orc.Pipelines.Delete(ctx, plBefore.ID)
	assert.Ok(t, err)
}

func TestPipelineOrchestrator_Delete_PipelineRunning(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusRunning,
	}

	orc := NewOrchestrator(db, plsMock, consMock, procsMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)

	err := orc.Pipelines.Delete(ctx, plBefore.ID)
	assert.Error(t, err)
	assert.Equal(t, pipeline.ErrPipelineRunning, err)
}

func TestPipelineOrchestrator_Delete_PipelineHasProcessorsAttached(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:           uuid.NewString(),
		Status:       pipeline.StatusSystemStopped,
		ProcessorIDs: []string{uuid.NewString()},
	}

	orc := NewOrchestrator(db, plsMock, consMock, procsMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)

	err := orc.Pipelines.Delete(ctx, plBefore.ID)
	assert.Error(t, err)
	assert.Equal(t, ErrPipelineHasProcessorsAttached, err)
}

func TestPipelineOrchestrator_Delete_PipelineHasConnectorsAttached(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock := newMockServices(t)

	plBefore := &pipeline.Instance{
		ID:           uuid.NewString(),
		Status:       pipeline.StatusSystemStopped,
		ConnectorIDs: []string{uuid.NewString()},
	}

	orc := NewOrchestrator(db, plsMock, consMock, procsMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), plBefore.ID).
		Return(plBefore, nil)

	err := orc.Pipelines.Delete(ctx, plBefore.ID)
	assert.Error(t, err)
	assert.Equal(t, ErrPipelineHasConnectorsAttached, err)
}

func TestPipelineOrchestrator_Delete_PipelineDoesntExist(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock := newMockServices(t)

	wantErr := cerrors.New("pipeline doesn't exist")
	orc := NewOrchestrator(db, plsMock, consMock, procsMock)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), gomock.AssignableToTypeOf("")).
		Return(nil, wantErr)

	err := orc.Pipelines.Delete(ctx, uuid.NewString())
	assert.Error(t, err)
}
