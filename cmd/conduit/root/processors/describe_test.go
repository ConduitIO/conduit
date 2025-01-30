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

package processors

import (
	"bytes"
	"context"
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

	expected := "requires a processor ID"

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
	processorID := "my-processor"

	c := DescribeCommand{}
	err := c.Args([]string{processorID})

	is.NoErr(err)
	is.Equal(c.args.ProcessorID, processorID)
}

func TestDescribeCommand_ExecuteWithClient(t *testing.T) {
	is := is.New(t)

	buf := new(bytes.Buffer)
	out := &ecdysis.DefaultOutput{}
	out.Output(buf, nil)

	ctx := context.Background()
	ctrl := gomock.NewController(t)

	cmd := &DescribeCommand{args: DescribeArgs{ProcessorID: "processor-id"}}
	cmd.Output(out)

	mockProcessorService := mock.NewMockProcessorService(ctrl)
	testutils.MockGetProcessor(
		mockProcessorService,
		cmd.args.ProcessorID, "custom.javascript", `{{ eq .Metadata.filter "true" }}`,
		&apiv1.Processor_Parent{
			Type: apiv1.Processor_Parent_TYPE_CONNECTOR,
			Id:   "source-connector",
		},
		map[string]string{})

	client := &api.Client{
		ProcessorServiceClient: mockProcessorService,
	}

	err := cmd.ExecuteWithClient(ctx, client)
	is.NoErr(err)

	output := buf.String()

	is.Equal(output, ""+
		"ID: processor-id\n"+
		"Plugin: custom.javascript\n"+
		"Parent: connector (source-connector)\n"+
		"Condition: {{ eq .Metadata.filter \"true\" }}\n"+
		"Workers: 0\n"+
		"Created At: 1970-01-01T00:00:00Z\n"+
		"Updated At: 1970-01-01T00:00:00Z\n")
}
