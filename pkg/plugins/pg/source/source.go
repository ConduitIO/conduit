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

package source

import (
	"context"
	"database/sql"
	"log"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/multierror"
	"github.com/conduitio/conduit/pkg/plugins"
	"github.com/conduitio/conduit/pkg/record"

	"github.com/batchcorp/pgoutput"
)

var (
	// ErrNotFound is returned when no record is returned at a given Position
	ErrNotFound = cerrors.New("record not found")
	// ErrNoTable is returned when no table is configured for reading
	ErrNoTable = cerrors.New("must provide a table name")
	// ErrInvalidURL is returned when the DB can't be connected to with the
	// provided URL
	ErrInvalidURL = cerrors.New("incorrect url")
	// ErrEmptyConfig is returned when no config is provided
	ErrEmptyConfig = cerrors.Errorf("must provide a plugin config")
)

// required is a set of our plugin defaults for the purposes of validation
var required = []string{"url", "table"}

// Source holds a connection to the database.
type Source struct {
	// subWG holds a WaitGroup used for Postgres subscription orchestration
	subWG sync.WaitGroup
	// inherit from Mutex so we can gain locks on our Source.
	sync.Mutex
	// holds a reference to our base Postgres database for Read queries.
	db *sql.DB
	// the table name being read
	table string
	// the columns columns that will be returned
	columns []string
	// key is the row that is being used to create the record.Key
	key string
	// sub holds a reference to the configured cdc subscription
	sub *pgoutput.Subscription
	// subErr is the return value of the `sub.Start` function in withCDC
	subErr error
	// cdc holds a reference to our CDCIterator so that we can read
	// from the cdc when rows are no longer available.
	cdc Iterator
	// snapshotter takes an Iterator of a snapshot of the database
	snapshotter Iterator
	// killswitch holds a context cancel that hooks into the postgres logical
	// replication subscription.
	killswitch context.CancelFunc
}

// Open attempts to open a database connection to Postgres. We use the `with`
// pattern here to mutate the Source struct at each point.
func (s *Source) Open(ctx context.Context, cfg plugins.Config) error {
	err := s.withTable(cfg)
	if err != nil {
		return cerrors.Errorf("failed to set table: %w", err)
	}
	// set database to read
	err = s.withDB(cfg)
	if err != nil {
		return cerrors.Errorf("failed to set db: %w", err)
	}
	// set column to use as record.Key
	err = s.withKeyColumn(cfg)
	if err != nil {
		return cerrors.Errorf("failed to set key: %w", err)
	}
	// set columns to return in Payload
	err = s.withColumns(cfg)
	if err != nil {
		return cerrors.Errorf("failed to set columns: %w", err)
	}
	err = s.withCDC(cfg)
	if err != nil {
		return cerrors.Errorf("failed to set reader: %w", err)
	}
	err = s.withSnapshot(cfg)
	if err != nil {
		return cerrors.Errorf("failed to set snapshot: %w", err)
	}
	return nil
}

// Teardown hits the killswitch and waits for the database connection and CDC
// subscriptions to close.
func (s *Source) Teardown() error {
	log.Printf("attempting graceful shutdown...")
	var teardownErr, dbErr error
	if s.snapshotter != nil {
		teardownErr = s.snapshotter.Teardown()
	}
	if s.killswitch != nil {
		s.killswitch()
	}
	// wait for PG subscription and DB to close and return any errors.
	s.subWG.Wait()
	// check that db exists before trying to close it.
	if s.db != nil {
		dbErr = s.db.Close()
	}
	return multierror.Append(dbErr, s.subErr, teardownErr)
}

// Validate checks for the existence of all required keys in a config and
// returns an error if any of them are missing.
func (s *Source) Validate(cfg plugins.Config) error {
	if cfg.Settings == nil {
		return ErrEmptyConfig
	}
	for _, k := range required {
		if _, ok := cfg.Settings[k]; !ok {
			return cerrors.Errorf("plugin config missing required field %s", k)
		}
	}
	return nil
}

// Read takes a context and a Position and returns a Record or an error.
// * This currently doesn't respect which Position it is fed.
// * Instead Read will send the next record in the Snapshot over until the
// initial table snapshot is finished.
// * Once the table snapshot is finished, it checks for a CDC buffer and if
// one exists it reads off that buffer.
// * If not CDC buffer exists it will return ErrEndData and switch to long
// polling the database.
// * Position is thus assigned to an anonymous variable to be explicit.
func (s *Source) Read(ctx context.Context, _ record.Position) (record.Record, error) {
	// read from Snapshotter if incomplete.
	// Snapshot completely ignores position so we don't care to even pass it.
	if s.snapshotter != nil {
		// HasNext must be called before Next each time we want a new record.
		if next := s.snapshotter.HasNext(); !next {
			// Teardown after we have no more records
			err := s.snapshotter.Teardown()
			if err != nil {
				return record.Record{}, cerrors.Errorf("snapshot teardown failed: %w", err)
			}
			// assign snapshotter to nil to skip snapshot on next Read
			s.snapshotter = nil
			return record.Record{}, plugins.ErrEndData
		}
		return s.snapshotter.Next()
	}

	// check cdc buffer
	if s.cdc != nil {
		if s.cdc.HasNext() {
			next, err := s.cdc.Next()
			return next, err
		}
	}
	// TODO: implement long polling reader here
	// no buffer present, return err end data.
	return record.Record{}, plugins.ErrEndData
}

func (s *Source) Ack(ctx context.Context, pos record.Position) error {
	return nil // noop because Postgres has no concept of acks
}

// getDefaultKeyColumn handles the query for getting the name of the primary
// key column for a table if one exists.
func getDefaultKeyColumn(db *sql.DB, table string) (string, error) {
	row := db.QueryRow(`
	SELECT column_name
	FROM information_schema.key_column_usage
	WHERE table_name = $1 AND constraint_name LIKE '%_pkey'
	LIMIT 1;
	`, table)
	var colName string
	err := row.Scan(&colName)
	if err != nil {
		return "", cerrors.Errorf("failed to scan row: %w", err)
	}
	if colName == "" {
		return "", cerrors.Errorf("got empty key column")
	}
	return colName, nil
}

// sets an integer position to the correct stringed integer on
func withPosition(rec record.Record, i int64) record.Record {
	rec.Position = record.Position(strconv.FormatInt(i, 10))
	return rec
}

// withMetadata sets the Metadata field on a Record.
// Currently it adds the table name and key column of the Record.
func withMetadata(rec record.Record, table string, key string) record.Record {
	if rec.Metadata == nil {
		rec.Metadata = make(map[string]string)
	}
	rec.Metadata["table"] = table
	rec.Metadata["key"] = key
	return rec
}

// withTimestampNow is used when no column name for records' timestamp
// field is set.
func withTimestampNow(rec record.Record) record.Record {
	rec.CreatedAt = time.Now()
	return rec
}

// withPayload builds a record's payload from *sql.Rows.
func withPayload(rec record.Record, rows *sql.Rows, columns []string, key string) (record.Record, error) {
	// make a new slice of empty interfaces to scan sql values into
	vals := make([]interface{}, len(columns))
	for i := range columns {
		vals[i] = new(sql.RawBytes)
	}

	// get the column types for those rows and record them as well
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return record.Record{}, cerrors.Errorf("failed to get column types: %w", err)
	}

	// attempt to build the payload from the rows
	// scan the row into the interface{} vals we declared earlier
	payload := make(record.StructuredData)
	err = rows.Scan(vals...)
	if err != nil {
		return record.Record{}, cerrors.Errorf("failed to scan: %w", err)
	}

	for i, col := range columns {
		t := colTypes[i]

		// handle and assign the record a Key
		if key == col {
			val := reflect.ValueOf(vals[i]).Elem()
			// TODO: Handle composite keys
			rec.Key = record.StructuredData{
				col: string(val.Bytes()),
			}
		}

		// TODO: Need to add the rest of the types that Postgres can support
		switch t.DatabaseTypeName() {
		case "BYTEA":
			val := string(*(vals[i].(*sql.RawBytes)))
			if val == "" {
				payload[col] = nil
				break
			}
			payload[col] = val
		case "VARCHAR":
			val := string(*(vals[i].(*sql.RawBytes)))
			if val == "" {
				payload[col] = nil
				break
			}
			payload[col] = val
		case "INT4":
			val := string(*(vals[i].(*sql.RawBytes)))
			if val == "" {
				payload[col] = nil
				break
			}
			i, err := strconv.ParseInt(val, 10, 16)
			if err != nil {
				return record.Record{}, cerrors.Errorf("failed to parse int: %w", err)
			}
			payload[col] = i
		case "INT8":
			val := string(*(vals[i].(*sql.RawBytes)))
			if val == "" {
				payload[col] = nil
				break
			}
			i, err := strconv.ParseInt(val, 10, 16)
			if err != nil {
				return record.Record{}, cerrors.Errorf("failed to parse int: %w", err)
			}
			payload[col] = i
		case "BOOL":
			val := string(*(vals[i].(*sql.RawBytes)))
			if val == "" {
				payload[col] = nil
				break
			}
			b, err := strconv.ParseBool(val)
			if err != nil {
				return record.Record{}, cerrors.Errorf("failed to parse boolean: %w", err)
			}
			payload[col] = b
		default:
			log.Printf("failed to handle type: %s", t.DatabaseTypeName())
			payload[col] = nil
		}
	}

	rec.Payload = payload
	return rec, nil
}
