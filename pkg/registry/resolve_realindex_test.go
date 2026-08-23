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

	"github.com/conduitio/conduit/pkg/registry"
	"github.com/conduitio/conduit/pkg/registry/index"
)

// This file is the regression test issue #2818 asked for: resolving a REAL
// published registry entry against a REAL running build's version strings,
// so the "processor compat gate reads the wrong protocol" and "a nightly
// prerelease never satisfies a stable minConduitVersion" bugs fail in CI
// instead of only reproducing in a user's terminal (which is how both
// shipped undetected — every existing Resolve/ResolveProcessor test used
// synthetic, mutually-consistent version fixtures that never exercised the
// real mismatch).
//
// The entries below are copied verbatim (fields relevant to resolution only)
// from https://github.com/ConduitIO/conduit-connector-registry's
// index/index.json on main, index.version 10, timestamp
// 2026-08-21T19:26:17Z — the same snapshot
// TestGalleryCatalog_PgvectorRAG_PrerequisitesMatchPublishedReality
// (template_gallery_test.go) verified its prose against. If the registry
// re-publishes ai.chunk/ai.embed/postgres with different min-versions this
// will drift from "real" and should be re-synced, but it will not go
// silently stale: a mismatch here would only make these tests pass or fail
// on stale data, not corrupt anything, and the fixtures are annotated with
// their source so re-syncing is a copy-paste.

// realAIChunkProcessor is ConduitIO/conduit-processor-ai's published
// ai.chunk@0.1.0 entry: minConduitVersion 0.20.0, minProtocolVersion 0.14.0
// (the value at the center of issue #2818 — a copied conduit-connector-sdk
// version that corresponds to no real conduit-processor-protocol module).
func realAIChunkProcessor() index.Processor {
	return index.Processor{
		Name: "ai.chunk",
		Publisher: index.Publisher{
			ExpectedOIDCIssuer:      "https://token.actions.githubusercontent.com",
			ExpectedIdentityPattern: `^https://github\.com/ConduitIO/conduit-processor-ai/\.github/workflows/publish\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$`,
		},
		Repository: "https://github.com/ConduitIO/conduit-processor-ai",
		Versions: []index.ProcessorVersion{
			{
				Version:            "0.1.0",
				MinConduitVersion:  "0.20.0",
				MinProtocolVersion: "0.14.0",
				Artifact: index.Artifact{
					OS: "wasip1", Arch: "wasm", Kind: registry.WASMProcessorArtifactKind,
					URL:    "https://github.com/ConduitIO/conduit-processor-ai/releases/download/v0.1.0/conduit-processor-ai-chunk_0.1.0_wasip1_wasm.tar.gz",
					SHA256: "c7da3607342e8699314eb434595ea914d714e0d5752fe3e190640736924892b9",
				},
			},
		},
	}
}

// realPostgresConnector is ConduitIO/conduit-connector-postgres's published
// v0.14.2 entry: minConduitVersion 0.15.0, minProtocolVersion 0.9.0 — used
// to prove the connector path's real-world resolution is unaffected by
// either #2818 fix.
func realPostgresConnector() index.Connector {
	return index.Connector{
		Name: "postgres",
		Publisher: index.Publisher{
			ExpectedOIDCIssuer:      "https://token.actions.githubusercontent.com",
			ExpectedIdentityPattern: `^https://github\.com/ConduitIO/conduit-connector-postgres/\.github/workflows/publish\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$`,
		},
		Repository: "https://github.com/ConduitIO/conduit-connector-postgres",
		Versions: []index.ConnectorVersion{
			{
				Version:            "0.14.2",
				MinConduitVersion:  "0.15.0",
				MinProtocolVersion: "0.9.0",
				Artifacts: []index.Artifact{
					{
						OS: "linux", Arch: "amd64", Kind: "standalone",
						URL:    "https://github.com/ConduitIO/conduit-connector-postgres/releases/download/v0.14.2/postgres_v0.14.2_linux_amd64",
						SHA256: "aa",
					},
				},
			},
		},
	}
}

func realIndexPayload() index.Payload {
	return index.Payload{
		SchemaVersion: 1,
		Index:         index.IndexMeta{Version: 10, Timestamp: time.Now()},
		Connectors:    []index.Connector{realPostgresConnector()},
		Processors:    []index.Processor{realAIChunkProcessor()},
	}
}

// realRunningConnectorProtocolVersion is go.mod's real pinned
// conduit-connector-protocol version (see this repo's go.mod) — what
// runningProtocolVersion() (cmd/conduit/root/{connectors,processorplugins})
// actually reports on any build, real or nightly.
const realRunningConnectorProtocolVersion = "v0.9.5"

// realNightlyConduitVersion is an ACTUAL `git describe --tags --dirty`
// output from this repo's nightly train (captured verbatim, not
// hand-simplified), and matches the exact string the issue's reproduction
// quoted ("conduit v0.20.0-nightly.20260805, protocol v0.9.5").
const realNightlyConduitVersion = "v0.20.0-nightly.20260805"

// TestResolveProcessor_RealPublishedEntry_NightlyBuildCanInstall reproduces
// issue #2818's exact failure and proves the fix: resolving the real
// ai.chunk@0.1.0 entry (minConduitVersion 0.20.0, minProtocolVersion 0.14.0)
// against a real nightly build's real versions (conduit
// v0.20.0-nightly.20260805, conduit-connector-protocol v0.9.5 — the
// running values `conduit processor-plugins install ai.chunk` actually
// sees) must now SUCCEED. Before the fix this failed with
// registry.incompatible_version because 0.9.5 (connector protocol) was
// compared against 0.14.0 (a processor's copied, meaningless
// minProtocolVersion) — an always-false comparison on every build, not
// just this one.
func TestResolveProcessor_RealPublishedEntry_NightlyBuildCanInstall(t *testing.T) {
	res, err := registry.ResolveProcessor(realIndexPayload(), registry.ResolveOptions{
		Name:                   "ai.chunk",
		RunningConduitVersion:  realNightlyConduitVersion,
		RunningProtocolVersion: realRunningConnectorProtocolVersion,
	})
	require.NoError(t, err)
	assert.Equal(t, "ai.chunk", res.Processor.Name)
	assert.Equal(t, "0.1.0", res.Version.Version)
}

// TestResolveProcessor_RealPublishedEntry_StableV0_19Refused proves the fix
// did not disable compatibility checking outright: the real v0.19.0 stable
// release (the latest stable at the time of #2818, which does not carry the
// postgres-pgvector-rag template) genuinely does not satisfy
// minConduitVersion 0.20.0 — 0.19.0's core version really is lower, not
// merely differently-tagged — so resolution must still refuse it.
func TestResolveProcessor_RealPublishedEntry_StableV0_19Refused(t *testing.T) {
	_, err := registry.ResolveProcessor(realIndexPayload(), registry.ResolveOptions{
		Name:                   "ai.chunk",
		RunningConduitVersion:  "v0.19.0",
		RunningProtocolVersion: realRunningConnectorProtocolVersion,
	})
	requireCode(t, err, registry.CodeIncompatibleVersion)
}

// TestResolve_RealPublishedEntry_ConnectorPathUnaffected proves the
// connector resolution path — which was already correct — still resolves
// the real, live postgres@0.14.2 entry the same way after both #2818
// fixes: a real nightly build's real conduit-connector-protocol version
// (0.9.5) genuinely satisfies postgres's real minProtocolVersion (0.9.0),
// so this must succeed for the ordinary reason, not because protocol
// checking was silently dropped (it is dropped only for PROCESSORS, see
// TestResolveProcessor_RealPublishedEntry_NightlyBuildCanInstall's sibling
// coverage and checkProcessorCompatible's doc).
func TestResolve_RealPublishedEntry_ConnectorPathUnaffected(t *testing.T) {
	res, err := registry.Resolve(realIndexPayload(), registry.ResolveOptions{
		Name:                   "postgres",
		RunningConduitVersion:  realNightlyConduitVersion,
		RunningProtocolVersion: realRunningConnectorProtocolVersion,
	})
	require.NoError(t, err)
	assert.Equal(t, "postgres", res.Connector.Name)
	assert.Equal(t, "0.14.2", res.Version.Version)
}

// TestResolve_MinProtocolVersionStillEnforcedForConnectors is the negative
// counterpart to the above: a connector version whose minProtocolVersion
// genuinely exceeds the running protocol must still be refused — proving
// the connector protocol comparison itself (correct before #2818, and
// untouched by its fix) still refuses on a real mismatch, not just that it
// happens to pass against postgres's low bar.
func TestResolve_MinProtocolVersionStillEnforcedForConnectors(t *testing.T) {
	payload := realIndexPayload()
	tooNew := realPostgresConnector()
	tooNew.Versions[0].MinProtocolVersion = "9.9.9"
	payload.Connectors = []index.Connector{tooNew}

	_, err := registry.Resolve(payload, registry.ResolveOptions{
		Name:                   "postgres",
		RunningConduitVersion:  realNightlyConduitVersion,
		RunningProtocolVersion: realRunningConnectorProtocolVersion,
	})
	requireCode(t, err, registry.CodeIncompatibleVersion)
}
