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
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/badger"
	schemaregistry "github.com/conduitio/conduit-schema-registry"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	conn_plugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	"github.com/conduitio/conduit/pkg/plugin/connector/connutils"
	"github.com/conduitio/conduit/pkg/plugin/connector/standalone"
	proc_plugin "github.com/conduitio/conduit/pkg/plugin/processor"
	proc_builtin "github.com/conduitio/conduit/pkg/plugin/processor/builtin"
	"github.com/conduitio/conduit/pkg/plugin/processor/procutils"
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

	oldPipelineInstance = &pipeline.Instance{
		ID: p1.P1.ID,
		Config: pipeline.Config{
			Name:        "name1",
			Description: "desc1",
		},
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
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	service, pipelineService, connService, procService, plugService := newTestService(ctrl, logger)
	service.pipelinesPath = "./test/pipelines1"

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
	pipelineService.EXPECT().Start(anyCtx, connService, procService, plugService, p1.P1.ID)

	err := service.Init(context.Background())
	is.NoErr(err)
}

func TestService_Init_Update(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	service, pipelineService, connService, procService, plugService := newTestService(ctrl, logger)
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
	connService.EXPECT().Update(anyCtx, p1.P1C1.ID, p1.P1C1.Config).Return(oldConnector1Instance, nil)
	procService.EXPECT().Update(anyCtx, p1.P1C2P1.ID, p1.P1C2P1.Config)
	procService.EXPECT().Update(anyCtx, p1.P1P1.ID, p1.P1P1.Config)

	// start pipeline
	pipelineService.EXPECT().Start(anyCtx, connService, procService, plugService, p1.P1.ID)

	err := service.Init(context.Background())
	is.NoErr(err)
}

func TestService_Init_Delete(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	service, pipelineService, connService, procService, plugService := newTestService(ctrl, logger)
	service.pipelinesPath = "" // don't init any pipelines, delete existing ones

	// old pipeline exists
	pipelineService.EXPECT().List(anyCtx).Return(map[string]*pipeline.Instance{oldPipelineInstance.ID: oldPipelineInstance})

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

	err := service.Init(context.Background())
	is.NoErr(err)
}

func TestService_Init_NoRollbackOnFailedStart(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	service, pipelineService, connService, procService, plugService := newTestService(ctrl, logger)
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
	pipelineService.EXPECT().Start(anyCtx, connService, procService, plugService, p1.P1.ID).Return(wantErr)

	err := service.Init(context.Background())
	is.True(cerrors.Is(err, wantErr))
}

func TestService_Init_RollbackCreate(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	service, pipelineService, connService, procService, plugService := newTestService(ctrl, logger)
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

	service, pipelineService, connService, procService, _ := newTestService(ctrl, logger)
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
	connService.EXPECT().Update(anyCtx, p1.P1C1.ID, p1.P1C1.Config).Return(oldConnector1Instance, nil)
	procService.EXPECT().Update(anyCtx, p1.P1C2P1.ID, p1.P1C2P1.Config)
	wantErr := cerrors.New("err")
	procService.EXPECT().Update(anyCtx, p1.P1P1.ID, p1.P1P1.Config).Return(nil, wantErr) // fails

	// rollback changes
	procService.EXPECT().Update(anyCtx, oldPipelineProcessorInstance.ID, oldPipelineProcessorInstance.Config)
	procService.EXPECT().Update(anyCtx, oldConnectorProcessorInstance.ID, oldConnectorProcessorInstance.Config)
	connService.EXPECT().Update(anyCtx, oldConnector1Instance.ID, oldConnector1Instance.Config).Return(oldConnector1Instance, nil)
	pipelineService.EXPECT().Update(anyCtx, oldPipelineInstance.ID, oldPipelineInstance.Config).Return(oldPipelineInstance, nil)
	pipelineService.EXPECT().UpdateDLQ(anyCtx, oldPipelineInstance.ID, oldPipelineInstance.DLQ)

	err := service.Init(context.Background())
	is.True(cerrors.Is(err, wantErr))
}

func TestService_Init_MultiplePipelinesDuplicatedPipelineID(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	service, pipelineService, connService, procService, plugService := newTestService(ctrl, logger)
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

	pipelineService.EXPECT().Start(anyCtx, connService, procService, plugService, p2.P2.ID)

	err := service.Init(context.Background())
	is.True(cerrors.Is(err, ErrDuplicatedPipelineID)) // duplicated pipeline id
}

func TestService_Init_MultiplePipelines(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	service, pipelineService, connService, procService, plugService := newTestService(ctrl, logger)
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

	pipelineService.EXPECT().Start(anyCtx, connService, procService, plugService, p3.P1.ID)

	// create pipeline2
	pipelineService.EXPECT().CreateWithInstance(anyCtx, p3.P2)
	pipelineService.EXPECT().UpdateDLQ(anyCtx, p3.P2.ID, p3.P2.DLQ)
	pipelineService.EXPECT().AddConnector(anyCtx, p3.P2.ID, p3.P2.ConnectorIDs[0])
	pipelineService.EXPECT().AddConnector(anyCtx, p3.P2.ID, p3.P2.ConnectorIDs[1])

	connService.EXPECT().CreateWithInstance(anyCtx, p3.P2C1)
	connService.EXPECT().CreateWithInstance(anyCtx, p3.P2C2)

	pipelineService.EXPECT().Start(anyCtx, connService, procService, plugService, p3.P2.ID)

	err := service.Init(context.Background())
	is.NoErr(err)
}

func TestService_Init_PipelineProvisionedFromAPI(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	tmp := *oldPipelineInstance
	APIPipelineInstance := &tmp
	APIPipelineInstance.ProvisionedBy = pipeline.ProvisionTypeAPI // change the test pipeline to be API provisioned

	service, pipelineService, _, _, _ := newTestService(ctrl, logger)
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

	service, pipelineService, connService, procService, plugService := newTestService(ctrl, logger)
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
	pipelineService.EXPECT().Start(anyCtx, connService, procService, plugService, p1.P1.ID)

	err := service.Init(context.Background())
	is.True(cerrors.Is(err, otherErr))
}

func TestService_Delete(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	service, pipelineService, connService, procService, plugService := newTestService(ctrl, logger)

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
	connPluginService.Init(ctx, "conn-utils-token:12345")

	procSchemaService := procutils.NewSchemaService(logger, schemaRegistry)

	procPluginService := proc_plugin.NewPluginService(
		logger,
		proc_builtin.NewRegistry(logger, proc_builtin.DefaultBuiltinProcessors, procSchemaService),
		nil,
	)

	plService := pipeline.NewService(logger, db)
	connService := connector.NewService(logger, db, connector.NewPersister(logger, db, time.Second, 3))
	procService := processor.NewService(logger, db, procPluginService)

	// create destination file
	destFile := "./test/dest-file.txt"
	_, err = os.Create(destFile)
	is.NoErr(err)
	t.Cleanup(func() {
		err := os.Remove(destFile)
		is.NoErr(err)
	})

	service := NewService(db, logger, plService, connService, procService, connPluginService, "./test/pipelines4-integration-test")
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
		is.Equal(got.Status, want.Status)
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
