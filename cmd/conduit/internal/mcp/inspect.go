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
	"fmt"

	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc/status"
)

// inspectClient is the narrow surface the inspect/start/stop tools need from
// a running Conduit's gRPC API — the same calls
// cmd/conduit/root/pipelines/{inspect,start,stop}.go make (GetPipeline,
// ListConnectors+GetConnector per connector, GetDLQ, StartPipeline,
// StopPipeline). Declaring it here (rather than depending on *api.Client
// concretely) lets every tool built on this seam be unit-tested against a
// fake, without a live gRPC server.
//
// StartPipeline/StopPipeline were added alongside the start/stop tools
// (design doc 20260712-cli-pipeline-lifecycle-verbs.md §2: "the MCP server's
// existing API client seam ... extended with the two lifecycle methods") —
// the name predates them but the seam is now shared by every tool that needs
// a live server, not just inspect.
type inspectClient interface {
	GetPipeline(ctx context.Context, id string) (*apiv1.Pipeline, error)
	ListConnectors(ctx context.Context, pipelineID string) ([]*apiv1.Connector, error)
	GetDLQ(ctx context.Context, id string) (*apiv1.Pipeline_DLQ, error)
	StartPipeline(ctx context.Context, id string) error
	StopPipeline(ctx context.Context, id string, force bool) error
	Close() error
}

// apiInspectClient adapts *api.Client (the real gRPC client the CLI's
// inspect command dials) to inspectClient.
type apiInspectClient struct{ c *api.Client }

// newAPIInspectClient is Config.newAPIClient's production default.
func newAPIInspectClient(ctx context.Context, address string) (inspectClient, error) {
	c, err := api.NewClient(ctx, address)
	if err != nil {
		return nil, err
	}
	return &apiInspectClient{c: c}, nil
}

func (a *apiInspectClient) GetPipeline(ctx context.Context, id string) (*apiv1.Pipeline, error) {
	resp, err := a.c.PipelineServiceClient.GetPipeline(ctx, &apiv1.GetPipelineRequest{Id: id})
	if err != nil {
		return nil, err
	}
	return resp.Pipeline, nil
}

func (a *apiInspectClient) ListConnectors(ctx context.Context, pipelineID string) ([]*apiv1.Connector, error) {
	listResp, err := a.c.ConnectorServiceClient.ListConnectors(ctx, &apiv1.ListConnectorsRequest{PipelineId: pipelineID})
	if err != nil {
		return nil, err
	}
	connectors := make([]*apiv1.Connector, 0, len(listResp.Connectors))
	for _, conn := range listResp.Connectors {
		detail, err := a.c.ConnectorServiceClient.GetConnector(ctx, &apiv1.GetConnectorRequest{Id: conn.Id})
		if err != nil {
			return nil, err
		}
		connectors = append(connectors, detail.Connector)
	}
	return connectors, nil
}

func (a *apiInspectClient) GetDLQ(ctx context.Context, id string) (*apiv1.Pipeline_DLQ, error) {
	resp, err := a.c.PipelineServiceClient.GetDLQ(ctx, &apiv1.GetDLQRequest{Id: id})
	if err != nil {
		return nil, err
	}
	return resp.Dlq, nil
}

func (a *apiInspectClient) StartPipeline(ctx context.Context, id string) error {
	_, err := a.c.PipelineServiceClient.StartPipeline(ctx, &apiv1.StartPipelineRequest{Id: id})
	return err
}

func (a *apiInspectClient) StopPipeline(ctx context.Context, id string, force bool) error {
	_, err := a.c.PipelineServiceClient.StopPipeline(ctx, &apiv1.StopPipelineRequest{Id: id, Force: force})
	return err
}

func (a *apiInspectClient) Close() error { return a.c.Close() }

// apiAddressSuggestion is the remediation every tool on the inspectClient
// seam (inspect, start, stop) attaches when Config.APIAddress is unset — kept
// as one string so the three call sites can't drift.
const apiAddressSuggestion = "restart conduit mcp with --api-address <host:port> pointing at a running `conduit run` instance"

// requireAPIAddress returns a common.unavailable error when apiAddress is
// empty — the shared "no live server configured" failure mode for every tool
// built on the inspectClient seam. tool names the calling tool in the
// message only (e.g. "inspect", "start", "stop"). Returns nil when
// apiAddress is set, i.e. "no error, proceed".
func requireAPIAddress(apiAddress, tool string) error {
	if apiAddress != "" {
		return nil
	}
	ce := conduiterr.New(conduiterr.CodeUnavailable,
		fmt.Sprintf("conduit mcp was not started with --api-address; %s requires the gRPC address of a running Conduit", tool))
	ce.Suggestion = apiAddressSuggestion
	return ce
}

// InspectArgs is the inspect tool's input.
type InspectArgs struct {
	PipelineID string `json:"pipelineId" jsonschema:"the ID of a pipeline registered in a running Conduit (see the deploy tool's result, or the CLI's pipelines list command)"`
}

// InspectResult mirrors cmd/conduit/root/pipelines.InspectResult's fields —
// the pipeline (with its live state/status), its connectors, and its DLQ.
type InspectResult struct {
	Pipeline   *apiv1.Pipeline     `json:"pipeline"`
	Connectors []*apiv1.Connector  `json:"connectors"`
	Dlq        *apiv1.Pipeline_DLQ `json:"dlq"`
}

// inspect implements the inspect tool: the online, status-forward
// operational view of a running pipeline. Unlike validate/lint/dry_run/
// deploy it dials the API (Config.APIAddress) — it needs a running Conduit,
// as its CLI peer (`conduit pipelines inspect`) does. It is a read tool: it
// only ever calls Get*/List* RPCs, never a mutating one.
func (s *server) inspect(ctx context.Context, _ *sdkmcp.CallToolRequest, in InspectArgs) (*sdkmcp.CallToolResult, Result[InspectResult], error) {
	if in.PipelineID == "" {
		ce := conduiterr.New(conduiterr.CodeInvalidArgument, "pipelineId is required")
		return toolErr[InspectResult](ce)
	}
	if ce := requireAPIAddress(s.cfg.APIAddress, "inspect"); ce != nil {
		return toolErr[InspectResult](ce)
	}

	client, err := s.cfg.newAPIClient(ctx, s.cfg.APIAddress)
	if err != nil {
		return toolErr[InspectResult](mapGRPCErr(err))
	}
	defer client.Close() // best-effort cleanup; nothing left to report to if this fails

	pipeline, err := client.GetPipeline(ctx, in.PipelineID)
	if err != nil {
		return toolErr[InspectResult](mapGRPCErr(fmt.Errorf("failed to get pipeline %q: %w", in.PipelineID, err)))
	}
	connectors, err := client.ListConnectors(ctx, in.PipelineID)
	if err != nil {
		return toolErr[InspectResult](mapGRPCErr(fmt.Errorf("failed to list connectors for pipeline %q: %w", in.PipelineID, err)))
	}
	dlq, err := client.GetDLQ(ctx, in.PipelineID)
	if err != nil {
		return toolErr[InspectResult](mapGRPCErr(fmt.Errorf("failed to fetch DLQ for pipeline %q: %w", in.PipelineID, err)))
	}

	return toolOK(true, InspectResult{Pipeline: pipeline, Connectors: connectors, Dlq: dlq}, nil)
}

// mapGRPCErr converts a gRPC error (or a wrapped one) into a
// *conduiterr.ConduitError via the same wire mapping the API boundary itself
// uses (conduiterr.FromStatus) — so an inspect failure carries the server's
// own code/suggestion when the server sent one (Conduit's API errors already
// round-trip an ErrorInfo detail), rather than a bare, uncoded message.
func mapGRPCErr(err error) error {
	if err == nil {
		return nil
	}
	if st, ok := status.FromError(err); ok {
		return conduiterr.FromStatus(st)
	}
	return conduiterr.Wrap(conduiterr.CodeUnknown, err.Error(), err)
}
