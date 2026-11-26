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
	"github.com/conduitio/conduit/pkg/foundation/log"
	procmock "github.com/conduitio/conduit/pkg/plugin/processor/mock"
	"github.com/conduitio/conduit/pkg/processor"
	procsvcmock "github.com/conduitio/conduit/pkg/processor/mock"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// TestDeleteProcessorAction_RollbackRestoresInstance is the provisioning half
// of #3370, the sibling of TestDeleteConnectorAction_RollbackKeepsPosition
// (#3137). deleteProcessorAction used to be a type alias over
// createProcessorAction with Do and Rollback swapped, so its rollback created
// the processor again from config. That handed the process a fresh Instance
// with new timestamps and new inspectors, dispensed the plugin a second time,
// and — inside the import's transaction — issued a store write through a
// transaction that was about to be discarded.
//
// The run below is the import's: Do and Rollback inside one transaction that
// is then discarded, as transactionalImport and provisionPipeline do, and
// again without a transaction, as Service.Import (the embed API's
// Engine.Import) does.
func TestDeleteProcessorAction_RollbackRestoresInstance(t *testing.T) {
	testCases := []struct {
		name       string
		txn        bool
		parentType processor.ParentType
	}{
		// provisionPipeline and transactionalImport (ApplyPlan, ApplyPlanLive).
		{name: "pipeline parent, in a discarded transaction", txn: true, parentType: processor.ParentTypePipeline},
		{name: "connector parent, in a discarded transaction", txn: true, parentType: processor.ParentTypeConnector},
		// Service.Import runs without a transaction: the delete really reaches
		// the store, so the rollback has to write the record back.
		{name: "pipeline parent, without a transaction", txn: false, parentType: processor.ParentTypePipeline},
		{name: "connector parent, without a transaction", txn: false, parentType: processor.ParentTypeConnector},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			logger := log.Nop()
			db := &inmemory.DB{}
			ctrl := gomock.NewController(t)

			// Dispense is expected ONCE, for the initial Create. A rollback
			// that went through Create would dispense a second time and fail
			// the test as an unexpected call — which is the regression.
			sdkProc := procmock.NewProcessor(ctrl)
			sdkProc.EXPECT().Teardown(gomock.Any()).Return(nil).Times(1)
			reg := procsvcmock.NewPluginService(ctrl)
			reg.EXPECT().NewProcessor(gomock.Any(), "test-processor", gomock.Any(), gomock.Any()).
				Return(sdkProc, nil).Times(1)

			procs := processor.NewService(logger, db, reg)

			cfg := config.Processor{
				ID:        uuid.NewString(),
				Plugin:    "test-processor",
				Settings:  map[string]string{"field": ".Payload.After.id"},
				Workers:   2,
				Condition: "{{ true }}",
			}
			parent := processor.Parent{ID: uuid.NewString(), Type: tc.parentType}

			proc, err := procs.Create(
				ctx,
				cfg.ID,
				cfg.Plugin,
				parent,
				processor.Config{Settings: cfg.Settings, Workers: cfg.Workers},
				processor.ProvisionTypeConfig,
				cfg.Condition,
			)
			is.NoErr(err)
			wantCreatedAt := proc.CreatedAt
			wantUpdatedAt := proc.UpdatedAt

			b := actionsBuilder{processorService: procs}
			actions := b.prepareProcessorActions(cfg, config.Processor{}, parent)
			is.Equal(len(actions), 1)

			actx := ctx
			var txn database.Transaction
			if tc.txn {
				txn, actx, err = db.NewTransaction(ctx, true)
				is.NoErr(err)
			}
			is.NoErr(actions[0].Do(actx))
			_, err = procs.Get(ctx, proc.ID)
			is.True(err != nil) // Do deleted it
			is.NoErr(actions[0].Rollback(actx))
			if txn != nil {
				txn.Discard()
			}

			got, err := procs.Get(ctx, proc.ID)
			is.NoErr(err)
			is.True(got == proc) // the same instance, not a fresh one
			is.True(got.CreatedAt.Equal(wantCreatedAt))
			is.True(got.UpdatedAt.Equal(wantUpdatedAt))
			is.Equal(got.ProvisionedBy, processor.ProvisionTypeConfig)
			is.Equal(got.Parent, parent)

			// Memory agrees with what a restart loads from the store.
			restarted := processor.NewService(logger, db, procsvcmock.NewPluginService(ctrl))
			is.NoErr(restarted.Init(ctx))
			stored, err := restarted.Get(ctx, proc.ID)
			is.NoErr(err)
			is.True(stored.CreatedAt.Equal(got.CreatedAt))
			is.Equal(stored.Parent, got.Parent)
			is.Equal(stored.ProvisionedBy, got.ProvisionedBy)
		})
	}
}

// TestDeleteProcessorAction_RollbackAfterFailedDo covers the case where Do
// deleted nothing, because the processor was not there. Rollback must restore
// nothing rather than create a processor the config no longer describes.
func TestDeleteProcessorAction_RollbackAfterFailedDo(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)

	// No NewProcessor expectation: nothing is created, and a rollback that
	// went through Create would dispense here and fail as unexpected.
	procs := processor.NewService(logger, db, procsvcmock.NewPluginService(ctrl))

	cfg := config.Processor{ID: uuid.NewString(), Plugin: "test-processor"}
	parent := processor.Parent{ID: uuid.NewString(), Type: processor.ParentTypePipeline}

	b := actionsBuilder{processorService: procs}
	actions := b.prepareProcessorActions(cfg, config.Processor{}, parent)
	is.Equal(len(actions), 1)

	is.NoErr(actions[0].Do(ctx)) // not found is not an error
	is.NoErr(actions[0].Rollback(ctx))

	_, err := procs.Get(ctx, cfg.ID)
	is.True(err != nil) // still absent
}
