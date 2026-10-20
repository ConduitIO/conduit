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

package orchestrator

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// failingRemoveConnector is a real pipeline.Service whose RemoveConnector
// fails, so ConnectorOrchestrator.Delete fails after the connector record was
// deleted and has to roll back.
type failingRemoveConnector struct {
	*pipeline.Service
	err error
}

// fakeDispenserFetcher returns dispenser for any plugin.
type fakeDispenserFetcher struct {
	dispenser connectorPlugin.Dispenser
}

func (f fakeDispenserFetcher) NewDispenser(log.CtxLogger, string, string) (connectorPlugin.Dispenser, error) {
	return f.dispenser, nil
}

func (f failingRemoveConnector) RemoveConnector(context.Context, string, string) (*pipeline.Instance, error) {
	return nil, f.err
}

// TestConnectorOrchestrator_Delete_RollbackRestoresInstance is the #3137
// regression test. When a step after connectors.Delete failed, the rollback
// used to call connectors.Create with the old ID, type, plugin and config.
// The transaction was discarded, so the store kept the original record, but
// in memory the connector became a fresh Instance until the next restart.
//
// The rollback must put back the same instance, position included. What
// happens next depends on whether Delete already ran the plugin's delete
// hook:
//
//   - The hook did not run (the connector never started, so there was no
//     "deleted" event to deliver): the connector starts normally.
//   - The hook ran: the plugin may have released what the position points
//     into. The Postgres connector drops its replication slot; resuming would
//     recreate the slot at the current WAL and skip the gap, and clearing the
//     position is no safer (a Kafka source without one starts at the end).
//     The connector fails closed: building it for a start returns
//     connector.delete_hook_ran, and the position stays intact (invariant 3).
func TestConnectorOrchestrator_Delete_RollbackRestoresInstance(t *testing.T) {
	testCases := []struct {
		name    string
		started bool // the connector has a LastActiveConfig, so the hook runs
	}{
		{name: "delete hook did not run, starts normally", started: false},
		{name: "delete hook ran, start fails closed", started: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			logger := log.Nop()
			db := &inmemory.DB{}
			ctrl := gomock.NewController(t)

			plsReal := pipeline.NewService(logger, db)
			conns := connector.NewService(logger, db, nil)

			pl, err := plsReal.Create(ctx, uuid.NewString(), pipeline.Config{Name: "p-" + uuid.NewString()}, pipeline.ProvisionTypeAPI)
			is.NoErr(err)
			pl.SetStatus(pipeline.StatusUserStopped)

			cfg := connector.Config{Name: "src", Settings: map[string]string{"table": "orders"}}
			conn, err := conns.Create(ctx, uuid.NewString(), connector.TypeSource, "test-plugin", pl.ID, cfg, connector.ProvisionTypeAPI)
			is.NoErr(err)
			_, err = plsReal.AddConnector(ctx, pl.ID, conn.ID)
			is.NoErr(err)

			// The connector has a source position (and, if it started, a
			// last active config). SetState persists the whole instance.
			if tc.started {
				conn.LastActiveConfig = cfg
			}
			position := connector.SourceState{Position: opencdc.Position("orders:42")}
			_, err = conns.SetState(ctx, conn.ID, position)
			is.NoErr(err)

			dispenser := pmock.NewDispenser(ctrl)
			if tc.started {
				// connectors.Delete runs the hook: the plugin is dispensed and
				// told the connector is deleted.
				src := pmock.NewSourcePlugin(ctrl)
				src.EXPECT().LifecycleOnDeleted(gomock.Any(), gomock.Any()).Return(pconnector.SourceLifecycleOnDeletedResponse{}, nil)
				src.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.SourceTeardownResponse{}, nil)
				dispenser.EXPECT().DispenseSource().Return(src, nil)
			}

			_, _, procsMock, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)
			connPluginMock.EXPECT().NewDispenser(gomock.Any(), conn.Plugin, conn.ID).Return(dispenser, nil)
			expectMutation(t, lifecycleMock, pl.ID, true)

			wantErr := cerrors.New("could not remove the connector from the pipeline")
			pls := failingRemoveConnector{Service: plsReal, err: wantErr}
			orc := NewOrchestrator(db, logger, pls, conns, procsMock, connPluginMock, procPluginMock, lifecycleMock)

			err = orc.Connectors.Delete(ctx, conn.ID)
			is.True(cerrors.Is(err, wantErr))

			got, err := conns.Get(ctx, conn.ID)
			is.NoErr(err)
			is.True(got == conn) // the same instance, not a fresh one
			is.True(got.CreatedAt.Equal(conn.CreatedAt))
			is.Equal(got.State, position) // never cleared
			if tc.started {
				is.Equal(got.LastActiveConfig, cfg)
			}

			// The next start builds the connector (lifecycle calls Connector).
			c, err := got.Connector(ctx, fakeDispenserFetcher{dispenser: pmock.NewDispenser(ctrl)})
			if tc.started {
				is.True(c == nil)
				ce, ok := conduiterr.Get(err)
				is.True(ok)
				is.Equal(ce.Code.Reason(), connector.CodeConnectorDeleteHookRan.Reason())
				is.Equal(got.State, position)
			} else {
				is.NoErr(err)
				is.True(c != nil)
			}

			// The discarded transaction kept the record: a restart still has
			// the connector, with its position. The fail-closed mark is in
			// memory only, so a restart loads it unmarked (see the PR's
			// failure-mode analysis).
			restarted := connector.NewService(logger, db, nil)
			is.NoErr(restarted.Init(ctx))
			stored, err := restarted.Get(ctx, conn.ID)
			is.NoErr(err)
			is.Equal(stored.Config, conn.Config)
			is.Equal(stored.PipelineID, conn.PipelineID)

			// The pipeline still lists the connector (the AddConnector rollback).
			gotPl, err := plsReal.Get(ctx, pl.ID)
			is.NoErr(err)
			is.Equal(gotPl.ConnectorIDs, []string{conn.ID})
		})
	}
}
