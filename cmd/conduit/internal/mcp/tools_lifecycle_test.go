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

package mcp

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/pipeline"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/matryer/is"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func newServerWithFakeInspectClient(fake *fakeInspectClient, allowMutations bool) *sdkmcp.Server {
	return NewServer(Config{
		AllowMutations: allowMutations,
		APIAddress:     "localhost:0",
		newAPIClient: func(context.Context, string) (inspectClient, error) {
			return fake, nil
		},
	})
}

// TestStart_NoAPIAddress_RefusesWithSuggestion mirrors
// TestInspect_NoAPIAddress_RefusesWithSuggestion: start/stop require a live
// server just like inspect (design doc §3), so the same "no --api-address"
// failure mode applies.
func TestStart_NoAPIAddress_RefusesWithSuggestion(t *testing.T) {
	is := is.New(t)

	srv := NewServer(Config{AllowMutations: true}) // APIAddress left empty
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolStart,
		Arguments: map[string]any{"pipelineId": "orders"},
	})
	is.NoErr(err)
	is.True(res.IsError)

	env := decodeStructuredContent[Result[LifecycleResult]](t, res)
	is.True(env.Error != nil)
	is.Equal(env.Error.Code, conduiterr.CodeUnavailable.Reason())
	is.True(env.Error.Suggestion != "")
}

func TestStop_NoAPIAddress_RefusesWithSuggestion(t *testing.T) {
	is := is.New(t)

	srv := NewServer(Config{AllowMutations: true}) // APIAddress left empty
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolStop,
		Arguments: map[string]any{"pipelineId": "orders"},
	})
	is.NoErr(err)
	is.True(res.IsError)

	env := decodeStructuredContent[Result[LifecycleResult]](t, res)
	is.True(env.Error != nil)
	is.Equal(env.Error.Code, conduiterr.CodeUnavailable.Reason())
	is.True(env.Error.Suggestion != "")
}

// TestStart_EmptyPipelineID_RejectedBeforeCallingRPC is AC-14: an empty
// pipelineId is refused before any RPC — the fake's StartPipeline must never
// be called.
func TestStart_EmptyPipelineID_RejectedBeforeCallingRPC(t *testing.T) {
	is := is.New(t)

	fake := &fakeInspectClient{}
	srv := newServerWithFakeInspectClient(fake, true)
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolStart,
		Arguments: map[string]any{"pipelineId": ""},
	})
	is.NoErr(err)
	is.True(res.IsError)
	is.True(!fake.startCalled)

	env := decodeStructuredContent[Result[LifecycleResult]](t, res)
	is.True(env.Error != nil)
	is.Equal(env.Error.Code, conduiterr.CodeInvalidArgument.Reason())
}

func TestStop_EmptyPipelineID_RejectedBeforeCallingRPC(t *testing.T) {
	is := is.New(t)

	fake := &fakeInspectClient{}
	srv := newServerWithFakeInspectClient(fake, true)
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolStop,
		Arguments: map[string]any{"pipelineId": ""},
	})
	is.NoErr(err)
	is.True(res.IsError)
	is.True(!fake.stopCalled)

	env := decodeStructuredContent[Result[LifecycleResult]](t, res)
	is.True(env.Error != nil)
	is.Equal(env.Error.Code, conduiterr.CodeInvalidArgument.Reason())
}

// TestStart_TransitionsStoppedPipelineToRunning is AC-11: a successful call
// starts the pipeline and returns a structured result matching the CLI's
// LifecycleResult fields, with an empty error.
func TestStart_TransitionsStoppedPipelineToRunning(t *testing.T) {
	is := is.New(t)

	fake := &fakeInspectClient{
		pipeline: &apiv1.Pipeline{Id: "orders", State: &apiv1.Pipeline_State{Status: apiv1.Pipeline_STATUS_RUNNING}},
	}
	srv := newServerWithFakeInspectClient(fake, true)
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolStart,
		Arguments: map[string]any{"pipelineId": "orders"},
	})
	is.NoErr(err)
	is.True(!res.IsError)
	is.True(fake.startCalled)
	is.True(fake.closed) // the client is always closed, even on success

	env := decodeStructuredContent[Result[LifecycleResult]](t, res)
	is.True(env.OK)
	is.True(env.Error == nil)
	is.Equal(env.Result.PipelineID, "orders")
	is.Equal(env.Result.Action, "start")
	is.Equal(env.Result.Force, false)
	is.Equal(env.Result.Status, "Running")
}

// TestStop_ForceTrue_CallsRPCWithForce is AC-13: force:true is passed through
// to the StopPipeline call unmodified.
func TestStop_ForceTrue_CallsRPCWithForce(t *testing.T) {
	is := is.New(t)

	fake := &fakeInspectClient{
		pipeline: &apiv1.Pipeline{Id: "orders", State: &apiv1.Pipeline_State{Status: apiv1.Pipeline_STATUS_STOPPED}},
	}
	srv := newServerWithFakeInspectClient(fake, true)
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolStop,
		Arguments: map[string]any{"pipelineId": "orders", "force": true},
	})
	is.NoErr(err)
	is.True(!res.IsError)
	is.True(fake.stopCalled)
	is.True(fake.stopForce)

	env := decodeStructuredContent[Result[LifecycleResult]](t, res)
	is.True(env.OK)
	is.Equal(env.Result.Force, true)
	is.Equal(env.Result.Status, "UserStopped")
}

// TestStop_Graceful_DefaultsForceFalse covers the default (no force key at
// all) path: force=false reaches the RPC and the result omits it from the
// struct's zero value.
func TestStop_Graceful_DefaultsForceFalse(t *testing.T) {
	is := is.New(t)

	fake := &fakeInspectClient{
		pipeline: &apiv1.Pipeline{Id: "orders", State: &apiv1.Pipeline_State{Status: apiv1.Pipeline_STATUS_STOPPED}},
	}
	srv := newServerWithFakeInspectClient(fake, true)
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolStop,
		Arguments: map[string]any{"pipelineId": "orders"},
	})
	is.NoErr(err)
	is.True(!res.IsError)
	is.True(fake.stopCalled)
	is.True(!fake.stopForce)

	env := decodeStructuredContent[Result[LifecycleResult]](t, res)
	is.Equal(env.Result.Force, false)
}

// TestStart_AlreadyRunning_ReturnsCodedErrorSameAsCLI is AC-13: the same
// pipeline.running code the CLI surfaces (see
// cmd/conduit/root/pipelines/start_test.go's equivalent) comes back through
// the MCP envelope, since both go through the same RPC — no divergent logic.
func TestStart_AlreadyRunning_ReturnsCodedErrorSameAsCLI(t *testing.T) {
	is := is.New(t)

	wantErr := conduiterr.Wrap(pipeline.CodePipelineRunning, "can't start pipeline orders: already running", pipeline.ErrPipelineRunning)
	fake := &fakeInspectClient{startErr: wantErr}
	srv := newServerWithFakeInspectClient(fake, true)
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolStart,
		Arguments: map[string]any{"pipelineId": "orders"},
	})
	is.NoErr(err)
	is.True(res.IsError)
	is.True(fake.closed)

	env := decodeStructuredContent[Result[LifecycleResult]](t, res)
	is.True(env.Error != nil)
	is.Equal(env.Error.Code, pipeline.CodePipelineRunning.Reason())
}

// TestStop_NotRunning_ReturnsCodedErrorSameAsCLI is the stop-side twin of the
// above, for pipeline.not_running.
func TestStop_NotRunning_ReturnsCodedErrorSameAsCLI(t *testing.T) {
	is := is.New(t)

	wantErr := conduiterr.Wrap(pipeline.CodePipelineNotRunning, "pipeline orders is not running", pipeline.ErrPipelineNotRunning)
	fake := &fakeInspectClient{stopErr: wantErr}
	srv := newServerWithFakeInspectClient(fake, true)
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolStop,
		Arguments: map[string]any{"pipelineId": "orders"},
	})
	is.NoErr(err)
	is.True(res.IsError)
	is.True(fake.closed)

	env := decodeStructuredContent[Result[LifecycleResult]](t, res)
	is.True(env.Error != nil)
	is.Equal(env.Error.Code, pipeline.CodePipelineNotRunning.Reason())
}

// TestStart_ReadBackFailure_SurfacesAsError: a successful transition whose
// read-back GetPipeline fails must not be reported as a silent success.
func TestStart_ReadBackFailure_SurfacesAsError(t *testing.T) {
	is := is.New(t)

	fake := &fakeInspectClient{err: context.DeadlineExceeded}
	srv := newServerWithFakeInspectClient(fake, true)
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolStart,
		Arguments: map[string]any{"pipelineId": "orders"},
	})
	is.NoErr(err)
	is.True(res.IsError)
	is.True(fake.startCalled)

	env := decodeStructuredContent[Result[LifecycleResult]](t, res)
	is.True(env.Error != nil)
}

// TestPipelineStatusLabel_MCP pins the same mapping the CLI's version does
// (lifecycle_test.go in cmd/conduit/root/pipelines) — kept in sync manually
// since the mcp package can't import the root command package (see
// pipelineStatusLabel's doc).
func TestPipelineStatusLabel_MCP(t *testing.T) {
	is := is.New(t)

	is.Equal(pipelineStatusLabel("start", apiv1.Pipeline_STATUS_RUNNING), "Running")
	is.Equal(pipelineStatusLabel("stop", apiv1.Pipeline_STATUS_STOPPED), "UserStopped")
	is.Equal(pipelineStatusLabel("start", apiv1.Pipeline_STATUS_STOPPED), "Stopped")
	is.Equal(pipelineStatusLabel("start", apiv1.Pipeline_STATUS_DEGRADED), "Degraded")
	is.Equal(pipelineStatusLabel("stop", apiv1.Pipeline_STATUS_RECOVERING), "Recovering")
}
