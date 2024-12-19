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

package root

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/conduitio/conduit/cmd/conduit/root/pipelines"
	"github.com/conduitio/conduit/cmd/conduit/root/version"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/ecdysis"
)

var (
	_ ecdysis.CommandWithFlags       = (*RootCommand)(nil)
	_ ecdysis.CommandWithExecute     = (*RootCommand)(nil)
	_ ecdysis.CommandWithDocs        = (*RootCommand)(nil)
	_ ecdysis.CommandWithSubCommands = (*RootCommand)(nil)
	_ ecdysis.CommandWithConfig      = (*RootCommand)(nil)
)

type RootFlags struct {
	Version bool `long:"version" short:"v" usage:"show the current Conduit version"`
	conduit.Config
}

type RootCommand struct {
	flags RootFlags
	cfg   conduit.Config
}

func (c *RootCommand) Execute(_ context.Context) error {
	if c.flags.Version {
		_, _ = fmt.Fprintf(os.Stdout, "%s\n", conduit.Version(true))
		return nil
	}

	e := &conduit.Entrypoint{}
	e.Serve(c.cfg)
	return nil
}

func (c *RootCommand) Config() ecdysis.Config {
	path := filepath.Dir(c.flags.ConduitCfgPath)

	return ecdysis.Config{
		EnvPrefix:     "CONDUIT",
		Parsed:        &c.cfg,
		Path:          c.flags.ConduitCfgPath,
		DefaultValues: conduit.DefaultConfigWithBasePath(path),
	}
}

func (c *RootCommand) Usage() string { return "conduit" }

func (c *RootCommand) Flags() []ecdysis.Flag {
	flags := ecdysis.BuildFlags(&c.flags)

	currentPath, err := os.Getwd()
	if err != nil {
		panic(cerrors.Errorf("failed to get current working directory: %w", err))
	}

	c.cfg = conduit.DefaultConfigWithBasePath(currentPath)
	flags.SetDefault("config.path", c.cfg.ConduitCfgPath)
	flags.SetDefault("db.type", c.cfg.DB.Type)
	flags.SetDefault("db.badger.path", c.cfg.DB.Badger.Path)
	flags.SetDefault("db.postgres.connection-string", c.cfg.DB.Postgres.ConnectionString)
	flags.SetDefault("db.postgres.table", c.cfg.DB.Postgres.Table)
	flags.SetDefault("db.sqlite.path", c.cfg.DB.SQLite.Path)
	flags.SetDefault("db.sqlite.table", c.cfg.DB.SQLite.Table)
	flags.SetDefault("api.enabled", c.cfg.API.Enabled)
	flags.SetDefault("http.address", c.cfg.API.HTTP.Address)
	flags.SetDefault("grpc.address", c.cfg.API.GRPC.Address)
	flags.SetDefault("log.level", c.cfg.Log.Level)
	flags.SetDefault("log.format", c.cfg.Log.Format)
	flags.SetDefault("connectors.path", c.cfg.Connectors.Path)
	flags.SetDefault("processors.path", c.cfg.Processors.Path)
	flags.SetDefault("pipelines.path", c.cfg.Pipelines.Path)
	flags.SetDefault("pipelines.exit-on-degraded", c.cfg.Pipelines.ExitOnDegraded)
	flags.SetDefault("pipelines.error-recovery.min-delay", c.cfg.Pipelines.ErrorRecovery.MinDelay)
	flags.SetDefault("pipelines.error-recovery.max-delay", c.cfg.Pipelines.ErrorRecovery.MaxDelay)
	flags.SetDefault("pipelines.error-recovery.backoff-factor", c.cfg.Pipelines.ErrorRecovery.BackoffFactor)
	flags.SetDefault("pipelines.error-recovery.max-retries", c.cfg.Pipelines.ErrorRecovery.MaxRetries)
	flags.SetDefault("pipelines.error-recovery.max-retries-window", c.cfg.Pipelines.ErrorRecovery.MaxRetriesWindow)
	flags.SetDefault("schema-registry.type", c.cfg.SchemaRegistry.Type)
	flags.SetDefault("schema-registry.confluent.connection-string", c.cfg.SchemaRegistry.Confluent.ConnectionString)
	flags.SetDefault("preview.pipeline-arch-v2", c.cfg.Preview.PipelineArchV2)
	return flags
}

func (c *RootCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Conduit CLI",
		Long:  `Conduit CLI is a command-line that helps you interact with and manage Conduit.`,
	}
}

func (c *RootCommand) SubCommands() []ecdysis.Command {
	return []ecdysis.Command{
		&ConfigCommand{rootCmd: c},
		&InitCommand{cfg: &c.cfg},
		&version.VersionCommand{},
		&pipelines.PipelinesCommand{},
	}
}
