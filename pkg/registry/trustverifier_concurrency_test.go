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
	"crypto/ed25519"
	"encoding/base64"
	"sync"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry"
	"github.com/conduitio/conduit/pkg/registry/index"
)

// TestTrustedVerifier_ConcurrentVerifyIndex_NeverRegressesHighWaterMark is
// the regression test for the concurrency finding from this PR's
// adversarial self-review: TrustedVerifier.VerifyIndex's
// LoadState->verify->SaveState sequence runs BEFORE Install's per-target
// lock is ever acquired, so two concurrent installs of DIFFERENT connector
// names (which never contend on a TargetLock) could otherwise both read
// the same high-water mark and race to write it back, letting a LOWER
// (though independently valid at its own read time) version silently win
// over a higher one already persisted — a non-monotonic regression of the
// anti-replay floor. acquireIndexStateLock (trustverifier.go) fixes this by
// making the whole read-modify-write sequence one atomic critical section.
//
// Two fixed versions (higher=100, lower=50) are verified concurrently,
// repeated across many trials with a fresh state file each time, so
// whichever goroutine's call happens to acquire the lock second observes
// the FIRST one's already-persisted result. Both trial orderings are
// legitimate and are asserted for explicitly:
//   - higher acquires the lock first: it succeeds (100 > initial 0); lower
//     then correctly REFUSES with CodeIndexRollback (50 < 100 — this is the
//     lock proving its job, not a bug).
//   - lower acquires the lock first: it succeeds (50 > initial 0); higher
//     then also succeeds (100 > 50).
//
// In EVERY trial, regardless of which ordering the scheduler picks, the
// final persisted state.Version must be exactly the higher value — never
// silently regressed by a lost update — and at most one of the two calls
// may ever return an error, which must be CodeIndexRollback specifically
// (a legitimate refusal), never anything else (which would indicate
// corruption or a lock-timeout/contention bug instead).
func TestTrustedVerifier_ConcurrentVerifyIndex_NeverRegressesHighWaterMark(t *testing.T) {
	const higher, lower = int64(100), int64(50)

	for trial := 0; trial < 20; trial++ {
		rootPub, rootPriv, err := ed25519.GenerateKey(nil)
		require.NoError(t, err)
		statePath := registry.IndexStatePath(t.TempDir())
		tv := &registry.TrustedVerifier{
			Anchors:   index.TrustAnchors{Roots: map[string]ed25519.PublicKey{mustKeyIDStr(t, rootPub): rootPub}},
			StatePath: statePath,
		}

		var wg sync.WaitGroup
		errCh := make(chan error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := tv.VerifyIndex(context.Background(), signedIndexForVersion(t, rootPub, rootPriv, higher))
			errCh <- err
		}()
		go func() {
			defer wg.Done()
			_, err := tv.VerifyIndex(context.Background(), signedIndexForVersion(t, rootPub, rootPriv, lower))
			errCh <- err
		}()
		wg.Wait()
		close(errCh)

		errorCount := 0
		for err := range errCh {
			if err == nil {
				continue
			}
			errorCount++
			ce, ok := conduiterr.Get(err)
			require.True(t, ok, "trial %d: unexpected error type: %v", trial, err)
			assert.Equalf(t, index.CodeIndexRollback, ce.Code, "trial %d: the only legitimate error here is a rollback refusal of the lower version, never anything else", trial)
		}
		assert.LessOrEqualf(t, errorCount, 1, "trial %d: at most one of the two concurrent calls may ever be refused", trial)

		st, loadErr := index.LoadState(statePath)
		require.NoError(t, loadErr)
		assert.Equalf(t, higher, st.Version, "trial %d: the persisted high-water mark must always end up at the higher verified version, never regressed by a lost update", trial)
	}
}

func signedIndexForVersion(t *testing.T, rootPub ed25519.PublicKey, rootPriv ed25519.PrivateKey, version int64) []byte {
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
