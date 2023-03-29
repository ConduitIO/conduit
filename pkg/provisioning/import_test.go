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

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/conduitio/conduit/pkg/provisioning/mock"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
	"github.com/matryer/is"
)

func TestService_PreparePipelineActions_Create(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, pipSrv, _, _, _ := newTestService(ctrl, logger)

	oldConfig := config.Pipeline{}
	newConfig := config.Pipeline{ID: "config-id"}

	want := []action{createPipelineAction{
		cfg:             newConfig,
		pipelineService: pipSrv,
	}}

	got := srv.preparePipelineActions(oldConfig, newConfig)
	is.Equal(got, want)
}

func TestService_PreparePipelineActions_Delete(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, pipSrv, _, _, _ := newTestService(ctrl, logger)

	oldConfig := config.Pipeline{ID: "config-id"}
	newConfig := config.Pipeline{}

	want := []action{deletePipelineAction{
		cfg:             oldConfig,
		pipelineService: pipSrv,
	}}

	got := srv.preparePipelineActions(oldConfig, newConfig)
	is.Equal(got, want)
}

func TestService_PreparePipelineActions_NoAction(t *testing.T) {
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, _, _, _, _ := newTestService(ctrl, logger)

	testCases := []struct {
		name      string
		oldConfig config.Pipeline
		newConfig config.Pipeline
	}{{
		name:      "empty",
		oldConfig: config.Pipeline{ID: "config-id"},
		newConfig: config.Pipeline{ID: "config-id"},
	}, {
		name:      "different Status",
		oldConfig: config.Pipeline{ID: "config-id", Status: "stopped"},
		newConfig: config.Pipeline{ID: "config-id", Status: "running"},
	}, {
		name: "different Connectors (same ID)",
		oldConfig: config.Pipeline{ID: "config-id", Connectors: []config.Connector{{
			ID:         "conn-id", // only ID has to match
			Type:       config.TypeSource,
			Plugin:     "old-plugin",
			Name:       "old-name",
			Settings:   map[string]string{"foo": "bar"},
			Processors: []config.Processor{{ID: "old-config-id"}},
		}}},
		newConfig: config.Pipeline{ID: "config-id", Connectors: []config.Connector{{
			ID:         "conn-id", // only ID has to match
			Type:       config.TypeDestination,
			Plugin:     "new-plugin",
			Name:       "new-name",
			Settings:   map[string]string{"foo": "baz"},
			Processors: []config.Processor{{ID: "new-config-id"}},
		}}},
	}, {
		name: "different Processors (same ID)",
		oldConfig: config.Pipeline{ID: "config-id", Processors: []config.Processor{{
			ID:       "proc-id", // only ID has to match
			Type:     "old-type",
			Settings: map[string]string{"foo": "bar"},
			Workers:  1,
		}}},
		newConfig: config.Pipeline{ID: "config-id", Processors: []config.Processor{{
			ID:       "proc-id", // only ID has to match
			Type:     "new-type",
			Settings: map[string]string{"foo": "baz"},
			Workers:  2,
		}}},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			got := srv.preparePipelineActions(tc.oldConfig, tc.newConfig)
			is.Equal(got, nil) // did not expect any actions
		})
	}
}

func TestService_PreparePipelineActions_Update(t *testing.T) {
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, pipSrv, _, _, _ := newTestService(ctrl, logger)

	testCases := []struct {
		name      string
		oldConfig config.Pipeline
		newConfig config.Pipeline
	}{{
		name:      "different Name",
		oldConfig: config.Pipeline{ID: "config-id", Name: "old-name"},
		newConfig: config.Pipeline{ID: "config-id", Name: "new-name"},
	}, {
		name:      "different Description",
		oldConfig: config.Pipeline{ID: "config-id", Description: "old-description"},
		newConfig: config.Pipeline{ID: "config-id", Description: "new-description"},
	}, {
		name:      "different Connectors.ID",
		oldConfig: config.Pipeline{ID: "config-id", Connectors: []config.Connector{{ID: "old-id"}}},
		newConfig: config.Pipeline{ID: "config-id", Connectors: []config.Connector{{ID: "new-id"}}},
	}, {
		name:      "different Processors.ID",
		oldConfig: config.Pipeline{ID: "config-id", Processors: []config.Processor{{ID: "old-id"}}},
		newConfig: config.Pipeline{ID: "config-id", Processors: []config.Processor{{ID: "new-id"}}},
	}, {
		name:      "different DLQ.Plugin",
		oldConfig: config.Pipeline{ID: "config-id", DLQ: config.DLQ{Plugin: "old-plugin"}},
		newConfig: config.Pipeline{ID: "config-id", DLQ: config.DLQ{Plugin: "new-plugin"}},
	}, {
		name:      "different DLQ.Settings",
		oldConfig: config.Pipeline{ID: "config-id", DLQ: config.DLQ{Settings: map[string]string{"foo": "bar"}}},
		newConfig: config.Pipeline{ID: "config-id", DLQ: config.DLQ{Settings: map[string]string{"foo": "baz"}}},
		// TODO different DLQ workers and threshold
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			want := []action{updatePipelineAction{
				oldConfig:       tc.oldConfig,
				newConfig:       tc.newConfig,
				pipelineService: pipSrv,
			}}
			got := srv.preparePipelineActions(tc.oldConfig, tc.newConfig)
			is.Equal(got, want)
		})
	}
}

func TestService_PrepareConnectorActions_Create(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, _, connSrv, _, plugSrv := newTestService(ctrl, logger)

	oldConfig := config.Connector{}
	newConfig := config.Connector{ID: "config-id"}
	pipelineID := uuid.NewString()

	want := []action{createConnectorAction{
		cfg:              newConfig,
		pipelineID:       pipelineID,
		connectorService: connSrv,
		pluginService:    plugSrv,
	}}

	got := srv.prepareConnectorActions(oldConfig, newConfig, pipelineID)
	is.Equal(got, want)
}

func TestService_PrepareConnectorActions_Delete(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, _, connSrv, _, plugSrv := newTestService(ctrl, logger)

	oldConfig := config.Connector{ID: "config-id"}
	newConfig := config.Connector{}
	pipelineID := uuid.NewString()

	want := []action{deleteConnectorAction{
		cfg:              oldConfig,
		pipelineID:       pipelineID,
		connectorService: connSrv,
		pluginService:    plugSrv,
	}}

	got := srv.prepareConnectorActions(oldConfig, newConfig, pipelineID)
	is.Equal(got, want)
}

func TestService_PrepareConnectorActions_NoAction(t *testing.T) {
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, _, _, _, _ := newTestService(ctrl, logger)

	testCases := []struct {
		name      string
		oldConfig config.Connector
		newConfig config.Connector
	}{{
		name:      "empty",
		oldConfig: config.Connector{ID: "config-id"},
		newConfig: config.Connector{ID: "config-id"},
	}, {
		name: "different Processors",
		oldConfig: config.Connector{ID: "config-id", Processors: []config.Processor{{
			ID:       "proc-id", // only ID has to match
			Type:     "old-type",
			Settings: map[string]string{"foo": "bar"},
			Workers:  1,
		}}},
		newConfig: config.Connector{ID: "config-id", Processors: []config.Processor{{
			ID:       "proc-id", // only ID has to match
			Type:     "new-type",
			Settings: map[string]string{"foo": "baz"},
			Workers:  2,
		}}},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			got := srv.prepareConnectorActions(tc.oldConfig, tc.newConfig, uuid.NewString())
			is.Equal(got, nil) // did not expect any actions
		})
	}
}

func TestService_PrepareConnectorActions_Update(t *testing.T) {
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, _, connSrv, _, _ := newTestService(ctrl, logger)

	testCases := []struct {
		name      string
		oldConfig config.Connector
		newConfig config.Connector
	}{{
		name:      "different Name",
		oldConfig: config.Connector{ID: "config-id", Name: "old-name"},
		newConfig: config.Connector{ID: "config-id", Name: "new-name"},
	}, {
		name:      "different Settings",
		oldConfig: config.Connector{ID: "config-id", Settings: map[string]string{"foo": "bar"}},
		newConfig: config.Connector{ID: "config-id", Settings: map[string]string{"foo": "baz"}},
	}, {
		name:      "different Processors.ID",
		oldConfig: config.Connector{ID: "config-id", Processors: []config.Processor{{ID: "old-id"}}},
		newConfig: config.Connector{ID: "config-id", Processors: []config.Processor{{ID: "new-id"}}},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			want := []action{updateConnectorAction{
				oldConfig:        tc.oldConfig,
				newConfig:        tc.newConfig,
				connectorService: connSrv,
			}}
			got := srv.prepareConnectorActions(tc.oldConfig, tc.newConfig, uuid.NewString())
			is.Equal(got, want)
		})
	}
}

func TestService_PrepareConnectorActions_Recreate(t *testing.T) {
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, _, connSrv, _, plugSrv := newTestService(ctrl, logger)
	pipelineID := uuid.NewString()

	testCases := []struct {
		name      string
		oldConfig config.Connector
		newConfig config.Connector
	}{{
		name:      "different Type",
		oldConfig: config.Connector{ID: "config-id", Type: config.TypeSource},
		newConfig: config.Connector{ID: "config-id", Type: config.TypeDestination},
	}, {
		name:      "different Plugin",
		oldConfig: config.Connector{ID: "config-id", Plugin: "old-plugin"},
		newConfig: config.Connector{ID: "config-id", Plugin: "new-plugin"},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			want := []action{deleteConnectorAction{
				cfg:              tc.oldConfig,
				pipelineID:       pipelineID,
				connectorService: connSrv,
				pluginService:    plugSrv,
			}, createConnectorAction{
				cfg:              tc.newConfig,
				pipelineID:       pipelineID,
				connectorService: connSrv,
				pluginService:    plugSrv,
			}}
			got := srv.prepareConnectorActions(tc.oldConfig, tc.newConfig, pipelineID)
			is.Equal(got, want)
		})
	}
}

func TestService_PrepareProcessorActions_Create(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, _, _, procSrv, _ := newTestService(ctrl, logger)

	oldConfig := config.Processor{}
	newConfig := config.Processor{ID: "config-id"}
	parent := processor.Parent{
		ID:   uuid.NewString(),
		Type: processor.ParentTypePipeline,
	}

	want := []action{createProcessorAction{
		cfg:              newConfig,
		parent:           parent,
		processorService: procSrv,
	}}

	got := srv.prepareProcessorActions(oldConfig, newConfig, parent)
	is.Equal(got, want)
}

func TestService_PrepareProcessorActions_Delete(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, _, _, procSrv, _ := newTestService(ctrl, logger)

	oldConfig := config.Processor{ID: "config-id"}
	newConfig := config.Processor{}
	parent := processor.Parent{
		ID:   uuid.NewString(),
		Type: processor.ParentTypePipeline,
	}

	want := []action{deleteProcessorAction{
		cfg:              oldConfig,
		parent:           parent,
		processorService: procSrv,
	}}

	got := srv.prepareProcessorActions(oldConfig, newConfig, parent)
	is.Equal(got, want)
}

func TestService_PrepareProcessorActions_NoAction(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, _, _, _, _ := newTestService(ctrl, logger)

	oldConfig := config.Processor{ID: "config-id"}
	newConfig := config.Processor{ID: "config-id"}
	parent := processor.Parent{
		ID:   uuid.NewString(),
		Type: processor.ParentTypeConnector,
	}

	got := srv.prepareProcessorActions(oldConfig, newConfig, parent)
	is.Equal(got, nil) // did not expect any actions
}

func TestService_PrepareProcessorActions_Update(t *testing.T) {
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, _, _, procSrv, _ := newTestService(ctrl, logger)
	parent := processor.Parent{
		ID:   uuid.NewString(),
		Type: processor.ParentTypeConnector,
	}

	testCases := []struct {
		name      string
		oldConfig config.Processor
		newConfig config.Processor
	}{{
		name:      "different Settings",
		oldConfig: config.Processor{ID: "config-id", Settings: map[string]string{"foo": "bar"}},
		newConfig: config.Processor{ID: "config-id", Settings: map[string]string{"foo": "baz"}},
	}, {
		name:      "different Workers",
		oldConfig: config.Processor{ID: "config-id", Workers: 1},
		newConfig: config.Processor{ID: "config-id", Workers: 2},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			want := []action{updateProcessorAction{
				oldConfig:        tc.oldConfig,
				newConfig:        tc.newConfig,
				processorService: procSrv,
			}}
			got := srv.prepareProcessorActions(tc.oldConfig, tc.newConfig, parent)
			is.Equal(got, want)
		})
	}
}

func TestService_PrepareProcessorActions_Recreate(t *testing.T) {
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, _, _, procSrv, _ := newTestService(ctrl, logger)
	parent := processor.Parent{
		ID:   uuid.NewString(),
		Type: processor.ParentTypePipeline,
	}

	testCases := []struct {
		name      string
		oldConfig config.Processor
		newConfig config.Processor
	}{{
		name:      "different Type",
		oldConfig: config.Processor{ID: "config-id", Type: "old-type"},
		newConfig: config.Processor{ID: "config-id", Type: "new-type"},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			want := []action{deleteProcessorAction{
				cfg:              tc.oldConfig,
				parent:           parent,
				processorService: procSrv,
			}, createProcessorAction{
				cfg:              tc.newConfig,
				parent:           parent,
				processorService: procSrv,
			}}
			got := srv.prepareProcessorActions(tc.oldConfig, tc.newConfig, parent)
			is.Equal(got, want)
		})
	}
}

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
			WindowSize:          intPtr(1),
			WindowNackThreshold: intPtr(2),
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
			WindowSize:          intPtr(1),
			WindowNackThreshold: intPtr(2),
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
			WindowSize:          intPtr(1),
			WindowNackThreshold: intPtr(2),
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
			WindowSize:          intPtr(1),
			WindowNackThreshold: intPtr(2),
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
			WindowSize:          intPtr(1),
			WindowNackThreshold: intPtr(2),
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
				cfg:              haveCfg,
				pipelineID:       pipelineID,
				connectorService: connSrv,
				pluginService:    nil, // only needed for Rollback
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

	plugSrv := mock.NewPluginService(ctrl)
	connSrv := mock.NewConnectorService(ctrl)
	connSrv.EXPECT().Delete(ctx, haveCfg.ID, plugSrv)

	a := createConnectorAction{
		cfg:              haveCfg,
		pipelineID:       pipelineID,
		connectorService: connSrv,
		pluginService:    plugSrv,
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
			connSrv.EXPECT().Update(ctx, haveCfg.ID, wantCfg).Return(instance, nil)
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

	plugSrv := mock.NewPluginService(ctrl)
	connSrv := mock.NewConnectorService(ctrl)
	connSrv.EXPECT().Delete(ctx, haveCfg.ID, plugSrv)

	a := deleteConnectorAction{
		cfg:              haveCfg,
		pipelineID:       pipelineID,
		connectorService: connSrv,
		pluginService:    plugSrv,
	}
	err := a.Do(ctx)
	is.NoErr(err)
}

func TestDeleteConnectorAction_Rollback(t *testing.T) {
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

			a := deleteConnectorAction{
				cfg:              haveCfg,
				pipelineID:       pipelineID,
				connectorService: connSrv,
				pluginService:    nil, // only needed for Do
			}
			err := a.Rollback(ctx)
			is.NoErr(err)
		})
	}
}

func TestCreateProcessorAction_Do(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	haveCfg := config.Processor{
		ID:       uuid.NewString(),
		Type:     "processor-type",
		Settings: map[string]string{"foo": "bar"},
		Workers:  2,
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
	procSrv.EXPECT().Create(ctx, haveCfg.ID, haveCfg.Type, parent, wantCfg, processor.ProvisionTypeConfig)

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
		Type:     "processor-type",
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
		Type:     "processor-type",
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
			connSrv.EXPECT().Update(ctx, haveCfg.ID, wantCfg).Return(instance, nil)

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
		Type:     "processor-type",
		Settings: map[string]string{"foo": "bar"},
		Workers:  2,
	}

	procSrv := mock.NewProcessorService(ctrl)
	procSrv.EXPECT().Delete(ctx, haveCfg.ID)

	a := deleteProcessorAction{
		cfg:              haveCfg,
		parent:           processor.Parent{}, // only needed for Rollback
		processorService: procSrv,
	}
	err := a.Do(ctx)
	is.NoErr(err)
}

func TestDeleteProcessorAction_Rollback(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	haveCfg := config.Processor{
		ID:       uuid.NewString(),
		Type:     "processor-type",
		Settings: map[string]string{"foo": "bar"},
		Workers:  2,
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
	procSrv.EXPECT().Create(ctx, haveCfg.ID, haveCfg.Type, parent, wantCfg, processor.ProvisionTypeConfig)

	a := deleteProcessorAction{
		cfg:              haveCfg,
		parent:           parent,
		processorService: procSrv,
	}
	err := a.Rollback(ctx)
	is.NoErr(err)
}

func intPtr(i int) *int { return &i }

func newTestService(ctrl *gomock.Controller, logger log.CtxLogger) (*Service, *mock.PipelineService, *mock.ConnectorService, *mock.ProcessorService, *mock.PluginService) {
	db := &inmemory.DB{}
	pipSrv := mock.NewPipelineService(ctrl)
	connSrv := mock.NewConnectorService(ctrl)
	procSrv := mock.NewProcessorService(ctrl)
	plugSrv := mock.NewPluginService(ctrl)

	srv := NewService(db, logger, pipSrv, connSrv, procSrv, plugSrv, "")

	return srv, pipSrv, connSrv, procSrv, plugSrv
}
