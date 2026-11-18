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

package connector

import (
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// startedDeleteTestConnector creates a connector whose settings resolve and
// that carries a LastActiveConfig, so a delete delivers the "deleted"
// lifecycle event to the plugin.
func startedDeleteTestConnector(t *testing.T, ctx context.Context, svc *Service, typ Type) *Instance {
	t.Helper()
	is := is.New(t)
	cfg := Config{Name: "conn", Settings: map[string]string{"table": "orders"}}
	conn, err := svc.Create(ctx, uuid.NewString(), typ, "test-plugin", uuid.NewString(), cfg, ProvisionTypeAPI)
	is.NoErr(err)
	conn.LastActiveConfig = cfg

	var state any = SourceState{Position: opencdc.Position("orders:42")}
	if typ == TypeDestination {
		state = DestinationState{Positions: map[string]opencdc.Position{"src": opencdc.Position("orders:42")}}
	}
	_, err = svc.SetState(ctx, conn.ID, state)
	is.NoErr(err)
	return conn
}

// stored reports whether the connector's record is still in the store, which
// is what a restart would load.
func stored(t *testing.T, ctx context.Context, db *inmemory.DB, id string) bool {
	t.Helper()
	is := is.New(t)
	all, err := NewStore(db, log.Nop()).GetAll(ctx)
	is.NoErr(err)
	_, ok := all[id]
	return ok
}

// TestService_Delete_HookFailureKeepsTheConnector is the #3058 regression:
// the plugin's "deleted" hook releases what the connector created outside
// Conduit (a Postgres replication slot, for example), so it has to run and
// succeed BEFORE the record is removed. A delete that removes the record and
// only logs the hook failure orphans that resource with nothing left to
// retry the hook from (invariant 3: nothing may be dropped without a way to
// recover it).
func TestService_Delete_HookFailureKeepsTheConnector(t *testing.T) {
	for _, typ := range []Type{TypeSource, TypeDestination} {
		t.Run(typ.String(), func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			ctrl := gomock.NewController(t)
			db := &inmemory.DB{}
			svc := NewService(log.Nop(), db, nil)
			conn := startedDeleteTestConnector(t, ctx, svc, typ)

			hookErr := cerrors.New("could not drop replication slot")
			err := svc.Delete(ctx, conn.ID, fakePluginFetcher{
				conn.Plugin: hookDispenser(ctrl, typ, true, hookErr),
			})

			// The delete failed, and it says why.
			is.True(err != nil)
			is.True(strings.Contains(err.Error(), "could not drop replication slot"))
			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.Equal(ce.Code.Reason(), CodeConnectorDeleteHookFailed.Reason())
			is.True(ce.Suggestion != "")

			// The connector is still there, in memory and in the store, so the
			// delete can be retried once the cause is fixed.
			got, err := svc.Get(ctx, conn.ID)
			is.NoErr(err)
			is.True(got == conn)
			is.True(stored(t, ctx, db, conn.ID))

			// Retrying with a plugin whose hook succeeds removes it.
			is.NoErr(svc.Delete(ctx, conn.ID, fakePluginFetcher{
				conn.Plugin: hookDispenser(ctrl, typ, true, nil),
			}))
			_, err = svc.Get(ctx, conn.ID)
			is.True(cerrors.Is(err, ErrInstanceNotFound))
			is.True(!stored(t, ctx, db, conn.ID))
		})
	}
}

// TestService_Delete_DispenseFailureKeepsTheConnector covers the same gap one
// step earlier: the plugin could not be dispensed at all, so the hook never
// ran. The record has to stay for the same reason.
func TestService_Delete_DispenseFailureKeepsTheConnector(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	svc := NewService(log.Nop(), db, nil)
	conn := startedDeleteTestConnector(t, ctx, svc, TypeSource)

	// An empty fetcher cannot dispense: plugin.ErrPluginNotFound.
	err := svc.Delete(ctx, conn.ID, fakePluginFetcher{})
	is.True(err != nil)
	is.True(cerrors.Is(err, plugin.ErrPluginNotFound))
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodeConnectorDeleteHookFailed.Reason())

	got, err := svc.Get(ctx, conn.ID)
	is.NoErr(err)
	is.True(got == conn)
	is.True(stored(t, ctx, db, conn.ID))
}

// TestService_Delete_NeverStartedNeedsNoPlugin is the other side of the same
// ordering: a connector that never started created nothing outside Conduit,
// so there is no hook to run and no plugin to dispense. A missing plugin
// binary must not make such a connector undeletable.
func TestService_Delete_NeverStartedNeedsNoPlugin(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	svc := NewService(log.Nop(), db, nil)
	conn, err := svc.Create(ctx, uuid.NewString(), TypeSource, "test-plugin", uuid.NewString(),
		Config{Name: "conn", Settings: map[string]string{"table": "orders"}}, ProvisionTypeAPI)
	is.NoErr(err)
	is.True(conn.LastActiveConfig.Settings == nil) // never started

	is.NoErr(svc.Delete(ctx, conn.ID, fakePluginFetcher{})) // no plugin available
	_, err = svc.Get(ctx, conn.ID)
	is.True(cerrors.Is(err, ErrInstanceNotFound))
	is.True(!stored(t, ctx, db, conn.ID))
}

// TestService_Delete_RunningConnectorKeepsTheConnector pins the trap in the
// never-started shortcut. A connector with empty settings triggers no
// lifecycle event, so LastActiveConfig.Settings stays nil while Open still
// sets Instance.connector. A shortcut keyed on the settings alone would skip
// the ErrConnectorRunning check and remove the record from under a live
// plugin.
func TestService_Delete_RunningConnectorKeepsTheConnector(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	svc := NewService(log.Nop(), db, nil)
	conn, err := svc.Create(ctx, uuid.NewString(), TypeSource, "test-plugin", uuid.NewString(),
		Config{Name: "conn"}, ProvisionTypeAPI)
	is.NoErr(err)
	is.True(conn.LastActiveConfig.Settings == nil)
	conn.connector = fakeConnector{} // as after a successful Open

	err = svc.Delete(ctx, conn.ID, fakePluginFetcher{})
	is.True(err != nil)
	is.True(cerrors.Is(err, ErrConnectorRunning))
	// conduiterr.Wrap keeps the cause's own code, which is the more precise
	// reason here.
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodeConnectorRunning.Reason())

	got, err := svc.Get(ctx, conn.ID)
	is.NoErr(err)
	is.True(got == conn)
	is.True(stored(t, ctx, db, conn.ID))
}

// TestService_Delete_ForcedRunningConnectorKeepsTheConnector pins what force
// does not buy. Force is the escape hatch for a hook that cannot run or that
// failed, and it skips the hook — but the stored record is the only handle
// Conduit has on a live run, so removing it while the plugin is still
// streaming loses the position and leaves the run unreachable (invariant 2).
// A forced delete of a running connector is refused exactly like an unforced
// one, and reports connector.running so the caller knows to stop the pipeline
// rather than retry with more force.
func TestService_Delete_ForcedRunningConnectorKeepsTheConnector(t *testing.T) {
	is := is.New(t)
	ctx := WithForceDelete(context.Background())
	db := &inmemory.DB{}
	svc := NewService(log.Nop(), db, nil)
	conn, err := svc.Create(ctx, uuid.NewString(), TypeSource, "test-plugin", uuid.NewString(),
		Config{Name: "conn"}, ProvisionTypeAPI)
	is.NoErr(err)
	conn.connector = fakeConnector{} // as after a successful Open

	err = svc.Delete(ctx, conn.ID, fakePluginFetcher{})
	is.True(err != nil)
	is.True(cerrors.Is(err, ErrConnectorRunning))
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodeConnectorRunning.Reason())

	got, err := svc.Get(ctx, conn.ID)
	is.NoErr(err)
	is.True(got == conn)
	is.True(stored(t, ctx, db, conn.ID))
}
