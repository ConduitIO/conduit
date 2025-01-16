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

	"github.com/alexeyco/simpletable"
	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/conduitio/ecdysis"
)

var (
	_ cecdysis.CommandWithExecuteWithClient = (*ListCommand)(nil)
	_ ecdysis.CommandWithAliases            = (*ListCommand)(nil)
	_ ecdysis.CommandWithDocs               = (*ListCommand)(nil)
)

type ListCommand struct{}

func (c *ListCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "List existing Conduit processors",
		Long: `This command requires Conduit to be already running since it will list all processors registered 
by Conduit.`,
		Example: "conduit processors list\nconduit processors ls",
	}
}

func (c *ListCommand) Aliases() []string { return []string{"ls"} }

func (c *ListCommand) Usage() string { return "list" }

func (c *ListCommand) ExecuteWithClient(ctx context.Context, client *api.Client) error {
	resp, err := client.ProcessorServiceClient.ListProcessors(ctx, &apiv1.ListProcessorsRequest{})
	if err != nil {
		return fmt.Errorf("failed to list processors: %w", err)
	}

	displayProcessors(resp.Processors)

	return nil
}

func displayProcessors(processors []*apiv1.Processor) {
	if len(processors) == 0 {
		return
	}

	table := simpletable.New()

	table.Header = &simpletable.Header{
		Cells: []*simpletable.Cell{
			{Align: simpletable.AlignCenter, Text: "ID"},
			{Align: simpletable.AlignCenter, Text: "PLUGIN"},
			{Align: simpletable.AlignCenter, Text: "CONDITION"},
			{Align: simpletable.AlignCenter, Text: "CREATED"},
			{Align: simpletable.AlignCenter, Text: "LAST_UPDATED"},
		},
	}

	for _, p := range processors {
		r := []*simpletable.Cell{
			{Align: simpletable.AlignRight, Text: p.Id},
			{Align: simpletable.AlignLeft, Text: p.Plugin},
			{Align: simpletable.AlignLeft, Text: p.Condition},
			{Align: simpletable.AlignLeft, Text: p.CreatedAt.AsTime().String()},
			{Align: simpletable.AlignLeft, Text: p.UpdatedAt.AsTime().String()},
		}

		table.Body.Cells = append(table.Body.Cells, r)
	}
	table.SetStyle(simpletable.StyleCompact)
	fmt.Println(table.String())
}
