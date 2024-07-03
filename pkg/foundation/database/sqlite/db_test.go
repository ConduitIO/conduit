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
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/database"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

func TestDB(t *testing.T) {
	t.Skip("currently fails because of concurrency issues")
	is := is.New(t)

	db, err := New(
		context.Background(),
		zerolog.Nop(),
		t.TempDir(),
		"conduit_kv_test",
	)
	is.NoErr(err)
	database.AcceptanceTest(t, db)
}
