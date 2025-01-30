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

package connectorplugins

import (
	"bytes"
	"context"
	"strings"
	"testing"

	configv1 "github.com/conduitio/conduit-commons/proto/config/v1"
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

	expected := "requires a connector plugin ID"

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
	connectorPluginID := "connector-plugin-id"

	c := DescribeCommand{}
	err := c.Args([]string{connectorPluginID})

	is.NoErr(err)
	is.Equal(c.args.connectorPluginID, connectorPluginID)
}

func TestDescribeCommand_ExecuteWithClient(t *testing.T) {
	is := is.New(t)

	buf := new(bytes.Buffer)
	out := &ecdysis.DefaultOutput{}
	out.Output(buf, nil)

	ctx := context.Background()
	ctrl := gomock.NewController(t)

	cmd := &DescribeCommand{args: DescribeArgs{connectorPluginID: "builtin:kafka@v0.11.1"}}
	cmd.Output(out)

	mockConnectorService := mock.NewMockConnectorService(ctrl)

	testutils.MockGetConnectorPlugins(
		mockConnectorService,
		cmd.args.connectorPluginID,
		[]*apiv1.ConnectorPluginSpecifications{
			{
				Name:        cmd.args.connectorPluginID,
				Summary:     "A Kafka source and destination plugin for Conduit.",
				Description: "A Kafka source and destination plugin for Conduit, written in Go.",
				Author:      "Meroxa, Inc.",
				Version:     "v0.11.1",
				SourceParams: map[string]*configv1.Parameter{
					"sdk.schema.extract.type": {
						Type:        configv1.Parameter_Type(apiv1.PluginSpecifications_Parameter_TYPE_STRING),
						Description: "The type of the payload schema.",
						Default:     "avro",
						Validations: []*configv1.Validation{
							{Type: configv1.Validation_TYPE_INCLUSION},
						},
					},
				},
				DestinationParams: map[string]*configv1.Parameter{
					"sdk.record.format": {
						Type:        configv1.Parameter_Type(apiv1.PluginSpecifications_Parameter_TYPE_UNSPECIFIED),
						Description: "The format of the output record.",
						Default:     "opencdc/json",
						Validations: []*configv1.Validation{
							{Type: configv1.Validation_TYPE_INCLUSION},
						},
					},
				},
			},
		})

	client := &api.Client{
		ConnectorServiceClient: mockConnectorService,
	}

	err := cmd.ExecuteWithClient(ctx, client)
	is.NoErr(err)

	output := buf.String()

	is.True(strings.Contains(output, ""+
		"Name: builtin:kafka@v0.11.1\n"+
		"Summary: A Kafka source and destination plugin for Conduit.\n"+
		"Description: A Kafka source and destination plugin for Conduit, written in Go.\n"+
		"Author: Meroxa, Inc.\n"+
		"Version: v0.11.1\n"+
		"\n"+
		"Source Parameters:\n"+
		"+-------------------------+--------+---------------------------------+---------+-------------+\n"+
		"|          NAME           |  TYPE  |           DESCRIPTION           | DEFAULT | VALIDATIONS |\n"+
		"+-------------------------+--------+---------------------------------+---------+-------------+\n"+
		"| sdk.schema.extract.type | string | The type of the payload schema. | avro    | [inclusion] |\n"+
		"+-------------------------+--------+---------------------------------+---------+-------------+\n"+
		"\n"+
		"Destination Parameters:\n"+
		"+-------------------+-------------+----------------------------------+--------------+-------------+\n"+
		"|       NAME        |    TYPE     |           DESCRIPTION            |   DEFAULT    | VALIDATIONS |\n"+
		"+-------------------+-------------+----------------------------------+--------------+-------------+\n"+
		"| sdk.record.format | unspecified | The format of the output record. | opencdc/json | [inclusion] |\n"+
		"+-------------------+-------------+----------------------------------+--------------+-------------+"))
}
