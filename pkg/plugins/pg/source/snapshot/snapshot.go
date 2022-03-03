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

package snapshot

import (
	"context"
	"database/sql"
	"reflect"
	"strconv"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin/sdk"

	sq "github.com/Masterminds/squirrel"
)

// Declare Postgres $ placeholder format
var psql = sq.StatementBuilder.PlaceholderFormat(sq.Dollar)

var (
	// ErrNoRows is returned when there are no rows to read.
	// * This can happen if the database is closed early, if there are no
	// rows in the result set, or if there are no results left to return.
	ErrNoRows = cerrors.Errorf("no more rows")
	// ErrSnapshotInterrupt is returned when Teardown or any other signal
	// cancels an in-progress snapshot.
	ErrSnapshotInterrupt = cerrors.Errorf("interrupted snapshot")
)

// Snapshotter implements the Iterator interface for capturing an initial table
// snapshot.
type Snapshotter struct {
	// table is the table to snapshot
	table string
	// key is the name of the key column for the table snapshot
	key string
	// list of columns that snapshotter should record
	columns []string
	// db handle to postgres
	db *sql.DB
	// rows holds a reference to the postgres connection. this can be nil so
	// we must always call loadRows before HasNext or Next.
	rows *sql.Rows
	// ineternalPos is an internal integer Position for the Snapshotter to
	// to return at each Read call.
	internalPos int64
	// snapshotComplete keeps an internal record of whether the snapshot is
	// complete yet
	snapshotComplete bool
}

// NewSnapshotter returns a Snapshotter that is an Iterator.
// * NewSnapshotter attempts to load the sql rows into the Snapshotter and will
// immediately begin to return them to subsequent Read calls.
// * It acquires a read only transaction lock before reading the table.
// * If Teardown is called while a snpashot is in progress, it will return an
// ErrSnapshotInterrupt error.
func NewSnapshotter(db *sql.DB, table string, columns []string, key string) (*Snapshotter, error) {
	s := &Snapshotter{
		db:               db,
		table:            table,
		columns:          columns,
		key:              key,
		internalPos:      0,
		snapshotComplete: false,
	}
	// load our initial set of rows into the snapshotter after we've set the db
	err := s.loadRows()
	if err != nil {
		return nil, cerrors.Errorf("failed to get rows for snapshot: %w", err)
	}
	return s, nil
}

// HasNext returns whether s.rows has another row.
// * It must be called before Snapshotter#Next is or else it will fail.
// * It increments the interal position if another row exists.
// * If HasNext is called and no rows are available, it will mark the snapshot
// as complete and then returns.
func (s *Snapshotter) HasNext() bool {
	next := s.rows.Next()
	if !next {
		s.snapshotComplete = true
		return next
	}
	s.internalPos++
	return next
}

// Next returns the next row in the snapshotter's rows.
// * If Next is called after HasNext has returned false, it will
// return an ErrNoRows error.
func (s *Snapshotter) Next(ctx context.Context) (sdk.Record, error) {
	if s.snapshotComplete {
		return sdk.Record{}, ErrNoRows
	}
	if s.rows == nil {
		return sdk.Record{}, ErrNoRows
	}
	rec := sdk.Record{}
	rec, err := withPayload(rec, s.rows, s.columns, s.key)
	if err != nil {
		return sdk.Record{}, cerrors.Errorf("failed to assign payload: %w",
			err)
	}
	rec = withMetadata(rec, s.table, s.key)
	rec = withTimestampNow(rec)
	rec = withPosition(rec, s.internalPos)
	return rec, nil
}

// Teardown cleans up the database snapshotter by committing and closing the
// connection to sql.Rows
// * If the snapshot is not complete yet, it will return an ErrSnpashotInterrupt
// * Teardown must be called by the caller, it will not automatically be called
// when the snapshot is completed.
// * Teardown handles all of its manual cleanup first then calls cancel to
// stop any unhandled contexts that we've received.
func (s *Snapshotter) Teardown() error {
	// throw interrupt error if we're not finished with snapshot
	var interruptErr error
	if !s.snapshotComplete {
		interruptErr = ErrSnapshotInterrupt
	}
	closeErr := s.rows.Close()
	if closeErr != nil {
		return cerrors.Errorf("failed to close rows: %w", closeErr)
	}
	rowsErr := s.rows.Err()
	if rowsErr != nil {
		return cerrors.Errorf("rows error: %w", rowsErr)
	}
	return interruptErr
}

// loadRows loads the rows returned from the database onto the snapshotter
// or returns an error.
// * It returns nil if no error was detected.
// * rows.Close and rows.Err are called at Teardown.
func (s *Snapshotter) loadRows() error {
	query, args, err := psql.Select(s.columns...).From(s.table).ToSql()
	if err != nil {
		return cerrors.Errorf("failed to create read query: %w", err)
	}
	//nolint:sqlclosecheck,rowserrcheck //both are called at teardown
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return cerrors.Errorf("failed to query context: %w", err)
	}
	s.rows = rows
	return nil
}

// Push is a noop in the snapshotter case
func (s *Snapshotter) Push(sdk.Record) {}

// sets an integer position to the correct stringed integer on
func withPosition(rec sdk.Record, i int64) sdk.Record {
	rec.Position = sdk.Position(strconv.FormatInt(i, 10))
	return rec
}

// withMetadata sets the Metadata field on a Record.
// Currently it adds the table name and key column of the Record.
func withMetadata(rec sdk.Record, table string, key string) sdk.Record {
	if rec.Metadata == nil {
		rec.Metadata = make(map[string]string)
	}
	rec.Metadata["table"] = table
	rec.Metadata["key"] = key
	return rec
}

// withTimestampNow is used when no column name for records' timestamp
// field is set.
func withTimestampNow(rec sdk.Record) sdk.Record {
	rec.CreatedAt = time.Now()
	return rec
}

// withPayload builds a record's payload from *sql.Rows.
func withPayload(rec sdk.Record, rows *sql.Rows, columns []string, key string) (sdk.Record, error) {
	// TODO: These next three blocks should be refactored out into smaller functions

	// BLOCK 1
	// make a new slice of empty interfaces to scan sql values into
	vals := make([]interface{}, len(columns))
	for i := range columns {
		vals[i] = new(sql.RawBytes)
	}

	// BLOCK 2
	// get the column types for those rows and record them as well
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return sdk.Record{}, cerrors.Errorf("failed to get column types: %w", err)
	}

	// BLOCK 3
	// attempt to build the payload from the rows
	// scan the row into the interface{} vals we declared earlier
	payload := make(sdk.StructuredData)
	err = rows.Scan(vals...)
	if err != nil {
		return sdk.Record{}, cerrors.Errorf("failed to scan: %w", err)
	}

	// BLOCK 3 or 4, argument could be made this is related to payload serialization
	for i, col := range columns {
		t := colTypes[i]

		// handle and assign the record a Key
		if key == col {
			val := reflect.ValueOf(vals[i]).Elem()
			// TODO: Handle composite keys
			rec.Key = sdk.StructuredData{
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
				return sdk.Record{}, cerrors.Errorf("failed to parse int: %w", err)
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
				return sdk.Record{}, cerrors.Errorf("failed to parse int: %w", err)
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
				return sdk.Record{}, cerrors.Errorf("failed to parse boolean: %w", err)
			}
			payload[col] = b
		default:
			sdk.Logger(context.Background()).Err(cerrors.Errorf("failed to handle type %T", t.DatabaseTypeName()))
			payload[col] = nil
		}
	}

	rec.Payload = payload
	return rec, nil
}
