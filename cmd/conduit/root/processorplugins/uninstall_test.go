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

// Full-stack CLI integration tests for `conduit processor-plugins uninstall`.
// No engine runs in these tests, so the in-use check exercises the offline
// provisioned-pipeline-config scan — specifically the PROCESSORS-block scan
// (AC-8), a distinct code path from the connector uninstall's connectors-block
// scan.
package processorplugins_test

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
	"github.com/conduitio/conduit/cmd/conduit/root/processorplugins"
	"github.com/conduitio/ecdysis"
)

func runUninstall(t *testing.T, args ...string) (output string, err error) {
	t.Helper()
	e := ecdysis.New(ecdysis.WithDecorators(cecdysis.CommandWithResultDecorator{}))
	cmd := e.MustBuildCobraCommand(&processorplugins.UninstallCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	_, err = cmd.ExecuteC()
	return out.String(), err
}

// seedProcessorManifest writes an installed-processor manifest entry plus its
// on-disk .wasm artifact under processorsPath, matching what InstallProcessor
// would have recorded (Kind "wasm-processor", filename
// conduit-processor-<name>_<version>.wasm).
func seedProcessorManifest(t *testing.T, processorsPath string, nameVersions ...[2]string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(processorsPath, ".registry"), 0o755))

	installs := map[string]any{}
	for _, nv := range nameVersions {
		name, version := nv[0], nv[1]
		artifactFile := "conduit-processor-" + name + "_" + version + ".wasm"
		require.NoError(t, os.WriteFile(filepath.Join(processorsPath, artifactFile), []byte("fixture-wasm-bytes"), 0o644))
		installs[name+"@"+version] = map[string]any{
			"name": name, "version": version, "kind": "wasm-processor",
			"os": "wasip1", "arch": "wasm", "artifactFile": artifactFile,
			"digest":      "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			"installedAt": time.Now().UTC().Format(time.RFC3339), "source": "index",
		}
	}
	manifest := map[string]any{"schemaVersion": 1, "installs": installs}
	data, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(processorsPath, ".registry", "manifest.json"), data, 0o644))
}

func artifactPath(processorsPath, name, version string) string {
	return filepath.Join(processorsPath, "conduit-processor-"+name+"_"+version+".wasm")
}

func TestUninstall_NoPipelinesProvisioned_Succeeds(t *testing.T) {
	processorsPath := t.TempDir()
	pipelinesPath := t.TempDir() // empty: nothing provisioned, so not in use
	seedProcessorManifest(t, processorsPath, [2]string{"ai.embed", "1.0.0"})

	out, err := runUninstall(t,
		"ai.embed@1.0.0",
		"--processors.path="+processorsPath,
		"--pipelines.path="+pipelinesPath,
		"--json",
	)
	require.NoError(t, err)

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	assert.True(t, res.OK)
	assert.Equal(t, "processor-plugins.uninstall", res.Command)

	_, statErr := os.Stat(artifactPath(processorsPath, "ai.embed", "1.0.0"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestUninstall_NotInstalled_HardError(t *testing.T) {
	processorsPath := t.TempDir()
	pipelinesPath := t.TempDir()

	out, err := runUninstall(t,
		"ai.embed",
		"--processors.path="+processorsPath,
		"--pipelines.path="+pipelinesPath,
		"--json",
	)
	require.Error(t, err)

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.NotNil(t, res.Error)
	assert.Equal(t, "registry.processor_not_installed", res.Error.Code)
}

// TestUninstall_AmbiguousBareName_Refused: two versions installed, a bare-name
// uninstall refuses rather than guessing (AC-8).
func TestUninstall_AmbiguousBareName_Refused(t *testing.T) {
	processorsPath := t.TempDir()
	pipelinesPath := t.TempDir()
	seedProcessorManifest(t, processorsPath,
		[2]string{"ai.embed", "1.0.0"}, [2]string{"ai.embed", "1.1.0"})

	out, err := runUninstall(t,
		"ai.embed",
		"--processors.path="+processorsPath,
		"--pipelines.path="+pipelinesPath,
		"--json",
	)
	require.Error(t, err)

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.NotNil(t, res.Error)
	assert.Equal(t, "registry.ambiguous_uninstall", res.Error.Code)

	// Nothing removed.
	_, statErr := os.Stat(artifactPath(processorsPath, "ai.embed", "1.0.0"))
	assert.NoError(t, statErr)
}

// TestUninstall_InUseViaProcessorsBlock_RefusesWithoutForce is AC-8's own AC: a
// pipeline config referencing the processor in a PROCESSORS block (not a
// connectors block) refuses the uninstall by default and succeeds loudly with
// --force. This exercises the distinct processors-block in-use scan.
func TestUninstall_InUseViaProcessorsBlock_RefusesWithoutForce(t *testing.T) {
	processorsPath := t.TempDir()
	pipelinesPath := t.TempDir()
	seedProcessorManifest(t, processorsPath, [2]string{"ai.embed", "1.0.0"})

	// Pipeline-level processors block referencing the standalone processor.
	pipelineYAML := `
version: "2.2"
pipelines:
  - id: pipe1
    status: running
    connectors:
      - id: conn1
        type: source
        plugin: "builtin:generator"
    processors:
      - id: proc1
        plugin: "standalone:ai.embed@1.0.0"
`
	require.NoError(t, os.WriteFile(filepath.Join(pipelinesPath, "pipeline1.yaml"), []byte(pipelineYAML), 0o644))

	out, err := runUninstall(t,
		"ai.embed@1.0.0",
		"--processors.path="+processorsPath,
		"--pipelines.path="+pipelinesPath,
		"--json",
	)
	require.Error(t, err)

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.NotNil(t, res.Error)
	assert.Equal(t, "registry.processor_in_use", res.Error.Code)
	assert.Contains(t, res.Error.Message, "pipe1")
	assert.Contains(t, res.Error.Message, "processor proc1")

	// Nothing removed.
	_, statErr := os.Stat(artifactPath(processorsPath, "ai.embed", "1.0.0"))
	assert.NoError(t, statErr)

	// --force proceeds, with a warning naming the pipeline.
	out, err = runUninstall(t,
		"ai.embed@1.0.0",
		"--processors.path="+processorsPath,
		"--pipelines.path="+pipelinesPath,
		"--force", "--json",
	)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	assert.True(t, res.OK)

	_, statErr = os.Stat(artifactPath(processorsPath, "ai.embed", "1.0.0"))
	assert.True(t, os.IsNotExist(statErr))
}

// TestUninstall_InUseViaConnectorLevelProcessor_Refused proves the in-use scan
// also reaches processors nested inside a connector's own processors block, not
// just pipeline-level processors.
func TestUninstall_InUseViaConnectorLevelProcessor_Refused(t *testing.T) {
	processorsPath := t.TempDir()
	pipelinesPath := t.TempDir()
	seedProcessorManifest(t, processorsPath, [2]string{"ai.embed", "1.0.0"})

	pipelineYAML := `
version: "2.2"
pipelines:
  - id: pipe1
    status: running
    connectors:
      - id: conn1
        type: source
        plugin: "builtin:generator"
        processors:
          - id: cproc1
            plugin: "standalone:ai.embed"
`
	require.NoError(t, os.WriteFile(filepath.Join(pipelinesPath, "pipeline1.yaml"), []byte(pipelineYAML), 0o644))

	out, err := runUninstall(t,
		"ai.embed@1.0.0",
		"--processors.path="+processorsPath,
		"--pipelines.path="+pipelinesPath,
		"--json",
	)
	require.Error(t, err)

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.NotNil(t, res.Error)
	// The reference is unpinned (standalone:ai.embed → latest), so it must match
	// the specific version being uninstalled (loose version matching, AC-8).
	assert.Equal(t, "registry.processor_in_use", res.Error.Code)
	assert.Contains(t, res.Error.Message, "processor cproc1")
}

func TestUninstall_MissingName_HardError(t *testing.T) {
	_, err := runUninstall(t, "--json")
	require.Error(t, err)
}
