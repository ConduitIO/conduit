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

	"github.com/alexeyco/simpletable"
	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/conduitio/ecdysis"
)

var (
	_ ecdysis.CommandWithAliases            = (*ListCommand)(nil)
	_ ecdysis.CommandWithDocs               = (*ListCommand)(nil)
	_ ecdysis.CommandWithFlags              = (*ListCommand)(nil)
	_ cecdysis.CommandWithExecuteWithClient = (*ListCommand)(nil)
)

type ListFlags struct {
	Name string `long:"name" usage:"name to filter processor plugins by"`
}

type ListCommand struct {
	flags ListFlags
}

func (c *ListCommand) Flags() []ecdysis.Flag {
	return ecdysis.BuildFlags(&c.flags)
}

func (c *ListCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "List existing Conduit Processor Plugins",
		Long: `This command requires Conduit to be already running since it will list all processor plugins that 
could be added to your pipelines.`,
		Example: "conduit processor-plugins list\nconduit processor-plugins ls",
	}
}

func (c *ListCommand) Aliases() []string { return []string{"ls"} }

func (c *ListCommand) Usage() string { return "list" }

func (c *ListCommand) ExecuteWithClient(ctx context.Context, client *api.Client) error {
	regex := fmt.Sprintf(".*%s.*", c.flags.Name)
	resp, err := client.ProcessorServiceClient.ListProcessorPlugins(ctx, &apiv1.ListProcessorPluginsRequest{
		Name: regex,
	})
	if err != nil {
		return fmt.Errorf("failed to list processor plugins: %w", err)
	}

	displayProcessorPlugins(resp.Plugins)

	return nil
}

func displayProcessorPlugins(processorPlugins []*apiv1.ProcessorPluginSpecifications) {
	if len(processorPlugins) == 0 {
		return
	}

	table := simpletable.New()

	table.Header = &simpletable.Header{
		Cells: []*simpletable.Cell{
			{Align: simpletable.AlignCenter, Text: "NAME"},
			{Align: simpletable.AlignCenter, Text: "SUMMARY"},
		},
	}

	for _, p := range processorPlugins {
		r := []*simpletable.Cell{
			{Align: simpletable.AlignLeft, Text: p.Name},
			{Align: simpletable.AlignLeft, Text: p.Summary},
		}

		table.Body.Cells = append(table.Body.Cells, r)
	}
	table.SetStyle(simpletable.StyleCompact)
	fmt.Println(table.String())
}
