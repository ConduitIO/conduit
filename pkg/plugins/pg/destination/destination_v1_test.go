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
		Position                 sdk.Position
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
						"key": "abcasdf",
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Adapter{
				UnimplementedDestination: tt.fields.UnimplementedDestination,
				Position:                 tt.fields.Position,
				conn:                     tt.fields.conn,
				config:                   tt.fields.config,
			}
			if err := d.Write(tt.args.ctx, tt.args.record); (err != nil) != tt.wantErr {
				t.Errorf("Adapter.Write() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// getTestPostgres is a testing helper that fails if it can't setup a Postgres
// connection and returns a DB and the connection string.
// * It starts and migrates a db with 4 rows for Test_Read* and Test_Open*
func getTestPostgres(t *testing.T) *pgx.Conn {
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
	db, err := pgx.Connect(context.Background(), DBURL)
	assert.Ok(t, err)
	db = migrate(t, db, prepareDB)
	assert.Ok(t, err)
	return db
}

// migrate will run a set of migrations on a database to prepare it for a test
// it fails the test if any migrations are not applied.
func migrate(t *testing.T, db *pgx.Conn, migrations []string) *pgx.Conn {
	for _, migration := range migrations {
		_, err := db.Exec(context.Background(), migration)
		assert.Ok(t, err)
	}
	return db
}
