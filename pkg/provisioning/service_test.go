// Copyright © 2022 Meroxa, Inc.
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
	"os"
	"sort"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/foundation/database/badger"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/builtin"
	"github.com/conduitio/conduit/pkg/plugin/standalone"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/processor/procbuiltin"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/conduitio/conduit/pkg/provisioning/mock"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/golang/mock/gomock"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

var pipeline1 = config.Pipeline{
	ID:          "pipeline1",
	Status:      "running",
	Name:        "pipeline1",
	Description: "desc1",
	Connectors: []config.Connector{{
		ID:     "pipeline1:con1",
		Type:   "source",
		Plugin: "builtin:file",
		Name:   "source",
		Settings: map[string]string{
			"path": "my/path/file1.txt",
		},
	}, {
		ID:     "pipeline1:con2",
		Type:   "destination",
		Plugin: "builtin:file",
		Name:   "dest",
		Settings: map[string]string{
			"path": "my/path/file2.txt",
		},
		Processors: []config.Processor{{
			ID:      "pipeline1:con2:proc1con",
			Type:    "js",
			Workers: 10,
			Settings: map[string]string{
				"additionalProp1": "string",
			},
		}},
	}},
	Processors: []config.Processor{{
		ID:      "pipeline1:proc1",
		Type:    "js",
		Workers: 1,
		Settings: map[string]string{
			"additionalProp1": "string",
		},
	}},
}

var pipeline2 = config.Pipeline{
	ID:          "pipeline2",
	Status:      "running",
	Name:        "pipeline2",
	Description: "desc2",
	Connectors: []config.Connector{{
		ID:     "pipeline2:con1",
		Type:   "source",
		Plugin: "builtin:file",
		Name:   "source",
		Settings: map[string]string{
			"path": "my/path/file1.txt",
		},
	}, {
		ID:     "pipeline2:con2",
		Type:   "destination",
		Plugin: "builtin:file",
		Name:   "dest",
		Settings: map[string]string{
			"path": "my/path/file2.txt",
		},
	}},
}

var pipeline3 = config.Pipeline{
	ID:          "pipeline3",
	Status:      "running",
	Name:        "pipeline3",
	Description: "desc3",
	Connectors: []config.Connector{{
		ID:     "pipeline3:con1",
		Type:   "source",
		Plugin: "builtin:file",
		Name:   "source",
		Settings: map[string]string{
			"path": "my/path/file1.txt",
		},
	}, {
		ID:     "pipeline3:con2",
		Type:   "destination",
		Plugin: "builtin:file",
		Name:   "dest",
		Settings: map[string]string{
			"path": "my/path/file2.txt",
		},
	}},
}

func TestProvision_Create(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	pipelineService := mock.NewPipelineService(ctrl)
	connService := mock.NewConnectorService(ctrl)
	procService := mock.NewProcessorService(ctrl)
	plugService := mock.NewPluginService(ctrl)

	pl1config := pipeline.Config{Name: pipeline1.Name, Description: pipeline1.Description}
	pl1 := &pipeline.Instance{
		ID:            pipeline1.ID,
		Config:        pl1config,
		Status:        pipeline.StatusRunning,
		ProvisionedBy: pipeline.ProvisionTypeConfig,
		ConnectorIDs:  []string{"pipeline1:con1", "pipeline1:con2"},
	}

	cfg1 := connector.Config{
		Name:     pipeline1.Connectors[0].Name,
		Settings: pipeline1.Connectors[0].Settings,
	}
	cfg2 := connector.Config{
		Name:     pipeline1.Connectors[1].Name,
		Settings: pipeline1.Connectors[1].Settings,
	}
	procCfg1 := processor.Config{
		Settings: pipeline1.Processors[0].Settings,
		Workers:  pipeline1.Processors[0].Workers,
	}
	procCfg2 := processor.Config{
		Settings: pipeline1.Connectors[1].Processors[0].Settings,
		Workers:  pipeline1.Connectors[1].Processors[0].Workers,
	}
	procParentPipeline := processor.Parent{
		ID:   pipeline1.ID,
		Type: processor.ParentTypePipeline,
	}
	procParentConn := processor.Parent{
		ID:   pipeline1.Connectors[1].ID,
		Type: processor.ParentTypeConnector,
	}

	pipelineService.EXPECT().List(gomock.Not(gomock.Nil()))
	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), pipeline1.Name).Return(nil, pipeline.ErrInstanceNotFound)
	pipelineService.EXPECT().Create(gomock.Not(gomock.Nil()), pipeline1.Name, pl1config, pipeline.ProvisionTypeConfig).Return(pl1, nil)
	pipelineService.EXPECT().UpdateDLQ(gomock.Not(gomock.Nil()), pl1.ID, pipeline.DLQ{WindowSize: 20, WindowNackThreshold: 10}).Return(pl1, nil)

	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:con1", connector.TypeSource, pipeline1.Connectors[0].Plugin, pipeline1.Name, cfg1, connector.ProvisionTypeConfig)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:con1")
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:con2", connector.TypeDestination, pipeline1.Connectors[1].Plugin, pipeline1.Name, cfg2, connector.ProvisionTypeConfig)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:con2")

	procService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:con2:proc1con", "js", procParentConn, procCfg2, processor.ProvisionTypeConfig)
	connService.EXPECT().AddProcessor(gomock.Not(gomock.Nil()), "pipeline1:con2", "pipeline1:con2:proc1con")
	procService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:proc1", "js", procParentPipeline, procCfg1, processor.ProvisionTypeConfig)
	pipelineService.EXPECT().AddProcessor(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:proc1")

	pipelineService.EXPECT().Start(gomock.Not(gomock.Nil()), connService, procService, plugService, pipeline1.Name)

	service := NewService(db, logger, pipelineService, connService, procService, plugService, "./test/pipelines1")
	err := service.Init(context.Background())
	is.NoErr(err)
}

func TestProvision_NoRollbackOnFailedStart(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	pipelineService := mock.NewPipelineService(ctrl)
	connService := mock.NewConnectorService(ctrl)
	procService := mock.NewProcessorService(ctrl)
	plugService := mock.NewPluginService(ctrl)

	pl1config := pipeline.Config{Name: pipeline1.Name, Description: pipeline1.Description}
	pl1 := &pipeline.Instance{
		ID:            pipeline1.ID,
		Config:        pl1config,
		Status:        pipeline.StatusRunning,
		ProvisionedBy: pipeline.ProvisionTypeConfig,
	}
	cfg1 := connector.Config{
		Name:     pipeline1.Connectors[0].Name,
		Settings: pipeline1.Connectors[0].Settings,
	}
	cfg2 := connector.Config{
		Name:     pipeline1.Connectors[1].Name,
		Settings: pipeline1.Connectors[1].Settings,
	}
	procCfg1 := processor.Config{
		Settings: pipeline1.Processors[0].Settings,
		Workers:  pipeline1.Processors[0].Workers,
	}
	procCfg2 := processor.Config{
		Settings: pipeline1.Connectors[1].Processors[0].Settings,
		Workers:  pipeline1.Connectors[1].Processors[0].Workers,
	}
	procParentPipeline := processor.Parent{
		ID:   pipeline1.ID,
		Type: processor.ParentTypePipeline,
	}
	procParentConn := processor.Parent{
		ID:   pipeline1.Connectors[1].ID,
		Type: processor.ParentTypeConnector,
	}

	pipelineService.EXPECT().List(gomock.Not(gomock.Nil()))
	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), pipeline1.Name).Return(nil, pipeline.ErrInstanceNotFound)
	pipelineService.EXPECT().Create(gomock.Not(gomock.Nil()), pipeline1.Name, pl1config, pipeline.ProvisionTypeConfig).Return(pl1, nil)
	pipelineService.EXPECT().UpdateDLQ(gomock.Not(gomock.Nil()), pl1.ID, pipeline.DLQ{WindowSize: 20, WindowNackThreshold: 10}).Return(pl1, nil)

	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:con1", connector.TypeSource, pipeline1.Connectors[0].Plugin, pipeline1.Name, cfg1, connector.ProvisionTypeConfig)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:con1")
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:con2", connector.TypeDestination, pipeline1.Connectors[1].Plugin, pipeline1.Name, cfg2, connector.ProvisionTypeConfig)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:con2")

	procService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:con2:proc1con", "js", procParentConn, procCfg2, processor.ProvisionTypeConfig)
	connService.EXPECT().AddProcessor(gomock.Not(gomock.Nil()), "pipeline1:con2", "pipeline1:con2:proc1con")
	procService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:proc1", "js", procParentPipeline, procCfg1, processor.ProvisionTypeConfig)
	pipelineService.EXPECT().AddProcessor(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:proc1")

	// returns an error, no rollback needed
	pipelineService.EXPECT().Start(gomock.Not(gomock.Nil()), connService, procService, plugService, pipeline1.Name).Return(cerrors.New("error"))

	service := NewService(db, logger, pipelineService, connService, procService, plugService, "./test/pipelines1")
	err := service.Init(context.Background())
	is.True(err != nil)
}

func TestProvision_RollbackCreate(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	pipelineService := mock.NewPipelineService(ctrl)
	connService := mock.NewConnectorService(ctrl)
	procService := mock.NewProcessorService(ctrl)
	plugService := mock.NewPluginService(ctrl)

	pl1config := pipeline.Config{Name: pipeline1.Name, Description: pipeline1.Description}
	pl1 := &pipeline.Instance{
		ID:            pipeline1.ID,
		Config:        pl1config,
		Status:        pipeline.StatusRunning,
		ProvisionedBy: pipeline.ProvisionTypeConfig,
	}
	cfg1 := connector.Config{
		Name:     pipeline1.Connectors[0].Name,
		Settings: pipeline1.Connectors[0].Settings,
	}
	cfg2 := connector.Config{
		Name:     pipeline1.Connectors[1].Name,
		Settings: pipeline1.Connectors[1].Settings,
	}
	procCfg1 := processor.Config{
		Settings: pipeline1.Processors[0].Settings,
		Workers:  pipeline1.Processors[0].Workers,
	}
	procCfg2 := processor.Config{
		Settings: pipeline1.Connectors[1].Processors[0].Settings,
		Workers:  pipeline1.Connectors[1].Processors[0].Workers,
	}
	procParentPipeline := processor.Parent{
		ID:   pipeline1.ID,
		Type: processor.ParentTypePipeline,
	}
	procParentConn := processor.Parent{
		ID:   pipeline1.Connectors[1].ID,
		Type: processor.ParentTypeConnector,
	}

	pipelineService.EXPECT().List(gomock.Not(gomock.Nil()))
	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), pipeline1.Name).Return(nil, pipeline.ErrInstanceNotFound)
	pipelineService.EXPECT().Create(gomock.Not(gomock.Nil()), pipeline1.Name, pl1config, pipeline.ProvisionTypeConfig).Return(pl1, nil)
	pipelineService.EXPECT().UpdateDLQ(gomock.Not(gomock.Nil()), pl1.ID, pipeline.DLQ{WindowSize: 20, WindowNackThreshold: 10}).Return(pl1, nil)

	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:con1", connector.TypeSource, pipeline1.Connectors[0].Plugin, pipeline1.Name, cfg1, connector.ProvisionTypeConfig)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:con1")
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:con2", connector.TypeDestination, pipeline1.Connectors[1].Plugin, pipeline1.Name, cfg2, connector.ProvisionTypeConfig)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:con2")

	procService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:con2:proc1con", "js", procParentConn, procCfg2, processor.ProvisionTypeConfig)
	connService.EXPECT().AddProcessor(gomock.Not(gomock.Nil()), "pipeline1:con2", "pipeline1:con2:proc1con")
	procService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:proc1", "js", procParentPipeline, procCfg1, processor.ProvisionTypeConfig).Return(nil, cerrors.New("error"))
	pipelineService.EXPECT().AddProcessor(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:proc1")

	// rollback the creation of all entities
	procService.EXPECT().Delete(gomock.Not(gomock.Nil()), "pipeline1:proc1")
	procService.EXPECT().Delete(gomock.Not(gomock.Nil()), "pipeline1:con2:proc1con")
	connService.EXPECT().Delete(gomock.Not(gomock.Nil()), "pipeline1:con2", plugService)
	connService.EXPECT().Delete(gomock.Not(gomock.Nil()), "pipeline1:con1", plugService)
	pipelineService.EXPECT().Delete(gomock.Not(gomock.Nil()), pipeline1.Name)

	service := NewService(db, logger, pipelineService, connService, procService, plugService, "./test/pipelines1")
	err := service.Init(context.Background())
	is.True(err != nil)
}

func TestProvision_RollbackUpdate(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	pipelineService := mock.NewPipelineService(ctrl)
	connService := mock.NewConnectorService(ctrl)
	procService := mock.NewProcessorService(ctrl)
	plugService := mock.NewPluginService(ctrl)

	pl1config := pipeline.Config{Name: pipeline1.Name, Description: pipeline1.Description}
	pl1 := &pipeline.Instance{
		ID:            pipeline1.ID,
		Config:        pl1config,
		Status:        pipeline.StatusRunning,
		ProvisionedBy: pipeline.ProvisionTypeConfig,
		ConnectorIDs:  []string{"pipeline1:con1", "pipeline1:con2"},
		ProcessorIDs:  []string{"pipeline1:proc1"},
		DLQ: pipeline.DLQ{
			Plugin:              "builtin:file",
			Settings:            map[string]string{"path": "dlq.out"},
			WindowSize:          2,
			WindowNackThreshold: 1,
		},
	}
	cfg1 := connector.Config{
		Name:     pipeline1.Connectors[0].Name,
		Settings: pipeline1.Connectors[0].Settings,
	}
	procCfg := processor.Config{
		Settings: pipeline1.Processors[0].Settings,
	}
	procParentPipeline := processor.Parent{
		ID:   pipeline1.ID,
		Type: processor.ParentTypePipeline,
	}
	procParentConn := processor.Parent{
		ID:   "pipeline1:con2",
		Type: processor.ParentTypeConnector,
	}
	proc1Instance := &processor.Instance{
		ID:     "pipeline1:proc1",
		Parent: procParentPipeline,
		Config: procCfg,
		Type:   "js",
	}
	proc1conInstance := &processor.Instance{
		ID:     "pipeline1:con2:proc1con",
		Parent: procParentConn,
		Config: procCfg,
		Type:   "js",
	}
	source := &connector.Instance{
		ID:         "pipeline1:con1",
		Type:       connector.TypeSource,
		Plugin:     "builtin:file",
		PipelineID: pipeline1.ID,
		Config: connector.Config{
			Name:     pipeline1.Connectors[0].Name,
			Settings: map[string]string{"path": "my/path/file1.txt"},
		},
	}
	destination := &connector.Instance{
		ID:         "pipeline1:con2",
		Type:       connector.TypeDestination,
		Plugin:     "builtin:file",
		PipelineID: pipeline1.ID,
		Config: connector.Config{
			Name:     pipeline1.Connectors[1].Name,
			Settings: map[string]string{"path": "my/path/file2.txt"},
		},
		ProcessorIDs: []string{"pipeline1:con2:proc1con"},
	}

	wantErr := cerrors.New("err")

	pipelineService.EXPECT().List(gomock.Any())
	// pipeline exists
	pipelineService.EXPECT().Get(gomock.Any(), pipeline1.ID).Return(pl1, nil)

	// export pipeline
	connService.EXPECT().Get(gomock.Any(), "pipeline1:con1").Return(source, nil).AnyTimes()
	connService.EXPECT().Get(gomock.Any(), "pipeline1:con2").Return(destination, nil).AnyTimes()
	procService.EXPECT().Get(gomock.Any(), "pipeline1:con2:proc1con").Return(proc1conInstance, nil)
	procService.EXPECT().Get(gomock.Any(), "pipeline1:proc1").Return(proc1Instance, nil)

	// update pipeline
	pipelineService.EXPECT().Update(gomock.Any(), pipeline1.ID, pl1config).Return(pl1, nil)
	pipelineService.EXPECT().UpdateDLQ(gomock.Any(), pl1.ID, pipeline.DLQ{WindowSize: 20, WindowNackThreshold: 10})
	connService.EXPECT().Update(gomock.Any(), source.ID, cfg1).Return(source, nil)
	procService.EXPECT().Update(gomock.Any(), "pipeline1:con2:proc1con", processor.Config{
		Settings: pipeline1.Connectors[1].Processors[0].Settings,
		Workers:  pipeline1.Connectors[1].Processors[0].Workers,
	})
	procService.EXPECT().Update(gomock.Any(), "pipeline1:proc1", processor.Config{
		Settings: pipeline1.Processors[0].Settings,
		Workers:  pipeline1.Processors[0].Workers,
	}).Return(nil, wantErr) // fails

	// rollback changes
	procService.EXPECT().Update(gomock.Any(), "pipeline1:proc1", proc1Instance.Config)
	procService.EXPECT().Update(gomock.Any(), "pipeline1:con2:proc1con", proc1conInstance.Config)
	connService.EXPECT().Update(gomock.Any(), source.ID, source.Config).Return(source, nil)
	pipelineService.EXPECT().Update(gomock.Any(), pipeline1.ID, pl1.Config).Return(pl1, nil)
	pipelineService.EXPECT().UpdateDLQ(gomock.Any(), pl1.ID, pl1.DLQ)

	service := NewService(db, logger, pipelineService, connService, procService, plugService, "./test/pipelines1")
	err := service.Init(context.Background())
	is.True(cerrors.Is(err, wantErr))
}

func TestProvision_Update(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	pipelineService := mock.NewPipelineService(ctrl)
	connService := mock.NewConnectorService(ctrl)
	procService := mock.NewProcessorService(ctrl)
	plugService := mock.NewPluginService(ctrl)

	pl1config := pipeline.Config{Name: pipeline1.Name, Description: pipeline1.Description}
	pl1 := &pipeline.Instance{
		ID:            pipeline1.ID,
		Config:        pl1config,
		Status:        pipeline.StatusRunning,
		ProvisionedBy: pipeline.ProvisionTypeConfig,
		ConnectorIDs:  []string{"pipeline1:con1", "pipeline1:con2"},
		ProcessorIDs:  []string{"pipeline1:proc1"},
	}
	cfg1 := connector.Config{
		Name:     pipeline1.Connectors[0].Name,
		Settings: pipeline1.Connectors[0].Settings,
	}
	procCfg1 := processor.Config{
		Settings: pipeline1.Processors[0].Settings,
		Workers:  pipeline1.Processors[0].Workers,
	}
	procCfg2 := processor.Config{
		Settings: pipeline1.Connectors[1].Processors[0].Settings,
		Workers:  pipeline1.Connectors[1].Processors[0].Workers,
	}
	procParentPipeline := processor.Parent{
		ID:   pipeline1.ID,
		Type: processor.ParentTypePipeline,
	}
	procParentConn := processor.Parent{
		ID:   pipeline1.Connectors[1].ID,
		Type: processor.ParentTypeConnector,
	}
	proc1Instance := &processor.Instance{
		ID:     "pipeline1:proc1",
		Parent: procParentPipeline,
		Config: procCfg1,
		Type:   "js",
	}
	proc1conInstance := &processor.Instance{
		ID:     "pipeline1:con2:proc1con",
		Parent: procParentConn,
		Config: procCfg2,
		Type:   "js",
	}
	source := &connector.Instance{
		ID:         "pipeline1:con1",
		Type:       connector.TypeSource,
		Plugin:     "builtin:file",
		PipelineID: pipeline1.ID,
		Config: connector.Config{
			Name:     pipeline1.Connectors[0].Name,
			Settings: map[string]string{"path": "my/path/file1.txt"},
		},
		State: connector.SourceState{Position: record.Position("test-pos")},
	}
	destination := &connector.Instance{
		ID:         "pipeline1:con2",
		Type:       connector.TypeDestination,
		Plugin:     "builtin:file",
		PipelineID: pipeline1.ID,
		Config: connector.Config{
			Name:     pipeline1.Connectors[1].Name,
			Settings: map[string]string{"path": "my/path/file2.txt"},
		},
		State:        connector.DestinationState{Positions: map[string]record.Position{"pipeline1:con1": record.Position("test-pos")}},
		ProcessorIDs: []string{"pipeline1:con2:proc1con"},
	}

	pipelineService.EXPECT().List(gomock.Not(gomock.Nil()))
	// pipeline already exists
	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), pipeline1.ID).Return(pl1, nil)

	// export pipeline
	connService.EXPECT().Get(gomock.Any(), "pipeline1:con1").Return(source, nil)
	connService.EXPECT().Get(gomock.Any(), "pipeline1:con2").Return(destination, nil)
	procService.EXPECT().Get(gomock.Any(), "pipeline1:con2:proc1con").Return(proc1conInstance, nil)
	procService.EXPECT().Get(gomock.Any(), "pipeline1:proc1").Return(proc1Instance, nil)

	// update pipeline
	pipelineService.EXPECT().Update(gomock.Any(), pipeline1.ID, pl1config).Return(pl1, nil)
	pipelineService.EXPECT().UpdateDLQ(gomock.Any(), pl1.ID, pipeline.DLQ{WindowSize: 20, WindowNackThreshold: 10})
	connService.EXPECT().Update(gomock.Any(), source.ID, cfg1).Return(source, nil)

	// start pipeline
	pipelineService.EXPECT().Start(gomock.Not(gomock.Nil()), connService, procService, plugService, pipeline1.Name)

	service := NewService(db, logger, pipelineService, connService, procService, plugService, "./test/pipelines1")
	err := service.Init(context.Background())
	is.NoErr(err)
}

func TestProvision_MultiplePipelines(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	pipelineService := mock.NewPipelineService(ctrl)
	connService := mock.NewConnectorService(ctrl)
	procService := mock.NewProcessorService(ctrl)
	plugService := mock.NewPluginService(ctrl)

	pl2config := pipeline.Config{Name: pipeline2.Name, Description: pipeline2.Description}
	pl3config := pipeline.Config{Name: pipeline3.Name, Description: pipeline3.Description}
	cfg2con1 := connector.Config{
		Name:     pipeline2.Connectors[0].Name,
		Settings: pipeline2.Connectors[0].Settings,
	}
	cfg2con2 := connector.Config{
		Name:     pipeline2.Connectors[1].Name,
		Settings: pipeline2.Connectors[1].Settings,
	}
	cfg3con1 := connector.Config{
		Name:     pipeline3.Connectors[0].Name,
		Settings: pipeline3.Connectors[0].Settings,
	}
	cfg3con2 := connector.Config{
		Name:     pipeline3.Connectors[1].Name,
		Settings: pipeline3.Connectors[1].Settings,
	}

	pipelineService.EXPECT().List(gomock.Not(gomock.Nil()))
	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), pipeline2.ID).Return(nil, pipeline.ErrInstanceNotFound)
	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), pipeline3.ID).Return(nil, pipeline.ErrInstanceNotFound)

	pipelineService.EXPECT().Create(gomock.Not(gomock.Nil()), pipeline2.ID, pl2config, pipeline.ProvisionTypeConfig)
	pipelineService.EXPECT().UpdateDLQ(gomock.Any(), pipeline2.ID, pipeline.DefaultDLQ)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline2.ID, "pipeline2:con1")
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline2.ID, "pipeline2:con2")
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline2:con1", connector.TypeSource, pipeline2.Connectors[0].Plugin, pipeline2.ID, cfg2con1, connector.ProvisionTypeConfig)
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline2:con2", connector.TypeDestination, pipeline2.Connectors[1].Plugin, pipeline2.ID, cfg2con2, connector.ProvisionTypeConfig)

	pipelineService.EXPECT().Start(gomock.Not(gomock.Nil()), connService, procService, plugService, pipeline2.ID)

	pipelineService.EXPECT().Create(gomock.Not(gomock.Nil()), pipeline3.ID, pl3config, pipeline.ProvisionTypeConfig)
	pipelineService.EXPECT().UpdateDLQ(gomock.Any(), pipeline3.ID, pipeline.DefaultDLQ)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline3.ID, "pipeline3:con1")
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline3.ID, "pipeline3:con2")
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline3:con1", connector.TypeSource, pipeline3.Connectors[0].Plugin, pipeline3.ID, cfg3con1, connector.ProvisionTypeConfig)
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline3:con2", connector.TypeDestination, pipeline3.Connectors[1].Plugin, pipeline3.ID, cfg3con2, connector.ProvisionTypeConfig)

	pipelineService.EXPECT().Start(gomock.Not(gomock.Nil()), connService, procService, plugService, pipeline3.ID)

	service := NewService(db, logger, pipelineService, connService, procService, plugService, "./test/pipelines3")
	err := service.Init(context.Background())
	is.NoErr(err)
}

func TestProvision_MultiplePipelinesDuplicatedPipelineID(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	pipelineService := mock.NewPipelineService(ctrl)
	connService := mock.NewConnectorService(ctrl)
	procService := mock.NewProcessorService(ctrl)
	plugService := mock.NewPluginService(ctrl)

	pl2config := pipeline.Config{Name: pipeline2.Name, Description: pipeline2.Description}
	pl2 := &pipeline.Instance{
		ID:            pipeline2.Name,
		Config:        pl2config,
		Status:        pipeline.StatusRunning,
		ProvisionedBy: pipeline.ProvisionTypeConfig,
	}
	cfg2con1 := connector.Config{
		Name:     pipeline2.Connectors[0].Name,
		Settings: pipeline2.Connectors[0].Settings,
	}
	cfg2con2 := connector.Config{
		Name:     pipeline2.Connectors[1].Name,
		Settings: pipeline2.Connectors[1].Settings,
	}

	pipelineService.EXPECT().List(gomock.Not(gomock.Nil()))
	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), pipeline2.Name).Return(nil, pipeline.ErrInstanceNotFound)

	pipelineService.EXPECT().Create(gomock.Not(gomock.Nil()), pipeline2.Name, pl2config, pipeline.ProvisionTypeConfig).Return(pl2, nil)
	pipelineService.EXPECT().UpdateDLQ(gomock.Any(), pipeline2.ID, pipeline.DefaultDLQ)
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline2:con1", connector.TypeSource, pipeline2.Connectors[0].Plugin, pipeline2.Name, cfg2con1, connector.ProvisionTypeConfig)
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline2:con2", connector.TypeDestination, pipeline2.Connectors[1].Plugin, pipeline2.Name, cfg2con2, connector.ProvisionTypeConfig)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline2.Name, "pipeline2:con1")
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline2.Name, "pipeline2:con2")

	pipelineService.EXPECT().Start(gomock.Not(gomock.Nil()), connService, procService, plugService, pipeline2.Name)

	service := NewService(db, logger, pipelineService, connService, procService, plugService, "./test/pipelines5")
	err := service.Init(context.Background())
	is.True(cerrors.Is(err, ErrDuplicatedPipelineID)) // duplicated pipeline id
}

func TestProvision_IntegrationTestServices(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()

	logger := log.InitLogger(zerolog.InfoLevel, log.FormatCLI)
	logger = logger.CtxHook(ctxutil.MessageIDLogCtxHook{})

	db, err := badger.New(logger.Logger, t.TempDir()+"/test.db")
	assert.Ok(t, err)
	t.Cleanup(func() {
		err := db.Close()
		assert.Ok(t, err)
	})

	pluginService := plugin.NewService(
		logger,
		builtin.NewRegistry(logger, builtin.DefaultDispenserFactories),
		standalone.NewRegistry(logger, ""),
	)

	plService := pipeline.NewService(logger, db)
	connService := connector.NewService(logger, db, connector.NewPersister(logger, db, time.Second, 3))
	procService := processor.NewService(logger, db, processor.GlobalBuilderRegistry)

	// add builtin processor for removing metadata
	// TODO at the time of writing we don't have a processor for manipulating
	//  metadata, once we have it we can use it instead of adding our own
	processor.GlobalBuilderRegistry.MustRegister("removereadat", func(config processor.Config) (processor.Interface, error) {
		return procbuiltin.NewFuncWrapper(func(ctx context.Context, r record.Record) (record.Record, error) {
			delete(r.Metadata, record.MetadataReadAt) // read at is different every time, remove it
			return r, nil
		}), nil
	})

	// create destination file
	destFile := "./test/dest-file.txt"
	_, err = os.Create(destFile)
	is.NoErr(err)
	defer func() {
		err := os.Remove(destFile)
		if err != nil {
			return
		}
	}()

	service := NewService(db, logger, plService, connService, procService, pluginService, "./test/pipelines4-integration-test")
	err = service.Init(context.Background())
	is.NoErr(err)

	// give the pipeline time to run through
	time.Sleep(1 * time.Second)

	// checking pipelines
	pipelines := []*pipeline.Instance{
		{
			ID: pipeline1.ID,
			Config: pipeline.Config{
				Name:        pipeline1.Name,
				Description: "desc1",
			},
			Status:        pipeline.StatusRunning,
			ProvisionedBy: pipeline.ProvisionTypeConfig,
			ConnectorIDs:  []string{"pipeline1:con1", "pipeline1:con2"},
			ProcessorIDs:  []string{"pipeline1:proc1"},
		},
		{
			ID: pipeline2.ID,
			Config: pipeline.Config{
				Name:        pipeline2.Name,
				Description: "desc2",
			},
			Status:        pipeline.StatusUserStopped,
			ProvisionedBy: pipeline.ProvisionTypeConfig,
			ConnectorIDs:  []string{"pipeline2:con3"},
		},
		{
			ID: pipeline3.ID,
			Config: pipeline.Config{
				Name:        pipeline3.Name,
				Description: "empty",
			},
			Status:        pipeline.StatusUserStopped,
			ProvisionedBy: pipeline.ProvisionTypeConfig,
		},
	}
	for _, pl := range pipelines {
		gotPl, err := plService.Get(ctx, pl.ID)
		is.NoErr(err)
		is.Equal(gotPl.Config, pl.Config)
		is.Equal(gotPl.Status, pl.Status)
		is.Equal(gotPl.ProvisionedBy, pl.ProvisionedBy)
		sort.Strings(gotPl.ConnectorIDs)
		is.Equal(gotPl.ConnectorIDs, pl.ConnectorIDs)
		is.Equal(gotPl.ProcessorIDs, pl.ProcessorIDs)
	}

	// checking processors
	processors := []*processor.Instance{
		{
			ID:            "pipeline1:proc1",
			ProvisionedBy: processor.ProvisionTypeConfig,
			Type:          "removereadat",
			Parent: processor.Parent{
				ID:   pipeline1.ID,
				Type: processor.ParentTypePipeline,
			},
			Config: processor.Config{
				Workers: 1,
			},
		},
		{
			ID:            "pipeline1:con2:con2proc1",
			ProvisionedBy: processor.ProvisionTypeConfig,
			Type:          "removereadat",
			Parent: processor.Parent{
				ID:   "pipeline1:con2",
				Type: processor.ParentTypeConnector,
			},
			Config: processor.Config{
				Workers: 1,
			},
		},
	}

	for _, proc := range processors {
		gotProc, err := procService.Get(ctx, proc.ID)
		is.NoErr(err)
		gotProc.CreatedAt = proc.CreatedAt
		gotProc.UpdatedAt = proc.UpdatedAt
		gotProc.Processor = proc.Processor
		is.Equal(gotProc, proc)
	}

	// checking connectors
	wantConn1 := &connector.Instance{
		ID:         "pipeline1:con1",
		Type:       connector.TypeSource,
		Plugin:     "builtin:file",
		PipelineID: pipeline1.ID,
		Config: connector.Config{
			Name: "file-src",
			Settings: map[string]string{
				"path": "./test/source-file.txt",
			},
		},
	}
	wantConn2 := &connector.Instance{
		ID:         "pipeline1:con2",
		Type:       connector.TypeDestination,
		Plugin:     "builtin:file",
		PipelineID: pipeline1.ID,
		Config: connector.Config{
			Name: "file-dest",
			Settings: map[string]string{
				"path": destFile,
			},
		},
		ProcessorIDs: []string{"pipeline1:con2:con2proc1"},
	}
	wantConn3 := &connector.Instance{
		ID:         "pipeline2:con3",
		Type:       connector.TypeDestination,
		Plugin:     "builtin:file",
		PipelineID: pipeline2.Name,
		Config: connector.Config{
			Name: "file-dest",
			Settings: map[string]string{
				"path": "./test/file3.txt",
			},
		},
	}
	// assert pipeline1:con1
	gotConn1, err := connService.Get(ctx, "pipeline1:con1")
	is.NoErr(err)
	is.Equal(gotConn1.ID, wantConn1.ID)
	is.Equal(gotConn1.Type, wantConn1.Type)
	is.Equal(gotConn1.Plugin, wantConn1.Plugin)
	is.Equal(gotConn1.PipelineID, wantConn1.PipelineID)
	is.Equal(gotConn1.Config, wantConn1.Config)
	// assert pipeline1:con2
	gotConn2, err := connService.Get(ctx, "pipeline1:con2")
	is.NoErr(err)
	is.Equal(gotConn2.ID, wantConn2.ID)
	is.Equal(gotConn2.Type, wantConn2.Type)
	is.Equal(gotConn2.Plugin, wantConn2.Plugin)
	is.Equal(gotConn2.PipelineID, wantConn2.PipelineID)
	is.Equal(gotConn2.Config, wantConn2.Config)
	// assert con3
	gotConn3, err := connService.Get(ctx, "pipeline2:con3")
	is.NoErr(err)
	is.Equal(gotConn3.ID, wantConn3.ID)
	is.Equal(gotConn3.Type, wantConn3.Type)
	is.Equal(gotConn3.Plugin, wantConn3.Plugin)
	is.Equal(gotConn3.PipelineID, wantConn3.PipelineID)
	is.Equal(gotConn3.Config, wantConn3.Config)

	data, err := os.ReadFile(destFile)
	is.NoErr(err)
	is.True(len(data) != 0) // destination file is empty
}
