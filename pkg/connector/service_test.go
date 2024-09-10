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
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/database/mock"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestService_Init_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db, nil)
	_, err := service.Create(
		ctx,
		uuid.NewString(),
		TypeSource,
		"test-plugin",
		uuid.NewString(),
		Config{
			Name:     "test-connector",
			Settings: map[string]string{"path": "."},
		},
		ProvisionTypeAPI,
	)
	is.NoErr(err)

	want := service.List(ctx)

	// create a new connector service and initialize it
	service = NewService(logger, db, nil)
	err = service.Init(ctx)
	is.NoErr(err)

	got := service.List(ctx)
	is.Equal(len(got), 1)
	is.Equal(want, got)
}

func TestService_Init_DeleteDLQConnector(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db, nil)
	dlqConn := &Instance{
		ID:            uuid.NewString(),
		Type:          TypeDestination,
		ProvisionedBy: ProvisionTypeDLQ,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	err := service.store.Set(ctx, dlqConn.ID, dlqConn)
	is.NoErr(err)

	gotAll, err := service.store.GetAll(ctx)
	is.NoErr(err)
	is.Equal(len(gotAll), 1)

	// initialize a new service and ensure it deleted the DLQ connector
	service = NewService(logger, db, nil)
	err = service.Init(ctx)
	is.NoErr(err)

	got := service.List(ctx)
	is.Equal(len(got), 0)

	gotAll, err = service.store.GetAll(ctx)
	is.NoErr(err)
	is.Equal(len(gotAll), 0)
}

func TestService_Check(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := mock.NewDB(gomock.NewController(t))

	testCases := []struct {
		name    string
		wantErr error
	}{
		{
			name:    "db ok",
			wantErr: nil,
		},
		{
			name:    "db not ok",
			wantErr: cerrors.New("db is under the weather today"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			db.EXPECT().GetKeys(gomock.Any(), gomock.Any()).Return(nil, nil)
			db.EXPECT().Ping(gomock.Any()).Return(tc.wantErr)
			service := NewService(logger, db, nil)

			gotErr := service.Check(ctx)
			is.Equal(tc.wantErr, gotErr)
		})
	}
}

func TestService_CreateSuccess(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

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
			Plugin:     "test-plugin",
			PipelineID: uuid.NewString(),
			Config: Config{
				Name: "my-destination",
			},
			ProvisionedBy: ProvisionTypeAPI,
			persister:     persister,
		},
	}, {
		name: "create generator source connector",
		want: &Instance{
			ID:         uuid.NewString(),
			Type:       TypeSource,
			Plugin:     "test-plugin",
			PipelineID: uuid.NewString(),
			Config: Config{
				Name: "my-source",
			},
			ProvisionedBy: ProvisionTypeConfig,
			persister:     persister,
		},
	}}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := service.Create(
				ctx,
				tt.want.ID,
				tt.want.Type,
				tt.want.Plugin,
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

	service := NewService(logger, db, nil)
	got, err := service.Create(
		ctx,
		uuid.NewString(),
		TypeDestination,
		"test-plugin",
		uuid.NewString(),
		Config{
			Name:     "test-connector",
			Settings: map[string]string{"foo": "bar"},
		},
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

	service := NewService(logger, db, nil)

	testCases := []struct {
		name       string
		connType   Type
		plugin     string
		pipelineID string
		data       Config
	}{
		{
			name:       "invalid connector type",
			connType:   0,
			plugin:     "test-plugin",
			pipelineID: uuid.NewString(),
			data: Config{
				Name:     "test-connector",
				Settings: map[string]string{"foo": "bar"},
			},
		}, {
			name:       "empty plugin",
			connType:   TypeSource,
			plugin:     "",
			pipelineID: uuid.NewString(),
			data: Config{
				Name:     "test-connector",
				Settings: map[string]string{"foo": "bar"},
			},
		}, {
			name:       "empty pipeline ID",
			connType:   TypeSource,
			plugin:     "test-plugin",
			pipelineID: "",
			data: Config{
				Name:     "test-connector",
				Settings: map[string]string{"foo": "bar"},
			},
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := service.Create(
				ctx,
				uuid.NewString(),
				tt.connType,
				tt.plugin,
				tt.pipelineID,
				tt.data,
				ProvisionTypeAPI,
			)
			is.True(err != nil)
			is.Equal(got, nil)
		})
	}
}

func TestService_Create_ValidateSuccess(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db, nil)

	testCases := []struct {
		name   string
		connID string
		data   Config
	}{{
		name:   "valid config name",
		connID: strings.Repeat("a", 128),
		data: Config{
			Name:     strings.Repeat("a", 128),
			Settings: map[string]string{"foo": "bar"},
		},
	}, {
		name:   "valid connector ID",
		connID: "Aa0-_.",
		data: Config{
			Name:     "Name#@-/_0%$",
			Settings: map[string]string{"foo": "bar"},
		},
	}}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := service.Create(
				ctx,
				tt.connID,
				TypeSource,
				"test-plugin",
				uuid.NewString(),
				tt.data,
				ProvisionTypeAPI,
			)
			is.True(got != nil)
			is.Equal(err, nil)
		})
	}
}

func TestService_Create_ValidateError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db, nil)

	testCases := []struct {
		name    string
		connID  string
		errType error
		data    Config
	}{{
		name:    "empty config name",
		connID:  uuid.NewString(),
		errType: ErrNameMissing,
		data: Config{
			Name:     "",
			Settings: map[string]string{"foo": "bar"},
		},
	}, {
		name:    "connector name over 256 characters",
		connID:  uuid.NewString(),
		errType: ErrNameOverLimit,
		data: Config{
			Name:     strings.Repeat("a", 257),
			Settings: map[string]string{"foo": "bar"},
		},
	}, {
		name:    "connector ID over 256 characters",
		connID:  strings.Repeat("a", 257),
		errType: ErrIDOverLimit,
		data: Config{
			Name:     "test-connector",
			Settings: map[string]string{"foo": "bar"},
		},
	}, {
		name:    "invalid characters in connector ID",
		connID:  "a%bc",
		errType: ErrInvalidCharacters,
		data: Config{
			Name:     "test-connector",
			Settings: map[string]string{"foo": "bar"},
		},
	}, {
		name:    "empty connector ID",
		connID:  "",
		errType: ErrIDMissing,
		data: Config{
			Name:     "test-connector",
			Settings: map[string]string{"foo": "bar"},
		},
	}}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := service.Create(
				ctx,
				tt.connID,
				TypeSource,
				"test-plugin",
				uuid.NewString(),
				tt.data,
				ProvisionTypeAPI,
			)
			is.True(cerrors.Is(err, tt.errType))
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
	ctrl := gomock.NewController(t)
	db := &inmemory.DB{}

	service := NewService(logger, db, nil)
	conn, err := service.Create(
		ctx,
		uuid.NewString(),
		TypeSource,
		"test-plugin",
		uuid.NewString(),
		Config{
			Name:     "test-connector",
			Settings: map[string]string{"foo": "bar"},
		},
		ProvisionTypeAPI,
	)
	is.NoErr(err)

	pluginDispenser := pmock.NewDispenser(ctrl)
	pluginFetcher := fakePluginFetcher{conn.Plugin: pluginDispenser}

	err = service.Delete(ctx, conn.ID, pluginFetcher)
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
	err := service.Delete(ctx, uuid.NewString(), nil)
	is.True(err != nil)
	is.True(cerrors.Is(err, ErrInstanceNotFound))
}

func TestService_List(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db, nil)
	want := make(map[string]*Instance)
	for i := 0; i < 10; i++ {
		conn, err := service.Create(
			ctx,
			uuid.NewString(),
			TypeSource,
			"test-plugin",
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

	service := NewService(logger, db, nil)

	want := Config{
		Name:     "changed-name",
		Settings: map[string]string{"foo": "bar"},
	}

	conn, err := service.Create(
		ctx,
		uuid.NewString(),
		TypeSource,
		"test-plugin",
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

func TestService_SetState(t *testing.T) {
	type testCase struct {
		name     string
		connType Type
		state    any
		wantErr  error
	}
	testCases := []testCase{
		{
			name:     "nil state",
			connType: TypeSource,
			state:    nil,
			wantErr:  nil,
		},
		{
			name:     "correct state (source)",
			connType: TypeSource,
			state:    SourceState{Position: opencdc.Position("test position")},
			wantErr:  nil,
		},
		{
			name:     "correct state (destination)",
			connType: TypeDestination,
			state: DestinationState{
				Positions: map[string]opencdc.Position{
					"test-connector": opencdc.Position("test-position"),
				},
			},
			wantErr: nil,
		},
		{
			name:     "wrong state",
			connType: TypeSource,
			state: DestinationState{
				Positions: map[string]opencdc.Position{
					"test-connector": opencdc.Position("test-position"),
				},
			},
			wantErr: ErrInvalidConnectorStateType,
		},
		{
			name:     "completely wrong state",
			connType: TypeSource,
			state:    testCase{name: "completely wrong state"},
			wantErr:  ErrInvalidConnectorStateType,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			logger := log.Nop()
			db := &inmemory.DB{}

			service := NewService(logger, db, nil)
			conn, err := service.Create(
				ctx,
				uuid.NewString(),
				tc.connType,
				"test-plugin",
				uuid.NewString(),
				Config{
					Name:     "test-connector",
					Settings: map[string]string{"foo": "bar"},
				},
				ProvisionTypeAPI,
			)
			is.NoErr(err)

			gotConn, err := service.SetState(
				ctx,
				conn.ID,
				tc.state,
			)
			if tc.wantErr != nil {
				is.True(cerrors.Is(err, tc.wantErr))
				is.True(gotConn == nil)
			} else {
				is.NoErr(err)
				is.Equal(conn, gotConn)
			}
		})
	}
}
