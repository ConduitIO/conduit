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

func TestListCommandExecuteWithClient_NoFlags(t *testing.T) {
	is := is.New(t)

	buf := new(bytes.Buffer)
	out := &ecdysis.DefaultOutput{}
	out.Output(buf, nil)

	ctx := context.Background()
	ctrl := gomock.NewController(t)

	mockService := mock.NewMockConnectorService(ctrl)

	connectors := []*apiv1.Connector{
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
	}

	testutils.MockGetConnectors(mockService, "", connectors)

	client := &api.Client{
		ConnectorServiceClient: mockService,
	}

	cmd := &ListCommand{}
	cmd.Output(out)

	err := cmd.ExecuteWithClient(ctx, client)
	is.NoErr(err)

	output := buf.String()
	is.Equal(output, ""+
		"+-------+---------+-------------+-------------+----------------------+----------------------+\n"+
		"|  ID   | PLUGIN  |    TYPE     | PIPELINE_ID |       CREATED        |     LAST_UPDATED     |\n"+
		"+-------+---------+-------------+-------------+----------------------+----------------------+\n"+
		"| conn1 | plugin1 | source      | pipeline1   | 1970-01-01T00:00:00Z | 1970-01-01T00:00:00Z |\n"+
		"| conn2 | plugin2 | destination | pipeline2   | 1970-01-01T00:00:00Z | 1970-01-01T00:00:00Z |\n"+
		"+-------+---------+-------------+-------------+----------------------+----------------------+\n")
}

func TestListCommandExecuteWithClient_WithFlags(t *testing.T) {
	is := is.New(t)

	buf := new(bytes.Buffer)
	out := &ecdysis.DefaultOutput{}
	out.Output(buf, nil)

	ctx := context.Background()
	ctrl := gomock.NewController(t)

	mockService := mock.NewMockConnectorService(ctrl)

	connectors := []*apiv1.Connector{
		{
			Id:           "conn1",
			Type:         apiv1.Connector_TYPE_SOURCE,
			Plugin:       "plugin1",
			ProcessorIds: []string{"proc3"},
			PipelineId:   "pipeline1",
			CreatedAt:    testutils.GetDateTime(),
			UpdatedAt:    testutils.GetDateTime(),
		},
	}

	testutils.MockGetConnectors(mockService, "pipeline1", connectors)

	client := &api.Client{
		ConnectorServiceClient: mockService,
	}

	cmd := &ListCommand{
		flags: ListFlags{PipelineID: "pipeline1"},
	}
	cmd.Output(out)

	err := cmd.ExecuteWithClient(ctx, client)
	is.NoErr(err)

	output := buf.String()

	is.Equal(output, ""+
		"+-------+---------+--------+-------------+----------------------+----------------------+\n"+
		"|  ID   | PLUGIN  |  TYPE  | PIPELINE_ID |       CREATED        |     LAST_UPDATED     |\n"+
		"+-------+---------+--------+-------------+----------------------+----------------------+\n"+
		"| conn1 | plugin1 | source | pipeline1   | 1970-01-01T00:00:00Z | 1970-01-01T00:00:00Z |\n"+
		"+-------+---------+--------+-------------+----------------------+----------------------+\n")
}

func TestListCommandExecuteWithClient_EmptyResponse(t *testing.T) {
	is := is.New(t)

	buf := new(bytes.Buffer)
	out := &ecdysis.DefaultOutput{}
	out.Output(buf, nil)

	ctx := context.Background()
	ctrl := gomock.NewController(t)

	mockService := mock.NewMockConnectorService(ctrl)

	testutils.MockGetConnectors(mockService, "", []*apiv1.Connector{})
	client := &api.Client{ConnectorServiceClient: mockService}

	cmd := &ListCommand{}
	cmd.Output(out)

	err := cmd.ExecuteWithClient(ctx, client)
	is.NoErr(err)

	output := strings.TrimSpace(buf.String())
	is.True(len(output) == 0)
}
