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

package connectors

import (
	"crypto/ed25519"
	"crypto/x509"
	"embed"
	"encoding/pem"
	"io/fs"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/registry/index"
)

// trustAnchorFS embeds the compiled-in registry public signing keys. The
// directory always exists (it carries doc.md); the two PEM files land at
// bootstrap ceremony Gate 2. Embedding the directory — not the individual
// files — is deliberate: it lets this build compile and run BEFORE the keys
// exist, degrading to a fail-closed empty anchor set rather than a build
// break. See trustanchors/doc.md.
//
//go:embed trustanchors
var trustAnchorFS embed.FS

const (
	rootAnchorPath      = "trustanchors/root.pub.pem"
	freshnessAnchorPath = "trustanchors/freshness.pub.pem"
)

// defaultTrustAnchors are this build's compiled-in Conduit registry
// root/freshness public keys (index.TrustAnchors), passed to
// registry.TrustedVerifier by install/audit/bundle.
//
// It is populated by init() from the embedded PEM files. If those files are
// absent (a build that predates the bootstrap ceremony) or fail to parse,
// init leaves this zero — and every real index then fails closed with
// registry.trust_anchor_expired, the correct pre-bootstrap behavior. A
// corrupt embedded key can never ship silently: TestEmbeddedTrustAnchorsParse
// is a CI guard that the real embedded files parse into a non-empty anchor
// set. We deliberately do NOT distinguish "missing/corrupt build" from
// "expired anchor" in the CLI error: both fail closed, the distinction adds
// control-flow risk, and the parse-guard test already prevents shipping a
// broken embed. Tests override the anchors via SetDefaultTrustAnchorsForTest
// (export_test.go).
var defaultTrustAnchors index.TrustAnchors

func init() {
	// On error (e.g. a build that predates the bootstrap ceremony, so the PEM
	// files are absent) leave defaultTrustAnchors zero => fail closed, never
	// fail open, and never panic an unrelated command like `conduit run`.
	if anchors, err := loadEmbeddedTrustAnchors(trustAnchorFS); err == nil {
		defaultTrustAnchors = anchors
	}
}

// loadEmbeddedTrustAnchors reads the root and freshness anchor PEMs from fsys
// and parses each into a keyId-keyed public-key map. Both roles are required:
// a missing or empty file for either is an error (the caller degrades to
// fail-closed).
func loadEmbeddedTrustAnchors(fsys fs.FS) (index.TrustAnchors, error) {
	roots, err := loadAnchorRole(fsys, rootAnchorPath)
	if err != nil {
		return index.TrustAnchors{}, cerrors.Errorf("loading root trust anchors: %w", err)
	}
	freshness, err := loadAnchorRole(fsys, freshnessAnchorPath)
	if err != nil {
		return index.TrustAnchors{}, cerrors.Errorf("loading freshness trust anchors: %w", err)
	}
	return index.TrustAnchors{Roots: roots, Freshness: freshness}, nil
}

func loadAnchorRole(fsys fs.FS, path string) (map[string]ed25519.PublicKey, error) {
	raw, err := fs.ReadFile(fsys, path)
	if err != nil {
		return nil, err
	}
	return parseAnchorPEMs(raw)
}

// parseAnchorPEMs parses one or more concatenated SPKI PUBLIC KEY PEM blocks
// into a map keyed by index.KeyID. Multiple blocks are the rotation window
// (outgoing + incoming key trusted simultaneously). It is strict: any
// non-"PUBLIC KEY" block, any non-ed25519 key, or a file with zero blocks is
// an error — a build must not ship a partially-parseable anchor set.
func parseAnchorPEMs(raw []byte) (map[string]ed25519.PublicKey, error) {
	out := make(map[string]ed25519.PublicKey)
	rest := raw
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "PUBLIC KEY" {
			return nil, cerrors.Errorf("unexpected PEM block type %q (want %q)", block.Type, "PUBLIC KEY")
		}
		pub, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, cerrors.Errorf("parsing SPKI public key: %w", err)
		}
		edPub, ok := pub.(ed25519.PublicKey)
		if !ok {
			return nil, cerrors.Errorf("anchor key is %T, want ed25519.PublicKey", pub)
		}
		keyID, err := index.KeyID(edPub)
		if err != nil {
			return nil, cerrors.Errorf("deriving keyId: %w", err)
		}
		out[keyID] = edPub
	}
	if len(out) == 0 {
		return nil, cerrors.New("no PEM PUBLIC KEY blocks found")
	}
	return out, nil
}
