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

func TestProvision_SimpleRunningPipeline(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	db := &inmemory.DB{}
	_, ctx, _ := db.NewTransaction(context.Background(), true)
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

	pipelineService.EXPECT().Get(ctx, "pipeline1").Return(nil, pipeline.ErrInstanceNotFound)
	pipelineService.
		EXPECT().
		Create(ctx, "pipeline1", pl1config, pipeline.ProvisionTypeConfig).
		Return(pl1, nil)

	connService.EXPECT().Create(ctx, "con1", connector.TypeSource, cfg1, connector.ProvisionTypeConfig)
	pipelineService.EXPECT().AddConnector(ctx, pl1, "con1")
	connService.EXPECT().Create(ctx, "con2", connector.TypeDestination, cfg2, connector.ProvisionTypeConfig)
	pipelineService.EXPECT().AddConnector(ctx, pl1, "con2")

	procService.EXPECT().Create(ctx, "proc1con", "js", procParentConn, procCfg, processor.ProvisionTypeConfig)
	connService.EXPECT().AddProcessor(ctx, "con2", "proc1con")
	procService.EXPECT().Create(ctx, "proc1", "js", procParentPipeline, procCfg, processor.ProvisionTypeConfig)
	pipelineService.EXPECT().AddProcessor(ctx, pl1, "proc1")

	pipelineService.EXPECT().Start(ctx, connService, procService, pl1)

	service := NewService(db, logger, pipelineService, connService, procService)
	err := service.ProvisionConfigFile(context.Background(), "./test/provision1-simple-pipeline.yml")
	is.NoErr(err)
}

func TestProvision_Rollback(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	db := &inmemory.DB{}
	_, ctx, _ := db.NewTransaction(context.Background(), true)
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

	pipelineService.EXPECT().Get(ctx, "pipeline1").Return(nil, pipeline.ErrInstanceNotFound)
	pipelineService.EXPECT().Create(ctx, "pipeline1", pl1config, pipeline.ProvisionTypeConfig).Return(pl1, nil)
	pipelineService.EXPECT().Delete(ctx, pl1)

	connService.EXPECT().Create(ctx, "con1", connector.TypeSource, cfg1, connector.ProvisionTypeConfig)
	connService.EXPECT().Delete(ctx, "con1")
	pipelineService.EXPECT().AddConnector(ctx, pl1, "con1")
	pipelineService.EXPECT().RemoveConnector(ctx, pl1, "con1")
	connService.EXPECT().Create(ctx, "con2", connector.TypeDestination, cfg2, connector.ProvisionTypeConfig)
	connService.EXPECT().Delete(ctx, "con2")
	pipelineService.EXPECT().AddConnector(ctx, pl1, "con2")
	pipelineService.EXPECT().RemoveConnector(ctx, pl1, "con2")

	procService.EXPECT().Create(ctx, "proc1con", "js", procParentConn, procCfg, processor.ProvisionTypeConfig)
	procService.EXPECT().Delete(ctx, "proc1con")
	connService.EXPECT().AddProcessor(ctx, "con2", "proc1con")
	connService.EXPECT().RemoveProcessor(ctx, "con2", "proc1con")
	procService.EXPECT().Create(ctx, "proc1", "js", procParentPipeline, procCfg, processor.ProvisionTypeConfig)
	procService.EXPECT().Delete(ctx, "proc1")
	pipelineService.EXPECT().AddProcessor(ctx, pl1, "proc1")
	pipelineService.EXPECT().RemoveProcessor(ctx, pl1, "proc1")

	// returns an error, so rollback needed
	pipelineService.EXPECT().Start(ctx, connService, procService, pl1).Return(cerrors.New("error"))

	service := NewService(db, logger, pipelineService, connService, procService)
	err := service.ProvisionConfigFile(context.Background(), "./test/provision1-simple-pipeline.yml")
	is.True(err != nil)
}

func TestProvision_CopyStates(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	db := &inmemory.DB{}
	_, ctx, _ := db.NewTransaction(context.Background(), true)
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
	pipelineService.EXPECT().Get(ctx, "pipeline1").Return(pl1, nil)

	// copy over connectors states
	connService.EXPECT().Get(ctx, "con1").Return(source, nil)
	source.EXPECT().State()
	source.EXPECT().SetState(connector.SourceState{})
	connService.EXPECT().Get(ctx, "con2").Return(destination, nil)
	destination.EXPECT().State()
	destination.EXPECT().SetState(connector.DestinationState{})

	// delete old pipeline
	pipelineService.EXPECT().Delete(ctx, pl1)
	pipelineService.EXPECT().RemoveConnector(ctx, pl1, "con1")
	pipelineService.EXPECT().RemoveConnector(ctx, pl1, "con2")
	connService.EXPECT().RemoveProcessor(ctx, "con2", "proc1con")
	pipelineService.EXPECT().RemoveProcessor(ctx, pl1, "proc1")

	// create new pipeline
	pipelineService.
		EXPECT().
		Create(ctx, "pipeline1", pl1config, pipeline.ProvisionTypeConfig).
		Return(pl1, nil)

	connService.EXPECT().Create(ctx, "con1", connector.TypeSource, cfg1, connector.ProvisionTypeConfig).Return(source, nil)
	pipelineService.EXPECT().AddConnector(ctx, pl1, "con1")
	connService.EXPECT().Create(ctx, "con2", connector.TypeDestination, cfg2, connector.ProvisionTypeConfig).Return(destination, nil)
	pipelineService.EXPECT().AddConnector(ctx, pl1, "con2")

	procService.EXPECT().Create(ctx, "proc1con", "js", procParentConn, procCfg, processor.ProvisionTypeConfig)
	connService.EXPECT().AddProcessor(ctx, "con2", "proc1con")
	procService.EXPECT().Create(ctx, "proc1", "js", procParentPipeline, procCfg, processor.ProvisionTypeConfig)
	pipelineService.EXPECT().AddProcessor(ctx, pl1, "proc1")

	pipelineService.EXPECT().Start(ctx, connService, procService, pl1)

	service := NewService(db, logger, pipelineService, connService, procService)
	err := service.ProvisionConfigFile(context.Background(), "./test/provision1-simple-pipeline.yml")
	is.NoErr(err)
}
