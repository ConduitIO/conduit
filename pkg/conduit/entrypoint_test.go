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

package conduit

import (
	"bytes"
	"strings"
	"testing"
)

func TestFlags_HelpOutput(t *testing.T) {
	var buf bytes.Buffer

	flags := Flags(&Config{})
	flags.SetOutput(&buf)

	flags.Usage()
	output := buf.String()

	expectedFlags := []string{
		"db.type",
		"db.badger.path",
		"db.postgres.connection-string",
		"db.postgres.table",
		"db.sqlite.path",
		"db.sqlite.table",
		"api.enabled",
		"http.address",
		"grpc.address",
		"log.level",
		"log.format",
		"connectors.path",
		"processors.path",
		"pipelines.path",
		"pipelines.exit-on-degraded",
		"pipelines.error-recovery.min-delay",
		"pipelines.error-recovery.max-delay",
		"pipelines.error-recovery.backoff-factor",
		"pipelines.error-recovery.max-retries",
		"pipelines.error-recovery.max-retries-window",
		"schema-registry.type",
		"schema-registry.confluent.connection-string",
		"preview.pipeline-arch-v2",
	}

	unexpectedFlags := []string{
		FlagPipelinesExitOnError,
		"dev",
		"dev.cpuprofile",
		"dev.memprofile",
		"dev.blockprofile",
	}

	for _, flag := range expectedFlags {
		if !strings.Contains(output, flag) {
			t.Errorf("expected flag %q not found in help output", flag)
		}
	}

	for _, flag := range unexpectedFlags {
		if strings.Contains(output, flag) {
			t.Errorf("unexpected flag %q found in help output", flag)
		}
	}
}
