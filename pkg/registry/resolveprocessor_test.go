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

// procArtifact builds a well-formed arch-neutral WASM-processor artifact for a
// resolution fixture (the bytes are never fetched — these are pure Resolve
// tests).
func procArtifact() index.Artifact {
	return index.Artifact{
		OS: "wasip1", Arch: "wasm", Kind: registry.WASMProcessorArtifactKind,
		URL: "https://example.test/p.tar.gz", SHA256: "sha256:deadbeef", Size: 1,
		Signature: index.SignatureRef{BundleURL: "https://example.test/p.sig"},
	}
}

// procIndex builds a single-processor index payload with the given versions.
func procIndex(name string, pub index.Publisher, versions ...index.ProcessorVersion) index.Payload {
	return index.Payload{
		SchemaVersion: 1,
		Index:         index.IndexMeta{Version: 3, Timestamp: time.Now()},
		Processors: []index.Processor{
			{Name: name, Publisher: pub, Versions: versions},
		},
	}
}

func okPublisher() index.Publisher {
	return index.Publisher{
		ExpectedOIDCIssuer:      "https://token.actions.githubusercontent.com",
		ExpectedIdentityPattern: `^https://github\.com/conduitio/conduit-processor-ai/.*$`,
	}
}

func procVersion(v string) index.ProcessorVersion {
	return index.ProcessorVersion{
		Version: v, MinConduitVersion: "0.1.0", MinProtocolVersion: "0.1.0",
		Artifact: procArtifact(),
	}
}

func requireCode(t *testing.T, err error, want conduiterr.Code) {
	t.Helper()
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.Truef(t, ok, "error is not a ConduitError: %v", err)
	assert.Equal(t, want, ce.Code)
}

func TestResolveProcessor_ExactVersion(t *testing.T) {
	payload := procIndex("ai.embed", okPublisher(), procVersion("1.0.0"), procVersion("1.2.0"))

	res, err := registry.ResolveProcessor(payload, registry.ResolveOptions{
		Name: "ai.embed", Version: "1.0.0",
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
	})
	require.NoError(t, err)
	assert.Equal(t, "ai.embed", res.Processor.Name)
	assert.Equal(t, "1.0.0", res.Version.Version)
}

func TestResolveProcessor_NewestCompatible(t *testing.T) {
	// Newest (2.0.0) needs a future Conduit; resolution must skip it and land
	// on the newest COMPATIBLE version (1.2.0), never a yanked one.
	v20 := procVersion("2.0.0")
	v20.MinConduitVersion = "9.9.9"
	yanked := procVersion("1.5.0")
	yanked.Yanked = &index.YankReason{Reason: "bad release"}

	payload := procIndex("ai.embed", okPublisher(), procVersion("1.0.0"), procVersion("1.2.0"), yanked, v20)

	res, err := registry.ResolveProcessor(payload, registry.ResolveOptions{
		Name:                  "ai.embed", // no version → newest compatible
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
	})
	require.NoError(t, err)
	assert.Equal(t, "1.2.0", res.Version.Version)
}

func TestResolveProcessor_NotFound_ExactMatchNoFuzzy(t *testing.T) {
	payload := procIndex("ai.embed", okPublisher(), procVersion("1.0.0"))

	// A near-miss name must NOT be nudged toward the real one (anti-typosquat).
	_, err := registry.ResolveProcessor(payload, registry.ResolveOptions{
		Name: "ai.embeded", RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
	})
	requireCode(t, err, registry.CodeProcessorNotFound)
}

func TestResolveProcessor_VersionNotFound(t *testing.T) {
	payload := procIndex("ai.embed", okPublisher(), procVersion("1.0.0"))

	_, err := registry.ResolveProcessor(payload, registry.ResolveOptions{
		Name: "ai.embed", Version: "9.9.9",
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
	})
	requireCode(t, err, registry.CodeVersionNotFound)
}

func TestResolveProcessor_PinnedYankedRefused(t *testing.T) {
	yanked := procVersion("1.0.0")
	yanked.Yanked = &index.YankReason{Reason: "cve-2026-0001"}
	payload := procIndex("ai.embed", okPublisher(), yanked)

	_, err := registry.ResolveProcessor(payload, registry.ResolveOptions{
		Name: "ai.embed", Version: "1.0.0",
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
	})
	requireCode(t, err, index.CodeVersionYanked)
}

func TestResolveProcessor_RevokedPublisherRefused(t *testing.T) {
	pub := okPublisher()
	pub.Revoked = &index.Revocation{Reason: "key compromise"}
	payload := procIndex("ai.embed", pub, procVersion("1.0.0"))

	// Even a non-yanked version is refused when the publisher identity is
	// revoked — the compromise is at the identity level.
	_, err := registry.ResolveProcessor(payload, registry.ResolveOptions{
		Name: "ai.embed", Version: "1.0.0",
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
	})
	requireCode(t, err, trust.CodeIdentityRevoked)
}

func TestResolveProcessor_IncompatiblePinnedVersion(t *testing.T) {
	v := procVersion("1.0.0")
	v.MinConduitVersion = "2.0.0"
	payload := procIndex("ai.embed", okPublisher(), v)

	_, err := registry.ResolveProcessor(payload, registry.ResolveOptions{
		Name: "ai.embed", Version: "1.0.0",
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
	})
	requireCode(t, err, registry.CodeIncompatibleVersion)
}

func TestResolveProcessor_NoCompatibleVersion(t *testing.T) {
	v := procVersion("1.0.0")
	v.MinConduitVersion = "9.9.9"
	payload := procIndex("ai.embed", okPublisher(), v)

	_, err := registry.ResolveProcessor(payload, registry.ResolveOptions{
		Name:                  "ai.embed", // newest-compatible, but none is compatible
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
	})
	requireCode(t, err, registry.CodeIncompatibleVersion)
}

// TestResolveProcessor_IgnoresMinProtocolVersion is issue #2818's Gate 1,
// unit-tested directly: a version whose MinProtocolVersion is impossibly
// high (99.9.9 — no running value could ever satisfy it under a real
// comparison) must still resolve successfully, because processor
// compatibility never compares it at all. See checkProcessorCompatible's
// doc for why: there is no meaningful processor-protocol version to compare
// against.
func TestResolveProcessor_IgnoresMinProtocolVersion(t *testing.T) {
	v := procVersion("1.0.0")
	v.MinProtocolVersion = "99.9.9"
	payload := procIndex("ai.embed", okPublisher(), v)

	res, err := registry.ResolveProcessor(payload, registry.ResolveOptions{
		Name: "ai.embed", Version: "1.0.0",
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "0.0.1", // wildly below MinProtocolVersion
	})
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", res.Version.Version)
}

// TestResolveProcessor_PrereleaseOfExactMinimumSatisfies mirrors
// TestResolve_PrereleaseOfExactMinimumSatisfies for the processor path: a
// nightly build (a prerelease of the exact minConduitVersion) must be able
// to install a processor gated to the release it is building toward.
func TestResolveProcessor_PrereleaseOfExactMinimumSatisfies(t *testing.T) {
	v := procVersion("0.1.0")
	v.MinConduitVersion = "0.20.0"
	payload := procIndex("ai.chunk", okPublisher(), v)

	res, err := registry.ResolveProcessor(payload, registry.ResolveOptions{
		Name: "ai.chunk", Version: "0.1.0",
		RunningConduitVersion: "0.20.0-nightly.20260805", RunningProtocolVersion: "0.9.5",
	})
	require.NoError(t, err)
	assert.Equal(t, "0.1.0", res.Version.Version)
}

// TestResolveProcessor_SearchesProcessorsNotConnectors proves the two
// collections are independent: a name present only in connectors[] is NOT
// found by ResolveProcessor, and vice versa (design doc failure mode 4).
func TestResolveProcessor_SearchesProcessorsNotConnectors(t *testing.T) {
	payload := index.Payload{
		SchemaVersion: 1,
		Index:         index.IndexMeta{Version: 1, Timestamp: time.Now()},
		Connectors: []index.Connector{
			{Name: "shared-name", Publisher: okPublisher(), Versions: []index.ConnectorVersion{
				{Version: "1.0.0", MinConduitVersion: "0.1.0", MinProtocolVersion: "0.1.0"},
			}},
		},
	}

	_, err := registry.ResolveProcessor(payload, registry.ResolveOptions{
		Name: "shared-name", RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
	})
	requireCode(t, err, registry.CodeProcessorNotFound)
}
