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

package pipeline

import (
	"context"
	"fmt"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/google/uuid"
	"github.com/matryer/is"
)

func TestService_Init_Simple(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db)
	_, err := service.Create(ctx, uuid.NewString(), Config{Name: "test-pipeline"}, ProvisionTypeAPI)
	is.NoErr(err)

	want := service.List(ctx)

	// create a new pipeline service and initialize it
	service = NewService(logger, db)
	err = service.Init(ctx)
	is.NoErr(err)

	got := service.List(ctx)

	// update expected times
	for k := range got {
		got[k].CreatedAt = want[k].CreatedAt
		got[k].UpdatedAt = want[k].UpdatedAt
	}
	is.Equal(want, got)
	is.Equal(len(got), 1)
}

func TestService_CreateSuccess(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db)

	testCases := []struct {
		id     string
		name   string
		config Config
		want   *Instance
	}{{
		id:   uuid.NewString(),
		name: "full config",
		config: Config{
			Name:        "test-pipeline1",
			Description: "pipeline description",
		},
		want: &Instance{
			Config: Config{
				Name:        "test-pipeline1",
				Description: "pipeline description",
			},
			DLQ:    service.defaultDLQ(),
			Status: StatusUserStopped,
		},
	}, {
		id:   uuid.NewString(),
		name: "empty description",
		config: Config{
			Name:        "test-pipeline2",
			Description: "",
		},
		want: &Instance{
			Config: Config{
				Name:        "test-pipeline2",
				Description: "",
			},
			DLQ:    service.defaultDLQ(),
			Status: StatusUserStopped,
		},
	}}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			got, err := service.Create(ctx, tt.id, tt.config, ProvisionTypeAPI)
			is.NoErr(err)

			tt.want.ID = got.ID
			tt.want.CreatedAt = got.CreatedAt
			tt.want.UpdatedAt = got.UpdatedAt
			is.Equal(tt.want, got)
		})
	}
}

func TestService_Create_PipelineNameExists(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db)

	conf := Config{Name: "test-pipeline"}
	got, err := service.Create(ctx, uuid.NewString(), conf, ProvisionTypeAPI)
	is.NoErr(err)
	is.True(got != nil)
	got, err = service.Create(ctx, uuid.NewString(), conf, ProvisionTypeAPI)
	is.Equal(got, nil)
	is.True(err != nil)
}

func TestService_CreateEmptyName(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db)
	got, err := service.Create(ctx, uuid.NewString(), Config{Name: ""}, ProvisionTypeAPI)
	is.True(err != nil)
	is.Equal(got, nil)
}

func TestService_GetSuccess(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db)
	want, err := service.Create(ctx, uuid.NewString(), Config{Name: "test-pipeline"}, ProvisionTypeAPI)
	is.NoErr(err)

	got, err := service.Get(ctx, want.ID)
	is.NoErr(err)
	is.Equal(want, got)
}

func TestService_GetInstanceNotFound(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db)

	// get pipeline instance that does not exist
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

	service := NewService(logger, db)
	instance, err := service.Create(ctx, uuid.NewString(), Config{Name: "test-pipeline"}, ProvisionTypeAPI)
	is.NoErr(err)

	err = service.Delete(ctx, instance.ID)
	is.NoErr(err)

	got, err := service.Get(ctx, instance.ID)
	is.True(err != nil)
	is.Equal(got, nil)
}

func TestService_List(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db)

	want := make(map[string]*Instance)
	for i := 0; i < 10; i++ {
		instance, err := service.Create(ctx, uuid.NewString(), Config{Name: fmt.Sprintf("test-pipeline-%d", i)}, ProvisionTypeAPI)
		is.NoErr(err)
		want[instance.ID] = instance
	}

	got := service.List(ctx)
	is.Equal(want, got)
}

func TestService_UpdateSuccess(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db)
	instance, err := service.Create(ctx, uuid.NewString(), Config{Name: "test-pipeline"}, ProvisionTypeAPI)
	is.NoErr(err)

	want := Config{
		Name:        "new-name",
		Description: "new description",
	}

	got, err := service.Update(ctx, instance.ID, want)
	is.NoErr(err)
	is.Equal(want, got.Config)
}

func TestService_Update_PipelineNameExists(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db)
	_, err := service.Create(ctx, uuid.NewString(), Config{Name: "test-pipeline"}, ProvisionTypeAPI)
	is.NoErr(err)
	instance2, err2 := service.Create(ctx, uuid.NewString(), Config{Name: "test-pipeline2"}, ProvisionTypeAPI)
	is.NoErr(err2)

	want := Config{
		Name:        "test-pipeline",
		Description: "new description",
	}

	got, err := service.Update(ctx, instance2.ID, want)
	is.True(err != nil)
	is.Equal(got, nil)
}

func TestService_UpdateInvalidConfig(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	service := NewService(logger, db)
	instance, err := service.Create(ctx, uuid.NewString(), Config{Name: "test-pipeline"}, ProvisionTypeAPI)
	is.NoErr(err)

	config := Config{Name: ""} // empty name is not allowed

	got, err := service.Update(ctx, instance.ID, config)
	is.True(err != nil)
	is.Equal(got, nil)
}
