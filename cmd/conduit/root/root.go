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
	"time"

	"github.com/conduitio/conduit/cmd/conduit/internal"
	"github.com/conduitio/conduit/cmd/conduit/root/pipelines"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/ecdysis"
)

var (
	_ ecdysis.CommandWithFlags       = (*RootCommand)(nil)
	_ ecdysis.CommandWithExecute     = (*RootCommand)(nil)
	_ ecdysis.CommandWithDocs        = (*RootCommand)(nil)
	_ ecdysis.CommandWithSubCommands = (*RootCommand)(nil)
)

type RootFlags struct {
	// Global flags -----------------------------------------------------------

	// Conduit configuration file
	ConduitConfigPath string `long:"config.path" usage:"global conduit configuration file" persistent:"true" default:"./conduit.yaml"`

	// Version
	Version bool `long:"version" short:"v" usage:"show current Conduit version" persistent:"true"`

	// Root flags -------------------------------------------------------------

	// Database configuration
	DBType                     string `long:"db.type" usage:"database type; accepts badger,postgres,inmemory,sqlite"`
	DBBadgerPath               string `long:"db.badger.path" usage:"path to badger DB"`
	DBPostgresConnectionString string `long:"db.postgres.connection-string" usage:"postgres connection string, may be a database URL or in PostgreSQL keyword/value format"`
	DBPostgresTable            string `long:"db.postgres.table" usage:"postgres table in which to store data (will be created if it does not exist)"`
	DBSQLitePath               string `long:"db.sqlite.path" usage:"path to sqlite3 DB"`
	DBSQLiteTable              string `long:"db.sqlite.table" usage:"sqlite3 table in which to store data (will be created if it does not exist)"`

	// API configuration
	APIEnabled     bool   `long:"api.enabled" usage:"enable HTTP and gRPC API"`
	APIHTTPAddress string `long:"http.address" usage:"address for serving the HTTP API"`
	APIGRPCAddress string `long:"grpc.address" usage:"address for serving the gRPC API"`

	// Logging configuration
	LogLevel  string `long:"log.level" usage:"sets logging level; accepts debug, info, warn, error, trace"`
	LogFormat string `long:"log.format" usage:"sets the format of the logging; accepts json, cli"`

	// Connectors and Processors paths
	ConnectorsPath string `long:"connectors.path" usage:"path to standalone connectors' directory"`
	ProcessorsPath string `long:"processors.path" usage:"path to standalone processors' directory"`

	// Pipeline configuration
	PipelinesPath                          string        `long:"pipelines.path" usage:"path to the directory that has the yaml pipeline configuration files, or a single pipeline configuration file"`
	PipelinesExitOnDegraded                bool          `long:"pipelines.exit-on-degraded" usage:"exit Conduit if a pipeline enters a degraded state"`
	PipelinesErrorRecoveryMinDelay         time.Duration `long:"pipelines.error-recovery.min-delay" usage:"minimum delay before restart"`
	PipelinesErrorRecoveryMaxDelay         time.Duration `long:"pipelines.error-recovery.max-delay" usage:"maximum delay before restart"`
	PipelinesErrorRecoveryBackoffFactor    int           `long:"pipelines.error-recovery.backoff-factor" usage:"backoff factor applied to the last delay"`
	PipelinesErrorRecoveryMaxRetries       int64         `long:"pipelines.error-recovery.max-retries" usage:"maximum number of retries"`
	PipelinesErrorRecoveryMaxRetriesWindow time.Duration `long:"pipelines.error-recovery.max-retries-window" usage:"amount of time running without any errors after which a pipeline is considered healthy"`

	// Schema registry configuration
	SchemaRegistryType                      string `long:"schema-registry.type" usage:"schema registry type; accepts builtin,confluent"`
	SchemaRegistryConfluentConnectionString string `long:"schema-registry.confluent.connection-string" usage:"confluent schema registry connection string"`

	// Preview features
	PreviewPipelineArchV2 bool `long:"preview.pipeline-arch-v2" usage:"enables experimental pipeline architecture v2 (note that the new architecture currently supports only 1 source and 1 destination per pipeline)"`

	// Development profiling
	DevCPUProfile   string `long:"dev.cpuprofile" usage:"write CPU profile to file"`
	DevMemProfile   string `long:"dev.memprofile" usage:"write memory profile to file"`
	DevBlockProfile string `long:"dev.blockprofile" usage:"write block profile to file"`
}

type RootCommand struct {
	flags RootFlags
	cfg   conduit.Config
}

func (c *RootCommand) updateConfigFromFlags() {
	c.cfg.DB.Type = c.flags.DBType
	c.cfg.DB.Postgres.ConnectionString = c.flags.DBPostgresConnectionString
	c.cfg.DB.Postgres.Table = c.flags.DBPostgresTable
	c.cfg.DB.SQLite.Table = c.flags.DBSQLiteTable

	// Map API configuration
	c.cfg.API.Enabled = c.flags.APIEnabled
	c.cfg.API.HTTP.Address = c.flags.APIHTTPAddress
	c.cfg.API.GRPC.Address = c.flags.APIGRPCAddress

	// Map logging configuration
	c.cfg.Log.Level = c.flags.LogLevel
	c.cfg.Log.Format = c.flags.LogFormat

	// Map pipeline configuration
	c.cfg.Pipelines.ExitOnDegraded = c.flags.PipelinesExitOnDegraded
	c.cfg.Pipelines.ErrorRecovery.MinDelay = c.flags.PipelinesErrorRecoveryMinDelay
	c.cfg.Pipelines.ErrorRecovery.MaxDelay = c.flags.PipelinesErrorRecoveryMaxDelay
	c.cfg.Pipelines.ErrorRecovery.BackoffFactor = c.flags.PipelinesErrorRecoveryBackoffFactor
	c.cfg.Pipelines.ErrorRecovery.MaxRetries = c.flags.PipelinesErrorRecoveryMaxRetries
	c.cfg.Pipelines.ErrorRecovery.MaxRetriesWindow = c.flags.PipelinesErrorRecoveryMaxRetriesWindow

	// Map schema registry configuration
	c.cfg.SchemaRegistry.Type = c.flags.SchemaRegistryType
	c.cfg.SchemaRegistry.Confluent.ConnectionString = c.flags.SchemaRegistryConfluentConnectionString

	// Map preview features
	c.cfg.Preview.PipelineArchV2 = c.flags.PreviewPipelineArchV2

	// Map development profiling
	c.cfg.Dev.CPUProfile = c.flags.DevCPUProfile
	c.cfg.Dev.MemProfile = c.flags.DevMemProfile
	c.cfg.Dev.BlockProfile = c.flags.DevBlockProfile

	// Update paths
	c.cfg.DB.SQLite.Path = c.flags.DBSQLitePath
	c.cfg.DB.Badger.Path = c.flags.DBBadgerPath
	c.cfg.Pipelines.Path = c.flags.PipelinesPath
	c.cfg.Connectors.Path = c.flags.ConnectorsPath
	c.cfg.Processors.Path = c.flags.ProcessorsPath
}

func (c *RootCommand) updateFlagValuesFromConfig() {
	// Map database configuration
	c.flags.DBType = c.cfg.DB.Type
	c.flags.DBPostgresConnectionString = c.cfg.DB.Postgres.ConnectionString
	c.flags.DBPostgresTable = c.cfg.DB.Postgres.Table
	c.flags.DBSQLiteTable = c.cfg.DB.SQLite.Table

	// Map API configuration
	c.flags.APIEnabled = c.cfg.API.Enabled
	c.flags.APIHTTPAddress = c.cfg.API.HTTP.Address
	c.flags.APIGRPCAddress = c.cfg.API.GRPC.Address

	// Map logging configuration
	c.flags.LogLevel = c.cfg.Log.Level
	c.flags.LogFormat = c.cfg.Log.Format

	// Map pipeline configuration
	c.flags.PipelinesExitOnDegraded = c.cfg.Pipelines.ExitOnDegraded
	c.flags.PipelinesErrorRecoveryMinDelay = c.cfg.Pipelines.ErrorRecovery.MinDelay
	c.flags.PipelinesErrorRecoveryMaxDelay = c.cfg.Pipelines.ErrorRecovery.MaxDelay
	c.flags.PipelinesErrorRecoveryBackoffFactor = c.cfg.Pipelines.ErrorRecovery.BackoffFactor
	c.flags.PipelinesErrorRecoveryMaxRetries = c.cfg.Pipelines.ErrorRecovery.MaxRetries
	c.flags.PipelinesErrorRecoveryMaxRetriesWindow = c.cfg.Pipelines.ErrorRecovery.MaxRetriesWindow

	// Map schema registry configuration
	c.flags.SchemaRegistryType = c.cfg.SchemaRegistry.Type
	c.flags.SchemaRegistryConfluentConnectionString = c.cfg.SchemaRegistry.Confluent.ConnectionString

	// Map preview features
	c.flags.PreviewPipelineArchV2 = c.cfg.Preview.PipelineArchV2

	// Map development profiling
	c.flags.DevCPUProfile = c.cfg.Dev.CPUProfile
	c.flags.DevMemProfile = c.cfg.Dev.MemProfile
	c.flags.DevBlockProfile = c.cfg.Dev.BlockProfile

	// Update paths
	c.flags.DBSQLitePath = c.cfg.DB.SQLite.Path
	c.flags.DBBadgerPath = c.cfg.DB.Badger.Path
	c.flags.PipelinesPath = c.cfg.Pipelines.Path
	c.flags.ConnectorsPath = c.cfg.Connectors.Path
	c.flags.ProcessorsPath = c.cfg.Processors.Path
}

func (c *RootCommand) Execute(ctx context.Context) error {
	if c.flags.Version {
		_, _ = fmt.Fprintf(os.Stdout, "%s\n", conduit.Version(true))
		return nil
	}

	// 1. Load conduit configuration file and update general config.
	if err := internal.LoadConfigFromFile(c.flags.ConduitConfigPath, &c.cfg); err != nil {
		return err
	}

	// 2. Load environment variables and update general config.
	if err := internal.LoadConfigFromEnv(&c.cfg); err != nil {
		return err
	}

	// 3. Update the general config from flags.
	c.updateConfigFromFlags()

	// 4. Update flags from global configuration (this will be needed for conduit init)
	c.updateFlagValuesFromConfig()

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
