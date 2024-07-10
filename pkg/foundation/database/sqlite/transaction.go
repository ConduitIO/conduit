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

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

type Transaction struct {
	ctx    context.Context
	tx     *sql.Tx
	logger log.CtxLogger
}

var _ database.Transaction = (*Transaction)(nil)

func (t *Transaction) Commit() error {
	return t.tx.Commit()
}

func (t *Transaction) Discard() {
	if err := t.tx.Rollback(); err != nil {
		if !cerrors.Is(err, sql.ErrTxDone) {
			t.logger.Error(t.ctx).Err(err).Msg("could not discard tx")
		}
	}
}
