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
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/conduitio/ecdysis"
)

var (
	_ cecdysis.CommandWithExecuteWithClient = (*DescribeCommand)(nil)
	_ ecdysis.CommandWithAliases            = (*DescribeCommand)(nil)
	_ ecdysis.CommandWithDocs               = (*DescribeCommand)(nil)
)

type DescribeCommand struct {
	PipelineID string
}

func (c *DescribeCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Describe an existing ",
		Long: `This command requires Conduit to be already running since it will list all pipelines registered 
by Conduit. This will depend on the configured pipelines directory, which by default is /pipelines; however, it could 
be configured via --pipelines.path at the time of running Conduit.`,
		Example: "conduit pipelines describe",
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

	c.PipelineID = args[0]
	return nil
}

func (c *DescribeCommand) ExecuteWithClient(ctx context.Context, client *api.Client) error {
	pipelineResp, err := client.PipelineServiceClient.GetPipeline(
		ctx,
		&apiv1.GetPipelineRequest{Id: c.PipelineID},
	)
	if err != nil {
		return fmt.Errorf("failed to list pipelines: %w", err)
	}

	// needed to show processors in connectors too
	connectorsResp, err := client.ConnectorServiceClient.ListConnectors(
		ctx,
		&apiv1.ListConnectorsRequest{
			PipelineId: c.PipelineID,
		},
	)
	if err != nil {
		return fmt.Errorf("failed to list connectors for pipeline %s: %w", c.PipelineID, err)
	}

	err = displayPipeline(ctx, pipelineResp.Pipeline, connectorsResp.Connectors)
	if err != nil {
		return fmt.Errorf("failed to display pipeline %s: %w", c.PipelineID, err)
	}

	return nil
}

func displayPipeline(ctx context.Context, pipeline *apiv1.Pipeline, connectors []*apiv1.Connector) error {
	cobraCmd := ecdysis.CobraCmdFromContext(ctx)
	w := cobraCmd.OutOrStdout()
	var b strings.Builder

	// Build ID
	fmt.Fprintf(&b, "ID: %s\n", pipeline.Id)

	// Build State
	b.WriteString("State:\n")
	if pipeline.State != nil {
		fmt.Fprintf(&b, "  Status: %s\n", pipeline.State.Status)
	}

	// Build Config
	b.WriteString("Config:\n")
	if pipeline.Config != nil {
		fmt.Fprintf(&b, "  Name: %s\n", pipeline.Config.Name)
		fmt.Fprintf(&b, "  Description: %s\n", pipeline.Config.Description)
	}

	// Build Connector IDs
	b.WriteString("Connector IDs:\n")
	for _, id := range pipeline.ConnectorIds {
		fmt.Fprintf(&b, "  - %s\n", id)
	}

	// Build Processor IDs
	b.WriteString("Processor IDs:\n")
	for _, id := range pipeline.ProcessorIds {
		fmt.Fprintf(&b, "  - %s\n", id)
	}
	for _, conn := range connectors {
		for _, id := range conn.ProcessorIds {
			fmt.Fprintf(&b, "  - %s (attached to %s connector %s)\n", id, conn.Type, conn.Id)
		}
	}

	// Build timestamps
	if pipeline.CreatedAt != nil {
		fmt.Fprintf(&b, "Created At: %s\n", pipeline.CreatedAt.AsTime().Format("2006-01-02T15:04:05.999999Z"))
	}
	if pipeline.UpdatedAt != nil {
		fmt.Fprintf(&b, "Updated At: %s\n", pipeline.UpdatedAt.AsTime().Format("2006-01-02T15:04:05.999999Z"))
	}

	// Write the complete string to the writer
	_, err := w.Write([]byte(b.String()))
	if err != nil {
		return fmt.Errorf("writing output: %w", err)
	}

	return nil
}
