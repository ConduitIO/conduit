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
	"github.com/conduitio/conduit/cmd/conduit/api/mock"
	"github.com/conduitio/ecdysis"
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/proto/api/v1"
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
	defer ctrl.Finish()

	mockService := mock.NewMockPipelineService(ctrl)

	mockService.EXPECT().ListPipelines(gomock.Any(), gomock.Any()).Return(&apiv1.ListPipelinesResponse{
		Pipelines: []*apiv1.Pipeline{
			{Id: "1", State: &apiv1.Pipeline_State{Status: apiv1.Pipeline_STATUS_RUNNING}},
			{Id: "2", State: &apiv1.Pipeline_State{Status: apiv1.Pipeline_STATUS_STOPPED}},
			{Id: "3", State: &apiv1.Pipeline_State{Status: apiv1.Pipeline_STATUS_RECOVERING}},
			{Id: "4", State: &apiv1.Pipeline_State{Status: apiv1.Pipeline_STATUS_DEGRADED}},
		},
	}, nil)

	client := &api.Client{
		PipelineServiceClient: mockService,
	}

	cmd := &ListCommand{}
	cmd.Output(out)

	err := cmd.ExecuteWithClient(ctx, client)
	is.NoErr(err)

	output := buf.String()
	is.True(len(output) > 0)
	is.True(strings.Contains(output, "ID"))
	is.True(strings.Contains(output, "CREATED"))
	is.True(strings.Contains(output, "LAST_UPDATED"))

	is.True(strings.Contains(output, "1"))
	is.True(strings.Contains(output, "running"))
	is.True(strings.Contains(output, "2"))
	is.True(strings.Contains(output, "stopped"))
	is.True(strings.Contains(output, "3"))
	is.True(strings.Contains(output, "recovering"))
	is.True(strings.Contains(output, "4"))
	is.True(strings.Contains(output, "degraded"))
}
