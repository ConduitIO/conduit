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

//go:build integration

package source

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record"
)

func TestSnapshotterReads(t *testing.T) {
	ctx := context.Background()
	db := getTestPostgres(t)
	s, err := NewSnapshotter(ctx, db, "records", []string{"id",
		"column1", "key"}, "key")
	assert.Ok(t, err)
	i := 0
	for {
		if next := s.HasNext(); !next {
			break
		}
		i++
		_, err := s.Next()
		assert.Ok(t, err)
	}
	assert.Equal(t, 4, i)
	assert.Ok(t, s.Teardown())
	assert.True(t, s.snapshotComplete == true,
		"failed to mark snapshot complete")
}

func TestSnapshotterTeardown(t *testing.T) {
	ctx := context.Background()
	db := getTestPostgres(t)
	s, err := NewSnapshotter(ctx, db, "records", []string{"id",
		"column1", "key"}, "key")
	assert.Ok(t, err)
	assert.True(t, s.HasNext(), "failed to queue up record")
	_, err = s.Next()
	assert.Ok(t, err)
	assert.True(t, !s.snapshotComplete,
		"snapshot prematurely marked complete")
	got := s.Teardown()
	assert.True(t, cerrors.Is(got, ErrSnapshotInterrupt),
		"failed to get snapshot interrupt")
}

func TestPrematureDBClose(t *testing.T) {
	ctx := context.Background()
	db := getTestPostgres(t)
	s, err := NewSnapshotter(ctx, db, "records", []string{"id",
		"column1", "key"}, "key")
	assert.Ok(t, err)
	// assert that we have at least one row and it's loading as expected
	next1 := s.HasNext()
	assert.Equal(t, true, next1)
	// teardown to prematurely kill our DB connection and assert we get
	// an ErrSnapshotInterrupt error
	teardownErr := s.Teardown()
	assert.True(t, cerrors.Is(teardownErr, ErrSnapshotInterrupt),
		"failed to get snapshot interrupt error")
	// assert Next fails because we have no rows to read.
	_, err = s.Next()
	assert.Error(t, err)
	// assert calling HasNext again returns false.
	next2 := s.HasNext()
	assert.Equal(t, false, next2)
	// finally , assert calling Next when HasNext has returned false returns
	// an ErrNoRows error for calling after database is closed.
	rec, err := s.Next()
	assert.Equal(t, rec, record.Record{})
	assert.True(t, cerrors.Is(err, ErrNoRows),
		"failed to get snapshot incomplete")
}
