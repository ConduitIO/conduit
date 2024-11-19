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
	"time"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/ecdysis"
)

var (
	_ ecdysis.CommandWithFlags       = (*RootCommand)(nil)
	_ ecdysis.CommandWithExecute     = (*RootCommand)(nil)
	_ ecdysis.CommandWithDocs        = (*RootCommand)(nil)
	_ ecdysis.CommandWithSubCommands = (*RootCommand)(nil)
)

// TODO: Check which ones are really global from the design document
type RootFlags struct {
	// Database configuration
	DBType                     string `long:"db.type" usage:"database type; accepts badger,postgres,inmemory,sqlite" persistent:"true"`
	DBBadgerPath               string `long:"db.badger.path" usage:"path to badger DB" persistent:"true"`
	DBPostgresConnectionString string `long:"db.postgres.connection-string" usage:"postgres connection string, may be a database URL or in PostgreSQL keyword/value format" persistent:"true"`
	DBPostgresTable            string `long:"db.postgres.table" usage:"postgres table in which to store data (will be created if it does not exist)" persistent:"true"`
	DBSQLitePath               string `long:"db.sqlite.path" usage:"path to sqlite3 DB" persistent:"true"`
	DBSQLiteTable              string `long:"db.sqlite.table" usage:"sqlite3 table in which to store data (will be created if it does not exist)" persistent:"true"`

	// API configuration
	APIEnabled     bool   `long:"api.enabled" usage:"enable HTTP and gRPC API" persistent:"true"`
	APIHTTPAddress string `long:"http.address" usage:"address for serving the HTTP API" persistent:"true"`
	APIGRPCAddress string `long:"grpc.address" usage:"address for serving the gRPC API" persistent:"true"`

	// Logging configuration
	LogLevel  string `long:"log.level" usage:"sets logging level; accepts debug, info, warn, error, trace" persistent:"true"`
	LogFormat string `long:"log.format" usage:"sets the format of the logging; accepts json, cli" persistent:"true"`

	// Connectors and Processors paths
	ConnectorsPath string `long:"connectors.path" usage:"path to standalone connectors' directory" persistent:"true"`
	ProcessorsPath string `long:"processors.path" usage:"path to standalone processors' directory" persistent:"true"`

	// Pipeline configuration
	PipelinesPath                          string        `long:"pipelines.path" usage:"path to the directory that has the yaml pipeline configuration files, or a single pipeline configuration file" persistent:"true"`
	PipelinesExitOnDegraded                bool          `long:"pipelines.exit-on-degraded" usage:"exit Conduit if a pipeline enters a degraded state" persistent:"true"`
	PipelinesErrorRecoveryMinDelay         time.Duration `long:"pipelines.error-recovery.min-delay" usage:"minimum delay before restart" persistent:"true"`
	PipelinesErrorRecoveryMaxDelay         time.Duration `long:"pipelines.error-recovery.max-delay" usage:"maximum delay before restart" persistent:"true"`
	PipelinesErrorRecoveryBackoffFactor    int           `long:"pipelines.error-recovery.backoff-factor" usage:"backoff factor applied to the last delay" persistent:"true"`
	PipelinesErrorRecoveryMaxRetries       int64         `long:"pipelines.error-recovery.max-retries" usage:"maximum number of retries" persistent:"true"`
	PipelinesErrorRecoveryMaxRetriesWindow time.Duration `long:"pipelines.error-recovery.max-retries-window" usage:"amount of time running without any errors after which a pipeline is considered healthy" persistent:"true"`

	// Schema registry configuration
	SchemaRegistryType                      string `long:"schema-registry.type" usage:"schema registry type; accepts builtin,confluent" persistent:"true"`
	SchemaRegistryConfluentConnectionString string `long:"schema-registry.confluent.connection-string" usage:"confluent schema registry connection string" persistent:"true"`

	// Preview features
	PreviewPipelineArchV2 bool `long:"preview.pipeline-arch-v2" usage:"enables experimental pipeline architecture v2 (note that the new architecture currently supports only 1 source and 1 destination per pipeline)" persistent:"true"`

	// Development profiling
	DevCPUProfile   string `long:"dev.cpuprofile" usage:"write CPU profile to file" persistent:"true"`
	DevMemProfile   string `long:"dev.memprofile" usage:"write memory profile to file" persistent:"true"`
	DevBlockProfile string `long:"dev.blockprofile" usage:"write block profile to file" persistent:"true"`

	// Version
	Version bool `long:"version" short:"v" usage:"show version" persistent:"true"`
}

type RootCommand struct {
	flags RootFlags
	cfg   conduit.Config
}

func (c *RootCommand) Execute(ctx context.Context) error {
	if c.flags.Version {
		// TODO: use the logger instead
		fmt.Print(conduit.Version(true))
		return nil
	}

	e := &conduit.Entrypoint{}
	e.Serve(c.cfg)

	return nil
}

func (c *RootCommand) Usage() string { return "conduit" }
func (c *RootCommand) Flags() []ecdysis.Flag {
	flags := ecdysis.BuildFlags(&c.flags)

	c.cfg = conduit.DefaultConfig()

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
		// inject root flags in sub-command
	}
}
