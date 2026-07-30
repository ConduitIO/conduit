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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/pkg/registry"
	"github.com/conduitio/conduit/pkg/registry/index"
)

// --- test wasm fixtures (built by pkg/plugin/processor/standalone's build
// script, the same real fixtures its own tests use) ---

var (
	buildProcessorsOnce sync.Once
	errBuildProcessors  error
)

// repoRootFromTest resolves the conduit module root from this test file's
// location (pkg/registry/), independent of the test's working directory.
func repoRootFromTest(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}

// buildTestProcessors builds the real standalone test WASM processors once per
// test binary (the same build-test-processors.sh the standalone package's
// TestMain runs). Requires the wasip1 toolchain + bash, exactly as the
// standalone suite already does.
func buildTestProcessors(t *testing.T) {
	t.Helper()
	root := repoRootFromTest(t)
	script := filepath.Join(root, "pkg", "plugin", "processor", "standalone", "test", "build-test-processors.sh")
	buildProcessorsOnce.Do(func() {
		cmd := exec.CommandContext(context.Background(), "bash", script)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		errBuildProcessors = cmd.Run()
	})
	require.NoError(t, errBuildProcessors, "building test wasm processors")
}

// readTestProcessorWASM returns the compiled bytes of one of the standalone
// test processors (sub is "chaos" or "specify_error").
func readTestProcessorWASM(t *testing.T, sub string) []byte {
	t.Helper()
	buildTestProcessors(t)
	p := filepath.Join(repoRootFromTest(t), "pkg", "plugin", "processor", "standalone", "test", "wasm_processors", sub, "processor.wasm")
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	require.NotEmpty(t, b)
	return b
}

// tarGzSingle wraps content as a single-root-file tar.gz — the archive shape
// ExtractBinary expects (a processor artifact is a tar.gz carrying one .wasm,
// exactly as a connector artifact carries one binary; the trust core and the
// extract step are reused verbatim).
func tarGzSingle(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}))
	_, err := tw.Write(content)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

// newProcessorInstallTestServer serves a schema-valid (unsigned-shape) index
// carrying ONE processor at ONE version plus its artifact and fake sig/prov
// bundles. mutate, if non-nil, is applied to the artifact after the arch-
// neutral defaults (wasip1/wasm, kind wasm-processor) are set — the seam for
// the malformed-artifact test.
func newProcessorInstallTestServer(t *testing.T, name, version string, artifactTarGz []byte, mutate func(a *index.Artifact)) (srv *httptest.Server, indexURL string) {
	t.Helper()
	digest := sha256.Sum256(artifactTarGz)

	mux := http.NewServeMux()
	srv = httptest.NewServer(mux)

	artifact := index.Artifact{
		OS: "wasip1", Arch: "wasm", Kind: registry.WASMProcessorArtifactKind,
		URL:       srv.URL + "/p.tar.gz",
		SHA256:    hexEncode(digest),
		Size:      int64(len(artifactTarGz)),
		Signature: index.SignatureRef{BundleURL: srv.URL + "/sig.json"},
	}
	if mutate != nil {
		mutate(&artifact)
	}

	payload := index.Payload{
		SchemaVersion: 1,
		Index:         index.IndexMeta{Version: 9, Timestamp: time.Now()},
		Processors: []index.Processor{
			{
				Name: name,
				Publisher: index.Publisher{
					ExpectedOIDCIssuer:      "https://token.actions.githubusercontent.com",
					ExpectedIdentityPattern: `^https://github\.com/conduitio/conduit-processor-ai/.*$`,
				},
				Versions: []index.ProcessorVersion{
					{
						Version: version, MinConduitVersion: "0.1.0", MinProtocolVersion: "0.1.0",
						Artifact:       artifact,
						SLSAProvenance: &index.ProvenanceRef{BundleURL: srv.URL + "/prov.json", PredicateType: "https://slsa.dev/provenance/v1"},
					},
				},
			},
		},
	}

	envelope := map[string]any{"payload": payload, "signatures": []any{}}
	indexBytes, err := json.Marshal(envelope)
	require.NoError(t, err)

	mux.HandleFunc("/index.json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(indexBytes) })
	mux.HandleFunc("/p.tar.gz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(artifactTarGz) })
	mux.HandleFunc("/sig.json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"sig":"fake-test-bundle"}`)) })
	mux.HandleFunc("/prov.json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"prov":"fake-test-bundle"}`)) })

	return srv, srv.URL + "/index.json"
}

func assertNoInstalledProcessor(t *testing.T, processorsPath string) {
	t.Helper()
	entries, err := os.ReadDir(processorsPath)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), "conduit-processor-",
			"no processor artifact should be visible under --processors.path")
	}
}

// TestInstallProcessor_FullPipeline is the processor twin of
// TestInstall_FullPipeline: resolve (processors[]) -> arch-neutral select ->
// download -> corruption -> (test) verification -> extract -> install-time
// WASM validation -> atomic rename -> manifest -> audit, using the REAL chaos
// processor wasm so install-time validation runs against a genuine module.
func TestInstallProcessor_FullPipeline(t *testing.T) {
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
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "chaos-processor", res.Name)
	assert.Equal(t, "1.3.5", res.Version)
	assert.Equal(t, "wasip1", res.OS)
	assert.Equal(t, "wasm", res.Arch)
	assert.Equal(t, "conduit-processor-chaos-processor_1.3.5.wasm", res.ArtifactFile)

	// Installed file present, complete, and 0644 (NOT 0755 — no exec bit).
	installedPath := filepath.Join(processorsPath, "conduit-processor-chaos-processor_1.3.5.wasm")
	data, err := os.ReadFile(installedPath)
	require.NoError(t, err)
	assert.Equal(t, wasm, data)
	info, err := os.Stat(installedPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())

	// Manifest under the PROCESSORS path (separate from any connector manifest),
	// Kind == wasm-processor, keyed name@version.
	m, err := registry.LoadManifest(filepath.Join(processorsPath, ".registry", "manifest.json"))
	require.NoError(t, err)
	entry, ok := m.Installs["chaos-processor@1.3.5"]
	require.True(t, ok)
	assert.Equal(t, registry.WASMProcessorArtifactKind, entry.Kind)
	assert.True(t, entry.Signed)

	// Audit log carries a processor_install event, not connector_install.
	auditData, err := os.ReadFile(filepath.Join(processorsPath, ".registry", "audit.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(auditData), `"event":"processor_install"`)
	assert.NotContains(t, string(auditData), `"event":"connector_install"`)

	// Staging (a subdir of the PROCESSORS path — same filesystem, so the rename
	// is atomic) is cleaned up.
	stagingEntries, err := os.ReadDir(filepath.Join(processorsPath, ".registry", "staging"))
	require.NoError(t, err)
	assert.Empty(t, stagingEntries)
}

// TestInstallProcessor_Idempotent re-installs the same name@version and gets a
// no-op AlreadyInstalled success (mirrors TestInstall_AlreadyInstalled).
func TestInstallProcessor_Idempotent(t *testing.T) {
	wasm := readTestProcessorWASM(t, "chaos")
	artifact := tarGzSingle(t, "processor.wasm", wasm)
	srv, indexURL := newProcessorInstallTestServer(t, "chaos-processor", "1.3.5", artifact, nil)
	defer srv.Close()

	processorsPath := t.TempDir()
	opts := registry.InstallOptions{
		Name: "chaos-processor", ProcessorsPath: processorsPath, IndexURL: indexURL,
		IndexVerifier: passThroughVerifier{}, ArtifactVerifier: passThroughVerifier{},
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
	}

	first, err := registry.InstallProcessor(context.Background(), opts)
	require.NoError(t, err)
	assert.False(t, first.AlreadyInstalled)

	second, err := registry.InstallProcessor(context.Background(), opts)
	require.NoError(t, err)
	assert.True(t, second.AlreadyInstalled)
	assert.Equal(t, first.Digest, second.Digest)
}

// TestInstallProcessor_DryRun resolves+selects but writes nothing under
// --processors.path.
func TestInstallProcessor_DryRun(t *testing.T) {
	wasm := readTestProcessorWASM(t, "chaos")
	artifact := tarGzSingle(t, "processor.wasm", wasm)
	srv, indexURL := newProcessorInstallTestServer(t, "chaos-processor", "1.3.5", artifact, nil)
	defer srv.Close()

	processorsPath := t.TempDir()
	res, err := registry.InstallProcessor(context.Background(), registry.InstallOptions{
		Name: "chaos-processor", ProcessorsPath: processorsPath, IndexURL: indexURL,
		IndexVerifier: passThroughVerifier{}, ArtifactVerifier: passThroughVerifier{},
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
		DryRun: true,
	})
	require.NoError(t, err)
	assert.True(t, res.DryRun)
	assert.Equal(t, srv.URL+"/p.tar.gz", res.ArtifactURL)

	entries, err := os.ReadDir(processorsPath)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// TestInstallProcessor_SpecNameMismatchRefused: a genuine, compilable wasm
// whose Specification().Name ("chaos-processor") disagrees with the index name
// ("not-the-real-name") is refused at install time (AC-9) — never installed
// under a name it does not own.
func TestInstallProcessor_SpecNameMismatchRefused(t *testing.T) {
	wasm := readTestProcessorWASM(t, "chaos")
	artifact := tarGzSingle(t, "processor.wasm", wasm)
	srv, indexURL := newProcessorInstallTestServer(t, "not-the-real-name", "1.3.5", artifact, nil)
	defer srv.Close()

	processorsPath := t.TempDir()
	_, err := registry.InstallProcessor(context.Background(), registry.InstallOptions{
		Name: "not-the-real-name", ProcessorsPath: processorsPath, IndexURL: indexURL,
		IndexVerifier: passThroughVerifier{}, ArtifactVerifier: passThroughVerifier{},
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
	})
	requireCode(t, err, registry.CodeInvalidProcessorArtifact)
	assertNoInstalledProcessor(t, processorsPath)
}

// TestInstallProcessor_CompileFailRefused: an artifact whose bytes are not a
// valid WASM module is refused at install time — the wazero compile step fails
// before the rename, so nothing lands under --processors.path.
func TestInstallProcessor_CompileFailRefused(t *testing.T) {
	artifact := tarGzSingle(t, "processor.wasm", []byte("this is definitely not a wasm module"))
	srv, indexURL := newProcessorInstallTestServer(t, "broken-processor", "1.0.0", artifact, nil)
	defer srv.Close()

	processorsPath := t.TempDir()
	_, err := registry.InstallProcessor(context.Background(), registry.InstallOptions{
		Name: "broken-processor", ProcessorsPath: processorsPath, IndexURL: indexURL,
		IndexVerifier: passThroughVerifier{}, ArtifactVerifier: passThroughVerifier{},
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
	})
	requireCode(t, err, registry.CodeInvalidProcessorArtifact)
	assertNoInstalledProcessor(t, processorsPath)
}

// TestInstallProcessor_SpecifyErrorRefused: a module that compiles but whose
// Specification() returns an error is refused at install time (the spec cannot
// be read, so its name cannot be trusted).
func TestInstallProcessor_SpecifyErrorRefused(t *testing.T) {
	wasm := readTestProcessorWASM(t, "specify_error")
	artifact := tarGzSingle(t, "processor.wasm", wasm)
	srv, indexURL := newProcessorInstallTestServer(t, "specify-error-processor", "1.0.0", artifact, nil)
	defer srv.Close()

	processorsPath := t.TempDir()
	_, err := registry.InstallProcessor(context.Background(), registry.InstallOptions{
		Name: "specify-error-processor", ProcessorsPath: processorsPath, IndexURL: indexURL,
		IndexVerifier: passThroughVerifier{}, ArtifactVerifier: passThroughVerifier{},
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
	})
	requireCode(t, err, registry.CodeInvalidProcessorArtifact)
	assertNoInstalledProcessor(t, processorsPath)
}

// TestInstallProcessor_HostArtifactRefused proves InstallProcessor wires
// SelectProcessorArtifact (arch-neutral) and NEVER the host-(GOOS,GOARCH)
// SelectArtifact: a processor entry whose single artifact declares a host
// os/arch is a hard CodeInvalidProcessorArtifact, not a silent skip (design
// doc failure mode 3). No download or install happens.
func TestInstallProcessor_HostArtifactRefused(t *testing.T) {
	artifact := tarGzSingle(t, "processor.wasm", []byte("irrelevant — never fetched"))
	srv, indexURL := newProcessorInstallTestServer(t, "chaos-processor", "1.3.5", artifact, func(a *index.Artifact) {
		a.OS = runtime.GOOS
		a.Arch = runtime.GOARCH
	})
	defer srv.Close()

	processorsPath := t.TempDir()
	_, err := registry.InstallProcessor(context.Background(), registry.InstallOptions{
		Name: "chaos-processor", ProcessorsPath: processorsPath, IndexURL: indexURL,
		IndexVerifier: passThroughVerifier{}, ArtifactVerifier: passThroughVerifier{},
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
	})
	requireCode(t, err, registry.CodeInvalidProcessorArtifact)
	assertNoInstalledProcessor(t, processorsPath)
}

// TestInstallProcessor_FailClosedByConstruction is the processor twin of
// TestInstall_FailClosedByConstruction: the REAL production wiring
// (FailClosedVerifier for BOTH verifiers) refuses every processor install with
// CodeVerificationUnavailable — the trust gate fails BEFORE WASM validation or
// the rename — and leaves nothing under --processors.path.
func TestInstallProcessor_FailClosedByConstruction(t *testing.T) {
	// The artifact need not be a real wasm: the verification gate refuses
	// before install-time validation is ever reached.
	artifact := tarGzSingle(t, "processor.wasm", []byte("bytes never validated — refused at the trust gate"))
	srv, indexURL := newProcessorInstallTestServer(t, "chaos-processor", "1.3.5", artifact, nil)
	defer srv.Close()

	processorsPath := t.TempDir()
	_, err := registry.InstallProcessor(context.Background(), registry.InstallOptions{
		Name: "chaos-processor", ProcessorsPath: processorsPath, IndexURL: indexURL,
		IndexVerifier:         registry.FailClosedVerifier{},
		ArtifactVerifier:      registry.FailClosedVerifier{},
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
	})
	requireCode(t, err, registry.CodeVerificationUnavailable)
	assertNoInstalledProcessor(t, processorsPath)
}

// TestInstallProcessor_NotFound returns registry.processor_not_found for a name
// absent from the index's processors[] collection.
func TestInstallProcessor_NotFound(t *testing.T) {
	artifact := tarGzSingle(t, "processor.wasm", []byte("unused"))
	srv, indexURL := newProcessorInstallTestServer(t, "ai.embed", "1.0.0", artifact, nil)
	defer srv.Close()

	processorsPath := t.TempDir()
	_, err := registry.InstallProcessor(context.Background(), registry.InstallOptions{
		Name: "ai.chunk", ProcessorsPath: processorsPath, IndexURL: indexURL,
		IndexVerifier: passThroughVerifier{}, ArtifactVerifier: passThroughVerifier{},
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
	})
	requireCode(t, err, registry.CodeProcessorNotFound)
	assertNoInstalledProcessor(t, processorsPath)
}

// TestInstallProcessor_MissingProcessorsPath refuses with an invalid-argument
// error when --processors.path is not set (validateProcessor).
func TestInstallProcessor_MissingProcessorsPath(t *testing.T) {
	_, err := registry.InstallProcessor(context.Background(), registry.InstallOptions{
		Name: "ai.embed", IndexURL: "https://example.test/index.json",
		IndexVerifier: passThroughVerifier{}, ArtifactVerifier: passThroughVerifier{},
	})
	require.Error(t, err)
}
