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

func TestListCommandExecuteWithClient(t *testing.T) {
	is := is.New(t)

	buf := new(bytes.Buffer)
	out := &ecdysis.DefaultOutput{}
	out.Output(buf, nil)

	ctx := context.Background()
	ctrl := gomock.NewController(t)

	mockService := mock.NewMockProcessorService(ctrl)

	processors := []*apiv1.Processor{
		{
			Id: "processor1",
			Parent: &apiv1.Processor_Parent{
				Type: apiv1.Processor_Parent_TYPE_CONNECTOR,
				Id:   "source-connector",
			},
			Plugin:    "plugin1",
			Condition: `{{ eq .Metadata.filter "true" }}`,
			Config: &apiv1.Processor_Config{
				Settings: map[string]string{
					"foo": "bar",
				},
			},
			CreatedAt: testutils.GetDateTime(),
			UpdatedAt: testutils.GetDateTime(),
		},
		{
			Id: "processor2",
			Parent: &apiv1.Processor_Parent{
				Type: apiv1.Processor_Parent_TYPE_PIPELINE,
				Id:   "pipeline",
			},
			Plugin:    "plugin2",
			Config:    &apiv1.Processor_Config{Settings: map[string]string{}},
			CreatedAt: testutils.GetDateTime(),
			UpdatedAt: testutils.GetDateTime(),
		},
	}

	testutils.MockGetProcessors(mockService, processors)

	client := &api.Client{
		ProcessorServiceClient: mockService,
	}

	cmd := &ListCommand{}
	cmd.Output(out)

	err := cmd.ExecuteWithClient(ctx, client)
	is.NoErr(err)

	output := buf.String()
	is.True(len(output) > 0)
	strings.Contains(output, ""+
		"+------------+---------+------------------------------+----------------------------------+----------------------+----------------------+\n"+
		"|     ID     | PLUGIN  |            PARENT            |            CONDITION             |       CREATED        |     LAST_UPDATED     |\n"+
		"+------------+---------+------------------------------+----------------------------------+----------------------+----------------------+\n"+
		"| processor1 | plugin1 | connector (source-connector) | {{ eq .Metadata.filter \"true\" }} | 1970-01-01T00:00:00Z | 1970-01-01T00:00:00Z |\n"+
		"| processor2 | plugin2 | pipeline (pipeline)          |                                  | 1970-01-01T00:00:00Z | 1970-01-01T00:00:00Z |\n"+
		"+------------+---------+------------------------------+----------------------------------+----------------------+----------------------+")
}

func TestListCommandExecuteWithClient_EmptyResponse(t *testing.T) {
	is := is.New(t)

	buf := new(bytes.Buffer)
	out := &ecdysis.DefaultOutput{}
	out.Output(buf, nil)

	ctx := context.Background()
	ctrl := gomock.NewController(t)

	mockService := mock.NewMockProcessorService(ctrl)

	testutils.MockGetProcessors(mockService, []*apiv1.Processor{})
	client := &api.Client{ProcessorServiceClient: mockService}

	cmd := &ListCommand{}
	cmd.Output(out)

	err := cmd.ExecuteWithClient(ctx, client)
	is.NoErr(err)

	output := strings.TrimSpace(buf.String())
	is.True(len(output) == 0)
}
