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

package connector_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/connector/mock"
	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/builtin"
	"github.com/conduitio/conduit/pkg/plugin/standalone"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
)

func TestService_Init_Success(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	connBuilder := mock.Builder{Ctrl: ctrl}

	service := connector.NewService(logger, db, connBuilder)
	_, err := service.Create(
		ctx,
		uuid.NewString(),
		connector.TypeSource,
		connector.Config{
			Name:       "test-connector",
			Settings:   map[string]string{"foo": "bar"},
			Plugin:     "test-plugin",
			PipelineID: uuid.NewString(),
		},
	)
	assert.Ok(t, err)

	want := service.List(ctx)

	// create a new connector service and initialize it
	service = connector.NewService(logger, db, connBuilder)
	err = service.Init(ctx)
	assert.Ok(t, err)

	got := service.List(ctx)
	assert.Equal(t, want, got)
	assert.Equal(t, len(got), 1)
}

func TestService_CreateSuccess(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	connBuilder := mock.Builder{Ctrl: ctrl}

	service := connector.NewService(logger, db, connBuilder)

	testCases := []struct {
		name     string
		connType connector.Type
		want     connector.Connector
	}{{
		name:     "create file destination connector",
		connType: connector.TypeDestination,
		want: connBuilder.NewDestinationMock(
			"my-destination",
			connector.Config{
				Plugin:     "path/to/plugin",
				PipelineID: uuid.NewString(),
			},
		),
	}, {
		name:     "create file source connector",
		connType: connector.TypeSource,
		want: connBuilder.NewSourceMock(
			"my-source",
			connector.Config{
				Plugin:     "path/to/plugin",
				PipelineID: uuid.NewString(),
			},
		),
	}}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := service.Create(
				ctx,
				uuid.NewString(),
				tt.connType,
				tt.want.Config(),
			)
			assert.Ok(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestService_CreateError(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	builder := connector.NewDefaultBuilder(logger, nil, plugin.NewRegistry(
		builtin.NewRegistry(), standalone.NewRegistry(logger)))

	service := connector.NewService(logger, db, builder)

	testCases := []struct {
		name     string
		connType connector.Type
		data     connector.Config
	}{{
		name:     "invalid connector type",
		connType: 0,
		data: connector.Config{
			Name:       "test-connector",
			Settings:   map[string]string{"foo": "bar"},
			Plugin:     "builtin:file",
			PipelineID: uuid.NewString(),
		},
	}, {
		name:     "invalid external plugin",
		connType: connector.TypeSource,
		data: connector.Config{
			Name:       "test-connector",
			Settings:   map[string]string{"foo": "bar"},
			Plugin:     "non-existing-file",
			PipelineID: uuid.NewString(),
		},
	}, {
		name:     "invalid builtin plugin",
		connType: connector.TypeSource,
		data: connector.Config{
			Name:       "test-connector",
			Settings:   map[string]string{"foo": "bar"},
			Plugin:     "builtin:non-existing-plugin",
			PipelineID: uuid.NewString(),
		},
	}, {
		name:     "empty plugin",
		connType: connector.TypeSource,
		data: connector.Config{
			Name:       "test-connector",
			Settings:   map[string]string{"foo": "bar"},
			Plugin:     "",
			PipelineID: uuid.NewString(),
		},
	}, {
		name:     "empty pipeline ID",
		connType: connector.TypeSource,
		data: connector.Config{
			Name:       "test-connector",
			Settings:   map[string]string{"foo": "bar"},
			Plugin:     "builtin:file",
			PipelineID: "",
		},
	}}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := service.Create(
				ctx,
				uuid.NewString(),
				tt.connType,
				tt.data,
			)
			assert.Error(t, err)
			assert.Nil(t, got)
		})
	}
}

func TestService_GetSuccess(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	connBuilder := mock.Builder{Ctrl: ctrl}

	service := connector.NewService(logger, db, connBuilder)
	want, err := service.Create(
		ctx,
		uuid.NewString(),
		connector.TypeSource,
		connector.Config{
			Name:       "test-connector",
			Settings:   map[string]string{"foo": "bar"},
			Plugin:     "test-plugin",
			PipelineID: uuid.NewString(),
		},
	)
	assert.Ok(t, err)

	got, err := service.Get(ctx, want.ID())
	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func TestService_GetInstanceNotFound(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := connector.NewService(logger, db, mock.Builder{})

	// get connector that does not exist
	got, err := service.Get(ctx, uuid.NewString())
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, connector.ErrInstanceNotFound), "did not get expected error")
	assert.Nil(t, got)
}

func TestService_DeleteSuccess(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	connBuilder := mock.Builder{
		Ctrl: ctrl,
		SetupSource: func(source *mock.Source) {
			// return stopped source
			source.EXPECT().IsRunning().Return(false)
		},
	}

	service := connector.NewService(logger, db, connBuilder)
	conn, err := service.Create(
		ctx,
		uuid.NewString(),
		connector.TypeSource,
		connector.Config{
			Name:       "test-connector",
			Settings:   map[string]string{"foo": "bar"},
			Plugin:     "test-plugin",
			PipelineID: uuid.NewString(),
		},
	)
	assert.Ok(t, err)

	err = service.Delete(ctx, conn.ID())
	assert.Ok(t, err)

	got, err := service.Get(ctx, conn.ID())
	assert.Error(t, err)
	assert.Nil(t, got)
}

func TestService_DeleteInstanceNotFound(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := connector.NewService(logger, db, mock.Builder{})
	// delete connector that does not exist
	err := service.Delete(ctx, uuid.NewString())
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, connector.ErrInstanceNotFound), "did not get expected error")
}

func TestService_DeleteConnectorIsRunning(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	connBuilder := mock.Builder{
		Ctrl: ctrl,
		SetupSource: func(source *mock.Source) {
			// return running source
			source.EXPECT().IsRunning().Return(true)
		},
	}

	service := connector.NewService(logger, db, connBuilder)
	conn, err := service.Create(
		ctx,
		uuid.NewString(),
		connector.TypeSource,
		connector.Config{
			Name:       "test-connector",
			Settings:   map[string]string{"foo": "bar"},
			Plugin:     "test-plugin",
			PipelineID: uuid.NewString(),
		},
	)
	assert.Ok(t, err)

	// delete connector that is running
	err = service.Delete(ctx, conn.ID())
	assert.Error(t, err)
}

func TestService_List(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	connBuilder := mock.Builder{Ctrl: ctrl}

	service := connector.NewService(logger, db, connBuilder)
	want := make(map[string]connector.Connector)
	for i := 0; i < 10; i++ {
		conn, err := service.Create(
			ctx,
			uuid.NewString(),
			connector.TypeSource,
			connector.Config{
				Name:       fmt.Sprintf("test-connector-%d", i),
				Settings:   map[string]string{"foo": "bar"},
				Plugin:     "test-plugin",
				PipelineID: uuid.NewString(),
			},
		)
		assert.Ok(t, err)
		want[conn.ID()] = conn
	}

	got := service.List(ctx)
	assert.Equal(t, want, got)
}

func TestService_UpdateSuccess(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)

	want := connector.Config{
		Name:       "changed-name",
		Settings:   map[string]string{"foo": "bar"},
		Plugin:     "test-plugin",
		PipelineID: uuid.NewString(),
	}

	connBuilder := mock.Builder{
		Ctrl: ctrl,
		SetupSource: func(source *mock.Source) {
			source.EXPECT().Validate(ctx, want.Settings).Return(nil)
			source.EXPECT().SetConfig(want)
		},
	}

	service := connector.NewService(logger, db, connBuilder)
	conn, err := service.Create(
		ctx,
		uuid.NewString(),
		connector.TypeSource,
		connector.Config{
			Name:       "test-connector",
			Settings:   map[string]string{"foo": "bar"},
			Plugin:     "test-plugin",
			PipelineID: uuid.NewString(),
		},
	)
	assert.Ok(t, err)

	_, err = service.Update(ctx, conn.ID(), want)
	assert.Ok(t, err)
}

func TestService_UpdateInstanceNotFound(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := connector.NewService(logger, db, mock.Builder{})
	// update connector that does not exist
	got, err := service.Update(ctx, uuid.NewString(), connector.Config{})
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, connector.ErrInstanceNotFound), "did not get expected error")
	assert.Nil(t, got)
}

func TestService_UpdateInvalidConfig(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)

	config := connector.Config{
		Name: "changed-name",
		Settings: map[string]string{
			"missing-param": "path is required",
		},
	}
	wantErr := cerrors.New("invalid settings")

	connBuilder := mock.Builder{
		Ctrl: ctrl,
		SetupSource: func(source *mock.Source) {
			source.EXPECT().Validate(ctx, config.Settings).Return(wantErr)
		},
	}

	service := connector.NewService(logger, db, connBuilder)
	conn, err := service.Create(
		ctx,
		uuid.NewString(),
		connector.TypeSource,
		connector.Config{
			Name:       "test-connector",
			Settings:   map[string]string{"foo": "bar"},
			Plugin:     "test-plugin",
			PipelineID: uuid.NewString(),
		},
	)
	assert.Ok(t, err)

	got, err := service.Update(ctx, conn.ID(), config)
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, wantErr), "did not get expected error")
	assert.Nil(t, got)
}
