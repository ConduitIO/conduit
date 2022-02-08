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

// TODO: Uncomment before final push
// //go:build integration

package snapshot

import (
	"database/sql"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin/sdk"

	_ "github.com/lib/pq"
)

// SNAPSHOT_TEST_URL is a non-replication user url for the test postgres d
const SNAPSHOT_TEST_URL = "postgres://meroxauser:meroxapass@localhost:5432/meroxadb?sslmode=disable"

func TestSnapshotterReads(t *testing.T) {
	db := getTestPostgres(t)
	s, err := NewSnapshotter(db, "records", []string{"id",
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
	db := getTestPostgres(t)
	s, err := NewSnapshotter(db, "records", []string{"id",
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
	db := getTestPostgres(t)
	s, err := NewSnapshotter(db, "records", []string{"id",
		"column1", "key"}, "key")
	assert.Ok(t, err)
	next1 := s.HasNext()
	assert.Equal(t, true, next1)
	teardownErr := s.Teardown()
	assert.True(t, cerrors.Is(teardownErr, ErrSnapshotInterrupt),
		"failed to get snapshot interrupt error")
	_, err = s.Next()
	assert.Error(t, err)
	next2 := s.HasNext()
	assert.Equal(t, false, next2)
	rec, err := s.Next()
	assert.Equal(t, rec, sdk.Record{})
	assert.True(t, cerrors.Is(err, ErrNoRows),
		"failed to get snapshot incomplete")
}

// getTestPostgres is a testing helper that fails if it can't setup a Postgres
// connection and returns a DB and the connection string.
// * It starts and migrates a db with 5 rows for Test_Read* and Test_Open*
func getTestPostgres(t *testing.T) *sql.DB {
	prepareDB := []string{
		// drop any existing data
		`DROP TABLE IF EXISTS records;`,
		// setup records table
		`CREATE TABLE IF NOT EXISTS records (
		id bigserial PRIMARY KEY,
		key bytea,
		column1 varchar(256),
		column2 integer,
		column3 boolean);`,
		// seed values
		`INSERT INTO records(key, column1, column2, column3)
		VALUES('1', 'foo', 123, false),
		('2', 'bar', 456, true),
		('3', 'baz', 789, false),
		('4', null, null, null);`,
	}
	db, err := sql.Open("postgres", SNAPSHOT_TEST_URL)
	assert.Ok(t, err)
	db = migrate(t, db, prepareDB)
	assert.Ok(t, err)
	return db
}

// migrate will run a set of migrations on a database to prepare it for a test
// it fails the test if any migrations are not applied.
func migrate(t *testing.T, db *sql.DB, migrations []string) *sql.DB {
	for _, migration := range migrations {
		_, err := db.Exec(migration)
		assert.Ok(t, err)
	}
	return db
}
