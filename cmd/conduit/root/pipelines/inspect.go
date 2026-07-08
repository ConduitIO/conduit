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
	"bytes"
	"context"
	"fmt"

	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal/display"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/conduitio/ecdysis"
	json "github.com/goccy/go-json"
)

var (
	_ cecdysis.CommandWithExecuteWithClientResult = (*InspectCommand)(nil)
	_ ecdysis.CommandWithDocs                     = (*InspectCommand)(nil)
	_ ecdysis.CommandWithArgs                     = (*InspectCommand)(nil)
)

type InspectArgs struct {
	PipelineID string
}

// InspectCommand implements `conduit pipelines inspect PIPELINE_ID`: the online,
// status-forward operational view of a running pipeline — the CLI peer of MCP
// inspect_pipeline. Unlike the offline validate/lint/dry-run verbs it dials the
// API and reports live state (the current status, per-stage summary, DLQ). See
// docs/design-documents/20260708-cli-pipeline-inspect-lint-dryrun.md.
//
// A bounded in-flight record sample (--records N over the InspectConnector
// stream) is a tracked follow-up; this v1 delivers the live status + per-stage
// operational view.
type InspectCommand struct {
	args InspectArgs
}

// InspectResult is the inspect view: the pipeline (with its live State/status)
// plus its connectors and DLQ. Like DescribeResult it marshals its proto parts
// via protojson so --json matches `pipelines list`/`describe` (enum names,
// RFC3339 timestamps).
type InspectResult struct {
	Pipeline   *apiv1.Pipeline     `json:"pipeline"`
	Connectors []*apiv1.Connector  `json:"connectors"`
	Dlq        *apiv1.Pipeline_DLQ `json:"dlq"`
}

func (r *InspectResult) MarshalJSON() ([]byte, error) {
	pipeline, err := cecdysis.ProtoJSON(r.Pipeline)
	if err != nil {
		return nil, err
	}
	connectors, err := cecdysis.ProtoJSONSlice(r.Connectors)
	if err != nil {
		return nil, err
	}
	dlq := json.RawMessage("null")
	if r.Dlq != nil {
		if dlq, err = cecdysis.ProtoJSON(r.Dlq); err != nil {
			return nil, err
		}
	}
	out, err := json.Marshal(struct {
		Pipeline   json.RawMessage   `json:"pipeline"`
		Connectors []json.RawMessage `json:"connectors"`
		Dlq        json.RawMessage   `json:"dlq"`
	}{pipeline, connectors, dlq})
	if err != nil {
		return nil, cerrors.Errorf("marshal pipeline inspect result: %w", err)
	}
	return out, nil
}

func (c *InspectCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Inspect a running pipeline's live status and per-stage state",
		Long: `Reports the live operational state of a pipeline registered in a running Conduit: its current
status (running, stopped, degraded, recovering), any error, and a per-stage summary of its sources,
destinations, and dead-letter queue. Unlike 'validate'/'lint'/'dry-run' this command requires a
running Conduit and dials its API.

Use 'conduit pipelines list' to see pipeline IDs, and 'conduit pipelines describe' for the full
static configuration detail.`,
		Example: "conduit pipelines inspect my-pipeline\n" +
			"conduit pipelines inspect my-pipeline --json",
	}
}

func (c *InspectCommand) Usage() string { return "inspect PIPELINE_ID" }

func (c *InspectCommand) Args(args []string) error {
	if len(args) == 0 {
		return cerrors.Errorf("requires a pipeline ID")
	}
	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
	}
	c.args.PipelineID = args[0]
	return nil
}

func (c *InspectCommand) ExecuteWithClientResult(ctx context.Context, client *api.Client) (any, error) {
	pipelineResp, err := client.PipelineServiceClient.GetPipeline(ctx, &apiv1.GetPipelineRequest{
		Id: c.args.PipelineID,
	})
	if err != nil {
		// Surfaces as the client-result decorator's exit code: a missing
		// pipeline (gRPC NotFound) -> validation/2, an unreachable server
		// (Unavailable) -> environment/3.
		return nil, cerrors.Errorf("failed to get pipeline %q: %w", c.args.PipelineID, err)
	}

	connectorsResp, err := client.ConnectorServiceClient.ListConnectors(ctx, &apiv1.ListConnectorsRequest{
		PipelineId: c.args.PipelineID,
	})
	if err != nil {
		return nil, cerrors.Errorf("failed to list connectors for pipeline %q: %w", c.args.PipelineID, err)
	}
	connectors := make([]*apiv1.Connector, 0, len(connectorsResp.Connectors))
	for _, conn := range connectorsResp.Connectors {
		connDetails, err := client.ConnectorServiceClient.GetConnector(ctx, &apiv1.GetConnectorRequest{Id: conn.Id})
		if err != nil {
			return nil, cerrors.Errorf("failed to get connector %q: %w", conn.Id, err)
		}
		connectors = append(connectors, connDetails.Connector)
	}

	dlq, err := client.PipelineServiceClient.GetDLQ(ctx, &apiv1.GetDLQRequest{Id: c.args.PipelineID})
	if err != nil {
		return nil, cerrors.Errorf("failed to fetch DLQ for pipeline %q: %w", c.args.PipelineID, err)
	}

	return &InspectResult{
		Pipeline:   pipelineResp.Pipeline,
		Connectors: connectors,
		Dlq:        dlq.Dlq,
	}, nil
}

func (c *InspectCommand) Render(result any) string {
	res, ok := result.(*InspectResult)
	if !ok {
		return ""
	}

	buf := new(bytes.Buffer)
	p := res.Pipeline

	status := "unknown"
	if p.State != nil {
		status = display.PrintStatusFromProtoString(p.State.Status.String())
	}
	fmt.Fprintf(buf, "Pipeline %s  [%s]\n", p.Id, status)
	if p.Config != nil && p.Config.Name != "" {
		fmt.Fprintf(buf, "  name: %s\n", p.Config.Name)
	}
	if p.State != nil && p.State.Error != "" {
		fmt.Fprintf(buf, "  error: %s\n", p.State.Error)
	}

	writeStage(buf, "source", res.Connectors, apiv1.Connector_TYPE_SOURCE)
	writeStage(buf, "destination", res.Connectors, apiv1.Connector_TYPE_DESTINATION)

	dlqPlugin := "(none)"
	if res.Dlq != nil && res.Dlq.Plugin != "" {
		dlqPlugin = res.Dlq.Plugin
	}
	fmt.Fprintf(buf, "  %-12s %s\n", "DLQ", dlqPlugin)

	return buf.String()
}

func writeStage(buf *bytes.Buffer, role string, connectors []*apiv1.Connector, t apiv1.Connector_Type) {
	for _, conn := range connectors {
		if conn.Type == t {
			fmt.Fprintf(buf, "  %-12s %-24s %s\n", role, conn.Id, conn.Plugin)
		}
	}
}
