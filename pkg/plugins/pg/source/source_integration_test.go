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

// State should not be shared between tests, so any test that relies on a
// logical replication slot should have its own slot and publication name for
// the test.
// Assigning tests unique slot names also means we can relate container logs
// to specific tests when debugging.
// Any test that calls `Open` successfully should call `Teardown`.

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugins"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	_ "github.com/lib/pq"
)

const (
	// DBURL is the URI for the Postgres server used for integration tests
	DBURL = "postgres://meroxauser:meroxapass@localhost:5432/meroxadb?sslmode=disable"
	// RepDBURL is the URI for the _logical replication_ server and user.
	// This is separate from the DB_URL used above since it requires a different
	// user and permissions for replication.
	RepDBURL = "postgres://repmgr:repmgrmeroxa@localhost:5432/meroxadb?sslmode=disable"
)

func TestSource_Open(t *testing.T) {
	_ = getTestPostgres(t)
	type fields struct {
		table   string
		columns []string
		key     string
	}
	type args struct {
		ctx context.Context
		cfg plugins.Config
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
		wanted  *Source
	}{
		{
			name:   "should default to collect all columns from table",
			fields: fields{},
			args: args{
				ctx: context.Background(),
				cfg: plugins.Config{
					Settings: map[string]string{
						"table":    "records",
						"url":      DBURL,
						"snapshot": "disabled",
						"cdc":      "disabled",
					},
				},
			},
			wantErr: false,
		},
		{
			name:   "should error if no url is provided",
			fields: fields{},
			args: args{
				ctx: context.Background(),
				cfg: plugins.Config{
					Settings: map[string]string{
						"table":    "records",
						"snapshot": "disabled",
						"cdc":      "disabled",
					},
				},
			},
			wantErr: true,
		},
		{
			name:   "should configure plugin to read selected columns",
			fields: fields{},
			args: args{
				ctx: context.Background(),
				cfg: plugins.Config{
					Settings: map[string]string{
						"table":    "records",
						"columns":  "key,column1,column2,column3",
						"url":      DBURL,
						"snapshot": "disabled",
						"cdc":      "disabled",
					},
				},
			},
			wantErr: false,
		},
		{
			name:   "should set key to table primary key by default",
			fields: fields{},
			args: args{
				ctx: context.Background(),
				cfg: plugins.Config{
					Settings: map[string]string{
						"table":    "records",
						"url":      DBURL,
						"snapshot": "disabled",
						"cdc":      "disabled",
					},
				},
			},
			wantErr: false,
		},
		{
			name:   "should handle key being set from config",
			fields: fields{},
			args: args{
				ctx: context.Background(),
				cfg: plugins.Config{
					Settings: map[string]string{
						"table":    "records",
						"url":      DBURL,
						"key":      "key",
						"cdc":      "disabled",
						"snapshot": "disabled",
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Source{
				table:   tt.fields.table,
				columns: tt.fields.columns,
				key:     tt.fields.key,
				db:      nil,
			}
			t.Cleanup(func() { assert.Ok(t, s.Teardown()) })
			if err := s.Open(tt.args.ctx, tt.args.cfg); (err != nil) != tt.wantErr {
				t.Errorf("Source.Open() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateDisabledSettings(t *testing.T) {
	_ = getTestPostgres(t)
	s := &Source{}
	cfg := plugins.Config{
		Settings: map[string]string{
			"table":    "records",
			"url":      DBURL,
			"snapshot": "disabled",
			"cdc":      "disabled",
		},
	}
	t.Cleanup(func() { assert.Ok(t, s.Teardown()) })
	err := s.Validate(cfg)
	assert.Ok(t, err)
	err = s.Open(context.Background(), cfg)
	assert.Ok(t, err)
	assert.Equal(t, nil, s.snapshotter)
	assert.Equal(t, nil, s.cdc)
}

func TestOpen_Defaults(t *testing.T) {
	_ = getTestPostgres(t)
	s := &Source{}
	err := s.Open(context.Background(), plugins.Config{
		Settings: map[string]string{
			"table":            "records",
			"url":              RepDBURL,
			"slot_name":        "meroxatestdefaults",
			"publication_name": "meroxatestdefaults",
		},
	})
	assert.Ok(t, err)
	t.Cleanup(func() { assert.Equal(t, ErrSnapshotInterrupt, s.Teardown()) })
	assert.Equal(t, s.key, "id")
	assert.Equal(t, []string{"id", "key", "column1", "column2", "column3"},
		s.columns)
	assert.True(t, s.snapshotter != nil, "failed to set snapshotter default")
	assert.True(t, s.cdc != nil, "failed to set cdc default")
}

func TestCDC(t *testing.T) {
	_ = getTestPostgres(t)
	s := &Source{}
	err := s.Open(context.Background(), plugins.Config{
		Settings: map[string]string{
			"table":            "records",
			"url":              RepDBURL,
			"snapshot":         "disabled",
			"slot_name":        "meroxatestcdc",
			"publication_name": "meroxatestcdc",
		},
	})
	assert.Ok(t, err)
	t.Cleanup(func() { assert.Ok(t, s.Teardown()) })
	rows, err := s.db.Query(`insert into records(id, column1, column2, column3)
	values (6, 'bizz', 456, false);`)
	assert.Ok(t, err)
	assert.Ok(t, rows.Err())
	assert.Equal(t, s.key, "id")
	assert.Equal(t, []string{"id", "key", "column1", "column2", "column3"},
		s.columns)
	assert.True(t, s.cdc != nil, "failed to set cdc default")
	// load bearing sleep because we have to wait on postgres.
	time.Sleep(1 * time.Second)
	assert.True(t, s.cdc.HasNext(), "failed to queue up a cdc record")
	rec2, err := s.Read(context.Background(), nil)
	assert.Ok(t, err)
	assert.Equal(t, map[string]string{
		"table":  "records",
		"action": "insert",
	}, rec2.Metadata)
	assert.True(t, rec2.Position.String() != "<nil>", "failed to set position")
}

func TestCDCIterator(t *testing.T) {
	_ = getTestPostgres(t)
	s := Source{}
	err := s.Open(context.Background(), plugins.Config{
		Settings: map[string]string{
			"table": "records",
			"url":   RepDBURL,
			// disable snapshot mode since it's not being tested
			"snapshot":         "disabled",
			"slot_name":        "meroxatestiterator",
			"publication_name": "meroxatestiterator",
		},
	})
	assert.Ok(t, err)
	t.Cleanup(func() { assert.Ok(t, s.Teardown()) })
	rec, err := s.Read(context.Background(), nil)
	assert.Equal(t, rec, record.Record{})
	assert.True(t, cerrors.Is(err, plugins.ErrEndData),
		"failed to get errenddata")
	// insert events to populate the cdc channel
	rows, err := s.db.Query(`insert into records(column1, column2, column3) 
	values ('biz', 666, false);`)
	assert.Ok(t, err)
	assert.Ok(t, rows.Err())
	// load bearing sleep because we have to wait on postgres.
	time.Sleep(1 * time.Second)
	assert.True(t, s.cdc.HasNext(), "failed to queue cdc record")
	rec, err = s.cdc.Next()
	assert.Ok(t, err)
	assert.True(t, len(rec.Payload.Bytes()) > 0, "failed to get cdc payload")
}
func TestByteaKeys(t *testing.T) {
	_ = getTestPostgres(t)
	s := &Source{}
	err := s.Open(context.Background(), plugins.Config{
		Settings: map[string]string{
			"table": "records",
			"key":   "key",
			"url":   RepDBURL,
		},
	})
	assert.Ok(t, err)
	// assert that we are keying by id by default
	assert.Equal(t, s.key, "key")
	// assert that we are collecting all columns by default
	assert.Equal(t, []string{"id", "key", "column1", "column2", "column3"}, s.columns)
	r1, err := s.Read(context.Background(), nil)
	assert.Ok(t, err)
	diff := cmp.Diff(record.Record{
		Position: record.Position("1"),
		Metadata: map[string]string{
			"key":   "key",
			"table": "records",
		},
		SourceID: "",
		Key:      record.StructuredData{"key": "1"},
		Payload: record.StructuredData{
			"column1": "foo",
			"column2": int64(123),
			"column3": false,
			"id":      int64(1),
			"key":     "1",
		}}, r1, cmpopts.IgnoreFields(record.Record{}, "CreatedAt"))
	if diff != "" {
		t.Errorf("failed to get proper record: %s", diff)
	}
	err = s.Teardown()
	assert.True(t, cerrors.Is(err, ErrSnapshotInterrupt), "failed to get snapshot interrupt error")
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
	db, err := sql.Open("postgres", DBURL)
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
