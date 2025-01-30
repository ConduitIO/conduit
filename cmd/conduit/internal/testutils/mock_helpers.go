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
	"time"

	configv1 "github.com/conduitio/conduit-commons/proto/config/v1"
	"github.com/conduitio/conduit/cmd/conduit/api/mock"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"go.uber.org/mock/gomock"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func GetDateTime() *timestamppb.Timestamp {
	parsedTime, _ := time.Parse(time.RFC3339, "1970-01-01T00:00:00Z")
	return timestamppb.New(parsedTime)
}

// PipelineService --------------------------------------------

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
			CreatedAt:    GetDateTime(),
			UpdatedAt:    GetDateTime(),
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

func MockGetListPipelines(mockService *mock.MockPipelineService, pipelines []*apiv1.Pipeline) {
	mockService.EXPECT().ListPipelines(gomock.Any(), gomock.Any()).Return(&apiv1.ListPipelinesResponse{
		Pipelines: pipelines,
	}, nil).Times(1)
}

// ProcessorService --------------------------------------------

func MockGetProcessor(
	mockService *mock.MockProcessorService,
	processorID, plugin, condition string,
	parent *apiv1.Processor_Parent,
	settings map[string]string,
) {
	mockService.EXPECT().GetProcessor(gomock.Any(), &apiv1.GetProcessorRequest{
		Id: processorID,
	}).Return(&apiv1.GetProcessorResponse{
		Processor: &apiv1.Processor{
			Id:     processorID,
			Plugin: plugin,
			Config: &apiv1.Processor_Config{
				Settings: settings,
			},
			Parent:    parent,
			Condition: condition,
		},
	}, nil).Times(1)
}

func MockGetProcessorPlugins(mockservice *mock.MockProcessorService, name string) {
	mockservice.EXPECT().ListProcessorPlugins(gomock.Any(), &apiv1.ListProcessorPluginsRequest{
		Name: name,
	}).Return(&apiv1.ListProcessorPluginsResponse{
		Plugins: []*apiv1.ProcessorPluginSpecifications{
			{
				Name:    name,
				Summary: "Encode a field to base64",
				Description: "The processor will encode the value of the target field to base64 and store the\n" +
					"result in the target field. It is not allowed to encode the `.Position` field.\n" +
					"If the provided field doesn't exist, the processor will create that field and\n" +
					"assign its value. Field is a reference to the target field. " +
					"Note that it is not allowed to base64 encode. `.Position` field. ",
				Author:  "Meroxa, Inc.",
				Version: "v0.1.0",
				Parameters: map[string]*configv1.Parameter{
					"field": {
						Type:        configv1.Parameter_Type(apiv1.PluginSpecifications_Parameter_TYPE_STRING),
						Description: "Field is a reference to the target field",
						Default:     "",
						Validations: []*configv1.Validation{
							{Type: configv1.Validation_TYPE_REQUIRED},
							{Type: configv1.Validation_TYPE_EXCLUSION},
						},
					},
				},
			},
		},
	}, nil).Times(1)
}

// ConnectorService --------------------------------------------

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
