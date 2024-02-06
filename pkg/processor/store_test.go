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
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/google/uuid"
	"github.com/matryer/is"
)

func TestConfigStore_SetGet(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	db := &inmemory.DB{}

	want := &processor.Instance{
		ID:     uuid.NewString(),
		Plugin: "test-processor",
		Parent: processor.Parent{
			ID:   uuid.NewString(),
			Type: processor.ParentTypePipeline,
		},
		Config: processor.Config{
			Settings: map[string]string{"foo": "bar"},
		},
	}

	s := processor.NewStore(db)

	err := s.Set(ctx, want.ID, want)
	is.NoErr(err)

	got, err := s.Get(ctx, want.ID)
	is.NoErr(err)
	is.Equal(want, got)
}

func TestConfigStore_GetAll(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}

	s := processor.NewStore(db)

	want := make(map[string]*processor.Instance)
	for i := 0; i < 10; i++ {
		instance := &processor.Instance{
			ID:     uuid.NewString(),
			Plugin: "test-processor",
			Parent: processor.Parent{
				ID:   uuid.NewString(),
				Type: processor.ParentTypePipeline,
			},
			Config: processor.Config{
				Settings: map[string]string{"foo": "bar"},
			},
		}
		if i%2 == 0 {
			// switch up parent types a bit
			instance.Parent.Type = processor.ParentTypeConnector
		}

		err := s.Set(ctx, instance.ID, instance)
		is.NoErr(err)
		want[instance.ID] = instance
	}

	got, err := s.GetAll(ctx)
	is.NoErr(err)
	is.Equal(want, got)
}

func TestConfigStore_Delete(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	db := &inmemory.DB{}

	want := &processor.Instance{
		ID:     uuid.NewString(),
		Plugin: "test-processor",
	}

	s := processor.NewStore(db)

	err := s.Set(ctx, want.ID, want)
	is.NoErr(err)

	err = s.Delete(ctx, want.ID)
	is.NoErr(err)

	got, err := s.Get(ctx, want.ID)
	is.True(err != nil)
	is.True(cerrors.Is(err, database.ErrKeyNotExist)) // expected error for non-existing key
	is.True(got == nil)
}
