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

// TestResolve_PrereleaseOfExactMinimumSatisfies is the nightly-train
// scenario from issue #2818: a running CONDUIT version that is a
// PRERELEASE of the exact minConduitVersion must satisfy it, even though
// plain semver precedence ranks any prerelease below its associated
// release.
//
// RunningProtocolVersion is deliberately "0.14.0" (an ordinary, exact,
// non-prerelease match) rather than mirroring RunningConduitVersion's
// prerelease — before PR #2822's review (Finding 2), this test used
// "0.14.0-nightly.1" for BOTH, which meant it was — unnoticed — also
// asserting that a PRERELEASE protocol version satisfies its exact
// minProtocolVersion. That was true only because checkMinVersion was, at
// the time, shared between the minConduitVersion and minProtocolVersion
// call sites: the carve-out this test exists to check leaked onto the
// protocol gate too, and this test's use of an identical prerelease string
// for both fields masked exactly that leak. Now that minProtocolVersion
// goes through checkMinVersion directly (no carve-out — see
// checkMinConduitVersion's doc, resolve.go), reusing the prerelease string
// here would fail for the correct reason: this test is about the
// CONDUIT-version carve-out specifically, so RunningProtocolVersion is
// pinned to a value that satisfies MinProtocolVersion ordinarily, with no
// carve-out required to make the point.
// TestResolve_MinProtocolVersionNeverGetsPrereleaseCarveOut
// (checkminversion_test.go) is the test that positively exercises the
// leak scenario this one used to mask.
func TestResolve_PrereleaseOfExactMinimumSatisfies(t *testing.T) {
	rv, err := registry.Resolve(testPayload(), registry.ResolveOptions{
		Name: "postgres", Version: "0.14.1", // MinConduitVersion 0.14.0, MinProtocolVersion 0.14.0
		RunningConduitVersion: "0.14.0-nightly.1", RunningProtocolVersion: "0.14.0",
	})
	require.NoError(t, err)
	assert.Equal(t, "0.14.1", rv.Version.Version)
}

// TestResolve_PrereleaseDoesNotSatisfyHigherMinimum bounds the deviation:
// a prerelease only widens compatibility within the SAME (major, minor,
// patch) core as the minimum. A prerelease of a version whose core is still
// BELOW the requirement must not be let through — running truly lacks that
// unreleased code.
func TestResolve_PrereleaseDoesNotSatisfyHigherMinimum(t *testing.T) {
	// 0.15.0 requires MinConduitVersion 99.0.0; a 0.15.0 prerelease's core
	// (0.15.0) is nowhere near that, so this must still refuse exactly as a
	// non-prerelease 0.15.0 would.
	_, err := registry.Resolve(testPayload(), registry.ResolveOptions{
		Name: "postgres", Version: "0.15.0",
		RunningConduitVersion: "0.15.0-rc.1", RunningProtocolVersion: "0.15.0-rc.1",
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeIncompatibleVersion, ce.Code)
}

// TestResolve_HigherCorePrereleaseAlreadySatisfiesOrdinarily is a sanity
// check that ordinary semver precedence (untouched by the deviation) already
// does the right thing when the running prerelease's core EXCEEDS the
// minimum: "0.14.1-rc.1" > "0.14.0" under plain semver (core compared
// first), with no special-casing required.
func TestResolve_HigherCorePrereleaseAlreadySatisfiesOrdinarily(t *testing.T) {
	rv, err := registry.Resolve(testPayload(), registry.ResolveOptions{
		Name: "postgres", Version: "0.14.1", // MinConduitVersion 0.14.0
		RunningConduitVersion: "0.14.1-rc.1", RunningProtocolVersion: "0.14.1-rc.1",
	})
	require.NoError(t, err)
	assert.Equal(t, "0.14.1", rv.Version.Version)
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
