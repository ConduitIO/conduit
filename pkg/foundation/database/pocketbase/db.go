// Copyright © 2023 Meroxa, Inc.
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

package pocketbase

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/foundation/database"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
)

type DB struct {
	logger log.CtxLogger
	db     *sql.DB
	table  string
}

func New(ctx context.Context, logger log.CtxLogger, table string, db *dbx.DB) (*DB, error) {
	d := &DB{
		db:     db.DB(),
		logger: logger,
		table:  table,
	}
	err := d.init(ctx)
	if err != nil {
		return nil, err
	}
	return d, nil
}

func NewFromPocketBase(ctx context.Context, logger log.CtxLogger, table string, pb *pocketbase.PocketBase) (*DB, error) {
	db, ok := pb.Dao().DB().(*dbx.DB)
	if !ok {
		return nil, cerrors.Errorf("could not extract DB from pocketbase: expected *dbx.DB, got %T", pb.Dao().DB())
	}
	return New(ctx, logger, table, db)
}

func NewFromPocketBaseConfig(ctx context.Context, logger log.CtxLogger, table string, cfg *pocketbase.Config) (*DB, error) {
	pb := pocketbase.NewWithConfig(cfg)
	err := pb.Bootstrap()
	if err != nil {
		return nil, err
	}
	return NewFromPocketBase(ctx, logger, table, pb)
}

// Init initializes the database structures needed by DB.
func (d *DB) init(ctx context.Context) error {
	query := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %q (
			key   VARCHAR PRIMARY KEY,
			value BYTEA
		)`, d.table)
	_, err := d.db.ExecContext(ctx, query)
	if err != nil {
		return cerrors.Errorf("could not create table %q: %w", d.table, err)
	}
	return nil
}

func (d *DB) NewTransaction(ctx context.Context, update bool) (database.Transaction, context.Context, error) {
	tx, err := d.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: !update})
	if err != nil {
		return nil, ctx, cerrors.Errorf("could not begin transaction: %w", err)
	}

	txn := &Txn{
		ctx:    ctx,
		logger: d.logger,
		tx:     tx,
	}
	return txn, ctxutil.ContextWithTransaction(ctx, txn), nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) Ping(_ context.Context) error {
	return d.db.Ping()
}

// Set will store the value under the key. If value is `nil` we consider that a
// delete.
func (d *DB) Set(ctx context.Context, key string, value []byte) error {
	switch value {
	case nil:
		return d.delete(ctx, key)
	default:
		return d.upsert(ctx, key, value)
	}
}

func (d *DB) delete(ctx context.Context, key string) error {
	query := fmt.Sprintf(`DELETE FROM %q WHERE key = $1`, d.table)
	_, err := d.getQuerier(ctx).ExecContext(ctx, query, key)
	if err != nil {
		return cerrors.Errorf("could not delete key %q: %w", key, err)
	}
	return nil
}

func (d *DB) upsert(ctx context.Context, key string, value []byte) error {
	query := fmt.Sprintf(`
		INSERT INTO %q (key, value)
		VALUES ($1, $2)
		ON CONFLICT (key)
		DO UPDATE SET value = EXCLUDED.value`, d.table)
	_, err := d.getQuerier(ctx).ExecContext(ctx, query, key, value)
	if err != nil {
		return cerrors.Errorf("could not upsert key %q: %w", key, err)
	}
	return nil
}

// Get returns the value stored under the key. If no value is found it returns
// database.ErrKeyNotExist.
func (d *DB) Get(ctx context.Context, key string) ([]byte, error) {
	query := fmt.Sprintf(`SELECT value FROM %q WHERE key = $1`, d.table)
	row := d.getQuerier(ctx).QueryRowContext(ctx, query, key)

	var value []byte
	err := row.Scan(&value)
	if cerrors.Is(err, sql.ErrNoRows) {
		// translate error
		err = database.ErrKeyNotExist
	}
	if err != nil {
		return nil, cerrors.Errorf("could not select key %q: %w", key, err)
	}
	return value, nil
}

// GetKeys returns all keys with the requested prefix. If prefix is an empty
// string it will return all keys.
func (d *DB) GetKeys(ctx context.Context, prefix string) ([]string, error) {
	query := fmt.Sprintf(`SELECT key FROM %q`, d.table)
	var args []interface{}
	if prefix != "" {
		query += " WHERE key LIKE $1"
		args = append(args, prefix+"%")
	}
	rows, err := d.getQuerier(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, cerrors.Errorf("could not select keys with prefix %q: %w", prefix, err)
	}
	defer rows.Close()

	var values []string
	for rows.Next() {
		var value string
		err := rows.Scan(&value)
		if err != nil {
			return nil, cerrors.Errorf("could not scan value: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, cerrors.Errorf("rows returned an error: %w", err)
	}

	return values, nil
}

// getQuerier tries to take the transaction out of the context, if it does not
// find a transaction it falls back directly to the postgres connection.
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
	return txn.(*Txn).tx
}

type querier interface {
	ExecContext(ctx context.Context, sql string, arguments ...interface{}) (sql.Result, error)
	QueryContext(ctx context.Context, sql string, optionsAndArgs ...interface{}) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, sql string, optionsAndArgs ...interface{}) *sql.Row
}
