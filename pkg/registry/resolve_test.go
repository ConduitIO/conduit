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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry"
	"github.com/conduitio/conduit/pkg/registry/index"
	"github.com/conduitio/conduit/pkg/registry/trust"
)

func testPayload() index.Payload {
	return index.Payload{
		SchemaVersion: 1,
		Index:         index.IndexMeta{Version: 42, Timestamp: time.Now()},
		Connectors: []index.Connector{
			{
				Name: "postgres",
				Publisher: index.Publisher{
					ExpectedOIDCIssuer:      "https://token.actions.githubusercontent.com",
					ExpectedIdentityPattern: "^https://github\\.com/ConduitIO/conduit-connector-postgres/.*$",
				},
				Versions: []index.ConnectorVersion{
					{
						Version: "0.14.0", MinConduitVersion: "0.14.0", MinProtocolVersion: "0.14.0",
						Artifacts: []index.Artifact{{OS: "linux", Arch: "amd64", Kind: "standalone", URL: "http://example/pg-0.14.0.tar.gz", SHA256: "aa"}},
						Yanked:    &index.YankReason{Reason: "regression drops the WAL replication slot"},
					},
					{
						Version: "0.14.1", MinConduitVersion: "0.14.0", MinProtocolVersion: "0.14.0",
						Artifacts: []index.Artifact{{OS: "linux", Arch: "amd64", Kind: "standalone", URL: "http://example/pg-0.14.1.tar.gz", SHA256: "bb"}},
					},
					{
						Version: "0.15.0", MinConduitVersion: "99.0.0", MinProtocolVersion: "99.0.0", // future, incompatible
						Artifacts: []index.Artifact{{OS: "linux", Arch: "amd64", Kind: "standalone", URL: "http://example/pg-0.15.0.tar.gz", SHA256: "cc"}},
					},
					{
						Version: "0.13.0", MinConduitVersion: "0.13.0", MinProtocolVersion: "0.13.0",
						Deprecated: true,
						Artifacts:  []index.Artifact{{OS: "linux", Arch: "amd64", Kind: "standalone", URL: "http://example/pg-0.13.0.tar.gz", SHA256: "dd"}},
					},
				},
			},
			{
				Name: "revoked-sink",
				Publisher: index.Publisher{
					ExpectedOIDCIssuer:      "https://token.actions.githubusercontent.com",
					ExpectedIdentityPattern: "^https://github\\.com/example/revoked-sink/.*$",
					Revoked:                 &index.Revocation{Reason: "leaked GITHUB_TOKEN"},
				},
				Versions: []index.ConnectorVersion{
					{Version: "1.0.0", MinConduitVersion: "0.1.0", MinProtocolVersion: "0.1.0"},
				},
			},
		},
	}
}

func TestResolve_ExactMatchNoFuzzy(t *testing.T) {
	_, err := registry.Resolve(testPayload(), registry.ResolveOptions{
		Name: "postgrez", RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeConnectorNotFound, ce.Code)
	// Never suggests the near-miss name "postgres" as a fix for the typo
	// "postgrez" — the anti-typosquat stance (plan-v2 §10): a first
	// registration is the one place a near-miss name has no cryptographic
	// backstop, so this lookup must never nudge a typo toward an existing
	// name.
	assert.NotContains(t, ce.Suggestion, "postgres")
}

func TestResolve_NewestCompatible(t *testing.T) {
	rv, err := registry.Resolve(testPayload(), registry.ResolveOptions{
		Name: "postgres", RunningConduitVersion: "0.14.0", RunningProtocolVersion: "0.14.0",
	})
	require.NoError(t, err)
	// 0.15.0 is newer but incompatible; 0.14.0 is newer than 0.13.0 but
	// yanked — 0.14.1 is the newest that is both non-yanked and compatible.
	assert.Equal(t, "0.14.1", rv.Version.Version)
}

func TestResolve_NeverAutoSelectsYanked(t *testing.T) {
	// Even with a running version that would make 0.14.0 "newest", it must
	// never be auto-selected because it is yanked.
	rv, err := registry.Resolve(testPayload(), registry.ResolveOptions{
		Name: "postgres", RunningConduitVersion: "0.14.0", RunningProtocolVersion: "0.14.0",
	})
	require.NoError(t, err)
	assert.NotEqual(t, "0.14.0", rv.Version.Version)
}

func TestResolve_ExactYankedRefuses(t *testing.T) {
	_, err := registry.Resolve(testPayload(), registry.ResolveOptions{
		Name: "postgres", Version: "0.14.0", RunningConduitVersion: "0.14.0", RunningProtocolVersion: "0.14.0",
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, index.CodeVersionYanked, ce.Code)
	assert.Contains(t, ce.Message, "WAL replication slot")
}

func TestResolve_ExactVersionNotFound(t *testing.T) {
	_, err := registry.Resolve(testPayload(), registry.ResolveOptions{
		Name: "postgres", Version: "9.9.9", RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeVersionNotFound, ce.Code)
}

func TestResolve_ExactIncompatiblePin(t *testing.T) {
	_, err := registry.Resolve(testPayload(), registry.ResolveOptions{
		Name: "postgres", Version: "0.15.0", RunningConduitVersion: "0.14.0", RunningProtocolVersion: "0.14.0",
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeIncompatibleVersion, ce.Code)
	assert.Contains(t, ce.Message, "99.0.0")
	assert.Contains(t, ce.Message, "0.14.0")
}

func TestResolve_LeadingVTolerant(t *testing.T) {
	rv, err := registry.Resolve(testPayload(), registry.ResolveOptions{
		Name: "postgres", Version: "v0.14.1", RunningConduitVersion: "0.14.0", RunningProtocolVersion: "0.14.0",
	})
	require.NoError(t, err)
	assert.Equal(t, "0.14.1", rv.Version.Version)
}

func TestResolve_RevokedPublisherRefusesEveryVersion(t *testing.T) {
	_, err := registry.Resolve(testPayload(), registry.ResolveOptions{
		Name: "revoked-sink", RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, trust.CodeIdentityRevoked, ce.Code)
	assert.Contains(t, ce.Message, "leaked GITHUB_TOKEN")
}

func TestResolve_DevBuildSkipsCompatibilityCheck(t *testing.T) {
	// "development" (Go's own fallback for a locally built binary with no
	// embedded semver) must not hard-refuse every install.
	rv, err := registry.Resolve(testPayload(), registry.ResolveOptions{
		Name: "postgres", Version: "0.15.0", RunningConduitVersion: "development", RunningProtocolVersion: "development",
	})
	require.NoError(t, err)
	assert.Equal(t, "0.15.0", rv.Version.Version)
}

func TestResolve_DeprecatedIsNotRefused(t *testing.T) {
	// Deprecated is a soft, informational flag (plan-v2 §7) — never a
	// refusal reason, and no error code exists for it in the canonical
	// table.
	rv, err := registry.Resolve(testPayload(), registry.ResolveOptions{
		Name: "postgres", Version: "0.13.0", RunningConduitVersion: "0.13.0", RunningProtocolVersion: "0.13.0",
	})
	require.NoError(t, err)
	assert.True(t, rv.Version.Deprecated)
}
