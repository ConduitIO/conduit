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

// Command gen writes a storecompat fixture: a badger directory or a SQLite
// file holding the fixture state, plus a manifest.json recording the
// storage-library versions linked into this binary.
//
// To be useful, it must be built with the OLD storage libraries. Pin them
// with a temporary modfile so the repository's go.mod is untouched, e.g.
// for the badger v4.9.1 / sqlite v1.38.0 fixtures:
//
//	cp go.mod /tmp/old.mod && cp go.sum /tmp/old.sum
//	go mod edit -modfile=/tmp/old.mod \
//	  -replace github.com/dgraph-io/badger/v4=github.com/dgraph-io/badger/v4@v4.9.1 \
//	  -replace modernc.org/sqlite=modernc.org/sqlite@v1.38.0 \
//	  -replace modernc.org/libc=modernc.org/libc@v1.65.10
//	go mod download -modfile=/tmp/old.mod
//	go run -modfile=/tmp/old.mod ./tests/upgrade/storecompat/internal/gen \
//	  -type badger -out tests/upgrade/storecompat/testdata/badger-v4.9.1
//	go run -modfile=/tmp/old.mod ./tests/upgrade/storecompat/internal/gen \
//	  -type sqlite -out tests/upgrade/storecompat/testdata/sqlite-v1.38.0
//
// The output directory must not exist yet.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-commons/database/badger"
	"github.com/conduitio/conduit-commons/database/sqlite"
	"github.com/conduitio/conduit/tests/upgrade/storecompat"
	"github.com/rs/zerolog"
)

func main() {
	typ := flag.String("type", "", "badger or sqlite")
	out := flag.String("out", "", "fixture directory to create")
	flag.Parse()
	if err := run(*typ, *out); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run(typ, out string) error {
	if out == "" {
		return fmt.Errorf("-out is required")
	}
	if _, err := os.Stat(out); err == nil {
		return fmt.Errorf("%s already exists; fixtures are never overwritten", out)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	ctx := context.Background()

	var db database.DB
	var err error
	switch typ {
	case "badger":
		db, err = badger.New(zerolog.Nop(), filepath.Join(out, "conduit.db"))
	case "sqlite":
		db, err = sqlite.New(ctx, zerolog.Nop(), filepath.Join(out, "conduit.db"), storecompat.SQLiteTable)
	default:
		return fmt.Errorf("unknown -type %q", typ)
	}
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	if err := storecompat.WriteFixture(ctx, db); err != nil {
		_ = db.Close()
		return fmt.Errorf("write fixture: %w", err)
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("close db: %w", err)
	}
	return storecompat.WriteManifest(out)
}
