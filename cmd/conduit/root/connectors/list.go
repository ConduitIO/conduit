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
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/alexeyco/simpletable"
	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal/display"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/registry"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/conduitio/ecdysis"
)

var (
	_ cecdysis.CommandWithExecuteWithClientResult = (*ListCommand)(nil)
	_ ecdysis.CommandWithAliases                  = (*ListCommand)(nil)
	_ ecdysis.CommandWithDocs                     = (*ListCommand)(nil)
	_ ecdysis.CommandWithFlags                    = (*ListCommand)(nil)
	_ ecdysis.CommandWithConfig                   = (*ListCommand)(nil)
)

type ListFlags struct {
	conduit.Config

	PipelineID string `long:"pipeline-id" usage:"filter connectors by pipeline ID"`

	// Installed switches this command to a COMPLETELY DIFFERENT data
	// source (step5/plan-v2 §3): the local install manifest (installed
	// plugin ARTIFACTS in --connectors.path) rather than the running
	// engine's pipeline connector INSTANCES. Mutually exclusive with
	// --pipeline-id.
	//
	// Known scope limitation of this implementation: ListCommand is wired
	// through cecdysis.CommandWithExecuteWithClientResultDecorator, which
	// unconditionally dials and health-checks the engine BEFORE calling
	// this command at all — so, as shipped, `--installed` still requires a
	// reachable engine even though its own data source (the manifest)
	// does not. Making `--installed` truly engine-independent needs either
	// a second decorator variant that skips the mandatory dial, or
	// splitting this into a separate CommandWithResult-based command —
	// either is a real (if small) change to shared cecdysis machinery or
	// command wiring, deliberately deferred out of this PR to keep its
	// blast radius to pkg/registry and this one file; flagged here and in
	// the PR description rather than silently shipping the design doc's
	// full "independent of whether the engine is running" aspiration.
	IndexURL  string `long:"index-url" usage:"registry index URL (only consulted with --installed, for a best-effort 'latest available' column)"`
	IndexFile string `long:"index-file" usage:"read the index from a local file instead of --index-url (only consulted with --installed)"`
	Installed bool   `long:"installed" usage:"list installed connector PLUGIN ARTIFACTS from the local manifest instead of pipeline connector instances (mutually exclusive with --pipeline-id)"`
}

type ListCommand struct {
	flags ListFlags
	Cfg   conduit.Config
}

func (c *ListCommand) Flags() []ecdysis.Flag {
	flags := ecdysis.BuildFlags(&c.flags)

	currentPath, err := os.Getwd()
	if err != nil {
		panic(cerrors.Errorf("failed to get current working directory: %w", err))
	}
	c.Cfg = conduit.DefaultConfigWithBasePath(currentPath)
	flags.SetDefault("config.path", c.Cfg.ConduitCfg.Path)
	flags.SetDefault("connectors.path", c.Cfg.Connectors.Path)
	flags.SetDefault("index-url", registry.DefaultIndexURL)

	return flags
}

func (c *ListCommand) Config() ecdysis.Config {
	path := filepath.Dir(c.flags.ConduitCfg.Path)
	return ecdysis.Config{
		EnvPrefix:     envPrefix,
		Parsed:        &c.Cfg,
		Path:          c.flags.ConduitCfg.Path,
		DefaultValues: conduit.DefaultConfigWithBasePath(path),
	}
}

func (c *ListCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "List existing Conduit connectors",
		Long: `This command requires Conduit to be already running since it will list all connectors registered
by Conduit.

With --installed, this instead lists installed connector PLUGIN ARTIFACTS from the local
install manifest (--connectors.path) — an entirely different thing from a pipeline connector
instance. Its data source (the manifest) does not itself require the engine, but this command
still dials the engine first, like every other 'connectors list' invocation; --installed does
not yet skip that precondition (a known scope limitation — see ListFlags.Installed's doc
comment). --pipeline-id and --installed are mutually exclusive.`,
		Example: "conduit connectors list\nconduit connectors ls\nconduit connectors list --installed",
	}
}

func (c *ListCommand) Aliases() []string { return []string{"ls"} }

func (c *ListCommand) Usage() string { return "list" }

// ExecuteWithClientResult implements the framework's client-result contract.
// With --installed, it ignores client entirely and instead lists the local
// install manifest (registry.ListInstalled) — a completely different data
// source from the default mode's pipeline connector instances (see
// ListFlags.Installed's doc comment for this implementation's one known
// scope limitation: the client is still dialed unconditionally by the
// decorator before this method ever runs).
func (c *ListCommand) ExecuteWithClientResult(ctx context.Context, client *api.Client) (any, error) {
	if c.flags.Installed {
		if c.flags.PipelineID != "" {
			return nil, cerrors.Errorf("--pipeline-id and --installed are mutually exclusive")
		}
		res, err := registry.ListInstalled(ctx, registry.ListInstalledOptions{
			ConnectorsPath: c.Cfg.Connectors.Path,
			IndexURL:       c.flags.IndexURL,
			IndexFile:      c.flags.IndexFile,
		})
		if err != nil {
			return nil, err
		}
		return res, nil
	}

	resp, err := client.ConnectorServiceClient.ListConnectors(ctx, &apiv1.ListConnectorsRequest{
		PipelineId: c.flags.PipelineID,
	})
	if err != nil {
		return nil, cerrors.Errorf("failed to list connectors: %w", err)
	}

	sort.Slice(resp.Connectors, func(i, j int) bool {
		return resp.Connectors[i].Id < resp.Connectors[j].Id
	})

	return resp, nil
}

// Render returns the human-readable table. The framework renders --json
// itself (protojson for *apiv1.ListConnectorsResponse, go-json for
// *registry.ListInstalledResult — see cecdysis.marshalJSON). The two result
// shapes render as VISIBLY DISTINCT tables (different columns, and the
// installed-mode table names itself explicitly) so a user never confuses
// "N connectors in my pipelines" with "N connector plugin artifacts
// installed on disk" (step5 §3).
func (c *ListCommand) Render(result any) string {
	switch v := result.(type) {
	case *apiv1.ListConnectorsResponse:
		return getConnectorsTable(v.Connectors) + "\n"
	case *registry.ListInstalledResult:
		return getInstalledConnectorsTable(v) + "\n"
	default:
		return ""
	}
}

func getInstalledConnectorsTable(res *registry.ListInstalledResult) string {
	if len(res.Installed) == 0 {
		return "No installed connector plugin artifacts found under --connectors.path."
	}

	table := simpletable.New()
	table.Header = &simpletable.Header{
		Cells: []*simpletable.Cell{
			{Align: simpletable.AlignCenter, Text: "NAME"},
			{Align: simpletable.AlignCenter, Text: "INSTALLED"},
			{Align: simpletable.AlignCenter, Text: "SIGNED"},
			{Align: simpletable.AlignCenter, Text: "INSTALLED_AT"},
			{Align: simpletable.AlignCenter, Text: "LATEST_AVAILABLE"},
			{Align: simpletable.AlignCenter, Text: "STATUS"},
		},
	}

	for _, row := range res.Installed {
		signed := "no"
		if row.Signed {
			signed = "yes"
		}
		latest := row.LatestAvailable
		if latest == "" {
			latest = "—"
		}
		table.Body.Cells = append(table.Body.Cells, []*simpletable.Cell{
			{Align: simpletable.AlignLeft, Text: row.Name},
			{Align: simpletable.AlignLeft, Text: row.Version},
			{Align: simpletable.AlignLeft, Text: signed},
			{Align: simpletable.AlignLeft, Text: row.InstalledAt.UTC().Format("2006-01-02 15:04 MST")},
			{Align: simpletable.AlignLeft, Text: latest},
			{Align: simpletable.AlignLeft, Text: string(row.Status)},
		})
	}

	var b strings.Builder
	b.WriteString("Installed connector PLUGIN ARTIFACTS (--connectors.path) — distinct from pipeline connector instances above.\n")
	if res.IndexUnreachable {
		b.WriteString("note: the registry index was unreachable; LATEST_AVAILABLE/STATUS columns are informational-only for this run.\n")
	}
	b.WriteString(table.String())
	return b.String()
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
