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
	"testing"

	"github.com/conduitio/ecdysis"
	"github.com/matryer/is"
	"github.com/spf13/pflag"
)

func TestRunCommandFlags(t *testing.T) {
	is := is.New(t)

	expectedFlags := []struct {
		longName   string
		shortName  string
		usage      string
		persistent bool
	}{
		{longName: "config.path", usage: "global conduit configuration file"},
		{longName: "db.type", usage: "database type; accepts badger,postgres,inmemory,sqlite"},
		{longName: "db.badger.path", usage: "path to badger DB"},
		{longName: "db.postgres.connection-string", usage: "postgres connection string, may be a database URL or in PostgreSQL keyword/value format"},
		{longName: "db.postgres.table", usage: "postgres table in which to store data (will be created if it does not exist)"},
		{longName: "db.sqlite.path", usage: "path to sqlite3 DB"},
		{longName: "db.sqlite.table", usage: "sqlite3 table in which to store data (will be created if it does not exist)"},
		{longName: "api.enabled", usage: "enable HTTP and gRPC API"},
		{longName: "http.address", usage: "address for serving the HTTP API"},
		{longName: "grpc.address", usage: "address for serving the gRPC API"},
		{longName: "log.level", usage: "sets logging level; accepts debug, info, warn, error, trace"},
		{longName: "log.format", usage: "sets the format of the logging; accepts json, cli"},
		{longName: "connectors.path", usage: "path to standalone connectors' directory"},
		{longName: "processors.path", usage: "path to standalone processors' directory"},
		{longName: "pipelines.path", usage: "path to pipelines' directory"},
		{longName: "pipelines.exit-on-degraded", usage: "exit Conduit if a pipeline is degraded"},
		{longName: "pipelines.error-recovery.min-delay", usage: "minimum delay before restart"},
		{longName: "pipelines.error-recovery.max-delay", usage: "maximum delay before restart"},
		{longName: "pipelines.error-recovery.backoff-factor", usage: "backoff factor applied to the last delay"},
		{longName: "pipelines.error-recovery.max-retries", usage: "maximum number of retries"},
		{longName: "pipelines.error-recovery.max-retries-window", usage: "amount of time running without any errors after which a pipeline is considered healthy"},
		{longName: "schema-registry.type", usage: "schema registry type; accepts builtin,confluent"},
		{longName: "schema-registry.confluent.connection-string", usage: "confluent schema registry connection string"},
		{longName: "preview.pipeline-arch-v2", usage: "enables experimental pipeline architecture v2 (note that the new architecture currently supports only 1 source and 1 destination per pipeline)"},
		{longName: "dev.cpuprofile", usage: "write CPU profile to file"},
		{longName: "dev.memprofile", usage: "write memory profile to file"},
		{longName: "dev.blockprofile", usage: "write block profile to file"},
	}

	e := ecdysis.New()
	c := e.MustBuildCobraCommand(&RunCommand{})

	persistentFlags := c.PersistentFlags()
	cmdFlags := c.Flags()

	for _, f := range expectedFlags {
		var cf *pflag.Flag

		if f.persistent {
			cf = persistentFlags.Lookup(f.longName)
		} else {
			cf = cmdFlags.Lookup(f.longName)
		}
		is.True(cf != nil)
		is.Equal(f.longName, cf.Name)
		is.Equal(f.shortName, cf.Shorthand)
		is.Equal(cf.Usage, f.usage)
	}
}
