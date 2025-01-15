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

package config

import (
	"bytes"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/root/run"
	"github.com/conduitio/ecdysis"
	"github.com/matryer/is"
)

func TestConfig_WithFlags(t *testing.T) {
	testCases := []struct {
		name      string
		args      []string
		wantLines []string
	}{
		{
			name: "with flags (api, config, connectors, db, log)",
			args: []string{
				"--api.enabled=false",
				"--api.grpc.address", "localhost:9999",
				"--api.http.address", "localhost:8888",
				"--config.path", "/etc/conduit/config.yaml",
				"--connectors.path", "/opt/conduit/connectors",
				"--db.badger.path", "/var/lib/conduit/data.db",
				"--db.postgres.connection-string", "postgres://user:pass@localhost:5432/conduit",
				"--db.postgres.table", "my_conduit_store",
				"--db.sqlite.path", "/var/lib/conduit/conduit.sqlite",
				"--db.sqlite.table", "my_sqlite_store",
				"--db.type", "postgres",
				"--log.format", "json",
				"--log.level", "debug",
			},
			wantLines: []string{
				"db.type: postgres",
				"db.postgres.table: my_conduit_store",
				"db.sqlite.table: my_sqlite_store",
				"api.enabled: false",
				"api.http.address: localhost:8888",
				"api.grpc.address: localhost:9999",
				"log.level: debug",
				"log.format: json",
				"pipelines.exit-on-degraded: false",
				"pipelines.error-recovery.min-delay: 1s",
				"pipelines.error-recovery.max-delay: 10m0s",
				"pipelines.error-recovery.backoff-factor: 2",
				"pipelines.error-recovery.max-retries: -1",
				"pipelines.error-recovery.max-retries-window: 5m0s",
				"schema-registry.type: builtin",
				"preview.pipeline-arch-v2: false",
			},
		},
		{
			name: "with flags (pipelines, preview, processors, schema, dev)",
			args: []string{
				"--pipelines.error-recovery.backoff-factor", "5",
				"--pipelines.error-recovery.max-delay", "30m",
				"--pipelines.error-recovery.max-retries", "10",
				"--pipelines.error-recovery.max-retries-window", "15m",
				"--pipelines.error-recovery.min-delay", "5s",
				"--pipelines.exit-on-degraded=true",
				"--pipelines.path", "/var/lib/conduit/pipelines",
				"--preview.pipeline-arch-v2=true",
				"--processors.path", "/opt/conduit/processors",
				"--schema-registry.confluent.connection-string", "http://localhost:8081",
				"--schema-registry.type", "confluent",
				"--dev.blockprofile", "/tmp/block.prof",
				"--dev.cpuprofile", "/tmp/cpu.prof",
				"--dev.memprofile", "/tmp/mem.prof",
			},
			wantLines: []string{
				"db.type: badger",
				"db.postgres.table: conduit_kv_store",
				"db.sqlite.table: conduit_kv_store",
				"api.enabled: true",
				"api.http.address: :8080",
				"api.grpc.address: :8084",
				"log.level: info",
				"log.format: cli",
				"processors.path: /opt/conduit/processors",
				"pipelines.path: /var/lib/conduit/pipelines",
				"pipelines.exit-on-degraded: true",
				"pipelines.error-recovery.min-delay: 5s",
				"pipelines.error-recovery.max-delay: 30m0s",
				"pipelines.error-recovery.backoff-factor: 5",
				"pipelines.error-recovery.max-retries: 10",
				"pipelines.error-recovery.max-retries-window: 15m0s",
				"schema-registry.type: confluent",
				"schema-registry.confluent.connection-string: http://localhost:8081",
				"preview.pipeline-arch-v2: false",
				"dev.cpuprofile: /tmp/cpu.prof",
				"dev.memprofile: /tmp/mem.prof",
				"dev.blockprofile: /tmp/block.prof",
			},
		},
		{
			name: "default values (no flags)",
			args: []string{},
			wantLines: []string{
				"db.type: badger",
				"db.postgres.table: conduit_kv_store",
				"db.sqlite.table: conduit_kv_store",
				"api.enabled: true",
				"api.http.address: :8080",
				"api.grpc.address: :8084",
				"log.level: info",
				"log.format: cli",
				"pipelines.exit-on-degraded: false",
				"pipelines.error-recovery.min-delay: 1s",
				"pipelines.error-recovery.max-delay: 10m0s",
				"pipelines.error-recovery.backoff-factor: 2",
				"pipelines.error-recovery.max-retries: -1",
				"pipelines.error-recovery.max-retries-window: 5m0s",
				"schema-registry.type: builtin",
				"preview.pipeline-arch-v2: false",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			readFrom, writeTo, err := os.Pipe()
			is.NoErr(err)

			e := ecdysis.New()
			cmd := e.MustBuildCobraCommand(&ConfigCommand{RunCmd: &run.RunCommand{}})
			cmd.SetArgs(tc.args)
			cmd.SetOut(writeTo)
			cmd.SetErr(writeTo)

			err = cmd.Execute()
			is.NoErr(err)

			err = writeTo.Close()
			is.NoErr(err)

			var buf bytes.Buffer
			_, err = io.Copy(&buf, readFrom)
			is.NoErr(err)

			output := buf.String()
			is.True(output != "")

			outputLines := strings.Split(output, "\n")
			for _, line := range tc.wantLines {
				if !slices.Contains(outputLines, line) {
					t.Errorf("output does not contain expected line: %q", line)
				}
			}
		})
	}
}
