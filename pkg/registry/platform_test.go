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

func TestSelectArtifact_Match(t *testing.T) {
	v := index.ConnectorVersion{
		Version: "0.14.1",
		Artifacts: []index.Artifact{
			{OS: "linux", Arch: "amd64", Kind: "standalone", URL: "http://x/linux"},
			{OS: "darwin", Arch: "arm64", Kind: "standalone", URL: "http://x/darwin"},
		},
	}
	a, err := registry.SelectArtifact("postgres", v, "darwin", "arm64")
	require.NoError(t, err)
	assert.Equal(t, "http://x/darwin", a.URL)
}

func TestSelectArtifact_NoMatchListsAvailable(t *testing.T) {
	v := index.ConnectorVersion{
		Version: "0.14.1",
		Artifacts: []index.Artifact{
			{OS: "linux", Arch: "amd64", Kind: "standalone", URL: "http://x/linux"},
		},
	}
	_, err := registry.SelectArtifact("postgres", v, "windows", "amd64")
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeNoPlatformArtifact, ce.Code)
	assert.Contains(t, ce.Suggestion, "linux/amd64")
}

func TestSelectArtifact_UnrecognizedKindSkippedGracefully(t *testing.T) {
	v := index.ConnectorVersion{
		Version: "0.14.1",
		Artifacts: []index.Artifact{
			{OS: "linux", Arch: "amd64", Kind: "wasm", URL: "http://x/wasm"},
		},
	}
	_, err := registry.SelectArtifact("postgres", v, "linux", "amd64")
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeNoPlatformArtifact, ce.Code)
	assert.Contains(t, ce.Suggestion, "no standalone artifacts")
}
