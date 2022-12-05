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
	"time"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/connector/mock"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/builtin"
	"github.com/conduitio/conduit/pkg/plugin/standalone"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
	"github.com/matryer/is"
)

func TestService_Init_Success(t *testing.T) {
	is := is.New(t)
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
		connector.ProvisionTypeAPI,
	)
	is.NoErr(err)

	want := service.List(ctx)

	// create a new connector service and initialize it
	service = connector.NewService(logger, db, connBuilder)
	err = service.Init(ctx)
	is.NoErr(err)

	got := service.List(ctx)
	is.Equal(want, got)
	is.Equal(len(got), 1)
}

func TestService_CreateSuccess(t *testing.T) {
	is := is.New(t)
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
				connector.ProvisionTypeAPI,
			)
			is.NoErr(err)
			is.Equal(tt.want, got)

			got2, err := service.Get(ctx, got.ID())
			is.NoErr(err)
			is.Equal(got, got2)
		})
	}
}

func TestService_CreateDLQ(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	connBuilder := mock.Builder{Ctrl: ctrl}

	service := connector.NewService(logger, db, connBuilder)
	got, err := service.Create(
		ctx,
		uuid.NewString(),
		connector.TypeDestination,
		connector.Config{
			Plugin:     "builtin:plugin",
			PipelineID: uuid.NewString(),
		},
		connector.ProvisionTypeDLQ,
	)
	is.NoErr(err)

	// DLQ connectors are not persisted
	_, err = service.Get(ctx, got.ID())
	is.True(cerrors.Is(err, connector.ErrInstanceNotFound))
}

func TestService_CreateError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	builder := connector.NewDefaultBuilder(logger, nil, plugin.NewService(
		logger,
		builtin.NewRegistry(logger),
		standalone.NewRegistry(logger, ""),
	))

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
				connector.ProvisionTypeAPI,
			)
			is.True(err != nil)
			is.Equal(got, nil)
		})
	}
}

func TestService_GetSuccess(t *testing.T) {
	is := is.New(t)
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
		connector.ProvisionTypeAPI,
	)
	is.NoErr(err)

	got, err := service.Get(ctx, want.ID())
	is.NoErr(err)
	is.Equal(want, got)
}

func TestService_GetInstanceNotFound(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := connector.NewService(logger, db, mock.Builder{})

	// get connector that does not exist
	got, err := service.Get(ctx, uuid.NewString())
	is.True(err != nil)
	is.True(cerrors.Is(err, connector.ErrInstanceNotFound))
	is.Equal(got, nil)
}

func TestService_DeleteSuccess(t *testing.T) {
	is := is.New(t)
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
		connector.ProvisionTypeAPI,
	)
	is.NoErr(err)

	err = service.Delete(ctx, conn.ID())
	is.NoErr(err)

	got, err := service.Get(ctx, conn.ID())
	is.True(err != nil)
	is.Equal(got, nil)
}

func TestService_DeleteInstanceNotFound(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := connector.NewService(logger, db, mock.Builder{})
	// delete connector that does not exist
	err := service.Delete(ctx, uuid.NewString())
	is.True(err != nil)
	is.True(cerrors.Is(err, connector.ErrInstanceNotFound))
}

func TestService_DeleteConnectorIsRunning(t *testing.T) {
	is := is.New(t)
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
		connector.ProvisionTypeAPI,
	)
	is.NoErr(err)

	// delete connector that is running
	err = service.Delete(ctx, conn.ID())
	is.True(err != nil)
}

func TestService_List(t *testing.T) {
	is := is.New(t)
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
			connector.ProvisionTypeAPI,
		)
		is.NoErr(err)
		want[conn.ID()] = conn
	}

	got := service.List(ctx)
	is.Equal(want, got)
}

func TestService_UpdateSuccess(t *testing.T) {
	is := is.New(t)
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

	beforeUpdate := time.Now()

	connBuilder := mock.Builder{
		Ctrl: ctrl,
		SetupSource: func(source *mock.Source) {
			source.EXPECT().Validate(ctx, want.Settings).Return(nil)
			source.EXPECT().SetConfig(want)
			source.
				EXPECT().
				SetUpdatedAt(gomock.AssignableToTypeOf(time.Time{})).
				Do(func(got time.Time) {
					is.Equal(got.After(beforeUpdate), true)
				})
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
		connector.ProvisionTypeAPI,
	)
	is.NoErr(err)

	_, err = service.Update(ctx, conn.ID(), want)
	is.NoErr(err)
}

func TestService_UpdateInstanceNotFound(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := connector.NewService(logger, db, mock.Builder{})
	// update connector that does not exist
	got, err := service.Update(ctx, uuid.NewString(), connector.Config{})
	is.True(err != nil)
	is.True(cerrors.Is(err, connector.ErrInstanceNotFound))
	is.Equal(got, nil)
}

func TestService_UpdateInvalidConfig(t *testing.T) {
	is := is.New(t)
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
		connector.ProvisionTypeAPI,
	)
	is.NoErr(err)

	got, err := service.Update(ctx, conn.ID(), config)
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr))
	is.Equal(got, nil)
}
