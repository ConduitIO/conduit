// Copyright © 2024 Meroxa, Inc.
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

package run

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
	_ ecdysis.CommandWithFlags   = (*RunCommand)(nil)
	_ ecdysis.CommandWithExecute = (*RunCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*RunCommand)(nil)
	_ ecdysis.CommandWithConfig  = (*RunCommand)(nil)
)

// pipelinesFlagName is the --pipelines alias for --pipelines.path. It is excluded
// from the viper config binding (name collides with the Pipelines struct) and
// applied in Execute.
const pipelinesFlagName = "pipelines"

// devFlagName is the short --dev alias for --dev.enabled (see
// docs/design-documents/20260712-pipeline-dev-hot-reload.md §4: "conduit run
// --dev [--pipelines.path <dir>]"). Like --pipelines/--pipelines.path, it
// can't be bound directly onto conduit.Config (the name "dev" is already a
// struct — conduit.Config.Dev — not a bool), so it is excluded from the
// viper config binding and applied in Execute.
const devFlagName = "dev"

type RunFlags struct {
	conduit.Config
}

type RunCommand struct {
	flags RunFlags
	Cfg   conduit.Config
	// pipelinesAlias backs the --pipelines flag, a GitOps-friendly alias for
	// --pipelines.path. It can't be bound to the config struct via viper (the name
	// collides with the Pipelines struct key), so it's excluded from the config
	// binding (see Config) and applied in Execute.
	pipelinesAlias string
	// devAlias backs the --dev flag — see devFlagName's doc.
	devAlias bool
}

func (c *RunCommand) Execute(ctx context.Context) error {
	cmd := ecdysis.CobraCmdFromContext(ctx)

	// --pipelines is an alias for --pipelines.path; apply it when explicitly set.
	if cmd != nil && cmd.Flags().Changed(pipelinesFlagName) {
		c.Cfg.Pipelines.Path = c.pipelinesAlias
	}
	// --dev is an alias for --dev.enabled; apply it when explicitly set (never
	// clobber an explicit --dev.enabled=false with a stray --dev.enabled default).
	if cmd != nil && cmd.Flags().Changed(devFlagName) {
		c.Cfg.Dev.Enabled = c.devAlias
	}

	e := &conduit.Entrypoint{}

	if !c.Cfg.API.Enabled {
		fmt.Print("Warning: API is currently disabled. Most Conduit CLI commands won't work without the API enabled." +
			"To enable it, run conduit with `--api.enabled=true` or set `CONDUIT_API_ENABLED=true` in your environment.")
	}

	e.Serve(c.Cfg)
	return nil
}

func (c *RunCommand) Config() ecdysis.Config {
	path := filepath.Dir(c.flags.ConduitCfg.Path)

	return ecdysis.Config{
		EnvPrefix: "CONDUIT",
		Parsed:    &c.Cfg,
		Path:      c.flags.ConduitCfg.Path,
		// --pipelines and --dev collide with existing conduit.Config struct
		// keys (Pipelines, Dev); exclude them from the viper binding and
		// apply them in Execute (see pipelinesAlias/devAlias).
		ExcludedFlags: []string{pipelinesFlagName, devFlagName},
		DefaultValues: conduit.DefaultConfigWithBasePath(path),
	}
}

func (c *RunCommand) Usage() string { return "run" }

func (c *RunCommand) Flags() []ecdysis.Flag {
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
	flags.SetDefault("api.http.cors.allowed-origins", c.Cfg.API.HTTP.CORS.AllowedOrigins)
	flags.SetDefault("api.http.ui.enabled", c.Cfg.API.HTTP.UI.Enabled)
	flags.SetDefault("api.grpc.address", c.Cfg.API.GRPC.Address)
	flags.SetDefault("api.allow-live-restart-apply", c.Cfg.API.AllowLiveRestartApply)
	flags.SetDefault("log.level", c.Cfg.Log.Level)
	flags.SetDefault("log.format", c.Cfg.Log.Format)
	flags.SetDefault("connectors.path", c.Cfg.Connectors.Path)
	flags.SetDefault("connectors.max-receive-record-size", c.Cfg.Connectors.MaxReceiveRecordSize)
	flags.SetDefault("processors.path", c.Cfg.Processors.Path)
	flags.SetDefault("pipelines.path", c.Cfg.Pipelines.Path)
	flags.SetDefault("pipelines.exit-on-degraded", c.Cfg.Pipelines.ExitOnDegraded)
	flags.SetDefault("pipelines.error-recovery.min-delay", c.Cfg.Pipelines.ErrorRecovery.MinDelay)
	flags.SetDefault("pipelines.error-recovery.max-delay", c.Cfg.Pipelines.ErrorRecovery.MaxDelay)
	flags.SetDefault("pipelines.error-recovery.backoff-factor", c.Cfg.Pipelines.ErrorRecovery.BackoffFactor)
	flags.SetDefault("pipelines.error-recovery.max-retries", c.Cfg.Pipelines.ErrorRecovery.MaxRetries)
	flags.SetDefault("pipelines.error-recovery.max-retries-window", c.Cfg.Pipelines.ErrorRecovery.MaxRetriesWindow)
	flags.SetDefault("schema-registry.type", c.Cfg.SchemaRegistry.Type)
	flags.SetDefault("schema-registry.confluent.connection-string", c.Cfg.SchemaRegistry.Confluent.ConnectionString)
	flags.SetDefault("preview.pipeline-arch-v2", c.Cfg.Preview.PipelineArchV2)
	flags.SetDefault("preview.pipeline-arch-v2-disable-metrics", c.Cfg.Preview.PipelineArchV2DisableMetrics)

	// --pipelines is a GitOps-friendly alias for --pipelines.path (point Conduit at
	// a directory of pipeline configs). It's excluded from the viper config binding
	// (see Config) because its name collides with the Pipelines struct; Execute
	// copies it into Cfg.Pipelines.Path when set.
	flags = append(flags, ecdysis.Flag{
		Long:    pipelinesFlagName,
		Usage:   "alias for --pipelines.path: path to a pipelines' directory or a pipeline configuration file",
		Ptr:     &c.pipelinesAlias,
		Default: c.Cfg.Pipelines.Path,
	})
	// --dev is the short alias for --dev.enabled: watch --pipelines.path and
	// hot-reload changes into the running engine (see
	// docs/design-documents/20260712-pipeline-dev-hot-reload.md §4). Excluded
	// from the viper config binding (see Config) because its name collides
	// with the Dev struct; Execute copies it into Cfg.Dev.Enabled when set.
	flags = append(flags, ecdysis.Flag{
		Long:    devFlagName,
		Usage:   "alias for --dev.enabled: watch pipelines.path and hot-reload changes into the running engine",
		Ptr:     &c.devAlias,
		Default: c.Cfg.Dev.Enabled,
	})
	return flags
}

func (c *RunCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Run Conduit",
		Long: `Starts the Conduit server and runs the configured pipelines.

With --dev, additionally watches pipelines.path and hot-reloads changes into the running engine as
you save: a processor-only edit applies in place (no restart); a source/destination/topology edit
applies via a labeled graceful restart; a bad edit (parse/validation failure) is reported and never
touches the running pipeline. Every apply --dev drives is authorized by the fact that you ran
--dev and are watching it — it does not set --api.allow-live-restart-apply, which stays
independently gated for the gRPC/HTTP/MCP surface. See
docs/design-documents/20260712-pipeline-dev-hot-reload.md and docs/operations/dev-hot-reload.md.`,
		Example: "conduit run\n" +
			"conduit run --dev\n" +
			"conduit run --dev --pipelines.path ./pipelines --dev.json",
	}
}
