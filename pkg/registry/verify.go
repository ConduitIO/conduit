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

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry/index"
	"github.com/conduitio/conduit/pkg/registry/trust"
)

// IndexVerifier authenticates a fetched index's raw bytes before any field
// is trusted for a security decision (R-1 §a). The real implementation
// (PR-2) is backed by pkg/registry/index.Verify; FailClosedVerifier is the
// only production wiring until then.
type IndexVerifier interface {
	VerifyIndex(ctx context.Context, raw []byte) (*index.VerifiedIndex, error)
}

// ArtifactVerifier is the authorization gate: signature + SLSA provenance
// against the connector name's pinned identity (R-1 §c steps 5b/6). The
// real implementation (PR-2) is backed by pkg/registry/trust.
type ArtifactVerifier interface {
	VerifyArtifact(ctx context.Context, ref ArtifactRef, identity trust.PinnedIdentity) (VerifyResult, error)
}

// ArtifactRef bundles what VerifyArtifact needs: the digest computed from
// actually-received bytes (the corruption check, CodeCorruptDownload, run
// first and separately from trust verification) and the two fetched
// Sigstore bundles. The install orchestrator (pkg/registry install
// pipeline, PR-1) fetches artifact.signature.bundleURL and the applicable
// slsaProvenance.bundleURL — via pkg/registry/boundedfetch, capped per
// P0-2 — before calling VerifyArtifact; this package never fetches on its
// own.
type ArtifactRef struct {
	Digest           [32]byte
	SignatureBundle  []byte
	ProvenanceBundle []byte
}

// VerifyResult is what a successful (or explicitly policy-bypassed)
// artifact verification reports back to the installer, for the manifest's
// Signed/VerifiedIdentity fields (§3).
type VerifyResult struct {
	// Signed is false only when the pkg/registry/policy --allow-unsigned
	// path explicitly bypassed verification.
	Signed bool
	// VerifiedIdentity is the SAN that signed the artifact, once PR-2
	// lands.
	VerifiedIdentity string
}

// ErrVerificationNotConfigured is returned by
// FailClosedVerifier.VerifyArtifact. Its presence in an error chain means:
// no real trust core is wired into this build, so connector installation
// is refused unconditionally.
var ErrVerificationNotConfigured = conduiterr.New(CodeVerificationUnavailable,
	"artifact verification is not configured in this build — connector installation is refused "+
		"until the trust core (signature + provenance verification) is wired in")

// FailClosedVerifier is the ONLY IndexVerifier/ArtifactVerifier this
// package wires into production Install by default until PR-2 lands. It
// cannot regress into "verification silently skipped": VerifyIndex only
// ever performs a shape/schema check (index.ParseUnverified) and marks the
// result Verified: false; VerifyArtifact unconditionally refuses. The
// install pipeline (PR-1) takes both verifiers as required constructor
// arguments with no default — the one production call site passes
// FailClosedVerifier{} explicitly and visibly until PR-2 swaps it for the
// real trust-backed implementation.
type FailClosedVerifier struct{}

var (
	_ IndexVerifier    = FailClosedVerifier{}
	_ ArtifactVerifier = FailClosedVerifier{}
)

// VerifyIndex performs a shape/schema check ONLY (index.ParseUnverified) —
// never a trust decision — and reports the result as Verified: false, so a
// caller cannot mistake it for a cryptographically-checked index by
// accident.
func (FailClosedVerifier) VerifyIndex(_ context.Context, raw []byte) (*index.VerifiedIndex, error) {
	payload, err := index.ParseUnverified(raw)
	if err != nil {
		return nil, err
	}
	return &index.VerifiedIndex{Payload: *payload, Verified: false}, nil
}

// VerifyArtifact always refuses: no real trust core exists in this build.
func (FailClosedVerifier) VerifyArtifact(_ context.Context, _ ArtifactRef, _ trust.PinnedIdentity) (VerifyResult, error) {
	return VerifyResult{}, ErrVerificationNotConfigured
}
