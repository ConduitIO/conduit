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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry"
	"github.com/conduitio/conduit/pkg/registry/index"
)

func wasmProcessorVersion() index.ProcessorVersion {
	return index.ProcessorVersion{
		Version: "0.1.0",
		Artifact: index.Artifact{
			OS: "wasip1", Arch: "wasm", Kind: "wasm-processor",
			URL: "http://x/proc.wasm",
		},
	}
}

func TestSelectProcessorArtifact_Match(t *testing.T) {
	a, err := registry.SelectProcessorArtifact("conduit-processor-ai", wasmProcessorVersion())
	require.NoError(t, err)
	assert.Equal(t, "http://x/proc.wasm", a.URL)
	assert.Equal(t, registry.WASMProcessorArtifactKind, a.Kind)
}

// TestSelectProcessorArtifact_WrongKindIsHardError proves the contrast with the
// connector path (design doc D2): an unexpected kind is a HARD validation
// error, never a silent skip.
func TestSelectProcessorArtifact_WrongKindIsHardError(t *testing.T) {
	v := wasmProcessorVersion()
	v.Artifact.Kind = "standalone"

	_, err := registry.SelectProcessorArtifact("conduit-processor-ai", v)
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeInvalidProcessorArtifact, ce.Code)
}

// TestSelectProcessorArtifact_HostOSArchIsHardError proves a processor artifact
// carrying a host os/arch (instead of the fixed wasip1/wasm) is rejected, not
// silently skipped — the anti-spoofing guard (D2, failure mode 3). It also
// confirms the selector NEVER consults runtime.GOOS/GOARCH: a linux/amd64
// artifact is refused even when the test host is linux/amd64.
func TestSelectProcessorArtifact_HostOSArchIsHardError(t *testing.T) {
	v := wasmProcessorVersion()
	v.Artifact.OS = "linux"
	v.Artifact.Arch = "amd64"

	_, err := registry.SelectProcessorArtifact("conduit-processor-ai", v)
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeInvalidProcessorArtifact, ce.Code)
	assert.Contains(t, ce.Suggestion, "wasip1/wasm")
}
