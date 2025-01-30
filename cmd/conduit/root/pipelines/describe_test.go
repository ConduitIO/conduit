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

	"github.com/conduitio/ecdysis"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"

	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/cmd/conduit/api/mock"
	"github.com/conduitio/conduit/cmd/conduit/internal/testutils"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
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

func TestDescribeCommandExecuteWithClient(t *testing.T) {
	is := is.New(t)

	buf := new(bytes.Buffer)
	out := &ecdysis.DefaultOutput{}
	out.Output(buf, nil)

	ctx := context.Background()
	ctrl := gomock.NewController(t)

	cmd := &DescribeCommand{args: DescribeArgs{PipelineID: "1"}}
	cmd.Output(out)

	mockPipelineService := mock.NewMockPipelineService(ctrl)
	mockProcessorService := mock.NewMockProcessorService(ctrl)
	mockConnectorService := mock.NewMockConnectorService(ctrl)

	testutils.MockGetPipeline(mockPipelineService, cmd.args.PipelineID, []string{"conn1", "conn2"}, []string{"proc1", "proc2"})

	testutils.MockGetProcessor(
		mockProcessorService,
		"proc1", "field.set", "",
		nil,
		map[string]string{
			".Payload.After.department": "finance",
		})

	testutils.MockGetProcessor(
		mockProcessorService,
		"proc2", "custom.javascript", "",
		nil,
		map[string]string{})

	testutils.MockGetConnectors(mockConnectorService, cmd.args.PipelineID, []*apiv1.Connector{
		{Id: "conn1", Type: apiv1.Connector_TYPE_SOURCE, Plugin: "plugin1", ProcessorIds: []string{"proc3"}},
		{Id: "conn2", Type: apiv1.Connector_TYPE_DESTINATION, Plugin: "plugin2"},
	})

	testutils.MockGetConnector(
		mockConnectorService, "conn1", "plugin1", cmd.args.PipelineID, apiv1.Connector_TYPE_SOURCE,
		&apiv1.Connector_Config{
			Name: "Test Pipeline",
			Settings: map[string]string{
				"foo": "bar",
			},
		}, []string{"proc3"})

	testutils.MockGetProcessor(
		mockProcessorService,
		"proc3", "custom.javascript", "",
		nil,
		map[string]string{})

	testutils.MockGetConnector(
		mockConnectorService, "conn2", "plugin2", cmd.args.PipelineID, apiv1.Connector_TYPE_DESTINATION,
		&apiv1.Connector_Config{
			Name: "Test Pipeline",
			Settings: map[string]string{
				"foo": "bar",
			},
		}, []string{"proc3"})

	testutils.MockGetDLQ(mockPipelineService, cmd.args.PipelineID, "dlq-plugin")

	client := &api.Client{
		PipelineServiceClient:  mockPipelineService,
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
		"  Plugin: dlq-plugin\n"+
		"Created At: 1970-01-01T00:00:00Z\n"+
		"Updated At: 1970-01-01T00:00:00Z"))
}
