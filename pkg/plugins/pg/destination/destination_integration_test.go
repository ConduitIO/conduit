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

package destination

import (
	"context"
	"database/sql"
	"math/big"
	"reflect"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/plugins"
	"github.com/conduitio/conduit/pkg/record"

	sq "github.com/Masterminds/squirrel"
	_ "github.com/lib/pq"
)

// DBURL is the URI to the Postgres instance that docker-compose starts
const DBURL = "postgres://meroxauser:meroxapass@localhost:5432/meroxadb?sslmode=disable"

// TestDestination_Write is a table test that writes sequentially to the database and
// DOES NOT cleanup rows in between test cases but it DOES between test runs.
// This means that order matters between test cases.
func TestDestination_Write(t *testing.T) {
	db := getTestPostgres(t)
	type fields struct {
		Position record.Position
		db       *sql.DB
	}
	type args struct {
		ctx context.Context
		r   record.Record
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    record.Position
		payload record.StructuredData
		wantErr bool
	}{
		{
			name: "should insert a record with a key and structured data",
			fields: fields{
				Position: nil,
				db:       db,
			},
			args: args{
				ctx: context.Background(),
				r: record.Record{
					Position: big.NewInt(int64(1)).Bytes(),
					Key: record.StructuredData{
						"key": "0xDEADBEEF",
					},
					Payload: record.StructuredData{
						"column1": "foo",
						"column2": 123,
						"column3": false,
					},
					CreatedAt: time.Now(),
					Metadata: map[string]string{
						"table": "records",
					},
				},
			},
			want: big.NewInt(int64(1)).Bytes(),
			payload: record.StructuredData{
				"column1": "foo",
				"column2": 123,
				"column3": false,
			},
			wantErr: false,
		},
		{
			name: "should upsert a record with a key and structured data",
			fields: fields{
				Position: big.NewInt(int64(1)).Bytes(),
				db:       db,
			},
			args: args{
				ctx: context.Background(),
				r: record.Record{
					Position: big.NewInt(int64(2)).Bytes(),
					Key: record.StructuredData{
						"key": "0xDEADBEEF",
					},
					Payload: record.StructuredData{
						"column1": "bar",
						"column2": 456,
						"column3": true,
					},
					CreatedAt: time.Now(),
					Metadata: map[string]string{
						"table": "records",
					},
				},
			},
			want: big.NewInt(int64(2)).Bytes(),
			payload: record.StructuredData{
				"column1": "bar",
				"column2": 456,
				"column3": true,
			},
			wantErr: false,
		},
		{
			name: "should error when column does not exist",
			fields: fields{
				Position: big.NewInt(int64(1)).Bytes(),
				db:       db,
			},
			args: args{
				ctx: context.Background(),
				r: record.Record{
					Position: big.NewInt(int64(2)).Bytes(),
					Payload: record.StructuredData{
						"coldoesnotexsit": 123,
					},
					CreatedAt: time.Now(),
				},
			},
			want:    big.NewInt(int64(1)).Bytes(),
			wantErr: true,
		}, {
			name: "should error if payload is not structured data",
			fields: fields{
				Position: big.NewInt(int64(1)).Bytes(),
				db:       db,
			},
			args: args{
				ctx: context.Background(),
				r: record.Record{
					Payload:   record.RawData{},
					CreatedAt: time.Now(),
					Metadata:  make(map[string]string),
				},
			},
			want:    big.NewInt(int64(1)).Bytes(),
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Destination{
				Position: tt.fields.Position,
				db:       tt.fields.db,
			}

			got, err := s.Write(tt.args.ctx, tt.args.r)
			if (err != nil) != tt.wantErr {
				t.Errorf("Destination.Write() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Destination.Write() = [%v] - want [%v]", got, tt.want)
			}

			if tt.wantErr == false {
				data, ok := tt.args.r.Key.(record.StructuredData)
				if !ok {
					// TODO: handle keys in non-structured formats
					t.Errorf("failed to provide valid key - want %T - got %T", record.StructuredData{}, tt.args.r.Key)
				}

				// dynamically get the key and val since we don't want to hardcode
				// those for tests
				var key string
				var val interface{}
				for k, v := range data {
					key = k
					val = v
					break
				}

				// get the key from the DB and compare it against what we wanted
				checkAndCompare(t, tt.fields.db, key, val, tt.payload)
			}
		})
	}
}

// TestDestination_Open attempts to Open the plugin with a variety of test cases.
// This test overrides an environment variable for the test and cleans it up.
func TestDestination_Open(t *testing.T) {
	_ = getTestPostgres(t)
	type fields struct {
		Position record.Position
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
	}{
		{
			name: "should open a connector with a given config",
			args: args{
				ctx: context.Background(),
				cfg: plugins.Config{
					Settings: map[string]string{
						"url": DBURL,
					},
				},
			},
			wantErr: false,
		},
		{
			name: "should error when invalid URL is provided",
			args: args{
				ctx: context.Background(),
				cfg: plugins.Config{
					Settings: map[string]string{
						"url": "foobar",
					},
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Destination{
				Position: tt.fields.Position,
			}
			if err := s.Open(tt.args.ctx, tt.args.cfg); (err != nil) != tt.wantErr {
				t.Errorf("Destination.Open() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// checkAndCompare will fetch a row from the database where colkey is equal
// to keyVal and will assert that it was written and equal to want.
// want expects a map of column names and associated values, in this test case
// those are column1, column2, and column3.
func checkAndCompare(t *testing.T, db *sql.DB, colKey string, keyVal interface{}, want record.StructuredData) {
	psql := sq.StatementBuilder.PlaceholderFormat(sq.Dollar)
	equal := sq.Eq{}
	equal[colKey] = keyVal
	sql, args, err := psql.
		Select("key, column1, column2, column3").
		From("records").
		Where(equal).
		ToSql()
	if err != nil {
		t.Errorf("failed to construct assertion query: %s", err)
	}
	row := db.QueryRow(sql, args...)
	var (
		k    string
		col1 string
		col2 int
		col3 bool
	)
	if row.Err() != nil {
		t.Errorf("failed to query row: %s", err)
	}
	err = row.Scan(&k, &col1, &col2, &col3)
	if err != nil {
		t.Errorf("failed to scan row: %s", err)
	}
	assert.Equal(t, k, keyVal)
	r := record.StructuredData{
		"column1": col1,
		"column2": col2,
		"column3": col3,
	}
	assert.Equal(t, want, r)
}

// getTestPostgres is a testing helper that fails if it can't setup a Postgres
// connection and returns a DB and the connection string.
// * It starts and migrates a db with 4 rows for Test_Read* and Test_Open*
func getTestPostgres(t *testing.T) *sql.DB {
	prepareDB := []string{
		// drop any existing data
		`DROP TABLE IF EXISTS records;`,
		// setup records table
		`CREATE TABLE IF NOT EXISTS records (
		key bytea PRIMARY KEY,
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
