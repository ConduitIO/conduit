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
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
)

func TestConnectorOrchestrator_Create_Success(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)
	connBuilder := connmock.Builder{Ctrl: gomock.NewController(t)}

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}
	config := connector.Config{
		Name:       "test-connector",
		Settings:   map[string]string{"foo": "bar"},
		Plugin:     "test-plugin",
		PipelineID: pl.ID,
	}
	want := connBuilder.NewSourceMock(uuid.NewString(), config)

	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	consMock.EXPECT().
		Create(
			gomock.AssignableToTypeOf(ctxType),
			gomock.AssignableToTypeOf(""),
			connector.TypeSource,
			config,
			connector.TypeAPI,
		).Return(want, nil)
	plsMock.EXPECT().
		AddConnector(gomock.AssignableToTypeOf(ctxType), pl, want.ID()).
		Return(pl, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	got, err := orc.Connectors.Create(ctx, connector.TypeSource, config)
	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func TestConnectorOrchestrator_Create_PipelineNotExist(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	pipelineID := uuid.NewString()
	wantErr := pipeline.ErrInstanceNotFound
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pipelineID).
		Return(nil, wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	got, err := orc.Connectors.Create(ctx, connector.TypeSource, connector.Config{PipelineID: pipelineID})
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, wantErr), "errors did not match")
	assert.Nil(t, got)
}

func TestConnectorOrchestrator_Create_PipelineRunning(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusRunning,
	}

	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	got, err := orc.Connectors.Create(ctx, connector.TypeSource, connector.Config{PipelineID: pl.ID})
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, pipeline.ErrPipelineRunning), "expected pipeline.ErrPipelineRunning")
	assert.Nil(t, got)
}

func TestConnectorOrchestrator_Create_CreateConnectorError(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}
	config := connector.Config{PipelineID: pl.ID}
	wantErr := cerrors.New("test error")
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	consMock.EXPECT().
		Create(
			gomock.AssignableToTypeOf(ctxType),
			gomock.AssignableToTypeOf(""),
			connector.TypeSource,
			config,
			connector.TypeAPI,
		).
		Return(nil, wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	got, err := orc.Connectors.Create(ctx, connector.TypeSource, config)
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, wantErr), "errors did not match")
	assert.Nil(t, got)
}

func TestConnectorOrchestrator_Create_AddConnectorError(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)
	connBuilder := connmock.Builder{Ctrl: gomock.NewController(t)}

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}
	config := connector.Config{PipelineID: pl.ID}
	wantErr := cerrors.New("test error")
	conn := connBuilder.NewSourceMock(uuid.NewString(), config)

	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	consMock.EXPECT().
		Create(
			gomock.AssignableToTypeOf(ctxType),
			gomock.AssignableToTypeOf(""),
			connector.TypeSource,
			config,
			connector.TypeAPI,
		).
		Return(conn, nil)
	plsMock.EXPECT().
		AddConnector(gomock.AssignableToTypeOf(ctxType), pl, conn.ID()).
		Return(nil, wantErr)
	// this is called in rollback
	consMock.EXPECT().
		Delete(gomock.AssignableToTypeOf(ctxType), conn.ID()).
		Return(nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	got, err := orc.Connectors.Create(ctx, connector.TypeSource, config)
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, wantErr), "errors did not match")
	assert.Nil(t, got)
}

func TestConnectorOrchestrator_Delete_Success(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)
	connBuilder := connmock.Builder{Ctrl: gomock.NewController(t)}

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}
	config := connector.Config{PipelineID: pl.ID}
	want := connBuilder.NewSourceMock(uuid.NewString(), config)

	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), want.ID()).
		Return(want, nil)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	consMock.EXPECT().
		Delete(gomock.AssignableToTypeOf(ctxType), want.ID()).
		Return(nil)
	plsMock.EXPECT().
		RemoveConnector(gomock.AssignableToTypeOf(ctxType), pl, want.ID()).
		Return(pl, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	err := orc.Connectors.Delete(ctx, want.ID())
	assert.Ok(t, err)
}

func TestConnectorOrchestrator_Delete_ConnectorNotExist(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)
	connBuilder := connmock.Builder{Ctrl: gomock.NewController(t)}

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}

	config := connector.Config{
		Name:       "test-connector",
		Settings:   map[string]string{"foo": "bar"},
		Plugin:     "test-plugin",
		PipelineID: pl.ID,
	}
	want := connBuilder.NewSourceMock(uuid.NewString(), config)

	wantErr := cerrors.New("connector doesn't exist")
	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), want.ID()).
		Return(nil, wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	err := orc.Connectors.Delete(ctx, want.ID())
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, wantErr), "errors did not match")
}

func TestConnectorOrchestrator_Delete_PipelineRunning(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)
	connBuilder := connmock.Builder{Ctrl: gomock.NewController(t)}

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusRunning,
	}

	config := connector.Config{
		Name:       "test-connector",
		Settings:   map[string]string{"foo": "bar"},
		Plugin:     "test-plugin",
		PipelineID: pl.ID,
	}
	want := connBuilder.NewSourceMock(uuid.NewString(), config)

	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), want.ID()).
		Return(want, nil)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	err := orc.Connectors.Delete(ctx, want.ID())
	assert.Error(t, err)
	assert.Equal(t, pipeline.ErrPipelineRunning, err)
}

func TestConnectorOrchestrator_Delete_ProcessorAttached(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)
	connBuilder := connmock.Builder{Ctrl: gomock.NewController(t)}

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusRunning,
	}
	config := connector.Config{
		Name:         "test-connector",
		Settings:     map[string]string{"foo": "bar"},
		Plugin:       "test-plugin",
		PipelineID:   pl.ID,
		ProcessorIDs: []string{uuid.NewString()},
	}
	want := connBuilder.NewSourceMock(uuid.NewString(), config)

	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), want.ID()).
		Return(want, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	err := orc.Connectors.Delete(ctx, want.ID())
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, ErrConnectorHasProcessorsAttached), "errors did not match")
}

func TestConnectorOrchestrator_Delete_Fail(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)
	connBuilder := connmock.Builder{Ctrl: gomock.NewController(t)}

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}
	config := connector.Config{
		Name:       "test-connector",
		Settings:   map[string]string{"foo": "bar"},
		Plugin:     "test-plugin",
		PipelineID: pl.ID,
	}
	want := connBuilder.NewSourceMock(uuid.NewString(), config)
	wantErr := cerrors.New("connector deletion failed")

	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), want.ID()).
		Return(want, nil)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	consMock.EXPECT().
		Delete(gomock.AssignableToTypeOf(ctxType), want.ID()).
		Return(wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	err := orc.Connectors.Delete(ctx, want.ID())
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, wantErr), "errors did not match")
}

func TestConnectorOrchestrator_Delete_RemoveConnectorFailed(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)
	connBuilder := connmock.Builder{Ctrl: gomock.NewController(t)}

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}
	config := connector.Config{
		Name:       "test-connector",
		Settings:   map[string]string{"foo": "bar"},
		Plugin:     "test-plugin",
		PipelineID: pl.ID,
	}
	want := connBuilder.NewSourceMock(uuid.NewString(), config)
	wantErr := cerrors.New("couldn't remove the connector from the pipeline")

	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), want.ID()).
		Return(want, nil)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	consMock.EXPECT().
		Delete(gomock.AssignableToTypeOf(ctxType), want.ID()).
		Return(nil)
	plsMock.EXPECT().
		RemoveConnector(gomock.AssignableToTypeOf(ctxType), pl, want.ID()).
		Return(nil, wantErr)
	// rollback
	consMock.EXPECT().
		Create(
			gomock.AssignableToTypeOf(ctxType),
			want.ID(),
			want.Type(),
			want.Config(),
			connector.TypeAPI,
		).
		Return(want, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	err := orc.Connectors.Delete(ctx, want.ID())
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, wantErr), "errors did not match")
}

func TestConnectorOrchestrator_Update_Success(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)
	connBuilder := connmock.Builder{Ctrl: gomock.NewController(t)}

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}
	oldConfig := connector.Config{
		Name:       "test-connector",
		Settings:   map[string]string{"foo": "bar"},
		Plugin:     "test-plugin",
		PipelineID: pl.ID,
	}
	newConfig := connector.Config{
		Name:       "updated-connector",
		Settings:   map[string]string{"foo": "baz"},
		Plugin:     "test-plugin",
		PipelineID: pl.ID,
	}
	before := connBuilder.NewSourceMock(uuid.NewString(), oldConfig)
	want := connBuilder.NewSourceMock(before.ID(), newConfig)

	consMock.EXPECT().
		Get(
			gomock.AssignableToTypeOf(ctxType),
			before.ID(),
		).
		Return(before, nil)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	consMock.EXPECT().
		Update(
			gomock.AssignableToTypeOf(ctxType),
			before.ID(),
			newConfig,
		).
		Return(want, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	got, err := orc.Connectors.Update(ctx, before.ID(), newConfig)
	assert.NotNil(t, got)
	assert.Equal(t, got, want)
	assert.Ok(t, err)
}

func TestConnectorOrchestrator_Update_ConnectorNotExist(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)
	connBuilder := connmock.Builder{Ctrl: gomock.NewController(t)}

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}
	oldConfig := connector.Config{
		Name:       "test-connector",
		Settings:   map[string]string{"foo": "bar"},
		Plugin:     "test-plugin",
		PipelineID: pl.ID,
	}
	newConfig := connector.Config{
		Name:       "updated-connector",
		Settings:   map[string]string{"foo": "baz"},
		Plugin:     "test-plugin",
		PipelineID: pl.ID,
	}
	before := connBuilder.NewSourceMock(uuid.NewString(), oldConfig)

	wantErr := cerrors.New("connector doesn't exist")
	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), before.ID()).
		Return(nil, wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	got, err := orc.Connectors.Update(ctx, before.ID(), newConfig)
	assert.Nil(t, got)
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, wantErr), "errors did not match")
}

func TestConnectorOrchestrator_Update_PipelineRunning(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)
	connBuilder := connmock.Builder{Ctrl: gomock.NewController(t)}

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusRunning,
	}
	oldConfig := connector.Config{
		Name:       "test-connector",
		Settings:   map[string]string{"foo": "bar"},
		Plugin:     "test-plugin",
		PipelineID: pl.ID,
	}
	newConfig := connector.Config{
		Name:       "updated-connector",
		Settings:   map[string]string{"foo": "bar"},
		Plugin:     "test-plugin",
		PipelineID: pl.ID,
	}
	conn := connBuilder.NewSourceMock(uuid.NewString(), oldConfig)

	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), conn.ID()).
		Return(conn, nil)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	got, err := orc.Connectors.Update(ctx, conn.ID(), newConfig)
	assert.Nil(t, got)
	assert.Error(t, err)
	assert.Equal(t, pipeline.ErrPipelineRunning, err)
}

func TestConnectorOrchestrator_Update_Fail(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, pluginMock := newMockServices(t)
	connBuilder := connmock.Builder{Ctrl: gomock.NewController(t)}

	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Status: pipeline.StatusSystemStopped,
	}

	oldConfig := connector.Config{
		Name:       "test-connector",
		Settings:   map[string]string{"foo": "bar"},
		Plugin:     "test-plugin",
		PipelineID: pl.ID,
	}
	newConfig := connector.Config{
		Name:       "updated-connector",
		Settings:   map[string]string{"foo": "bar"},
		Plugin:     "test-plugin",
		PipelineID: pl.ID,
	}
	before := connBuilder.NewSourceMock(uuid.NewString(), oldConfig)
	want := connBuilder.NewSourceMock(before.ID(), newConfig)
	wantErr := cerrors.New("connector update failed")

	consMock.EXPECT().
		Get(
			gomock.AssignableToTypeOf(ctxType),
			before.ID(),
		).
		Return(before, nil)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	consMock.EXPECT().
		Update(
			gomock.AssignableToTypeOf(ctxType),
			before.ID(),
			newConfig,
		).
		Return(want, wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, pluginMock)
	got, err := orc.Connectors.Update(ctx, before.ID(), newConfig)
	assert.Nil(t, got)
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, wantErr), "errors did not match")
}
