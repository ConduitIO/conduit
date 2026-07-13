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

package pipelines

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
	_ ecdysis.CommandWithFlags   = (*DevCommand)(nil)
	_ ecdysis.CommandWithExecute = (*DevCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*DevCommand)(nil)
	_ ecdysis.CommandWithConfig  = (*DevCommand)(nil)
	_ ecdysis.CommandWithArgs    = (*DevCommand)(nil)
)

// DevArgs holds DevCommand's optional positional argument.
type DevArgs struct {
	// Dir is a GitOps-friendly alias for --pipelines.path, matching `run`'s
	// --pipelines flag but as a positional argument (`conduit pipelines dev
	// [dir]` reads more naturally than `conduit pipelines dev
	// --pipelines.path dir`).
	Dir string
}

// DevFlags holds DevCommand's flags. conduit.Config is embedded for the same
// reason RunFlags embeds it: `pipelines dev` runs a full `conduit run --dev`
// server, so it accepts the same db.*/api.*/pipelines.*/... configuration.
type DevFlags struct {
	conduit.Config
}

// DevCommand implements `conduit pipelines dev [dir]`: the design doc's §4
// alias, "thin sugar over run --dev ... carrying no logic of its own". It
// does not reimplement any watch/apply behavior (that all lives in
// pkg/conduit/dev, driven from pkg/conduit.Runtime) — it only sets
// Cfg.Dev.Enabled, points Cfg.Pipelines.Path at the optional dir argument,
// applies the "dev defaults" (exit-on-error off, so one bad pipeline while
// iterating doesn't take the whole dev server down), and calls the exact
// same conduit.Entrypoint.Serve RunCommand does.
type DevCommand struct {
	args  DevArgs
	flags DevFlags
	Cfg   conduit.Config
}

func (c *DevCommand) Usage() string { return "dev [dir]" }

func (c *DevCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Run Conduit with the hot-reload dev watcher (alias for 'conduit run --dev')",
		Long: `Starts the Conduit server exactly like 'conduit run --dev', watching pipelines.path (or
[dir], if given) for changes and applying them into the running engine as you save — a processor
edit applies in place (no restart), a source/destination edit applies via a labeled graceful
restart, and a bad edit is reported without ever touching the running pipeline. See
docs/design-documents/20260712-pipeline-dev-hot-reload.md §4 and docs/operations/dev-hot-reload.md.

This is sugar over 'conduit run --dev [dir]', with exit-on-degraded forced off (dev iterates on
pipelines that may be intentionally broken mid-edit; one degraded pipeline should not take the
whole dev server down) — it carries no watcher logic of its own.`,
		Example: "conduit pipelines dev\n" +
			"conduit pipelines dev ./pipelines\n" +
			"conduit pipelines dev --dev.json",
	}
}

func (c *DevCommand) Args(args []string) error {
	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
	}
	if len(args) == 1 {
		c.args.Dir = args[0]
	}
	return nil
}

func (c *DevCommand) Execute(ctx context.Context) error {
	if c.args.Dir != "" {
		c.Cfg.Pipelines.Path = c.args.Dir
	}
	// `pipelines dev` always runs in dev mode — that is the entire point of
	// the alias — and defaults exit-on-degraded off (see the type doc).
	c.Cfg.Dev.Enabled = true
	c.Cfg.Pipelines.ExitOnDegraded = false

	if !c.Cfg.API.Enabled {
		fmt.Print("Warning: API is currently disabled. Most Conduit CLI commands won't work without the API enabled." +
			"To enable it, run conduit with `--api.enabled=true` or set `CONDUIT_API_ENABLED=true` in your environment.")
	}

	(&conduit.Entrypoint{}).Serve(c.Cfg)
	return nil
}

func (c *DevCommand) Config() ecdysis.Config {
	path := filepath.Dir(c.flags.ConduitCfg.Path)
	return ecdysis.Config{
		EnvPrefix:     envPrefix,
		Parsed:        &c.Cfg,
		Path:          c.flags.ConduitCfg.Path,
		DefaultValues: conduit.DefaultConfigWithBasePath(path),
	}
}

// Flags mirrors run.RunCommand.Flags's defaulting (storeConfigDefaults
// covers only the offline deploy/apply subset; `dev` runs a full server, so
// it needs the same defaults `run` sets, including the dev.* group so
// --dev.json works without also passing --dev).
func (c *DevCommand) Flags() []ecdysis.Flag {
	flags := ecdysis.BuildFlags(&c.flags)

	currentPath, err := os.Getwd()
	if err != nil {
		panic(cerrors.Errorf("failed to get current working directory: %w", err))
	}

	c.Cfg = conduit.DefaultConfigWithBasePath(currentPath)
	flags.SetDefault("config.path", c.Cfg.ConduitCfg.Path)
	flags.SetDefault("db.type", c.Cfg.DB.Type)
	flags.SetDefault("db.badger.path", c.Cfg.DB.Badger.Path)
	flags.SetDefault("db.postgres.connection-string", c.Cfg.DB.Postgres.ConnectionString)
	flags.SetDefault("db.postgres.table", c.Cfg.DB.Postgres.Table)
	flags.SetDefault("db.sqlite.path", c.Cfg.DB.SQLite.Path)
	flags.SetDefault("db.sqlite.table", c.Cfg.DB.SQLite.Table)
	flags.SetDefault("api.enabled", c.Cfg.API.Enabled)
	flags.SetDefault("api.http.address", c.Cfg.API.HTTP.Address)
	flags.SetDefault("api.grpc.address", c.Cfg.API.GRPC.Address)
	flags.SetDefault("api.allow-live-restart-apply", c.Cfg.API.AllowLiveRestartApply)
	flags.SetDefault("log.level", c.Cfg.Log.Level)
	flags.SetDefault("log.format", c.Cfg.Log.Format)
	flags.SetDefault("connectors.path", c.Cfg.Connectors.Path)
	flags.SetDefault("connectors.max-receive-record-size", c.Cfg.Connectors.MaxReceiveRecordSize)
	flags.SetDefault("processors.path", c.Cfg.Processors.Path)
	flags.SetDefault("pipelines.path", c.Cfg.Pipelines.Path)
	flags.SetDefault("pipelines.error-recovery.min-delay", c.Cfg.Pipelines.ErrorRecovery.MinDelay)
	flags.SetDefault("pipelines.error-recovery.max-delay", c.Cfg.Pipelines.ErrorRecovery.MaxDelay)
	flags.SetDefault("pipelines.error-recovery.backoff-factor", c.Cfg.Pipelines.ErrorRecovery.BackoffFactor)
	flags.SetDefault("pipelines.error-recovery.max-retries", c.Cfg.Pipelines.ErrorRecovery.MaxRetries)
	flags.SetDefault("pipelines.error-recovery.max-retries-window", c.Cfg.Pipelines.ErrorRecovery.MaxRetriesWindow)
	flags.SetDefault("schema-registry.type", c.Cfg.SchemaRegistry.Type)
	flags.SetDefault("schema-registry.confluent.connection-string", c.Cfg.SchemaRegistry.Confluent.ConnectionString)
	flags.SetDefault("preview.pipeline-arch-v2", c.Cfg.Preview.PipelineArchV2)
	flags.SetDefault("preview.pipeline-arch-v2-disable-metrics", c.Cfg.Preview.PipelineArchV2DisableMetrics)
	// pipelines.exit-on-degraded is deliberately NOT defaulted from
	// DefaultConfigWithBasePath here: Execute always forces it false for
	// `pipelines dev` regardless of flags/env/config file (see the type doc).

	return flags
}
