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
	"github.com/conduitio/conduit/cmd/conduit/internal/display"
	"github.com/conduitio/conduit/cmd/conduit/internal/testutils"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/conduitio/ecdysis"
	"github.com/matryer/is"
	"github.com/spf13/pflag"
	"go.uber.org/mock/gomock"
)

func TestConnectorsListCommandFlags(t *testing.T) {
	is := is.New(t)

	expectedFlags := []struct {
		longName   string
		shortName  string
		usage      string
		persistent bool
	}{
		{longName: "pipeline-id", usage: "filter connectors by pipeline ID"},
	}

	e := ecdysis.New()
	c := e.MustBuildCobraCommand(&ListCommand{})

	persistentFlags := c.PersistentFlags()
	cmdFlags := c.Flags()

	for _, f := range expectedFlags {
		var cf *pflag.Flag

		if f.persistent {
			cf = persistentFlags.Lookup(f.longName)
		} else {
			cf = cmdFlags.Lookup(f.longName)
		}
		is.True(cf != nil)
		is.Equal(f.longName, cf.Name)
		is.Equal(f.shortName, cf.Shorthand)
		is.Equal(cf.Usage, f.usage)
	}
}

func TestListCommandExecuteWithClient(t *testing.T) {
	tests := []struct {
		name       string
		flags      ListFlags
		connectors []*apiv1.Connector
	}{
		{
			name:  "WithConnectorsAndNoFlags",
			flags: ListFlags{},
			connectors: []*apiv1.Connector{
				{
					Id:           "conn1",
					Type:         apiv1.Connector_TYPE_SOURCE,
					Plugin:       "plugin1",
					ProcessorIds: []string{"proc3"},
					PipelineId:   "pipeline1",
					CreatedAt:    testutils.GetDateTime(),
					UpdatedAt:    testutils.GetDateTime(),
				},
				{
					Id:         "conn2",
					Type:       apiv1.Connector_TYPE_DESTINATION,
					Plugin:     "plugin2",
					PipelineId: "pipeline2",
					CreatedAt:  testutils.GetDateTime(),
					UpdatedAt:  testutils.GetDateTime(),
				},
			},
		},
		{
			name:  "WithConnectorsAndFlags",
			flags: ListFlags{PipelineID: "pipeline1"},
			connectors: []*apiv1.Connector{
				{
					Id:           "conn1",
					Type:         apiv1.Connector_TYPE_SOURCE,
					Plugin:       "plugin1",
					ProcessorIds: []string{"proc3"},
					PipelineId:   "pipeline1",
					CreatedAt:    testutils.GetDateTime(),
					UpdatedAt:    testutils.GetDateTime(),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)

			buf := new(bytes.Buffer)
			out := &ecdysis.DefaultOutput{}
			out.Output(buf, nil)

			ctx := context.Background()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockService := mock.NewMockConnectorService(ctrl)

			testutils.MockGetListConnectors(mockService, tt.flags.PipelineID, tt.connectors)

			client := &api.Client{
				ConnectorServiceClient: mockService,
			}

			cmd := &ListCommand{
				flags: tt.flags,
			}
			cmd.Output(out)

			err := cmd.ExecuteWithClient(ctx, client)
			is.NoErr(err)

			output := buf.String()
			is.True(len(output) > 0)

			is.True(strings.Contains(output, "ID"))
			is.True(strings.Contains(output, "PLUGIN"))
			is.True(strings.Contains(output, "TYPE"))
			is.True(strings.Contains(output, "PIPELINE_ID"))
			is.True(strings.Contains(output, "CREATED"))
			is.True(strings.Contains(output, "LAST_UPDATED"))

			for _, connector := range tt.connectors {
				is.True(strings.Contains(output, connector.Id))
				is.True(strings.Contains(output, connector.Plugin))
				is.True(strings.Contains(output, connector.PipelineId))
				is.True(strings.Contains(output, display.ConnectorTypeToString(connector.Type)))
			}

			is.True(strings.Contains(output, "1970-01-01T00:00:00Z"))
		})
	}
}

func TestListCommandExecuteWithClient_EmptyResponse(t *testing.T) {
	is := is.New(t)

	buf := new(bytes.Buffer)
	out := &ecdysis.DefaultOutput{}
	out.Output(buf, nil)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockService := mock.NewMockConnectorService(ctrl)

	testutils.MockGetListConnectors(mockService, "", []*apiv1.Connector{})
	client := &api.Client{ConnectorServiceClient: mockService}

	cmd := &ListCommand{}
	cmd.Output(out)

	err := cmd.ExecuteWithClient(ctx, client)
	is.NoErr(err)

	output := buf.String()
	is.True(len(output) == 0)
}
