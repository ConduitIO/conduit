// Copyright © 2026 Meroxa, Inc.
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

package storecompat

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-commons/database/badger"
	"github.com/conduitio/conduit-commons/database/sqlite"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

type fixture struct {
	dir string
	// writtenBy pins the library version the fixture must have been
	// written with; a regenerated fixture from the wrong build fails here.
	module, writtenBy string
	open              func(ctx context.Context, dir string) (database.DB, error)
}

var fixtures = []fixture{
	{
		dir:       "testdata/badger-v4.9.1",
		module:    "github.com/dgraph-io/badger/v4",
		writtenBy: "v4.9.1",
		open: func(_ context.Context, dir string) (database.DB, error) {
			return badger.New(zerolog.Nop(), filepath.Join(dir, "conduit.db"))
		},
	},
	{
		dir:       "testdata/sqlite-v1.38.0",
		module:    "modernc.org/sqlite",
		writtenBy: "v1.38.0",
		open: func(ctx context.Context, dir string) (database.DB, error) {
			return sqlite.New(ctx, zerolog.Nop(), filepath.Join(dir, "conduit.db"), SQLiteTable)
		},
	},
}

// TestStoreCompat_OldFixtureReopensAndResumes opens a database written by
// an older storage-library version, checks every instance reads back
// exactly, then resumes: a newer source position is persisted the way a
// running source persists it, the database is closed and reopened, and the
// newer position must be the one read back (Invariant 2: positions survive
// a restart and never move backwards).
func TestStoreCompat_OldFixtureReopensAndResumes(t *testing.T) {
	for _, f := range fixtures {
		t.Run(filepath.Base(f.dir), func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()

			m, err := ReadManifest(f.dir)
			is.NoErr(err)
			if got := m.Modules[f.module]; got != f.writtenBy {
				t.Fatalf("fixture %s was written by %s %s, want %s; regenerate it with the old library pinned (see internal/gen)",
					f.dir, f.module, got, f.writtenBy)
			}
			t.Logf("fixture written by %s %s, reading with %s", f.module, f.writtenBy, GoListVersion(ctx, f.module))

			dir := CopyFixture(t, f.dir)

			db, err := f.open(ctx, dir)
			is.NoErr(err)
			AssertState(t, ctx, db, SourcePosition)

			// Resume: persist the next position through the runtime's path.
			conns := ExpectedConnectors(ResumedPosition)
			src := conns[SourceID]
			is.NoErr(PersistConnectors(ctx, db, log.Nop(), map[string]*connector.Instance{src.ID: src}))
			is.NoErr(db.Close())

			db, err = f.open(ctx, dir)
			is.NoErr(err)
			t.Cleanup(func() { _ = db.Close() })
			AssertState(t, ctx, db, ResumedPosition)
		})
	}
}
