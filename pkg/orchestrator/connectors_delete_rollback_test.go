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

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
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

func (f failingRemoveConnector) RemoveConnector(context.Context, string, string) (*pipeline.Instance, error) {
	return nil, f.err
}

// TestConnectorOrchestrator_Delete_RollbackKeepsPosition is the #3137
// regression test. When a step after connectors.Delete failed, the rollback
// used to call connectors.Create with the old ID, type, plugin and config.
// The transaction was discarded, so the store kept the original record, but
// in memory the connector became a fresh Instance with no State (no source
// position) and no LastActiveConfig. Until the next restart, starting the
// pipeline would read the source from its beginning (invariant 2: duplicates,
// no loss) and fire a "created" lifecycle event instead of "updated".
//
// The rollback must put back the same instance, so that memory agrees with
// what a restart loads from the store.
func TestConnectorOrchestrator_Delete_RollbackKeepsPosition(t *testing.T) {
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

	// The connector has run: it has a source position and a last active
	// config. SetState persists the whole instance, both included.
	conn.LastActiveConfig = cfg
	wantState := connector.SourceState{Position: opencdc.Position("orders:42")}
	_, err = conns.SetState(ctx, conn.ID, wantState)
	is.NoErr(err)

	// The delete hook runs inside connectors.Delete (LastActiveConfig is set,
	// so the plugin is dispensed and told the connector is deleted).
	src := pmock.NewSourcePlugin(ctrl)
	src.EXPECT().LifecycleOnDeleted(gomock.Any(), gomock.Any()).Return(pconnector.SourceLifecycleOnDeletedResponse{}, nil)
	src.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.SourceTeardownResponse{}, nil)
	dispenser := pmock.NewDispenser(ctrl)
	dispenser.EXPECT().DispenseSource().Return(src, nil)

	_, _, procsMock, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)
	connPluginMock.EXPECT().NewDispenser(gomock.Any(), conn.Plugin, conn.ID).Return(dispenser, nil)
	expectMutation(t, lifecycleMock, pl.ID, true)

	wantErr := cerrors.New("could not remove the connector from the pipeline")
	pls := failingRemoveConnector{Service: plsReal, err: wantErr}
	orc := NewOrchestrator(db, logger, pls, conns, procsMock, connPluginMock, procPluginMock, lifecycleMock)

	err = orc.Connectors.Delete(ctx, conn.ID)
	is.True(cerrors.Is(err, wantErr))

	// In memory: the same instance, with its position and last active config.
	got, err := conns.Get(ctx, conn.ID)
	is.NoErr(err)
	is.True(got == conn) // the rollback did not replace the instance
	is.Equal(got.State, wantState)
	is.Equal(got.LastActiveConfig, cfg)

	// Memory agrees with the store, i.e. with what a restart would load.
	assertStoredConnector(t, db, logger, got)

	// The pipeline still lists the connector (the AddConnector rollback).
	gotPl, err := plsReal.Get(ctx, pl.ID)
	is.NoErr(err)
	is.Equal(gotPl.ConnectorIDs, []string{conn.ID})
}

// assertStoredConnector loads the connectors from db, as Init does on a
// restart, and checks that want's record matches it.
func assertStoredConnector(t *testing.T, db database.DB, logger log.CtxLogger, want *connector.Instance) {
	t.Helper()
	is := is.New(t)
	restarted := connector.NewService(logger, db, nil)
	is.NoErr(restarted.Init(context.Background()))
	stored, err := restarted.Get(context.Background(), want.ID)
	is.NoErr(err)
	is.Equal(stored.State, want.State)
	is.Equal(stored.LastActiveConfig, want.LastActiveConfig)
	is.Equal(stored.Config, want.Config)
	is.Equal(stored.PipelineID, want.PipelineID)
	is.True(stored.CreatedAt.Equal(want.CreatedAt))
}
