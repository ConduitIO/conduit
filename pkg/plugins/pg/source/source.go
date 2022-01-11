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

	sq "github.com/Masterminds/squirrel"
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
)

// Declare Postgres $ placeholder format
var psql = sq.StatementBuilder.PlaceholderFormat(sq.Dollar)

// bufferSize is the size of the WAL buffer.
var bufferSize = 1000

// Source holds a connection to the database.
type Source struct {
	// subWG holds a WaitGroup used for Postgres subscription orchestration
	subWG sync.WaitGroup
	// killswitch is a context CancelFunc that gets hit at Teardown
	killswitch context.CancelFunc
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
	// buffer holds a reference to our CDCIterator so that we can read
	// from the buffer when rows are no longer available.
	buffer *CDCIterator
	// snapshotter takes an Iterator of a snapshot of the database
	snapshotter Iterator
}

// Open attempts to open a database connection to Postgres. We use the `with`
// pattern here to mutate the Source struct at each point.
func (s *Source) Open(ctx context.Context, cfg plugins.Config) error {
	ctx, s.killswitch = context.WithCancel(ctx)
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
	// if cdc is set, turn on CDC subscription
	if v, ok := cfg.Settings["cdc"]; ok && v == "true" {
		err = s.withCDC(ctx, cfg)
		if err != nil {
			return cerrors.Errorf("failed to set reader: %w", err)
		}
	}
	// set snapshotter if configured and inherit same column and table settings
	if v, ok := cfg.Settings["shapshot"]; ok && v == "true" {
		snap, err := NewSnapshotter(ctx, s.db, s.table, s.columns, s.key)
		if err != nil {
			return cerrors.Errorf("failed to set snapshotter: %w", err)
		}
		s.snapshotter = snap
	}

	return nil
}

// Teardown hits the killswitch and waits for the database connection and CDC
// subscriptions to close.
func (s *Source) Teardown() error {
	if s.killswitch != nil {
		s.killswitch()
	}

	var teardownErr error
	if s.snapshotter != nil {
		teardownErr = s.snapshotter.Teardown()
	}

	// wait for PG subscription and DB to close and return any errors.
	s.subWG.Wait()
	dbErr := s.db.Close()
	return multierror.Append(dbErr, s.subErr, teardownErr)
}

// Validate opens up a connection to the DB to see if it was successful and then
// calls Teardown to drop the DB connection and clean up.
func (s *Source) Validate(cfg plugins.Config) error {
	err := s.Open(context.Background(), cfg)
	if err != nil {
		return cerrors.Errorf("invalid config: %w", err)
	}
	return s.Teardown()
}

// Read attempts to increment the Position and then queries for the row at
// that Position.
// * It builds the payload from the rows and assigns a timestamp, key,
// metadata, payload, and position to the record.
// * Read takes the _current_ position of the connector, and returns the row
// at the _next_ position.
func (s *Source) Read(ctx context.Context, pos record.Position) (record.Record, error) {
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

	// TODO: Pull this code out into its own integer iterator
	i, err := incrementPosition(pos)
	if err != nil {
		return record.Record{}, cerrors.Errorf("failed to increment: %w", err)
	}
	// query row at the incremented position i
	query, args, err := psql.
		Select(s.columns...).
		From(s.table).
		Where(sq.GtOrEq{s.key: i}).
		Limit(1).
		ToSql()
	if err != nil {
		return record.Record{}, cerrors.Errorf("failed to create read query: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return record.Record{}, cerrors.Errorf("failed to query context: %w", err)
	}
	defer rows.Close()
	rec := record.Record{}
	rec, err = s.build(rows, i, rec)
	if err != nil {
		// if we're at the end of rows, then read from the CDCIterator
		if cerrors.Is(err, plugins.ErrEndData) {
			if s.buffer != nil {
				if s.buffer.HasNext() {
					next, err := s.buffer.Next()
					return next, err
				}
			}
			return record.Record{}, plugins.ErrEndData
		}
		return record.Record{}, cerrors.Errorf("failed to build payload: %w", err)
	}
	return rec, nil
}

func (s *Source) Ack(ctx context.Context, pos record.Position) error {
	return nil // noop because Postgres has no concept of acks
}

// turns the record position into a big.Int and increments it
func incrementPosition(pos record.Position) (int64, error) {
	// if position is nil, we assume position is 0 and set the highwater there
	if pos == nil {
		return int64(1), nil
	}
	// we must perform an additional nil check because of how Go handles empty
	// interfaces. we use the String() function and compare to its nil value.
	if pos.String() == "<nil>" {
		return int64(1), nil
	}
	// attempt to parse the int as a string and fail if we can't
	i, err := strconv.ParseInt(string(pos), 10, 64)
	if err != nil {
		return 0, cerrors.Errorf("incrementPosition failed to parse int: %w", err)
	}
	// set the highwater position to i since we don't know if the _next_ record
	// finally, we increment and return
	inc := i + 1
	return inc, nil
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

// build takes the sql.Rows `rows` and a position `i` and builds out the record
// fields on `rec`. It returns a Record or an error.
func (s *Source) build(
	rows *sql.Rows,
	pos int64,
	rec record.Record,
) (record.Record, error) {
	rec = withPosition(rec, pos)
	rec = withMetadata(rec, s.table, s.key)
	rec = withTimestampNow(rec)

	// tick to the next record, return end of data error if none exists.
	cont := rows.Next()
	if !cont {
		// check the rows error after ticking
		if rows.Err() != nil {
			return record.Record{}, cerrors.Errorf("failed to read next record: %w", rows.Err())
		}
		// if there's no error, but no next, that means that we're at the end
		// of the table because our query returned nothing.
		return record.Record{}, plugins.ErrEndData
	}

	// assign the payload
	rec, err := withPayload(rec, rows, s.columns, s.key)
	if err != nil {
		return record.Record{}, cerrors.Errorf("failed to build payload: %w", rows.Err())
	}

	return rec, nil
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
