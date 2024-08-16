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
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestProcessorOrchestrator_CreateOnPipeline_Success(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	pl.SetStatus(pipeline.StatusSystemStopped)

	want := &processor.Instance{
		ID:     uuid.NewString(),
		Plugin: "test-processor",
		Parent: processor.Parent{
			ID:   pl.ID,
			Type: processor.ParentTypePipeline,
		},
		Config: processor.Config{
			Settings: map[string]string{"foo": "bar"},
		},
		Condition: "{{ true }}",
	}

	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	procsMock.EXPECT().
		Create(
			gomock.AssignableToTypeOf(ctxType),
			gomock.AssignableToTypeOf(""),
			want.Plugin,
			want.Parent,
			want.Config,
			processor.ProvisionTypeAPI,
			want.Condition,
		).
		Return(want, nil)
	plsMock.EXPECT().
		AddProcessor(gomock.AssignableToTypeOf(ctxType), pl.ID, want.ID).
		Return(pl, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	got, err := orc.Processors.Create(ctx, want.Plugin, want.Parent, want.Config, want.Condition)
	is.NoErr(err)
	is.Equal(want, got)
}

func TestProcessorOrchestrator_CreateOnPipeline_PipelineNotExist(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	parent := processor.Parent{
		ID:   uuid.NewString(),
		Type: processor.ParentTypePipeline,
	}
	wantErr := pipeline.ErrInstanceNotFound
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), parent.ID).
		Return(nil, wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	got, err := orc.Processors.Create(ctx, "test-processor", parent, processor.Config{}, "")
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr)) // errors did not match
	is.True(got == nil)
}

func TestProcessorOrchestrator_CreateOnPipeline_PipelineRunning(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	pl.SetStatus(pipeline.StatusRunning)

	parent := processor.Parent{
		ID:   pl.ID,
		Type: processor.ParentTypePipeline,
	}
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	got, err := orc.Processors.Create(ctx, "test-processor", parent, processor.Config{}, "")
	is.True(err != nil)
	is.Equal(pipeline.ErrPipelineRunning, err)
	is.True(got == nil)
}

func TestProcessorOrchestrator_CreateOnPipeline_PipelineProvisionedByConfig(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID:            uuid.NewString(),
		ProvisionedBy: pipeline.ProvisionTypeConfig,
	}
	pl.SetStatus(pipeline.StatusUserStopped)

	parent := processor.Parent{
		ID:   pl.ID,
		Type: processor.ParentTypePipeline,
	}
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	got, err := orc.Processors.Create(ctx, "test-processor", parent, processor.Config{}, "")
	is.Equal(got, nil)
	is.True(err != nil)
	is.True(cerrors.Is(err, ErrImmutableProvisionedByConfig)) // expected ErrImmutableProvisionedByConfig
}

func TestProcessorOrchestrator_CreateOnPipeline_CreateProcessorError(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	pl.SetStatus(pipeline.StatusSystemStopped)

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
			processor.ProvisionTypeAPI,
			"",
		).
		Return(nil, wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	got, err := orc.Processors.Create(ctx, "test-processor", parent, processor.Config{}, "")
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr)) // errors did not match
	is.True(got == nil)
}

func TestProcessorOrchestrator_CreateOnPipeline_AddProcessorError(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	pl.SetStatus(pipeline.StatusSystemStopped)

	proc := &processor.Instance{
		ID:     uuid.NewString(),
		Plugin: "test-processor",
		Parent: processor.Parent{
			ID:   pl.ID,
			Type: processor.ParentTypePipeline,
		},
		Config: processor.Config{
			Settings: map[string]string{"foo": "bar"},
		},
		Condition: "{{ true }}",
	}
	wantErr := cerrors.New("test error")

	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	procsMock.EXPECT().
		Create(
			gomock.AssignableToTypeOf(ctxType),
			gomock.AssignableToTypeOf(""),
			proc.Plugin,
			proc.Parent,
			proc.Config,
			processor.ProvisionTypeAPI,
			proc.Condition,
		).
		Return(proc, nil)
	plsMock.EXPECT().
		AddProcessor(gomock.AssignableToTypeOf(ctxType), pl.ID, proc.ID).
		Return(nil, wantErr)
	// this is called in rollback
	procsMock.EXPECT().
		Delete(gomock.AssignableToTypeOf(ctxType), proc.ID).
		Return(nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	got, err := orc.Processors.Create(ctx, proc.Plugin, proc.Parent, proc.Config, proc.Condition)
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr)) // errors did not match
	is.True(got == nil)
}

func TestProcessorOrchestrator_CreateOnConnector_Success(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	pl.SetStatus(pipeline.StatusSystemStopped)

	conn := &connector.Instance{
		ID:         uuid.NewString(),
		PipelineID: pl.ID,
	}
	want := &processor.Instance{
		ID:     uuid.NewString(),
		Plugin: "test-processor",
		Parent: processor.Parent{
			ID:   conn.ID,
			Type: processor.ParentTypeConnector,
		},
		Config: processor.Config{
			Settings: map[string]string{"foo": "bar"},
		},
		Condition: "{{ true }}",
	}

	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), conn.ID).
		Return(conn, nil)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	procsMock.EXPECT().
		Create(
			gomock.AssignableToTypeOf(ctxType),
			gomock.AssignableToTypeOf(""),
			want.Plugin,
			want.Parent,
			want.Config,
			processor.ProvisionTypeAPI,
			want.Condition,
		).
		Return(want, nil)
	consMock.EXPECT().
		AddProcessor(gomock.AssignableToTypeOf(ctxType), conn.ID, want.ID).
		Return(conn, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	got, err := orc.Processors.Create(ctx, want.Plugin, want.Parent, want.Config, want.Condition)
	is.NoErr(err)
	is.Equal(want, got)
}

func TestProcessorOrchestrator_CreateOnConnector_ConnectorNotExist(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	parent := processor.Parent{
		ID:   uuid.NewString(),
		Type: processor.ParentTypeConnector,
	}
	wantErr := connector.ErrInstanceNotFound
	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), parent.ID).
		Return(nil, wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	got, err := orc.Processors.Create(ctx, "test-processor", parent, processor.Config{}, "")
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr)) // errors did not match
	is.True(got == nil)
}

func TestProcessorOrchestrator_UpdateOnPipeline_Success(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	pl.SetStatus(pipeline.StatusSystemStopped)

	before := &processor.Instance{
		ID:     uuid.NewString(),
		Plugin: "test-processor",
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
		ID:     before.ID,
		Plugin: "test-processor",
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

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	got, err := orc.Processors.Update(ctx, before.ID, newConfig)
	is.NoErr(err)
	is.Equal(want, got)
}

func TestProcessorOrchestrator_UpdateOnPipeline_ProcessorNotExist(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	pl.SetStatus(pipeline.StatusSystemStopped)

	before := &processor.Instance{
		ID:     uuid.NewString(),
		Plugin: "test-processor",
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

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	got, err := orc.Processors.Update(ctx, before.ID, newConfig)
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr)) // errors did not match")
	is.True(got == nil)
}

func TestProcessorOrchestrator_UpdateOnPipeline_PipelineRunning(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	pl.SetStatus(pipeline.StatusRunning)

	before := &processor.Instance{
		ID:     uuid.NewString(),
		Plugin: "test-processor",
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

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	got, err := orc.Processors.Update(ctx, before.ID, newConfig)
	is.True(err != nil)
	is.Equal(pipeline.ErrPipelineRunning, err)
	is.True(got == nil)
}

func TestProcessorOrchestrator_UpdateOnPipeline_ProcessorProvisionedByConfig(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID:            uuid.NewString(),
		ProvisionedBy: pipeline.ProvisionTypeConfig,
	}
	pl.SetStatus(pipeline.StatusRunning)

	before := &processor.Instance{
		ID:     uuid.NewString(),
		Plugin: "test-processor",
		Parent: processor.Parent{
			ID:   pl.ID,
			Type: processor.ParentTypePipeline,
		},
		Config: processor.Config{
			Settings: map[string]string{"foo": "bar"},
		},
		ProvisionedBy: processor.ProvisionTypeConfig,
	}
	newConfig := processor.Config{
		Settings: map[string]string{"foo2": "bar2"},
	}

	procsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), before.ID).
		Return(before, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	got, err := orc.Processors.Update(ctx, before.ID, newConfig)
	is.Equal(got, nil)
	is.True(err != nil)
	is.True(cerrors.Is(err, ErrImmutableProvisionedByConfig)) // expected ErrImmutableProvisionedByConfig
}

func TestProcessorOrchestrator_UpdateOnPipeline_UpdateFail(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	pl.SetStatus(pipeline.StatusSystemStopped)

	before := &processor.Instance{
		ID:     uuid.NewString(),
		Plugin: "test-processor",
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
		ID:     before.ID,
		Plugin: "test-processor",
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

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	got, err := orc.Processors.Update(ctx, before.ID, newConfig)
	is.True(err != nil)
	is.Equal(wantErr, err)
	is.True(got == nil)
}

func TestProcessorOrchestrator_UpdateOnConnector_ConnectorNotExist(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	connID := uuid.NewString()
	want := &processor.Instance{
		ID:     uuid.NewString(),
		Plugin: "test-processor",
		Parent: processor.Parent{
			ID:   connID,
			Type: processor.ParentTypeConnector,
		},
	}
	wantErr := cerrors.New("connector doesn't exist")

	procsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), want.ID).
		Return(want, nil)
	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), connID).
		Return(nil, wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	got, err := orc.Processors.Update(ctx, want.ID, processor.Config{})
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr)) // errors did not match
	is.True(got == nil)
}

func TestProcessorOrchestrator_DeleteOnPipeline_Success(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	pl.SetStatus(pipeline.StatusSystemStopped)

	want := &processor.Instance{
		ID:     uuid.NewString(),
		Plugin: "test-processor",
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
		RemoveProcessor(gomock.AssignableToTypeOf(ctxType), pl.ID, want.ID).
		Return(pl, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	err := orc.Processors.Delete(ctx, want.ID)
	is.NoErr(err)
}

func TestProcessorOrchestrator_DeleteOnPipeline_ProcessorNotExist(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	pl.SetStatus(pipeline.StatusSystemStopped)

	want := &processor.Instance{
		ID:     uuid.NewString(),
		Plugin: "test-processor",
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

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	err := orc.Processors.Delete(ctx, want.ID)
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr)) // errors did not match
}

func TestProcessorOrchestrator_DeleteOnPipeline_PipelineRunning(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	pl.SetStatus(pipeline.StatusRunning)
	want := &processor.Instance{
		ID:     uuid.NewString(),
		Plugin: "test-processor",
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

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	err := orc.Processors.Delete(ctx, want.ID)
	is.True(err != nil)
	is.Equal(pipeline.ErrPipelineRunning, err)
}

func TestProcessorOrchestrator_DeleteOnPipeline_Fail(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	pl.SetStatus(pipeline.StatusSystemStopped)

	want := &processor.Instance{
		ID:     uuid.NewString(),
		Plugin: "test-processor",
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

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	err := orc.Processors.Delete(ctx, want.ID)
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr)) // errors did not match
}

func TestProcessorOrchestrator_DeleteOnPipeline_RemoveProcessorFail(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	pl.SetStatus(pipeline.StatusSystemStopped)

	want := &processor.Instance{
		ID:     uuid.NewString(),
		Plugin: "test-processor",
		Parent: processor.Parent{
			ID:   pl.ID,
			Type: processor.ParentTypePipeline,
		},
		Config: processor.Config{
			Settings: map[string]string{"foo": "bar"},
		},
		Condition: "{{ true }}",
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
		RemoveProcessor(gomock.AssignableToTypeOf(ctxType), pl.ID, want.ID).
		Return(nil, wantErr)
	// rollback
	procsMock.EXPECT().
		Create(
			gomock.AssignableToTypeOf(ctxType),
			want.ID,
			want.Plugin,
			want.Parent,
			want.Config,
			processor.ProvisionTypeAPI,
			want.Condition,
		).
		Return(want, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	err := orc.Processors.Delete(ctx, want.ID)
	is.True(err != nil)
}

func TestProcessorOrchestrator_DeleteOnConnector_Fail(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	pl.SetStatus(pipeline.StatusSystemStopped)

	conn := &connector.Instance{
		ID:         uuid.NewString(),
		PipelineID: pl.ID,
	}
	want := &processor.Instance{
		ID:     uuid.NewString(),
		Plugin: "test-processor",
		Parent: processor.Parent{
			ID:   conn.ID,
			Type: processor.ParentTypeConnector,
		},
		Config: processor.Config{
			Settings: map[string]string{"foo": "bar"},
		},
		Condition: "{{ true }}",
	}

	wantErr := cerrors.New("couldn't remove processor from connector")
	procsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), want.ID).
		Return(want, nil)
	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), conn.ID).
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
			want.Plugin,
			want.Parent,
			want.Config,
			processor.ProvisionTypeAPI,
			want.Condition,
		).
		Return(want, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	err := orc.Processors.Delete(ctx, want.ID)
	is.True(err != nil)
}
