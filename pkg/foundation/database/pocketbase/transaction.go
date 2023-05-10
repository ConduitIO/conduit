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

	"github.com/conduitio/conduit/pkg/foundation/cerrors"

	"github.com/conduitio/conduit/pkg/foundation/log"
)

type Txn struct {
	ctx    context.Context
	logger log.CtxLogger
	tx     *sql.Tx
}

func (t *Txn) Commit() error {
	return t.tx.Commit()
}

func (t *Txn) Discard() {
	err := t.tx.Rollback()
	if err != nil && !cerrors.Is(err, sql.ErrTxDone) {
		t.logger.Err(t.ctx, err).Msg("could not rollback transaction")
	}
}
