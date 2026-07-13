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
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/matryer/is"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeInspectClient is an inspectClient test double: canned
// responses/errors, no gRPC dial. Shared by the inspect and start/stop tool
// tests, since they all go through the same client seam (inspect.go's doc).
type fakeInspectClient struct {
	pipeline   *apiv1.Pipeline
	connectors []*apiv1.Connector
	dlq        *apiv1.Pipeline_DLQ
	err        error
	closed     bool

	// startErr/stopErr let start/stop tool tests exercise a transition
	// failure independently of err (which also drives GetPipeline/
	// ListConnectors/GetDLQ) — e.g. a successful StopPipeline whose read-back
	// GetPipeline then fails.
	startErr error
	stopErr  error

	startCalled bool
	stopCalled  bool
	stopForce   bool
}

func (f *fakeInspectClient) GetPipeline(context.Context, string) (*apiv1.Pipeline, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.pipeline, nil
}

func (f *fakeInspectClient) ListConnectors(context.Context, string) ([]*apiv1.Connector, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.connectors, nil
}

func (f *fakeInspectClient) GetDLQ(context.Context, string) (*apiv1.Pipeline_DLQ, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.dlq, nil
}

func (f *fakeInspectClient) StartPipeline(context.Context, string) error {
	f.startCalled = true
	return f.startErr
}

func (f *fakeInspectClient) StopPipeline(_ context.Context, _ string, force bool) error {
	f.stopCalled = true
	f.stopForce = force
	return f.stopErr
}

func (f *fakeInspectClient) Close() error {
	f.closed = true
	return nil
}

// TestInspect_NoAPIAddress_RefusesWithSuggestion covers the "no server
// configured" failure mode without needing a real gRPC dial: the tool must
// never hang or panic when Config.APIAddress is unset, and the resulting
// error must carry a code + suggestion (AC-6).
func TestInspect_NoAPIAddress_RefusesWithSuggestion(t *testing.T) {
	is := is.New(t)

	srv := NewServer(Config{}) // APIAddress left empty
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolInspect,
		Arguments: map[string]any{"pipelineId": "orders"},
	})
	is.NoErr(err)
	is.True(res.IsError)

	env := decodeStructuredContent[Result[InspectResult]](t, res)
	is.True(env.Error != nil)
	is.Equal(env.Error.Code, conduiterr.CodeUnavailable.Reason())
	is.True(env.Error.Suggestion != "")
}

// TestInspect_ReturnsPipelineConnectorsAndDLQ is AC-1's inspect case: a
// successful call returns the pipeline/connectors/DLQ, and the fake client's
// Close is always invoked.
func TestInspect_ReturnsPipelineConnectorsAndDLQ(t *testing.T) {
	is := is.New(t)

	fake := &fakeInspectClient{
		pipeline:   &apiv1.Pipeline{Id: "orders"},
		connectors: []*apiv1.Connector{{Id: "orders:src", Type: apiv1.Connector_TYPE_SOURCE}},
		dlq:        &apiv1.Pipeline_DLQ{Plugin: "builtin:log"},
	}
	srv := NewServer(Config{
		APIAddress: "localhost:0",
		newAPIClient: func(context.Context, string) (inspectClient, error) {
			return fake, nil
		},
	})
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolInspect,
		Arguments: map[string]any{"pipelineId": "orders"},
	})
	is.NoErr(err)
	is.True(!res.IsError)
	is.True(fake.closed) // the client is always closed, even on success

	env := decodeStructuredContent[Result[InspectResult]](t, res)
	is.True(env.OK)
	is.Equal(env.Result.Pipeline.Id, "orders")
	is.Equal(len(env.Result.Connectors), 1)
	is.Equal(env.Result.Dlq.Plugin, "builtin:log")
}

// TestInspect_ClientError_ClosesClientAndMapsError verifies the fake client
// is still closed on a failed call, and the resulting error is coded.
func TestInspect_ClientError_ClosesClientAndMapsError(t *testing.T) {
	is := is.New(t)

	fake := &fakeInspectClient{err: context.DeadlineExceeded}
	srv := NewServer(Config{
		APIAddress: "localhost:0",
		newAPIClient: func(context.Context, string) (inspectClient, error) {
			return fake, nil
		},
	})
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolInspect,
		Arguments: map[string]any{"pipelineId": "orders"},
	})
	is.NoErr(err)
	is.True(res.IsError)
	is.True(fake.closed)

	env := decodeStructuredContent[Result[InspectResult]](t, res)
	is.True(env.Error != nil)
	is.True(env.Error.Code != "")
}
