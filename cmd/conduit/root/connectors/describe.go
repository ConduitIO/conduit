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
	ConnectorID string
}

type DescribeCommand struct {
	args DescribeArgs
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

	displayConnector(resp.Connector, processors)

	return nil
}

func displayConnector(connector *apiv1.Connector, processors []*apiv1.Processor) {
	if connector == nil {
		return
	}

	fmt.Printf("ID: %s\n", connector.Id)

	var connectorType string

	switch connector.Type {
	case apiv1.Connector_TYPE_SOURCE:
		connectorType = "Source"
	case apiv1.Connector_TYPE_DESTINATION:
		connectorType = "Destination"
	case apiv1.Connector_TYPE_UNSPECIFIED:
		connectorType = "Unspecified"
	default:
		connectorType = "Unknown"
	}

	fmt.Printf("Type: %s\n", connectorType)
	fmt.Printf("Plugin: %s\n", connector.Plugin)
	fmt.Printf("Pipeline ID: %s\n", connector.PipelineId)

	displayConnectorConfig(connector.Config)
	fmt.Printf("Created At: %s\n", internal.PrintTime(connector.CreatedAt))
	fmt.Printf("Updated At: %s\n", internal.PrintTime(connector.UpdatedAt))

	internal.DisplayProcessors(processors, 0)
}

func displayConnectorConfig(cfg *apiv1.Connector_Config) {
	fmt.Println("Config:")

	for name, value := range cfg.Settings {
		fmt.Printf("%s%s: %s\n", internal.Indentation(1), name, value)
	}
}
