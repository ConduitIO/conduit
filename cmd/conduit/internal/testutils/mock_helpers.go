// Copyright © 2025 Meroxa, Inc.
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

package testutils

import (
	"github.com/conduitio/conduit/cmd/conduit/api/mock"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"go.uber.org/mock/gomock"
)

func MockGetPipeline(mockService *mock.MockPipelineService, pipelineID string, connectorIds, processorIds []string) {
	mockService.EXPECT().GetPipeline(gomock.Any(), &apiv1.GetPipelineRequest{
		Id: pipelineID,
	}).Return(&apiv1.GetPipelineResponse{
		Pipeline: &apiv1.Pipeline{
			Id:    pipelineID,
			State: &apiv1.Pipeline_State{Status: apiv1.Pipeline_STATUS_RUNNING},
			Config: &apiv1.Pipeline_Config{
				Name:        "Test Pipeline",
				Description: "A test pipeline description",
			},
			ConnectorIds: connectorIds,
			ProcessorIds: processorIds,
		},
	}, nil).Times(1)
}

func MockGetProcessor(mockService *mock.MockProcessorService, processorID, plugin string, settings map[string]string) {
	mockService.EXPECT().GetProcessor(gomock.Any(), &apiv1.GetProcessorRequest{
		Id: processorID,
	}).Return(&apiv1.GetProcessorResponse{
		Processor: &apiv1.Processor{
			Id:     processorID,
			Plugin: plugin,
			Config: &apiv1.Processor_Config{
				Settings: settings,
			},
		},
	}, nil).Times(1)
}

func MockGetListConnectors(mockService *mock.MockConnectorService, pipelineID string, connectors []*apiv1.Connector) {
	mockService.EXPECT().ListConnectors(gomock.Any(), &apiv1.ListConnectorsRequest{
		PipelineId: pipelineID,
	}).Return(&apiv1.ListConnectorsResponse{
		Connectors: connectors,
	}, nil).Times(1)
}

func MockGetConnector(
	mockService *mock.MockConnectorService,
	connectorID, plugin, pipelineID string,
	conType apiv1.Connector_Type,
	config *apiv1.Connector_Config,
	processorIds []string,
) {
	mockService.EXPECT().GetConnector(gomock.Any(), &apiv1.GetConnectorRequest{
		Id: connectorID,
	}).Return(&apiv1.GetConnectorResponse{
		Connector: &apiv1.Connector{
			Id:           connectorID,
			Type:         conType,
			Plugin:       plugin,
			PipelineId:   pipelineID,
			Config:       config,
			ProcessorIds: processorIds,
		},
	}, nil).Times(1)
}

func MockGetDLQ(mockService *mock.MockPipelineService, pipelineID, plugin string) {
	mockService.EXPECT().GetDLQ(gomock.Any(), &apiv1.GetDLQRequest{
		Id: pipelineID,
	}).Return(&apiv1.GetDLQResponse{
		Dlq: &apiv1.Pipeline_DLQ{Plugin: plugin},
	}, nil).Times(1)
}
