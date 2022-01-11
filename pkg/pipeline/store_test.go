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

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/google/uuid"
)

type boringError struct {
}

func (e boringError) Error() string {
	return "a very, very boring error"
}

func TestConfigStore_SetGet(t *testing.T) {
	testCases := []struct {
		name string
		err  error
	}{
		{
			name: "no error",
			err:  nil,
		},
		{
			name: "non-wrapped error",
			err:  cerrors.New("save failed successfully"),
		},
		{
			name: "wrapped error",
			err:  cerrors.Errorf("wrapper: %w", cerrors.New("burrito too spicy")),
		},
		{
			name: "custom error type",
			err:  boringError{},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			testConfigStoreSetGet(t, testCase.err)
		})
	}
}

func testConfigStoreSetGet(t *testing.T, e error) {
	ctx := context.Background()
	db := &inmemory.DB{}

	s := NewStore(db)

	want := &Instance{
		ID: uuid.NewString(),
		Config: Config{
			Name:        "test-pipeline",
			Description: "test pipeline description",
		},
		Status:       StatusSystemStopped,
		ConnectorIDs: []string{uuid.NewString(), uuid.NewString(), uuid.NewString()},
		ProcessorIDs: []string{uuid.NewString(), uuid.NewString()},
		Error:        fmt.Sprintf("%+v", e),
	}

	err := s.Set(ctx, want.ID, want)
	assert.Ok(t, err)

	got, err := s.Get(ctx, want.ID)
	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func TestConfigStore_GetAll(t *testing.T) {
	ctx := context.Background()
	db := &inmemory.DB{}

	s := NewStore(db)

	want := make(map[string]*Instance)
	for i := 0; i < 10; i++ {
		instance := &Instance{
			ID: uuid.NewString(),
			Config: Config{
				Name:        fmt.Sprintf("test-pipeline-%d", i),
				Description: "test pipeline description",
			},
			ConnectorIDs: []string{uuid.NewString(), uuid.NewString(), uuid.NewString()},
			ProcessorIDs: []string{uuid.NewString(), uuid.NewString()},
		}
		err := s.Set(ctx, instance.ID, instance)
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

	s := NewStore(db)

	want := &Instance{ID: uuid.NewString()}

	err := s.Set(ctx, want.ID, want)
	assert.Ok(t, err)

	err = s.Delete(ctx, want.ID)
	assert.Ok(t, err)

	got, err := s.Get(ctx, want.ID)
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, database.ErrKeyNotExist), "expected error for non-existing key")
	assert.Nil(t, got)
}
