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
	"strings"

	"github.com/conduitio/conduit/cmd/conduit/root/pipelines"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/ecdysis"
	"github.com/spf13/viper"
)

var (
	_ ecdysis.CommandWithFlags       = (*RootCommand)(nil)
	_ ecdysis.CommandWithExecute     = (*RootCommand)(nil)
	_ ecdysis.CommandWithDocs        = (*RootCommand)(nil)
	_ ecdysis.CommandWithSubCommands = (*RootCommand)(nil)
)

const ConduitPrefix = "CONDUIT"

type RootFlags struct {
	// Global flags -----------------------------------------------------------

	// Conduit configuration file
	ConduitConfigPath string `long:"config.path" usage:"global conduit configuration file" persistent:"true" default:"./conduit.yaml"`

	// Version
	Version bool `long:"version" short:"v" usage:"show current Conduit version" persistent:"true"`

	conduit.Config
}

type RootCommand struct {
	flags RootFlags
	cfg   conduit.Config
}

func (c *RootCommand) updateFlagValuesFromConfig() {
	// Map database configuration
	c.flags.DB.Type = c.cfg.DB.Type
	c.flags.DB.Postgres.ConnectionString = c.cfg.DB.Postgres.ConnectionString
	c.flags.DB.Postgres.Table = c.cfg.DB.Postgres.Table
	c.flags.DB.SQLite.Table = c.cfg.DB.SQLite.Table

	// Map API configuration
	c.flags.API.Enabled = c.cfg.API.Enabled
	c.flags.API.HTTP.Address = c.cfg.API.HTTP.Address
	c.flags.API.GRPC.Address = c.cfg.API.GRPC.Address

	// Map logging configuration
	c.flags.Log.Level = c.cfg.Log.Level
	c.flags.Log.Format = c.cfg.Log.Format

	// Map pipeline configuration
	c.flags.Pipelines.ExitOnDegraded = c.cfg.Pipelines.ExitOnDegraded
	c.flags.Pipelines.ErrorRecovery.MinDelay = c.cfg.Pipelines.ErrorRecovery.MinDelay
	c.flags.Pipelines.ErrorRecovery.MaxDelay = c.cfg.Pipelines.ErrorRecovery.MaxDelay
	c.flags.Pipelines.ErrorRecovery.BackoffFactor = c.cfg.Pipelines.ErrorRecovery.BackoffFactor
	c.flags.Pipelines.ErrorRecovery.MaxRetries = c.cfg.Pipelines.ErrorRecovery.MaxRetries
	c.flags.Pipelines.ErrorRecovery.MaxRetriesWindow = c.cfg.Pipelines.ErrorRecovery.MaxRetriesWindow

	// Map schema registry configuration
	c.flags.SchemaRegistry.Type = c.cfg.SchemaRegistry.Type
	c.flags.SchemaRegistry.Confluent.ConnectionString = c.cfg.SchemaRegistry.Confluent.ConnectionString

	// Map preview features
	c.flags.Preview.PipelineArchV2 = c.cfg.Preview.PipelineArchV2

	// Map development profiling
	c.flags.Dev.CPUProfile = c.cfg.Dev.CPUProfile
	c.flags.Dev.MemProfile = c.cfg.Dev.MemProfile
	c.flags.Dev.BlockProfile = c.cfg.Dev.BlockProfile

	// Update paths
	c.flags.DB.SQLite.Path = c.cfg.DB.SQLite.Path
	c.flags.DB.Badger.Path = c.cfg.DB.Badger.Path
	c.flags.Pipelines.Path = c.cfg.Pipelines.Path
	c.flags.Connectors.Path = c.cfg.Connectors.Path
	c.flags.Processors.Path = c.cfg.Processors.Path
}

func (c *RootCommand) LoadConfig() error {
	v := viper.New()

	// Set default values
	v.SetDefault("config.path", c.flags.ConduitConfigPath)
	v.SetDefault("db.type", c.flags.DB.Type)
	v.SetDefault("api.enabled", c.flags.API.Enabled)
	v.SetDefault("log.level", c.flags.Log.Level)
	v.SetDefault("log.format", c.flags.Log.Format)
	v.SetDefault("connectors.path", c.flags.Connectors.Path)
	v.SetDefault("processors.path", c.flags.Processors.Path)
	v.SetDefault("pipelines.path", c.flags.Pipelines.Path)
	v.SetDefault("pipelines.exit-on-degraded", c.flags.Pipelines.ExitOnDegraded)
	v.SetDefault("pipelines.error-recovery.min-delay", c.flags.Pipelines.ErrorRecovery.MinDelay)
	v.SetDefault("pipelines.error-recovery.max-delay", c.flags.Pipelines.ErrorRecovery.MaxDelay)
	v.SetDefault("pipelines.error-recovery.backoff-factor", c.flags.Pipelines.ErrorRecovery.BackoffFactor)
	v.SetDefault("pipelines.error-recovery.max-retries", c.flags.Pipelines.ErrorRecovery.MaxRetries)
	v.SetDefault("pipelines.error-recovery.max-retries-window", c.flags.Pipelines.ErrorRecovery.MaxRetriesWindow)
	v.SetDefault("schema-registry.type", c.flags.SchemaRegistry.Type)
	v.SetDefault("schema-registry.confluent.connection-string", c.flags.SchemaRegistry.Confluent.ConnectionString)
	v.SetDefault("preview.pipeline-arch-v2", c.flags.Preview.PipelineArchV2)
	v.SetDefault("dev.cpuprofile", c.flags.Dev.CPUProfile)
	v.SetDefault("dev.memprofile", c.flags.Dev.MemProfile)
	v.SetDefault("dev.blockprofile", c.flags.Dev.BlockProfile)

	// Read configuration from file
	v.SetConfigFile(c.flags.ConduitConfigPath)
	if err := v.ReadInConfig(); err != nil {
		//return fmt.Errorf("error reading config file: %w", err)
	}

	// Set environment variable prefix and automatic mapping
	v.SetEnvPrefix(ConduitPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Bind flags to Viper
	v.BindEnv("db.type")
	v.BindEnv("db.badger.path")
	v.BindEnv("db.postgres.connection-string")
	v.BindEnv("db.postgres.table")
	v.BindEnv("db.sqlite.path")
	v.BindEnv("db.sqlite.table")
	v.BindEnv("api.enabled")
	v.BindEnv("http.address")
	v.BindEnv("grpc.address")
	v.BindEnv("log.level")
	v.BindEnv("log.format")
	v.BindEnv("connectors.path")
	v.BindEnv("processors.path")
	v.BindEnv("pipelines.path")
	v.BindEnv("pipelines.exit-on-degraded")
	v.BindEnv("pipelines.error-recovery.min-delay")
	v.BindEnv("pipelines.error-recovery.max-delay")
	v.BindEnv("pipelines.error-recovery.backoff-factor")
	v.BindEnv("pipelines.error-recovery.max-retries")
	v.BindEnv("pipelines.error-recovery.max-retries-window")
	v.BindEnv("schema-registry.type")
	v.BindEnv("schema-registry.confluent.connection-string")
	v.BindEnv("preview.pipeline-arch-v2")
	v.BindEnv("dev.cpuprofile")
	v.BindEnv("dev.memprofile")
	v.BindEnv("dev.blockprofile")

	// Unmarshal into the configuration struct
	if err := v.Unmarshal(&c.cfg); err != nil {
		return fmt.Errorf("unable to decode into struct: %w", err)
	}

	return nil
}

func (c *RootCommand) updateConfiguration() error {
	// 1. Load conduit configuration file and update general config.
	if err := c.LoadConfig(); err != nil {
		return err
	}

	// 4. Update flags from global configuration (this will be needed for conduit init)
	c.updateFlagValuesFromConfig()

	return nil
}

func (c *RootCommand) Execute(_ context.Context) error {
	if c.flags.Version {
		_, _ = fmt.Fprintf(os.Stdout, "%s\n", conduit.Version(true))
		return nil
	}

	if err := c.updateConfiguration(); err != nil {
		return err
	}

	e := &conduit.Entrypoint{}
	e.Serve(c.cfg)
	return nil
}

func (c *RootCommand) Usage() string { return "conduit" }
func (c *RootCommand) Flags() []ecdysis.Flag {
	flags := ecdysis.BuildFlags(&c.flags)

	currentPath, err := os.Getwd()
	if err != nil {
		panic(cerrors.Errorf("failed to get current working directory: %w", err))
	}
	c.cfg = conduit.DefaultConfigWithBasePath(currentPath)

	conduitConfigPath := filepath.Join(currentPath, "conduit.yaml")
	flags.SetDefault("config.path", conduitConfigPath)
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
		&InitCommand{rootFlags: &c.flags},
		&pipelines.PipelinesCommand{},
	}
}
