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

//go:build integration

package postgres

import (
	"context"
	"fmt"
	"github.com/matryer/is"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/database"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

func TestDB(t *testing.T) {
	is := is.New(t)
	const testTable = "conduit_store_test"

	ctx := context.Background()
	logger := log.Nop()
	db, err := New(ctx, logger, "postgres://meroxauser:meroxapass@localhost:5432/meroxadb?sslmode=disable", testTable)
	is.NoErr(err)
	t.Cleanup(func() {
		_, err := db.pool.Exec(ctx, fmt.Sprintf("DROP TABLE %q", testTable))
		is.NoErr(err)
		err = db.Close()
		is.NoErr(err)
	})
	database.AcceptanceTest(t, db)
}
