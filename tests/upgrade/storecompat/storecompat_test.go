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
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-commons/database/badger"
	"github.com/conduitio/conduit-commons/database/sqlite"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
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
			t.Logf("fixture written by %s %s, reading with %s", f.module, f.writtenBy, selectedVersion(ctx, f.module))

			dir := copyFixture(t, f.dir)

			db, err := f.open(ctx, dir)
			is.NoErr(err)
			assertState(t, ctx, db, sourcePosition)

			// Resume: persist the next position through the runtime's path.
			conns := expectedConnectors(resumedPosition)
			src := conns[sourceID]
			is.NoErr(persistConnectors(ctx, db, log.Nop(), map[string]*connector.Instance{src.ID: src}))
			is.NoErr(db.Close())

			db, err = f.open(ctx, dir)
			is.NoErr(err)
			t.Cleanup(func() { _ = db.Close() })
			assertState(t, ctx, db, resumedPosition)
		})
	}
}

// assertState checks that db holds exactly the fixture state, with srcPos
// as the source connector's position.
func assertState(t *testing.T, ctx context.Context, db database.DB, srcPos opencdc.Position) {
	t.Helper()
	is := is.New(t)

	pipelines, err := pipeline.NewStore(db).GetAll(ctx)
	is.NoErr(err)
	is.Equal(len(pipelines), len(expectedPipelines()))
	for id, want := range expectedPipelines() {
		got, ok := pipelines[id]
		if !ok {
			t.Fatalf("pipeline %q missing after reopen", id)
		}
		is.Equal(got.GetStatus(), want.GetStatus())
		is.Equal(got, want)
	}

	connectors, err := connector.NewStore(db, log.Nop()).GetAll(ctx)
	is.NoErr(err)
	wantConns := expectedConnectors(srcPos)
	is.Equal(len(connectors), len(wantConns))
	for id, want := range wantConns {
		got, ok := connectors[id]
		if !ok {
			t.Fatalf("connector %q missing after reopen", id)
		}
		is.Equal(got, want)
	}

	processors, err := processor.NewStore(db).GetAll(ctx)
	is.NoErr(err)
	is.Equal(len(processors), len(expectedProcessors()))
	for id, want := range expectedProcessors() {
		got, ok := processors[id]
		if !ok {
			t.Fatalf("processor %q missing after reopen", id)
		}
		is.Equal(got, want)
	}
}

// selectedVersion reports the version of module this build selects, for
// the test log only. Test binaries carry no dependency build info, so ask
// the go command; an error is reported in place of the version.
func selectedVersion(ctx context.Context, module string) string {
	out, err := exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}", module).Output()
	if err != nil {
		return "unknown (" + err.Error() + ")"
	}
	return strings.TrimSpace(string(out))
}

// copyFixture copies a fixture directory into a temp dir so the committed
// files are never opened for writing.
func copyFixture(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600)
	})
	if err != nil {
		t.Fatalf("copy fixture %s: %v", src, err)
	}
	return dst
}
