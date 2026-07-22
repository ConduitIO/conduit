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

// This file is the --allow-unsigned security-warranty suite (plan-v2 §6),
// run against the REAL registry.Install pipeline — not policy.Decide in
// isolation (pkg/registry/policy/gate_test.go already exhaustively covers
// that function's own logic). What this file proves on top: the WIRING —
// that Install actually threads InstallOptions' primitive TTY/CIEnv/IsMCP/
// EnvVarSet/TypedConfirmation/OperatorAllowUnsigned fields into
// policy.Decide correctly, that AllowUnsigned never skips the sha256
// corruption check, and that a successful unsigned install is recorded
// correctly in both the manifest and the append-only audit log.
package registry_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry"
	"github.com/conduitio/conduit/pkg/registry/index"
	"github.com/conduitio/conduit/pkg/registry/policy"
	"github.com/conduitio/conduit/pkg/registry/trust"
)

// alwaysUnsignedVerifier is a test-only ArtifactVerifier whose VerifyArtifact
// must NEVER be called — proving registry.Install's runVerificationGate
// skips step 6 (the artifact verification gate) ENTIRELY on the
// policy.Decide-approved unsigned path (plan-v2 §4 step 7: "skip step 6
// entirely"), rather than calling VerifyArtifact and merely discarding a
// refusal. VerifyIndex is a pass-through, identical to passThroughVerifier's.
type alwaysUnsignedVerifier struct{}

func (alwaysUnsignedVerifier) VerifyIndex(_ context.Context, raw []byte) (*index.VerifiedIndex, error) {
	p, err := index.ParseUnverified(raw)
	if err != nil {
		return nil, err
	}
	return &index.VerifiedIndex{Payload: *p, Verified: true}, nil
}

func (alwaysUnsignedVerifier) VerifyArtifact(context.Context, registry.ArtifactRef, trust.PinnedIdentity) (registry.VerifyResult, error) {
	panic("VerifyArtifact must never be called on the AllowUnsigned-approved path")
}

func TestAllowUnsigned_OperatorPolicyDisabled_Refuses(t *testing.T) {
	srv, indexURL, _ := newInstallTestServer(t, "widget", "1.0.0")
	defer srv.Close()
	connectorsPath := t.TempDir()

	_, err := registry.Install(context.Background(), registry.InstallOptions{
		Name: "widget", ConnectorsPath: connectorsPath, IndexURL: indexURL,
		IndexVerifier: passThroughVerifier{}, ArtifactVerifier: passThroughVerifier{},
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
		AllowUnsigned: true, TTY: true, TypedConfirmation: true, OperatorAllowUnsigned: false,
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, policy.CodeUnsignedInstallDisabledByPolicy, ce.Code)
	assertNoInstalledBinary(t, connectorsPath)
}

func TestAllowUnsigned_NonInteractiveWithoutEnvVar_Refuses(t *testing.T) {
	srv, indexURL, _ := newInstallTestServer(t, "widget", "1.0.0")
	defer srv.Close()
	connectorsPath := t.TempDir()

	_, err := registry.Install(context.Background(), registry.InstallOptions{
		Name: "widget", ConnectorsPath: connectorsPath, IndexURL: indexURL,
		IndexVerifier: passThroughVerifier{}, ArtifactVerifier: passThroughVerifier{},
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
		AllowUnsigned: true, TTY: false, EnvVarSet: false, OperatorAllowUnsigned: true,
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, policy.CodeUnsignedInstallNonInteractive, ce.Code)
	assertNoInstalledBinary(t, connectorsPath)
}

func TestAllowUnsigned_NonInteractiveWithEnvVar_InstallsAndAudits(t *testing.T) {
	srv, indexURL, _ := newInstallTestServer(t, "widget", "1.0.0")
	defer srv.Close()
	connectorsPath := t.TempDir()

	res, err := registry.Install(context.Background(), registry.InstallOptions{
		Name: "widget", ConnectorsPath: connectorsPath, IndexURL: indexURL,
		IndexVerifier: passThroughVerifier{}, ArtifactVerifier: alwaysUnsignedVerifier{},
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0", InstalledBy: "test-operator",
		AllowUnsigned: true, TTY: false, EnvVarSet: true, OperatorAllowUnsigned: true,
	})
	require.NoError(t, err)
	require.NotNil(t, res)

	m, err := registry.LoadManifest(filepath.Join(connectorsPath, ".registry", "manifest.json"))
	require.NoError(t, err)
	entry, ok := m.Installs["widget@1.0.0"]
	require.True(t, ok)
	assert.False(t, entry.Signed)
	assert.True(t, entry.AllowUnsigned)
	assert.Empty(t, entry.VerifiedIdentity)

	logData, err := os.ReadFile(filepath.Join(connectorsPath, ".registry", "unsigned-installs.log"))
	require.NoError(t, err)
	assert.Contains(t, string(logData), `"connector":"widget"`)
	assert.Contains(t, string(logData), `"context":"non-interactive-env"`)
}

func TestAllowUnsigned_InteractiveConfirmed_InstallsAndAuditsAsTTY(t *testing.T) {
	srv, indexURL, _ := newInstallTestServer(t, "widget", "1.0.0")
	defer srv.Close()
	connectorsPath := t.TempDir()

	_, err := registry.Install(context.Background(), registry.InstallOptions{
		Name: "widget", ConnectorsPath: connectorsPath, IndexURL: indexURL,
		IndexVerifier: passThroughVerifier{}, ArtifactVerifier: alwaysUnsignedVerifier{},
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
		AllowUnsigned: true, TTY: true, TypedConfirmation: true, OperatorAllowUnsigned: true,
	})
	require.NoError(t, err)

	logData, err := os.ReadFile(filepath.Join(connectorsPath, ".registry", "unsigned-installs.log"))
	require.NoError(t, err)
	assert.Contains(t, string(logData), `"context":"tty"`)
}

func TestAllowUnsigned_InteractiveNotConfirmed_Refuses(t *testing.T) {
	srv, indexURL, _ := newInstallTestServer(t, "widget", "1.0.0")
	defer srv.Close()
	connectorsPath := t.TempDir()

	_, err := registry.Install(context.Background(), registry.InstallOptions{
		Name: "widget", ConnectorsPath: connectorsPath, IndexURL: indexURL,
		IndexVerifier: passThroughVerifier{}, ArtifactVerifier: passThroughVerifier{},
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
		AllowUnsigned: true, TTY: true, TypedConfirmation: false, OperatorAllowUnsigned: true,
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, policy.CodeUnsignedInstallNonInteractive, ce.Code)
	assertNoInstalledBinary(t, connectorsPath)
}

func TestAllowUnsigned_MCPAlwaysRefuses_EvenWithEverythingElseFavorable(t *testing.T) {
	srv, indexURL, _ := newInstallTestServer(t, "widget", "1.0.0")
	defer srv.Close()
	connectorsPath := t.TempDir()

	_, err := registry.Install(context.Background(), registry.InstallOptions{
		Name: "widget", ConnectorsPath: connectorsPath, IndexURL: indexURL,
		IndexVerifier: passThroughVerifier{}, ArtifactVerifier: passThroughVerifier{},
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
		AllowUnsigned: true, TTY: true, EnvVarSet: true, TypedConfirmation: true, OperatorAllowUnsigned: true,
		IsMCP: true,
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, policy.CodeUnsignedInstallNonInteractive, ce.Code)
	assertNoInstalledBinary(t, connectorsPath)
}

// truncatingRoundTripper wraps http.DefaultTransport but chops the last
// byte off any response whose body it can read, so the received bytes
// never match the index's declared sha256 — a corrupted download.
type truncatingRoundTripper struct{}

func (truncatingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = &truncateOneByteReadCloser{rc: resp.Body}
	return resp, nil
}

type truncateOneByteReadCloser struct {
	rc interface {
		Read([]byte) (int, error)
		Close() error
	}
	dropped bool
}

func (t *truncateOneByteReadCloser) Read(p []byte) (int, error) {
	n, err := t.rc.Read(p)
	if n > 0 && !t.dropped {
		n--
		t.dropped = true
	}
	return n, err
}

func (t *truncateOneByteReadCloser) Close() error { return t.rc.Close() }

// TestAllowUnsigned_NeverSkipsCorruptionCheck is the single most important
// test in this file: --allow-unsigned skips ONLY the signature/provenance
// check, NEVER the sha256 corruption check (plan-v2 §4 step 7 / P1-3). A
// corrupted download must refuse with CodeCorruptDownload even with every
// AllowUnsigned/policy field maximally permissive.
func TestAllowUnsigned_NeverSkipsCorruptionCheck(t *testing.T) {
	srv, indexURL, _ := newInstallTestServer(t, "widget", "1.0.0")
	defer srv.Close()
	connectorsPath := t.TempDir()

	_, err := registry.Install(context.Background(), registry.InstallOptions{
		Name: "widget", ConnectorsPath: connectorsPath, IndexURL: indexURL,
		IndexVerifier: passThroughVerifier{}, ArtifactVerifier: passThroughVerifier{},
		RunningConduitVersion: "1.0.0", RunningProtocolVersion: "1.0.0",
		AllowUnsigned: true, TTY: false, EnvVarSet: true, OperatorAllowUnsigned: true,
		HTTPClient: &http.Client{Transport: truncatingRoundTripper{}},
	})
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeCorruptDownload, ce.Code)
	assertNoInstalledBinary(t, connectorsPath)
}
