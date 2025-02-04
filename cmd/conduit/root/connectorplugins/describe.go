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

package connectorplugins

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
	connectorPluginID string
}

type DescribeCommand struct {
	args   DescribeArgs
	output ecdysis.Output
}

func (c *DescribeCommand) Output(output ecdysis.Output) {
	c.output = output
}

func (c *DescribeCommand) Usage() string { return "describe <connector-plugin-id>" }

func (c *DescribeCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Describe an existing connector plugin",
		Long: `This command requires Conduit to be already running since it will describe a connector plugin that 
could be added to your pipelines. You can list existing connector plugins with the 'conduit connector-plugins list' command.`,
		Example: "conduit connector-plugins describe builtin:file@v0.1.0\n" +
			"conduit connector-plugins desc standalone:postgres@v0.9.0",
	}
}

func (c *DescribeCommand) Aliases() []string { return []string{"desc"} }

func (c *DescribeCommand) Args(args []string) error {
	if len(args) == 0 {
		return cerrors.Errorf("requires a connector plugin ID")
	}

	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
	}

	c.args.connectorPluginID = args[0]
	return nil
}

func (c *DescribeCommand) ExecuteWithClient(ctx context.Context, client *api.Client) error {
	resp, err := client.ConnectorServiceClient.ListConnectorPlugins(ctx, &apiv1.ListConnectorPluginsRequest{
		Name: c.args.connectorPluginID,
	})
	if err != nil {
		return cerrors.Errorf("failed to list connector plguin: %w", err)
	}

	if len(resp.Plugins) == 0 {
		return nil
	}

	displayConnectorPluginsDescription(c.output, resp.Plugins[0])

	return nil
}

func displayConnectorPluginsDescription(out ecdysis.Output, c *apiv1.ConnectorPluginSpecifications) {
	if !display.IsEmpty(c.Name) {
		out.Stdout(fmt.Sprintf("Name: %s\n", c.Name))
	}
	if !display.IsEmpty(c.Summary) {
		out.Stdout(fmt.Sprintf("Summary: %s\n", c.Summary))
	}
	if !display.IsEmpty(c.Description) {
		out.Stdout(fmt.Sprintf("Description: %s\n", c.Description))
	}
	if !display.IsEmpty(c.Author) {
		out.Stdout(fmt.Sprintf("Author: %s\n", c.Author))
	}
	if !display.IsEmpty(c.Version) {
		out.Stdout(fmt.Sprintf("Version: %s\n", c.Version))
	}
	if len(c.SourceParams) > 0 {
		out.Stdout("\nSource Parameters:\n")
		display.DisplayConfigParams(out, c.SourceParams)
	}
	if len(c.DestinationParams) > 0 {
		out.Stdout("\nDestination Parameters:\n")
		display.DisplayConfigParams(out, c.DestinationParams)
	}
}
