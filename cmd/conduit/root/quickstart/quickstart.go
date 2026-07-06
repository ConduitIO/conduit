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

// Package quickstart implements `conduit quickstart`, the zero-config 5-minute-wow
// command: it scaffolds an ephemeral demo pipeline (built-in generator -> built-in
// log) and runs it in-process so a new user sees records flowing within seconds,
// with nothing written to their working directory.
package quickstart

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/ecdysis"
)

var (
	_ ecdysis.CommandWithExecute = (*QuickstartCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*QuickstartCommand)(nil)
	_ ecdysis.CommandWithFlags   = (*QuickstartCommand)(nil)
)

// demoPipeline is a self-contained generator->log pipeline. The generator produces
// structured sample records at 1/s and the log destination prints them, so records
// visibly flow with no setup. The generator settings match the no-flag demo used by
// `conduit pipelines init` so the two stay consistent.
const demoPipeline = `version: "2.2"
pipelines:
  - id: quickstart
    status: running
    description: Demo pipeline — generates sample records and logs them.
    connectors:
      - id: source
        type: source
        plugin: builtin:generator
        settings:
          format.type: structured
          format.options.scheduledDeparture: time
          format.options.airline: string
          rate: "1"
          # the sample records use unstructured keys; skip key-schema encoding so
          # the demo output stays clean.
          sdk.schema.extract.key.enabled: "false"
      - id: destination
        type: destination
        plugin: builtin:log
        settings:
          sdk.schema.extract.key.enabled: "false"
`

// logFormatJSON is the config string for JSON-formatted logs.
const logFormatJSON = "json"

type QuickstartFlags struct {
	JSON bool `long:"json" usage:"Emit logs and records as JSON instead of human-readable text."`
}

type QuickstartCommand struct {
	flags QuickstartFlags
}

func (c *QuickstartCommand) Flags() []ecdysis.Flag { return ecdysis.BuildFlags(&c.flags) }

func (c *QuickstartCommand) Usage() string { return "quickstart" }

func (c *QuickstartCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Run a demo pipeline to see Conduit working in under a minute.",
		Long: `Scaffold an ephemeral demo pipeline — a built-in generator streaming sample
records to the built-in log destination — and run it immediately. Records flow to
your console within seconds, with no configuration and nothing written to your
working directory.

State is in-memory and the scaffolded config lives in a temporary directory that is
removed on exit. Press Ctrl-C to stop. To build your own pipeline, see
'conduit pipelines init'.`,
	}
}

// setup scaffolds the ephemeral demo workspace and builds the runtime config. It is
// separated from Execute so it can be tested without starting the blocking server.
// On any error it removes anything it created.
func (c *QuickstartCommand) setup() (dir string, cfg conduit.Config, err error) {
	dir, err = os.MkdirTemp("", "conduit-quickstart-*")
	if err != nil {
		return "", conduit.Config{}, cerrors.Errorf("failed to create temp workspace: %w", err)
	}
	// Create all workspace dirs the runtime scans, so it doesn't warn about missing
	// connectors/processors directories during a clean demo.
	for _, sub := range []string{"pipelines", "connectors", "processors"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o750); err != nil {
			_ = os.RemoveAll(dir)
			return "", conduit.Config{}, cerrors.Errorf("failed to create %s dir: %w", sub, err)
		}
	}
	pipelinesDir := filepath.Join(dir, "pipelines")
	if err := os.WriteFile(filepath.Join(pipelinesDir, "quickstart.yaml"), []byte(demoPipeline), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", conduit.Config{}, cerrors.Errorf("failed to write demo pipeline: %w", err)
	}

	cfg = conduit.DefaultConfigWithBasePath(dir)
	cfg.DB.Type = conduit.DBTypeInMemory // zero disk footprint
	cfg.Pipelines.Path = pipelinesDir
	cfg.Connectors.Path = filepath.Join(dir, "connectors")
	cfg.Processors.Path = filepath.Join(dir, "processors")
	cfg.API.Enabled = true
	if c.flags.JSON {
		cfg.Log.Format = logFormatJSON
	}
	return dir, cfg, nil
}

func (c *QuickstartCommand) Execute(_ context.Context) error {
	dir, cfg, err := c.setup()
	if err != nil {
		return err
	}
	// Serve returns on a graceful (single) Ctrl-C/SIGTERM, at which point we clean
	// up. A second signal or a fatal error calls os.Exit, skipping this — the temp
	// dir is then reclaimed by the OS, and the in-memory store means nothing to
	// corrupt.
	defer os.RemoveAll(dir)

	if cfg.Log.Format != logFormatJSON {
		fmt.Fprint(os.Stdout, "Starting a demo pipeline (generator → log). "+
			"Records will flow below — press Ctrl-C to stop.\n\n")
	}

	(&conduit.Entrypoint{}).Serve(cfg)
	return nil
}
