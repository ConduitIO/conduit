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

package connectors

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/cmd/conduit/api/mock"
	"github.com/conduitio/conduit/cmd/conduit/internal/testutils"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/conduitio/ecdysis"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestDescribeExecutionNoArgs(t *testing.T) {
	is := is.New(t)

	c := DescribeCommand{}
	err := c.Args([]string{})

	expected := "requires a connector ID"

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
	connectorID := "my-connector"

	c := DescribeCommand{}
	err := c.Args([]string{connectorID})

	is.NoErr(err)
	is.Equal(c.args.ConnectorID, connectorID)
}

func TestDescribeCommand_ExecuteWithClient(t *testing.T) {
	const pipelineId = "my-pipeline"
	is := is.New(t)

	buf := new(bytes.Buffer)
	out := &ecdysis.DefaultOutput{}
	out.Output(buf, nil)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cmd := &DescribeCommand{args: DescribeArgs{ConnectorID: "1"}}
	cmd.Output(out)

	mockConnectorService := mock.NewMockConnectorService(ctrl)
	testutils.MockGetConnector(
		mockConnectorService, cmd.args.ConnectorID, "plugin1", pipelineId, apiv1.Connector_TYPE_SOURCE,
		&apiv1.Connector_Config{
			Name: "Test Pipeline",
			Settings: map[string]string{
				"foo": "bar",
			},
		}, []string{"proc3"})

	mockProcessorService := mock.NewMockProcessorService(ctrl)
	testutils.MockGetProcessor(mockProcessorService, "proc3", "custom.javascript", map[string]string{})

	client := &api.Client{
		ProcessorServiceClient: mockProcessorService,
		ConnectorServiceClient: mockConnectorService,
	}

	err := cmd.ExecuteWithClient(ctx, client)
	is.NoErr(err)

	output := buf.String()

	is.True(strings.Contains(output, ""+
		"ID: 1\n"+
		"Type: source\n"+
		"Plugin: plugin1\n"+
		"Pipeline ID: my-pipeline\n"+
		"Config:\n"+
		"  foo: bar\n"+
		"Created At: 1970-01-01T00:00:00Z\n"+
		"Updated At: 1970-01-01T00:00:00Z\n"+
		"Processors:\n"+
		"  - ID: proc3\n"+
		"    Plugin: custom.javascript\n"+
		"    Config:\n"+
		"      Workers: 0\n"+
		"    Created At: 1970-01-01T00:00:00Z\n"+
		"    Updated At: 1970-01-01T00:00:00Z"))
}
