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

package connector_test

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/connector/mock"
	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
)

func TestConfigStore_SetGet(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	connBuilder := mock.Builder{Ctrl: ctrl}

	s := connector.NewStore(db, logger, connBuilder)

	want := connBuilder.NewSourceMock(uuid.NewString(), connector.Config{})

	err := s.Set(ctx, want.ID(), want)
	assert.Ok(t, err)

	got, err := s.Get(ctx, want.ID())
	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func TestConfigStore_GetAll(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	connBuilder := mock.Builder{Ctrl: ctrl}

	s := connector.NewStore(db, logger, connBuilder)

	want := make(map[string]connector.Connector)
	for i := 0; i < 10; i++ {
		conn := connBuilder.NewSourceMock(uuid.NewString(), connector.Config{})
		err := s.Set(ctx, conn.ID(), conn)
		assert.Ok(t, err)
		want[conn.ID()] = conn
	}

	got, err := s.GetAll(ctx)
	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func TestConfigStore_Delete(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	connBuilder := mock.Builder{Ctrl: ctrl}

	s := connector.NewStore(db, logger, connBuilder)

	want := connBuilder.NewDestinationMock(uuid.NewString(), connector.Config{})

	err := s.Set(ctx, want.ID(), want)
	assert.Ok(t, err)

	err = s.Delete(ctx, want.ID())
	assert.Ok(t, err)

	got, err := s.Get(ctx, want.ID())
	assert.Error(t, err)
	assert.True(t, cerrors.Is(err, database.ErrKeyNotExist), "expected error for non-existing key")
	assert.Nil(t, got)
}

func TestConfigStore_SetLocker(t *testing.T) {
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	connBuilder := mock.Builder{Ctrl: ctrl}

	s := connector.NewStore(db, logger, connBuilder)

	source := connBuilder.NewSourceMock(uuid.NewString(), connector.Config{})

	var lockCalled bool
	var unlockCalled bool

	want := &lockerConnector{
		Source: source,
		onLock: func() {
			if unlockCalled {
				t.Fatal("Unlock was called before Lock")
			}
			lockCalled = true
		},
		onUnlock: func() {
			if !lockCalled {
				t.Fatal("Unlock was called without Lock")
			}
			unlockCalled = true
		},
	}

	err := s.Set(ctx, want.ID(), want)
	assert.Ok(t, err)

	assert.True(t, lockCalled, "expected Lock to be called")
	assert.True(t, unlockCalled, "expected Unlock to be called")
}

type lockerConnector struct {
	*mock.Source

	onLock   func()
	onUnlock func()
}

func (lc *lockerConnector) Lock() {
	lc.onLock()
}

func (lc *lockerConnector) Unlock() {
	lc.onUnlock()
}
