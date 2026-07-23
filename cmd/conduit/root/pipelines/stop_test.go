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
	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal/testutils"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/pipeline"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
)

func TestStopExecutionArgs(t *testing.T) {
	is := is.New(t)

	is.Equal((&StopCommand{}).Args([]string{}).Error(), "requires a pipeline ID")
	is.Equal((&StopCommand{}).Args([]string{"a", "b"}).Error(), "too many arguments")

	c := &StopCommand{}
	is.NoErr(c.Args([]string{"orders"}))
	is.Equal(c.args.PipelineID, "orders")
	is.Equal(c.flags.Force, false) // default: graceful
}

// AC-3: stop on a running pipeline calls StopPipeline with force=false (the
// graceful path), reads the status back, and the result carries no stray
// "force" (Force is the Go zero value, JSON-omitted).
func TestStopCommandExecuteWithClient_Graceful(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	cmd := &StopCommand{args: StopArgs{PipelineID: "orders"}}

	mockPipeline := mock.NewMockPipelineService(ctrl)
	testutils.MockStopPipeline(mockPipeline, "orders", false, nil)
	testutils.MockGetPipelineWithStatus(mockPipeline, "orders", apiv1.Pipeline_STATUS_STOPPED)

	client := &api.Client{PipelineServiceClient: mockPipeline}

	result, err := cmd.ExecuteWithClientResult(ctx, client)
	is.NoErr(err)

	lr, ok := result.(*LifecycleResult)
	is.True(ok)
	is.Equal(lr.PipelineID, "orders")
	is.Equal(lr.Action, "stop")
	is.Equal(lr.Force, false)
	is.Equal(lr.Status, "UserStopped")

	// Family B negative check (v0.19 workstream 8 — cli-contract.md §4.6):
	// this command's --json output must never accidentally start
	// conforming to Family A's envelope shape.
	b, err := cecdysis.MarshalJSON(result)
	is.NoErr(err)
	is.True(!testutils.MatchesEnvelope(b))

	out := cmd.Render(result)
	is.True(strings.Contains(out, "Pipeline orders  [stopped]"))
}

// AC-4: --force calls StopPipeline with force=true and the result carries
// Force:true.
func TestStopCommandExecuteWithClient_Force(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	cmd := &StopCommand{args: StopArgs{PipelineID: "orders"}, flags: StopFlags{Force: true}}

	mockPipeline := mock.NewMockPipelineService(ctrl)
	testutils.MockStopPipeline(mockPipeline, "orders", true, nil)
	testutils.MockGetPipelineWithStatus(mockPipeline, "orders", apiv1.Pipeline_STATUS_STOPPED)

	client := &api.Client{PipelineServiceClient: mockPipeline}

	result, err := cmd.ExecuteWithClientResult(ctx, client)
	is.NoErr(err)

	lr, ok := result.(*LifecycleResult)
	is.True(ok)
	is.Equal(lr.Force, true)
	is.Equal(lr.Status, "UserStopped")
}

// AC-7: stopping an already-stopped pipeline surfaces pipeline.not_running
// unchanged, and GetPipeline is never called.
func TestStopCommandExecuteWithClient_NotRunning(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	cmd := &StopCommand{args: StopArgs{PipelineID: "orders"}}

	wantErr := conduiterr.Wrap(pipeline.CodePipelineNotRunning, `can't stop pipeline with status "UserStopped"`, pipeline.ErrPipelineNotRunning)
	mockPipeline := mock.NewMockPipelineService(ctrl)
	mockPipeline.EXPECT().StopPipeline(gomock.Any(), &apiv1.StopPipelineRequest{Id: "orders", Force: false}).
		Return(nil, wantErr).Times(1)

	client := &api.Client{PipelineServiceClient: mockPipeline}

	_, err := cmd.ExecuteWithClientResult(ctx, client)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, pipeline.CodePipelineNotRunning)
}

// AC-8: an unknown pipeline ID surfaces pipeline.instance_not_found unchanged.
func TestStopCommandExecuteWithClient_NotFound(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	cmd := &StopCommand{args: StopArgs{PipelineID: "nope"}}

	wantErr := conduiterr.Wrap(pipeline.CodePipelineNotFound, "pipeline nope not found", pipeline.ErrInstanceNotFound)
	mockPipeline := mock.NewMockPipelineService(ctrl)
	mockPipeline.EXPECT().StopPipeline(gomock.Any(), &apiv1.StopPipelineRequest{Id: "nope", Force: false}).
		Return(nil, wantErr).Times(1)

	client := &api.Client{PipelineServiceClient: mockPipeline}

	_, err := cmd.ExecuteWithClientResult(ctx, client)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, pipeline.CodePipelineNotFound)
}
