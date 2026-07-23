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

// Full-stack CLI integration tests for `conduit connectors uninstall`. The
// gRPC engine is never running in these tests, so every run exercises the
// offline provisioned-pipeline-config fallback (uninstall.go's
// listConnectorInstancesFromProvisionedConfig) for the in-use check — a
// real, not mocked, proof that uninstall works without a running engine.
package connectors_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/root/connectors"
	"github.com/conduitio/ecdysis"
)

func runUninstall(t *testing.T, args ...string) (output string, err error) {
	t.Helper()
	e := ecdysis.New(ecdysis.WithDecorators(cecdysis.CommandWithResultDecorator{}))
	cmd := e.MustBuildCobraCommand(&connectors.UninstallCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	_, err = cmd.ExecuteC()
	return out.String(), err
}

func writeUninstallManifestFixture(t *testing.T, connectorsPath, name, version string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(connectorsPath, ".registry"), 0o755))
	artifactFile := name + "_" + version
	require.NoError(t, os.WriteFile(filepath.Join(connectorsPath, artifactFile), []byte("fixture-bytes"), 0o755))

	manifest := map[string]any{
		"schemaVersion": 1,
		"installs": map[string]any{
			name + "@" + version: map[string]any{
				"name": name, "version": version, "kind": "standalone",
				"os": "linux", "arch": "amd64", "artifactFile": artifactFile,
				"digest":      "sha256:0000000000000000000000000000000000000000000000000000000000000000",
				"installedAt": time.Now().UTC().Format(time.RFC3339), "source": "index",
			},
		},
	}
	data, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(connectorsPath, ".registry", "manifest.json"), data, 0o644))
}

func TestUninstall_NoPipelinesProvisioned_Succeeds(t *testing.T) {
	connectorsPath := t.TempDir()
	pipelinesPath := t.TempDir() // empty: nothing provisioned, so not in use
	writeUninstallManifestFixture(t, connectorsPath, "postgres", "0.14.1")

	out, err := runUninstall(t,
		"postgres@0.14.1",
		"--connectors.path="+connectorsPath,
		"--pipelines.path="+pipelinesPath,
		"--json",
	)
	require.NoError(t, err)

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	assert.True(t, res.OK)

	_, statErr := os.Stat(filepath.Join(connectorsPath, "postgres_0.14.1"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestUninstall_NotInstalled_HardError(t *testing.T) {
	connectorsPath := t.TempDir()
	pipelinesPath := t.TempDir()

	out, err := runUninstall(t,
		"postgres",
		"--connectors.path="+connectorsPath,
		"--pipelines.path="+pipelinesPath,
		"--json",
	)
	require.Error(t, err)

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.NotNil(t, res.Error)
	assert.Equal(t, "registry.connector_not_installed", res.Error.Code)
}

// TestUninstall_InUseViaProvisionedConfig_RefusesWithoutForce is the
// offline in-use check's own AC (step5 §2): a pipeline config referencing
// the exact plugin@version being uninstalled, found via the provisioned-
// config fallback (no engine running in this test process at all), refuses
// the uninstall by default and succeeds loudly with --force.
func TestUninstall_InUseViaProvisionedConfig_RefusesWithoutForce(t *testing.T) {
	connectorsPath := t.TempDir()
	pipelinesPath := t.TempDir()
	writeUninstallManifestFixture(t, connectorsPath, "postgres", "0.14.1")

	pipelineYAML := `
version: "2.2"
pipelines:
  - id: pipe1
    status: running
    connectors:
      - id: conn1
        type: source
        plugin: "standalone:postgres@0.14.1"
        settings:
          foo: bar
`
	require.NoError(t, os.WriteFile(filepath.Join(pipelinesPath, "pipeline1.yaml"), []byte(pipelineYAML), 0o644))

	out, err := runUninstall(t,
		"postgres@0.14.1",
		"--connectors.path="+connectorsPath,
		"--pipelines.path="+pipelinesPath,
		"--json",
	)
	require.Error(t, err)

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.NotNil(t, res.Error)
	assert.Equal(t, "registry.connector_in_use", res.Error.Code)
	assert.Contains(t, res.Error.Message, "pipe1")

	// Nothing removed.
	_, statErr := os.Stat(filepath.Join(connectorsPath, "postgres_0.14.1"))
	assert.NoError(t, statErr)

	// --force proceeds, with a warning.
	out, err = runUninstall(t,
		"postgres@0.14.1",
		"--connectors.path="+connectorsPath,
		"--pipelines.path="+pipelinesPath,
		"--force", "--json",
	)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	assert.True(t, res.OK)

	_, statErr = os.Stat(filepath.Join(connectorsPath, "postgres_0.14.1"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestUninstall_MissingName_HardError(t *testing.T) {
	_, err := runUninstall(t, "--json")
	require.Error(t, err)
}
