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

package processor_test

import (
	"context"
	"github.com/conduitio/conduit/pkg/foundation/assert"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	dbmock "github.com/conduitio/conduit/pkg/foundation/database/mock"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/processor/mock"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
	"github.com/matryer/is"
)

func TestService_Init_Success(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)

	procType := "processor-type"
	p := mock.NewProcessor(ctrl)

	registry := newTestBuilderRegistry(t, map[string]processor.Interface{procType: p})
	service := processor.NewService(log.Nop(), db, registry)

	// create a processor instance
	_, err := service.Create(ctx, uuid.NewString(), procType, processor.Parent{}, processor.Config{}, processor.ProvisionTypeAPI)
	assert.Ok(t, err)

	want := service.List(ctx)

	// create a new processor service and initialize it
	service = processor.NewService(log.Nop(), db, registry)
	err = service.Init(ctx)
	assert.Ok(t, err)

	got := service.List(ctx)

	for k := range got {
		got[k].UpdatedAt = want[k].UpdatedAt
		got[k].CreatedAt = want[k].CreatedAt
	}
	assert.Equal(t, want, got)
	assert.Equal(t, len(got), 1)
}

func TestService_Check(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := dbmock.NewDB(gomock.NewController(t))

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
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			db.EXPECT().Ping(gomock.Any()).Return(tc.wantErr)
			service := processor.NewService(logger, db, processor.NewBuilderRegistry())

			gotErr := service.Check(ctx)
			is.Equal(tc.wantErr, gotErr)
		})
	}
}

func TestService_Create_Success(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)

	p := mock.NewProcessor(ctrl)

	want := &processor.Instance{
		ID:   "uuid will be taken from the result",
		Type: "processor-type",
		Parent: processor.Parent{
			ID:   uuid.NewString(),
			Type: processor.ParentTypeConnector,
		},
		Config: processor.Config{
			Settings: map[string]string{
				"processor-config-field-1": "foo",
				"processor-config-field-2": "bar",
			},
		},
		Processor: p,
	}

	registry := newTestBuilderRegistry(t, map[string]processor.Interface{want.Type: p})
	service := processor.NewService(log.Nop(), db, registry)

	got, err := service.Create(ctx, want.ID, want.Type, want.Parent, want.Config, processor.ProvisionTypeAPI)
	assert.Ok(t, err)
	want.ID = got.ID // uuid is random
	want.CreatedAt = got.CreatedAt
	want.UpdatedAt = got.UpdatedAt
	assert.Equal(t, want, got)
}

func TestService_Create_BuilderNotFound(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}

	emptyRegistry := processor.NewBuilderRegistry()
	service := processor.NewService(log.Nop(), db, emptyRegistry)

	got, err := service.Create(
		ctx,
		uuid.NewString(),
		"non-existent processor",
		processor.Parent{},
		processor.Config{},
		processor.ProvisionTypeAPI,
	)

	assert.Error(t, err)
	assert.Nil(t, got)
}

func TestService_Create_BuilderFail(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}

	procType := "processor-type"
	wantErr := cerrors.New("builder failed")

	registry := processor.NewBuilderRegistry()
	err := registry.Register(
		procType,
		func(got processor.Config) (processor.Interface, error) {
			return nil, wantErr
		},
	)
	assert.Ok(t, err)

	service := processor.NewService(log.Nop(), db, registry)

	got, err := service.Create(
		ctx,
		uuid.NewString(),
		procType,
		processor.Parent{},
		processor.Config{},
		processor.ProvisionTypeAPI,
	)
	assert.True(t, cerrors.Is(err, wantErr), "expected builder error")
	assert.Nil(t, got)
}

func TestService_Delete_Success(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)

	procType := "processor-type"
	p := mock.NewProcessor(ctrl)

	registry := newTestBuilderRegistry(t, map[string]processor.Interface{procType: p})
	service := processor.NewService(log.Nop(), db, registry)

	// create a processor instance
	i, err := service.Create(ctx, uuid.NewString(), procType, processor.Parent{}, processor.Config{}, processor.ProvisionTypeAPI)
	assert.Ok(t, err)

	err = service.Delete(ctx, i.ID)
	assert.Ok(t, err)

	got, err := service.Get(ctx, i.ID)
	assert.True(t, cerrors.Is(err, processor.ErrInstanceNotFound), "expected instance not found error")
	assert.Nil(t, got)
}

func TestService_Delete_Fail(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	service := processor.NewService(log.Nop(), db, processor.NewBuilderRegistry())

	err := service.Delete(ctx, "non-existent processor")
	assert.True(t, cerrors.Is(err, processor.ErrInstanceNotFound), "expected instance not found error")
}

func TestService_Get_Success(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)

	procType := "processor-type"
	p := mock.NewProcessor(ctrl)

	registry := newTestBuilderRegistry(t, map[string]processor.Interface{procType: p})
	service := processor.NewService(log.Nop(), db, registry)

	// create a processor instance
	want, err := service.Create(ctx, uuid.NewString(), procType, processor.Parent{}, processor.Config{}, processor.ProvisionTypeAPI)
	assert.Ok(t, err)

	got, err := service.Get(ctx, want.ID)
	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func TestService_Get_Fail(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	service := processor.NewService(log.Nop(), db, processor.NewBuilderRegistry())

	got, err := service.Get(ctx, "non-existent processor")
	assert.True(t, cerrors.Is(err, processor.ErrInstanceNotFound), "expected instance not found error")
	assert.Nil(t, got)
}

func TestService_List_Empty(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	service := processor.NewService(log.Nop(), db, processor.NewBuilderRegistry())

	instances := service.List(ctx)
	assert.NotNil(t, instances)
	assert.True(t, len(instances) == 0, "expected an empty map")
}

func TestService_List_Some(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)

	procType := "processor-type"
	p := mock.NewProcessor(ctrl)

	registry := newTestBuilderRegistry(t, map[string]processor.Interface{procType: p})
	service := processor.NewService(log.Nop(), db, registry)

	// create a couple of processor instances
	i1, err := service.Create(ctx, uuid.NewString(), procType, processor.Parent{}, processor.Config{}, processor.ProvisionTypeAPI)
	assert.Ok(t, err)
	i2, err := service.Create(ctx, uuid.NewString(), procType, processor.Parent{}, processor.Config{}, processor.ProvisionTypeAPI)
	assert.Ok(t, err)
	i3, err := service.Create(ctx, uuid.NewString(), procType, processor.Parent{}, processor.Config{}, processor.ProvisionTypeAPI)
	assert.Ok(t, err)

	instances := service.List(ctx)
	assert.Equal(t, map[string]*processor.Instance{i1.ID: i1, i2.ID: i2, i3.ID: i3}, instances)
}

func TestService_Update_Success(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)

	procType := "processor-type"
	p := mock.NewProcessor(ctrl)

	registry := newTestBuilderRegistry(t, map[string]processor.Interface{procType: p})
	service := processor.NewService(log.Nop(), db, registry)

	// create a processor instance
	want, err := service.Create(ctx, uuid.NewString(), procType, processor.Parent{}, processor.Config{}, processor.ProvisionTypeAPI)
	assert.Ok(t, err)

	newConfig := processor.Config{
		Settings: map[string]string{
			"processor-config-field-1": "foo",
			"processor-config-field-2": "bar",
		},
	}

	got, err := service.Update(ctx, want.ID, newConfig)
	assert.Ok(t, err)
	assert.Equal(t, want, got)             // same instance is returned
	assert.Equal(t, newConfig, got.Config) // config was updated

	got, err = service.Get(ctx, want.ID)
	assert.Ok(t, err)
	assert.Equal(t, newConfig, got.Config)
}

func TestService_Update_Fail(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	service := processor.NewService(log.Nop(), db, processor.NewBuilderRegistry())

	got, err := service.Update(ctx, "non-existent processor", processor.Config{})
	assert.True(t, cerrors.Is(err, processor.ErrInstanceNotFound), "expected instance not found error")
	assert.Nil(t, got)
}

// newTestBuilderRegistry creates a registry with builders for the supplied
// processors map keyed by processor type. If a value in the map is nil then a
// builder will be registered that returns an error.
func newTestBuilderRegistry(t *testing.T, processors map[string]processor.Interface) *processor.BuilderRegistry {
	registry := processor.NewBuilderRegistry()
	for procType, p := range processors {
		err := registry.Register(
			procType,
			func(got processor.Config) (processor.Interface, error) {
				if p != nil {
					return p, nil
				}
				return nil, cerrors.New("builder error")
			},
		)
		assert.Ok(t, err)
	}
	return registry
}
