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

	"github.com/conduitio/conduit/cmd/conduit/internal/display"

	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/conduitio/ecdysis"
)

var (
	_ cecdysis.CommandWithExecuteWithClient = (*DescribeCommand)(nil)
	_ ecdysis.CommandWithAliases            = (*DescribeCommand)(nil)
	_ ecdysis.CommandWithDocs               = (*DescribeCommand)(nil)
	_ ecdysis.CommandWithArgs               = (*DescribeCommand)(nil)
	_ ecdysis.CommandWithOutput             = (*DescribeCommand)(nil)
)

type DescribeArgs struct {
	PipelineID string
}

type DescribeCommand struct {
	args   DescribeArgs
	output ecdysis.Output
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

func (c *DescribeCommand) Output(output ecdysis.Output) {
	c.output = output
}

func (c *DescribeCommand) Aliases() []string { return []string{"desc"} }

func (c *DescribeCommand) Usage() string { return "describe <pipeline-id>" }

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
		return cerrors.Errorf("failed to get pipeline: %w", err)
	}

	// Fetch pipeline processors
	var pipelineProcessors []*apiv1.Processor

	for _, processorID := range pipelineResp.Pipeline.ProcessorIds {
		processor, err := client.ProcessorServiceClient.GetProcessor(ctx, &apiv1.GetProcessorRequest{
			Id: processorID,
		})
		if err != nil {
			return cerrors.Errorf("failed to get processor: %w", err)
		}
		pipelineProcessors = append(pipelineProcessors, processor.Processor)
	}

	// needed to show processors in connectors too
	connectorsResp, err := client.ConnectorServiceClient.ListConnectors(ctx, &apiv1.ListConnectorsRequest{
		PipelineId: c.args.PipelineID,
	})
	if err != nil {
		return cerrors.Errorf("failed to list connectors for pipeline %s: %w", c.args.PipelineID, err)
	}

	connectors := make([]*apiv1.Connector, 0, len(connectorsResp.Connectors))

	for _, conn := range connectorsResp.Connectors {
		connDetails, err := client.ConnectorServiceClient.GetConnector(ctx, &apiv1.GetConnectorRequest{
			Id: conn.Id,
		})
		if err != nil {
			return cerrors.Errorf("failed to get connector: %w", err)
		}

		connectors = append(connectors, connDetails.Connector)
	}

	// store processors for each connector
	connectorProcessors := make(map[string][]*apiv1.Processor, len(connectorsResp.Connectors))

	for _, conn := range connectorsResp.Connectors {
		var processors []*apiv1.Processor

		for _, processorID := range conn.ProcessorIds {
			processor, err := client.ProcessorServiceClient.GetProcessor(ctx, &apiv1.GetProcessorRequest{
				Id: processorID,
			})
			if err != nil {
				return cerrors.Errorf("failed to get processor: %w", err)
			}
			processors = append(processors, processor.Processor)
		}
		connectorProcessors[conn.Id] = processors
	}

	dlq, err := client.PipelineServiceClient.GetDLQ(ctx, &apiv1.GetDLQRequest{
		Id: c.args.PipelineID,
	})
	if err != nil {
		return cerrors.Errorf("failed to fetch DLQ for pipeline %s: %w", c.args.PipelineID, err)
	}

	err = displayPipeline(c.output, pipelineResp.Pipeline, pipelineProcessors, connectors, connectorProcessors, dlq.Dlq)
	if err != nil {
		return cerrors.Errorf("failed to display pipeline %s: %w", c.args.PipelineID, err)
	}

	return nil
}

func displayPipeline(out ecdysis.Output, pipeline *apiv1.Pipeline, pipelineProcessors []*apiv1.Processor, connectors []*apiv1.Connector, connectorProcessors map[string][]*apiv1.Processor, dlq *apiv1.Pipeline_DLQ) error {
	// ID
	out.Stdout(fmt.Sprintf("ID: %s\n", pipeline.Id))

	// State
	if pipeline.State != nil {
		out.Stdout(fmt.Sprintf("Status: %s\n", display.PrintStatusFromProtoString(pipeline.State.Status.String())))
		if pipeline.State.Error != "" {
			out.Stdout(fmt.Sprintf("Error: %s\n", pipeline.State.Error))
		}
	}

	// Config
	if pipeline.Config != nil {
		out.Stdout(fmt.Sprintf("Name: %s\n", pipeline.Config.Name))
		// no new line after description, as it's always added
		// when parsed from the YAML config file
		out.Stdout(fmt.Sprintf("Description: %s\n", pipeline.Config.Description))
	}

	// Connectors
	out.Stdout("Sources:\n")
	displayConnectorsAndProcessors(out, connectors, connectorProcessors, apiv1.Connector_TYPE_SOURCE)

	display.DisplayProcessors(out, pipelineProcessors, 0)

	out.Stdout("Destinations:\n")
	displayConnectorsAndProcessors(out, connectors, connectorProcessors, apiv1.Connector_TYPE_DESTINATION)

	displayDLQ(out, dlq)

	// Timestamps
	if pipeline.CreatedAt != nil {
		out.Stdout(fmt.Sprintf("Created At: %s\n", display.PrintTime(pipeline.CreatedAt)))
	}
	if pipeline.UpdatedAt != nil {
		out.Stdout(fmt.Sprintf("Updated At: %s\n", display.PrintTime(pipeline.UpdatedAt)))
	}

	return nil
}

func displayDLQ(out ecdysis.Output, dlq *apiv1.Pipeline_DLQ) {
	out.Stdout("Dead-letter queue:\n")
	out.Stdout(fmt.Sprintf("%sPlugin: %s\n", display.Indentation(1), dlq.Plugin))
}

func displayConnectorsAndProcessors(out ecdysis.Output, connectors []*apiv1.Connector, connectorProcessors map[string][]*apiv1.Processor, connType apiv1.Connector_Type) {
	for _, conn := range connectors {
		if conn.Type == connType {
			out.Stdout(fmt.Sprintf("%s- ID: %s\n", display.Indentation(1), conn.Id))
			out.Stdout(fmt.Sprintf("%sPlugin: %s\n", display.Indentation(2), conn.Plugin))
			out.Stdout(fmt.Sprintf("%sPipeline ID: %s\n", display.Indentation(2), conn.PipelineId))
			display.DisplayConnectorConfig(out, conn.Config, 2)
			if processors, ok := connectorProcessors[conn.Id]; ok {
				display.DisplayProcessors(out, processors, 2)
			}
			out.Stdout(fmt.Sprintf("%sCreated At: %s\n", display.Indentation(2), display.PrintTime(conn.CreatedAt)))
			out.Stdout(fmt.Sprintf("%sUpdated At: %s\n", display.Indentation(2), display.PrintTime(conn.UpdatedAt)))
		}
	}
}
