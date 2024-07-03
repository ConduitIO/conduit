// Copyright © 2024 Meroxa, Inc.
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

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/foundation/database"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/rs/zerolog"
	_ "modernc.org/sqlite"
)

type DB struct {
	db     *sql.DB
	logger log.CtxLogger
	table  string
}

var _ database.DB = (*DB)(nil)

// New initializes the database structures needed by DB.
func New(ctx context.Context, l zerolog.Logger, path, table string) (*DB, error) {
	dbpath, err := dburl(path)
	if err != nil {
		return nil, cerrors.Errorf("failed to construct db path: %w", err)
	}
	fmt.Println("path", dbpath)

	db, err := sql.Open("sqlite", dbpath)
	if err != nil {
		return nil, cerrors.Errorf("failed to open database: %w", err)
	}

	query := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %q (
			key TEXT CHECK(key != "") NOT NULL PRIMARY KEY,
			value BLOB
		)`,
		table,
	)
	if _, err := db.ExecContext(ctx, query); err != nil {
		return nil, cerrors.Errorf("failed to init database: %w", err)
	}

	return &DB{
		logger: log.New(l.With().Str(log.ComponentField, "sqlite3.DB").Logger()),
		db:     db,
		table:  table,
	}, nil
}

// NewTransaction starts a new transaction. The `update` parameter controls the
// access mode ("read only" or "read write"). It returns the transaction as well
// as a context that contains the transaction. Passing the context in future
// calls to *DB will execute that action in that transaction.
func (d *DB) NewTransaction(ctx context.Context, update bool) (database.Transaction, context.Context, error) {
	tx, err := d.db.BeginTx(ctx, &sql.TxOptions{
		ReadOnly: !update,
	})
	if err != nil {
		return nil, nil, cerrors.Errorf("failed to start transaction: %w", err)
	}

	txn := &Transaction{
		tx:     tx,
		logger: d.logger,
		ctx:    ctx,
	}

	return txn, ctxutil.ContextWithTransaction(ctx, txn), nil
}

// Close closes all open connections.
func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) Ping(ctx context.Context) error {
	return d.db.Ping()
}

// Set will store the value under the key. If value is `nil` we consider that a
// delete.
func (d *DB) Set(ctx context.Context, key string, v []byte) error {
	if v == nil {
		return d.delete(ctx, key)
	}
	return d.upsert(ctx, key, v)
}

func (d *DB) Get(ctx context.Context, key string) ([]byte, error) {
	var v []byte

	query := fmt.Sprintf("SELECT value FROM %q WHERE key = $1 LIMIT 1", d.table)
	if err := d.getQuerier(ctx).QueryRowContext(
		ctx, query, key,
	).Scan(&v); err != nil {
		if cerrors.Is(err, sql.ErrNoRows) {
			return v, database.ErrKeyNotExist
		}
		return v, cerrors.Errorf("failed to get key %q: %w", key, err)
	}

	return v, nil
}

func (d *DB) GetKeys(ctx context.Context, prefix string) ([]string, error) {
	var v string

	query := "SELECT ifnull(group_concat(key), '') AS keys FROM %q WHERE key LIKE '%s%%'"
	if err := d.getQuerier(ctx).QueryRowContext(
		ctx,
		fmt.Sprintf(query, d.table, prefix),
	).Scan(&v); err != nil {
		return nil, cerrors.Errorf("failed to get keys with prefix %q: %w", prefix, err)
	}

	if v == "" {
		return nil, nil
	}

	return strings.Split(v, ","), nil
}

func (d *DB) upsert(ctx context.Context, key string, v []byte) error {
	query := fmt.Sprintf(`
		INSERT INTO %q (key, value) VALUES ($1, $2) ON CONFLICT(key)
			DO UPDATE SET value = $2`,
		d.table,
	)
	res, err := d.getQuerier(ctx).ExecContext(ctx, query, key, v)
	if err != nil {
		return cerrors.Errorf("failed to set %q: %w", key, err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return cerrors.Errorf("failed to retrieve affected rows: %w", err)
	}
	if n != 1 {
		return cerrors.Errorf("unexpected write result: %d", n)
	}

	return nil
}

func (d *DB) delete(ctx context.Context, key string) error {
	query := fmt.Sprintf("DELETE FROM %q WHERE key = $1", d.table)
	res, err := d.getQuerier(ctx).ExecContext(ctx, query, key)
	if err != nil {
		return cerrors.Errorf("failed to delete %q: %w", key, err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return cerrors.Errorf("failed to retrieve affected rows: %w", err)
	}

	if n == 0 {
		d.logger.Warn(ctx).Str("key", key).Msg("zero rows deleted")
	}

	return nil
}

type querier interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// getQuerier tries to take the transaction out of the context, if it does not
// find a transaction it falls back directly to the sqlite connection.
func (d *DB) getQuerier(ctx context.Context) querier {
	txn := d.getTxn(ctx)
	if txn != nil {
		return txn
	}
	return d.db
}

// getTxn takes the transaction out of the context and returns it. If the
// context does not contain a transaction it returns nil.
func (d *DB) getTxn(ctx context.Context) *sql.Tx {
	txn := ctxutil.TransactionFromContext(ctx)
	if txn == nil {
		return nil
	}
	return txn.(*Transaction).tx
}

func dburl(path string) (string, error) {
	v := url.Values{}
	v.Add("_pragma", "journal_mode(WAL)")
	v.Add("_pragma", "synchronous(NORMAL)")
	v.Add("_pragma", "temp_store(MEMORY)")

	abspath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(abspath, 0o750); err != nil {
		return "", err
	}

	u := url.URL{
		Scheme:   "file",
		Path:     filepath.Join(abspath, "conduit.db"),
		RawQuery: v.Encode(),
	}

	return u.String(), nil
}
