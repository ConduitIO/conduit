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
	"sort"
	"testing"

	"github.com/conduitio/conduit/pkg/connector"
	connmock "github.com/conduitio/conduit/pkg/connector/mock"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/provisioning/mock"
	"github.com/golang/mock/gomock"
	"github.com/matryer/is"
)

var pipeline1 = PipelineConfig{
	Status:      "running",
	Name:        "pipeline1",
	Description: "desc1",
	Connectors: map[string]ConnectorConfig{
		"con1": {
			Type:   "source",
			Plugin: "builtin:file",
			Name:   "source",
			Settings: map[string]string{
				"path": "my/path/file1.txt",
			},
		},
		"con2": {
			Type:   "destination",
			Plugin: "builtin:file",
			Name:   "dest",
			Settings: map[string]string{
				"path": "my/path/file2.txt",
			},
			Processors: map[string]ProcessorConfig{
				"proc1con": {
					Type: "js",
					Settings: map[string]string{
						"additionalProp1": "string",
					},
				},
			},
		},
	},
	Processors: map[string]ProcessorConfig{
		"proc1": {
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
		"con1": {
			Type:   "source",
			Plugin: "builtin:file",
			Name:   "source",
			Settings: map[string]string{
				"path": "my/path/file1.txt",
			},
		},
		"con2": {
			Type:   "destination",
			Plugin: "builtin:file",
			Name:   "dest",
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
		"con1": {
			Type:   "source",
			Plugin: "builtin:file",
			Name:   "source",
			Settings: map[string]string{
				"path": "my/path/file1.txt",
			},
		},
		"con2": {
			Type:   "destination",
			Plugin: "builtin:file",
			Name:   "dest",
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
		ID:            "pipeline1",
		Config:        pl1config,
		Status:        pipeline.StatusRunning,
		ProvisionedBy: pipeline.ProvisionTypeConfig,
		ConnectorIDs:  []string{"con1", "con2"},
	}
	cfg1 := connector.Config{
		Name:       pipeline1.Connectors["con1"].Name,
		Plugin:     pipeline1.Connectors["con1"].Plugin,
		PipelineID: pipeline1.Name,
		Settings:   pipeline1.Connectors["con1"].Settings,
	}
	cfg2 := connector.Config{
		Name:       pipeline1.Connectors["con2"].Name,
		Plugin:     pipeline1.Connectors["con2"].Plugin,
		PipelineID: pipeline1.Name,
		Settings:   pipeline1.Connectors["con2"].Settings,
	}
	procCfg := processor.Config{
		Settings: pipeline1.Processors["proc1"].Settings,
	}
	procParentPipeline := processor.Parent{
		ID:   "pipeline1",
		Type: processor.ParentTypePipeline,
	}
	procParentConn := processor.Parent{
		ID:   "con2",
		Type: processor.ParentTypeConnector,
	}

	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), "pipeline1").Return(nil, pipeline.ErrInstanceNotFound)
	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), "pipeline1").Return(pl1, nil)
	pipelineService.
		EXPECT().
		Create(gomock.Not(gomock.Nil()), "pipeline1", pl1config, pipeline.ProvisionTypeConfig).
		Return(pl1, nil)

	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "con1", connector.TypeSource, cfg1, connector.ProvisionTypeConfig)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pl1, "con1")
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "con2", connector.TypeDestination, cfg2, connector.ProvisionTypeConfig)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pl1, "con2")

	procService.EXPECT().Create(gomock.Not(gomock.Nil()), "proc1con", "js", procParentConn, procCfg, processor.ProvisionTypeConfig)
	connService.EXPECT().AddProcessor(gomock.Not(gomock.Nil()), "con2", "proc1con")
	procService.EXPECT().Create(gomock.Not(gomock.Nil()), "proc1", "js", procParentPipeline, procCfg, processor.ProvisionTypeConfig)
	pipelineService.EXPECT().AddProcessor(gomock.Not(gomock.Nil()), pl1, "proc1")

	pipelineService.EXPECT().Start(gomock.Not(gomock.Nil()), connService, procService, pl1)

	service := NewService(db, logger, pipelineService, connService, procService)
	pls, err := service.ProvisionConfigFile(context.Background(), "./test/provision1-pipeline-with-processors.yml")
	is.NoErr(err)
	is.Equal(pls, []string{"pipeline1"})
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
		ID:            "pipeline1",
		Config:        pl1config,
		Status:        pipeline.StatusRunning,
		ProvisionedBy: pipeline.ProvisionTypeConfig,
	}
	cfg1 := connector.Config{
		Name:       pipeline1.Connectors["con1"].Name,
		Plugin:     pipeline1.Connectors["con1"].Plugin,
		PipelineID: pipeline1.Name,
		Settings:   pipeline1.Connectors["con1"].Settings,
	}
	cfg2 := connector.Config{
		Name:       pipeline1.Connectors["con2"].Name,
		Plugin:     pipeline1.Connectors["con2"].Plugin,
		PipelineID: pipeline1.Name,
		Settings:   pipeline1.Connectors["con2"].Settings,
	}
	procCfg := processor.Config{
		Settings: pipeline1.Processors["proc1"].Settings,
	}
	procParentPipeline := processor.Parent{
		ID:   "pipeline1",
		Type: processor.ParentTypePipeline,
	}
	procParentConn := processor.Parent{
		ID:   "con2",
		Type: processor.ParentTypeConnector,
	}

	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), "pipeline1").Return(nil, pipeline.ErrInstanceNotFound)
	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), "pipeline1").Return(pl1, nil)
	pipelineService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1", pl1config, pipeline.ProvisionTypeConfig).Return(pl1, nil)
	pipelineService.EXPECT().Delete(gomock.Not(gomock.Nil()), pl1)

	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "con1", connector.TypeSource, cfg1, connector.ProvisionTypeConfig)
	connService.EXPECT().Delete(gomock.Not(gomock.Nil()), "con1")
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pl1, "con1")
	pipelineService.EXPECT().RemoveConnector(gomock.Not(gomock.Nil()), pl1, "con1")
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "con2", connector.TypeDestination, cfg2, connector.ProvisionTypeConfig)
	connService.EXPECT().Delete(gomock.Not(gomock.Nil()), "con2")
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pl1, "con2")
	pipelineService.EXPECT().RemoveConnector(gomock.Not(gomock.Nil()), pl1, "con2")

	procService.EXPECT().Create(gomock.Not(gomock.Nil()), "proc1con", "js", procParentConn, procCfg, processor.ProvisionTypeConfig)
	procService.EXPECT().Delete(gomock.Not(gomock.Nil()), "proc1con")
	connService.EXPECT().AddProcessor(gomock.Not(gomock.Nil()), "con2", "proc1con")
	connService.EXPECT().RemoveProcessor(gomock.Not(gomock.Nil()), "con2", "proc1con")
	procService.EXPECT().Create(gomock.Not(gomock.Nil()), "proc1", "js", procParentPipeline, procCfg, processor.ProvisionTypeConfig)
	procService.EXPECT().Delete(gomock.Not(gomock.Nil()), "proc1")
	pipelineService.EXPECT().AddProcessor(gomock.Not(gomock.Nil()), pl1, "proc1")
	pipelineService.EXPECT().RemoveProcessor(gomock.Not(gomock.Nil()), pl1, "proc1")

	// returns an error, so rollback needed
	pipelineService.EXPECT().Start(gomock.Not(gomock.Nil()), connService, procService, pl1).Return(cerrors.New("error"))

	service := NewService(db, logger, pipelineService, connService, procService)
	pls, err := service.ProvisionConfigFile(context.Background(), "./test/provision1-pipeline-with-processors.yml")
	is.True(err != nil)
	is.Equal(pls, nil)
}

func TestProvision_RollbackDeletePipeline(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	pipelineService := mock.NewPipelineService(ctrl)
	connService := mock.NewConnectorService(ctrl)
	procService := mock.NewProcessorService(ctrl)

	pl1config := pipeline.Config{Name: pipeline1.Name, Description: pipeline1.Description}
	pl1 := &pipeline.Instance{
		ID:            "pipeline1",
		Config:        pl1config,
		Status:        pipeline.StatusRunning,
		ProvisionedBy: pipeline.ProvisionTypeConfig,
	}
	cfg1 := connector.Config{
		Name:       pipeline1.Connectors["con1"].Name,
		Plugin:     pipeline1.Connectors["con1"].Plugin,
		PipelineID: pipeline1.Name,
		Settings:   pipeline1.Connectors["con1"].Settings,
	}
	cfg2 := connector.Config{
		Name:       pipeline1.Connectors["con2"].Name,
		Plugin:     pipeline1.Connectors["con2"].Plugin,
		PipelineID: pipeline1.Name,
		Settings:   pipeline1.Connectors["con2"].Settings,
	}
	procCfg := processor.Config{
		Settings: pipeline1.Processors["proc1"].Settings,
	}
	procParentPipeline := processor.Parent{
		ID:   "pipeline1",
		Type: processor.ParentTypePipeline,
	}
	procParentConn := processor.Parent{
		ID:   "con2",
		Type: processor.ParentTypeConnector,
	}

	// pipeline exists
	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), "pipeline1").Return(pl1, nil)
	pipelineService.EXPECT().Stop(gomock.Not(gomock.Nil()), pl1)
	pipelineService.EXPECT().Delete(gomock.Not(gomock.Nil()), pl1)

	connService.EXPECT().Delete(gomock.Not(gomock.Nil()), "con1")
	pipelineService.EXPECT().RemoveConnector(gomock.Not(gomock.Nil()), pl1, "con1")
	connService.EXPECT().Delete(gomock.Not(gomock.Nil()), "con2")
	pipelineService.EXPECT().RemoveConnector(gomock.Not(gomock.Nil()), pl1, "con2")

	procService.EXPECT().Delete(gomock.Not(gomock.Nil()), "proc1con")
	connService.EXPECT().RemoveProcessor(gomock.Not(gomock.Nil()), "con2", "proc1con")
	procService.EXPECT().Delete(gomock.Not(gomock.Nil()), "proc1")
	pipelineService.EXPECT().RemoveProcessor(gomock.Not(gomock.Nil()), pl1, "proc1")

	// create new pipeline failed, rollback
	pipelineService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1", pl1config, pipeline.ProvisionTypeConfig).Return(nil, cerrors.New("err"))

	// create old processor
	pipelineService.EXPECT().Create(gomock.Not(gomock.Nil()), "pipeline1", pl1config, pipeline.ProvisionTypeConfig).Return(pl1, nil)

	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "con1", connector.TypeSource, cfg1, connector.ProvisionTypeConfig)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pl1, "con1")
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "con2", connector.TypeDestination, cfg2, connector.ProvisionTypeConfig)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pl1, "con2")

	procService.EXPECT().Create(gomock.Not(gomock.Nil()), "proc1con", "js", procParentConn, procCfg, processor.ProvisionTypeConfig)
	connService.EXPECT().AddProcessor(gomock.Not(gomock.Nil()), "con2", "proc1con")
	procService.EXPECT().Create(gomock.Not(gomock.Nil()), "proc1", "js", procParentPipeline, procCfg, processor.ProvisionTypeConfig)
	pipelineService.EXPECT().AddProcessor(gomock.Not(gomock.Nil()), pl1, "proc1")

	pipelineService.EXPECT().Start(gomock.Not(gomock.Nil()), connService, procService, pl1)

	service := NewService(db, logger, pipelineService, connService, procService)
	pls, err := service.ProvisionConfigFile(context.Background(), "./test/provision1-pipeline-with-processors.yml")
	is.True(err != nil)
	is.Equal(pls, nil)
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
		ID:            "pipeline1",
		Config:        pl1config,
		Status:        pipeline.StatusRunning,
		ProvisionedBy: pipeline.ProvisionTypeConfig,
		ConnectorIDs:  []string{"con1", "con2"},
	}
	cfg1 := connector.Config{
		Name:       pipeline1.Connectors["con1"].Name,
		Plugin:     pipeline1.Connectors["con1"].Plugin,
		PipelineID: pipeline1.Name,
		Settings:   pipeline1.Connectors["con1"].Settings,
	}
	cfg2 := connector.Config{
		Name:       pipeline1.Connectors["con2"].Name,
		Plugin:     pipeline1.Connectors["con2"].Plugin,
		PipelineID: pipeline1.Name,
		Settings:   pipeline1.Connectors["con2"].Settings,
	}
	procCfg := processor.Config{
		Settings: pipeline1.Processors["proc1"].Settings,
	}
	procParentPipeline := processor.Parent{
		ID:   "pipeline1",
		Type: processor.ParentTypePipeline,
	}
	procParentConn := processor.Parent{
		ID:   "con2",
		Type: processor.ParentTypeConnector,
	}
	sourceConfig := connector.Config{
		Name:       "con1",
		Settings:   map[string]string{"path": "my/path/file1.txt"},
		Plugin:     "builtin:file",
		PipelineID: "pipeline1",
	}
	destConfig := connector.Config{
		Name:       "con2",
		Settings:   map[string]string{"path": "my/path/file2.txt"},
		Plugin:     "builtin:file",
		PipelineID: "pipeline1",
	}

	source := connBuilder.NewSourceMock("con1", sourceConfig)
	destination := connBuilder.NewDestinationMock("con2", destConfig)

	// pipeline already exists
	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), "pipeline1").Return(pl1, nil).Times(2)
	pipelineService.EXPECT().Stop(gomock.Not(gomock.Nil()), pl1)

	// copy over connectors states
	connService.EXPECT().Get(gomock.Not(gomock.Nil()), "con1").Return(source, nil).AnyTimes()
	source.EXPECT().State()
	connService.EXPECT().SetSourceState(gomock.Not(gomock.Nil()), "con1", connector.SourceState{})
	connService.EXPECT().Get(gomock.Not(gomock.Nil()), "con2").Return(destination, nil).AnyTimes()
	destination.EXPECT().State()
	connService.EXPECT().SetDestinationState(gomock.Not(gomock.Nil()), "con2", connector.DestinationState{})

	// delete old pipeline
	pipelineService.EXPECT().Delete(gomock.Not(gomock.Nil()), pl1)
	pipelineService.EXPECT().RemoveConnector(gomock.Not(gomock.Nil()), pl1, "con1")
	connService.EXPECT().Delete(gomock.Not(gomock.Nil()), "con1")
	pipelineService.EXPECT().RemoveConnector(gomock.Not(gomock.Nil()), pl1, "con2")
	connService.EXPECT().Delete(gomock.Not(gomock.Nil()), "con2")
	connService.EXPECT().RemoveProcessor(gomock.Not(gomock.Nil()), "con2", "proc1con")
	procService.EXPECT().Delete(gomock.Not(gomock.Nil()), "proc1con")
	pipelineService.EXPECT().RemoveProcessor(gomock.Not(gomock.Nil()), pl1, "proc1")
	procService.EXPECT().Delete(gomock.Not(gomock.Nil()), "proc1")

	// create new pipeline
	pipelineService.
		EXPECT().
		Create(gomock.Not(gomock.Nil()), "pipeline1", pl1config, pipeline.ProvisionTypeConfig).
		Return(pl1, nil)

	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "con1", connector.TypeSource, cfg1, connector.ProvisionTypeConfig).Return(source, nil)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pl1, "con1")
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "con2", connector.TypeDestination, cfg2, connector.ProvisionTypeConfig).Return(destination, nil)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pl1, "con2")

	procService.EXPECT().Create(gomock.Not(gomock.Nil()), "proc1con", "js", procParentConn, procCfg, processor.ProvisionTypeConfig)
	connService.EXPECT().AddProcessor(gomock.Not(gomock.Nil()), "con2", "proc1con")
	procService.EXPECT().Create(gomock.Not(gomock.Nil()), "proc1", "js", procParentPipeline, procCfg, processor.ProvisionTypeConfig)
	pipelineService.EXPECT().AddProcessor(gomock.Not(gomock.Nil()), pl1, "proc1")

	pipelineService.EXPECT().Start(gomock.Not(gomock.Nil()), connService, procService, pl1)

	service := NewService(db, logger, pipelineService, connService, procService)
	pls, err := service.ProvisionConfigFile(context.Background(), "./test/provision1-pipeline-with-processors.yml")
	is.NoErr(err)
	is.Equal(pls, []string{"pipeline1"})
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
		ID:            "pipeline2",
		Config:        pl2config,
		Status:        pipeline.StatusRunning,
		ProvisionedBy: pipeline.ProvisionTypeConfig,
	}
	pl3config := pipeline.Config{Name: pipeline3.Name, Description: pipeline3.Description}
	pl3 := &pipeline.Instance{
		ID:            "pipeline3",
		Config:        pl3config,
		Status:        pipeline.StatusRunning,
		ProvisionedBy: pipeline.ProvisionTypeConfig,
	}
	cfg2con1 := connector.Config{
		Name:       pipeline2.Connectors["con1"].Name,
		Plugin:     pipeline2.Connectors["con1"].Plugin,
		PipelineID: pipeline2.Name,
		Settings:   pipeline2.Connectors["con1"].Settings,
	}
	cfg2con2 := connector.Config{
		Name:       pipeline2.Connectors["con2"].Name,
		Plugin:     pipeline2.Connectors["con2"].Plugin,
		PipelineID: pipeline2.Name,
		Settings:   pipeline2.Connectors["con2"].Settings,
	}
	cfg3con1 := connector.Config{
		Name:       pipeline3.Connectors["con1"].Name,
		Plugin:     pipeline3.Connectors["con1"].Plugin,
		PipelineID: pipeline3.Name,
		Settings:   pipeline3.Connectors["con1"].Settings,
	}
	cfg3con2 := connector.Config{
		Name:       pipeline3.Connectors["con2"].Name,
		Plugin:     pipeline3.Connectors["con2"].Plugin,
		PipelineID: pipeline3.Name,
		Settings:   pipeline3.Connectors["con2"].Settings,
	}

	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), "pipeline2").Return(nil, pipeline.ErrInstanceNotFound)
	pipelineService.EXPECT().Get(gomock.Not(gomock.Nil()), "pipeline3").Return(nil, pipeline.ErrInstanceNotFound)
	pipelineService.
		EXPECT().
		Create(gomock.Not(gomock.Nil()), "pipeline2", pl2config, pipeline.ProvisionTypeConfig).
		Return(pl2, nil)
	pipelineService.
		EXPECT().
		Create(gomock.Not(gomock.Nil()), "pipeline3", pl3config, pipeline.ProvisionTypeConfig).
		Return(pl3, nil)

	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "con1", connector.TypeSource, cfg2con1, connector.ProvisionTypeConfig)
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "con2", connector.TypeDestination, cfg2con2, connector.ProvisionTypeConfig)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pl2, "con1")
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pl2, "con2")
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "con1", connector.TypeSource, cfg3con1, connector.ProvisionTypeConfig)
	connService.EXPECT().Create(gomock.Not(gomock.Nil()), "con2", connector.TypeDestination, cfg3con2, connector.ProvisionTypeConfig)
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pl3, "con1")
	pipelineService.EXPECT().AddConnector(gomock.Not(gomock.Nil()), pl3, "con2")

	pipelineService.EXPECT().Start(gomock.Not(gomock.Nil()), connService, procService, pl2)
	pipelineService.EXPECT().Start(gomock.Not(gomock.Nil()), connService, procService, pl3)

	service := NewService(db, logger, pipelineService, connService, procService)
	pls, err := service.ProvisionConfigFile(context.Background(), "./test/provision3-multiple-pipelines.yml")
	is.NoErr(err)
	// sort slice before comparing
	sort.Strings(pls)
	is.Equal(pls, []string{"pipeline2", "pipeline3"})
}
