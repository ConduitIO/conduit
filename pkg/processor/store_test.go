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

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/processor/mock"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
)

func TestConfigStore_SetGet(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	processorName := "test-processor"

	registry := processor.NewBuilderRegistry()
	registry.MustRegister(processorName, func(_ processor.Config) (processor.Processor, error) {
		p := mock.NewProcessor(ctrl)
		return p, nil
	})

	want := &processor.Instance{
		ID:   uuid.NewString(),
		Name: "test-processor",
		Parent: processor.Parent{
			ID:   uuid.NewString(),
			Type: processor.ParentTypePipeline,
		},
		Config: processor.Config{
			Settings: map[string]string{"foo": "bar"},
		},
	}

	var err error
	want.Processor, err = registry.MustGet(processorName)(want.Config)
	assert.Ok(t, err)

	s := processor.NewStore(db, registry)

	err = s.Set(ctx, want.ID, want)
	assert.Ok(t, err)
	assert.NotNil(t, want.Processor) // make sure processor is left untouched

	got, err := s.Get(ctx, want.ID)
	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func TestConfigStore_GetAll(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	processorName := "test-processor"

	registry := processor.NewBuilderRegistry()
	registry.MustRegister(processorName, func(_ processor.Config) (processor.Processor, error) {
		p := mock.NewProcessor(ctrl)
		return p, nil
	})

	s := processor.NewStore(db, registry)

	want := make(map[string]*processor.Instance)
	for i := 0; i < 10; i++ {
		instance := &processor.Instance{
			ID:   uuid.NewString(),
			Name: "test-processor",
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
		var err error
		instance.Processor, err = registry.MustGet(processorName)(instance.Config)
		assert.Ok(t, err)

		err = s.Set(ctx, instance.ID, instance)
		assert.Ok(t, err)
		want[instance.ID] = instance
	}

	got, err := s.GetAll(ctx)
	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func TestConfigStore_Delete(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}
	registry := processor.NewBuilderRegistry()

	want := &processor.Instance{
		ID:   uuid.NewString(),
		Name: "test-processor",
	}

	s := processor.NewStore(db, registry)

	err := s.Set(ctx, want.ID, want)
	assert.Ok(t, err)

	err = s.Delete(ctx, want.ID)
	assert.Ok(t, err)

	got, err := s.Get(ctx, want.ID)
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, database.ErrKeyNotExist), "expected error for non-existing key")
	assert.Nil(t, got)
}
