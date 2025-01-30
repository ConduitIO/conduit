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

package processorplugins

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
		{longName: "name", usage: "name to filter processor plugins by"},
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

	mockService := mock.NewMockProcessorService(ctrl)

	testutils.MockGetProcessorPlugins(
		mockService,
		".*builtin.*",
		[]*apiv1.ProcessorPluginSpecifications{
			{
				Name:    "builtin:avro.decode@v0.1.0",
				Summary: "Decodes a field's raw data in the Avro format.",
			},
			{
				Name:    "builtin:avro.encode@v0.1.0",
				Summary: "Encodes a record's field into the Avro format.",
			},
		})

	client := &api.Client{ProcessorServiceClient: mockService}

	err := cmd.ExecuteWithClient(ctx, client)
	is.NoErr(err)

	output := strings.TrimSpace(buf.String())
	is.True(len(output) > 0)

	is.True(strings.Contains(output, ""+
		"+----------------------------+------------------------------------------------+\n"+
		"|            NAME            |                    SUMMARY                     |\n"+
		"+----------------------------+------------------------------------------------+\n"+
		"| builtin:avro.decode@v0.1.0 | Decodes a field's raw data in the Avro format. |\n"+
		"| builtin:avro.encode@v0.1.0 | Encodes a record's field into the Avro format. |\n"+
		"+----------------------------+------------------------------------------------+"))
}

func TestListCommandExecuteWithClient_NoFlags(t *testing.T) {
	is := is.New(t)

	buf := new(bytes.Buffer)
	out := &ecdysis.DefaultOutput{}
	out.Output(buf, nil)

	cmd := &ListCommand{}
	cmd.Output(out)

	ctx := context.Background()
	ctrl := gomock.NewController(t)

	mockService := mock.NewMockProcessorService(ctrl)

	testutils.MockGetProcessorPlugins(
		mockService,
		".*.*",
		[]*apiv1.ProcessorPluginSpecifications{
			{
				Name:    "builtin:avro.decode@v0.1.0",
				Summary: "Decodes a field's raw data in the Avro format.",
			},
			{
				Name:    "builtin:avro.encode@v0.1.0",
				Summary: "Encodes a record's field into the Avro format.",
			},
			{
				Name:    "standalone:processor-simple",
				Summary: "Example of a standalone processor.",
			},
		})

	client := &api.Client{ProcessorServiceClient: mockService}

	err := cmd.ExecuteWithClient(ctx, client)
	is.NoErr(err)

	output := strings.TrimSpace(buf.String())
	is.True(len(output) > 0)

	is.True(strings.Contains(output, ""+
		"+-----------------------------+------------------------------------------------+\n"+
		"|            NAME             |                    SUMMARY                     |\n"+
		"+-----------------------------+------------------------------------------------+\n"+
		"| builtin:avro.decode@v0.1.0  | Decodes a field's raw data in the Avro format. |\n"+
		"| builtin:avro.encode@v0.1.0  | Encodes a record's field into the Avro format. |\n"+
		"| standalone:processor-simple | Example of a standalone processor.             |\n"+
		"+-----------------------------+------------------------------------------------+"))
}

func TestListCommandExecuteWithClient_EmptyResponse(t *testing.T) {
	is := is.New(t)

	buf := new(bytes.Buffer)
	out := &ecdysis.DefaultOutput{}
	out.Output(buf, nil)

	ctx := context.Background()
	ctrl := gomock.NewController(t)

	mockService := mock.NewMockProcessorService(ctrl)

	testutils.MockGetProcessorPlugins(
		mockService,
		".*.*",
		[]*apiv1.ProcessorPluginSpecifications{})

	client := &api.Client{ProcessorServiceClient: mockService}
	cmd := &ListCommand{}
	cmd.Output(out)

	err := cmd.ExecuteWithClient(ctx, client)
	is.NoErr(err)

	output := strings.TrimSpace(buf.String())
	is.True(len(output) == 0)
}
