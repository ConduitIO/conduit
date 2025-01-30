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

package processorplugins

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

	expected := "requires a processor plugin ID"

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
	processorPluginID := "processor-plugin-id"

	c := DescribeCommand{}
	err := c.Args([]string{processorPluginID})

	is.NoErr(err)
	is.Equal(c.args.processorPluginID, processorPluginID)
}

func TestDescribeCommand_ExecuteWithClient(t *testing.T) {
	is := is.New(t)

	buf := new(bytes.Buffer)
	out := &ecdysis.DefaultOutput{}
	out.Output(buf, nil)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cmd := &DescribeCommand{args: DescribeArgs{processorPluginID: "builtin:base64.encode@v0.1.0"}}
	cmd.Output(out)

	mockProcessorService := mock.NewMockProcessorService(ctrl)

	testutils.MockGetProcessorPlugins(
		mockProcessorService,
		cmd.args.processorPluginID,
		[]*apiv1.ProcessorPluginSpecifications{
			{
				Name:    cmd.args.processorPluginID,
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
		})

	client := &api.Client{
		ProcessorServiceClient: mockProcessorService,
	}

	err := cmd.ExecuteWithClient(ctx, client)
	is.NoErr(err)

	output := buf.String()

	is.True(strings.Contains(output, ""+
		"Name: builtin:base64.encode@v0.1.0\n"+
		"Summary: Encode a field to base64\n"+
		"Description: The processor will encode the value of the target field to base64 and store the\n"+
		"result in the target field. It is not allowed to encode the `.Position` field.\n"+
		"If the provided field doesn't exist, the processor will create that field and\n"+
		"assign its value. Field is a reference to the target field. Note that it is not allowed to base64 encode. `.Position` field. \n"+
		"Author: Meroxa, Inc.\n"+
		"Version: v0.1.0\n"+
		"Parameters:\n"+
		"+-------+--------+------------------------------------------+---------+-------------------------+\n"+
		"| NAME  |  TYPE  |               DESCRIPTION                | DEFAULT |       VALIDATIONS       |\n"+
		"+-------+--------+------------------------------------------+---------+-------------------------+\n"+
		"| field | string | Field is a reference to the target field |         | [required], [exclusion] |\n"+
		"+-------+--------+------------------------------------------+---------+-------------------------+"))
}
