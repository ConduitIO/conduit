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
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/google/uuid"
	"github.com/matryer/is"
)

func TestConfigStore_SetGet(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	s := NewStore(db, logger)

	want := &Instance{
		ID:   uuid.NewString(),
		Type: TypeSource,
		State: SourceState{
			Position: []byte(uuid.NewString()),
		},
		CreatedAt: time.Now().UTC(),
	}

	err := s.Set(ctx, want.ID, want)
	is.NoErr(err)

	got, err := s.Get(ctx, want.ID)
	is.NoErr(err)
	is.Equal(want, got)
}

func TestConfigStore_GetAll(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	s := NewStore(db, logger)

	want := make(map[string]*Instance)
	for i := 0; i < 10; i++ {
		conn := &Instance{ID: uuid.NewString()}
		switch i % 2 {
		case 0:
			conn.Type = TypeSource
			conn.State = SourceState{
				Position: []byte(uuid.NewString()),
			}
		case 1:
			conn.Type = TypeDestination
			conn.State = DestinationState{
				Positions: map[string]record.Position{
					uuid.NewString(): []byte(uuid.NewString()),
				},
			}
		}
		err := s.Set(ctx, conn.ID, conn)
		is.NoErr(err)
		want[conn.ID] = conn
	}

	got, err := s.GetAll(ctx)
	is.NoErr(err)
	is.Equal(want, got)
}

func TestConfigStore_Delete(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	s := NewStore(db, logger)

	want := &Instance{ID: uuid.NewString(), Type: TypeSource}

	err := s.Set(ctx, want.ID, want)
	is.NoErr(err)

	err = s.Delete(ctx, want.ID)
	is.NoErr(err)

	got, err := s.Get(ctx, want.ID)
	is.True(err != nil)
	is.True(cerrors.Is(err, database.ErrKeyNotExist)) // expected error for non-existing key
	is.True(got == nil)
}
