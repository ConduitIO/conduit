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

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/google/uuid"
	"github.com/matryer/is"
)

func TestStore_PrepareSet(t *testing.T) {
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

	// prepare only prepares the connector for storage, but doesn't store it yet
	set, err := s.PrepareSet(want.ID, want)
	is.NoErr(err)

	// at this point the store should still be empty
	got, err := s.Get(ctx, want.ID)
	is.True(cerrors.Is(err, database.ErrKeyNotExist)) // expected error for non-existing key
	is.True(got == nil)

	// now we actually store the connector
	err = set(ctx)
	is.NoErr(err)

	// get should return the connector now
	got, err = s.Get(ctx, want.ID)
	is.NoErr(err)
	is.Equal(want, got)
}

func TestStore_SetGet(t *testing.T) {
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

func TestStore_GetAll(t *testing.T) {
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
				Positions: map[string]opencdc.Position{
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

func TestStore_Delete(t *testing.T) {
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

func TestStore_MigratePre041(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}

	pre041connectors := map[string]string{
		"file-to-file:example.in":  `{"Type":"Source","Data":{"XID":"file-to-file:example.in","XConfig":{"Name":"file-to-file:example.in","Settings":{"path":"./example.in"},"Plugin":"builtin:file","PipelineID":"file-to-file","ProcessorIDs":null},"XState":{"Position":null},"XProvisionedBy":1,"XCreatedAt":"2022-12-21T16:53:52.159532+01:00","XUpdatedAt":"2022-12-21T16:53:52.159532+01:00"}}`,
		"file-to-file:example.out": `{"Type":"Destination","Data":{"XID":"file-to-file:example.out","XConfig":{"Name":"file-to-file:example.out","Settings":{"path":"./example.out"},"Plugin":"builtin:file","PipelineID":"file-to-file","ProcessorIDs":null},"XState":{"Positions":null},"XProvisionedBy":1,"XCreatedAt":"2022-12-21T16:53:52.159532+01:00","XUpdatedAt":"2022-12-21T16:53:52.159532+01:00"}}`,
	}

	timestamp, err := time.Parse(time.RFC3339, "2022-12-21T16:53:52.159532+01:00")
	is.NoErr(err)
	want := map[string]*Instance{
		"file-to-file:example.in": {
			ID:   "file-to-file:example.in",
			Type: TypeSource,
			Config: Config{
				Name: "file-to-file:example.in",
				Settings: map[string]string{
					"path": "./example.in",
				},
			},
			PipelineID:    "file-to-file",
			Plugin:        "builtin:file",
			ProcessorIDs:  nil,
			State:         SourceState{Position: nil},
			ProvisionedBy: ProvisionTypeConfig,
			CreatedAt:     timestamp,
			UpdatedAt:     timestamp,
		},
		"file-to-file:example.out": {
			ID:   "file-to-file:example.out",
			Type: TypeDestination,
			Config: Config{
				Name: "file-to-file:example.out",
				Settings: map[string]string{
					"path": "./example.out",
				},
			},
			PipelineID:    "file-to-file",
			Plugin:        "builtin:file",
			ProcessorIDs:  nil,
			State:         DestinationState{Positions: nil},
			ProvisionedBy: ProvisionTypeConfig,
			CreatedAt:     timestamp,
			UpdatedAt:     timestamp,
		},
	}

	for k, v := range pre041connectors {
		err := db.Set(ctx, "connector:connector:"+k, []byte(v))
		is.NoErr(err)
	}

	store := NewStore(db, logger)
	got, err := store.GetAll(ctx)
	is.NoErr(err)
	is.Equal(len(got), 2)
	is.Equal(want, got)
}
