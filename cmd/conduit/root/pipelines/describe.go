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
	"context"
	"fmt"
	"strings"

	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/conduitio/ecdysis"
)

var (
	_ cecdysis.CommandWithExecuteWithClient = (*DescribeCommand)(nil)
	_ ecdysis.CommandWithAliases            = (*DescribeCommand)(nil)
	_ ecdysis.CommandWithDocs               = (*DescribeCommand)(nil)
	_ ecdysis.CommandWithArgs               = (*DescribeCommand)(nil)
)

type DescribeArgs struct {
	PipelineID string
}

type DescribeCommand struct {
	args DescribeArgs
}

func (c *DescribeCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Describe an existing pipeline",
		Long: `This command requires Conduit to be already running since it will describe a pipeline registered 
by Conduit. You can list existing pipelines with the 'conduit pipelines list' command.`,
		Example: "conduit pipelines describe pipeline-with-dlq\n" +
			"conduit pipelines desc multiple-source-with-processor",
	}
}

func (c *DescribeCommand) Aliases() []string { return []string{"desc"} }

func (c *DescribeCommand) Usage() string { return "describe" }

func (c *DescribeCommand) Args(args []string) error {
	if len(args) == 0 {
		return cerrors.Errorf("requires a pipeline ID")
	}

	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
	}

	c.args.PipelineID = args[0]
	return nil
}

func (c *DescribeCommand) ExecuteWithClient(ctx context.Context, client *api.Client) error {
	pipelineResp, err := client.PipelineServiceClient.GetPipeline(ctx, &apiv1.GetPipelineRequest{
		Id: c.args.PipelineID,
	})
	if err != nil {
		return fmt.Errorf("failed to get pipeline: %w", err)
	}

	// needed to show processors in connectors too
	connectorsResp, err := client.ConnectorServiceClient.ListConnectors(ctx, &apiv1.ListConnectorsRequest{
		PipelineId: c.args.PipelineID,
	})
	if err != nil {
		return fmt.Errorf("failed to list connectors for pipeline %s: %w", c.args.PipelineID, err)
	}

	dlq, err := client.PipelineServiceClient.GetDLQ(ctx, &apiv1.GetDLQRequest{
		Id: c.args.PipelineID,
	})
	if err != nil {
		return fmt.Errorf("failed to fetch DLQ for pipeline %s: %w", c.args.PipelineID, err)
	}

	err = displayPipeline(ctx, pipelineResp.Pipeline, connectorsResp.Connectors, dlq.Dlq)
	if err != nil {
		return fmt.Errorf("failed to display pipeline %s: %w", c.args.PipelineID, err)
	}

	return nil
}

func displayPipeline(ctx context.Context, pipeline *apiv1.Pipeline, connectors []*apiv1.Connector, dlq *apiv1.Pipeline_DLQ) error {
	cobraCmd := ecdysis.CobraCmdFromContext(ctx)
	w := cobraCmd.OutOrStdout()
	var b strings.Builder

	// ID
	fmt.Fprintf(&b, "ID: %s\n", pipeline.Id)

	// State
	if pipeline.State != nil {
		fmt.Fprintf(&b, "Status: %s\n", internal.PrintStatusFromProtoString(pipeline.State.Status.String()))
		if pipeline.State.Error != "" {
			fmt.Fprintf(&b, "Error: %s\n", pipeline.State.Error)
		}
	}

	// Config
	if pipeline.Config != nil {
		fmt.Fprintf(&b, "Name: %s\n", pipeline.Config.Name)
		// no new line after description, as it's always added
		// when parsed from the YAML config file
		fmt.Fprintf(&b, "Description: %s\n", pipeline.Config.Description)
	}

	// Connectors
	b.WriteString("Sources:\n")
	printConnectors(&b, connectors, apiv1.Connector_TYPE_SOURCE)

	printProcessors(&b, pipeline.ProcessorIds, 0)

	b.WriteString("Destinations:\n")
	printConnectors(&b, connectors, apiv1.Connector_TYPE_DESTINATION)

	printDLQ(&b, dlq)

	// Timestamps
	if pipeline.CreatedAt != nil {
		fmt.Fprintf(&b, "Created At: %s\n", internal.PrintTime(pipeline.CreatedAt))
	}
	if pipeline.UpdatedAt != nil {
		fmt.Fprintf(&b, "Updated At: %s\n", internal.PrintTime(pipeline.UpdatedAt))
	}

	// Write the complete string to the writer
	_, err := w.Write([]byte(b.String()))
	if err != nil {
		return fmt.Errorf("writing output: %w", err)
	}

	return nil
}

func printDLQ(b *strings.Builder, dlq *apiv1.Pipeline_DLQ) {
	b.WriteString("Dead-letter queue:\n")
	fmt.Fprintf(b, "%sPlugin: %s\n", internal.Indentation(1), dlq.Plugin)
}

func printConnectors(b *strings.Builder, connectors []*apiv1.Connector, connType apiv1.Connector_Type) {
	for _, conn := range connectors {
		if conn.Type == connType {
			fmt.Fprintf(b, "%s- %s (%s)\n", internal.Indentation(1), conn.Id, conn.Plugin)
			printProcessors(b, conn.ProcessorIds, 2)
		}
	}
}

func printProcessors(b *strings.Builder, ids []string, indent int) {
	if len(ids) == 0 {
		return
	}

	fmt.Fprintf(b, "%sProcessors:\n", internal.Indentation(indent))
	for _, id := range ids {
		fmt.Fprintf(b, "%s- %s\n", internal.Indentation(indent+1), id)
	}
}
