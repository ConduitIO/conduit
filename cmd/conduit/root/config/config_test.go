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
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/root/run"
	"github.com/conduitio/ecdysis"
	"github.com/matryer/is"
)

func TestPrintStructOutput(t *testing.T) {
	is := is.New(t)

	readFrom, writeTo, err := os.Pipe()
	is.NoErr(err)

	e := ecdysis.New()
	cmd := e.MustBuildCobraCommand(&ConfigCommand{RunCmd: &run.RunCommand{}})
	cmd.SetArgs([]string{
		"--api.enabled", "true",
		"--grpc.address", "localhost:1234",
		"--http.address", "localhost:5678",
	})
	cmd.SetOut(writeTo)

	err = cmd.Execute()
	is.NoErr(err)

	err = writeTo.Close()
	is.NoErr(err)

	var buf bytes.Buffer
	_, err = io.Copy(&buf, readFrom)
	is.NoErr(err)

	output := buf.String()

	expectedLines := []string{
		"db.type: badger",
		"db.postgres.table: conduit_kv_store",
		"db.sqlite.table: conduit_kv_store",
		"api.enabled: true",
		"http.address: :8080",
		"grpc.address: localhost:1234",
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
	}

	for _, line := range expectedLines {
		if !strings.Contains(output, line) {
			t.Errorf("output does not contain expected line: %q", line)
		}
	}
}
