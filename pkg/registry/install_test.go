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
	"strings"
	"sync"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry"
	"github.com/conduitio/conduit/pkg/registry/index"
	"github.com/conduitio/conduit/pkg/registry/trust"
)

// passThroughVerifier is a test-only IndexVerifier/ArtifactVerifier that
// unconditionally treats every index/artifact as verified. It is the ONLY
// way this test suite exercises the download/stage/sha256/extract/
// rename/manifest steps end to end — it is never exported, never
// referenced from cmd/conduit, and exists specifically to prove those steps
// work while the real trust core (PR-2) does not exist yet. See
// TestInstall_FailClosedByConstruction, in the same file, for the test that
// proves the REAL production wiring (FailClosedVerifier) refuses instead.
type passThroughVerifier struct{}

func (passThroughVerifier) VerifyIndex(_ context.Context, raw []byte) (*index.VerifiedIndex, error) {
	p, err := index.ParseUnverified(raw)
	if err != nil {
		return nil, err
	}
	return &index.VerifiedIndex{Payload: *p, Verified: true}, nil
}

func (passThroughVerifier) VerifyArtifact(_ context.Context, _ registry.ArtifactRef, identity trust.PinnedIdentity) (registry.VerifyResult, error) {
	return registry.VerifyResult{Signed: true, VerifiedIdentity: "test:" + identity.OIDCIssuer}, nil
}

// buildInstallArchive returns a valid tar.gz containing a single root-level
// regular file — a fixture connector "binary".
func buildInstallArchive(t *testing.T, label string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	content := []byte("binary-content-for-" + label)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "conduit-connector-" + label, Mode: 0o755, Size: int64(len(content)),
	}))
	_, err := tw.Write(content)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

// newInstallTestServer serves a schema-valid (but unsigned — signatures[]
// is empty, which is fine since every test here uses either
// FailClosedVerifier, which never checks it, or passThroughVerifier, which
// deliberately skips crypto by design) registry index for one connector at
// one version, plus its artifact and (fake, uninspected by any code this PR
// ships) signature/provenance bundles.
func newInstallTestServer(t *testing.T, name, version string) (srv *httptest.Server, indexURL string, archiveBytes []byte) {
	t.Helper()
	archiveBytes = buildInstallArchive(t, name)
	digest := sha256.Sum256(archiveBytes)

	mux := http.NewServeMux()
	srv = httptest.NewServer(mux)

	payload := index.Payload{
		SchemaVersion: 1,
		Index:         index.IndexMeta{Version: 7, Timestamp: time.Now()},
		Connectors: []index.Connector{
			{
				Name: name,
				Publisher: index.Publisher{
					ExpectedOIDCIssuer:      "https://token.actions.githubusercontent.com",
					ExpectedIdentityPattern: `^https://github\.com/example/` + name + `/.*$`,
				},
				Versions: []index.ConnectorVersion{
					{
						Version: version, MinConduitVersion: "0.1.0", MinProtocolVersion: "0.1.0",
						Artifacts: []index.Artifact{
							{
								OS: runtime.GOOS, Arch: runtime.GOARCH, Kind: registry.StandaloneArtifactKind,
								URL:       srv.URL + "/artifact.tar.gz",
								SHA256:    hexEncode(digest),
								Size:      int64(len(archiveBytes)),
								Signature: index.SignatureRef{BundleURL: srv.URL + "/sig.json"},
							},
						},
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
	mux.HandleFunc("/artifact.tar.gz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archiveBytes) })
	mux.HandleFunc("/sig.json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"sig":"fake-test-bundle"}`)) })
	mux.HandleFunc("/prov.json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"prov":"fake-test-bundle"}`)) })

	return srv, srv.URL + "/index.json", archiveBytes
}

func hexEncode(d [32]byte) string {
	const hextable = "0123456789abcdef"
	out := make([]byte, 64)
	for i, b := range d {
		out[i*2] = hextable[b>>4]
		out[i*2+1] = hextable[b&0x0f]
	}
	return string(out)
}

// TestInstall_FailClosedByConstruction is the AC-6 test: the REAL
// production wiring (FailClosedVerifier for BOTH verifiers — exactly what
// cmd/conduit/root/connectors/install.go passes, with no way to configure
// anything else) must refuse every install attempt with
// CodeVerificationUnavailable, and must leave NOTHING resembling an
// installed connector binary under ConnectorsPath.
func TestInstall_FailClosedByConstruction(t *testing.T) {
	srv, indexURL, _ := newInstallTestServer(t, "widget", "1.0.0")
	defer srv.Close()

	connectorsPath := t.TempDir()

	_, err := registry.Install(context.Background(), registry.InstallOptions{
		Name: "widget", ConnectorsPath: connectorsPath, IndexURL: indexURL,
		IndexVerifier:          registry.FailClosedVerifier{},
		ArtifactVerifier:       registry.FailClosedVerifier{},
		RunningConduitVersion:  "1.0.0",
		RunningProtocolVersion: "1.0.0",
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeVerificationUnavailable, ce.Code)

	assertNoInstalledBinary(t, connectorsPath)
}

func assertNoInstalledBinary(t *testing.T, connectorsPath string) {
	t.Helper()
	entries, err := os.ReadDir(connectorsPath)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), "conduit-connector-",
			"no connector binary should ever be visible under --connectors.path in this PR's shipped configuration")
	}
}

// TestInstall_FullPipeline exercises resolve -> platform select -> download
// -> corruption check -> (test-only) verification -> extract -> atomic
// rename -> manifest write -> audit, using passThroughVerifier injected via
// the SAME InstallOptions field the real command uses for FailClosedVerifier
// — proving the seam, not a separate code path.
func TestInstall_FullPipeline(t *testing.T) {
	srv, indexURL, archiveBytes := newInstallTestServer(t, "widget", "1.0.0")
	defer srv.Close()

	connectorsPath := t.TempDir()

	res, err := registry.Install(context.Background(), registry.InstallOptions{
		Name: "widget", ConnectorsPath: connectorsPath, IndexURL: indexURL,
		IndexVerifier:          passThroughVerifier{},
		ArtifactVerifier:       passThroughVerifier{},
		RunningConduitVersion:  "1.0.0",
		RunningProtocolVersion: "1.0.0",
		InstalledBy:            "test-operator",
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "widget", res.Name)
	assert.Equal(t, "1.0.0", res.Version)
	assert.Equal(t, runtime.GOOS, res.OS)
	assert.Equal(t, runtime.GOARCH, res.Arch)
	assert.False(t, res.AlreadyInstalled)
	assert.Equal(t, "conduit-connector-widget_1.0.0", res.ArtifactFile)

	// The installed binary is present, complete, and executable.
	installedPath := filepath.Join(connectorsPath, "conduit-connector-widget_1.0.0")
	data, err := os.ReadFile(installedPath)
	require.NoError(t, err)
	assert.Equal(t, "binary-content-for-widget", string(data))
	info, err := os.Stat(installedPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())

	expectedDigest := "sha256:" + hexEncode(sha256.Sum256(archiveBytes))
	assert.Equal(t, expectedDigest, res.Digest)

	// Manifest entry present and correctly keyed.
	m, err := registry.LoadManifest(filepath.Join(connectorsPath, ".registry", "manifest.json"))
	require.NoError(t, err)
	entry, ok := m.Installs["widget@1.0.0"]
	require.True(t, ok)
	assert.Equal(t, expectedDigest, entry.Digest)
	assert.True(t, entry.Signed)
	assert.Equal(t, "test-operator", entry.InstalledBy)

	// Audit log has exactly one connector_install event.
	auditData, err := os.ReadFile(filepath.Join(connectorsPath, ".registry", "audit.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(auditData), `"event":"connector_install"`)
	assert.Contains(t, string(auditData), `"connector":"widget"`)

	// Staging is cleaned up; nothing but the manifest/lock/audit
	// bookkeeping and the installed binary remain.
	stagingEntries, err := os.ReadDir(filepath.Join(connectorsPath, ".registry", "staging"))
	require.NoError(t, err)
	assert.Empty(t, stagingEntries)
}

func TestInstall_AlreadyInstalled(t *testing.T) {
	srv, indexURL, _ := newInstallTestServer(t, "widget", "1.0.0")
	defer srv.Close()
	connectorsPath := t.TempDir()

	opts := registry.InstallOptions{
		Name: "widget", ConnectorsPath: connectorsPath, IndexURL: indexURL,
		IndexVerifier: passThroughVerifier{}, ArtifactVerifier: passThroughVerifier{},
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
	}

	first, err := registry.Install(context.Background(), opts)
	require.NoError(t, err)
	require.False(t, first.AlreadyInstalled)

	second, err := registry.Install(context.Background(), opts)
	require.NoError(t, err)
	assert.True(t, second.AlreadyInstalled)
	assert.Equal(t, first.Digest, second.Digest)
}

func TestInstall_DryRun(t *testing.T) {
	srv, indexURL, _ := newInstallTestServer(t, "widget", "1.0.0")
	defer srv.Close()
	connectorsPath := t.TempDir()

	res, err := registry.Install(context.Background(), registry.InstallOptions{
		Name: "widget", ConnectorsPath: connectorsPath, IndexURL: indexURL,
		IndexVerifier: passThroughVerifier{}, ArtifactVerifier: passThroughVerifier{},
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
		DryRun: true,
	})
	require.NoError(t, err)
	assert.True(t, res.DryRun)
	assert.Equal(t, srv.URL+"/artifact.tar.gz", res.ArtifactURL)

	// Nothing at all written under ConnectorsPath — not even .registry/.
	entries, err := os.ReadDir(connectorsPath)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestInstall_ConcurrentSameName(t *testing.T) {
	srv, indexURL, _ := newInstallTestServer(t, "widget", "1.0.0")
	defer srv.Close()
	connectorsPath := t.TempDir()

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := registry.Install(context.Background(), registry.InstallOptions{
				Name: "widget", ConnectorsPath: connectorsPath, IndexURL: indexURL,
				IndexVerifier: passThroughVerifier{}, ArtifactVerifier: passThroughVerifier{},
				RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
				LockTimeout: 10 * time.Second,
			})
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoErrorf(t, err, "goroutine %d", i)
	}

	m, err := registry.LoadManifest(filepath.Join(connectorsPath, ".registry", "manifest.json"))
	require.NoError(t, err)
	assert.Len(t, m.Installs, 1)

	installedPath := filepath.Join(connectorsPath, "conduit-connector-widget_1.0.0")
	data, err := os.ReadFile(installedPath)
	require.NoError(t, err)
	assert.Equal(t, "binary-content-for-widget", string(data))
}

func TestInstall_ConcurrentDifferentNames(t *testing.T) {
	srvA, indexURLA, _ := newInstallTestServer(t, "widget-a", "1.0.0")
	defer srvA.Close()
	srvB, indexURLB, _ := newInstallTestServer(t, "widget-b", "1.0.0")
	defer srvB.Close()

	connectorsPath := t.TempDir()

	var wg sync.WaitGroup
	var errA, errB error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errA = registry.Install(context.Background(), registry.InstallOptions{
			Name: "widget-a", ConnectorsPath: connectorsPath, IndexURL: indexURLA,
			IndexVerifier: passThroughVerifier{}, ArtifactVerifier: passThroughVerifier{},
			RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
			LockTimeout: 10 * time.Second,
		})
	}()
	go func() {
		defer wg.Done()
		_, errB = registry.Install(context.Background(), registry.InstallOptions{
			Name: "widget-b", ConnectorsPath: connectorsPath, IndexURL: indexURLB,
			IndexVerifier: passThroughVerifier{}, ArtifactVerifier: passThroughVerifier{},
			RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
			LockTimeout: 10 * time.Second,
		})
	}()
	wg.Wait()

	require.NoError(t, errA)
	require.NoError(t, errB)

	m, err := registry.LoadManifest(filepath.Join(connectorsPath, ".registry", "manifest.json"))
	require.NoError(t, err)
	assert.Len(t, m.Installs, 2)
	_, ok := m.Installs["widget-a@1.0.0"]
	assert.True(t, ok)
	_, ok = m.Installs["widget-b@1.0.0"]
	assert.True(t, ok)
}

// TestChaosHelperProcess is the subprocess entry point TestInstall_ChaosKill
// re-execs (via os.Args[0]) with a chaos point selected by environment
// variable. It is a no-op (skipped) unless CONDUIT_CHAOS_HELPER=1, so it
// never runs as part of a normal `go test` invocation.
//
// os.Exit at the injected chaos point emulates exactly the state a SIGKILL
// would leave: no deferred cleanup runs (Go's os.Exit never runs defers,
// matching a real kill -9's effect on this process), and death is
// immediate. 137 (128+9) is used as the exit status by the same convention
// shells report for a SIGKILL'd child, so a human reading `echo $?` sees
// the familiar number — this test does not send a literal SIGKILL signal.
func TestChaosHelperProcess(t *testing.T) {
	if os.Getenv("CONDUIT_CHAOS_HELPER") != "1" {
		t.Skip("only runs as TestInstall_ChaosKill's subprocess")
	}

	point := os.Getenv("CONDUIT_CHAOS_POINT")
	registry.SetChaosHookForTest(func(p string) {
		if p == point {
			os.Exit(137)
		}
	})

	_, err := registry.Install(context.Background(), registry.InstallOptions{
		Name:                   os.Getenv("CONDUIT_CHAOS_NAME"),
		Version:                os.Getenv("CONDUIT_CHAOS_VERSION"),
		ConnectorsPath:         os.Getenv("CONDUIT_CHAOS_CONNECTORS_PATH"),
		IndexURL:               os.Getenv("CONDUIT_CHAOS_INDEX_URL"),
		IndexVerifier:          passThroughVerifier{},
		ArtifactVerifier:       passThroughVerifier{},
		RunningConduitVersion:  "1.0.0",
		RunningProtocolVersion: "1.0.0",
		InstalledBy:            "chaos-test",
	})
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// TestInstall_ChaosKill is the AC-9-style chaos test: kill the install
// process (see TestChaosHelperProcess) at each of the four injection points
// and assert the connectors directory is left in one of exactly two states
// — no binary at all, or the fully-written new one — never truncated, and
// that a subsequent install of the same target still succeeds (the killed
// process's flock does not wedge it).
func TestInstall_ChaosKill(t *testing.T) {
	points := []string{
		registry.ChaosPointDownloadComplete,
		registry.ChaosPointExtractComplete,
		registry.ChaosPointPrerenameFDOpened,
		registry.ChaosPointPostRenamePreManifest,
	}

	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			srv, indexURL, _ := newInstallTestServer(t, "widget", "1.0.0")
			defer srv.Close()
			connectorsPath := t.TempDir()

			cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestChaosHelperProcess$", "-test.v")
			cmd.Env = append(os.Environ(),
				"CONDUIT_CHAOS_HELPER=1",
				"CONDUIT_CHAOS_POINT="+point,
				"CONDUIT_CHAOS_NAME=widget",
				"CONDUIT_CHAOS_VERSION=1.0.0",
				"CONDUIT_CHAOS_CONNECTORS_PATH="+connectorsPath,
				"CONDUIT_CHAOS_INDEX_URL="+indexURL,
			)
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &out

			runErr := cmd.Run()
			var exitErr *exec.ExitError
			require.ErrorAsf(t, runErr, &exitErr, "subprocess output:\n%s", out.String())
			assert.Equalf(t, 137, exitErr.ExitCode(), "subprocess should have been killed at %q; output:\n%s", point, out.String())

			// Invariant: never a truncated/partial binary — either nothing
			// named conduit-connector-widget* exists yet, or it is fully
			// present at its expected size.
			entries, rerr := os.ReadDir(connectorsPath)
			require.NoError(t, rerr)
			var binaryPresent bool
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "conduit-connector-widget") {
					binaryPresent = true
					info, ierr := e.Info()
					require.NoError(t, ierr)
					assert.Equal(t, int64(len("binary-content-for-widget")), info.Size(),
						"installed binary must never be a truncated/partial file")
				}
			}
			if point == registry.ChaosPointPostRenamePreManifest {
				assert.True(t, binaryPresent, "binary should already be fully renamed into place by this injection point")
			}

			// A subsequent install of the SAME target must still succeed:
			// the killed process's flock must not wedge it (flock releases
			// at process death), and re-running past a partially-completed
			// attempt (e.g. binary renamed but manifest not yet written)
			// must be idempotent, not an error.
			res, ierr := registry.Install(context.Background(), registry.InstallOptions{
				Name: "widget", Version: "1.0.0", ConnectorsPath: connectorsPath, IndexURL: indexURL,
				IndexVerifier: passThroughVerifier{}, ArtifactVerifier: passThroughVerifier{},
				RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
				LockTimeout: 5 * time.Second,
			})
			require.NoError(t, ierr)
			assert.Equal(t, "widget", res.Name)

			installedPath := filepath.Join(connectorsPath, "conduit-connector-widget_1.0.0")
			data, rerr := os.ReadFile(installedPath)
			require.NoError(t, rerr)
			assert.Equal(t, "binary-content-for-widget", string(data))
		})
	}
}
