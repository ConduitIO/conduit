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

//go:generate mockgen -destination=mock/database.go -package=mock -mock_names=DB=DB . DB

package database

import (
	"context"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

var (
	// ErrKeyNotExist is returned from DB.Get when retrieving a non-existing key.
	ErrKeyNotExist = cerrors.New("key does not exist")
)

// DB defines the interface for a key-value store.
type DB interface {
	// NewTransaction starts and returns a new transaction.
	NewTransaction(ctx context.Context, update bool) (Transaction, context.Context, error)
	// Close should flush any cached writes, release all resources and close the
	// store. After Close is called other methods should return an error.
	Close() error

	Set(ctx context.Context, key string, value []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	GetKeys(ctx context.Context, prefix string) ([]string, error)
}

// Transaction defines the interface for an isolated ACID DB transaction.
type Transaction interface {
	// Commit commits the transaction. If the transaction can't be committed
	// (e.g. read values were changed since the transaction started) it returns
	// an error.
	Commit() error
	// Discard discards a created transaction. This method is very important and
	// must be called. Calling this multiple times or after a Commit call
	// doesn't cause any issues, so this can safely be called via a defer right
	// when transaction is created.
	Discard()
}
