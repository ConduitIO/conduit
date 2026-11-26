// Copyright © 2023 Meroxa, Inc.
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
	"github.com/conduitio/conduit-commons/lang"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	procMock "github.com/conduitio/conduit/pkg/plugin/processor/mock"
	"github.com/conduitio/conduit/pkg/processor"
	procSvcMock "github.com/conduitio/conduit/pkg/processor/mock"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/conduitio/conduit/pkg/provisioning/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestCreatePipelineAction_Do(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	haveCfg := config.Pipeline{
		ID:          uuid.NewString(),
		Name:        "pipeline-name",
		Description: "pipeline description",
		Connectors:  []config.Connector{{ID: "conn1"}, {ID: "conn2"}},
		Processors:  []config.Processor{{ID: "proc1"}, {ID: "proc2"}},
		DLQ: config.DLQ{
			Plugin:              "dlq-plugin",
			Settings:            map[string]string{"foo": "bar"},
			WindowSize:          lang.Ptr(1),
			WindowNackThreshold: lang.Ptr(2),
		},
	}
	wantCfg := pipeline.Config{
		Name:        haveCfg.Name,
		Description: haveCfg.Description,
	}
	wantDLQ := pipeline.DLQ{
		Plugin:              haveCfg.DLQ.Plugin,
		Settings:            haveCfg.DLQ.Settings,
		WindowSize:          *haveCfg.DLQ.WindowSize,
		WindowNackThreshold: *haveCfg.DLQ.WindowNackThreshold,
	}

	pipSrv := mock.NewPipelineService(ctrl)
	pipSrv.EXPECT().Create(ctx, haveCfg.ID, wantCfg, pipeline.ProvisionTypeConfig)
	pipSrv.EXPECT().UpdateDLQ(ctx, haveCfg.ID, wantDLQ)
	pipSrv.EXPECT().AddConnector(ctx, haveCfg.ID, haveCfg.Connectors[0].ID)
	pipSrv.EXPECT().AddConnector(ctx, haveCfg.ID, haveCfg.Connectors[1].ID)
	pipSrv.EXPECT().AddProcessor(ctx, haveCfg.ID, haveCfg.Processors[0].ID)
	pipSrv.EXPECT().AddProcessor(ctx, haveCfg.ID, haveCfg.Processors[1].ID)

	a := createPipelineAction{
		cfg:             haveCfg,
		provisionedBy:   pipeline.ProvisionTypeConfig,
		pipelineService: pipSrv,
	}
	err := a.Do(ctx)
	is.NoErr(err)
}

func TestCreatePipelineAction_Rollback(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	haveCfg := config.Pipeline{
		ID:          uuid.NewString(),
		Name:        "pipeline-name",
		Description: "pipeline description",
		Connectors:  []config.Connector{{ID: "conn1"}, {ID: "conn2"}},
		Processors:  []config.Processor{{ID: "proc1"}, {ID: "proc2"}},
		DLQ: config.DLQ{
			Plugin:              "dlq-plugin",
			Settings:            map[string]string{"foo": "bar"},
			WindowSize:          lang.Ptr(1),
			WindowNackThreshold: lang.Ptr(2),
		},
	}

	pipelineService := mock.NewPipelineService(ctrl)
	pipelineService.EXPECT().Delete(ctx, haveCfg.ID)

	a := createPipelineAction{
		cfg:             haveCfg,
		pipelineService: pipelineService,
	}
	err := a.Rollback(ctx)
	is.NoErr(err)
}

func TestUpdatePipelineAction(t *testing.T) {
	haveCfg := config.Pipeline{
		ID:          uuid.NewString(),
		Name:        "pipeline-name",
		Description: "pipeline description",
		Connectors:  []config.Connector{{ID: "conn1"}, {ID: "conn2"}},
		Processors:  []config.Processor{{ID: "proc1"}, {ID: "proc2"}},
		DLQ: config.DLQ{
			Plugin:              "dlq-plugin",
			Settings:            map[string]string{"foo": "bar"},
			WindowSize:          lang.Ptr(1),
			WindowNackThreshold: lang.Ptr(2),
		},
	}

	testCases := []struct {
		name      string
		oldConfig config.Pipeline
		newConfig config.Pipeline
		execute   func(context.Context, updatePipelineAction) error
	}{{
		name:      "Do",
		oldConfig: config.Pipeline{}, // not used in Do
		newConfig: haveCfg,
		execute: func(ctx context.Context, a updatePipelineAction) error {
			return a.Do(ctx)
		},
	}, {
		name:      "Rollback",
		oldConfig: haveCfg,
		newConfig: config.Pipeline{}, // not used in Rollback
		execute: func(ctx context.Context, a updatePipelineAction) error {
			return a.Rollback(ctx)
		},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			ctrl := gomock.NewController(t)

			instance := &pipeline.Instance{
				ID:           haveCfg.ID,
				ConnectorIDs: []string{"oldConn1", "oldConn2"},
				ProcessorIDs: []string{"oldProc1", "oldProc2"},
			}
			wantCfg := pipeline.Config{
				Name:        haveCfg.Name,
				Description: haveCfg.Description,
			}
			wantDLQ := pipeline.DLQ{
				Plugin:              haveCfg.DLQ.Plugin,
				Settings:            haveCfg.DLQ.Settings,
				WindowSize:          *haveCfg.DLQ.WindowSize,
				WindowNackThreshold: *haveCfg.DLQ.WindowNackThreshold,
			}

			pipSrv := mock.NewPipelineService(ctrl)
			pipSrv.EXPECT().Update(ctx, haveCfg.ID, wantCfg).Return(instance, nil)
			pipSrv.EXPECT().UpdateDLQ(ctx, haveCfg.ID, wantDLQ)
			pipSrv.EXPECT().RemoveConnector(ctx, haveCfg.ID, instance.ConnectorIDs[0])
			pipSrv.EXPECT().RemoveConnector(ctx, haveCfg.ID, instance.ConnectorIDs[1])
			pipSrv.EXPECT().RemoveProcessor(ctx, haveCfg.ID, instance.ProcessorIDs[0])
			pipSrv.EXPECT().RemoveProcessor(ctx, haveCfg.ID, instance.ProcessorIDs[1])
			pipSrv.EXPECT().AddConnector(ctx, haveCfg.ID, haveCfg.Connectors[0].ID)
			pipSrv.EXPECT().AddConnector(ctx, haveCfg.ID, haveCfg.Connectors[1].ID)
			pipSrv.EXPECT().AddProcessor(ctx, haveCfg.ID, haveCfg.Processors[0].ID)
			pipSrv.EXPECT().AddProcessor(ctx, haveCfg.ID, haveCfg.Processors[1].ID)

			a := updatePipelineAction{
				oldConfig:       tc.oldConfig,
				newConfig:       tc.newConfig,
				pipelineService: pipSrv,
			}
			err := tc.execute(ctx, a)
			is.NoErr(err)
		})
	}
}

func TestDeletePipelineAction_Do(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	haveCfg := config.Pipeline{
		ID:          uuid.NewString(),
		Name:        "pipeline-name",
		Description: "pipeline description",
		Connectors:  []config.Connector{{ID: "conn1"}, {ID: "conn2"}},
		Processors:  []config.Processor{{ID: "proc1"}, {ID: "proc2"}},
		DLQ: config.DLQ{
			Plugin:              "dlq-plugin",
			Settings:            map[string]string{"foo": "bar"},
			WindowSize:          lang.Ptr(1),
			WindowNackThreshold: lang.Ptr(2),
		},
	}

	pipSrv := mock.NewPipelineService(ctrl)
	pipSrv.EXPECT().Delete(ctx, haveCfg.ID)

	a := deletePipelineAction{
		cfg:             haveCfg,
		pipelineService: pipSrv,
	}
	err := a.Do(ctx)
	is.NoErr(err)
}

func TestDeletePipelineAction_Rollback(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	haveCfg := config.Pipeline{
		ID:          uuid.NewString(),
		Name:        "pipeline-name",
		Description: "pipeline description",
		Connectors:  []config.Connector{{ID: "conn1"}, {ID: "conn2"}},
		Processors:  []config.Processor{{ID: "proc1"}, {ID: "proc2"}},
		DLQ: config.DLQ{
			Plugin:              "dlq-plugin",
			Settings:            map[string]string{"foo": "bar"},
			WindowSize:          lang.Ptr(1),
			WindowNackThreshold: lang.Ptr(2),
		},
	}
	wantCfg := pipeline.Config{
		Name:        haveCfg.Name,
		Description: haveCfg.Description,
	}
	wantDLQ := pipeline.DLQ{
		Plugin:              haveCfg.DLQ.Plugin,
		Settings:            haveCfg.DLQ.Settings,
		WindowSize:          *haveCfg.DLQ.WindowSize,
		WindowNackThreshold: *haveCfg.DLQ.WindowNackThreshold,
	}

	pipSrv := mock.NewPipelineService(ctrl)
	pipSrv.EXPECT().Create(ctx, haveCfg.ID, wantCfg, pipeline.ProvisionTypeConfig)
	pipSrv.EXPECT().UpdateDLQ(ctx, haveCfg.ID, wantDLQ)
	pipSrv.EXPECT().AddConnector(ctx, haveCfg.ID, haveCfg.Connectors[0].ID)
	pipSrv.EXPECT().AddConnector(ctx, haveCfg.ID, haveCfg.Connectors[1].ID)
	pipSrv.EXPECT().AddProcessor(ctx, haveCfg.ID, haveCfg.Processors[0].ID)
	pipSrv.EXPECT().AddProcessor(ctx, haveCfg.ID, haveCfg.Processors[1].ID)

	a := deletePipelineAction{
		cfg:             haveCfg,
		provisionedBy:   pipeline.ProvisionTypeConfig,
		pipelineService: pipSrv,
	}
	err := a.Rollback(ctx)
	is.NoErr(err)
}

func TestCreateConnectorAction_Do(t *testing.T) {
	testCases := []struct {
		haveConnType string
		wantConnType connector.Type
	}{
		{haveConnType: config.TypeSource, wantConnType: connector.TypeSource},
		{haveConnType: config.TypeDestination, wantConnType: connector.TypeDestination},
	}

	for _, tc := range testCases {
		t.Run(tc.haveConnType, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			ctrl := gomock.NewController(t)

			pipelineID := uuid.NewString()
			haveCfg := config.Connector{
				ID:         uuid.NewString(),
				Name:       "connector-name",
				Type:       tc.haveConnType,
				Plugin:     "my-plugin",
				Settings:   map[string]string{"foo": "bar"},
				Processors: []config.Processor{{ID: "proc1"}, {ID: "proc2"}},
			}
			wantCfg := connector.Config{
				Name:     haveCfg.Name,
				Settings: haveCfg.Settings,
			}

			connSrv := mock.NewConnectorService(ctrl)
			connSrv.EXPECT().Create(ctx, haveCfg.ID, tc.wantConnType, haveCfg.Plugin, pipelineID, wantCfg, connector.ProvisionTypeConfig)
			connSrv.EXPECT().AddProcessor(ctx, haveCfg.ID, haveCfg.Processors[0].ID)
			connSrv.EXPECT().AddProcessor(ctx, haveCfg.ID, haveCfg.Processors[1].ID)

			a := createConnectorAction{
				cfg:                    haveCfg,
				pipelineID:             pipelineID,
				connectorService:       connSrv,
				connectorPluginService: nil, // only needed for Rollback
			}
			err := a.Do(ctx)
			is.NoErr(err)
		})
	}
}

func TestCreateConnectorAction_Rollback(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	pipelineID := uuid.NewString()
	haveCfg := config.Connector{
		ID:         uuid.NewString(),
		Name:       "connector-name",
		Type:       config.TypeSource,
		Plugin:     "my-plugin",
		Settings:   map[string]string{"foo": "bar"},
		Processors: []config.Processor{{ID: "proc1"}, {ID: "proc2"}},
	}

	connPlugSrv := mock.NewConnectorPluginService(ctrl)
	connSrv := mock.NewConnectorService(ctrl)
	connSrv.EXPECT().Delete(ctx, haveCfg.ID, connPlugSrv)

	a := createConnectorAction{
		cfg:                    haveCfg,
		pipelineID:             pipelineID,
		connectorService:       connSrv,
		connectorPluginService: connPlugSrv,
	}
	err := a.Rollback(ctx)
	is.NoErr(err)
}

func TestUpdateConnectorAction(t *testing.T) {
	haveCfg := config.Connector{
		ID:         uuid.NewString(),
		Name:       "connector-name",
		Type:       config.TypeSource,
		Plugin:     "my-plugin",
		Settings:   map[string]string{"foo": "bar"},
		Processors: []config.Processor{{ID: "proc1"}, {ID: "proc2"}},
	}

	testCases := []struct {
		name      string
		oldConfig config.Connector
		newConfig config.Connector
		execute   func(context.Context, updateConnectorAction) error
	}{{
		name:      "Do",
		oldConfig: config.Connector{}, // not used in Do
		newConfig: haveCfg,
		execute: func(ctx context.Context, a updateConnectorAction) error {
			return a.Do(ctx)
		},
	}, {
		name:      "Rollback",
		oldConfig: haveCfg,
		newConfig: config.Connector{}, // not used in Rollback
		execute: func(ctx context.Context, a updateConnectorAction) error {
			return a.Rollback(ctx)
		},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			ctrl := gomock.NewController(t)

			instance := &connector.Instance{
				ID:           haveCfg.ID,
				ProcessorIDs: []string{"oldProc1", "oldProc2"},
			}
			wantCfg := connector.Config{
				Name:     haveCfg.Name,
				Settings: haveCfg.Settings,
			}

			connSrv := mock.NewConnectorService(ctrl)
			connSrv.EXPECT().Update(ctx, haveCfg.ID, haveCfg.Plugin, wantCfg).Return(instance, nil)
			connSrv.EXPECT().RemoveProcessor(ctx, haveCfg.ID, instance.ProcessorIDs[0])
			connSrv.EXPECT().RemoveProcessor(ctx, haveCfg.ID, instance.ProcessorIDs[1])
			connSrv.EXPECT().AddProcessor(ctx, haveCfg.ID, haveCfg.Processors[0].ID)
			connSrv.EXPECT().AddProcessor(ctx, haveCfg.ID, haveCfg.Processors[1].ID)

			a := updateConnectorAction{
				oldConfig:        tc.oldConfig,
				newConfig:        tc.newConfig,
				connectorService: connSrv,
			}
			err := tc.execute(ctx, a)
			is.NoErr(err)
		})
	}
}

func TestDeleteConnectorAction_Do(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	pipelineID := uuid.NewString()
	haveCfg := config.Connector{
		ID:         uuid.NewString(),
		Name:       "connector-name",
		Type:       config.TypeSource,
		Plugin:     "my-plugin",
		Settings:   map[string]string{"foo": "bar"},
		Processors: []config.Processor{{ID: "proc1"}, {ID: "proc2"}},
	}
	instance := &connector.Instance{ID: haveCfg.ID}

	connPlugSrv := mock.NewConnectorPluginService(ctrl)
	connSrv := mock.NewConnectorService(ctrl)
	connSrv.EXPECT().Get(ctx, haveCfg.ID).Return(instance, nil)
	connSrv.EXPECT().Delete(ctx, haveCfg.ID, connPlugSrv)

	a := deleteConnectorAction{
		cfg:                    haveCfg,
		pipelineID:             pipelineID,
		connectorService:       connSrv,
		connectorPluginService: connPlugSrv,
		deleted:                &deletedConnector{},
	}
	err := a.Do(ctx)
	is.NoErr(err)
	is.True(a.deleted.instance == instance) // kept for Rollback
}

func TestDeleteConnectorAction_Do_NotFound(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	haveCfg := config.Connector{ID: uuid.NewString()}
	connSrv := mock.NewConnectorService(ctrl)
	connSrv.EXPECT().Get(ctx, haveCfg.ID).Return(nil, connector.ErrInstanceNotFound)

	a := deleteConnectorAction{cfg: haveCfg, connectorService: connSrv, deleted: &deletedConnector{}}
	is.NoErr(a.Do(ctx))
	is.True(a.deleted.instance == nil)
	is.NoErr(a.Rollback(ctx)) // nothing to restore, no call
}

func TestDeleteConnectorAction_Rollback(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	instance := &connector.Instance{ID: uuid.NewString()}
	connSrv := mock.NewConnectorService(ctrl)
	// #3137: the deleted instance is put back, not created again from config.
	connSrv.EXPECT().Restore(ctx, instance).Return(nil)

	a := deleteConnectorAction{
		cfg:              config.Connector{ID: instance.ID},
		connectorService: connSrv,
		deleted:          &deletedConnector{instance: instance},
	}
	is.NoErr(a.Rollback(ctx))
	is.NoErr(a.Rollback(ctx)) // a second rollback restores nothing
}

func TestCreateProcessorAction_Do(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	haveCfg := config.Processor{
		ID:        uuid.NewString(),
		Plugin:    "processor-type",
		Settings:  map[string]string{"foo": "bar"},
		Workers:   2,
		Condition: "{{ eq .Metadata.opencdc.version \"v1\" }}",
	}
	wantCfg := processor.Config{
		Settings: haveCfg.Settings,
		Workers:  haveCfg.Workers,
	}
	parent := processor.Parent{
		ID:   uuid.NewString(),
		Type: processor.ParentTypePipeline,
	}

	procSrv := mock.NewProcessorService(ctrl)
	procSrv.EXPECT().Create(ctx, haveCfg.ID, haveCfg.Plugin, parent, wantCfg, processor.ProvisionTypeConfig, haveCfg.Condition)

	a := createProcessorAction{
		cfg:              haveCfg,
		parent:           parent,
		processorService: procSrv,
	}
	err := a.Do(ctx)
	is.NoErr(err)
}

func TestCreateProcessorAction_Rollback(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	haveCfg := config.Processor{
		ID:       uuid.NewString(),
		Plugin:   "processor-type",
		Settings: map[string]string{"foo": "bar"},
		Workers:  2,
	}

	procSrv := mock.NewProcessorService(ctrl)
	procSrv.EXPECT().Delete(ctx, haveCfg.ID)

	a := createProcessorAction{
		cfg:              haveCfg,
		parent:           processor.Parent{}, // only needed for Do
		processorService: procSrv,
	}
	err := a.Rollback(ctx)
	is.NoErr(err)
}

func TestUpdateProcessorAction(t *testing.T) {
	haveCfg := config.Processor{
		ID:       uuid.NewString(),
		Plugin:   "processor-type",
		Settings: map[string]string{"foo": "bar"},
		Workers:  2,
	}

	testCases := []struct {
		name      string
		oldConfig config.Processor
		newConfig config.Processor
		execute   func(context.Context, updateProcessorAction) error
	}{{
		name:      "Do",
		oldConfig: config.Processor{}, // not used in Do
		newConfig: haveCfg,
		execute: func(ctx context.Context, a updateProcessorAction) error {
			return a.Do(ctx)
		},
	}, {
		name:      "Rollback",
		oldConfig: haveCfg,
		newConfig: config.Processor{}, // not used in Rollback
		execute: func(ctx context.Context, a updateProcessorAction) error {
			return a.Rollback(ctx)
		},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			ctrl := gomock.NewController(t)

			instance := &processor.Instance{
				ID: haveCfg.ID,
			}
			wantCfg := processor.Config{
				Settings: haveCfg.Settings,
				Workers:  haveCfg.Workers,
			}

			connSrv := mock.NewProcessorService(ctrl)
			connSrv.EXPECT().UpdateWhileRunning(ctx, haveCfg.ID, haveCfg.Plugin, wantCfg).Return(instance, nil)

			a := updateProcessorAction{
				oldConfig:        tc.oldConfig,
				newConfig:        tc.newConfig,
				processorService: connSrv,
			}
			err := tc.execute(ctx, a)
			is.NoErr(err)
		})
	}
}

func TestDeleteProcessorAction_Do(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	haveCfg := config.Processor{
		ID:       uuid.NewString(),
		Plugin:   "processor-type",
		Settings: map[string]string{"foo": "bar"},
		Workers:  2,
	}
	instance := &processor.Instance{ID: haveCfg.ID}

	procSrv := mock.NewProcessorService(ctrl)
	procSrv.EXPECT().Get(ctx, haveCfg.ID).Return(instance, nil)
	procSrv.EXPECT().Delete(ctx, haveCfg.ID)

	a := deleteProcessorAction{
		cfg:              haveCfg,
		processorService: procSrv,
		deleted:          &deletedProcessor{},
	}
	err := a.Do(ctx)
	is.NoErr(err)
	is.True(a.deleted.instance == instance) // kept for Rollback
}

func TestDeleteProcessorAction_Do_NotFound(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	haveCfg := config.Processor{ID: uuid.NewString()}
	procSrv := mock.NewProcessorService(ctrl)
	procSrv.EXPECT().Get(ctx, haveCfg.ID).Return(nil, processor.ErrInstanceNotFound)

	a := deleteProcessorAction{cfg: haveCfg, processorService: procSrv, deleted: &deletedProcessor{}}
	is.NoErr(a.Do(ctx))
	is.True(a.deleted.instance == nil)
	is.NoErr(a.Rollback(ctx)) // nothing to restore, no call
}

func TestDeleteProcessorAction_Do_NilHolder(t *testing.T) {
	is := is.New(t)
	a := deleteProcessorAction{cfg: config.Processor{ID: uuid.NewString()}}
	is.True(a.Do(context.Background()) != nil)
}

func TestDeleteProcessorAction_Rollback(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	instance := &processor.Instance{ID: uuid.NewString()}
	procSrv := mock.NewProcessorService(ctrl)
	// #3385: the deleted instance is put back, not created again from config.
	procSrv.EXPECT().Restore(ctx, instance).Return(nil)

	a := deleteProcessorAction{
		cfg:              config.Processor{ID: instance.ID},
		processorService: procSrv,
		deleted:          &deletedProcessor{instance: instance},
	}
	is.NoErr(a.Rollback(ctx))
	is.NoErr(a.Rollback(ctx)) // a second rollback restores nothing
}

// TestDeleteProcessorAction_RollbackRestoresInstance is the #3385 end-to-end
// regression test against processor.Service. Rolling back a deleted processor
// must put back the exact *processor.Instance that Do deleted (preserving
// CreatedAt/UpdatedAt and live inspector sessions) and must not re-dispense the
// processor plugin.
func TestDeleteProcessorAction_RollbackRestoresInstance(t *testing.T) {
	for _, tc := range []struct {
		name string
		txn  bool
	}{
		{name: "without transaction", txn: false},
		{name: "in discarded transaction", txn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			logger := log.Nop()
			db := &inmemory.DB{}
			ctrl := gomock.NewController(t)

			// Dispense is expected ONCE (initial Create); rollback must NOT re-dispense.
			sdkProc := procMock.NewProcessor(ctrl)
			sdkProc.EXPECT().Teardown(gomock.Any()).Return(nil).Times(1)
			reg := procSvcMock.NewPluginService(ctrl)
			reg.EXPECT().NewProcessor(gomock.Any(), "test-processor", gomock.Any(), gomock.Any()).Return(sdkProc, nil).Times(1)

			procs := processor.NewService(logger, db, reg)
			parent := processor.Parent{ID: uuid.NewString(), Type: processor.ParentTypePipeline}
			cfg := config.Processor{
				ID:        uuid.NewString(),
				Plugin:    "test-processor",
				Settings:  map[string]string{"field": "val"},
				Workers:   2,
				Condition: "{{ true }}",
			}
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

			b := actionsBuilder{processorService: procs}
			actions := b.prepareProcessorActions(cfg, config.Processor{}, parent)
			is.Equal(len(actions), 1)
			is.Equal(actions[0].Describe().Code, "provisioning.processor.delete")

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
			is.True(got == proc) // Invariant 2: same instance pointer, not a fresh one
			is.True(got.CreatedAt.Equal(proc.CreatedAt))
			is.True(got.UpdatedAt.Equal(proc.UpdatedAt))

			// Live inspector stays usable on the restored instance.
			sess := got.InspectIn(ctx, "prov-session")
			is.True(sess != nil)
			got.Close()

			// Memory agrees with what a restart loads from the store.
			restarted := processor.NewService(logger, db, procSvcMock.NewPluginService(ctrl))
			is.NoErr(restarted.Init(ctx))
			stored, err := restarted.Get(ctx, proc.ID)
			is.NoErr(err)
			is.Equal(stored.Config, proc.Config)
			is.Equal(stored.Condition, proc.Condition)
			is.Equal(stored.ProvisionedBy, processor.ProvisionTypeConfig)
			is.True(stored.CreatedAt.Equal(proc.CreatedAt))
			is.True(stored.UpdatedAt.Equal(proc.UpdatedAt))
		})
	}
}

// TestDeleteProcessorAction_RollbackAfterFailedDo verifies that when Do fails
// before removing anything, Rollback leaves the live processor untouched
// instead of replacing it.
func TestDeleteProcessorAction_RollbackAfterFailedDo(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)

	sdkProc := procMock.NewProcessor(ctrl)
	sdkProc.EXPECT().Teardown(gomock.Any()).Return(nil).Times(1)
	reg := procSvcMock.NewPluginService(ctrl)
	reg.EXPECT().NewProcessor(gomock.Any(), "test-processor", gomock.Any(), gomock.Any()).Return(sdkProc, nil).Times(1)

	procs := processor.NewService(logger, db, reg)
	parent := processor.Parent{ID: uuid.NewString(), Type: processor.ParentTypePipeline}
	cfg := config.Processor{ID: uuid.NewString(), Plugin: "test-processor", Workers: 1}
	proc, err := procs.Create(ctx, cfg.ID, cfg.Plugin, parent, processor.Config{Workers: 1}, processor.ProvisionTypeConfig, "")
	is.NoErr(err)

	procsFailing := processor.NewService(logger, &failingDeleteDB{DB: db}, procSvcMock.NewPluginService(ctrl))
	is.NoErr(procsFailing.Init(ctx))
	live, err := procsFailing.Get(ctx, proc.ID)
	is.NoErr(err)

	b := actionsBuilder{processorService: procsFailing}
	actions := b.prepareProcessorActions(cfg, config.Processor{}, parent)
	is.Equal(len(actions), 1)

	is.True(actions[0].Do(ctx) != nil)
	is.NoErr(actions[0].Rollback(ctx))

	got, err := procsFailing.Get(ctx, proc.ID)
	is.NoErr(err)
	is.True(got == live) // not replaced by a fresh instance
}

func TestDeleteProcessorAction_ErrorBranches(t *testing.T) {
	ctx := context.Background()
	wantErr := processor.ErrProcessorRunning

	t.Run("String and Describe", func(t *testing.T) {
		is := is.New(t)
		a := deleteProcessorAction{cfg: config.Processor{ID: "proc-1"}}
		is.Equal(a.String(), "delete processor with ID proc-1")
		is.Equal(a.Describe(), Change{
			Resource: ResourceProcessor,
			ID:       "proc-1",
			Action:   ChangeActionDelete,
			Effect:   EffectRestart,
			Code:     "provisioning.processor.delete",
		})
	})

	t.Run("Get error", func(t *testing.T) {
		is := is.New(t)
		ctrl := gomock.NewController(t)
		procSrv := mock.NewProcessorService(ctrl)
		procSrv.EXPECT().Get(ctx, "proc-1").Return(nil, wantErr)
		a := deleteProcessorAction{cfg: config.Processor{ID: "proc-1"}, processorService: procSrv, deleted: &deletedProcessor{}}
		is.True(a.Do(ctx) != nil)
	})

	t.Run("Restore error", func(t *testing.T) {
		is := is.New(t)
		ctrl := gomock.NewController(t)
		inst := &processor.Instance{ID: "proc-1"}
		procSrv := mock.NewProcessorService(ctrl)
		procSrv.EXPECT().Restore(ctx, inst).Return(wantErr)
		a := deleteProcessorAction{cfg: config.Processor{ID: "proc-1"}, processorService: procSrv, deleted: &deletedProcessor{instance: inst}}
		is.True(a.Rollback(ctx) != nil)
	})
}
