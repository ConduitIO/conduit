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

package connector

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/mock"
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

	pluginDispenser := mock.NewDispenser(ctrl)
	pluginDispenser.EXPECT().FullName().Return(plugin.FullName("test")).AnyTimes()

	service := NewService(logger, db, nil)
	_, err := service.Create(
		ctx,
		uuid.NewString(),
		TypeSource,
		pluginDispenser,
		uuid.NewString(),
		Config{
			Name:     "test-connector",
			Settings: map[string]string{"path": "."},
		},
		ProvisionTypeAPI,
	)
	is.NoErr(err)

	want := service.List(ctx)

	registry := mock.NewRegistry(ctrl)
	registry.EXPECT().NewDispenser(gomock.Any(), pluginDispenser.FullName()).Return(pluginDispenser, nil)
	pluginService := plugin.NewService(logger, registry, standalone.NewRegistry(logger, ""))

	// create a new connector service and initialize it
	service = NewService(logger, db, nil)
	err = service.Init(ctx, pluginService)
	is.NoErr(err)

	got := service.List(ctx)
	is.Equal(len(got), 1)
	is.Equal(want, got)
}

func TestService_CreateSuccess(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)

	pluginDispenser := mock.NewDispenser(ctrl)
	pluginDispenser.EXPECT().FullName().Return(plugin.FullName("test")).AnyTimes()

	persister := NewPersister(logger, db, DefaultPersisterDelayThreshold, 1)
	service := NewService(logger, db, persister)

	testCases := []struct {
		name string
		want *Instance
	}{{
		name: "create log destination connector",
		want: &Instance{
			ID:         uuid.NewString(),
			Type:       TypeDestination,
			Plugin:     "test",
			PipelineID: uuid.NewString(),
			Config: Config{
				Name: "my-destination",
			},
			ProvisionedBy:   ProvisionTypeAPI,
			pluginDispenser: pluginDispenser,
			persister:       persister,
		},
	}, {
		name: "create generator source connector",
		want: &Instance{
			ID:         uuid.NewString(),
			Type:       TypeSource,
			Plugin:     "test",
			PipelineID: uuid.NewString(),
			Config: Config{
				Name: "my-source",
			},
			ProvisionedBy:   ProvisionTypeConfig,
			pluginDispenser: pluginDispenser,
			persister:       persister,
		},
	}}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := service.Create(
				ctx,
				tt.want.ID,
				tt.want.Type,
				tt.want.pluginDispenser,
				tt.want.PipelineID,
				tt.want.Config,
				tt.want.ProvisionedBy,
			)
			is.NoErr(err)

			// manually check fields populated by the service
			is.True(!got.CreatedAt.IsZero())
			is.True(!got.UpdatedAt.IsZero())
			is.Equal(got.UpdatedAt, got.CreatedAt)
			is.Equal(got.logger.Component(), "connector."+tt.want.Type.String())
			is.True(got.inspector != nil)

			// copy over fields populated by the service
			tt.want.CreatedAt = got.CreatedAt
			tt.want.UpdatedAt = got.UpdatedAt
			tt.want.logger = got.logger
			tt.want.inspector = got.inspector

			is.Equal(tt.want, got)

			// fetching the connector from the service should return the same
			got2, err := service.Get(ctx, got.ID)
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

	pluginDispenser := mock.NewDispenser(ctrl)
	pluginDispenser.EXPECT().FullName().Return(plugin.FullName("test")).AnyTimes()

	service := NewService(logger, db, nil)
	got, err := service.Create(
		ctx,
		uuid.NewString(),
		TypeDestination,
		pluginDispenser,
		uuid.NewString(),
		Config{},
		ProvisionTypeDLQ,
	)
	is.NoErr(err)

	// DLQ connectors are not persisted
	_, err = service.Get(ctx, got.ID)
	is.True(cerrors.Is(err, ErrInstanceNotFound))
}

func TestService_CreateError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)

	pluginDispenser := mock.NewDispenser(ctrl)
	pluginDispenser.EXPECT().FullName().Return(plugin.FullName("test")).AnyTimes()

	service := NewService(logger, db, nil)

	testCases := []struct {
		name            string
		connType        Type
		pluginDispenser plugin.Dispenser
		pipelineID      string
		data            Config
	}{{
		name:            "invalid connector type",
		connType:        0,
		pluginDispenser: pluginDispenser,
		pipelineID:      uuid.NewString(),
		data: Config{
			Name:     "test-connector",
			Settings: map[string]string{"foo": "bar"},
		},
	}, {
		name:            "empty plugin",
		connType:        TypeSource,
		pluginDispenser: nil,
		pipelineID:      uuid.NewString(),
		data: Config{
			Name:     "test-connector",
			Settings: map[string]string{"foo": "bar"},
		},
	}, {
		name:            "empty pipeline ID",
		connType:        TypeSource,
		pluginDispenser: pluginDispenser,
		pipelineID:      "",
		data: Config{
			Name:     "test-connector",
			Settings: map[string]string{"foo": "bar"},
		},
	}}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := service.Create(
				ctx,
				uuid.NewString(),
				tt.connType,
				tt.pluginDispenser,
				tt.pipelineID,
				tt.data,
				ProvisionTypeAPI,
			)
			is.True(err != nil)
			is.Equal(got, nil)
		})
	}
}

func TestService_GetInstanceNotFound(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db, nil)

	// get connector that does not exist
	got, err := service.Get(ctx, uuid.NewString())
	is.True(err != nil)
	is.True(cerrors.Is(err, ErrInstanceNotFound))
	is.Equal(got, nil)
}

func TestService_DeleteSuccess(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)

	pluginDispenser := mock.NewDispenser(ctrl)
	pluginDispenser.EXPECT().FullName().Return(plugin.FullName("test")).AnyTimes()

	service := NewService(logger, db, nil)
	conn, err := service.Create(
		ctx,
		uuid.NewString(),
		TypeSource,
		pluginDispenser,
		uuid.NewString(),
		Config{
			Name:     "test-connector",
			Settings: map[string]string{"foo": "bar"},
		},
		ProvisionTypeAPI,
	)
	is.NoErr(err)

	err = service.Delete(ctx, conn.ID)
	is.NoErr(err)

	got, err := service.Get(ctx, conn.ID)
	is.True(err != nil)
	is.Equal(got, nil)
}

func TestService_DeleteInstanceNotFound(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db, nil)
	// delete connector that does not exist
	err := service.Delete(ctx, uuid.NewString())
	is.True(err != nil)
	is.True(cerrors.Is(err, ErrInstanceNotFound))
}

func TestService_List(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)

	pluginDispenser := mock.NewDispenser(ctrl)
	pluginDispenser.EXPECT().FullName().Return(plugin.FullName("test")).AnyTimes()

	service := NewService(logger, db, nil)
	want := make(map[string]*Instance)
	for i := 0; i < 10; i++ {
		conn, err := service.Create(
			ctx,
			uuid.NewString(),
			TypeSource,
			pluginDispenser,
			uuid.NewString(),
			Config{
				Name:     fmt.Sprintf("test-connector-%d", i),
				Settings: map[string]string{"foo": "bar"},
			},
			ProvisionTypeAPI,
		)
		is.NoErr(err)
		want[conn.ID] = conn
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

	pluginDispenser := mock.NewDispenser(ctrl)
	pluginDispenser.EXPECT().FullName().Return(plugin.FullName("test")).AnyTimes()

	service := NewService(logger, db, nil)

	want := Config{
		Name:     "changed-name",
		Settings: map[string]string{"foo": "bar"},
	}

	conn, err := service.Create(
		ctx,
		uuid.NewString(),
		TypeSource,
		pluginDispenser,
		uuid.NewString(),
		Config{
			Name:     "test-connector",
			Settings: map[string]string{"foo": "bar"},
		},
		ProvisionTypeAPI,
	)
	is.NoErr(err)

	beforeUpdate := time.Now()
	got, err := service.Update(ctx, conn.ID, want)
	is.NoErr(err)

	is.Equal(got.Config, want)
	is.True(!got.UpdatedAt.Before(beforeUpdate))
}

func TestService_UpdateInstanceNotFound(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db, nil)
	// update connector that does not exist
	got, err := service.Update(ctx, uuid.NewString(), Config{})
	is.True(err != nil)
	is.True(cerrors.Is(err, ErrInstanceNotFound))
	is.Equal(got, nil)
}
