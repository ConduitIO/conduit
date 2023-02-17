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

package inmemory

import (
	"bytes"
	"context"
	"strings"
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/foundation/database"
)

// DB is a naive store that stores values in memory. Do not use in production,
// changes are lost on restart.
type DB struct {
	initOnce sync.Once
	values   map[string][]byte
	m        sync.Mutex
}

var _ database.DB = (*DB)(nil)

type Txn struct {
	db      *DB
	old     map[string][]byte
	changes map[string][]byte
}

func (d *DB) Ping(_ context.Context) error {
	return nil
}

func (t *Txn) Commit() error {
	t.db.m.Lock()
	defer t.db.m.Unlock()

	for k := range t.changes {
		oldVal, oldOk := t.old[k]
		newVal, newOk := t.db.values[k]

		if !bytes.Equal(oldVal, newVal) || oldOk != newOk {
			return cerrors.Errorf("conflict on key %s", k)
		}
	}

	for k, v := range t.changes {
		t.db.values[k] = v
		if v == nil {
			delete(t.db.values, k)
		}
	}
	return nil
}

func (t *Txn) Discard() {
	// do nothing
}

func (d *DB) NewTransaction(ctx context.Context, update bool) (database.Transaction, context.Context, error) {
	d.m.Lock()
	defer d.m.Unlock()

	valuesCopy := make(map[string][]byte, len(d.values))
	for k, v := range d.values {
		valuesCopy[k] = v
	}

	t := &Txn{
		db:      d,
		old:     valuesCopy,
		changes: make(map[string][]byte),
	}
	return t, ctxutil.ContextWithTransaction(ctx, t), nil
}

func (d *DB) Set(ctx context.Context, key string, value []byte) error {
	d.init()
	txn := d.getTxn(ctx)
	if txn != nil {
		txn.changes[key] = value
		return nil
	}

	d.m.Lock()
	defer d.m.Unlock()

	if value != nil {
		d.values[key] = value
	} else {
		delete(d.values, key)
	}
	return nil
}

func (d *DB) Get(ctx context.Context, key string) ([]byte, error) {
	d.init()
	txn := d.getTxn(ctx)
	if txn != nil {
		val, ok := txn.changes[key]
		if ok {
			if val == nil {
				return nil, database.ErrKeyNotExist
			}
			return val, nil
		}
		val, ok = txn.old[key]
		if !ok {
			return nil, database.ErrKeyNotExist
		}
		return val, nil
	}
	val, ok := d.values[key]
	if !ok {
		return nil, database.ErrKeyNotExist
	}
	return val, nil
}

func (d *DB) GetKeys(ctx context.Context, prefix string) ([]string, error) {
	d.init()
	txn := d.getTxn(ctx)
	if txn != nil {
		var result []string
		for k := range txn.old {
			if strings.HasPrefix(k, prefix) {
				result = append(result, k)
			}
		}
		for k, v := range txn.changes {
			if strings.HasPrefix(k, prefix) {
				if v == nil {
					// it's a delete, remove the key
					for i, rk := range result {
						if rk == k {
							result = append(result[:i], result[i+1:]...)
							break
						}
					}
				} else {
					result = append(result, k)
				}
			}
		}
		return result, nil
	}
	var result []string
	for k := range d.values {
		if strings.HasPrefix(k, prefix) {
			result = append(result, k)
		}
	}
	return result, nil
}

// init lazily initializes the values map.
func (d *DB) init() {
	d.initOnce.Do(func() {
		d.values = make(map[string][]byte)
	})
}

// Close is a noop.
func (d *DB) Close() error {
	return nil
}

func (d *DB) getTxn(ctx context.Context) *Txn {
	t := ctxutil.TransactionFromContext(ctx)
	if t == nil {
		return nil
	}
	return t.(*Txn)
}
