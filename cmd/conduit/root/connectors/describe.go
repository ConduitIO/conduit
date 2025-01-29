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

package connectors

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
	ConnectorID string
}

type DescribeCommand struct {
	args   DescribeArgs
	output ecdysis.Output
}

func (c *DescribeCommand) Output(output ecdysis.Output) {
	c.output = output
}

func (c *DescribeCommand) Usage() string { return "describe" }

func (c *DescribeCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Describe an existing connector",
		Long: `This command requires Conduit to be already running since it will describe a connector registered
by Conduit. You can list existing connectors with the 'conduit connectors list' command.`,
		Example: "conduit connectors describe connector:source\n" +
			"conduit connectors desc connector:destination",
	}
}

func (c *DescribeCommand) Aliases() []string { return []string{"desc"} }

func (c *DescribeCommand) Args(args []string) error {
	if len(args) == 0 {
		return cerrors.Errorf("requires a connector ID")
	}

	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
	}

	c.args.ConnectorID = args[0]
	return nil
}

func (c *DescribeCommand) ExecuteWithClient(ctx context.Context, client *api.Client) error {
	resp, err := client.ConnectorServiceClient.GetConnector(ctx, &apiv1.GetConnectorRequest{
		Id: c.args.ConnectorID,
	})
	if err != nil {
		return fmt.Errorf("failed to get connector: %w", err)
	}

	var processors []*apiv1.Processor

	for _, processorID := range resp.Connector.ProcessorIds {
		processor, err := client.ProcessorServiceClient.GetProcessor(ctx, &apiv1.GetProcessorRequest{
			Id: processorID,
		})
		if err != nil {
			return fmt.Errorf("failed to get processor: %w", err)
		}
		processors = append(processors, processor.Processor)
	}

	displayConnector(c.output, resp.Connector, processors)

	return nil
}

func displayConnector(out ecdysis.Output, connector *apiv1.Connector, processors []*apiv1.Processor) {
	if connector == nil {
		return
	}

	out.Stdout(fmt.Sprintf("ID: %s\n", connector.Id))

	out.Stdout(fmt.Sprintf("Type: %s\n", display.ConnectorTypeToString(connector.Type)))
	out.Stdout(fmt.Sprintf("Plugin: %s\n", connector.Plugin))
	out.Stdout(fmt.Sprintf("Pipeline ID: %s\n", connector.PipelineId))

	display.DisplayConnectorConfig(out, connector.Config, 0)
	out.Stdout(fmt.Sprintf("Created At: %s\n", display.PrintTime(connector.CreatedAt)))
	out.Stdout(fmt.Sprintf("Updated At: %s\n", display.PrintTime(connector.UpdatedAt)))

	display.DisplayProcessors(out, processors, 0)
}
