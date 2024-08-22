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
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestConnectorOrchestrator_Create_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	pl.SetStatus(pipeline.StatusSystemStopped)

	want := &connector.Instance{
		ID:   uuid.NewString(),
		Type: connector.TypeSource,
		Config: connector.Config{
			Name:     "test-connector",
			Settings: map[string]string{"foo": "bar"},
		},
		PipelineID:    pl.ID,
		Plugin:        "test-plugin",
		ProcessorIDs:  nil,
		State:         nil,
		ProvisionedBy: connector.ProvisionTypeAPI,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}

	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	connPluginMock.EXPECT().
		ValidateSourceConfig(
			gomock.AssignableToTypeOf(ctxType),
			want.Plugin,
			want.Config.Settings,
		).Return(nil)
	consMock.EXPECT().
		Create(
			gomock.AssignableToTypeOf(ctxType),
			gomock.AssignableToTypeOf(""),
			connector.TypeSource,
			want.Plugin,
			want.PipelineID,
			want.Config,
			connector.ProvisionTypeAPI,
		).Return(want, nil)
	plsMock.EXPECT().
		AddConnector(gomock.AssignableToTypeOf(ctxType), pl.ID, want.ID).
		Return(pl, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	got, err := orc.Connectors.Create(ctx, want.Type, want.Plugin, want.PipelineID, want.Config)
	is.NoErr(err)
	is.Equal(want, got)
}

func TestConnectorOrchestrator_Create_PipelineNotExist(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pipelineID := uuid.NewString()
	wantErr := pipeline.ErrInstanceNotFound
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pipelineID).
		Return(nil, wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	got, err := orc.Connectors.Create(ctx, connector.TypeSource, "test-plugin", pipelineID, connector.Config{})
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr))
	is.True(got == nil)
}

func TestConnectorOrchestrator_Create_PipelineRunning(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	pl.SetStatus(pipeline.StatusRunning)

	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	got, err := orc.Connectors.Create(ctx, connector.TypeSource, "test-plugin", pl.ID, connector.Config{})
	is.True(err != nil)
	is.True(cerrors.Is(err, pipeline.ErrPipelineRunning))
	is.True(got == nil)
}

func TestConnectorOrchestrator_Create_PipelineProvisionByConfig(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID:            uuid.NewString(),
		ProvisionedBy: pipeline.ProvisionTypeConfig,
	}
	pl.SetStatus(pipeline.StatusRunning)

	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	got, err := orc.Connectors.Create(ctx, connector.TypeSource, "test-plugin", pl.ID, connector.Config{})
	is.Equal(got, nil)
	is.True(err != nil)
	is.True(cerrors.Is(err, ErrImmutableProvisionedByConfig)) // expected ErrImmutableProvisionedByConfig
}

func TestConnectorOrchestrator_Create_CreateConnectorError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	pl.SetStatus(pipeline.StatusSystemStopped)

	config := connector.Config{}
	wantErr := cerrors.New("test error")
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	connPluginMock.EXPECT().
		ValidateSourceConfig(
			gomock.AssignableToTypeOf(ctxType),
			"test-plugin",
			config.Settings,
		).Return(nil)
	consMock.EXPECT().
		Create(
			gomock.AssignableToTypeOf(ctxType),
			gomock.AssignableToTypeOf(""),
			connector.TypeSource,
			"test-plugin",
			pl.ID,
			config,
			connector.ProvisionTypeAPI,
		).
		Return(nil, wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	got, err := orc.Connectors.Create(ctx, connector.TypeSource, "test-plugin", pl.ID, config)
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr))
	is.True(got == nil)
}

func TestConnectorOrchestrator_Create_AddConnectorError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	pl.SetStatus(pipeline.StatusSystemStopped)

	conn := &connector.Instance{
		ID:   uuid.NewString(),
		Type: connector.TypeSource,
		Config: connector.Config{
			Name:     "test-connector",
			Settings: map[string]string{"foo": "bar"},
		},
		PipelineID:    pl.ID,
		Plugin:        "test-plugin",
		ProcessorIDs:  nil,
		State:         nil,
		ProvisionedBy: connector.ProvisionTypeAPI,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}

	wantErr := cerrors.New("test error")
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	connPluginMock.EXPECT().
		ValidateSourceConfig(
			gomock.AssignableToTypeOf(ctxType),
			conn.Plugin,
			conn.Config.Settings,
		).Return(nil)
	consMock.EXPECT().
		Create(
			gomock.AssignableToTypeOf(ctxType),
			gomock.AssignableToTypeOf(""),
			conn.Type,
			conn.Plugin,
			conn.PipelineID,
			conn.Config,
			connector.ProvisionTypeAPI,
		).
		Return(conn, nil)
	plsMock.EXPECT().
		AddConnector(gomock.AssignableToTypeOf(ctxType), pl.ID, conn.ID).
		Return(nil, wantErr)
	// this is called in rollback
	consMock.EXPECT().
		Delete(gomock.AssignableToTypeOf(ctxType), conn.ID, connPluginMock).
		Return(nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	got, err := orc.Connectors.Create(ctx, connector.TypeSource, conn.Plugin, pl.ID, conn.Config)
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr))
	is.True(got == nil)
}

func TestConnectorOrchestrator_Delete_Success(t *testing.T) {
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

	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), conn.ID).
		Return(conn, nil)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	consMock.EXPECT().
		Delete(gomock.AssignableToTypeOf(ctxType), conn.ID, connPluginMock).
		Return(nil)
	plsMock.EXPECT().
		RemoveConnector(gomock.AssignableToTypeOf(ctxType), pl.ID, conn.ID).
		Return(pl, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	err := orc.Connectors.Delete(ctx, conn.ID)
	is.NoErr(err)
}

func TestConnectorOrchestrator_Delete_ConnectorNotExist(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	id := uuid.NewString()
	wantErr := cerrors.New("connector doesn't exist")
	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), id).
		Return(nil, wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	err := orc.Connectors.Delete(ctx, id)
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr))
}

func TestConnectorOrchestrator_Delete_PipelineRunning(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	pl.SetStatus(pipeline.StatusRunning)

	conn := &connector.Instance{
		ID:         uuid.NewString(),
		PipelineID: pl.ID,
	}

	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), conn.ID).
		Return(conn, nil)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	err := orc.Connectors.Delete(ctx, conn.ID)
	is.True(err != nil)
	is.Equal(pipeline.ErrPipelineRunning, err)
}

func TestConnectorOrchestrator_Delete_ProcessorAttached(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	pl.SetStatus(pipeline.StatusRunning)

	conn := &connector.Instance{
		ID:           uuid.NewString(),
		PipelineID:   pl.ID,
		ProcessorIDs: []string{uuid.NewString()},
	}

	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), conn.ID).
		Return(conn, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	err := orc.Connectors.Delete(ctx, conn.ID)
	is.True(err != nil)
	is.True(cerrors.Is(err, ErrConnectorHasProcessorsAttached))
}

func TestConnectorOrchestrator_Delete_Fail(t *testing.T) {
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
	wantErr := cerrors.New("connector deletion failed")

	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), conn.ID).
		Return(conn, nil)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	consMock.EXPECT().
		Delete(gomock.AssignableToTypeOf(ctxType), conn.ID, connPluginMock).
		Return(wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	err := orc.Connectors.Delete(ctx, conn.ID)
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr))
}

func TestConnectorOrchestrator_Delete_RemoveConnectorFailed(t *testing.T) {
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
		Plugin:     "test-plugin",
		PipelineID: pl.ID,
	}
	wantErr := cerrors.New("couldn't remove the connector from the pipeline")

	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), conn.ID).
		Return(conn, nil)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	consMock.EXPECT().
		Delete(gomock.AssignableToTypeOf(ctxType), conn.ID, connPluginMock).
		Return(nil)
	plsMock.EXPECT().
		RemoveConnector(gomock.AssignableToTypeOf(ctxType), pl.ID, conn.ID).
		Return(nil, wantErr)
	// rollback
	consMock.EXPECT().
		Create(
			gomock.AssignableToTypeOf(ctxType),
			conn.ID,
			conn.Type,
			conn.Plugin,
			pl.ID,
			conn.Config,
			connector.ProvisionTypeAPI,
		).
		Return(conn, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	err := orc.Connectors.Delete(ctx, conn.ID)
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr))
}

func TestConnectorOrchestrator_Update_Success(t *testing.T) {
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
		Plugin:     "test-plugin",
		PipelineID: pl.ID,
		Type:       connector.TypeSource,
		Config: connector.Config{
			Name:     "test-connector",
			Settings: map[string]string{"foo": "bar"},
		},
	}
	newConfig := connector.Config{
		Name:     "updated-connector",
		Settings: map[string]string{"foo": "baz"},
	}
	want := &connector.Instance{
		ID:         uuid.NewString(),
		Plugin:     "test-plugin",
		PipelineID: pl.ID,
		Config:     newConfig,
	}

	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), conn.ID).
		Return(conn, nil)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	connPluginMock.EXPECT().
		ValidateSourceConfig(gomock.Any(), conn.Plugin, newConfig.Settings).
		Return(nil)
	consMock.EXPECT().
		Update(gomock.AssignableToTypeOf(ctxType), conn.ID, newConfig).
		Return(want, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	got, err := orc.Connectors.Update(ctx, conn.ID, newConfig)
	is.NoErr(err)
	is.Equal(got, want)
}

func TestConnectorOrchestrator_Update_ConnectorNotExist(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	id := uuid.NewString()
	wantErr := cerrors.New("connector doesn't exist")
	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), id).
		Return(nil, wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	got, err := orc.Connectors.Update(ctx, id, connector.Config{})
	is.True(got == nil)
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr))
}

func TestConnectorOrchestrator_Update_PipelineRunning(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	plsMock, consMock, procsMock, connPluginMock, procPluginMock := newMockServices(t)

	pl := &pipeline.Instance{
		ID: uuid.NewString(),
	}
	pl.SetStatus(pipeline.StatusRunning)

	conn := &connector.Instance{
		ID:         uuid.NewString(),
		PipelineID: pl.ID,
	}

	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), conn.ID).
		Return(conn, nil)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	got, err := orc.Connectors.Update(ctx, conn.ID, connector.Config{})
	is.True(got == nil)
	is.True(err != nil)
	is.Equal(pipeline.ErrPipelineRunning, err)
}

func TestConnectorOrchestrator_Update_Fail(t *testing.T) {
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
		Type:       connector.TypeDestination,
		PipelineID: pl.ID,
	}
	wantErr := cerrors.New("connector update failed")

	consMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), conn.ID).
		Return(conn, nil)
	plsMock.EXPECT().
		Get(gomock.AssignableToTypeOf(ctxType), pl.ID).
		Return(pl, nil)
	connPluginMock.EXPECT().
		ValidateDestinationConfig(gomock.Any(), conn.Plugin, conn.Config.Settings).
		Return(nil)
	consMock.EXPECT().
		Update(gomock.AssignableToTypeOf(ctxType), conn.ID, connector.Config{}).
		Return(nil, wantErr)

	orc := NewOrchestrator(db, log.Nop(), plsMock, consMock, procsMock, connPluginMock, procPluginMock)
	got, err := orc.Connectors.Update(ctx, conn.ID, connector.Config{})
	is.True(got == nil)
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr))
}
