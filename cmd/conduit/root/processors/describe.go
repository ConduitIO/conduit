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

package processors

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
	_ ecdysis.CommandWithOutput             = (*DescribeCommand)(nil)
)

type DescribeArgs struct {
	ProcessorID string
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
		Short: "Describe an existing processor",
		Long: `This command requires Conduit to be already running since it will describe a processor registered
by Conduit. You can list existing processors with the 'conduit processors list' command.`,
		Example: "conduit processors describe pipeline-processor\n" +
			"conduit processor desc connector-processor",
	}
}

func (c *DescribeCommand) Aliases() []string { return []string{"desc"} }

func (c *DescribeCommand) Args(args []string) error {
	if len(args) == 0 {
		return cerrors.Errorf("requires a processor ID")
	}

	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
	}

	c.args.ProcessorID = args[0]
	return nil
}

func (c *DescribeCommand) ExecuteWithClient(ctx context.Context, client *api.Client) error {
	resp, err := client.ProcessorServiceClient.GetProcessor(ctx, &apiv1.GetProcessorRequest{
		Id: c.args.ProcessorID,
	})
	if err != nil {
		return fmt.Errorf("failed to get processor: %w", err)
	}

	displayProcessor(c.output, resp.Processor)
	return nil
}

func displayProcessor(out ecdysis.Output, p *apiv1.Processor) {
	out.Stdout(fmt.Sprintf("ID: %s\n", p.Id))
	out.Stdout(fmt.Sprintf("Plugin: %s\n", p.Plugin))

	out.Stdout(fmt.Sprintf("Parent: %s (%s)\n", output.ProcessorParentToString(p.Parent.Type), p.Parent.Id))

	if !output.IsEmpty(p.Condition) {
		out.Stdout(fmt.Sprintf("Condition: %s\n", p.Condition))
	}

	if len(p.Config.Settings) > 0 {
		out.Stdout("Config:\n")
		for name, value := range p.Config.Settings {
			out.Stdout(fmt.Sprintf("%s%s: %s\n", output.Indentation(1), name, value))
		}
	}
	out.Stdout(fmt.Sprintf("Workers: %d\n", p.Config.Workers))

	out.Stdout(fmt.Sprintf("Created At: %s\n", output.PrintTime(p.CreatedAt)))
	out.Stdout(fmt.Sprintf("Updated At: %s\n", output.PrintTime(p.UpdatedAt)))
}
