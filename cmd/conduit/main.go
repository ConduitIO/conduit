// Copyright © 2022 Meroxa, Inc.
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

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

const (
	exitCodeErr       = 1
	exitCodeInterrupt = 2
)

func main() {
	cfg := parseConfig()
	runtime, err := conduit.NewRuntime(cfg)
	if err != nil {
		exitWithError(cerrors.Errorf("failed to setup conduit runtime: %w", err))
	}

	_, _ = fmt.Fprintf(os.Stdout, "%s\n", conduit.Splash())

	// As per the docs, the signals SIGKILL and SIGSTOP may not be caught by a program
	ctx := cancelOnInterrupt(context.Background())
	err = runtime.Run(ctx)
	if err != nil && !cerrors.Is(err, context.Canceled) {
		exitWithError(cerrors.Errorf("conduit runtime error: %w", err))
	}
}

func parseConfig() conduit.Config {
	// TODO extract flags from config struct rather than defining flags manually
	// TODO allow parsing config from a file or from env variables
	flags := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	var (
		dbType                     = flags.String("db.type", "badger", "database type; accepts badger,postgres,inmemory")
		dbBadgerPath               = flags.String("db.badger.path", "conduit.db", "path to badger DB")
		dbPostgresConnectionString = flags.String("db.postgres.connection-string", "", "postgres connection string")
		dbPostgresTable            = flags.String("db.postgres.table", "conduit_kv_store", "postgres table in which to store data (will be created if it does not exist)")

		grpcAddress = flags.String("grpc.address", ":8084", "address for serving the GRPC API")

		httpAddress = flags.String("http.address", ":8080", "address for serving the HTTP API")

		version = flags.Bool("version", false, "prints current Conduit version")
	)

	// flags is set up to exit on error, we can safely ignore the error
	_ = flags.Parse(os.Args[1:])

	// check if the -version flag is set
	if *version {
		_, _ = fmt.Fprintf(os.Stdout, "%s\n", conduit.Version(true))
		os.Exit(0)
	}

	var cfg conduit.Config
	cfg.DB.Type = stringPtrToVal(dbType)
	cfg.DB.Badger.Path = stringPtrToVal(dbBadgerPath)
	cfg.DB.Postgres.ConnectionString = stringPtrToVal(dbPostgresConnectionString)
	cfg.DB.Postgres.Table = stringPtrToVal(dbPostgresTable)
	cfg.GRPC.Address = stringPtrToVal(grpcAddress)
	cfg.HTTP.Address = stringPtrToVal(httpAddress)

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

func stringPtrToVal(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
