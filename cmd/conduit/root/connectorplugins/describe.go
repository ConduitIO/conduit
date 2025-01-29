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

	"github.com/conduitio/conduit/cmd/conduit/internal/output"

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
)

type DescribeArgs struct {
	ConnectorPluginID string
}

type DescribeCommand struct {
	args DescribeArgs
}

func (c *DescribeCommand) Usage() string { return "describe" }

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

	c.args.ConnectorPluginID = args[0]
	return nil
}

func (c *DescribeCommand) ExecuteWithClient(ctx context.Context, client *api.Client) error {
	resp, err := client.ConnectorServiceClient.ListConnectorPlugins(ctx, &apiv1.ListConnectorPluginsRequest{
		Name: c.args.ConnectorPluginID,
	})
	if err != nil {
		return fmt.Errorf("failed to list connector plguin: %w", err)
	}

	if len(resp.Plugins) == 0 {
		return nil
	}

	displayConnectorPluginsDescription(resp.Plugins[0])

	return nil
}

func displayConnectorPluginsDescription(c *apiv1.ConnectorPluginSpecifications) {
	if !output.IsEmpty(c.Name) {
		fmt.Printf("Name: %s\n", c.Name)
	}
	if !output.IsEmpty(c.Summary) {
		fmt.Printf("Summary: %s\n", c.Summary)
	}
	if !output.IsEmpty(c.Description) {
		fmt.Printf("Description: %s\n", c.Description)
	}
	if !output.IsEmpty(c.Author) {
		fmt.Printf("Author: %s\n", c.Author)
	}
	if !output.IsEmpty(c.Version) {
		fmt.Printf("Version: %s\n", c.Version)
	}
	if len(c.SourceParams) > 0 {
		fmt.Printf("\nSource Parameters:\n")
		output.DisplayConfigParams(c.SourceParams)
	}
	if len(c.DestinationParams) > 0 {
		fmt.Printf("\nDestination Parameters:\n")
		output.DisplayConfigParams(c.DestinationParams)
	}
}
