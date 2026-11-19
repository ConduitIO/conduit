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

package pipeline

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/google/uuid"
	"github.com/matryer/is"
)

// newRestoreTestPipeline creates a pipeline with two connectors attached.
func newRestoreTestPipeline(t *testing.T, s *Service) (*Instance, []string) {
	t.Helper()
	is := is.New(t)
	ctx := context.Background()

	pl, err := s.Create(ctx, uuid.NewString(), Config{Name: "p-" + uuid.NewString()}, ProvisionTypeAPI)
	is.NoErr(err)
	ids := []string{uuid.NewString(), uuid.NewString()}
	for _, id := range ids {
		_, err = s.AddConnector(ctx, pl.ID, id)
		is.NoErr(err)
	}
	return pl, ids
}

// TestService_RestoreConnectorIDs_InTransactionDoesNotWrite checks that
// RestoreConnectorIDs leaves the store alone inside a transaction: the caller
// is rolling back and discards it, and once Commit has run (even a failed
// Commit) a write through that transaction is rejected outright.
func TestService_RestoreConnectorIDs_InTransactionDoesNotWrite(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	s := NewService(log.Nop(), db)
	pl, ids := newRestoreTestPipeline(t, s)
	updatedAt := pl.UpdatedAt

	txn, tctx, err := db.NewTransaction(ctx, true)
	is.NoErr(err)
	_, err = s.RemoveConnector(tctx, pl.ID, ids[0])
	is.NoErr(err)
	is.NoErr(s.RestoreConnectorIDs(tctx, pl.ID, ids, updatedAt))
	// Nothing was written in txn after the removal: committing it now would
	// apply the removal alone.
	is.NoErr(txn.Commit())
	stored, err := NewStore(db).Get(ctx, pl.ID)
	is.NoErr(err)
	is.Equal(stored.ConnectorIDs, []string{ids[1]})

	// Memory is restored regardless, order and timestamp included.
	got, err := s.Get(ctx, pl.ID)
	is.NoErr(err)
	is.Equal(got.ConnectorIDs, ids)
	is.True(got.UpdatedAt.Equal(updatedAt))
}

// TestService_RestoreConnectorIDs_OutsideTransactionWrites checks the other
// branch: without a transaction the mutation reached the store, so the restore
// has to write the old list back.
func TestService_RestoreConnectorIDs_OutsideTransactionWrites(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	s := NewService(log.Nop(), db)
	pl, ids := newRestoreTestPipeline(t, s)
	updatedAt := pl.UpdatedAt

	_, err := s.RemoveConnector(ctx, pl.ID, ids[0])
	is.NoErr(err)
	is.NoErr(s.RestoreConnectorIDs(ctx, pl.ID, ids, updatedAt))

	stored, err := NewStore(db).Get(ctx, pl.ID)
	is.NoErr(err)
	is.Equal(stored.ConnectorIDs, ids)
	is.True(stored.UpdatedAt.Equal(updatedAt))
}

// TestService_RestoreConnectorIDs_CopiesTheList checks that the service does
// not keep the caller's slice: a rollback callback's captured copy must not be
// aliased by the instance a later mutation shifts in place.
func TestService_RestoreConnectorIDs_CopiesTheList(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	s := NewService(log.Nop(), &inmemory.DB{})
	pl, ids := newRestoreTestPipeline(t, s)
	want := slices.Clone(ids)

	is.NoErr(s.RestoreConnectorIDs(ctx, pl.ID, ids, pl.UpdatedAt))
	// RemoveConnector shifts the instance's slice in place. If the instance
	// still held the caller's slice, this would rewrite it under the caller.
	_, err := s.RemoveConnector(ctx, pl.ID, ids[0])
	is.NoErr(err)
	is.Equal(ids, want)
	got, err := s.Get(ctx, pl.ID)
	is.NoErr(err)
	is.Equal(got.ConnectorIDs, []string{ids[1]})
}

// TestService_RestoreConnectorIDs_UnknownPipeline checks that an unknown ID is
// an error rather than a panic: a rollback callback must not create a pipeline.
func TestService_RestoreConnectorIDs_UnknownPipeline(t *testing.T) {
	is := is.New(t)
	s := NewService(log.Nop(), &inmemory.DB{})
	err := s.RestoreConnectorIDs(context.Background(), "nope", []string{"a"}, time.Now())
	is.True(err != nil)
}
