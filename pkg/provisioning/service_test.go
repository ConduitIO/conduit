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
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/badger"
	"github.com/conduitio/conduit-connector-protocol/pconnector/server"
	schemaregistry "github.com/conduitio/conduit-schema-registry"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/lifecycle"
	"github.com/conduitio/conduit/pkg/pipeline"
	conn_plugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	"github.com/conduitio/conduit/pkg/plugin/connector/connutils"
	"github.com/conduitio/conduit/pkg/plugin/connector/standalone"
	proc_plugin "github.com/conduitio/conduit/pkg/plugin/processor"
	proc_builtin "github.com/conduitio/conduit/pkg/plugin/processor/builtin"
	"github.com/conduitio/conduit/pkg/processor"
	p1 "github.com/conduitio/conduit/pkg/provisioning/test/pipelines1"
	p2 "github.com/conduitio/conduit/pkg/provisioning/test/pipelines2"
	p3 "github.com/conduitio/conduit/pkg/provisioning/test/pipelines3"
	p4 "github.com/conduitio/conduit/pkg/provisioning/test/pipelines4-integration-test"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

var (
	anyCtx = gomock.Any()

	oldPipelineInstance = func() *pipeline.Instance {
		pl := &pipeline.Instance{
			ID: p1.P1.ID,
			Config: pipeline.Config{
				Name:        "name1",
				Description: "desc1",
			},
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
		pl.SetStatus(pipeline.StatusRunning)

		return pl
	}()

	oldConnector1Instance = &connector.Instance{
		ID:         "pipeline1:con1",
		Type:       connector.TypeSource,
		Plugin:     "builtin:file",
		PipelineID: oldPipelineInstance.ID,
		Config: connector.Config{
			Name:     "source-new",
			Settings: map[string]string{"path": "my/path/file1.txt"},
		},
	}
	oldConnector2Instance = &connector.Instance{
		ID:         "pipeline1:con2",
		Type:       connector.TypeDestination,
		Plugin:     "builtin:file",
		PipelineID: oldPipelineInstance.ID,
		Config: connector.Config{
			Name:     "dest",
			Settings: map[string]string{"path": "my/path/file2.txt"},
		},
		ProcessorIDs: []string{"pipeline1:con2:proc1con"},
	}
	oldPipelineProcessorInstance = &processor.Instance{
		ID: "pipeline1:proc1",
		Parent: processor.Parent{
			ID:   oldPipelineInstance.ID,
			Type: processor.ParentTypePipeline,
		},
		Config: processor.Config{Settings: map[string]string{}},
		Plugin: "js",
	}
	oldConnectorProcessorInstance = &processor.Instance{
		ID: "pipeline1:con2:proc1con",
		Parent: processor.Parent{
			ID:   oldConnector2Instance.ID,
			Type: processor.ParentTypeConnector,
		},
		Config: processor.Config{Settings: map[string]string{}},
		Plugin: "js",
	}
)

func TestService_Init_Create(t *testing.T) {
	testCases := []struct {
		name     string
		testPath string
	}{
		{
			name:     "pipelines directory",
			testPath: "./test/pipelines1",
		},
		{
			name:     "pipeline file",
			testPath: "./test/pipelines1/pipelines.yml",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			logger := log.Nop()
			ctrl := gomock.NewController(t)

			service, pipelineService, connService, procService, _, lifecycleService := newTestService(ctrl, logger)
			service.pipelinesPath = tc.testPath

			// return a pipeline not provisioned by API
			pipelineService.EXPECT().Get(anyCtx, p1.P1.ID).Return(nil, pipeline.ErrInstanceNotFound)

			pipelineService.EXPECT().List(anyCtx)
			// pipeline doesn't exist
			pipelineService.EXPECT().Get(anyCtx, p1.P1.ID).Return(nil, pipeline.ErrInstanceNotFound)
			// create pipeline
			pipelineService.EXPECT().CreateWithInstance(anyCtx, p1.P1)
			pipelineService.EXPECT().UpdateDLQ(anyCtx, p1.P1.ID, p1.P1.DLQ)
			pipelineService.EXPECT().AddConnector(anyCtx, p1.P1.ID, p1.P1.ConnectorIDs[0])
			pipelineService.EXPECT().AddConnector(anyCtx, p1.P1.ID, p1.P1.ConnectorIDs[1])
			pipelineService.EXPECT().AddProcessor(anyCtx, p1.P1.ID, p1.P1.ProcessorIDs[0])

			connService.EXPECT().CreateWithInstance(anyCtx, p1.P1C1)
			connService.EXPECT().CreateWithInstance(anyCtx, p1.P1C2)
			connService.EXPECT().AddProcessor(anyCtx, p1.P1C2.ID, p1.P1C2.ProcessorIDs[0])

			procService.EXPECT().CreateWithInstance(anyCtx, p1.P1C2P1)
			procService.EXPECT().CreateWithInstance(anyCtx, p1.P1P1)

			// start pipeline
			lifecycleService.EXPECT().Start(anyCtx, p1.P1.ID)

			err := service.Init(context.Background())
			is.NoErr(err)
		})
	}
}

func TestService_Init_Update(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	service, pipelineService, connService, procService, _, lifecycleService := newTestService(ctrl, logger)
	service.pipelinesPath = "./test/pipelines1"

	pipelineService.EXPECT().List(anyCtx)
	// return a pipeline not provisioned by API
	pipelineService.EXPECT().Get(anyCtx, p1.P1.ID).Return(nil, pipeline.ErrInstanceNotFound)

	// pipeline exists
	pipelineService.EXPECT().Get(anyCtx, p1.P1.ID).Return(oldPipelineInstance, nil)

	// export pipeline
	connService.EXPECT().Get(anyCtx, oldConnector1Instance.ID).Return(oldConnector1Instance, nil)
	connService.EXPECT().Get(anyCtx, oldConnector2Instance.ID).Return(oldConnector2Instance, nil)
	procService.EXPECT().Get(anyCtx, oldConnectorProcessorInstance.ID).Return(oldConnectorProcessorInstance, nil)
	procService.EXPECT().Get(anyCtx, oldPipelineProcessorInstance.ID).Return(oldPipelineProcessorInstance, nil)

	// update pipeline
	pipelineService.EXPECT().Update(anyCtx, p1.P1.ID, p1.P1.Config).Return(oldPipelineInstance, nil)
	pipelineService.EXPECT().UpdateDLQ(anyCtx, p1.P1.ID, p1.P1.DLQ)
	connService.EXPECT().Update(anyCtx, p1.P1C1.ID, p1.P1C1.Plugin, p1.P1C1.Config).Return(oldConnector1Instance, nil)
	procService.EXPECT().Update(anyCtx, p1.P1C2P1.ID, p1.P1C2P1.Plugin, p1.P1C2P1.Config)
	procService.EXPECT().Update(anyCtx, p1.P1P1.ID, p1.P1P1.Plugin, p1.P1P1.Config)

	// start pipeline
	lifecycleService.EXPECT().Start(anyCtx, p1.P1.ID)

	err := service.Init(context.Background())
	is.NoErr(err)
}

func TestService_Init_Delete(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	service, _, _, _, _, _ := newTestService(ctrl, logger)
	service.pipelinesPath = "" // don't init any pipelines, delete existing ones

	err := service.Init(context.Background())
	is.True(strings.Contains(err.Error(), "pipeline path cannot be empty"))
	is.True(strings.Contains(err.Error(), "failed to read pipelines folder"))
}

func TestService_Init_NoRollbackOnFailedStart(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	service, pipelineService, connService, procService, _, lifecycleService := newTestService(ctrl, logger)
	service.pipelinesPath = "./test/pipelines1"

	// return a pipeline not provisioned by API
	pipelineService.EXPECT().Get(anyCtx, p1.P1.ID).Return(nil, pipeline.ErrInstanceNotFound)

	pipelineService.EXPECT().List(anyCtx)
	pipelineService.EXPECT().Get(anyCtx, p1.P1.ID).Return(nil, pipeline.ErrInstanceNotFound)

	// create pipeline
	pipelineService.EXPECT().CreateWithInstance(anyCtx, p1.P1)
	pipelineService.EXPECT().UpdateDLQ(anyCtx, p1.P1.ID, p1.P1.DLQ)
	pipelineService.EXPECT().AddConnector(anyCtx, p1.P1.ID, p1.P1.ConnectorIDs[0])
	pipelineService.EXPECT().AddConnector(anyCtx, p1.P1.ID, p1.P1.ConnectorIDs[1])
	pipelineService.EXPECT().AddProcessor(anyCtx, p1.P1.ID, p1.P1.ProcessorIDs[0])

	connService.EXPECT().CreateWithInstance(anyCtx, p1.P1C1)
	connService.EXPECT().CreateWithInstance(anyCtx, p1.P1C2)
	connService.EXPECT().AddProcessor(anyCtx, p1.P1C2.ID, p1.P1C2.ProcessorIDs[0])

	procService.EXPECT().CreateWithInstance(anyCtx, p1.P1C2P1)
	procService.EXPECT().CreateWithInstance(anyCtx, p1.P1P1)

	// returns an error, no rollback needed
	wantErr := cerrors.New("error")
	lifecycleService.EXPECT().Start(anyCtx, p1.P1.ID).Return(wantErr)

	err := service.Init(context.Background())
	is.True(cerrors.Is(err, wantErr))
}

func TestService_Init_RollbackCreate(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	service, pipelineService, connService, procService, plugService, _ := newTestService(ctrl, logger)
	service.pipelinesPath = "./test/pipelines1"

	// return a pipeline not provisioned by API
	pipelineService.EXPECT().Get(anyCtx, p1.P1.ID).Return(nil, pipeline.ErrInstanceNotFound)

	pipelineService.EXPECT().List(anyCtx)
	pipelineService.EXPECT().Get(anyCtx, p1.P1.ID).Return(nil, pipeline.ErrInstanceNotFound)

	pipelineService.EXPECT().CreateWithInstance(anyCtx, p1.P1)
	pipelineService.EXPECT().UpdateDLQ(anyCtx, p1.P1.ID, p1.P1.DLQ)
	pipelineService.EXPECT().AddConnector(anyCtx, p1.P1.ID, p1.P1.ConnectorIDs[0])
	pipelineService.EXPECT().AddConnector(anyCtx, p1.P1.ID, p1.P1.ConnectorIDs[1])
	pipelineService.EXPECT().AddProcessor(anyCtx, p1.P1.ID, p1.P1.ProcessorIDs[0])

	connService.EXPECT().CreateWithInstance(anyCtx, p1.P1C1)
	connService.EXPECT().CreateWithInstance(anyCtx, p1.P1C2)
	connService.EXPECT().AddProcessor(anyCtx, p1.P1C2.ID, p1.P1C2.ProcessorIDs[0])

	procService.EXPECT().CreateWithInstance(anyCtx, p1.P1C2P1)
	wantErr := cerrors.New("error")
	procService.EXPECT().CreateWithInstance(anyCtx, p1.P1P1).Return(nil, wantErr)

	// rollback the creation of all entities
	procService.EXPECT().Delete(anyCtx, p1.P1P1.ID)
	procService.EXPECT().Delete(anyCtx, p1.P1C2P1.ID)
	connService.EXPECT().Delete(anyCtx, p1.P1C2.ID, plugService)
	connService.EXPECT().Delete(anyCtx, p1.P1C1.ID, plugService)
	pipelineService.EXPECT().Delete(anyCtx, p1.P1.ID)

	err := service.Init(context.Background())
	is.True(cerrors.Is(err, wantErr))
}

func TestService_Init_RollbackUpdate(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	service, pipelineService, connService, procService, _, _ := newTestService(ctrl, logger)
	service.pipelinesPath = "./test/pipelines1"

	// return a pipeline not provisioned by API
	pipelineService.EXPECT().Get(anyCtx, p1.P1.ID).Return(nil, pipeline.ErrInstanceNotFound)

	pipelineService.EXPECT().List(anyCtx)
	// pipeline exists
	pipelineService.EXPECT().Get(anyCtx, p1.P1.ID).Return(oldPipelineInstance, nil)

	// export pipeline
	connService.EXPECT().Get(anyCtx, oldConnector1Instance.ID).Return(oldConnector1Instance, nil)
	connService.EXPECT().Get(anyCtx, oldConnector2Instance.ID).Return(oldConnector2Instance, nil)
	procService.EXPECT().Get(anyCtx, oldConnectorProcessorInstance.ID).Return(oldConnectorProcessorInstance, nil)
	procService.EXPECT().Get(anyCtx, oldPipelineProcessorInstance.ID).Return(oldPipelineProcessorInstance, nil)

	// update pipeline
	pipelineService.EXPECT().Update(anyCtx, p1.P1.ID, p1.P1.Config).Return(oldPipelineInstance, nil)
	pipelineService.EXPECT().UpdateDLQ(anyCtx, p1.P1.ID, p1.P1.DLQ)
	connService.EXPECT().Update(anyCtx, p1.P1C1.ID, p1.P1C1.Plugin, p1.P1C1.Config).Return(oldConnector1Instance, nil)
	procService.EXPECT().Update(anyCtx, p1.P1C2P1.ID, p1.P1C2P1.Plugin, p1.P1C2P1.Config)
	wantErr := cerrors.New("err")
	procService.EXPECT().Update(anyCtx, p1.P1P1.ID, p1.P1P1.Plugin, p1.P1P1.Config).Return(nil, wantErr) // fails

	// rollback changes
	procService.EXPECT().Update(anyCtx, oldPipelineProcessorInstance.ID, oldPipelineProcessorInstance.Plugin, oldPipelineProcessorInstance.Config)
	procService.EXPECT().Update(anyCtx, oldConnectorProcessorInstance.ID, oldPipelineProcessorInstance.Plugin, oldConnectorProcessorInstance.Config)
	connService.EXPECT().Update(anyCtx, oldConnector1Instance.ID, oldConnector1Instance.Plugin, oldConnector1Instance.Config).Return(oldConnector1Instance, nil)
	pipelineService.EXPECT().Update(anyCtx, oldPipelineInstance.ID, oldPipelineInstance.Config).Return(oldPipelineInstance, nil)
	pipelineService.EXPECT().UpdateDLQ(anyCtx, oldPipelineInstance.ID, oldPipelineInstance.DLQ)

	err := service.Init(context.Background())
	is.True(cerrors.Is(err, wantErr))
}

func TestService_Init_MultiplePipelinesDuplicatedPipelineID(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	service, pipelineService, connService, _, _, lifecycleService := newTestService(ctrl, logger)
	service.pipelinesPath = "./test/pipelines2"

	// return a pipeline not provisioned by API
	pipelineService.EXPECT().Get(anyCtx, p2.P2.ID).Return(nil, pipeline.ErrInstanceNotFound)

	pipelineService.EXPECT().List(anyCtx)
	pipelineService.EXPECT().Get(anyCtx, p2.P2.ID).Return(nil, pipeline.ErrInstanceNotFound)

	// one pipeline is not duplicated, it still gets provisioned
	pipelineService.EXPECT().CreateWithInstance(anyCtx, p2.P2)
	pipelineService.EXPECT().UpdateDLQ(anyCtx, p2.P2.ID, p2.P2.DLQ)
	pipelineService.EXPECT().AddConnector(anyCtx, p2.P2.ID, p2.P2.ConnectorIDs[0])
	pipelineService.EXPECT().AddConnector(anyCtx, p2.P2.ID, p2.P2.ConnectorIDs[1])

	connService.EXPECT().CreateWithInstance(anyCtx, p2.P2C1)
	connService.EXPECT().CreateWithInstance(anyCtx, p2.P2C2)

	lifecycleService.EXPECT().Start(anyCtx, p2.P2.ID)

	err := service.Init(context.Background())
	is.True(cerrors.Is(err, ErrDuplicatedPipelineID)) // duplicated pipeline id
}

func TestService_Init_MultiplePipelines(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	service, pipelineService, connService, _, _, lifecycleService := newTestService(ctrl, logger)
	service.pipelinesPath = "./test/pipelines3"

	// return a pipeline not provisioned by API
	pipelineService.EXPECT().Get(anyCtx, p3.P1.ID).Return(nil, pipeline.ErrInstanceNotFound)
	pipelineService.EXPECT().Get(anyCtx, p3.P2.ID).Return(nil, pipeline.ErrInstanceNotFound)

	pipelineService.EXPECT().List(anyCtx)
	pipelineService.EXPECT().Get(anyCtx, p3.P1.ID).Return(nil, pipeline.ErrInstanceNotFound)
	pipelineService.EXPECT().Get(anyCtx, p3.P2.ID).Return(nil, pipeline.ErrInstanceNotFound)

	// create pipeline1
	pipelineService.EXPECT().CreateWithInstance(anyCtx, p3.P1)
	pipelineService.EXPECT().UpdateDLQ(anyCtx, p3.P1.ID, p3.P1.DLQ)
	pipelineService.EXPECT().AddConnector(anyCtx, p3.P1.ID, p3.P1.ConnectorIDs[0])
	pipelineService.EXPECT().AddConnector(anyCtx, p3.P1.ID, p3.P1.ConnectorIDs[1])

	connService.EXPECT().CreateWithInstance(anyCtx, p3.P1C1)
	connService.EXPECT().CreateWithInstance(anyCtx, p3.P1C2)

	lifecycleService.EXPECT().Start(anyCtx, p3.P1.ID)

	// create pipeline2
	pipelineService.EXPECT().CreateWithInstance(anyCtx, p3.P2)
	pipelineService.EXPECT().UpdateDLQ(anyCtx, p3.P2.ID, p3.P2.DLQ)
	pipelineService.EXPECT().AddConnector(anyCtx, p3.P2.ID, p3.P2.ConnectorIDs[0])
	pipelineService.EXPECT().AddConnector(anyCtx, p3.P2.ID, p3.P2.ConnectorIDs[1])

	connService.EXPECT().CreateWithInstance(anyCtx, p3.P2C1)
	connService.EXPECT().CreateWithInstance(anyCtx, p3.P2C2)

	lifecycleService.EXPECT().Start(anyCtx, p3.P2.ID)

	err := service.Init(context.Background())
	is.NoErr(err)
}

func TestService_Init_PipelineProvisionedFromAPI(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	APIPipelineInstance := &pipeline.Instance{
		ID:            p1.P1.ID,
		ProvisionedBy: pipeline.ProvisionTypeAPI,
	}

	service, pipelineService, _, _, _, _ := newTestService(ctrl, logger)
	service.pipelinesPath = "./test/pipelines1"

	// pipeline provisioned by API
	pipelineService.EXPECT().Get(anyCtx, p1.P1.ID).Return(APIPipelineInstance, nil)

	pipelineService.EXPECT().List(anyCtx)

	err := service.Init(context.Background())
	is.True(cerrors.Is(err, ErrNotProvisionedByConfig))
}

func TestService_Init_PipelineProvisionedFromAPI_Error(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)
	otherErr := cerrors.New("GetError")

	service, pipelineService, connService, procService, _, lifecycleService := newTestService(ctrl, logger)
	service.pipelinesPath = "./test/pipelines1"

	// error from calling Get
	pipelineService.EXPECT().Get(anyCtx, p1.P1.ID).Return(nil, otherErr)

	pipelineService.EXPECT().List(anyCtx)
	// pipeline doesn't exist
	pipelineService.EXPECT().Get(anyCtx, p1.P1.ID).Return(nil, pipeline.ErrInstanceNotFound)
	// create pipeline
	pipelineService.EXPECT().CreateWithInstance(anyCtx, p1.P1)
	pipelineService.EXPECT().UpdateDLQ(anyCtx, p1.P1.ID, p1.P1.DLQ)
	pipelineService.EXPECT().AddConnector(anyCtx, p1.P1.ID, p1.P1.ConnectorIDs[0])
	pipelineService.EXPECT().AddConnector(anyCtx, p1.P1.ID, p1.P1.ConnectorIDs[1])
	pipelineService.EXPECT().AddProcessor(anyCtx, p1.P1.ID, p1.P1.ProcessorIDs[0])

	connService.EXPECT().CreateWithInstance(anyCtx, p1.P1C1)
	connService.EXPECT().CreateWithInstance(anyCtx, p1.P1C2)
	connService.EXPECT().AddProcessor(anyCtx, p1.P1C2.ID, p1.P1C2.ProcessorIDs[0])

	procService.EXPECT().CreateWithInstance(anyCtx, p1.P1C2P1)
	procService.EXPECT().CreateWithInstance(anyCtx, p1.P1P1)

	// start pipeline
	lifecycleService.EXPECT().Start(anyCtx, p1.P1.ID)

	err := service.Init(context.Background())
	is.True(cerrors.Is(err, otherErr))
}

func TestService_Delete(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	service, pipelineService, connService, procService, plugService, _ := newTestService(ctrl, logger)

	// export pipeline
	pipelineService.EXPECT().Get(anyCtx, oldPipelineInstance.ID).Return(oldPipelineInstance, nil).Times(2)
	connService.EXPECT().Get(anyCtx, oldConnector1Instance.ID).Return(oldConnector1Instance, nil)
	connService.EXPECT().Get(anyCtx, oldConnector2Instance.ID).Return(oldConnector2Instance, nil)
	procService.EXPECT().Get(anyCtx, oldConnectorProcessorInstance.ID).Return(oldConnectorProcessorInstance, nil)
	procService.EXPECT().Get(anyCtx, oldPipelineProcessorInstance.ID).Return(oldPipelineProcessorInstance, nil)
	// delete pipeline
	pipelineService.EXPECT().Delete(anyCtx, oldPipelineInstance.ID).Return(nil)
	connService.EXPECT().Delete(anyCtx, oldConnector1Instance.ID, plugService).Return(nil)
	connService.EXPECT().Delete(anyCtx, oldConnector2Instance.ID, plugService).Return(nil)
	procService.EXPECT().Delete(anyCtx, oldConnectorProcessorInstance.ID).Return(nil)
	procService.EXPECT().Delete(anyCtx, oldPipelineProcessorInstance.ID).Return(nil)

	err := service.Delete(context.Background(), oldPipelineInstance.ID)
	is.NoErr(err)
}

func TestService_UpsertYaml(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Add cleanup for output files that will be created during the test
	t.Cleanup(func() {
		for _, path := range []string{"./output-v1.txt", "./output-v2.txt", "./output-v3.txt", "./input.txt"} {
			_ = os.Remove(path)
		}
	})

	// Set up a real service with non-mock components for actual runtime verification
	logger := log.InitLogger(zerolog.InfoLevel, log.FormatCLI)
	logger.Logger = logger.Hook(ctxutil.MessageIDLogCtxHook{})

	// Create a temporary database
	db, err := badger.New(logger.Logger, t.TempDir()+"/test.db")
	is.NoErr(err)
	t.Cleanup(func() {
		err := db.Close()
		is.NoErr(err)
	})

	// Set up the necessary services
	tokenService := connutils.NewAuthManager()
	schemaRegistry, err := schemaregistry.NewSchemaRegistry(db)
	is.NoErr(err)
	connSchemaService := connutils.NewSchemaService(logger, schemaRegistry, tokenService)

	// Set up plugin services
	connPluginService := conn_plugin.NewPluginService(
		logger,
		builtin.NewRegistry(logger, builtin.DefaultBuiltinConnectors, connSchemaService),
		standalone.NewRegistry(logger, ""),
		tokenService,
	)
	connPluginService.Init(ctx, "conn-utils-token:12345")

	procPluginService := proc_plugin.NewPluginService(
		logger,
		proc_builtin.NewRegistry(logger, proc_builtin.DefaultBuiltinProcessors, schemaRegistry),
		nil,
	)

	// Set up core services
	plService := pipeline.NewService(logger, db)
	connService := connector.NewService(logger, db, connector.NewPersister(logger, db, time.Second, 3))
	procService := processor.NewService(logger, db, procPluginService)

	// Set up lifecycle service
	errRecoveryCfg := &lifecycle.ErrRecoveryCfg{
		MinDelay:         time.Second,
		MaxDelay:         10 * time.Minute,
		BackoffFactor:    2,
		MaxRetries:       0,
		MaxRetriesWindow: 5 * time.Minute,
	}
	lifecycleService := lifecycle.NewService(logger, errRecoveryCfg, connService, procService, connPluginService, plService)

	// Create the provisioning service
	service := NewService(db, logger, plService, connService, procService, connPluginService, lifecycleService, "")

	// Define the base YAML template for single pipeline
	singlePipelineTemplate := `version: 2.2
pipelines:
  - id: test-pipeline
    status: running
    name: Test Pipeline
    description: %s
    connectors:
      - id: source
        type: source
        plugin: builtin:file
        settings:
          path: ./input.txt
      - id: destination
        type: destination
        plugin: builtin:file
        settings:
          path: %s`

	// Define the base YAML template for multiple pipelines
	// Note: Each pipeline needs its own YAML document with version number
	multiPipelineTemplate := `---
version: 2.2
pipelines:
  - id: test-pipeline-1
    status: running
    name: Test Pipeline 1
    description: %s
    connectors:
      - id: source-1
        type: source
        plugin: builtin:file
        settings:
          path: ./input.txt
      - id: destination-1
        type: destination
        plugin: builtin:file
        settings:
          path: %s
---
version: 2.2
pipelines:
  - id: test-pipeline-2
    status: running
    name: Test Pipeline 2
    description: %s
    connectors:
      - id: source-2
        type: source
        plugin: builtin:file
        settings:
          path: ./input.txt
      - id: destination-2
        type: destination
        plugin: builtin:file
        settings:
          path: %s`

	// Define test cases
	type testCase struct {
		name         string
		source       string
		sourceType   string
		description  string
		outputPath   string
		multiple     bool   // Whether to use multiple pipelines
		description2 string // Description for second pipeline (if multiple)
		outputPath2  string // Output path for second pipeline (if multiple)
	}

	testCases := []testCase{
		{
			name:        "Initial configuration (single pipeline)",
			sourceType:  "string",
			description: "Initial configuration",
			outputPath:  "./output-v1.txt",
			multiple:    false,
		},
		{
			name:        "Update via YAML string (single pipeline)",
			sourceType:  "string",
			description: "Updated configuration via string",
			outputPath:  "./output-v2.txt",
			multiple:    false,
		},
		{
			name:        "Update via YAML file (single pipeline)",
			sourceType:  "file",
			description: "Updated configuration via file",
			outputPath:  "./output-v3.txt",
			multiple:    false,
		},
		{
			name:         "Multiple pipelines via YAML string",
			sourceType:   "string",
			description:  "First pipeline in multi-config",
			outputPath:   "./output-multi-1.txt",
			multiple:     true,
			description2: "Second pipeline in multi-config",
			outputPath2:  "./output-multi-2.txt",
		},
	}

	// Create a temporary file for the file-based test case
	tmpfile, err := os.CreateTemp("", "test-pipeline-*.yaml")
	is.NoErr(err)
	defer os.Remove(tmpfile.Name())

	// Add cleanup for additional output files that will be created during the test
	t.Cleanup(func() {
		for _, path := range []string{"./output-multi-1.txt", "./output-multi-2.txt"} {
			_ = os.Remove(path)
		}
	})

	// Run each test case
	for i, tc := range testCases {
		// Add a small delay between test cases to ensure the pipeline has time to stabilize
		if i > 0 {
			time.Sleep(1 * time.Second)
		}
		t.Logf("Running test case %d: %s", i+1, tc.name)

		// Generate the YAML content based on whether we're testing single or multiple pipelines
		var yamlContent string
		if tc.multiple {
			yamlContent = fmt.Sprintf(multiPipelineTemplate, tc.description, tc.outputPath, tc.description2, tc.outputPath2)
		} else {
			yamlContent = fmt.Sprintf(singlePipelineTemplate, tc.description, tc.outputPath)
		}

		// Set up the source based on the source type
		var source string
		if tc.sourceType == "file" {
			// Write the YAML content to the file
			err = os.WriteFile(tmpfile.Name(), []byte(yamlContent), 0o644)
			is.NoErr(err)
			source = tmpfile.Name()
		} else {
			source = yamlContent
		}

		// Reload the pipeline with the current configuration
		var expectedPipelineIDs []string
		if tc.multiple {
			expectedPipelineIDs = []string{"test-pipeline-1", "test-pipeline-2"} // Both pipeline IDs in multi-pipeline case
		} else {
			expectedPipelineIDs = []string{"test-pipeline"} // Single pipeline case
		}

		result, err := service.UpsertYaml(ctx, source)
		is.NoErr(err)
		is.Equal(len(result.PipelineIDs), len(expectedPipelineIDs))

		// Check that all expected pipeline IDs are in the result
		for _, expectedID := range expectedPipelineIDs {
			found := false
			for _, actualID := range result.PipelineIDs {
				if actualID == expectedID {
					found = true
					break
				}
			}
			is.True(found)
		}

		// Give the pipeline time to update
		time.Sleep(500 * time.Millisecond)

		if tc.multiple {
			// Verify the first pipeline was created/updated correctly
			pipeline1, err := plService.Get(ctx, "test-pipeline-1")
			is.NoErr(err)
			is.Equal(pipeline1.Config.Name, "Test Pipeline 1")
			is.Equal(pipeline1.Config.Description, tc.description)

			// Get the destination connector directly and verify its path setting
			destConn1, err := connService.Get(ctx, "test-pipeline-1:destination-1")
			is.NoErr(err)
			is.Equal(destConn1.Config.Settings["path"], tc.outputPath)

			// Verify the second pipeline was created/updated correctly
			pipeline2, err := plService.Get(ctx, "test-pipeline-2")
			is.NoErr(err)
			is.Equal(pipeline2.Config.Name, "Test Pipeline 2")
			is.Equal(pipeline2.Config.Description, tc.description2)

			// Get the destination connector directly and verify its path setting
			destConn2, err := connService.Get(ctx, "test-pipeline-2:destination-2")
			is.NoErr(err)
			is.Equal(destConn2.Config.Settings["path"], tc.outputPath2)
		} else {
			// Verify the configuration was applied correctly for single pipeline
			pipeline, err := plService.Get(ctx, "test-pipeline")
			is.NoErr(err)
			is.Equal(pipeline.Config.Name, "Test Pipeline")
			is.Equal(pipeline.Config.Description, tc.description)

			// Get the destination connector directly and verify its path setting
			destConn, err := connService.Get(ctx, "test-pipeline:destination")
			is.NoErr(err)
			is.Equal(destConn.Config.Settings["path"], tc.outputPath)
		}
	}
}

func TestService_IntegrationTestServices(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()

	logger := log.InitLogger(zerolog.InfoLevel, log.FormatCLI)
	logger.Logger = logger.Hook(ctxutil.MessageIDLogCtxHook{})

	db, err := badger.New(logger.Logger, t.TempDir()+"/test.db")
	is.NoErr(err)
	t.Cleanup(func() {
		err := db.Close()
		is.NoErr(err)
	})

	tokenService := connutils.NewAuthManager()

	schemaRegistry, err := schemaregistry.NewSchemaRegistry(db)
	is.NoErr(err)

	connSchemaService := connutils.NewSchemaService(logger, schemaRegistry, tokenService)

	connPluginService := conn_plugin.NewPluginService(
		logger,
		builtin.NewRegistry(logger, builtin.DefaultBuiltinConnectors, connSchemaService),
		standalone.NewRegistry(logger, ""),
		tokenService,
	)
	connPluginService.Init(ctx, "conn-utils-token:12345", server.DefaultMaxReceiveRecordSize)

	procPluginService := proc_plugin.NewPluginService(
		logger,
		proc_builtin.NewRegistry(logger, proc_builtin.DefaultBuiltinProcessors, schemaRegistry),
		nil,
	)

	plService := pipeline.NewService(logger, db)
	connService := connector.NewService(logger, db, connector.NewPersister(logger, db, time.Second, 3))
	procService := processor.NewService(logger, db, procPluginService)

	errRecoveryCfg := &lifecycle.ErrRecoveryCfg{
		MinDelay:         time.Second,
		MaxDelay:         10 * time.Minute,
		BackoffFactor:    2,
		MaxRetries:       0,
		MaxRetriesWindow: 5 * time.Minute,
	}
	lifecycleService := lifecycle.NewService(logger, errRecoveryCfg, connService, procService, connPluginService, plService)

	// create destination file
	destFile := "./test/dest-file.txt"
	_, err = os.Create(destFile)
	is.NoErr(err)
	t.Cleanup(func() {
		err := os.Remove(destFile)
		is.NoErr(err)
	})

	service := NewService(db, logger, plService, connService, procService, connPluginService, lifecycleService, "./test/pipelines4-integration-test")
	err = service.Init(context.Background())
	is.NoErr(err)

	// give the pipeline time to run through
	time.Sleep(1 * time.Second)

	// checking pipelines
	pipelines := []*pipeline.Instance{p4.P1, p4.P2, p4.P3}
	for _, want := range pipelines {
		got, err := plService.Get(ctx, want.ID)
		is.NoErr(err)
		is.Equal(got.Config, want.Config)
		is.Equal(got.GetStatus(), want.GetStatus())
		is.Equal(got.ProvisionedBy, want.ProvisionedBy)
		is.Equal(got.ConnectorIDs, want.ConnectorIDs)
		is.Equal(got.ProcessorIDs, want.ProcessorIDs)
		is.Equal(got.DLQ, want.DLQ)
	}

	// checking processors
	processors := []*processor.Instance{p4.P1P1, p4.P1C2P1}
	for _, want := range processors {
		got, err := procService.Get(ctx, want.ID)
		is.NoErr(err)
		want.CreatedAt = got.CreatedAt
		want.UpdatedAt = got.UpdatedAt
		diff := cmp.Diff(got, want, cmpopts.IgnoreUnexported(processor.Instance{}))
		if diff != "" {
			t.Errorf("mismatch (-want +got): %s", diff)
		}
	}

	// checking connectors
	connectors := []*connector.Instance{p4.P1C1, p4.P1C2, p4.P2C1}
	for _, want := range connectors {
		got, err := connService.Get(ctx, want.ID)
		is.NoErr(err)
		is.Equal(got.ID, want.ID)
		is.Equal(got.Type, want.Type)
		is.Equal(got.Plugin, want.Plugin)
		is.Equal(got.PipelineID, want.PipelineID)
		is.Equal(got.Config, want.Config)
		is.Equal(got.ProcessorIDs, want.ProcessorIDs)
		is.Equal(got.ProvisionedBy, want.ProvisionedBy)
	}

	data, err := os.ReadFile(destFile)
	is.NoErr(err)
	is.True(len(data) != 0) // destination file is empty
}

func TestService_getYamlFiles(t *testing.T) {
	is := is.New(t)
	pipelinesPath := "./test/different_pipeline_file_types/pipelines"
	service := NewService(nil, log.Nop(), nil, nil, nil, nil, nil, pipelinesPath)

	// 	├── another-pipeline.yaml
	// 	└── pipelines (the configured path)
	// 		├── conduit-rocks.yaml (picked up because it's a YAML file)
	// 		├── conduit-rocks.yml (picked up because it's a YAML file)
	// 		├── nested
	// 		│         └── p.yaml (ignored because it's nested)
	// 		├── pipeline-symlink.yml -> ../another-pipeline.yaml (picked up, because it ends in YAML)
	// 		└── p.txt (ignored because it's not a YAML file)

	want := []string{
		"test/different_pipeline_file_types/pipelines/pipeline-symlink.yml",
		"test/different_pipeline_file_types/pipelines/conduit-rocks.yaml",
		"test/different_pipeline_file_types/pipelines/conduit-rocks.yml",
	}
	slices.Sort(want)

	got, err := service.getPipelineConfigFiles(context.Background(), pipelinesPath)
	is.NoErr(err)

	slices.Sort(got)
	is.Equal("", cmp.Diff(want, got)) // -want +got
}

func TestService_getYamlFiles_FilePaths(t *testing.T) {
	testCases := []struct {
		name string
		path string
		want []string
	}{
		{
			name: "read yaml file",
			path: "test/different_pipeline_file_types/another-pipeline.yaml",
			want: []string{"test/different_pipeline_file_types/another-pipeline.yaml"},
		},
		{
			name: "read yaml file (symlink)",
			path: "test/different_pipeline_file_types/pipelines/pipeline-symlink.yml",
			want: []string{"test/different_pipeline_file_types/pipelines/pipeline-symlink.yml"},
		},
		{
			name: "non-yaml file",
			path: "test/different_pipeline_file_types/pipelines/p.txt",
			want: []string{"test/different_pipeline_file_types/pipelines/p.txt"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			service := NewService(nil, log.Nop(), nil, nil, nil, nil, nil, tc.path)

			got, err := service.getPipelineConfigFiles(context.Background(), tc.path)
			is.NoErr(err)
			is.Equal(tc.want, got) // expected a different pipeline
		})
	}
}
