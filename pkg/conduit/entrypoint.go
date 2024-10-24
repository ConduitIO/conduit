// Copyright © 2023 Meroxa, Inc.
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

package conduit

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/conduitio/conduit/pkg/conduit/internal"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/peterbourgon/ff/v3"
	"github.com/peterbourgon/ff/v3/ffyaml"
)

// Serve is a shortcut for Entrypoint.Serve.
func Serve(cfg Config) {
	e := &Entrypoint{}
	e.Serve(cfg)
}

const (
	exitCodeErr       = 1
	exitCodeInterrupt = 2
)

// Entrypoint provides methods related to the Conduit entrypoint (parsing
// config, managing interrupt signals etc.).
type Entrypoint struct{}

// Serve is the entrypoint for Conduit. It is a convenience function if you want
// to tweak the Conduit CLI and inject different default values or built-in
// plugins while retaining the same flags and exit behavior.
// You can adjust the default values by setting the corresponding field in
// Config. The default config can be retrieved with DefaultConfig.
// The config will be populated with values parsed from:
//   - command line flags (highest priority)
//   - environment variables
//   - config file (lowest priority)
func (e *Entrypoint) Serve(cfg Config) {
	flags := e.Flags(&cfg)
	e.ParseConfig(flags)
	if cfg.cliMode() {
		e.switchToCLI(cfg)
		os.Exit(0)
	}

	if cfg.Log.Format == "cli" {
		_, _ = fmt.Fprintf(os.Stdout, "%s\n", e.Splash())
	}

	runtime, err := NewRuntime(cfg)
	if err != nil {
		e.exitWithError(cerrors.Errorf("failed to set up conduit runtime: %w", err))
	}

	// As per the docs, the signals SIGKILL and SIGSTOP may not be caught by a program
	ctx := e.CancelOnInterrupt(context.Background())
	err = runtime.Run(ctx)
	if err != nil && !cerrors.Is(err, context.Canceled) {
		e.exitWithError(cerrors.Errorf("conduit runtime error: %w", err))
	}
}

// Flags returns a flag set that, when parsed, stores the values in the provided
// config struct.
func (*Entrypoint) Flags(cfg *Config) *flag.FlagSet {
	// TODO extract flags from config struct rather than defining flags manually
	flags := flag.NewFlagSet("conduit", flag.ExitOnError)

	flags.StringVar(&cfg.DB.Type, "db.type", cfg.DB.Type, "database type; accepts badger,postgres,inmemory,sqlite")
	flags.StringVar(&cfg.DB.Badger.Path, "db.badger.path", cfg.DB.Badger.Path, "path to badger DB")
	flags.StringVar(
		&cfg.DB.Postgres.ConnectionString,
		"db.postgres.connection-string",
		cfg.DB.Postgres.ConnectionString,
		"postgres connection string, may be a database URL or in PostgreSQL keyword/value format",
	)
	flags.StringVar(&cfg.DB.Postgres.Table, "db.postgres.table", cfg.DB.Postgres.Table, "postgres table in which to store data (will be created if it does not exist)")
	flags.StringVar(&cfg.DB.SQLite.Path, "db.sqlite.path", cfg.DB.SQLite.Path, "path to sqlite3 DB")
	flags.StringVar(&cfg.DB.SQLite.Table, "db.sqlite.table", cfg.DB.SQLite.Table, "sqlite3 table in which to store data (will be created if it does not exist)")
	flags.BoolVar(&cfg.API.Enabled, "api.enabled", cfg.API.Enabled, "enable HTTP and gRPC API")
	flags.StringVar(&cfg.API.HTTP.Address, "http.address", cfg.API.HTTP.Address, "address for serving the HTTP API")
	flags.StringVar(&cfg.API.GRPC.Address, "grpc.address", cfg.API.GRPC.Address, "address for serving the gRPC API")

	flags.StringVar(&cfg.Log.Level, "log.level", cfg.Log.Level, "sets logging level; accepts debug, info, warn, error, trace")
	flags.StringVar(&cfg.Log.Format, "log.format", cfg.Log.Format, "sets the format of the logging; accepts json, cli")

	flags.StringVar(&cfg.Connectors.Path, "connectors.path", cfg.Connectors.Path, "path to standalone connectors' directory")
	flags.StringVar(&cfg.Processors.Path, "processors.path", cfg.Processors.Path, "path to standalone processors' directory")

	// Pipeline configuration
	flags.StringVar(
		&cfg.Pipelines.Path,
		"pipelines.path",
		cfg.Pipelines.Path,
		"path to the directory that has the yaml pipeline configuration files, or a single pipeline configuration file",
	)

	// Deprecated: use `pipelines.exit-on-degraded` instead
	// Note: If both `pipeline.exit-on-error` and `pipeline.exit-on-degraded` are set, `pipeline.exit-on-degraded` will take precedence
	flags.BoolVar(
		&cfg.Pipelines.ExitOnDegraded,
		"pipelines.exit-on-error",
		cfg.Pipelines.ExitOnDegraded,
		"Deprecated: use `exit-on-degraded` instead.\nexit Conduit if a pipeline experiences an error while running",
	)

	flags.BoolVar(
		&cfg.Pipelines.ExitOnDegraded,
		"pipelines.exit-on-degraded",
		cfg.Pipelines.ExitOnDegraded,
		"exit Conduit if a pipeline enters a degraded state",
	)

	flags.DurationVar(
		&cfg.Pipelines.ErrorRecovery.MinDelay,
		"pipelines.error-recovery.min-delay",
		cfg.Pipelines.ErrorRecovery.MinDelay,
		"minimum delay before restart",
	)
	flags.DurationVar(
		&cfg.Pipelines.ErrorRecovery.MaxDelay,
		"pipelines.error-recovery.max-delay",
		cfg.Pipelines.ErrorRecovery.MaxDelay,
		"maximum delay before restart",
	)
	flags.IntVar(
		&cfg.Pipelines.ErrorRecovery.BackoffFactor,
		"pipelines.error-recovery.backoff-factor",
		cfg.Pipelines.ErrorRecovery.BackoffFactor,
		"backoff factor applied to the last delay",
	)
	flags.Int64Var(
		&cfg.Pipelines.ErrorRecovery.MaxRetries,
		"pipelines.error-recovery.max-retries",
		cfg.Pipelines.ErrorRecovery.MaxRetries,
		"maximum number of retries",
	)
	flags.DurationVar(
		&cfg.Pipelines.ErrorRecovery.MaxRetriesWindow,
		"pipelines.error-recovery.max-retries-window",
		cfg.Pipelines.ErrorRecovery.MaxRetriesWindow,
		"amount of time running without any errors after which a pipeline is considered healthy",
	)

	flags.StringVar(&cfg.SchemaRegistry.Type, "schema-registry.type", cfg.SchemaRegistry.Type, "schema registry type; accepts builtin,confluent")
	flags.StringVar(&cfg.SchemaRegistry.Confluent.ConnectionString, "schema-registry.confluent.connection-string", cfg.SchemaRegistry.Confluent.ConnectionString, "confluent schema registry connection string")

	// NB: flags with prefix dev.* are hidden from help output by default, they only show up using '-dev -help'
	showDevHelp := flags.Bool("dev", false, "used together with the dev flag it shows dev flags")
	flags.StringVar(&cfg.dev.cpuprofile, "dev.cpuprofile", "", "write cpu profile to file")
	flags.StringVar(&cfg.dev.memprofile, "dev.memprofile", "", "write memory profile to file")
	flags.StringVar(&cfg.dev.blockprofile, "dev.blockprofile", "", "write block profile to file")

	flags.BoolVar(&cfg.BuildPipeline.Enabled, "build-pipeline", false, "build a new pipeline")
	flags.StringVar(&cfg.BuildPipeline.Source, "source", "", "source connector (only used with --build-pipeline)")
	flags.StringVar(&cfg.BuildPipeline.Destination, "destination", "", "destination connector (only used with --build-pipeline)")
	flags.StringVar(&cfg.BuildPipeline.OutPath, "out-path", "", "path where the pipeline will be written to (only used with --build-pipeline)")

	// Deprecated flags that are hidden from help output
	deprecatedFlags := map[string]bool{
		"pipelines.exit-on-error": true,
	}

	// show user or dev flags
	flags.Usage = func() {
		tmpFlags := flag.NewFlagSet("conduit", flag.ExitOnError)
		flags.VisitAll(func(f *flag.Flag) {
			if f.Name == "dev" || strings.HasPrefix(f.Name, "dev.") != *showDevHelp || deprecatedFlags[f.Name] {
				return // hide flag from output
			}
			// reset value to its default, to ensure default is shown correctly
			_ = f.Value.Set(f.DefValue)
			tmpFlags.Var(f.Value, f.Name, f.Usage)
		})
		tmpFlags.Usage()
	}

	return flags
}

func (e *Entrypoint) ParseConfig(flags *flag.FlagSet) {
	_ = flags.String("config", "conduit.yaml", "global config file")
	version := flags.Bool("version", false, "prints current Conduit version")

	// flags is set up to exit on error, we can safely ignore the error
	err := ff.Parse(flags, os.Args[1:],
		ff.WithEnvVarPrefix("CONDUIT"),
		ff.WithConfigFileFlag("config"),
		ff.WithConfigFileParser(ffyaml.Parser),
		ff.WithAllowMissingConfigFile(true),
	)
	if err != nil {
		e.exitWithError(err)
	}

	// check if the -version flag is set
	if *version {
		_, _ = fmt.Fprintf(os.Stdout, "%s\n", Version(true))
		os.Exit(0)
	}
}

// CancelOnInterrupt returns a context that is canceled when the interrupt
// signal is received.
// * After the first signal the function will continue to listen
// * On the second signal executes a hard exit, without waiting for a graceful
// shutdown.
func (*Entrypoint) CancelOnInterrupt(ctx context.Context) context.Context {
	ctx, cancel := context.WithCancel(ctx)
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt)
	go func() {
		select {
		case <-signalChan: // first interrupt signal
			cancel()
		case <-ctx.Done():
		}
		<-signalChan // second interrupt signal
		os.Exit(exitCodeInterrupt)
	}()

	return ctx
}

func (*Entrypoint) exitWithError(err error) {
	_, _ = fmt.Fprintf(os.Stderr, "error: %+v\n", err)
	os.Exit(exitCodeErr)
}

func (*Entrypoint) exitWithErrorNoStack(err error) {
	_, _ = fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(exitCodeErr)
}

func (*Entrypoint) Splash() string {
	const splash = "" +
		"             ....            \n" +
		"         .::::::::::.        \n" +
		"       .:::::‘‘‘‘:::::.      \n" +
		"      .::::        ::::.     \n" +
		" .::::::::          ::::::::.\n" +
		" `::::::::          ::::::::‘\n" +
		"      `::::        ::::‘     \n" +
		"       `:::::....:::::‘      \n" +
		"         `::::::::::‘        Conduit %s\n" +
		"             ‘‘‘‘            "
	return fmt.Sprintf(splash, Version(true))
}

func (e *Entrypoint) switchToCLI(cfg Config) {
	if err := cfg.validateCLI(); err != nil {
		e.exitWithErrorNoStack(cerrors.Errorf("invalid configuration: %w", err))
	}

	if cfg.BuildPipeline.Enabled {
		pb := internal.PipelineBuilder{
			Source:      cfg.BuildPipeline.Source,
			Destination: cfg.BuildPipeline.Destination,
			OutPath:     cfg.BuildPipeline.OutPath,
		}

		err := pb.Build()
		if err != nil {
			e.exitWithErrorNoStack(cerrors.Errorf("failed to build pipeline: %w", err))
		}
	}
}
