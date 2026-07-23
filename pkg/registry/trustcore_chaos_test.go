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
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/pkg/registry"
	"github.com/conduitio/conduit/pkg/registry/index"
)

// TestChaosHelperProcess_IndexStateWrite is
// TestInstall_ChaosKill_IndexStateWrite's subprocess: it calls
// TrustedVerifier.VerifyIndex directly (not the full Install pipeline —
// VerifyIndex's own state.json write is what this test is about) with the
// chaos hook set to os.Exit(137) at ChaosPointIndexStateBeforeWrite.
func TestChaosHelperProcess_IndexStateWrite(t *testing.T) {
	if os.Getenv("CONDUIT_CHAOS_HELPER") != "1" {
		t.Skip("only runs as TestInstall_ChaosKill_IndexStateWrite's subprocess")
	}

	registry.SetChaosHookForTest(func(p string) {
		if p == registry.ChaosPointIndexStateBeforeWrite {
			os.Exit(137)
		}
	})

	rootPub := decodeTestPub(t, os.Getenv("CONDUIT_CHAOS_ROOT_PUB"))
	tv := &registry.TrustedVerifier{
		Anchors:   index.TrustAnchors{Roots: map[string]ed25519.PublicKey{mustKeyIDStr(t, rootPub): rootPub}},
		StatePath: os.Getenv("CONDUIT_CHAOS_STATE_PATH"),
	}
	raw, err := os.ReadFile(os.Getenv("CONDUIT_CHAOS_INDEX_PATH"))
	if err != nil {
		os.Exit(1)
	}
	if _, err := tv.VerifyIndex(context.Background(), raw); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func decodeTestPub(t *testing.T, b64 string) ed25519.PublicKey {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(b64)
	require.NoError(t, err)
	return ed25519.PublicKey(raw)
}

func mustKeyIDStr(t *testing.T, pub ed25519.PublicKey) string {
	t.Helper()
	id, err := index.KeyID(pub)
	require.NoError(t, err)
	return id
}

func signedIndexBytes(t *testing.T, rootPub ed25519.PublicKey, rootPriv ed25519.PrivateKey, version int64) []byte {
	t.Helper()
	payload := index.Payload{
		SchemaVersion: 1,
		Index:         index.IndexMeta{Version: version, Timestamp: time.Now()},
		Connectors:    []index.Connector{{Name: "widget", Publisher: index.Publisher{ExpectedOIDCIssuer: "https://x", ExpectedIdentityPattern: "^x$"}}},
	}
	payloadRaw, err := json.Marshal(payload)
	require.NoError(t, err)
	canonical, err := index.Canonicalize(payloadRaw)
	require.NoError(t, err)
	sig := ed25519.Sign(rootPriv, canonical)
	envelope := map[string]any{"payload": payload, "signatures": []map[string]any{
		{"role": "root", "keyId": mustKeyIDStr(t, rootPub), "algorithm": "ed25519", "signature": base64.StdEncoding.EncodeToString(sig)},
	}}
	data, err := json.Marshal(envelope)
	require.NoError(t, err)
	return data
}

// TestInstall_ChaosKill_IndexStateWrite is PR-2's own kill-mid-write chaos
// test (plan-v2 §15.3): SIGKILL the process at the exact point between
// "index verification succeeded, computed the new high-water mark" and
// "persisted it" — assert index-state.json is never torn (either absent or
// the prior complete value), and that a subsequent VerifyIndex call still
// works correctly (recovers, and correctly re-derives/updates the
// high-water mark) rather than wedging or silently regressing rollback
// protection.
func TestInstall_ChaosKill_IndexStateWrite(t *testing.T) {
	rootPub, rootPriv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	connectorsPath := t.TempDir()
	statePath := registry.IndexStatePath(connectorsPath)
	indexPath := filepath.Join(t.TempDir(), "index.json")
	require.NoError(t, os.WriteFile(indexPath, signedIndexBytes(t, rootPub, rootPriv, 9), 0o644))

	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestChaosHelperProcess_IndexStateWrite$", "-test.v")
	cmd.Env = append(os.Environ(),
		"CONDUIT_CHAOS_HELPER=1",
		"CONDUIT_CHAOS_ROOT_PUB="+base64.StdEncoding.EncodeToString(rootPub),
		"CONDUIT_CHAOS_STATE_PATH="+statePath,
		"CONDUIT_CHAOS_INDEX_PATH="+indexPath,
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	runErr := cmd.Run()
	var exitErr *exec.ExitError
	require.ErrorAsf(t, runErr, &exitErr, "subprocess output:\n%s", out.String())
	assert.Equalf(t, 137, exitErr.ExitCode(), "subprocess should have been killed at the index-state write point; output:\n%s", out.String())

	// Invariant: index-state.json is either completely absent (a crash
	// before any prior successful write ever happened) or fully valid JSON
	// — LoadState must never see a torn/partial file.
	_, loadErr := index.LoadState(statePath)
	require.NoError(t, loadErr, "index-state.json must never be left as a torn/partial file after a kill mid-write")

	// A subsequent, real VerifyIndex call against the SAME (now
	// higher-version) index must still succeed and correctly persist the
	// high-water mark — the killed process must not have wedged anything
	// (there is no lock here to stale-wedge, but the state file itself
	// must remain usable).
	tv := &registry.TrustedVerifier{
		Anchors:   index.TrustAnchors{Roots: map[string]ed25519.PublicKey{mustKeyIDStr(t, rootPub): rootPub}},
		StatePath: statePath,
	}
	raw, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	_, err = tv.VerifyIndex(context.Background(), raw)
	require.NoError(t, err)

	st, err := index.LoadState(statePath)
	require.NoError(t, err)
	assert.Equal(t, int64(9), st.Version)
}
