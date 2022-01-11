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
	"database/sql"
	"math/big"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/assert"
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
						"table": "records",
						"url":   DBURL,
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
						"table": "records",
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
						"table":   "records",
						"columns": "key,column1,column2,column3",
						"url":     DBURL,
					},
				},
			},
			wantErr: false,
		},
		{
			name:   "should configure plugin to key from primary key column by default",
			fields: fields{},
			args: args{
				ctx: context.Background(),
				cfg: plugins.Config{
					Settings: map[string]string{
						"table": "records",
						"url":   DBURL,
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
						"table": "records",
						"url":   DBURL,
						"key":   "key",
					},
				},
			},
			wantErr: false,
		},
		{
			name:   "should handle active mode being set from config",
			fields: fields{},
			args: args{
				ctx: context.Background(),
				cfg: plugins.Config{
					Settings: map[string]string{
						"table": "records",
						"url":   DBURL,
						"key":   "key",
						"mode":  "active",
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
			if err := s.Open(tt.args.ctx, tt.args.cfg); (err != nil) != tt.wantErr {
				t.Errorf("Source.Open() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSource_Read(t *testing.T) {
	db := getTestPostgres(t)
	type fields struct {
		db      *sql.DB
		table   string
		columns []string
		key     string
	}
	type args struct {
		ctx context.Context
		pos record.Position
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    record.Record
		wantErr bool
	}{
		{
			name: "should read record at position 2 when position 1 is passed",
			fields: fields{
				db:      db,
				table:   "records",
				columns: []string{"key", "column1", "column2", "column3"},
				key:     "key",
			},
			args: args{
				ctx: context.Background(),
				pos: []byte("1"),
			},
			want: record.Record{
				Key: record.StructuredData{
					"key": "2",
				},
				Position: record.Position("2"),
				Payload: record.StructuredData{
					"key":     "2",
					"column1": "bar",
					"column2": int64(456),
					"column3": true,
				},
				Metadata: map[string]string{
					"key":   "key",
					"table": "records",
				},
			},
			wantErr: false,
		},
		{
			name: "should read record at position 1 when position is nil",
			fields: fields{
				db:      db,
				table:   "records",
				columns: []string{"key", "column1", "column2", "column3"},
				key:     "key",
			},
			args: args{
				ctx: context.Background(),
				pos: nil,
			},
			want: record.Record{
				Key: record.StructuredData{
					"key": "1",
				},
				Position: record.Position("1"),
				Payload: record.StructuredData{
					"key":     "1",
					"column1": "foo",
					"column2": int64(123),
					"column3": false,
				},
				Metadata: map[string]string{
					"key":   "key",
					"table": "records",
				},
			},
			wantErr: false,
		},
		{
			name: "should return only selected columns",
			fields: fields{
				db:      db,
				table:   "records",
				columns: []string{"key", "column1"},
				key:     "key",
			},
			args: args{
				ctx: context.Background(),
				pos: nil,
			},
			want: record.Record{
				Key: record.StructuredData{
					"key": "1",
				},
				Position: record.Position("1"),
				Payload: record.StructuredData{
					"key":     "1",
					"column1": "foo",
				},
				Metadata: map[string]string{
					"key":   "key",
					"table": "records",
				},
			},
			wantErr: false,
		},
		{
			name: "should error if table does not exist",
			fields: fields{
				db:      db,
				table:   "doesnotexist",
				columns: []string{"key", "column1"},
				key:     "key",
			},
			args: args{
				ctx: context.Background(),
				pos: big.NewInt(int64(1)).Bytes(),
			},
			want:    record.Record{},
			wantErr: true,
		},
		{
			name: "should error if position is greater than highest record id",
			fields: fields{
				db:      db,
				table:   "records",
				columns: []string{"key", "column1"},
				key:     "key",
			},
			args: args{
				ctx: context.Background(),
				pos: big.NewInt(int64(4)).Bytes(),
			},
			want:    record.Record{},
			wantErr: true,
		},
		{
			name: "should use any integer column as key if specified",
			fields: fields{
				db:      db,
				table:   "records",
				columns: []string{"id", "column1", "column2", "column3"},
				key:     "id",
			},
			args: args{
				ctx: context.Background(),
				pos: []byte("2"),
			},
			want: record.Record{
				Key: record.StructuredData{
					"id": "3",
				},
				Position: record.Position("3"),
				Payload: record.StructuredData{
					"id":      int64(3),
					"column1": "baz",
					"column2": int64(789),
					"column3": false,
				},
				Metadata: map[string]string{
					"table": "records",
					"key":   "id",
				},
			},
			wantErr: false,
		},
		{
			name: "should return nil payload values with column names when database values are nil",
			fields: fields{
				db:      db,
				table:   "records",
				columns: []string{"id", "key", "column1", "column2", "column3"},
				key:     "id",
			},
			args: args{
				ctx: context.Background(),
				pos: []byte("3"),
			},
			want: record.Record{
				Key: record.StructuredData{
					"id": "4",
				},
				Position: record.Position("4"),
				Payload: record.StructuredData{
					"id":      int64(4),
					"key":     "4",
					"column1": nil,
					"column2": nil,
					"column3": nil,
				},
				Metadata: map[string]string{
					"table": "records",
					"key":   "id",
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Source{
				db:      tt.fields.db,
				table:   tt.fields.table,
				columns: tt.fields.columns,
				key:     tt.fields.key,
			}
			got, err := s.Read(tt.args.ctx, tt.args.pos)
			if (err != nil) != tt.wantErr {
				t.Errorf("Source.Read() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if diff := cmp.Diff(got, tt.want, cmpopts.IgnoreFields(record.Record{}, "CreatedAt")); diff != "" {
				t.Errorf("Source.Read(): %s", diff)
			}
			if !tt.wantErr {
				assert.True(t, got.CreatedAt.IsZero() == false, "failed to assign a proper timestamp")
			}
		})
	}
}

func TestOpen_Defaults(t *testing.T) {
	_ = getTestPostgres(t)
	s := &Source{}
	err := s.Open(context.Background(), plugins.Config{
		Settings: map[string]string{
			"table": "records",
			"url":   DBURL,
		},
	})
	assert.Ok(t, err)
	// assert that we are keying by id by default
	assert.Equal(t, s.key, "id")
	// assert that we are collecting all columns by default
	assert.Equal(t, []string{"id", "key", "column1", "column2", "column3"}, s.columns)
}

func TestRead_Rows(t *testing.T) {
	db := getTestPostgres(t)
	s := &Source{
		db:      db,
		table:   "records",
		columns: []string{"id", "key", "column1", "column2", "column3"},
		key:     "key",
	}

	i := "0"
	for {
		rec, err := s.Read(context.Background(), record.Position(i))
		if err != nil {
			assert.True(t, plugins.IsRecoverableError(err), "expected recoverable error")
			break
		}
		assert.Ok(t, err)
		i = rec.Position.String()
	}
	assert.Equal(t, i, "4")
}

func TestRead_Iterator(t *testing.T) {
	ctx := context.Background()
	conn, err := sql.Open("postgres", DBURL)
	assert.Ok(t, err)
	migrations := []string{
		`DROP TABLE IF EXISTS records;`,
		`CREATE TABLE IF NOT EXISTS records (
			id serial,
			key bytea PRIMARY KEY,
			column1 varchar(256),
			column2 integer,
			column3 boolean
		);`,
	}
	_ = migrate(t, conn, migrations)
	src := &Source{}
	err = src.Open(context.Background(), plugins.Config{
		Settings: map[string]string{
			"cdc":              "true",
			"publication_name": "meroxa",
			"slot_name":        "meroxa",
			"url":              RepDBURL,
			"key":              "key",
			"table":            "records",
			"columns":          "key,column1,column2,column3",
		},
	})
	assert.Ok(t, err)
	sql, args, err := psql.
		Insert("records").
		Columns("key", "column1", "column2", "column3").
		Values("1", "foo", 123, false).
		ToSql()
	assert.Ok(t, err)
	_, err = conn.Exec(sql, args...)
	assert.Ok(t, err)
	time.Sleep(time.Second * 1)

	r1, err := src.Read(ctx, record.Position("0"))
	assert.Ok(t, err)
	r2, err := src.Read(ctx, record.Position("1"))
	assert.Ok(t, err)

	want1 := record.Record{
		Position: record.Position("1"),
		Metadata: map[string]string{
			"key":   "key",
			"table": "records",
		},
		Key: record.StructuredData{
			"key": "1",
		},
		Payload: record.StructuredData{
			"column1": "foo",
			"column2": int64(123),
			"column3": false,
			"key":     "1",
		},
	}
	want2 := record.Record{
		Position: record.Position("1"),
		Metadata: map[string]string{
			"action": "insert",
			"table":  "records",
		},
		Key: record.StructuredData{
			"key": "1",
		},
		Payload: record.StructuredData{
			"column1": "foo",
			"column2": int64(123),
			"column3": false,
			"key":     "1",
		},
	}
	diff1 := cmp.Diff(r1, want1, cmpopts.IgnoreFields(
		// ignore timestamp only.
		// the connector should always returns the rows first and then check
		// the buffer, so we can assert on this record's Position.
		record.Record{}, "CreatedAt"),
	)
	diff2 := cmp.Diff(r2, want2, cmpopts.IgnoreFields(
		// ignore timestamp and position on second record because it's coming
		// from the WAL and we don't know that record's Position.
		record.Record{}, "CreatedAt", "Position"),
	)
	assert.Equal(t, "", diff1)
	assert.Equal(t, "", diff2)
	assert.True(t, r1.CreatedAt.IsZero() == false, "failed to set timestampe")
	assert.True(t, r2.CreatedAt.IsZero() == false, "failed to set timestampe")
	err = src.Teardown()
	assert.Ok(t, err)
	assert.True(t, src.sub == nil, "failed to cleanup subscription")
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
