// Copyright © 2026 Meroxa, Inc.
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
	"context"
	"strings"
	"testing"

	"github.com/matryer/is"
	"go.uber.org/mock/gomock"

	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/cmd/conduit/api/mock"
	"github.com/conduitio/conduit/cmd/conduit/internal/testutils"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/pipeline"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
)

func TestStartExecutionArgs(t *testing.T) {
	is := is.New(t)

	is.Equal((&StartCommand{}).Args([]string{}).Error(), "requires a pipeline ID")
	is.Equal((&StartCommand{}).Args([]string{"a", "b"}).Error(), "too many arguments")

	c := &StartCommand{}
	is.NoErr(c.Args([]string{"orders"}))
	is.Equal(c.args.PipelineID, "orders")
}

// AC-1/AC-2: start on a stopped pipeline calls StartPipeline, reads the
// resulting status back via GetPipeline, and renders both the --json shape
// (via LifecycleResult) and the human text line.
func TestStartCommandExecuteWithClient(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	cmd := &StartCommand{args: StartArgs{PipelineID: "orders"}}

	mockPipeline := mock.NewMockPipelineService(ctrl)
	testutils.MockStartPipeline(mockPipeline, "orders", nil)
	testutils.MockGetPipelineWithStatus(mockPipeline, "orders", apiv1.Pipeline_STATUS_RUNNING)

	client := &api.Client{PipelineServiceClient: mockPipeline}

	result, err := cmd.ExecuteWithClientResult(ctx, client)
	is.NoErr(err)

	lr, ok := result.(*LifecycleResult)
	is.True(ok)
	is.Equal(lr.PipelineID, "orders")
	is.Equal(lr.Action, "start")
	is.Equal(lr.Force, false)
	is.Equal(lr.Status, "Running")

	out := cmd.Render(result)
	is.True(strings.Contains(out, "Pipeline orders  [running]"))
}

// AC-6: starting an already-running pipeline surfaces the server's
// pipeline.running error unchanged, and GetPipeline is never called (nothing
// mutated, no further RPCs attempted after the failure).
func TestStartCommandExecuteWithClient_AlreadyRunning(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	cmd := &StartCommand{args: StartArgs{PipelineID: "orders"}}

	wantErr := conduiterr.Wrap(pipeline.CodePipelineRunning, "can't start pipeline orders: already running", pipeline.ErrPipelineRunning)
	mockPipeline := mock.NewMockPipelineService(ctrl)
	mockPipeline.EXPECT().StartPipeline(gomock.Any(), &apiv1.StartPipelineRequest{Id: "orders"}).
		Return(nil, wantErr).Times(1)
	// No GetPipeline expectation: the gomock controller fails the test if the
	// command calls it after StartPipeline already failed.

	client := &api.Client{PipelineServiceClient: mockPipeline}

	_, err := cmd.ExecuteWithClientResult(ctx, client)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, pipeline.CodePipelineRunning)
}

// AC-8: an unknown pipeline ID surfaces pipeline.instance_not_found unchanged.
func TestStartCommandExecuteWithClient_NotFound(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	cmd := &StartCommand{args: StartArgs{PipelineID: "nope"}}

	wantErr := conduiterr.Wrap(pipeline.CodePipelineNotFound, "pipeline nope not found", pipeline.ErrInstanceNotFound)
	mockPipeline := mock.NewMockPipelineService(ctrl)
	mockPipeline.EXPECT().StartPipeline(gomock.Any(), &apiv1.StartPipelineRequest{Id: "nope"}).
		Return(nil, wantErr).Times(1)

	client := &api.Client{PipelineServiceClient: mockPipeline}

	_, err := cmd.ExecuteWithClientResult(ctx, client)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, pipeline.CodePipelineNotFound)
}
