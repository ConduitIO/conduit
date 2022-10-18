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
	connmock "github.com/conduitio/conduit/pkg/connector/mock"
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
	"github.com/conduitio/conduit/pkg/provisioning/mock"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/golang/mock/gomock"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

var pipeline1 = PipelineConfig{
	Status:      "running",
	Name:        "pipeline1",
	Description: "desc1",
	Connectors: map[string]ConnectorConfig{
		"pipeline1:con1": {
			Type:   "source",
			Plugin: "builtin:file",
			Name:   "pipeline1:source",
			Settings: map[string]string{
				"path": "my/path/file1.txt",
			},
		},
		"pipeline1:con2": {
			Type:   "destination",
			Plugin: "builtin:file",
			Name:   "pipeline1:dest",
			Settings: map[string]string{
				"path": "my/path/file2.txt",
			},
			Processors: map[string]ProcessorConfig{
				"pipeline1:con2:proc1con": {
					Type: "js",
					Settings: map[string]string{
						"additionalProp1": "string",
					},
				},
			},
		},
	},
	Processors: map[string]ProcessorConfig{
		"pipeline1:proc1": {
			Type: "js",
			Settings: map[string]string{
				"additionalProp1": "string",
			},
		},
	},
}

var pipeline2 = PipelineConfig{
	Status:      "running",
	Name:        "pipeline2",
	Description: "desc2",
	Connectors: map[string]ConnectorConfig{
		"pipeline2:con1": {
			Type:   "source",
			Plugin: "builtin:file",
			Name:   "pipeline2:source",
			Settings: map[string]string{
				"path": "my/path/file1.txt",
			},
		},
		"pipeline2:con2": {
			Type:   "destination",
			Plugin: "builtin:file",
			Name:   "pipeline2:dest",
			Settings: map[string]string{
				"path": "my/path/file2.txt",
			},
		},
	},
}

var pipeline3 = PipelineConfig{
	Status:      "running",
	Name:        "pipeline3",
	Description: "desc3",
	Connectors: map[string]ConnectorConfig{
		"pipeline3:con1": {
			Type:   "source",
			Plugin: "builtin:file",
			Name:   "pipeline3:source",
			Settings: map[string]string{
				"path": "my/path/file1.txt",
			},
		},
		"pipeline3:con2": {
			Type:   "destination",
			Plugin: "builtin:file",
			Name:   "pipeline3:dest",
			Settings: map[string]string{
				"path": "my/path/file2.txt",
			},
		},
	},
}

func TestProvision_PipelineWithConnectorsAndProcessors(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	pipelineService := mock.NewPipelineService(ctrl)
	connService := mock.NewConnectorService(ctrl)
	procService := mock.NewProcessorService(ctrl)

	pl1config := pipeline.Config{Name: pipeline1.Name, Description: pipeline1.Description}
	pl1 := &pipeline.Instance{
		ID:            pipeline1.Name,
		Config:        pl1config,
		Status:        pipeline.StatusRunning,
		ProvisionedBy: pipeline.ProvisionTypeConfig,
		ConnectorIDs:  []string{"pipeline1:con1", "pipeline1:con2"},
	}
	cfg1 := connector.Config{
		Name:       pipeline1.Connectors["pipeline1:con1"].Name,
		Plugin:     pipeline1.Connectors["pipeline1:con1"].Plugin,
		PipelineID: pipeline1.Name,
		Settings:   pipeline1.Connectors["pipeline1:con1"].Settings,
	}
	cfg2 := connector.Config{
		Name:       pipeline1.Connectors["pipeline1:con2"].Name,
		Plugin:     pipeline1.Connectors["pipeline1:con2"].Plugin,
		PipelineID: pipeline1.Name,
		Settings:   pipeline1.Connectors["pipeline1:con2"].Settings,
	}
	procCfg := processor.Config{
		Settings: pipeline1.Processors["pipeline1:proc1"].Settings,
	}
	procParentPipeline := processor.Parent{
		ID:   pipeline1.Name,
		Type: processor.ParentTypePipeline,
	}
	procParentConn := processor.Parent{
		ID:   "pipeline1:con2",
		Type: processor.ParentTypeConnector,
	}

	pipelineService.EXPECT().List(gomock.Not(gomock.Nil()))
	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), pipeline1.Name).Return(nil, pipeline.ErrInstanceNotFound)
	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), pipeline1.Name).Return(pl1, nil)
	pipelineService.
		EXPECT().
		Create(gomock.Not(gomock.Nil()), pipeline1.Name, pl1config, pipeline.ProvisionTypeConfig).
		Return(pl1, nil)

	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:con1", connector.TypeSource, cfg1, connector.ProvisionTypeConfig)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:con1")
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:con2", connector.TypeDestination, cfg2, connector.ProvisionTypeConfig)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:con2")

	procService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:con2:proc1con", "js", procParentConn, procCfg, processor.ProvisionTypeConfig)
	connService.EXPECT().AddProcessor(gomock.Not(gomock.Nil()), "pipeline1:con2", "pipeline1:con2:proc1con")
	procService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:proc1", "js", procParentPipeline, procCfg, processor.ProvisionTypeConfig)
	pipelineService.EXPECT().AddProcessor(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:proc1")

	pipelineService.EXPECT().Start(gomock.Not(gomock.Nil()), connService, procService, pipeline1.Name)

	service := NewService(db, logger, pipelineService, connService, procService, "./test/pipelines1")
	err := service.Init(context.Background())
	is.NoErr(err)
}

func TestProvision_Rollback(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	pipelineService := mock.NewPipelineService(ctrl)
	connService := mock.NewConnectorService(ctrl)
	procService := mock.NewProcessorService(ctrl)

	pl1config := pipeline.Config{Name: pipeline1.Name, Description: pipeline1.Description}
	pl1 := &pipeline.Instance{
		ID:            pipeline1.Name,
		Config:        pl1config,
		Status:        pipeline.StatusRunning,
		ProvisionedBy: pipeline.ProvisionTypeConfig,
	}
	cfg1 := connector.Config{
		Name:       pipeline1.Connectors["pipeline1:con1"].Name,
		Plugin:     pipeline1.Connectors["pipeline1:con1"].Plugin,
		PipelineID: pipeline1.Name,
		Settings:   pipeline1.Connectors["pipeline1:con1"].Settings,
	}
	cfg2 := connector.Config{
		Name:       pipeline1.Connectors["pipeline1:con2"].Name,
		Plugin:     pipeline1.Connectors["pipeline1:con2"].Plugin,
		PipelineID: pipeline1.Name,
		Settings:   pipeline1.Connectors["pipeline1:con2"].Settings,
	}
	procCfg := processor.Config{
		Settings: pipeline1.Processors["pipeline1:proc1"].Settings,
	}
	procParentPipeline := processor.Parent{
		ID:   pipeline1.Name,
		Type: processor.ParentTypePipeline,
	}
	procParentConn := processor.Parent{
		ID:   "pipeline1:con2",
		Type: processor.ParentTypeConnector,
	}

	pipelineService.EXPECT().List(gomock.Not(gomock.Nil()))
	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), pipeline1.Name).Return(nil, pipeline.ErrInstanceNotFound)
	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), pipeline1.Name).Return(pl1, nil)
	pipelineService.EXPECT().Create(gomock.Not(gomock.Nil()), pipeline1.Name, pl1config, pipeline.ProvisionTypeConfig).Return(pl1, nil)
	pipelineService.EXPECT().Delete(gomock.Not(gomock.Nil()), pipeline1.Name)

	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:con1", connector.TypeSource, cfg1, connector.ProvisionTypeConfig)
	connService.EXPECT().Delete(gomock.Not(gomock.Nil()), "pipeline1:con1")
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:con1")
	pipelineService.EXPECT().RemoveConnector(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:con1")
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:con2", connector.TypeDestination, cfg2, connector.ProvisionTypeConfig)
	connService.EXPECT().Delete(gomock.Not(gomock.Nil()), "pipeline1:con2")
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:con2")
	pipelineService.EXPECT().RemoveConnector(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:con2")

	procService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:con2:proc1con", "js", procParentConn, procCfg, processor.ProvisionTypeConfig)
	procService.EXPECT().Delete(gomock.Not(gomock.Nil()), "pipeline1:con2:proc1con")
	connService.EXPECT().AddProcessor(gomock.Not(gomock.Nil()), "pipeline1:con2", "pipeline1:con2:proc1con")
	connService.EXPECT().RemoveProcessor(gomock.Not(gomock.Nil()), "pipeline1:con2", "pipeline1:con2:proc1con")
	procService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:proc1", "js", procParentPipeline, procCfg, processor.ProvisionTypeConfig)
	procService.EXPECT().Delete(gomock.Not(gomock.Nil()), "pipeline1:proc1")
	pipelineService.EXPECT().AddProcessor(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:proc1")
	pipelineService.EXPECT().RemoveProcessor(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:proc1")

	// returns an error, so rollback needed
	pipelineService.EXPECT().Start(gomock.Not(gomock.Nil()), connService, procService, pipeline1.Name).Return(cerrors.New("error"))

	service := NewService(db, logger, pipelineService, connService, procService, "./test/pipelines1")
	err := service.Init(context.Background())
	is.True(err != nil)
}

func TestProvision_RollbackDeletePipeline(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	pipelineService := mock.NewPipelineService(ctrl)
	connService := mock.NewConnectorService(ctrl)
	procService := mock.NewProcessorService(ctrl)
	connBuilder := connmock.Builder{Ctrl: ctrl}

	pl1config := pipeline.Config{Name: pipeline1.Name, Description: pipeline1.Description}
	pl1 := &pipeline.Instance{
		ID:            pipeline1.Name,
		Config:        pl1config,
		Status:        pipeline.StatusRunning,
		ProvisionedBy: pipeline.ProvisionTypeConfig,
		ConnectorIDs:  []string{"pipeline1:con1", "pipeline1:con2"},
		ProcessorIDs:  []string{"pipeline1:proc1", "pipeline1:con2:proc1con"},
	}
	cfg1 := connector.Config{
		Name:       pipeline1.Connectors["pipeline1:con1"].Name,
		Plugin:     pipeline1.Connectors["pipeline1:con1"].Plugin,
		PipelineID: pipeline1.Name,
		Settings:   pipeline1.Connectors["pipeline1:con1"].Settings,
	}
	cfg2 := connector.Config{
		Name:       pipeline1.Connectors["pipeline1:con2"].Name,
		Plugin:     pipeline1.Connectors["pipeline1:con2"].Plugin,
		PipelineID: pipeline1.Name,
		Settings:   pipeline1.Connectors["pipeline1:con2"].Settings,
	}
	procCfg := processor.Config{
		Settings: pipeline1.Processors["pipeline1:proc1"].Settings,
	}
	procParentPipeline := processor.Parent{
		ID:   pipeline1.Name,
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
	sourceConfig := connector.Config{
		Name:       pipeline1.Connectors["pipeline1:con1"].Name,
		Settings:   map[string]string{"path": "my/path/file1.txt"},
		Plugin:     "builtin:file",
		PipelineID: pipeline1.Name,
	}
	destConfig := connector.Config{
		Name:       pipeline1.Connectors["pipeline1:con2"].Name,
		Settings:   map[string]string{"path": "my/path/file2.txt"},
		Plugin:     "builtin:file",
		PipelineID: pipeline1.Name,
	}

	source := connBuilder.NewSourceMock("pipeline1:con1", sourceConfig)
	destination := connBuilder.NewDestinationMock("pipeline1:con2", destConfig)

	pipelineService.EXPECT().List(gomock.Not(gomock.Nil()))
	// pipeline exists
	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), pipeline1.Name).Return(pl1, nil)
	pipelineService.EXPECT().Stop(gomock.Not(gomock.Nil()), pipeline1.Name)
	pipelineService.EXPECT().Delete(gomock.Not(gomock.Nil()), pipeline1.Name)

	connService.EXPECT().Get(gomock.Not(gomock.Nil()), "pipeline1:con1").Return(source, nil).AnyTimes()
	connService.EXPECT().Delete(gomock.Not(gomock.Nil()), "pipeline1:con1")
	pipelineService.EXPECT().RemoveConnector(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:con1")
	connService.EXPECT().Get(gomock.Not(gomock.Nil()), "pipeline1:con2").Return(destination, nil).AnyTimes()
	connService.EXPECT().Delete(gomock.Not(gomock.Nil()), "pipeline1:con2")
	pipelineService.EXPECT().RemoveConnector(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:con2")

	procService.EXPECT().Get(gomock.Not(gomock.Nil()), "pipeline1:con2:proc1con").Return(proc1conInstance, nil)
	procService.EXPECT().Delete(gomock.Not(gomock.Nil()), "pipeline1:con2:proc1con")
	connService.EXPECT().RemoveProcessor(gomock.Not(gomock.Nil()), "pipeline1:con2", "pipeline1:con2:proc1con")
	procService.EXPECT().Get(gomock.Not(gomock.Nil()), "pipeline1:proc1").Return(proc1Instance, nil)
	procService.EXPECT().Delete(gomock.Not(gomock.Nil()), "pipeline1:proc1")
	pipelineService.EXPECT().RemoveProcessor(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:proc1")

	// create new pipeline failed, rollback
	pipelineService.EXPECT().Create(gomock.Not(gomock.Nil()), pipeline1.Name, pl1config, pipeline.ProvisionTypeConfig).Return(nil, cerrors.New("err"))

	// create old processor
	pipelineService.EXPECT().Create(gomock.Not(gomock.Nil()), pipeline1.Name, pl1config, pipeline.ProvisionTypeConfig).Return(pl1, nil)

	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:con1", connector.TypeSource, cfg1, connector.ProvisionTypeConfig)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:con1")
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:con2", connector.TypeDestination, cfg2, connector.ProvisionTypeConfig)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:con2")
	source.EXPECT().State()
	connService.EXPECT().SetSourceState(gomock.Not(gomock.Nil()), "pipeline1:con1", connector.SourceState{})
	destination.EXPECT().State()
	connService.EXPECT().SetDestinationState(gomock.Not(gomock.Nil()), "pipeline1:con2", connector.DestinationState{})

	procService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:con2:proc1con", "js", procParentConn, procCfg, processor.ProvisionTypeConfig)
	connService.EXPECT().AddProcessor(gomock.Not(gomock.Nil()), "pipeline1:con2", "pipeline1:con2:proc1con")
	procService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:proc1", "js", procParentPipeline, procCfg, processor.ProvisionTypeConfig)
	pipelineService.EXPECT().AddProcessor(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:proc1")

	pipelineService.EXPECT().Start(gomock.Not(gomock.Nil()), connService, procService, pipeline1.Name)

	service := NewService(db, logger, pipelineService, connService, procService, "./test/pipelines1")
	err := service.Init(context.Background())
	is.True(err != nil)
}

func TestProvision_ExistingPipeline(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	pipelineService := mock.NewPipelineService(ctrl)
	connService := mock.NewConnectorService(ctrl)
	procService := mock.NewProcessorService(ctrl)
	connBuilder := connmock.Builder{Ctrl: ctrl}

	pl1config := pipeline.Config{Name: pipeline1.Name, Description: pipeline1.Description}
	pl1 := &pipeline.Instance{
		ID:            pipeline1.Name,
		Config:        pl1config,
		Status:        pipeline.StatusRunning,
		ProvisionedBy: pipeline.ProvisionTypeConfig,
		ConnectorIDs:  []string{"pipeline1:con1", "pipeline1:con2"},
		ProcessorIDs:  []string{"pipeline1:proc1", "pipeline1:con2:proc1con"},
	}
	cfg1 := connector.Config{
		Name:       pipeline1.Connectors["pipeline1:con1"].Name,
		Plugin:     pipeline1.Connectors["pipeline1:con1"].Plugin,
		PipelineID: pipeline1.Name,
		Settings:   pipeline1.Connectors["pipeline1:con1"].Settings,
	}
	cfg2 := connector.Config{
		Name:       pipeline1.Connectors["pipeline1:con2"].Name,
		Plugin:     pipeline1.Connectors["pipeline1:con2"].Plugin,
		PipelineID: pipeline1.Name,
		Settings:   pipeline1.Connectors["pipeline1:con2"].Settings,
	}
	procCfg := processor.Config{
		Settings: pipeline1.Processors["pipeline1:proc1"].Settings,
	}
	procParentPipeline := processor.Parent{
		ID:   pipeline1.Name,
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
	sourceConfig := connector.Config{
		Name:       "pipeline1:con1",
		Settings:   map[string]string{"path": "my/path/file1.txt"},
		Plugin:     "builtin:file",
		PipelineID: pipeline1.Name,
	}
	destConfig := connector.Config{
		Name:       "pipeline1:con2",
		Settings:   map[string]string{"path": "my/path/file2.txt"},
		Plugin:     "builtin:file",
		PipelineID: pipeline1.Name,
	}

	source := connBuilder.NewSourceMock("pipeline1:con1", sourceConfig)
	destination := connBuilder.NewDestinationMock("pipeline1:con2", destConfig)

	pipelineService.EXPECT().List(gomock.Not(gomock.Nil()))
	// pipeline already exists
	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), pipeline1.Name).Return(pl1, nil).Times(2)
	pipelineService.EXPECT().Stop(gomock.Not(gomock.Nil()), pipeline1.Name)

	// copy over connectors states
	connService.EXPECT().Get(gomock.Not(gomock.Nil()), "pipeline1:con1").Return(source, nil).AnyTimes()
	source.EXPECT().State()
	connService.EXPECT().SetSourceState(gomock.Not(gomock.Nil()), "pipeline1:con1", connector.SourceState{})
	connService.EXPECT().Get(gomock.Not(gomock.Nil()), "pipeline1:con2").Return(destination, nil).AnyTimes()
	destination.EXPECT().State()
	connService.EXPECT().SetDestinationState(gomock.Not(gomock.Nil()), "pipeline1:con2", connector.DestinationState{})

	// delete old pipeline
	pipelineService.EXPECT().Delete(gomock.Not(gomock.Nil()), pipeline1.Name)
	pipelineService.EXPECT().RemoveConnector(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:con1")
	connService.EXPECT().Delete(gomock.Not(gomock.Nil()), "pipeline1:con1")
	pipelineService.EXPECT().RemoveConnector(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:con2")
	connService.EXPECT().Delete(gomock.Not(gomock.Nil()), "pipeline1:con2")
	connService.EXPECT().RemoveProcessor(gomock.Not(gomock.Nil()), "pipeline1:con2", "pipeline1:con2:proc1con")
	procService.EXPECT().Get(gomock.Not(gomock.Nil()), "pipeline1:con2:proc1con").Return(proc1conInstance, nil)
	procService.EXPECT().Delete(gomock.Not(gomock.Nil()), "pipeline1:con2:proc1con")
	pipelineService.EXPECT().RemoveProcessor(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:proc1")
	procService.EXPECT().Get(gomock.Not(gomock.Nil()), "pipeline1:proc1").Return(proc1Instance, nil)
	procService.EXPECT().Delete(gomock.Not(gomock.Nil()), "pipeline1:proc1")

	// create new pipeline
	pipelineService.
		EXPECT().
		Create(gomock.Not(gomock.Nil()), pipeline1.Name, pl1config, pipeline.ProvisionTypeConfig).
		Return(pl1, nil)

	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:con1", connector.TypeSource, cfg1, connector.ProvisionTypeConfig).Return(source, nil)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:con1")
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:con2", connector.TypeDestination, cfg2, connector.ProvisionTypeConfig).Return(destination, nil)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:con2")

	procService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:con2:proc1con", "js", procParentConn, procCfg, processor.ProvisionTypeConfig)
	connService.EXPECT().AddProcessor(gomock.Not(gomock.Nil()), "pipeline1:con2", "pipeline1:con2:proc1con")
	procService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1:proc1", "js", procParentPipeline, procCfg, processor.ProvisionTypeConfig)
	pipelineService.EXPECT().AddProcessor(gomock.Not(gomock.Nil()), pipeline1.Name, "pipeline1:proc1")

	pipelineService.EXPECT().Start(gomock.Not(gomock.Nil()), connService, procService, pipeline1.Name)

	service := NewService(db, logger, pipelineService, connService, procService, "./test/pipelines1")
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

	pl2config := pipeline.Config{Name: pipeline2.Name, Description: pipeline2.Description}
	pl2 := &pipeline.Instance{
		ID:            pipeline2.Name,
		Config:        pl2config,
		Status:        pipeline.StatusRunning,
		ProvisionedBy: pipeline.ProvisionTypeConfig,
	}
	pl3config := pipeline.Config{Name: pipeline3.Name, Description: pipeline3.Description}
	pl3 := &pipeline.Instance{
		ID:            pipeline3.Name,
		Config:        pl3config,
		Status:        pipeline.StatusRunning,
		ProvisionedBy: pipeline.ProvisionTypeConfig,
	}
	cfg2con1 := connector.Config{
		Name:       pipeline2.Connectors["pipeline2:con1"].Name,
		Plugin:     pipeline2.Connectors["pipeline2:con1"].Plugin,
		PipelineID: pipeline2.Name,
		Settings:   pipeline2.Connectors["pipeline2:con1"].Settings,
	}
	cfg2con2 := connector.Config{
		Name:       pipeline2.Connectors["pipeline2:con2"].Name,
		Plugin:     pipeline2.Connectors["pipeline2:con2"].Plugin,
		PipelineID: pipeline2.Name,
		Settings:   pipeline2.Connectors["pipeline2:con2"].Settings,
	}
	cfg3con1 := connector.Config{
		Name:       pipeline3.Connectors["pipeline3:con1"].Name,
		Plugin:     pipeline3.Connectors["pipeline3:con1"].Plugin,
		PipelineID: pipeline3.Name,
		Settings:   pipeline3.Connectors["pipeline3:con1"].Settings,
	}
	cfg3con2 := connector.Config{
		Name:       pipeline3.Connectors["pipeline3:con2"].Name,
		Plugin:     pipeline3.Connectors["pipeline3:con2"].Plugin,
		PipelineID: pipeline3.Name,
		Settings:   pipeline3.Connectors["pipeline3:con2"].Settings,
	}

	pipelineService.EXPECT().List(gomock.Not(gomock.Nil()))
	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), pipeline2.Name).Return(nil, pipeline.ErrInstanceNotFound)
	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), pipeline3.Name).Return(nil, pipeline.ErrInstanceNotFound)
	pipelineService.
		EXPECT().
		Create(gomock.Not(gomock.Nil()), pipeline2.Name, pl2config, pipeline.ProvisionTypeConfig).
		Return(pl2, nil)
	pipelineService.
		EXPECT().
		Create(gomock.Not(gomock.Nil()), pipeline3.Name, pl3config, pipeline.ProvisionTypeConfig).
		Return(pl3, nil)

	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline2:con1", connector.TypeSource, cfg2con1, connector.ProvisionTypeConfig)
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline2:con2", connector.TypeDestination, cfg2con2, connector.ProvisionTypeConfig)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline2.Name, "pipeline2:con1")
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline2.Name, "pipeline2:con2")
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline3:con1", connector.TypeSource, cfg3con1, connector.ProvisionTypeConfig)
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline3:con2", connector.TypeDestination, cfg3con2, connector.ProvisionTypeConfig)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline3.Name, "pipeline3:con1")
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline3.Name, "pipeline3:con2")

	pipelineService.EXPECT().Start(gomock.Not(gomock.Nil()), connService, procService, pipeline2.Name)
	pipelineService.EXPECT().Start(gomock.Not(gomock.Nil()), connService, procService, pipeline3.Name)

	service := NewService(db, logger, pipelineService, connService, procService, "./test/pipelines3")
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

	pl2config := pipeline.Config{Name: pipeline2.Name, Description: pipeline2.Description}
	pl2 := &pipeline.Instance{
		ID:            pipeline2.Name,
		Config:        pl2config,
		Status:        pipeline.StatusRunning,
		ProvisionedBy: pipeline.ProvisionTypeConfig,
	}
	pl3config := pipeline.Config{Name: pipeline3.Name, Description: pipeline3.Description}
	pl3 := &pipeline.Instance{
		ID:            pipeline3.Name,
		Config:        pl3config,
		Status:        pipeline.StatusRunning,
		ProvisionedBy: pipeline.ProvisionTypeConfig,
	}
	cfg2con1 := connector.Config{
		Name:       pipeline2.Connectors["pipeline2:con1"].Name,
		Plugin:     pipeline2.Connectors["pipeline2:con1"].Plugin,
		PipelineID: pipeline2.Name,
		Settings:   pipeline2.Connectors["pipeline2:con1"].Settings,
	}
	cfg2con2 := connector.Config{
		Name:       pipeline2.Connectors["pipeline2:con2"].Name,
		Plugin:     pipeline2.Connectors["pipeline2:con2"].Plugin,
		PipelineID: pipeline2.Name,
		Settings:   pipeline2.Connectors["pipeline2:con2"].Settings,
	}
	cfg3con1 := connector.Config{
		Name:       pipeline3.Connectors["pipeline3:con1"].Name,
		Plugin:     pipeline3.Connectors["pipeline3:con1"].Plugin,
		PipelineID: pipeline3.Name,
		Settings:   pipeline3.Connectors["pipeline3:con1"].Settings,
	}
	cfg3con2 := connector.Config{
		Name:       pipeline3.Connectors["pipeline3:con2"].Name,
		Plugin:     pipeline3.Connectors["pipeline3:con2"].Plugin,
		PipelineID: pipeline3.Name,
		Settings:   pipeline3.Connectors["pipeline3:con2"].Settings,
	}

	pipelineService.EXPECT().List(gomock.Not(gomock.Nil()))
	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), pipeline2.Name).Return(nil, pipeline.ErrInstanceNotFound)
	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), pipeline3.Name).Return(nil, pipeline.ErrInstanceNotFound)
	pipelineService.
		EXPECT().
		Create(gomock.Not(gomock.Nil()), pipeline2.Name, pl2config, pipeline.ProvisionTypeConfig).
		Return(pl2, nil)
	pipelineService.
		EXPECT().
		Create(gomock.Not(gomock.Nil()), pipeline3.Name, pl3config, pipeline.ProvisionTypeConfig).
		Return(pl3, nil)

	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline2:con1", connector.TypeSource, cfg2con1, connector.ProvisionTypeConfig)
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline2:con2", connector.TypeDestination, cfg2con2, connector.ProvisionTypeConfig)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline2.Name, "pipeline2:con1")
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline2.Name, "pipeline2:con2")
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline3:con1", connector.TypeSource, cfg3con1, connector.ProvisionTypeConfig)
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline3:con2", connector.TypeDestination, cfg3con2, connector.ProvisionTypeConfig)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline3.Name, "pipeline3:con1")
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pipeline3.Name, "pipeline3:con2")

	pipelineService.EXPECT().Start(gomock.Not(gomock.Nil()), connService, procService, pipeline2.Name)
	pipelineService.EXPECT().Start(gomock.Not(gomock.Nil()), connService, procService, pipeline3.Name)

	service := NewService(db, logger, pipelineService, connService, procService, "./test/pipelines5")
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
		builtin.NewRegistry(logger, builtin.DefaultDispenserFactories...),
		standalone.NewRegistry(logger, ""),
	)

	plService := pipeline.NewService(logger, db)
	connService := connector.NewService(logger, db, connector.NewDefaultBuilder(logger, connector.NewPersister(logger, db, time.Second, 3), pluginService))
	procService := processor.NewService(logger, db, processor.GlobalBuilderRegistry)

	// add builtin processor for removing metadata
	// TODO at the time of writing we don't have a processor for manipulating
	//  metadata, once we have it we can use it instead of adding our own
	processor.GlobalBuilderRegistry.MustRegister("removereadat", func(config processor.Config) (processor.Interface, error) {
		return processor.InterfaceFunc(func(ctx context.Context, r record.Record) (record.Record, error) {
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

	service := NewService(db, logger, plService, connService, procService, "./test/pipelines4-integration-test")
	err = service.Init(context.Background())
	is.NoErr(err)

	// give the pipeline time to run through
	time.Sleep(1 * time.Second)

	// checking pipelines
	pipelines := []*pipeline.Instance{
		{
			ID: pipeline1.Name,
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
			ID: pipeline2.Name,
			Config: pipeline.Config{
				Name:        pipeline2.Name,
				Description: "desc2",
			},
			Status:        pipeline.StatusUserStopped,
			ProvisionedBy: pipeline.ProvisionTypeConfig,
			ConnectorIDs:  []string{"pipeline2:con3"},
		},
		{
			ID: pipeline3.Name,
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
				ID:   pipeline1.Name,
				Type: processor.ParentTypePipeline,
			},
			Config: processor.Config{},
		},
		{
			ID:            "pipeline1:con2:con2proc1",
			ProvisionedBy: processor.ProvisionTypeConfig,
			Type:          "removereadat",
			Parent: processor.Parent{
				ID:   "pipeline1:con2",
				Type: processor.ParentTypeConnector,
			},
			Config: processor.Config{},
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
	wantConn1 := connector.Config{
		Name:   "pipeline1:file-src",
		Plugin: "builtin:file",
		Settings: map[string]string{
			"path": "./test/source-file.txt",
		},
		PipelineID: pipeline1.Name,
	}
	wantConn2 := connector.Config{
		Name:   "pipeline1:file-dest",
		Plugin: "builtin:file",
		Settings: map[string]string{
			"path": destFile,
		},
		PipelineID:   pipeline1.Name,
		ProcessorIDs: []string{"pipeline1:con2:con2proc1"},
	}
	wantConn3 := connector.Config{
		Name:   "pipeline2:file-dest",
		Plugin: "builtin:file",
		Settings: map[string]string{
			"path": "./test/file3.txt",
		},
		PipelineID: pipeline2.Name,
	}
	// assert pipeline1:con1
	gotConn1, err := connService.Get(ctx, "pipeline1:con1")
	is.NoErr(err)
	is.Equal(gotConn1.Config(), wantConn1)
	is.Equal(gotConn1.Type(), connector.TypeSource)
	// assert pipeline1:con2
	gotConn2, err := connService.Get(ctx, "pipeline1:con2")
	is.NoErr(err)
	is.Equal(gotConn2.Config(), wantConn2)
	is.Equal(gotConn2.Type(), connector.TypeDestination)
	// assert con3
	gotConn3, err := connService.Get(ctx, "pipeline2:con3")
	is.NoErr(err)
	is.Equal(gotConn3.Config(), wantConn3)
	is.Equal(gotConn3.Type(), connector.TypeDestination)

	data, err := os.ReadFile(destFile)
	is.NoErr(err)
	is.True(len(data) != 0) // destination file is empty
}
