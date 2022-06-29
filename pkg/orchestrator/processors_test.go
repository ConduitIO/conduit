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

	"github.com/conduitio/conduit/pkg/connector"
	connmock "github.com/conduitio/conduit/pkg/connector/mock"
	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
)

func TestProcessorOrchestrator_CreateOnPipeline_Success(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}
	want := &processor.Instance{
		ID:   uuid.NewString(),
		Name: "test-processor",
		Parent: processor.Parent{
			ID:   pl.ID,
			Type: processor.ParentTypePipeline,
		},
		Config: processor.Config{
			Settings: map[string]string{"foo": "bar"},
		},
	}

	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	procsMock.EXPECT().
		Create(
			gomock.AssignableToTypeOf(ctxType),
			gomock.AssignableToTypeOf(""),
			want.Name,
			want.Parent,
			want.Config,
		).
		Return(want, nil)
	plsMock.EXPECT().
		AddProcessor(gomock.AssignableToTypeOf(ctxType), pl, want.ID).
		Return(pl, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	got, err := orc.Processors.Create(ctx, want.Name, want.Parent, want.Config)
	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func TestProcessorOrchestrator_CreateOnPipeline_PipelineNotExist(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	parent := processor.Parent{
		ID:   uuid.NewString(),
		Type: processor.ParentTypePipeline,
	}
	wantErr := pipeline.ErrInstanceNotFound
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), parent.ID).
		Return(nil, wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	got, err := orc.Processors.Create(ctx, "test-processor", parent, processor.Config{})
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, wantErr), "errors did not match")
	assert.Nil(t, got)
}

func TestProcessorOrchestrator_CreateOnPipeline_PipelineRunning(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusRunning,
	}
	parent := processor.Parent{
		ID:   pl.ID,
		Type: processor.ParentTypePipeline,
	}
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	got, err := orc.Processors.Create(ctx, "test-processor", parent, processor.Config{})
	assert.Error(t, err)
	assert.Equal(t, pipeline.ErrPipelineRunning, err)
	assert.Nil(t, got)
}

func TestProcessorOrchestrator_CreateOnPipeline_CreateProcessorError(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}
	parent := processor.Parent{
		ID:   pl.ID,
		Type: processor.ParentTypePipeline,
	}
	wantErr := cerrors.New("test error")

	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	procsMock.EXPECT().
		Create(
			gomock.AssignableToTypeOf(ctxType),
			gomock.AssignableToTypeOf(""),
			"test-processor",
			parent,
			processor.Config{},
		).
		Return(nil, wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	got, err := orc.Processors.Create(ctx, "test-processor", parent, processor.Config{})
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, wantErr), "errors did not match")
	assert.Nil(t, got)
}

func TestProcessorOrchestrator_CreateOnPipeline_AddProcessorError(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}
	proc := &processor.Instance{
		ID:   uuid.NewString(),
		Name: "test-processor",
		Parent: processor.Parent{
			ID:   pl.ID,
			Type: processor.ParentTypePipeline,
		},
		Config: processor.Config{
			Settings: map[string]string{"foo": "bar"},
		},
	}
	wantErr := cerrors.New("test error")

	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	procsMock.EXPECT().
		Create(
			gomock.AssignableToTypeOf(ctxType),
			gomock.AssignableToTypeOf(""),
			proc.Name,
			proc.Parent,
			proc.Config,
		).
		Return(proc, nil)
	plsMock.EXPECT().
		AddProcessor(gomock.AssignableToTypeOf(ctxType), pl, proc.ID).
		Return(nil, wantErr)
	// this is called in rollback
	procsMock.EXPECT().
		Delete(gomock.AssignableToTypeOf(ctxType), proc.ID).
		Return(nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	got, err := orc.Processors.Create(ctx, proc.Name, proc.Parent, proc.Config)
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, wantErr), "errors did not match")
	assert.Nil(t, got)
}

func TestProcessorOrchestrator_CreateOnConnector_Success(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)
	connBuilder := connmock.Builder{Ctrl: gomock.NewController(t)}

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}
	conn := connBuilder.NewSourceMock(uuid.NewString(), connector.Config{PipelineID: pl.ID})
	want := &processor.Instance{
		ID:   uuid.NewString(),
		Name: "test-processor",
		Parent: processor.Parent{
			ID:   conn.ID(),
			Type: processor.ParentTypeConnector,
		},
		Config: processor.Config{
			Settings: map[string]string{"foo": "bar"},
		},
	}

	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), conn.ID()).
		Return(conn, nil)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	procsMock.EXPECT().
		Create(
			gomock.AssignableToTypeOf(ctxType),
			gomock.AssignableToTypeOf(""),
			want.Name,
			want.Parent,
			want.Config,
		).
		Return(want, nil)
	consMock.EXPECT().
		AddProcessor(gomock.AssignableToTypeOf(ctxType), conn.ID(), want.ID).
		Return(conn, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	got, err := orc.Processors.Create(ctx, want.Name, want.Parent, want.Config)
	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func TestProcessorOrchestrator_CreateOnConnector_ConnectorNotExist(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	parent := processor.Parent{
		ID:   uuid.NewString(),
		Type: processor.ParentTypeConnector,
	}
	wantErr := connector.ErrInstanceNotFound
	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), parent.ID).
		Return(nil, wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	got, err := orc.Processors.Create(ctx, "test-processor", parent, processor.Config{})
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, wantErr), "errors did not match")
	assert.Nil(t, got)
}

func TestProcessorOrchestrator_UpdateOnPipeline_Success(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}
	before := &processor.Instance{
		ID:   uuid.NewString(),
		Name: "test-processor",
		Parent: processor.Parent{
			ID:   pl.ID,
			Type: processor.ParentTypePipeline,
		},
		Config: processor.Config{
			Settings: map[string]string{"foo": "bar"},
		},
	}
	newConfig := processor.Config{
		Settings: map[string]string{"foo2": "bar2"},
	}
	want := &processor.Instance{
		ID:   before.ID,
		Name: "test-processor",
		Parent: processor.Parent{
			ID:   pl.ID,
			Type: processor.ParentTypePipeline,
		},
		Config: processor.Config{
			Settings: map[string]string{"foo2": "bar2"},
		},
	}

	procsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), want.ID).
		Return(before, nil)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	procsMock.EXPECT().
		Update(gomock.AssignableToTypeOf(ctxType), want.ID, want.Config).
		Return(want, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	got, err := orc.Processors.Update(ctx, before.ID, newConfig)
	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func TestProcessorOrchestrator_UpdateOnPipeline_ProcessorNotExist(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}
	before := &processor.Instance{
		ID:   uuid.NewString(),
		Name: "test-processor",
		Parent: processor.Parent{
			ID:   pl.ID,
			Type: processor.ParentTypePipeline,
		},
		Config: processor.Config{
			Settings: map[string]string{"foo": "bar"},
		},
	}
	newConfig := processor.Config{
		Settings: map[string]string{"foo2": "bar2"},
	}

	wantErr := cerrors.New("processor not found")
	procsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), before.ID).
		Return(nil, wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	got, err := orc.Processors.Update(ctx, before.ID, newConfig)
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, wantErr), "errors did not match")
	assert.Nil(t, got)
}

func TestProcessorOrchestrator_UpdateOnPipeline_PipelineRunning(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusRunning,
	}
	before := &processor.Instance{
		ID:   uuid.NewString(),
		Name: "test-processor",
		Parent: processor.Parent{
			ID:   pl.ID,
			Type: processor.ParentTypePipeline,
		},
		Config: processor.Config{
			Settings: map[string]string{"foo": "bar"},
		},
	}
	newConfig := processor.Config{
		Settings: map[string]string{"foo2": "bar2"},
	}

	procsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), before.ID).
		Return(before, nil)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	got, err := orc.Processors.Update(ctx, before.ID, newConfig)
	assert.Error(t, err)
	assert.Equal(t, pipeline.ErrPipelineRunning, err)
	assert.Nil(t, got)
}

func TestProcessorOrchestrator_UpdateOnPipeline_UpdateFail(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}
	before := &processor.Instance{
		ID:   uuid.NewString(),
		Name: "test-processor",
		Parent: processor.Parent{
			ID:   pl.ID,
			Type: processor.ParentTypePipeline,
		},
		Config: processor.Config{
			Settings: map[string]string{"foo": "bar"},
		},
	}
	newConfig := processor.Config{
		Settings: map[string]string{"foo2": "bar2"},
	}
	want := &processor.Instance{
		ID:   before.ID,
		Name: "test-processor",
		Parent: processor.Parent{
			ID:   pl.ID,
			Type: processor.ParentTypePipeline,
		},
		Config: processor.Config{
			Settings: map[string]string{"foo2": "bar2"},
		},
	}

	wantErr := cerrors.New("couldn't update processor")
	procsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), want.ID).
		Return(before, nil)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	procsMock.EXPECT().
		Update(gomock.AssignableToTypeOf(ctxType), want.ID, want.Config).
		Return(nil, wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	got, err := orc.Processors.Update(ctx, before.ID, newConfig)
	assert.Error(t, err)
	assert.Equal(t, wantErr, err)
	assert.Nil(t, got)
}

func TestProcessorOrchestrator_UpdateOnConnector_ConnectorNotExist(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)
	connBuilder := connmock.Builder{Ctrl: gomock.NewController(t)}

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}
	newConfig := processor.Config{
		Settings: map[string]string{"foo2": "bar2"},
	}
	conn := connBuilder.NewSourceMock(uuid.NewString(), connector.Config{PipelineID: pl.ID})
	want := &processor.Instance{
		ID:   uuid.NewString(),
		Name: "test-processor",
		Parent: processor.Parent{
			ID:   conn.ID(),
			Type: processor.ParentTypeConnector,
		},
		Config: processor.Config{
			Settings: map[string]string{"foo": "bar"},
		},
	}
	wantErr := cerrors.New("connector doesn't exist")

	procsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), want.ID).
		Return(want, nil)
	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), conn.ID()).
		Return(nil, wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	got, err := orc.Processors.Update(ctx, want.ID, newConfig)
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, wantErr), "errors did not match")
	assert.Nil(t, got)
}

func TestProcessorOrchestrator_DeleteOnPipeline_Success(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}
	want := &processor.Instance{
		ID:   uuid.NewString(),
		Name: "test-processor",
		Parent: processor.Parent{
			ID:   pl.ID,
			Type: processor.ParentTypePipeline,
		},
		Config: processor.Config{
			Settings: map[string]string{"foo": "bar"},
		},
	}

	procsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), want.ID).
		Return(want, nil)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	procsMock.EXPECT().
		Delete(gomock.AssignableToTypeOf(ctxType), want.ID).
		Return(nil)
	plsMock.EXPECT().
		RemoveProcessor(gomock.AssignableToTypeOf(ctxType), pl, want.ID).
		Return(pl, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	err := orc.Processors.Delete(ctx, want.ID)
	assert.Ok(t, err)
}

func TestProcessorOrchestrator_DeleteOnPipeline_ProcessorNotExist(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}
	want := &processor.Instance{
		ID:   uuid.NewString(),
		Name: "test-processor",
		Parent: processor.Parent{
			ID:   pl.ID,
			Type: processor.ParentTypePipeline,
		},
		Config: processor.Config{
			Settings: map[string]string{"foo": "bar"},
		},
	}

	wantErr := cerrors.New("processor doesn't exist")
	procsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), want.ID).
		Return(nil, wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	err := orc.Processors.Delete(ctx, want.ID)
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, wantErr), "errors did not match")
}

func TestProcessorOrchestrator_DeleteOnPipeline_PipelineRunning(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusRunning,
	}
	want := &processor.Instance{
		ID:   uuid.NewString(),
		Name: "test-processor",
		Parent: processor.Parent{
			ID:   pl.ID,
			Type: processor.ParentTypePipeline,
		},
		Config: processor.Config{
			Settings: map[string]string{"foo": "bar"},
		},
	}

	procsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), want.ID).
		Return(want, nil)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	err := orc.Processors.Delete(ctx, want.ID)
	assert.Error(t, err)
	assert.Equal(t, pipeline.ErrPipelineRunning, err)
}

func TestProcessorOrchestrator_DeleteOnPipeline_Fail(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}
	want := &processor.Instance{
		ID:   uuid.NewString(),
		Name: "test-processor",
		Parent: processor.Parent{
			ID:   pl.ID,
			Type: processor.ParentTypePipeline,
		},
		Config: processor.Config{
			Settings: map[string]string{"foo": "bar"},
		},
	}

	wantErr := cerrors.New("couldn't delete the procesor")
	procsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), want.ID).
		Return(want, nil)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	procsMock.EXPECT().
		Delete(gomock.AssignableToTypeOf(ctxType), want.ID).
		Return(wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	err := orc.Processors.Delete(ctx, want.ID)
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, wantErr), "errors did not match")
}

func TestProcessorOrchestrator_DeleteOnPipeline_RemoveProcessorFail(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}
	want := &processor.Instance{
		ID:   uuid.NewString(),
		Name: "test-processor",
		Parent: processor.Parent{
			ID:   pl.ID,
			Type: processor.ParentTypePipeline,
		},
		Config: processor.Config{
			Settings: map[string]string{"foo": "bar"},
		},
	}

	wantErr := cerrors.New("couldn't remove the processor")
	procsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), want.ID).
		Return(want, nil)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	procsMock.EXPECT().
		Delete(gomock.AssignableToTypeOf(ctxType), want.ID).
		Return(nil)
	plsMock.EXPECT().
		RemoveProcessor(gomock.AssignableToTypeOf(ctxType), pl, want.ID).
		Return(nil, wantErr)
	// rollback
	procsMock.EXPECT().
		Create(
			gomock.AssignableToTypeOf(ctxType),
			want.ID,
			want.Name,
			want.Parent,
			want.Config,
		).
		Return(want, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	err := orc.Processors.Delete(ctx, want.ID)
	assert.Error(t, err)
}

func TestProcessorOrchestrator_DeleteOnConnector_Fail(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)
	connBuilder := connmock.Builder{Ctrl: gomock.NewController(t)}

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}
	conn := connBuilder.NewSourceMock(uuid.NewString(), connector.Config{PipelineID: pl.ID})
	want := &processor.Instance{
		ID:   uuid.NewString(),
		Name: "test-processor",
		Parent: processor.Parent{
			ID:   conn.ID(),
			Type: processor.ParentTypeConnector,
		},
		Config: processor.Config{
			Settings: map[string]string{"foo": "bar"},
		},
	}

	wantErr := cerrors.New("couldn't remove processor from connector")
	procsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), want.ID).
		Return(want, nil)
	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), conn.ID()).
		Return(conn, nil)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	procsMock.EXPECT().
		Delete(gomock.AssignableToTypeOf(ctxType), want.ID).
		Return(nil)
	consMock.EXPECT().
		RemoveProcessor(gomock.AssignableToTypeOf(ctxType), want.Parent.ID, want.ID).
		Return(nil, wantErr)
	// rollback
	procsMock.EXPECT().
		Create(
			gomock.AssignableToTypeOf(ctxType),
			want.ID,
			want.Name,
			want.Parent,
			want.Config,
		).
		Return(want, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	err := orc.Processors.Delete(ctx, want.ID)
	assert.Error(t, err)
}
