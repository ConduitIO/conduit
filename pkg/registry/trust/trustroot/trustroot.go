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

// Package trustroot embeds a pinned, offline snapshot of Sigstore's own
// public-good trust root (layer 1: Fulcio CA, Rekor transparency log, CTFE
// public keys — see pkg/registry/trust's package doc for the two-layer
// distinction). This is deliberately separate from pkg/registry/index's
// TrustAnchors (Conduit's OWN registry root/freshness keys, layer 2) — a
// future contributor must never be able to confuse "Conduit's registry root
// key" with "Sigstore's Fulcio root" by proximity (plan-v2 §2.2 / step3 plan
// §2.2).
//
// # Why embedded, not live-fetched (plan-v2 §3.3)
//
// sigstore-go supports fetching a live, auto-updating TrustedRoot via its TUF
// client (root.FetchTrustedRoot / NewLiveTrustedRoot*), but this package
// deliberately does NOT use that path: a live TUF fetch at every `install`
// invocation (a) breaks the fully-offline/air-gapped install story, and (b)
// adds an availability dependency on Sigstore's public infrastructure to
// every Conduit install — a worse failure mode than "occasionally a Conduit
// release ships slightly-stale Sigstore keys" (Sigstore root rotation is
// rare and well-announced). The spike at
// conduit-registry-plans/sigstore-spike/main.go confirmed sigstore-go's core
// verify.Verifier.Verify path never calls the network when given a
// TrustedRoot loaded via root.NewTrustedRootFromPath/FromJSON.
//
// # Refresh cadence
//
// SigstoreTrustedRootJSON should be refreshed on the same cadence as a normal
// dependency bump (e.g. re-fetched from
// https://tuf-repo-cdn.sigstore.dev/targets/ during a release-prep pass, not
// at Conduit runtime) — this is a release-process action item, not something
// this package automates, precisely because automating it would reintroduce
// the live-fetch dependency this package exists to avoid.
package trustroot

import _ "embed"

// SigstoreTrustedRootJSON is Sigstore's public-good TrustedRoot document
// (application/vnd.dev.sigstore.trustedroot+json), embedded at build time.
// Pass it to sigstore-go's root.NewTrustedRootFromJSON to construct a
// root.TrustedMaterial for verify.NewVerifier — see
// pkg/registry/trust/sigstore.go.
//
//go:embed sigstore-trusted-root.json
var SigstoreTrustedRootJSON []byte
