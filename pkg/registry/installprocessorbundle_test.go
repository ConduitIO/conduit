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
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry"
	"github.com/conduitio/conduit/pkg/registry/index"
	"github.com/conduitio/conduit/pkg/registry/trust"
)

// oneProcessorSnapshotPayload builds a signed-shape payload carrying one
// processor version whose single artifact is the arch-neutral WASM shape
// pointing at the given digest.
func oneProcessorSnapshotPayload(name, version, sha256hex string, size int64) index.Payload {
	return index.Payload{
		SchemaVersion: 1,
		Index:         index.IndexMeta{Version: 3, Timestamp: time.Now().UTC()},
		Processors: []index.Processor{{
			Name: name,
			Publisher: index.Publisher{
				ExpectedOIDCIssuer:      "https://token.actions.githubusercontent.com",
				ExpectedIdentityPattern: `^https://github\.com/conduitio/conduit-processor-ai/.*$`,
			},
			Versions: []index.ProcessorVersion{{
				Version: version, MinConduitVersion: "0.1.0", MinProtocolVersion: "0.1.0",
				Artifact: index.Artifact{
					OS: "wasip1", Arch: "wasm", Kind: registry.WASMProcessorArtifactKind,
					URL: "https://example.test/p.tar.gz", SHA256: sha256hex, Size: size,
				},
			}},
		}},
	}
}

// TestInstallProcessorBundle_MissingProcessorsPath refuses with an
// invalid-argument error when --processors.path is not set.
func TestInstallProcessorBundle_MissingProcessorsPath(t *testing.T) {
	manifest := registry.BundleManifest{BundleFormatVersion: 1, Name: "ai.embed", Version: "1.0.0", OS: "wasip1", Arch: "wasm"}
	path := buildTestBundleTar(t, manifest, []byte("bytes"), nil, nil, []byte("{}"))

	_, err := registry.InstallProcessorBundle(context.Background(), registry.InstallBundleOptions{
		BundlePath: path, Verifier: &registry.TrustedVerifier{},
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, conduiterr.CodeInvalidArgument, ce.Code)
}

// TestInstallProcessorBundle_NotFoundInSnapshot proves the bundle path resolves
// the processors[] collection (distinct from connectors[]): a name absent from
// processors[] refuses with registry.processor_not_found even before artifact
// verification.
func TestInstallProcessorBundle_NotFoundInSnapshot(t *testing.T) {
	dir := t.TempDir()
	f := newAuditTrustCoreFixture(t)
	// Snapshot carries a processor named ai.embed, but the bundle claims ai.chunk.
	payload := oneProcessorSnapshotPayload("ai.embed", "1.0.0", sha256Hex("bytes"), 5)
	snapshot := f.sign(t, payload)
	manifest := registry.BundleManifest{BundleFormatVersion: 1, Name: "ai.chunk", Version: "1.0.0", OS: "wasip1", Arch: "wasm"}
	path := buildTestBundleTar(t, manifest, []byte("bytes"), nil, nil, snapshot)

	verifier := &registry.TrustedVerifier{Anchors: f.anchors(t), StatePath: filepath.Join(dir, ".registry", "index-state.json")}
	_, err := registry.InstallProcessorBundle(context.Background(), registry.InstallBundleOptions{
		BundlePath: path, ProcessorsPath: dir, Verifier: verifier,
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeProcessorNotFound, ce.Code)
}

// TestInstallProcessorBundle_HostArtifactRefused proves the bundle path uses the
// arch-neutral SelectProcessorArtifact, never the host-platform SelectArtifact:
// a processor snapshot whose single artifact declares a host os/arch refuses
// with registry.invalid_processor_artifact.
func TestInstallProcessorBundle_HostArtifactRefused(t *testing.T) {
	dir := t.TempDir()
	f := newAuditTrustCoreFixture(t)
	payload := oneProcessorSnapshotPayload("ai.embed", "1.0.0", sha256Hex("bytes"), 5)
	// Corrupt the arch-neutral artifact into a host-platform shape.
	payload.Processors[0].Versions[0].Artifact.OS = "linux"
	payload.Processors[0].Versions[0].Artifact.Arch = "amd64"
	snapshot := f.sign(t, payload)
	manifest := registry.BundleManifest{BundleFormatVersion: 1, Name: "ai.embed", Version: "1.0.0", OS: "wasip1", Arch: "wasm"}
	path := buildTestBundleTar(t, manifest, []byte("bytes"), nil, nil, snapshot)

	verifier := &registry.TrustedVerifier{Anchors: f.anchors(t), StatePath: filepath.Join(dir, ".registry", "index-state.json")}
	_, err := registry.InstallProcessorBundle(context.Background(), registry.InstallBundleOptions{
		BundlePath: path, ProcessorsPath: dir, Verifier: verifier,
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeInvalidProcessorArtifact, ce.Code)
}

// TestInstallProcessorBundle_ReachesArtifactVerification proves the offline
// processor bundle path drives the FULL trust core (index verification →
// processor resolution → arch-neutral selection → corruption check) up to the
// artifact-verification gate, which refuses with trust.CodeUnsigned because
// this fixture carries no real signature bundle. Nothing lands under
// --processors.path.
func TestInstallProcessorBundle_ReachesArtifactVerification(t *testing.T) {
	dir := t.TempDir()
	f := newAuditTrustCoreFixture(t)
	payload := oneProcessorSnapshotPayload("ai.embed", "1.0.0", sha256Hex("processor-bytes"), int64(len("processor-bytes")))
	snapshot := f.sign(t, payload)
	manifest := registry.BundleManifest{
		BundleFormatVersion: 1, Name: "ai.embed", Version: "1.0.0", OS: "wasip1", Arch: "wasm",
		SHA256: "sha256:" + sha256Hex("processor-bytes"), Size: int64(len("processor-bytes")),
	}
	// No signature bundle → the trust gate refuses with CodeUnsigned once
	// resolution/selection/corruption all succeed.
	path := buildTestBundleTar(t, manifest, []byte("processor-bytes"), nil, nil, snapshot)

	verifier := &registry.TrustedVerifier{Anchors: f.anchors(t), StatePath: filepath.Join(dir, ".registry", "index-state.json")}
	_, err := registry.InstallProcessorBundle(context.Background(), registry.InstallBundleOptions{
		BundlePath: path, ProcessorsPath: dir, Verifier: verifier,
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, trust.CodeUnsigned, ce.Code)

	assertNoInstalledProcessor(t, dir)
}
