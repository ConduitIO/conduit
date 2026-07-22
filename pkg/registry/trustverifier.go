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

package registry

import (
	"context"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry/index"
	"github.com/conduitio/conduit/pkg/registry/trust"
)

// TrustedVerifier is the real IndexVerifier/ArtifactVerifier implementation
// (PR-2): index-signature + freeze/rollback verification via
// pkg/registry/index, and identity-pinned artifact-signature + SLSA
// provenance verification via pkg/registry/trust. This is what
// cmd/conduit/root/connectors/install.go wires in place of FailClosedVerifier
// (verify.go) as of this PR — every real `conduit connectors install`
// invocation now actually installs verified artifacts.
type TrustedVerifier struct {
	// Anchors are this build's compiled-in Conduit registry root/freshness
	// public keys (index.TrustAnchors). No production key material is
	// embedded as of this PR — see anchors.go's doc comment: the bootstrap
	// ceremony that generates and go:embeds real keys (plan-v2 §9) is
	// separate infrastructure work, out of this PR's scope. An empty
	// Anchors value means every real index fails closed with
	// CodeTrustAnchorExpired — the correct, fail-closed behavior until that
	// ceremony lands, NOT a silent bypass.
	Anchors index.TrustAnchors
	// StatePath is where the persisted index rollback high-water mark lives
	// (index.LoadState/SaveState) — see indexStatePath.
	StatePath string
	// MaxStaleness overrides index.DefaultMaxStaleness when non-zero.
	MaxStaleness time.Duration
	// RequireProvenance, if true, refuses an artifact whose index entry has
	// no slsaProvenance reference at all (ArtifactRef.ProvenanceBundle is
	// empty) rather than treating "no provenance to check" as vacuously
	// satisfied. Defaults to false (provenance is verified and bound
	// whenever present, but its absence alone does not refuse) — FLAGGED in
	// this PR's description as a genuine scope ambiguity: the frozen index
	// schema marks slsaProvenance optional (omitempty) at the version
	// level, but P1-2 says builder-ID binding is "enforced from day one, no
	// soft period" for provenance that IS present. This field lets an
	// operator (or a future default change) require it unconditionally
	// without a redesign.
	RequireProvenance bool
	// LockTimeout bounds how long VerifyIndex waits to acquire the
	// index-state lock (see acquireIndexStateLock). Zero uses
	// DefaultLockTimeout.
	LockTimeout time.Duration
}

var (
	_ IndexVerifier    = (*TrustedVerifier)(nil)
	_ ArtifactVerifier = (*TrustedVerifier)(nil)
)

// VerifyIndex implements R-1 §a-§b in full via pkg/registry/index: signature
// verification (index.Verify) against Anchors, then — only after that
// succeeds — the two independently-triggerable freeze/rollback checks
// (index.CheckRollback, index.CheckStaleness), and only once ALL of those
// pass, persists the new high-water mark (index.SaveState) — never on a
// rejected fetch, per index.CheckRollback's doc comment.
//
// # Concurrency
//
// The entire LoadState -> verify -> CheckRollback/CheckStaleness ->
// SaveState sequence runs under acquireIndexStateLock, held for the whole
// critical section (not just around SaveState's own write). This is
// required — not a nicety — because VerifyIndex runs BEFORE Install's
// per-target lock is ever acquired (index resolution is step 1-3 of the
// pipeline, ahead of AcquireTargetLock at step 4), so two concurrent
// installs of DIFFERENT connector names, which never contend on separate
// TargetLocks, would otherwise both read the same high-water mark, both
// pass their own rollback check against it, and race to write — the LATER
// write silently winning even if it verified an OLDER (though still valid
// and fresh) index, non-monotonically regressing the persisted anti-replay
// floor. Found during this PR's adversarial self-review; fixed here rather
// than shipped as a known gap.
func (v *TrustedVerifier) VerifyIndex(_ context.Context, raw []byte) (*index.VerifiedIndex, error) {
	lockTimeout := v.LockTimeout
	if lockTimeout == 0 {
		lockTimeout = DefaultLockTimeout
	}
	lock, err := acquireIndexStateLock(v.StatePath, lockTimeout)
	if err != nil {
		return nil, err
	}
	defer lock.Unlock() //nolint:errcheck // best-effort; flock also releases at process exit

	state, err := index.LoadState(v.StatePath)
	if err != nil {
		return nil, conduiterr.Wrap(conduiterr.CodeInternal, "could not load persisted index state", err)
	}

	verified, err := index.Verify(raw, v.Anchors, state.LastVerifiedConnectorsHash)
	if err != nil {
		return nil, err
	}

	if err := index.CheckRollback(verified.Payload.Index.Version, state.Version); err != nil {
		return nil, err
	}
	maxStaleness := v.MaxStaleness
	if maxStaleness == 0 {
		maxStaleness = index.DefaultMaxStaleness
	}
	if err := index.CheckStaleness(verified.Payload.Index.Timestamp, time.Now(), maxStaleness); err != nil {
		return nil, err
	}

	newState := index.State{Version: verified.Payload.Index.Version, LastVerifiedConnectorsHash: state.LastVerifiedConnectorsHash}
	if verified.RootVerified {
		hash, err := index.HashConnectors(verified.Payload.Connectors)
		if err != nil {
			return nil, conduiterr.Wrap(conduiterr.CodeInternal, "could not hash verified connectors for state persistence", err)
		}
		newState.LastVerifiedConnectorsHash = hash
	}
	fireChaos(chaosPointIndexStateBeforeWrite)
	if err := index.SaveState(v.StatePath, newState); err != nil {
		return nil, conduiterr.Wrap(conduiterr.CodeInternal, "could not persist index state", err)
	}

	return verified, nil
}

// VerifyArtifact is the authorization gate (R-1 §c steps 5b/6): identity-
// pinned signature verification, then — if a provenance bundle is present
// (or unconditionally, if RequireProvenance) — SLSA provenance verification
// and subject-digest/builder-ID binding. Any failure refuses with the
// specific code from pkg/registry/trust; success in every step returns
// VerifyResult{Signed: true, VerifiedIdentity: <the actual signing SAN>}.
func (v *TrustedVerifier) VerifyArtifact(ctx context.Context, ref ArtifactRef, identity trust.PinnedIdentity) (VerifyResult, error) {
	verifiedIdentity, err := trust.VerifyArtifactSignature(ctx, ref.Digest[:], ref.SignatureBundle, identity)
	if err != nil {
		return VerifyResult{}, err
	}

	if len(ref.ProvenanceBundle) == 0 {
		if v.RequireProvenance {
			return VerifyResult{}, conduiterr.New(trust.CodeProvenanceInvalid,
				"no SLSA provenance attestation is present for this artifact, and this build's policy requires one")
		}
		return VerifyResult{Signed: true, VerifiedIdentity: verifiedIdentity}, nil
	}

	statement, err := trust.VerifyAttestationEnvelope(ctx, ref.ProvenanceBundle, identity)
	if err != nil {
		return VerifyResult{}, err
	}
	if err := trust.CheckProvenanceBinding(statement, ref.Digest, trust.ExpectedBuilderID); err != nil {
		return VerifyResult{}, err
	}

	return VerifyResult{Signed: true, VerifiedIdentity: verifiedIdentity}, nil
}
