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

package index

import (
	"crypto/sha256"
	"encoding/hex"

	json "github.com/goccy/go-json"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// HashContentSubtree returns "sha256:<hex>" over the JCS-canonicalized
// CONTENT SUBTREE of the index — the {connectors[], processors[]} pair — the
// value persisted in State.LastVerifiedContentHash and compared by Verify's
// freshness-only acceptance path (R-1 §a.2.c; design doc
// 20260727-registry-processor-artifacts, D4): a freshness signature may only
// extend index.timestamp/index.version over BYTE-IDENTICAL content, never
// authorize different content on its own. Hashing (rather than comparing raw
// canonical bytes directly) keeps State small and gives a stable, loggable
// value for diagnostics.
//
// The "content subtree" spans BOTH the connectors[] and processors[]
// collections. It was widened from connectors[]-alone when processors[] was
// added (design doc D4): if the subtree covered only connectors[], the
// unattended freshness key could re-sign an index whose processors[] tree was
// mutated while connectors[] stayed byte-identical — silently authorizing a
// changed processor tree, a content-authorization escalation. Covering both is
// the fix, and TestVerify_FreshnessSignatureWithMutatedProcessorsRequiresRoot
// is its regression guard.
//
// The hash is computed over the two content collections only, NOT the full
// payload (which also carries index.version/timestamp that legitimately change
// on every heartbeat re-sign) — hashing the whole payload would make every
// nightly freshness re-sign look like new content and defeat the byte-identical
// check this function exists to support. The collections are wrapped in a fixed
// {"connectors":...,"processors":...} object before canonicalization so the two
// arrays can never be confused for one another (e.g. moving an entry between
// them changes the hash), and so producer and verifier — which both call this
// one function — agree byte-for-byte.
func HashContentSubtree(connectors []Connector, processors []Processor) (string, error) {
	subtree := struct {
		Connectors []Connector `json:"connectors"`
		Processors []Processor `json:"processors"`
	}{Connectors: connectors, Processors: processors}
	raw, err := json.Marshal(subtree)
	if err != nil {
		return "", cerrors.Errorf("could not marshal content subtree for hashing: %w", err)
	}
	canonical, err := Canonicalize(raw)
	if err != nil {
		return "", cerrors.Errorf("could not canonicalize content subtree for hashing: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
