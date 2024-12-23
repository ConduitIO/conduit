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

type RunFlags struct {
	conduit.Config
}

type RunCommand struct {
	flags RunFlags
	Cfg   conduit.Config
}

func (c *RunCommand) Execute(_ context.Context) error {
	e := &conduit.Entrypoint{}
	e.Serve(c.Cfg)
	return nil
}

func (c *RunCommand) Config() ecdysis.Config {
	path := filepath.Dir(c.flags.ConduitCfgPath)

	return ecdysis.Config{
		EnvPrefix:     "CONDUIT",
		Parsed:        &c.Cfg,
		Path:          c.flags.ConduitCfgPath,
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
	flags.SetDefault("config.path", c.Cfg.ConduitCfgPath)
	flags.SetDefault("db.type", c.Cfg.DB.Type)
	flags.SetDefault("db.badger.path", c.Cfg.DB.Badger.Path)
	flags.SetDefault("db.postgres.connection-string", c.Cfg.DB.Postgres.ConnectionString)
	flags.SetDefault("db.postgres.table", c.Cfg.DB.Postgres.Table)
	flags.SetDefault("db.sqlite.path", c.Cfg.DB.SQLite.Path)
	flags.SetDefault("db.sqlite.table", c.Cfg.DB.SQLite.Table)
	flags.SetDefault("api.enabled", c.Cfg.API.Enabled)
	flags.SetDefault("http.address", c.Cfg.API.HTTP.Address)
	flags.SetDefault("grpc.address", c.Cfg.API.GRPC.Address)
	flags.SetDefault("log.level", c.Cfg.Log.Level)
	flags.SetDefault("log.format", c.Cfg.Log.Format)
	flags.SetDefault("connectors.path", c.Cfg.Connectors.Path)
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
	return flags
}

func (c *RunCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Run Conduit",
		Long:  `Starts the Conduit server and runs the configured pipelines.`,
	}
}
