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
	"database/sql"
	"log"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugins"
	"github.com/conduitio/conduit/pkg/record"
)

var (
	// ErrNoRows is returned when there are no rows to read.
	// * This can happen if the database is closed early, if there are no
	// rows in the result set, or if there are no results left to return.
	ErrNoRows = cerrors.Errorf("no more rows")
	// ErrSnapshotInterrupt is return when Teardown or other signal cancels
	// an in-progress snapshot.
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

// Snapshotter must fulfill Iterator
var _ Iterator = (*Snapshotter)(nil)

// NewSnapshotter returns a Snapshotter that is an Iterator.
// * NewSnapshotter attempts to load the sql rows into the Snapshotter and will
// immediately begin to return them to subsequent Read calls.
// * It acquires a read only transaction lock before reading the table.
// * If Teardown is called while a snpashot is in progress, it will return an
// ErrSnapshotInterrupt error.
func NewSnapshotter(
	db *sql.DB,
	table string,
	columns []string,
	key string,
) (*Snapshotter, error) {
	s := &Snapshotter{
		db:               db,
		table:            table,
		columns:          columns,
		key:              key,
		internalPos:      0,
		snapshotComplete: false,
	}
	// load our initial set of rows into the snapshotter after we've set the db
	err := s.loadRows(db)
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

// Next returns the next record in the buffer.
// * If the snapshot is complete it returns an ErrNoRows error
// * If there is no rows property it returns an ErrNoRows error
// * If Next is called after HasNext has returned false, it will
// return an ErrNoRows error.
func (s *Snapshotter) Next() (record.Record, error) {
	if s.snapshotComplete {
		return record.Record{}, ErrNoRows
	}
	// check if rows exists and if it does, check if it has errored.
	if s.rows == nil {
		return record.Record{}, ErrNoRows
	}
	// build our record out and return
	rec := record.Record{}
	rec, err := withPayload(rec, s.rows, s.columns, s.key)
	if err != nil {
		return record.Record{}, cerrors.Errorf("failed to assign payload: %w",
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
	log.Printf("snapshotter attempting graceful teardown")
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
func (s *Snapshotter) loadRows(db *sql.DB) error {
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

// withSnapshot sets up snapshotter on the Source by default unless the
// configuration turns it off. It returns an error if there's an issue during
// setup and nil if the snapshotter has been turned off.
func (s *Source) withSnapshot(cfg plugins.Config) error {
	v, ok := cfg.Settings["snapshot"]
	if ok {
		switch v {
		case "disabled", "false", "off", "0":
			log.Println("snapshot behavior turned off")
			return nil
		}
	}
	snap, err := NewSnapshotter(s.db, s.table, s.columns, s.key)
	if err != nil {
		return cerrors.Errorf("failed to set snapshotter: %w", err)
	}
	s.snapshotter = snap
	log.Println("snapshotter created and ready to start")
	return nil
}

// Push is a noop in the snapshotter case
func (s *Snapshotter) Push(record.Record) {}
