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
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	dbmock "github.com/conduitio/conduit/pkg/foundation/database/mock"
	"github.com/conduitio/conduit/pkg/foundation/log"
	proc_plugin "github.com/conduitio/conduit/pkg/plugin/processor"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/processor/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestService_Init_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}

	procType := "processor-type"
	p := newMockProcessor(t)

	registry := newTestBuilderRegistry(map[string]processor.Interface{procType: p})
	service := processor.NewService(log.Nop(), db, registry)

	// create a processor instance
	_, err := service.Create(
		ctx,
		uuid.NewString(),
		procType,
		processor.Parent{},
		processor.Config{},
		processor.ProvisionTypeAPI,
		"",
	)
	is.NoErr(err)

	want := service.List(ctx)

	// create a new processor service and initialize it
	service = processor.NewService(log.Nop(), db, registry)
	err = service.Init(ctx)
	is.NoErr(err)

	got := service.List(ctx)

	for k := range got {
		got[k].UpdatedAt = want[k].UpdatedAt
		got[k].CreatedAt = want[k].CreatedAt
	}
	is.Equal(want, got)
	is.Equal(len(got), 1)
}

func newMockProcessor(t *testing.T) *mock.Processor {
	p := mock.NewProcessor(gomock.NewController(t))
	p.EXPECT().Specification().Return(sdk.Specification{
		Name:        "test-processor",
		Summary:     "test-processor",
		Description: "test-processor",
		Version:     "v0.1.2",
		Author:      "Conduit",
		Parameters:  nil,
	}, nil)

	return p
}

func TestService_Check(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := dbmock.NewDB(gomock.NewController(t))

	testCases := []struct {
		name    string
		wantErr error
	}{
		{name: "db ok", wantErr: nil},
		{name: "db not ok", wantErr: cerrors.New("db error")},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			db.EXPECT().Ping(gomock.Any()).Return(tc.wantErr)
			service := processor.NewService(logger, db, &proc_plugin.Registry{})

			gotErr := service.Check(ctx)
			is.Equal(tc.wantErr, gotErr)
		})
	}
}

func TestService_Create_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	p := newMockProcessor(t)

	want := &processor.Instance{
		ID:     "uuid will be taken from the result",
		Plugin: "processor-type",
		Parent: processor.Parent{
			ID:   uuid.NewString(),
			Type: processor.ParentTypeConnector,
		},
		Config: processor.Config{
			Settings: map[string]string{
				"processor-config-field-1": "foo",
				"processor-config-field-2": "bar",
			},
			Workers: 1,
		},
		Processor: p,
	}

	registry := newTestBuilderRegistry(map[string]processor.Interface{want.Plugin: p})
	service := processor.NewService(log.Nop(), db, registry)

	got, err := service.Create(ctx, want.ID, want.Plugin, want.Parent, want.Config, processor.ProvisionTypeAPI, "")
	is.NoErr(err)
	want.ID = got.ID // uuid is random
	want.CreatedAt = got.CreatedAt
	want.UpdatedAt = got.UpdatedAt
	is.Equal(want, got)
}

func TestService_Create_BuilderNotFound(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}

	service := processor.NewService(log.Nop(), db, &proc_plugin.Registry{})

	got, err := service.Create(
		ctx,
		uuid.NewString(),
		"non-existent processor",
		processor.Parent{},
		processor.Config{},
		processor.ProvisionTypeAPI,
		"{{true}}",
	)

	is.True(err != nil)
	is.Equal(got, nil)
}

func TestService_Create_BuilderFail(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}

	procType := "foo:processor-type"
	wantErr := cerrors.New("builder failed")
	service := processor.NewService(log.Nop(), db, &proc_plugin.Registry{})

	got, err := service.Create(
		ctx,
		uuid.NewString(),
		procType,
		processor.Parent{},
		processor.Config{},
		processor.ProvisionTypeAPI,
		"{{true}}",
	)
	is.True(cerrors.Is(err, wantErr)) // expected builder error
	is.Equal(got, nil)
}

func TestService_Create_WorkersNegative(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}

	service := processor.NewService(log.Nop(), db, &proc_plugin.Registry{})

	got, err := service.Create(
		ctx,
		uuid.NewString(),
		"processor-type",
		processor.Parent{},
		processor.Config{},
		processor.ProvisionTypeAPI,
		"{{true}}",
	)
	is.True(err != nil) // expected workers error
	is.Equal(got, nil)
}

func TestService_Delete_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}

	procType := "processor-type"
	p := newMockProcessor(t)
	p.EXPECT().Close()

	registry := newTestBuilderRegistry(map[string]processor.Interface{procType: p})
	service := processor.NewService(log.Nop(), db, registry)

	// create a processor instance
	i, err := service.Create(ctx, uuid.NewString(), procType, processor.Parent{}, processor.Config{}, processor.ProvisionTypeAPI, "cond")
	is.NoErr(err)

	err = service.Delete(ctx, i.ID)
	is.NoErr(err)

	got, err := service.Get(ctx, i.ID)
	is.True(cerrors.Is(err, processor.ErrInstanceNotFound)) // expected instance not found error
	is.Equal(got, nil)
}

func TestService_Delete_Fail(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	service := processor.NewService(log.Nop(), db, &proc_plugin.Registry{})

	err := service.Delete(ctx, "non-existent processor")
	is.True(cerrors.Is(err, processor.ErrInstanceNotFound)) // expected instance not found error
}

func TestService_Get_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}

	procType := "processor-type"
	p := newMockProcessor(t)

	registry := newTestBuilderRegistry(map[string]processor.Interface{procType: p})
	service := processor.NewService(log.Nop(), db, registry)

	// create a processor instance
	want, err := service.Create(ctx, uuid.NewString(), procType, processor.Parent{}, processor.Config{}, processor.ProvisionTypeAPI, "cond")
	is.NoErr(err)

	got, err := service.Get(ctx, want.ID)
	is.NoErr(err)
	is.Equal(want, got)
}

func TestService_Get_Fail(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	service := processor.NewService(log.Nop(), db, &proc_plugin.Registry{})

	got, err := service.Get(ctx, "non-existent processor")
	is.True(cerrors.Is(err, processor.ErrInstanceNotFound)) // expected instance not found error
	is.Equal(got, nil)
}

func TestService_List_Empty(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	service := processor.NewService(log.Nop(), db, &proc_plugin.Registry{})

	instances := service.List(ctx)
	is.True(instances != nil)
	is.True(len(instances) == 0) // expected an empty map
}

func TestService_List_Some(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}

	procType := "processor-type"
	p := newMockProcessor(t)

	registry := newTestBuilderRegistry(map[string]processor.Interface{procType: p})
	service := processor.NewService(log.Nop(), db, registry)

	// create a couple of processor instances
	i1, err := service.Create(ctx, uuid.NewString(), procType, processor.Parent{}, processor.Config{}, processor.ProvisionTypeAPI, "")
	is.NoErr(err)
	i2, err := service.Create(ctx, uuid.NewString(), procType, processor.Parent{}, processor.Config{}, processor.ProvisionTypeAPI, "")
	is.NoErr(err)
	i3, err := service.Create(ctx, uuid.NewString(), procType, processor.Parent{}, processor.Config{}, processor.ProvisionTypeAPI, "")
	is.NoErr(err)

	instances := service.List(ctx)
	is.Equal(map[string]*processor.Instance{i1.ID: i1, i2.ID: i2, i3.ID: i3}, instances)
}

func TestService_Update_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}

	procType := "processor-type"
	p := newMockProcessor(t)

	registry := newTestBuilderRegistry(map[string]processor.Interface{procType: p})
	service := processor.NewService(log.Nop(), db, registry)

	// create a processor instance
	want, err := service.Create(ctx, uuid.NewString(), procType, processor.Parent{}, processor.Config{}, processor.ProvisionTypeAPI, "")
	is.NoErr(err)

	newConfig := processor.Config{
		Settings: map[string]string{
			"processor-config-field-1": "foo",
			"processor-config-field-2": "bar",
		},
	}

	got, err := service.Update(ctx, want.ID, newConfig)
	is.NoErr(err)
	is.Equal(want, got)             // same instance is returned
	is.Equal(newConfig, got.Config) // config was updated

	got, err = service.Get(ctx, want.ID)
	is.NoErr(err)
	is.Equal(newConfig, got.Config)
}

func TestService_Update_Fail(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	service := processor.NewService(log.Nop(), db, &proc_plugin.Registry{})

	got, err := service.Update(ctx, "non-existent processor", processor.Config{})
	is.True(cerrors.Is(err, processor.ErrInstanceNotFound)) // expected instance not found error
	is.Equal(got, nil)
}

// newTestBuilderRegistry creates a registry with builders for the supplied
// processors map keyed by processor type. If a value in the map is nil then a
// builder will be registered that returns an error.
func newTestBuilderRegistry(processors map[string]processor.Interface) *proc_plugin.Registry {
	ctors := make(map[string]builtin.ProcessorPluginConstructor, len(processors))
	for name, proc := range processors {
		ctors[name] = func(log.CtxLogger) sdk.Processor {
			return proc
		}
	}

	return &proc_plugin.Registry{
		BuiltinReg: builtin.NewRegistry(log.Nop(), ctors),
	}
}
