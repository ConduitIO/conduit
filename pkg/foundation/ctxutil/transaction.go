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

package ctxutil

import (
	"context"

	"github.com/conduitio/conduit-commons/database"
)

// transactionCtxKey is used as the key when saving the transaction in a context.
type transactionCtxKey struct{}

// ContextWithTransaction wraps ctx and returns a context that contains transaction.
func ContextWithTransaction(ctx context.Context, transaction database.Transaction) context.Context {
	return context.WithValue(ctx, transactionCtxKey{}, transaction)
}

// TransactionFromContext fetches the transaction from the context. If the
// context does not contain a transaction it returns nil.
func TransactionFromContext(ctx context.Context) database.Transaction {
	transaction := ctx.Value(transactionCtxKey{})
	if transaction != nil {
		return transaction.(database.Transaction)
	}
	return nil
}
