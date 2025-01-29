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

package pipelines

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/cmd/conduit/api/mock"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/conduitio/ecdysis"
	"go.uber.org/mock/gomock"

	"github.com/matryer/is"
)

func TestDescribeExecutionNoArgs(t *testing.T) {
	is := is.New(t)

	c := DescribeCommand{}
	err := c.Args([]string{})

	expected := "requires a pipeline ID"

	is.True(err != nil)
	is.Equal(err.Error(), expected)
}

func TestDescribeExecutionMultipleArgs(t *testing.T) {
	is := is.New(t)

	c := DescribeCommand{}
	err := c.Args([]string{"foo", "bar"})

	expected := "too many arguments"

	is.True(err != nil)
	is.Equal(err.Error(), expected)
}

func TestDescribeExecutionCorrectArgs(t *testing.T) {
	is := is.New(t)
	pipelineID := "my-pipeline"

	c := DescribeCommand{}
	err := c.Args([]string{pipelineID})

	is.NoErr(err)
	is.Equal(c.args.PipelineID, pipelineID)
}

func TestDescribeCommandExecuteWithClient_WithPipelineDetails(t *testing.T) {
	is := is.New(t)

	buf := new(bytes.Buffer)
	out := &ecdysis.DefaultOutput{}
	out.Output(buf, nil)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cmd := &DescribeCommand{args: DescribeArgs{PipelineID: "1"}}
	cmd.Output(out)

	mockService := mock.NewMockPipelineService(ctrl)
	mockProcessorService := mock.NewMockProcessorService(ctrl)
	mockConnectorService := mock.NewMockConnectorService(ctrl)

	mockService.EXPECT().GetPipeline(ctx, &apiv1.GetPipelineRequest{
		Id: cmd.args.PipelineID,
	}).Return(&apiv1.GetPipelineResponse{
		Pipeline: &apiv1.Pipeline{
			Id:    cmd.args.PipelineID,
			State: &apiv1.Pipeline_State{Status: apiv1.Pipeline_STATUS_RUNNING},
			Config: &apiv1.Pipeline_Config{
				Name:        "Test Pipeline",
				Description: "A test pipeline description",
			},
			ConnectorIds: []string{"conn1", "conn2"},
			ProcessorIds: []string{"proc1", "proc2"},
		},
	}, nil).Times(1)

	mockProcessorService.EXPECT().GetProcessor(ctx, &apiv1.GetProcessorRequest{
		Id: "proc1",
	}).Return(&apiv1.GetProcessorResponse{
		Processor: &apiv1.Processor{
			Id:     "proc1",
			Plugin: "field.set",
			Config: &apiv1.Processor_Config{
				Settings: map[string]string{
					".Payload.After.department": "finance",
				},
			},
		},
	}, nil).Times(1)

	mockProcessorService.EXPECT().GetProcessor(ctx, &apiv1.GetProcessorRequest{
		Id: "proc2",
	}).Return(&apiv1.GetProcessorResponse{
		Processor: &apiv1.Processor{
			Id:     "proc2",
			Plugin: "custom.javascript",
			Config: &apiv1.Processor_Config{},
		},
	}, nil).Times(1)

	mockConnectorService.EXPECT().ListConnectors(ctx, &apiv1.ListConnectorsRequest{
		PipelineId: cmd.args.PipelineID,
	}).Return(&apiv1.ListConnectorsResponse{
		Connectors: []*apiv1.Connector{
			{Id: "conn1", Type: apiv1.Connector_TYPE_SOURCE, Plugin: "plugin1", ProcessorIds: []string{"proc3"}},
			{Id: "conn2", Type: apiv1.Connector_TYPE_DESTINATION, Plugin: "plugin2"},
		},
	}, nil).Times(1)

	mockConnectorService.EXPECT().GetConnector(ctx, &apiv1.GetConnectorRequest{
		Id: "conn1",
	}).Return(&apiv1.GetConnectorResponse{
		Connector: &apiv1.Connector{
			Id:         "conn1",
			Type:       apiv1.Connector_TYPE_SOURCE,
			Plugin:     "plugin1",
			PipelineId: "1",
			Config: &apiv1.Connector_Config{
				Name: "Test Pipeline",
				Settings: map[string]string{
					"foo": "bar",
				},
			},
			ProcessorIds: []string{"proc3"},
		},
	}, nil).Times(1)

	mockProcessorService.EXPECT().GetProcessor(ctx, &apiv1.GetProcessorRequest{
		Id: "proc3",
	}).Return(&apiv1.GetProcessorResponse{
		Processor: &apiv1.Processor{
			Id:     "proc3",
			Plugin: "custom.javascript",
			Config: &apiv1.Processor_Config{},
		},
	}, nil).Times(1)

	mockConnectorService.EXPECT().GetConnector(ctx, &apiv1.GetConnectorRequest{
		Id: "conn2",
	}).Return(&apiv1.GetConnectorResponse{
		Connector: &apiv1.Connector{
			Id:         "conn2",
			Type:       apiv1.Connector_TYPE_DESTINATION,
			Plugin:     "plugin2",
			PipelineId: "1",
			Config: &apiv1.Connector_Config{
				Name: "Test Pipeline",
				Settings: map[string]string{
					"foo": "bar",
				},
			},
		},
	}, nil).Times(1)

	mockService.EXPECT().GetDLQ(ctx, &apiv1.GetDLQRequest{
		Id: cmd.args.PipelineID,
	}).Return(&apiv1.GetDLQResponse{
		Dlq: &apiv1.Pipeline_DLQ{Plugin: "dlq-plugin"},
	}, nil).Times(1)

	client := &api.Client{
		PipelineServiceClient:  mockService,
		ProcessorServiceClient: mockProcessorService,
		ConnectorServiceClient: mockConnectorService,
	}

	err := cmd.ExecuteWithClient(ctx, client)
	is.NoErr(err)

	output := buf.String()

	is.True(strings.Contains(output, ""+
		"ID: 1\n"+
		"Status: running\n"+
		"Name: Test Pipeline\n"+
		"Description: A test pipeline description\n"+
		"Sources:\n"+
		"  - ID: conn1\n"+
		"    Plugin: plugin1\n"+
		"    Pipeline ID: 1\n"+
		"    Config:\n"+
		"      foo: bar\n"+
		"    Processors:\n"+
		"      - ID: proc3\n"+
		"        Plugin: custom.javascript\n"+
		"        Config:\n"+
		"          Workers: 0\n"+
		"        Created At: 1970-01-01T00:00:00Z\n"+
		"        Updated At: 1970-01-01T00:00:00Z\n"+
		"    Created At: 1970-01-01T00:00:00Z\n"+
		"    Updated At: 1970-01-01T00:00:00Z\n"+
		"Processors:\n"+
		"  - ID: proc1\n"+
		"    Plugin: field.set\n"+
		"    Config:\n"+
		"      .Payload.After.department: finance\n"+
		"      Workers: 0\n"+
		"    Created At: 1970-01-01T00:00:00Z\n"+
		"    Updated At: 1970-01-01T00:00:00Z\n"+
		"  - ID: proc2\n"+
		"    Plugin: custom.javascript\n"+
		"    Config:\n"+
		"      Workers: 0\n"+
		"    Created At: 1970-01-01T00:00:00Z\n"+
		"    Updated At: 1970-01-01T00:00:00Z\n"+
		"Destinations:\n"+
		"  - ID: conn2\n"+
		"    Plugin: plugin2\n"+
		"    Pipeline ID: 1\n"+
		"    Config:\n"+
		"      foo: bar\n"+
		"    Created At: 1970-01-01T00:00:00Z\n"+
		"    Updated At: 1970-01-01T00:00:00Z\n"+
		"Dead-letter queue:\n"+
		"  Plugin: dlq-plugin"))
}
