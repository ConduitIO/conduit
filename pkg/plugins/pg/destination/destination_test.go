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

// //go:build integration

package destination

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/plugin/sdk"

	"github.com/jackc/pgx/v4"
)

// DBURL is the URI to the Postgres instance that docker-compose starts
const DBURL = "postgres://meroxauser:meroxapass@localhost:5432/meroxadb?sslmode=disable"

func TestAdapter_Write(t *testing.T) {
	type fields struct {
		UnimplementedDestination sdk.UnimplementedDestination
		conn                     *pgx.Conn
		config                   config
	}
	type args struct {
		ctx    context.Context
		record sdk.Record
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name: "should insert with default configs",
			fields: fields{
				conn:   getTestPostgres(t),
				config: config{},
			},
			args: args{
				ctx: context.Background(),
				record: sdk.Record{
					Position: sdk.Position("5"),
					Metadata: map[string]string{
						"action": "insert",
						"table":  "records",
					},
					Key: sdk.StructuredData{
						"key": "uuid-mimicking-key-1234",
					},
					Payload: sdk.StructuredData{
						"column1": "foo",
						"column2": 456,
						"column3": false,
					},
				},
			},
			wantErr: false,
		},
		{
			name: "should update on conflict",
			fields: fields{
				conn:   getTestPostgres(t),
				config: config{},
			},
			args: args{
				ctx: context.Background(),
				record: sdk.Record{
					Position: sdk.Position("5"),
					Metadata: map[string]string{
						"action": "update",
						"table":  "records",
					},
					Key: sdk.StructuredData{
						"key": "uuid-mimicking-key-1234",
					},
					Payload: sdk.StructuredData{
						"column1": "updateonconflict",
						"column2": 567,
						"column3": true,
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Destination{
				UnimplementedDestination: tt.fields.UnimplementedDestination,
				conn:                     tt.fields.conn,
				config:                   tt.fields.config,
			}
			if err := d.Write(tt.args.ctx, tt.args.record); (err != nil) != tt.wantErr {
				t.Errorf("Adapter.Write() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
func getTestPostgres(t *testing.T) *pgx.Conn {
	prepareDB := []string{
		`DROP TABLE IF EXISTS records;`,
		`CREATE TABLE IF NOT EXISTS records (
		key bytea PRIMARY KEY,
		column1 varchar(256),
		column2 integer,
		column3 boolean);`,
		`INSERT INTO records(key, column1, column2, column3)
		VALUES('1', 'foo', 123, false),
		('2', 'bar', 456, true),
		('3', 'baz', 789, false),
		('4', null, null, null);`,
	}
	db, err := pgx.Connect(context.Background(), DBURL)
	assert.Ok(t, err)
	db = migrate(t, db, prepareDB)
	assert.Ok(t, err)
	return db
}

func migrate(t *testing.T, db *pgx.Conn, migrations []string) *pgx.Conn {
	for _, migration := range migrations {
		_, err := db.Exec(context.Background(), migration)
		assert.Ok(t, err)
	}
	return db
}
