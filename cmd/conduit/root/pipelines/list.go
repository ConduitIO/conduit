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

	"github.com/alexeyco/simpletable"
	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/cmd/conduit/root/run"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/conduitio/ecdysis"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
)

var (
	_ ecdysis.CommandWithExecute = (*ListCommand)(nil)
	_ ecdysis.CommandWithAliases = (*ListCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*ListCommand)(nil)
	// with API client ?
)

type ListCommand struct {
	client *api.Client
	RunCmd *run.RunCommand
}

func (c *ListCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "List existing Conduit pipelines",
		Long: `This command requires Conduit to be already running since it will list all pipelines registered 
by Conduit. This will depend on the configured pipelines directory, which by default is /pipelines; however, it could 
be configured via --pipelines.path at the time of running Conduit.`,
		Example: "conduit pipelines ls",
	}
}

func (c *ListCommand) Aliases() []string { return []string{"ls"} }

func (c *ListCommand) Usage() string { return "list" }

func (c *ListCommand) Execute(ctx context.Context) error {
	// TODO: Move this elsewhere since it'll be common for all commands that require having Conduit Running
	// --------- START
	conduitNotRunning := "Notice: To inspect the API, Conduit needs to be running" +
		"\nPlease execute `conduit run`"

	conduitGRPCAddr := c.RunCmd.GRPCAddress()

	conduitClient, err := api.NewClient(ctx, conduitGRPCAddr)
	if err != nil {
		return fmt.Errorf("failed to connect to Conduit server: %w", err)
	}
	defer conduitClient.Close()

	sourceHealthResp, err := conduitClient.HealthService.Check(ctx, &healthgrpc.HealthCheckRequest{})
	if err != nil || sourceHealthResp.Status != healthgrpc.HealthCheckResponse_SERVING {
		fmt.Println(conduitNotRunning)
		return nil
	}
	// --------- END

	resp, err := conduitClient.PipelineService.ListPipelines(ctx, &apiv1.ListPipelinesRequest{})
	if err != nil {
		return fmt.Errorf("failed to list pipelines: %w", err)
	}

	displayPipelines(resp.Pipelines)

	return nil
}

func displayPipelines(pipelines []*apiv1.Pipeline) {
	if len(pipelines) != 0 {
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
				{Align: simpletable.AlignRight, Text: p.Id},
				{Align: simpletable.AlignLeft, Text: p.State.Status.String()},
				{Align: simpletable.AlignLeft, Text: p.CreatedAt.AsTime().String()},
				{Align: simpletable.AlignLeft, Text: p.UpdatedAt.AsTime().String()},
			}

			table.Body.Cells = append(table.Body.Cells, r)
		}
		table.SetStyle(simpletable.StyleCompact)
		fmt.Println(table.String())
	}
}
