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
	"sort"

	"github.com/alexeyco/simpletable"
	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal/display"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/conduitio/ecdysis"
)

var (
	_ cecdysis.CommandWithExecuteWithClient = (*ListCommand)(nil)
	_ ecdysis.CommandWithAliases            = (*ListCommand)(nil)
	_ ecdysis.CommandWithDocs               = (*ListCommand)(nil)
	_ ecdysis.CommandWithFlags              = (*ListCommand)(nil)
	_ ecdysis.CommandWithOutput             = (*ListCommand)(nil)
)

type ListFlags struct {
	PipelineID string `long:"pipeline-id" usage:"filter connectors by pipeline ID"`
}

type ListCommand struct {
	flags  ListFlags
	output ecdysis.Output
}

func (c *ListCommand) Output(output ecdysis.Output) {
	c.output = output
}

func (c *ListCommand) Flags() []ecdysis.Flag {
	return ecdysis.BuildFlags(&c.flags)
}

func (c *ListCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "List existing Conduit connectors",
		Long: `This command requires Conduit to be already running since it will list all connectors registered 
by Conduit.`,
		Example: "conduit connectors list\nconduit connectors ls",
	}
}

func (c *ListCommand) Aliases() []string { return []string{"ls"} }

func (c *ListCommand) Usage() string { return "list" }

func (c *ListCommand) ExecuteWithClient(ctx context.Context, client *api.Client) error {
	resp, err := client.ConnectorServiceClient.ListConnectors(ctx, &apiv1.ListConnectorsRequest{
		PipelineId: c.flags.PipelineID,
	})
	if err != nil {
		return fmt.Errorf("failed to list connectors: %w", err)
	}

	sort.Slice(resp.Connectors, func(i, j int) bool {
		return resp.Connectors[i].Id < resp.Connectors[j].Id
	})

	c.output.Stdout(getConnectorsTable(resp.Connectors) + "\n")

	return nil
}

func getConnectorsTable(connectors []*apiv1.Connector) string {
	if len(connectors) == 0 {
		return ""
	}

	table := simpletable.New()

	table.Header = &simpletable.Header{
		Cells: []*simpletable.Cell{
			{Align: simpletable.AlignCenter, Text: "ID"},
			{Align: simpletable.AlignCenter, Text: "PLUGIN"},
			{Align: simpletable.AlignCenter, Text: "TYPE"},
			{Align: simpletable.AlignCenter, Text: "PIPELINE_ID"},
			{Align: simpletable.AlignCenter, Text: "CREATED"},
			{Align: simpletable.AlignCenter, Text: "LAST_UPDATED"},
		},
	}

	for _, c := range connectors {
		r := []*simpletable.Cell{
			{Align: simpletable.AlignLeft, Text: c.Id},
			{Align: simpletable.AlignLeft, Text: c.Plugin},
			{Align: simpletable.AlignLeft, Text: display.ConnectorTypeToString(c.Type)},
			{Align: simpletable.AlignLeft, Text: c.PipelineId},
			{Align: simpletable.AlignLeft, Text: display.PrintTime(c.CreatedAt)},
			{Align: simpletable.AlignLeft, Text: display.PrintTime(c.UpdatedAt)},
		}
		table.Body.Cells = append(table.Body.Cells, r)
	}
	return table.String()
}
