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

package processorplugins

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
	processorPluginID string
}

type DescribeCommand struct {
	args   DescribeArgs
	output ecdysis.Output
}

func (c *DescribeCommand) Output(output ecdysis.Output) {
	c.output = output
}

func (c *DescribeCommand) Usage() string { return "describe <processor-plugin-id>" }

func (c *DescribeCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Describe an existing processor plugin.",
		Long: `This command requires Conduit to be already running since it will show a processor plugins that 
could be added to your pipelines.`,
		Example: "conduit processor-plugin describe builtin:base64.decode@v0.1.0\n" +
			"conduit processor-plugin desc standalone:log-processor@v2.0.0",
	}
}

func (c *DescribeCommand) Aliases() []string { return []string{"desc"} }

func (c *DescribeCommand) Args(args []string) error {
	if len(args) == 0 {
		return cerrors.Errorf("requires a processor plugin ID")
	}

	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
	}

	c.args.processorPluginID = args[0]
	return nil
}

func (c *DescribeCommand) ExecuteWithClient(ctx context.Context, client *api.Client) error {
	resp, err := client.ProcessorServiceClient.ListProcessorPlugins(ctx, &apiv1.ListProcessorPluginsRequest{
		Name: c.args.processorPluginID,
	})
	if err != nil {
		return cerrors.Errorf("failed to get processor plugin: %w", err)
	}

	if len(resp.Plugins) == 0 {
		return nil
	}

	displayConnectorPluginsDescription(c.output, resp.Plugins[0])

	return nil
}

func displayConnectorPluginsDescription(out ecdysis.Output, p *apiv1.ProcessorPluginSpecifications) {
	if !display.IsEmpty(p.Name) {
		out.Stdout(fmt.Sprintf("Name: %s\n", p.Name))
	}
	if !display.IsEmpty(p.Summary) {
		out.Stdout(fmt.Sprintf("Summary: %s\n", p.Summary))
	}
	if !display.IsEmpty(p.Description) {
		out.Stdout(fmt.Sprintf("Description: %s\n", p.Description))
	}
	if !display.IsEmpty(p.Author) {
		out.Stdout(fmt.Sprintf("Author: %s\n", p.Author))
	}
	if !display.IsEmpty(p.Version) {
		out.Stdout(fmt.Sprintf("Version: %s\n", p.Version))
	}

	if len(p.Parameters) > 0 {
		out.Stdout("Parameters:\n")
		display.DisplayConfigParams(out, p.Parameters)
	}
}
