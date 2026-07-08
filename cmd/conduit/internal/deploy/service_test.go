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

package deploy

import (
	"context"
	"os"
	"testing"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

// NewLocalService is default-deny: it constructs a standalone PlanApplier ONLY
// for a BadgerDB store, whose exclusive store-directory lock is the sole
// structural guarantee that a concurrent live `conduit run` makes OpenStore fail
// closed. Every other store type (SQLite in WAL mode, Postgres, InMemory) allows
// concurrent multi-process access — or a private empty store — so a standalone
// apply against them could silently mutate a pipeline a live server is running
// (an Invariant-7 / AC-13 violation). Those must be refused with
// CodeStandaloneUnsafe, not allowed. This is the regression test for that gate.
func TestNewLocalService_RefusesNonBadgerStores(t *testing.T) {
	for _, dbType := range []string{
		conduit.DBTypeSQLite,   // WAL mode → concurrent multi-process, NOT fail-closed
		conduit.DBTypePostgres, // no store lock at all
		conduit.DBTypeInMemory, // private per-process store — can't see the live server's state
	} {
		t.Run(dbType, func(t *testing.T) {
			is := is.New(t)

			var cfg conduit.Config
			cfg.DB.Type = dbType

			applier, cleanup, err := NewLocalService(context.Background(), cfg)
			is.True(err != nil)     // refused
			is.True(applier == nil) // no PlanApplier constructed
			is.True(cleanup != nil) // cleanup is always non-nil (safe to defer)
			is.NoErr(cleanup())     // ...and a no-op on the refusal path

			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.Equal(ce.Code, CodeStandaloneUnsafe)
		})
	}
}

// A BadgerDB store — the default, and the one store type with a real exclusive
// lock — is allowed: NewLocalService constructs a working PlanApplier.
func TestNewLocalService_AllowsBadger(t *testing.T) {
	is := is.New(t)

	cfg := conduit.DefaultConfigWithBasePath(t.TempDir())
	is.Equal(cfg.DB.Type, conduit.DBTypeBadger) // sanity: default is Badger
	// A non-default pipelines/connectors/processors path must exist to pass
	// config validation.
	for _, p := range []string{cfg.Pipelines.Path, cfg.Connectors.Path, cfg.Processors.Path} {
		is.NoErr(os.MkdirAll(p, 0o755))
	}

	applier, cleanup, err := NewLocalService(context.Background(), cfg)
	is.NoErr(err)
	is.True(applier != nil)
	is.True(cleanup != nil)
	t.Cleanup(func() { _ = cleanup() })
}
