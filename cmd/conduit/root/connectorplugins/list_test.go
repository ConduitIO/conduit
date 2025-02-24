// Copyright © 2024 Meroxa, Inc.
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

	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/cmd/conduit/api/mock"
	"github.com/conduitio/conduit/cmd/conduit/internal/testutils"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/conduitio/ecdysis"
	"github.com/matryer/is"
	"github.com/spf13/pflag"
	"go.uber.org/mock/gomock"
)

func TestListCommandFlags(t *testing.T) {
	is := is.New(t)

	expectedFlags := []struct {
		longName   string
		shortName  string
		usage      string
		persistent bool
	}{
		{longName: "name", usage: "name to filter connector plugins by"},
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

func TestListCommandExecuteWithClient_WithFlags(t *testing.T) {
	is := is.New(t)

	buf := new(bytes.Buffer)
	out := &ecdysis.DefaultOutput{}
	out.Output(buf, nil)

	cmd := &ListCommand{
		flags: ListFlags{
			Name: "builtin",
		},
	}
	cmd.Output(out)

	ctx := context.Background()
	ctrl := gomock.NewController(t)

	mockService := mock.NewMockConnectorService(ctrl)

	testutils.MockGetConnectorPlugins(
		mockService,
		".*builtin.*",
		[]*apiv1.ConnectorPluginSpecifications{
			{
				Name:    "builtin:file@v0.9.0",
				Summary: "A file source and destination plugin for Conduit.",
			},
			{
				Name:    "builtin:kafka@v0.11.1",
				Summary: "A Kafka source and destination plugin for Conduit, written in Go.",
			},
		})

	client := &api.Client{ConnectorServiceClient: mockService}

	err := cmd.ExecuteWithClient(ctx, client)
	is.NoErr(err)

	output := buf.String()

	is.Equal(output, ""+
		"+-----------------------+-------------------------------------------------------------------+\n"+
		"|         NAME          |                              SUMMARY                              |\n"+
		"+-----------------------+-------------------------------------------------------------------+\n"+
		"| builtin:file@v0.9.0   | A file source and destination plugin for Conduit.                 |\n"+
		"| builtin:kafka@v0.11.1 | A Kafka source and destination plugin for Conduit, written in Go. |\n"+
		"+-----------------------+-------------------------------------------------------------------+\n")
}

func TestListCommandExecuteWithClient_NoFlags(t *testing.T) {
	is := is.New(t)

	buf := new(bytes.Buffer)
	out := &ecdysis.DefaultOutput{}
	out.Output(buf, nil)

	cmd := &ListCommand{
		flags: ListFlags{},
	}
	cmd.Output(out)

	ctx := context.Background()
	ctrl := gomock.NewController(t)

	mockService := mock.NewMockConnectorService(ctrl)

	testutils.MockGetConnectorPlugins(
		mockService,
		".*.*",
		[]*apiv1.ConnectorPluginSpecifications{
			{
				Name:    "builtin:file@v0.9.0",
				Summary: "A file source and destination plugin for Conduit.",
			},
			{
				Name:    "builtin:kafka@v0.11.1",
				Summary: "A Kafka source and destination plugin for Conduit, written in Go.",
			},
			{
				Name:    "standalone:chaos@v0.1.1",
				Summary: "A chaos destination connector",
			},
		})

	client := &api.Client{ConnectorServiceClient: mockService}

	err := cmd.ExecuteWithClient(ctx, client)
	is.NoErr(err)

	output := buf.String()

	is.Equal(output, ""+
		"+-------------------------+-------------------------------------------------------------------+\n"+
		"|          NAME           |                              SUMMARY                              |\n"+
		"+-------------------------+-------------------------------------------------------------------+\n"+
		"| builtin:file@v0.9.0     | A file source and destination plugin for Conduit.                 |\n"+
		"| builtin:kafka@v0.11.1   | A Kafka source and destination plugin for Conduit, written in Go. |\n"+
		"| standalone:chaos@v0.1.1 | A chaos destination connector                                     |\n"+
		"+-------------------------+-------------------------------------------------------------------+\n")
}

func TestListCommandExecuteWithClient_EmptyResponse(t *testing.T) {
	is := is.New(t)

	buf := new(bytes.Buffer)
	out := &ecdysis.DefaultOutput{}
	out.Output(buf, nil)

	ctx := context.Background()
	ctrl := gomock.NewController(t)

	mockService := mock.NewMockConnectorService(ctrl)

	testutils.MockGetConnectorPlugins(
		mockService,
		".*.*",
		[]*apiv1.ConnectorPluginSpecifications{})

	client := &api.Client{ConnectorServiceClient: mockService}

	cmd := &ListCommand{}
	cmd.Output(out)

	err := cmd.ExecuteWithClient(ctx, client)
	is.NoErr(err)

	output := strings.TrimSpace(buf.String())
	is.True(len(output) == 0)
}
