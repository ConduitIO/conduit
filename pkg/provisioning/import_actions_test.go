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

	"github.com/conduitio/conduit-commons/lang"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
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

	connPlugSrv := mock.NewConnectorPluginService(ctrl)
	connSrv := mock.NewConnectorService(ctrl)
	connSrv.EXPECT().Delete(ctx, haveCfg.ID, connPlugSrv)

	a := deleteConnectorAction{
		cfg:                    haveCfg,
		pipelineID:             pipelineID,
		connectorService:       connSrv,
		connectorPluginService: connPlugSrv,
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
				cfg:                    haveCfg,
				pipelineID:             pipelineID,
				connectorService:       connSrv,
				connectorPluginService: nil, // only needed for Do
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
			connSrv.EXPECT().Update(ctx, haveCfg.ID, haveCfg.Plugin, wantCfg).Return(instance, nil)

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

	a := deleteProcessorAction{
		cfg:              haveCfg,
		parent:           parent,
		processorService: procSrv,
	}
	err := a.Rollback(ctx)
	is.NoErr(err)
}
