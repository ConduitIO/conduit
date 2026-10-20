// Copyright © 2026 Meroxa, Inc.
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

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/log"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func newRestoreTestConnector(t *testing.T, s *Service) *Instance {
	t.Helper()
	is := is.New(t)
	ctx := context.Background()
	conn, err := s.Create(ctx, uuid.NewString(), TypeSource, "test-plugin", uuid.NewString(),
		Config{Name: "src", Settings: map[string]string{"table": "orders"}}, ProvisionTypeAPI)
	is.NoErr(err)
	_, err = s.SetState(ctx, conn.ID, SourceState{Position: opencdc.Position("orders:42")})
	is.NoErr(err)
	return conn
}

// TestService_Restore_AfterDelete checks the #3137 contract: the deleted
// instance itself comes back, in memory and in the store, with its state.
func TestService_Restore_AfterDelete(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	ctrl := gomock.NewController(t)
	db := &inmemory.DB{}
	s := NewService(logger, db, nil)
	conn := newRestoreTestConnector(t, s)

	is.NoErr(s.Delete(ctx, conn.ID, fakePluginFetcher{conn.Plugin: pmock.NewDispenser(ctrl)}))
	is.NoErr(s.Restore(ctx, conn))

	got, err := s.Get(ctx, conn.ID)
	is.NoErr(err)
	is.True(got == conn)
	is.Equal(got.State, SourceState{Position: opencdc.Position("orders:42")})

	// The delete really reached the store (no transaction), so Restore wrote
	// the record back: a restart sees the connector with its position.
	restarted := NewService(logger, db, nil)
	is.NoErr(restarted.Init(ctx))
	stored, err := restarted.Get(ctx, conn.ID)
	is.NoErr(err)
	is.Equal(stored.State, conn.State)
	is.True(stored.CreatedAt.Equal(conn.CreatedAt))
}

func TestService_Restore_SameInstanceIsNoOp(t *testing.T) {
	is := is.New(t)
	s := NewService(log.Nop(), &inmemory.DB{}, nil)
	conn := newRestoreTestConnector(t, s)

	is.NoErr(s.Restore(context.Background(), conn))
	got, err := s.Get(context.Background(), conn.ID)
	is.NoErr(err)
	is.True(got == conn)
}

// TestService_Restore_RefusesToReplace makes sure a rollback can never swap
// out a different live instance registered under the same ID.
func TestService_Restore_RefusesToReplace(t *testing.T) {
	is := is.New(t)
	s := NewService(log.Nop(), &inmemory.DB{}, nil)
	conn := newRestoreTestConnector(t, s)

	other := &Instance{ID: conn.ID, Type: TypeSource}
	is.True(s.Restore(context.Background(), other) != nil)
	got, err := s.Get(context.Background(), conn.ID)
	is.NoErr(err)
	is.True(got == conn)

	is.True(s.Restore(context.Background(), nil) != nil)
}
