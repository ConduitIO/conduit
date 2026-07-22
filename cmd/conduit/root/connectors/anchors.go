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
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry"
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
// absent or fail to parse, init leaves this zero and records errAnchorLoad —
// install/audit/bundle then refuse up front with a clear
// registry.trust_anchors_unavailable rather than letting the operator chase a
// generic trust_anchor_expired. A corrupt embedded key can never ship
// silently: TestEmbeddedTrustAnchorsParse is a CI guard that the real embedded
// files parse into the expected anchor set. Tests override the anchors via
// SetDefaultTrustAnchorsForTest (export_test.go).
var defaultTrustAnchors index.TrustAnchors

// errAnchorLoad records a failure to load the embedded anchor PEMs. Non-nil
// only on a broken/anchor-stripped build (never in a normal release, where the
// two PEMs are embedded and CI-verified). It NEVER permits skipping
// verification — see guardTrustAnchors. Parsing happens here, not in a way
// that could panic the binary, so unrelated commands (e.g. `conduit run`) are
// unaffected by a bad embed.
var errAnchorLoad error

func init() {
	anchors, err := loadEmbeddedTrustAnchors(trustAnchorFS)
	if err != nil {
		errAnchorLoad = err
		return // leave defaultTrustAnchors zero => fail closed, never fail open
	}
	defaultTrustAnchors = anchors
}

// guardTrustAnchors returns a clear, machine-actionable error when this build's
// embedded trust anchors could not be loaded. install/audit/bundle call it
// before constructing the verifier so a broken build reports
// registry.trust_anchors_unavailable ("reinstall conduit") instead of a
// generic expired-anchor message. A load failure can only block — it never
// lets an unverified install through.
func guardTrustAnchors() error {
	if errAnchorLoad != nil {
		return conduiterr.Wrap(registry.CodeTrustAnchorsUnavailable,
			"this conduit build has no usable registry trust anchors (a build/release defect — reinstall a release build of conduit); connector installation cannot verify indexes without them",
			errAnchorLoad)
	}
	return nil
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
