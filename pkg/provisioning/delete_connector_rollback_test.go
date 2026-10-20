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

package provisioning

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/conduitio/conduit/pkg/provisioning/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// TestDeleteConnectorAction_RollbackKeepsPosition is the provisioning half of
// #3137. deleteConnectorAction.Rollback used to be createConnectorAction.Do:
// it created a fresh connector from the config, so after a failed apply the
// in-memory connector had no State (source position), no LastActiveConfig and
// a new CreatedAt until the next restart, while the store (the import's
// transaction was discarded) still had the original record.
//
// The run below is the import's: Do and Rollback inside one transaction that
// is then discarded, as transactionalImport and provisionPipeline do.
func TestDeleteConnectorAction_RollbackKeepsPosition(t *testing.T) {
	testCases := []struct {
		name string
		txn  bool
	}{
		// provisionPipeline and transactionalImport (ApplyPlan, ApplyPlanLive).
		{name: "in a discarded transaction", txn: true},
		// Service.Import (the embed API's Engine.Import) runs without a
		// transaction: the delete really reaches the store, so the rollback
		// has to write the record back.
		{name: "without a transaction", txn: false},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			logger := log.Nop()
			db := &inmemory.DB{}
			ctrl := gomock.NewController(t)
			conns := connector.NewService(logger, db, nil)

			pipelineID := uuid.NewString()
			cfg := config.Connector{
				ID:         uuid.NewString(),
				Type:       config.TypeSource,
				Plugin:     "test-plugin",
				Name:       "src",
				Settings:   map[string]string{"table": "orders"},
				Processors: []config.Processor{{ID: "proc1"}},
			}
			connCfg := connector.Config{Name: cfg.Name, Settings: cfg.Settings}
			conn, err := conns.Create(ctx, cfg.ID, connector.TypeSource, cfg.Plugin, pipelineID, connCfg, connector.ProvisionTypeConfig)
			is.NoErr(err)
			_, err = conns.AddProcessor(ctx, conn.ID, "proc1")
			is.NoErr(err)
			// The connector has run and has a position. It has no
			// LastActiveConfig, so the delete hook does not dispense the
			// plugin.
			wantState := connector.SourceState{Position: opencdc.Position("orders:42")}
			_, err = conns.SetState(ctx, conn.ID, wantState)
			is.NoErr(err)

			plugins := mock.NewConnectorPluginService(ctrl)
			plugins.EXPECT().NewDispenser(gomock.Any(), cfg.Plugin, cfg.ID).Return(pmock.NewDispenser(ctrl), nil)

			b := actionsBuilder{connectorService: conns, connectorPluginService: plugins}
			actions := b.prepareConnectorActions(cfg, config.Connector{}, pipelineID)
			is.Equal(len(actions), 1)

			actx := ctx
			var txn database.Transaction
			if tc.txn {
				txn, actx, err = db.NewTransaction(ctx, true)
				is.NoErr(err)
			}
			is.NoErr(actions[0].Do(actx))
			_, err = conns.Get(ctx, conn.ID)
			is.True(err != nil) // Do deleted it
			is.NoErr(actions[0].Rollback(actx))
			if txn != nil {
				txn.Discard()
			}

			got, err := conns.Get(ctx, conn.ID)
			is.NoErr(err)
			is.True(got == conn) // the same instance, not a fresh one
			is.Equal(got.State, wantState)
			is.Equal(got.ProcessorIDs, []string{"proc1"})

			// Memory agrees with what a restart loads from the store.
			restarted := connector.NewService(logger, db, nil)
			is.NoErr(restarted.Init(ctx))
			stored, err := restarted.Get(ctx, conn.ID)
			is.NoErr(err)
			is.Equal(stored.State, got.State)
			is.Equal(stored.ProcessorIDs, got.ProcessorIDs)
			is.True(stored.CreatedAt.Equal(got.CreatedAt))
		})
	}
}

// TestDeleteConnectorAction_RollbackAfterFailedDo checks the case where Do
// itself failed and deleted nothing: importPipeline rolls back the failed
// action too, and the rollback must leave the live connector alone instead of
// replacing it.
func TestDeleteConnectorAction_RollbackAfterFailedDo(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	conns := connector.NewService(logger, db, nil)

	cfg := config.Connector{ID: uuid.NewString(), Type: config.TypeSource, Plugin: "test-plugin", Name: "src"}
	conn, err := conns.Create(ctx, cfg.ID, connector.TypeSource, cfg.Plugin, uuid.NewString(), connector.Config{Name: cfg.Name}, connector.ProvisionTypeConfig)
	is.NoErr(err)
	wantState := connector.SourceState{Position: opencdc.Position("orders:42")}
	_, err = conns.SetState(ctx, conn.ID, wantState)
	is.NoErr(err)

	// The store refuses the delete, so connector.Service.Delete fails before
	// it removes anything.
	failing := &failingDeleteDB{DB: db}
	connsFailing := connector.NewService(logger, failing, nil)
	is.NoErr(connsFailing.Init(ctx))
	live, err := connsFailing.Get(ctx, conn.ID)
	is.NoErr(err)

	// The delete hook may run before the store delete (it does once #3058's
	// hook-first order lands); with no LastActiveConfig it dispenses nothing.
	plugins := mock.NewConnectorPluginService(ctrl)
	plugins.EXPECT().NewDispenser(gomock.Any(), cfg.Plugin, cfg.ID).Return(pmock.NewDispenser(ctrl), nil).AnyTimes()
	b := actionsBuilder{connectorService: connsFailing, connectorPluginService: plugins}
	actions := b.prepareConnectorActions(cfg, config.Connector{}, uuid.NewString())
	is.Equal(len(actions), 1)

	is.True(actions[0].Do(ctx) != nil)
	is.NoErr(actions[0].Rollback(ctx))

	got, err := connsFailing.Get(ctx, conn.ID)
	is.NoErr(err)
	is.True(got == live) // not replaced by a fresh instance
	is.Equal(got.State, wantState)
}

// failingDeleteDB fails every delete (a Set with a nil value).
type failingDeleteDB struct {
	database.DB
}

func (f *failingDeleteDB) Set(ctx context.Context, key string, value []byte) error {
	if value == nil {
		return errDeleteRefused
	}
	return f.DB.Set(ctx, key, value)
}

var errDeleteRefused = cerrors.New("store refused the delete")

// TestDeleteConnectorAction_RollbackAfterDeleteHookFailsClosed is the
// provisioning half of the fail-closed rule (#3137). A provisioning apply
// that deletes a started connector runs its delete hook; if a later action
// fails, the rollback restores the instance with its position, and its next
// start (at boot, provisioning starts the pipeline right after the import)
// is refused with connector.delete_hook_ran instead of resuming into what
// the hook may have released.
func TestDeleteConnectorAction_RollbackAfterDeleteHookFailsClosed(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	conns := connector.NewService(logger, db, nil)

	cfg := config.Connector{ID: uuid.NewString(), Type: config.TypeSource, Plugin: "test-plugin", Name: "src", Settings: map[string]string{"slot": "s1"}}
	connCfg := connector.Config{Name: cfg.Name, Settings: cfg.Settings}
	conn, err := conns.Create(ctx, cfg.ID, connector.TypeSource, cfg.Plugin, uuid.NewString(), connCfg, connector.ProvisionTypeConfig)
	is.NoErr(err)
	conn.LastActiveConfig = connCfg // it has started
	position := connector.SourceState{Position: opencdc.Position("orders:42")}
	_, err = conns.SetState(ctx, conn.ID, position)
	is.NoErr(err)

	src := pmock.NewSourcePlugin(ctrl)
	src.EXPECT().LifecycleOnDeleted(gomock.Any(), gomock.Any()).Return(pconnector.SourceLifecycleOnDeletedResponse{}, nil)
	src.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.SourceTeardownResponse{}, nil)
	dispenser := pmock.NewDispenser(ctrl)
	dispenser.EXPECT().DispenseSource().Return(src, nil)
	plugins := mock.NewConnectorPluginService(ctrl)
	plugins.EXPECT().NewDispenser(gomock.Any(), cfg.Plugin, cfg.ID).Return(dispenser, nil).AnyTimes()

	b := actionsBuilder{connectorService: conns, connectorPluginService: plugins}
	actions := b.prepareConnectorActions(cfg, config.Connector{}, conn.PipelineID)
	is.Equal(len(actions), 1)

	txn, tctx, err := db.NewTransaction(ctx, true)
	is.NoErr(err)
	is.NoErr(actions[0].Do(tctx))
	is.NoErr(actions[0].Rollback(tctx))
	txn.Discard()

	got, err := conns.Get(ctx, conn.ID)
	is.NoErr(err)
	is.True(got == conn)
	is.Equal(got.State, position)

	c, err := got.Connector(ctx, plugins)
	is.True(c == nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), connector.CodeConnectorDeleteHookRan.Reason())
	is.Equal(got.State, position)
}
