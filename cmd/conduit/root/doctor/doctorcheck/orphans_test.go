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

package doctorcheck_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/root/doctor/doctorcheck"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/conduit/check"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/matryer/is"
)

// noPlugins fails every dispense, so a started connector's delete hook
// can't run and ForceDelete records an orphan.
type noPlugins struct{}

func (noPlugins) NewDispenser(log.CtxLogger, string, string) (connectorPlugin.Dispenser, error) {
	return nil, cerrors.New(`plugin "builtin:postgres" not found`)
}

// forceDeleteWithFailingHook writes one orphan entry into the store at cfg,
// the way a forced delete through the API does, and closes the store.
func forceDeleteWithFailingHook(t *testing.T, cfg conduit.Config, id string) {
	t.Helper()
	is := is.New(t)
	ctx := context.Background()
	db, err := conduit.OpenStore(ctx, cfg, log.Nop())
	is.NoErr(err)
	defer func() { is.NoErr(db.Close()) }()

	svc := connector.NewService(log.Nop(), db, nil)
	conn, err := svc.Create(ctx, id, connector.TypeSource, "builtin:postgres", "orders-pipeline",
		connector.Config{Name: "orders", Settings: map[string]string{"slot.name": "conduit_orders"}}, connector.ProvisionTypeAPI)
	is.NoErr(err)
	conn.LastActiveConfig = conn.Config
	orphan, err := svc.ForceDelete(ctx, id, noPlugins{})
	is.NoErr(err)
	is.True(orphan != nil)
}

// TestOrphanedResources_ReportsForcedDeletes: an orphan entry left by a
// forced delete is a warning (never a failure) naming the connector and
// how to clear it.
func TestOrphanedResources_ReportsForcedDeletes(t *testing.T) {
	is := is.New(t)
	cfg := validConfig(t)
	cfg.DB.Type = conduit.DBTypeBadger
	cfg.DB.Badger.Path = filepath.Join(t.TempDir(), "conduit.db")
	forceDeleteWithFailingHook(t, cfg, "orders-src")

	report := runChecks(cfg, doctorcheck.Options{})
	result := findResult(t, report, doctorcheck.NameConnectorsOrphanedResources)
	is.Equal(result.Status, check.StatusWarn)
	is.Equal(result.Code, "connector.orphaned_resources")
	is.True(strings.Contains(result.Message, `"orders-src"`))
	is.True(strings.Contains(result.Message, "not found")) // the hook's error, as recorded
	is.True(strings.Contains(result.Suggestion, "--acknowledge-orphan' for: orders-src"))
	is.Equal(report.ExitCode(), 0) // a warning never fails doctor on its own
}

func TestOrphanedResources_NoEntriesPasses(t *testing.T) {
	is := is.New(t)
	cfg := validConfig(t)
	cfg.DB.Type = conduit.DBTypeBadger
	cfg.DB.Badger.Path = filepath.Join(t.TempDir(), "conduit.db")
	db, err := conduit.OpenStore(context.Background(), cfg, log.Nop())
	is.NoErr(err)
	is.NoErr(db.Close())

	result := findResult(t, runChecks(cfg, doctorcheck.Options{}), doctorcheck.NameConnectorsOrphanedResources)
	is.Equal(result.Status, check.StatusPass)
}

// TestOrphanedResources_NoStoreLeavesNothingBehind: doctor's no-side-effects
// rule. A store path that doesn't exist is not created by this check.
func TestOrphanedResources_NoStoreLeavesNothingBehind(t *testing.T) {
	is := is.New(t)
	cfg := validConfig(t)
	cfg.DB.Type = conduit.DBTypeBadger
	cfg.DB.Badger.Path = filepath.Join(t.TempDir(), "fresh", "conduit.db")

	result := findResult(t, runChecks(cfg, doctorcheck.Options{}), doctorcheck.NameConnectorsOrphanedResources)
	is.Equal(result.Status, check.StatusPass)
	_, statErr := os.Stat(cfg.DB.Badger.Path)
	is.True(os.IsNotExist(statErr))
}
