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
package cdc

import (
	"context"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/plugin/sdk"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/jackc/pgx/v4"
)

const (
	// CDC_TEST_URL is the URI for the _logical replication_ server and user.
	// This is separate from the DB_URL used above since it requires a different
	// user and permissions for replication.
	CDC_TEST_URL = "postgres://repmgr:repmgrmeroxa@localhost:5432/meroxadb?sslmode=disable"
)

func TestCDC(t *testing.T) {
	t.Run("should detect insert", func(t *testing.T) {
		_ = getTestPostgres(t)
		i := getDefaultConnector(t)
		t.Cleanup(func() {
			assert.Ok(t, i.Teardown())
		})
		now := time.Now()
		rows, err := i.conn.Query(`insert into
		records(id, column1, column2, column3)
		values (6, 'bizz', 456, false);`)
		assert.Ok(t, err)
		assert.Ok(t, rows.Err())
		time.Sleep(1 * time.Second)
		assert.True(t, i.HasNext(), "failed to queue up a cdc record")
		got, err := i.Next()
		assert.Ok(t, err)
		want := sdk.Record{
			Key: sdk.StructuredData{"id": int64(6)},
			Metadata: map[string]string{
				"table":  "records",
				"action": "insert",
			},
			Payload: sdk.StructuredData{
				"column1": string("bizz"),
				"column2": int32(456),
				"column3": bool(false),
			},
		}
		// TODO: don't ignore position here. we're working out position handling
		// but we should make sure we absolutely can predict and understand a
		// records position to some degree.
		diff := cmp.Diff(
			got,
			want,
			cmpopts.IgnoreFields(sdk.Record{}, "CreatedAt", "Position"))
		if diff != "" {
			t.Errorf("%s", diff)
		}
		assert.True(t, got.CreatedAt.After(now), "failed to set CreatedAt")
	})

	t.Run("should detect update", func(t *testing.T) {
		_ = getTestPostgres(t)
		i := getDefaultConnector(t)
		t.Cleanup(func() {
			assert.Ok(t, i.Teardown())
		})
		now := time.Now()
		_, err := i.conn.Query(`update records
			set column1 = 'fizz', column2 = 789, column3 = true
			where id = 1;
		`)
		assert.Ok(t, err)
		time.Sleep(1 * time.Second)
		assert.True(t, i.HasNext(), "failed to queue a cdc record after update")

		want := sdk.Record{
			Key: sdk.StructuredData{"id": int64(1)},
			Metadata: map[string]string{
				"action": "update",
				"table":  "records",
			},
			Payload: sdk.StructuredData{
				"column1": string("fizz"),
				"column2": int32(789),
				"column3": bool(true),
				"key":     []uint8("1"),
			},
		}
		got, err := i.Next()
		assert.Ok(t, err)
		if diff := cmp.Diff(
			got,
			want,
			cmpopts.IgnoreFields(
				sdk.Record{},
				"CreatedAt",
				"Position", // TODO: sanity check assert on position
			)); diff != "" {
			t.Errorf("%s", diff)
		}
		assert.True(t, got.CreatedAt.After(now), "failed to set CreatedAt")
	})

	t.Run("should detect a row delete", func(t *testing.T) {
		_ = getTestPostgres(t)
		i := getDefaultConnector(t)
		t.Cleanup(func() {
			assert.Ok(t, i.Teardown())
		})
		_, err := i.conn.Query(`delete from records where column1 = 'bar';`)
		assert.Ok(t, err)
		want := sdk.Record{
			Key: sdk.StructuredData{"id": int64(2)},
			Metadata: map[string]string{
				"action": "delete",
				"table":  "records",
			},
			Payload: nil,
		}
		got, err := i.Next()
		assert.Ok(t, err)
		if diff := cmp.Diff(
			got,
			want,
			cmpopts.IgnoreFields(
				sdk.Record{},
				"CreatedAt", // TODO: Assert what we can about time and date
				"Position",  // TODO: Assert what we can about position
			)); diff != "" {
			t.Errorf("%s", diff)
		}
	})
}

func getDefaultConnector(t *testing.T) *Iterator {
	_ = getTestPostgres(t)
	ctx := context.Background()
	config := Config{
		URL:       CDC_TEST_URL,
		TableName: "records",
		// Position:  sdk.Position("0"),
	}
	i, err := NewCDCIterator(ctx, config)
	assert.Ok(t, err)
	assert.Equal(t, i.config.KeyColumnName, "id")
	assert.Equal(t, []string{"id", "key", "column1", "column2", "column3"},
		i.config.Columns)
	return i
}

// getTestPostgres is a testing helper that fails if it can't setup a Postgres
// connection and returns a DB and the connection string.
// * It starts and migrates a db with 5 rows for Test_Read* and Test_Open*
func getTestPostgres(t *testing.T) *pgx.Conn {
	prepareDB := []string{
		`DROP TABLE IF EXISTS records;`,
		`CREATE TABLE IF NOT EXISTS records (
		id bigserial PRIMARY KEY,
		key bytea,
		column1 varchar(256),
		column2 integer,
		column3 boolean);`,
		`INSERT INTO records(key, column1, column2, column3)
		VALUES('1', 'foo', 123, false),
		('2', 'bar', 456, true),
		('3', 'baz', 789, false),
		('4', null, null, null);`,
	}
	// db, err := Open("postgres", CDC_TEST_URL)
	conn, err := pgx.Connect(context.Background(), CDC_TEST_URL)
	assert.Ok(t, err)
	conn = migrate(t, conn, prepareDB)
	assert.Ok(t, err)
	return conn
}

// migrate will run a set of migrations on a database to prepare it for a test
// it fails the test if any migrations are not applied.
func migrate(t *testing.T, conn *pgx.Conn, migrations []string) *pgx.Conn {
	for _, migration := range migrations {
		_, err := conn.Exec(context.Background(), migration)
		assert.Ok(t, err)
	}
	return conn
}
