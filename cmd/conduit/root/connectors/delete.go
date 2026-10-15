// Copyright © 2026 Meroxa, Inc.
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
	"strings"

	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/conduitio/ecdysis"
)

var (
	_ cecdysis.CommandWithExecuteWithClientResult = (*DeleteCommand)(nil)
	_ ecdysis.CommandWithDocs                     = (*DeleteCommand)(nil)
	_ ecdysis.CommandWithArgs                     = (*DeleteCommand)(nil)
	_ ecdysis.CommandWithFlags                    = (*DeleteCommand)(nil)
)

// DeleteArgs holds DeleteCommand's positional argument.
type DeleteArgs struct {
	ConnectorID string
}

// DeleteFlags holds DeleteCommand's flags.
type DeleteFlags struct {
	Force             bool `long:"force" usage:"delete the connector even if its delete hook fails; resources it created outside Conduit may remain and are reported by 'conduit doctor'"`
	AcknowledgeOrphan bool `long:"acknowledge-orphan" usage:"clear the orphan entry a forced delete recorded for this connector ID, after you removed what it left behind"`
}

// DeleteCommand implements `conduit connectors delete CONNECTOR_ID`, via
// the DeleteConnector RPC. The connector's delete hook runs first and the
// connector is removed only if it succeeds (#3058); --force removes it
// anyway and prints the connector.orphaned_resources warning. It is a
// Family B command: --json prints the DeleteConnectorResponse as protojson.
type DeleteCommand struct {
	args  DeleteArgs
	flags DeleteFlags
}

func (c *DeleteCommand) Usage() string { return "delete CONNECTOR_ID" }

func (c *DeleteCommand) Flags() []ecdysis.Flag {
	return ecdysis.BuildFlags(&c.flags)
}

func (c *DeleteCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Delete a connector from a running Conduit",
		Long: `Deletes a connector registered in a running Conduit. The connector's pipeline must not be running.

The connector's delete hook runs first. It releases what the connector created outside Conduit, such as a
Postgres replication slot. If the hook fails, the connector is kept and the command fails with
connector.delete_hook_failed (exit 2), so you can fix the cause and delete again.

With --force the connector is deleted even if the hook fails. The command then prints a
connector.orphaned_resources warning naming what may be left behind, and 'conduit doctor' reports it until you
clean up and run 'conduit connectors delete CONNECTOR_ID --acknowledge-orphan'.

Connectors created from a pipeline config file can't be deleted here; remove them from the file instead.`,
		Example: "conduit connectors delete my-connector\n" +
			"conduit connectors delete my-connector --force --json\n" +
			"conduit connectors delete my-connector --acknowledge-orphan",
	}
}

func (c *DeleteCommand) Args(args []string) error {
	if len(args) == 0 {
		return cerrors.Errorf("requires a connector ID")
	}
	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
	}
	c.args.ConnectorID = args[0]
	return nil
}

func (c *DeleteCommand) ExecuteWithClientResult(ctx context.Context, client *api.Client) (any, error) {
	if c.flags.Force && c.flags.AcknowledgeOrphan {
		err := conduiterr.New(conduiterr.CodeInvalidArgument, "--force and --acknowledge-orphan can't be combined")
		err.Suggestion = "delete with --force first; once you cleaned up, run again with only --acknowledge-orphan"
		return nil, err
	}
	resp, err := client.ConnectorServiceClient.DeleteConnector(ctx, &apiv1.DeleteConnectorRequest{
		Id:                c.args.ConnectorID,
		Force:             c.flags.Force,
		AcknowledgeOrphan: c.flags.AcknowledgeOrphan,
	})
	if err != nil {
		return nil, cerrors.Errorf("failed to delete connector %q: %w", c.args.ConnectorID, err)
	}
	return resp, nil
}

func (c *DeleteCommand) Render(result any) string {
	resp, ok := result.(*apiv1.DeleteConnectorResponse)
	if !ok {
		return ""
	}
	var b strings.Builder
	if c.flags.AcknowledgeOrphan {
		fmt.Fprintf(&b, "Orphan entry for connector %s acknowledged.\n", c.args.ConnectorID)
		return b.String()
	}
	fmt.Fprintf(&b, "Connector %s deleted.\n", c.args.ConnectorID)
	for _, w := range resp.GetWarnings() {
		fmt.Fprintf(&b, "\nWarning [%s]: %s\n", w.GetCode(), w.GetMessage())
		if w.GetSuggestion() != "" {
			fmt.Fprintf(&b, "  %s\n", w.GetSuggestion())
		}
	}
	return b.String()
}
