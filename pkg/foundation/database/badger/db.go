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

package badger

import (
	"context"
	"github.com/google/uuid"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/foundation/database"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/dgraph-io/badger/v3"
	"github.com/rs/zerolog"
)

// DB implements the database.DB interface for server persistence.
type DB struct {
	db *badger.DB
}

var _ database.DB = (*DB)(nil)

// New returns a DB implementation that fulfills the Store interface.
func New(l zerolog.Logger, path string) (*DB, error) {
	opt := badger.DefaultOptions(path)
	opt.Logger = logger(l.With().Str(log.ComponentField, "badger.DB").Logger())

	db, err := badger.Open(opt)
	if err != nil {
		return nil, cerrors.Errorf("badger: could not open db: %w", err)
	}

	return &DB{
		db: db,
	}, nil
}

func (d *DB) Ping(ctx context.Context) error {
	key := "test-" + uuid.NewString()
	defer d.Set(ctx, key, nil)

	return d.Set(ctx, key, []byte{})
}

// NewTransaction starts a new transaction and returns a context that contains
// it. If the context is passed to other functions in this DB the transaction
// will be used for executing the operation.
func (d *DB) NewTransaction(ctx context.Context, update bool) (database.Transaction, context.Context, error) {
	txn := d.db.NewTransaction(update)
	return txn, ctxutil.ContextWithTransaction(ctx, txn), nil
}

// Close flushes any pending writes and closes the db.
func (d *DB) Close() error {
	return d.db.Close()
}

// Get finds the value corresponding to the key and returns it or an error.
// If the key doesn't exist an error is returned.
func (d *DB) Get(ctx context.Context, key string) ([]byte, error) {
	txn := d.getTxn(ctx)
	if txn == nil {
		// create new transaction just for this call
		txn = d.db.NewTransaction(false)
		defer txn.Discard()
	}
	return d.getWithTxn(txn, key)
}

func (d *DB) getWithTxn(txn *badger.Txn, key string) ([]byte, error) {
	item, err := txn.Get([]byte(key))
	if err != nil {
		if err == badger.ErrKeyNotFound {
			err = database.ErrKeyNotExist // translate to internal error
		}
		return nil, cerrors.Errorf("badger: could not get key %q: %w", key, err)
	}

	val, err := item.ValueCopy(nil)
	if err != nil {
		return nil, cerrors.Errorf("badger: could not get value for key %q: %w", key, err)
	}

	return val, nil
}

// Set will update a key with a given value and returns an error if anything went wrong.
func (d *DB) Set(ctx context.Context, key string, value []byte) (err error) {
	txn := d.getTxn(ctx)
	if txn == nil {
		// create new transaction just for this call
		txn = d.db.NewTransaction(true)
		defer func() {
			if err != nil {
				txn.Discard()
				return
			}
			// transaction is committed only if we exit without an error
			err = txn.Commit()
			if err != nil {
				err = cerrors.Errorf("badger: could not commit transaction setting key %q: %w", key, err)
			}
		}()
	}

	return d.setWithTxn(txn, key, value)
}

// setWithTxn will update a key using the provided transaction.
func (d *DB) setWithTxn(txn *badger.Txn, key string, value []byte) error {
	// if we set the value to nothing, we're considering that a delete.
	// if we need to maintain an empty value, it should be an empty byte slice.
	if value == nil {
		err := txn.Delete([]byte(key))
		if err != nil {
			return cerrors.Errorf("badger: could not delete key %q: %w", key, err)
		}
	} else {
		err := txn.Set([]byte(key), value)
		if err != nil {
			return cerrors.Errorf("badger: could not set key %q: %w", key, err)
		}
	}
	return nil
}

// GetKeys will range over DB keys and return the ones with the given prefix.
func (d *DB) GetKeys(ctx context.Context, prefix string) ([]string, error) {
	txn := d.getTxn(ctx)
	if txn == nil {
		// create new transaction just for this call
		txn = d.db.NewTransaction(false)
		defer txn.Discard()
	}
	return d.getKeysWithTxn(txn, prefix)
}

// getKeysWithTxn will return keys with a prefix using the provided transaction.
func (d *DB) getKeysWithTxn(txn *badger.Txn, prefix string) ([]string, error) {
	opt := badger.DefaultIteratorOptions
	opt.Prefix = []byte(prefix)
	opt.PrefetchValues = false // only iterate keys
	it := txn.NewIterator(opt)
	defer it.Close()

	var results []string
	for it.Rewind(); it.Valid(); it.Next() {
		results = append(results, string(it.Item().Key()))
	}

	return results, nil
}

// getTxn takes the transaction out of the context and returns it. If the
// context does not contain a transaction it returns nil.
func (d *DB) getTxn(ctx context.Context) *badger.Txn {
	txn := ctxutil.TransactionFromContext(ctx)
	if txn == nil {
		return nil
	}
	return txn.(*badger.Txn)
}
