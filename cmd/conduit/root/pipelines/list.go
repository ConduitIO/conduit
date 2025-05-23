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
	"sort"

	"github.com/conduitio/conduit/cmd/conduit/internal/display"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"

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
	_ ecdysis.CommandWithOutput             = (*ListCommand)(nil)
)

type ListCommand struct {
	output ecdysis.Output
}

func (c *ListCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "List existing Conduit pipelines",
		Long: `This command requires Conduit to be already running since it will list all pipelines registered 
by Conduit. This will depend on the configured pipelines directory, which by default is /pipelines; however, it could 
be configured via --pipelines.path at the time of running Conduit.`,
		Example: "conduit pipelines list\nconduit pipelines ls",
	}
}

func (c *ListCommand) Output(output ecdysis.Output) {
	c.output = output
}

func (c *ListCommand) Aliases() []string { return []string{"ls"} }

func (c *ListCommand) Usage() string { return "list" }

func (c *ListCommand) ExecuteWithClient(ctx context.Context, client *api.Client) error {
	resp, err := client.PipelineServiceClient.ListPipelines(ctx, &apiv1.ListPipelinesRequest{})
	if err != nil {
		return cerrors.Errorf("failed to list pipelines: %w", err)
	}

	sort.Slice(resp.Pipelines, func(i, j int) bool {
		return resp.Pipelines[i].Id < resp.Pipelines[j].Id
	})

	c.output.Stdout(getPipelinesTable(resp.Pipelines) + "\n")

	return nil
}

func getPipelinesTable(pipelines []*apiv1.Pipeline) string {
	if len(pipelines) == 0 {
		return ""
	}

	table := simpletable.New()

	table.Header = &simpletable.Header{
		Cells: []*simpletable.Cell{
			{Align: simpletable.AlignCenter, Text: "ID"},
			{Align: simpletable.AlignCenter, Text: "STATE"},
			{Align: simpletable.AlignCenter, Text: "CREATED"},
			{Align: simpletable.AlignCenter, Text: "LAST_UPDATED"},
		},
	}

	for _, p := range pipelines {
		r := []*simpletable.Cell{
			{Align: simpletable.AlignLeft, Text: p.Id},
			{Align: simpletable.AlignLeft, Text: display.PrintStatusFromProtoString(p.State.Status.String())},
			{Align: simpletable.AlignLeft, Text: display.PrintTime(p.CreatedAt)},
			{Align: simpletable.AlignLeft, Text: display.PrintTime(p.UpdatedAt)},
		}

		table.Body.Cells = append(table.Body.Cells, r)
	}
	return table.String()
}
