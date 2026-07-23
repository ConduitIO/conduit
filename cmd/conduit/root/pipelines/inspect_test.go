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
	"github.com/conduitio/conduit/cmd/conduit/internal/display"
	"github.com/conduitio/conduit/cmd/conduit/internal/testutils"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
)

func TestInspectExecutionArgs(t *testing.T) {
	is := is.New(t)

	is.Equal((&InspectCommand{}).Args([]string{}).Error(), "requires a pipeline ID")
	is.Equal((&InspectCommand{}).Args([]string{"a", "b"}).Error(), "too many arguments")

	c := &InspectCommand{}
	is.NoErr(c.Args([]string{"my-pipeline"}))
	is.Equal(c.args.PipelineID, "my-pipeline")
}

// AC-8: inspect of a running pipeline reports its status and per-stage summary.
func TestInspectCommandExecuteWithClient(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	cmd := &InspectCommand{args: InspectArgs{PipelineID: "1"}}

	mockPipeline := mock.NewMockPipelineService(ctrl)
	mockConnector := mock.NewMockConnectorService(ctrl)

	testutils.MockGetPipeline(mockPipeline, "1", []string{"conn1", "conn2"}, nil)
	testutils.MockGetConnectors(mockConnector, "1", []*apiv1.Connector{
		{Id: "conn1", Type: apiv1.Connector_TYPE_SOURCE, Plugin: "builtin:generator"},
		{Id: "conn2", Type: apiv1.Connector_TYPE_DESTINATION, Plugin: "builtin:log"},
	})
	testutils.MockGetConnector(mockConnector, "conn1", "builtin:generator", "1", apiv1.Connector_TYPE_SOURCE, &apiv1.Connector_Config{}, nil)
	testutils.MockGetConnector(mockConnector, "conn2", "builtin:log", "1", apiv1.Connector_TYPE_DESTINATION, &apiv1.Connector_Config{}, nil)
	testutils.MockGetDLQ(mockPipeline, "1", "builtin:log")

	client := &api.Client{
		PipelineServiceClient:  mockPipeline,
		ConnectorServiceClient: mockConnector,
	}

	result, err := cmd.ExecuteWithClientResult(ctx, client)
	is.NoErr(err)

	// Family B negative check (v0.19 workstream 8 — cli-contract.md §4.6):
	// this command's --json output must never accidentally start
	// conforming to Family A's envelope shape.
	b, err := cecdysis.MarshalJSON(result)
	is.NoErr(err)
	is.True(!testutils.MatchesEnvelope(b))

	out := cmd.Render(result)
	is.True(strings.Contains(out, "Pipeline 1  [running]")) // status-forward header
	is.True(strings.Contains(out, "source"))                // per-stage rows
	is.True(strings.Contains(out, "conn1"))
	is.True(strings.Contains(out, "destination"))
	is.True(strings.Contains(out, "conn2"))
	is.True(strings.Contains(out, "DLQ"))
	is.True(strings.Contains(out, "builtin:log"))
}

// AC-9: a missing pipeline (GetPipeline errors) surfaces as an error, which the
// client-result decorator classifies for the exit code — never a panic.
func TestInspectCommandExecuteWithClient_MissingPipeline(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	cmd := &InspectCommand{args: InspectArgs{PipelineID: "nope"}}
	mockPipeline := mock.NewMockPipelineService(ctrl)
	mockPipeline.EXPECT().GetPipeline(gomock.Any(), &apiv1.GetPipelineRequest{Id: "nope"}).
		Return(nil, cerrors.New("pipeline not found")).Times(1)

	client := &api.Client{PipelineServiceClient: mockPipeline}
	_, err := cmd.ExecuteWithClientResult(ctx, client)
	is.True(err != nil)
}

// AC-12: the pipeline status enum renders a distinct, non-empty label for every
// proto status value (running/stopped/degraded/recovering), the pinned
// status-display AC. inspect reads live state through the API, so it renders the
// proto enum (which collapses the internal system/user-stopped into STOPPED).
func TestInspect_StatusEnum_AllValues(t *testing.T) {
	is := is.New(t)
	seen := map[string]bool{}
	for _, s := range []apiv1.Pipeline_Status{
		apiv1.Pipeline_STATUS_RUNNING,
		apiv1.Pipeline_STATUS_STOPPED,
		apiv1.Pipeline_STATUS_DEGRADED,
		apiv1.Pipeline_STATUS_RECOVERING,
	} {
		label := display.PrintStatusFromProtoString(s.String())
		is.True(label != "")  // every status renders
		is.True(!seen[label]) // ...to a distinct label
		seen[label] = true
	}
}
