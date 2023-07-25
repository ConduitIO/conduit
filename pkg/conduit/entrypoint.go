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

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/peterbourgon/ff/v3"
	"github.com/peterbourgon/ff/v3/ffyaml"
)

// Serve is the entrypoint for Conduit. It is a convenience function if you want
// to tweak the Conduit CLI and inject different default values or built-in
// plugins while retaining the same flags and exit behavior.
// You can adjust the default values by setting the corresponding field in
// Config. The default config can be retrieved with DefaultConfig.
// The config will be populated with values parsed from:
//   - command line flags (highest priority)
//   - environment variables
//   - config file (lowest priority)
func Serve(cfg Config) {
	cfg = parseConfig(cfg)
	if cfg.Log.Format == "cli" {
		_, _ = fmt.Fprintf(os.Stdout, "%s\n", Splash())
	}

	runtime, err := NewRuntime(cfg)
	if err != nil {
		exitWithError(cerrors.Errorf("failed to set up conduit runtime: %w", err))
	}

	// As per the docs, the signals SIGKILL and SIGSTOP may not be caught by a program
	ctx := cancelOnInterrupt(context.Background())
	err = runtime.Run(ctx)
	if err != nil && !cerrors.Is(err, context.Canceled) {
		exitWithError(cerrors.Errorf("conduit runtime error: %w", err))
	}
}

const (
	exitCodeErr       = 1
	exitCodeInterrupt = 2
)

func parseConfig(cfg Config) Config {
	// TODO extract flags from config struct rather than defining flags manually
	// TODO allow parsing config from a file or from env variables
	flags := flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	flags.StringVar(&cfg.DB.Type, "db.type", cfg.DB.Type, "database type; accepts badger,postgres,inmemory")
	flags.StringVar(&cfg.DB.Badger.Path, "db.badger.path", cfg.DB.Badger.Path, "path to badger DB")
	flags.StringVar(&cfg.DB.Postgres.ConnectionString, "db.postgres.connection-string", cfg.DB.Postgres.ConnectionString, "postgres connection string")
	flags.StringVar(&cfg.DB.Postgres.Table, "db.postgres.table", cfg.DB.Postgres.Table, "postgres table in which to store data (will be created if it does not exist)")

	flags.StringVar(&cfg.HTTP.Address, "http.address", cfg.HTTP.Address, "address for serving the HTTP API")

	flags.StringVar(&cfg.GRPC.Address, "grpc.address", cfg.GRPC.Address, "address for serving the GRPC API")

	flags.StringVar(&cfg.Log.Level, "log.level", cfg.Log.Level, "sets logging level; accepts debug, info, warn, error, trace")
	flags.StringVar(&cfg.Log.Format, "log.format", cfg.Log.Format, "sets the format of the logging; accepts json, cli")

	flags.StringVar(&cfg.Connectors.Path, "connectors.path", cfg.Connectors.Path, "path to standalone connectors directory")

	flags.StringVar(&cfg.Pipelines.Path, "pipelines.path", cfg.Pipelines.Path, "path to the directory that has the yaml pipeline configuration files, or a single pipeline configuration file")
	flags.BoolVar(&cfg.Pipelines.ExitOnError, "pipelines.exit-on-error", cfg.Pipelines.ExitOnError, "exit Conduit if a pipeline experiences an error while running")

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
		exitWithError(err)
	}

	// check if the -version flag is set
	if *version {
		_, _ = fmt.Fprintf(os.Stdout, "%s\n", Version(true))
		os.Exit(0)
	}

	return cfg
}

// cancelOnInterrupt returns a context that is canceled when the interrupt
// signal is received.
// * After the first signal the function will continue to listen
// * On the second signal executes a hard exit, without waiting for a graceful
// shutdown.
func cancelOnInterrupt(ctx context.Context) context.Context {
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

func exitWithError(err error) {
	_, _ = fmt.Fprintf(os.Stderr, "error: %+v\n", err)
	os.Exit(exitCodeErr)
}
