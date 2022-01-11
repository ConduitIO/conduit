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

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/multierror"
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
	// cancel is used to kill the snapshot's context
	cancel context.CancelFunc
	// tx holds the transaction that is opened to read the table.
	tx *sql.Tx
}

// Snapshotter must fulfill Iterator
var _ Iterator = (*Snapshotter)(nil)

// NewSnapshotter returns a Snapshotter that is an Iterator.
// * NewSnapshotter attempts to load the sql rows into the Snapshotter and will
// immediately begin to return them to subsequent Read calls.
// * It acquires a read only transaction lock before reading the table.
// * If Teardown is called while a snpashot is in progress, it will return an
// ErrSnapshotInterrupt error.
func NewSnapshotter(ctx context.Context,
	db *sql.DB,
	table string,
	columns []string,
	key string,
) (*Snapshotter, error) {
	ctx, cancel := context.WithCancel(ctx)
	s := &Snapshotter{
		db:               db,
		table:            table,
		columns:          columns,
		key:              key,
		internalPos:      0,
		snapshotComplete: false,
		cancel:           cancel,
	}
	// load our initial set of rows into the snapshotter after we've set the db
	err := s.loadRows(ctx, db)
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
	closeErr := s.rows.Close()
	if closeErr != nil {
		return cerrors.Errorf("failed to close rows: %w", closeErr)
	}
	rowsErr := s.rows.Err()
	if rowsErr != nil {
		return cerrors.Errorf("rows error: %w", rowsErr)
	}

	// throw interrupt error if we're not finished with snapshot
	var interruptErr error
	if !s.snapshotComplete {
		interruptErr = ErrSnapshotInterrupt
	}

	// commit our snapshot's read transaction and rollback if it fails.
	if commitErr := s.tx.Commit(); commitErr != nil {
		rollbackErr := s.tx.Rollback()
		if rollbackErr != nil {
			return multierror.Append(rollbackErr, commitErr, interruptErr)
		}
		return multierror.Append(commitErr, interruptErr)
	}
	s.cancel()
	return interruptErr
}

// loadRows should get the rows of the initial query and then load it into the
// Snapshotter which wraps it to the Iterator pattern.
// * Starts a read only transaction for the snapshotter for the configured
// table, key, and columns.
// * When it's done reading, it commits that transaction and marks the
// snapshot as completed.
// * If a context cancellation is detected, it will rollback and return any
// rows errors that were encountered.
func (s *Snapshotter) loadRows(ctx context.Context, db *sql.DB) error {
	// if context is canceled, sql will rollback the transaction and abort
	tx, err := db.BeginTx(ctx, &sql.TxOptions{
		ReadOnly:  true,
		Isolation: sql.LevelReadCommitted,
	})
	if err != nil {
		return cerrors.Errorf("failed to start transaction: %w", err)
	}
	query, args, err := psql.Select(s.columns...).From(s.table).ToSql()
	if err != nil {
		return cerrors.Errorf("failed to create read query: %w", err)
	}
	//nolint:sqlclosecheck,rowserrcheck // NB: both are called in Teardown
	// because we lock the table until we've read every row.
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return cerrors.Errorf("failed to query context: %w", err)
	}
	s.rows = rows
	s.tx = tx
	return nil
}
