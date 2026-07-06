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
	"bytes"
	"context"
	"fmt"

	"github.com/conduitio/conduit/cmd/conduit/internal/display"

	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/conduitio/ecdysis"
	json "github.com/goccy/go-json"
)

var (
	_ cecdysis.CommandWithExecuteWithClientResult = (*DescribeCommand)(nil)
	_ ecdysis.CommandWithAliases                  = (*DescribeCommand)(nil)
	_ ecdysis.CommandWithDocs                     = (*DescribeCommand)(nil)
	_ ecdysis.CommandWithArgs                     = (*DescribeCommand)(nil)
)

type DescribeArgs struct {
	ConnectorID string
}

type DescribeCommand struct {
	args DescribeArgs
}

// DescribeResult is the result of describing a connector: the connector plus
// the processors attached to it. It has no single proto message, so it marshals
// its proto parts via protojson (see MarshalJSON) to keep --json consistent with
// the single-message commands.
type DescribeResult struct {
	Connector  *apiv1.Connector   `json:"connector"`
	Processors []*apiv1.Processor `json:"processors"`
}

// MarshalJSON renders the proto parts via protojson so this composite view emits
// the same JSON shape (enum names, RFC3339 timestamps) as `connectors list`.
func (r *DescribeResult) MarshalJSON() ([]byte, error) {
	connector, err := cecdysis.ProtoJSON(r.Connector)
	if err != nil {
		return nil, err
	}
	processors, err := cecdysis.ProtoJSONSlice(r.Processors)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(struct {
		Connector  json.RawMessage   `json:"connector"`
		Processors []json.RawMessage `json:"processors"`
	}{connector, processors})
	if err != nil {
		return nil, cerrors.Errorf("marshal connector describe result: %w", err)
	}
	return out, nil
}

func (c *DescribeCommand) Usage() string { return "describe CONNECTOR_ID" }

func (c *DescribeCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Describe an existing connector",
		Long: `This command requires Conduit to be already running since it will describe a connector registered
by Conduit. You can list existing connectors with the 'conduit connectors list' command.`,
		Example: "conduit connectors describe connector:source\n" +
			"conduit connectors desc connector:destination",
	}
}

func (c *DescribeCommand) Aliases() []string { return []string{"desc"} }

func (c *DescribeCommand) Args(args []string) error {
	if len(args) == 0 {
		return cerrors.Errorf("requires a connector ID")
	}

	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
	}

	c.args.ConnectorID = args[0]
	return nil
}

func (c *DescribeCommand) ExecuteWithClientResult(ctx context.Context, client *api.Client) (any, error) {
	resp, err := client.ConnectorServiceClient.GetConnector(ctx, &apiv1.GetConnectorRequest{
		Id: c.args.ConnectorID,
	})
	if err != nil {
		return nil, cerrors.Errorf("failed to get connector: %w", err)
	}

	var processors []*apiv1.Processor

	for _, processorID := range resp.Connector.ProcessorIds {
		processor, err := client.ProcessorServiceClient.GetProcessor(ctx, &apiv1.GetProcessorRequest{
			Id: processorID,
		})
		if err != nil {
			return nil, cerrors.Errorf("failed to get processor: %w", err)
		}
		processors = append(processors, processor.Processor)
	}

	return &DescribeResult{Connector: resp.Connector, Processors: processors}, nil
}

// Render returns the human-readable detail view.
func (c *DescribeCommand) Render(result any) string {
	res, ok := result.(*DescribeResult)
	if !ok {
		return ""
	}

	buf := new(bytes.Buffer)
	out := &ecdysis.DefaultOutput{}
	out.Output(buf, nil)

	displayConnector(out, res.Connector, res.Processors)

	return buf.String()
}

func displayConnector(out ecdysis.Output, connector *apiv1.Connector, processors []*apiv1.Processor) {
	if connector == nil {
		return
	}

	out.Stdout(fmt.Sprintf("ID: %s\n", connector.Id))

	out.Stdout(fmt.Sprintf("Type: %s\n", display.ConnectorTypeToString(connector.Type)))
	out.Stdout(fmt.Sprintf("Plugin: %s\n", connector.Plugin))
	out.Stdout(fmt.Sprintf("Pipeline ID: %s\n", connector.PipelineId))

	display.DisplayConnectorConfig(out, connector.Config, 0)
	out.Stdout(fmt.Sprintf("Created At: %s\n", display.PrintTime(connector.CreatedAt)))
	out.Stdout(fmt.Sprintf("Updated At: %s\n", display.PrintTime(connector.UpdatedAt)))

	display.DisplayProcessors(out, processors, 0)
}
