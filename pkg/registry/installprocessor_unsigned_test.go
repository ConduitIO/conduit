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
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry"
	"github.com/conduitio/conduit/pkg/registry/policy"
)

// TestInstallProcessor_AllowUnsigned_RealInstall_LogsUnderProcessorsPath is the
// regression guard for the unsigned-install-log-path fix (AC-14): a processor
// install goes through the SAME single policy.Decide gate connectors use, and
// its mandatory durable audit entry must land under --processors.path/.registry
// (NOT the empty --connectors.path, which would drop it into a stray relative
// path). Uses a REAL wasip1 module so install-time WASM validation passes and
// the artifact actually lands on disk.
func TestInstallProcessor_AllowUnsigned_RealInstall_LogsUnderProcessorsPath(t *testing.T) {
	wasm := readTestProcessorWASM(t, "chaos")
	artifact := tarGzSingle(t, "processor.wasm", wasm)
	srv, indexURL := newProcessorInstallTestServer(t, "chaos-processor", "1.3.5", artifact, nil)
	defer srv.Close()

	processorsPath := t.TempDir()
	res, err := registry.InstallProcessor(context.Background(), registry.InstallOptions{
		Name: "chaos-processor", ProcessorsPath: processorsPath, IndexURL: indexURL,
		IndexVerifier: passThroughVerifier{}, ArtifactVerifier: passThroughVerifier{},
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
		InstalledBy: "test-operator",
		// Gated unsigned install: operator policy permits it, non-interactive
		// env-var escape hatch (no TTY) — policy.Decide allows.
		AllowUnsigned:         true,
		OperatorAllowUnsigned: true,
		EnvVarSet:             true,
		TTY:                   false,
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "chaos-processor", res.Name)
	assert.Equal(t, "1.3.5", res.Version)

	// Artifact landed on disk (WASM validation passed on the real module).
	installedPath := filepath.Join(processorsPath, "conduit-processor-chaos-processor_1.3.5.wasm")
	_, statErr := os.Stat(installedPath)
	require.NoError(t, statErr)

	// The mandatory unsigned-install audit entry is under --processors.path — the
	// fix: unsignedInstallGate now keys the log off the install target dir, not
	// the (empty, for a processor install) ConnectorsPath.
	logData, readErr := os.ReadFile(filepath.Join(processorsPath, ".registry", "unsigned-installs.log"))
	require.NoError(t, readErr, "unsigned-install log must be written under --processors.path/.registry")
	assert.Contains(t, string(logData), "chaos-processor")

	// Manifest records it as an unsigned install.
	m, err := registry.LoadManifest(filepath.Join(processorsPath, ".registry", "manifest.json"))
	require.NoError(t, err)
	entry, ok := m.Installs["chaos-processor@1.3.5"]
	require.True(t, ok)
	assert.False(t, entry.Signed)
	assert.True(t, entry.AllowUnsigned)
	assert.Equal(t, registry.WASMProcessorArtifactKind, entry.Kind)
}

// TestInstallProcessor_AllowUnsigned_OperatorDisabled_Refuses proves the same
// gate refuses when operator policy forbids it (the connector path's identical
// behavior), leaving nothing on disk and no unsigned-install log.
func TestInstallProcessor_AllowUnsigned_OperatorDisabled_Refuses(t *testing.T) {
	artifact := tarGzSingle(t, "processor.wasm", []byte("bytes never validated — refused at the policy gate"))
	srv, indexURL := newProcessorInstallTestServer(t, "chaos-processor", "1.3.5", artifact, nil)
	defer srv.Close()

	processorsPath := t.TempDir()
	_, err := registry.InstallProcessor(context.Background(), registry.InstallOptions{
		Name: "chaos-processor", ProcessorsPath: processorsPath, IndexURL: indexURL,
		IndexVerifier: passThroughVerifier{}, ArtifactVerifier: passThroughVerifier{},
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
		AllowUnsigned:         true,
		OperatorAllowUnsigned: false, // operator forbids the whole gate
		EnvVarSet:             true,
		TTY:                   false,
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, policy.CodeUnsignedInstallDisabledByPolicy, ce.Code)

	assertNoInstalledProcessor(t, processorsPath)
	_, statErr := os.Stat(filepath.Join(processorsPath, ".registry", "unsigned-installs.log"))
	assert.True(t, os.IsNotExist(statErr))
}
