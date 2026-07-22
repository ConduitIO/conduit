// Copyright © 2026 Meroxa, Inc.
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

package registry_test

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/pkg/registry"
)

func TestAppendAuditEvent_AppendsJSONLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")

	ev1 := registry.AuditEvent{Event: "connector_install", Connector: "postgres", Version: "0.14.1", Timestamp: time.Now().UTC()}
	ev2 := registry.AuditEvent{Event: "connector_install", Connector: "kafka", Version: "1.0.0", Timestamp: time.Now().UTC()}

	require.NoError(t, registry.AppendAuditEvent(path, ev1))
	require.NoError(t, registry.AppendAuditEvent(path, ev2))

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	lines := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		if len(scanner.Bytes()) > 0 {
			lines++
		}
	}
	assert.Equal(t, 2, lines)
}
