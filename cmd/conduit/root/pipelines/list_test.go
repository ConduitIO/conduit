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

func TestListCommandExecuteWithClient(t *testing.T) {
	is := is.New(t)

	buf := new(bytes.Buffer)
	out := &ecdysis.DefaultOutput{}
	out.Output(buf, nil)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockService := mock.NewMockPipelineService(ctrl)

	testutils.MockGetPipelines(mockService, []*apiv1.Pipeline{
		{Id: "1", State: &apiv1.Pipeline_State{Status: apiv1.Pipeline_STATUS_RUNNING}},
		{Id: "2", State: &apiv1.Pipeline_State{Status: apiv1.Pipeline_STATUS_STOPPED}},
		{Id: "3", State: &apiv1.Pipeline_State{Status: apiv1.Pipeline_STATUS_RECOVERING}},
		{Id: "4", State: &apiv1.Pipeline_State{Status: apiv1.Pipeline_STATUS_DEGRADED}},
	})

	client := &api.Client{
		PipelineServiceClient: mockService,
	}

	cmd := &ListCommand{}
	cmd.Output(out)

	err := cmd.ExecuteWithClient(ctx, client)
	is.NoErr(err)

	output := buf.String()
	is.True(len(output) > 0)

	is.True(strings.Contains(output, ""+
		"+----+------------+----------------------+----------------------+\n"+
		"| ID |   STATE    |       CREATED        |     LAST_UPDATED     |\n"+
		"+----+------------+----------------------+----------------------+\n"+
		"| 1  | running    | 1970-01-01T00:00:00Z | 1970-01-01T00:00:00Z |\n"+
		"| 2  | stopped    | 1970-01-01T00:00:00Z | 1970-01-01T00:00:00Z |\n"+
		"| 3  | recovering | 1970-01-01T00:00:00Z | 1970-01-01T00:00:00Z |\n"+
		"| 4  | degraded   | 1970-01-01T00:00:00Z | 1970-01-01T00:00:00Z |\n"+
		"+----+------------+----------------------+----------------------+"))
}

func TestListCommandExecuteWithClient_EmptyResponse(t *testing.T) {
	is := is.New(t)

	buf := new(bytes.Buffer)
	out := &ecdysis.DefaultOutput{}
	out.Output(buf, nil)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockService := mock.NewMockPipelineService(ctrl)

	testutils.MockGetPipelines(mockService, []*apiv1.Pipeline{})
	client := &api.Client{
		PipelineServiceClient: mockService,
	}

	cmd := &ListCommand{}
	cmd.Output(out)

	err := cmd.ExecuteWithClient(ctx, client)
	is.NoErr(err)

	output := strings.TrimSpace(buf.String())
	is.True(len(output) == 0)
}
