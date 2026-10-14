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

package index_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/matryer/is"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry/index"
)

// TestVerify_FreshnessOnlyResignChangingTierOrLicenseRejected guards the
// claim that tier and license are content, not liveness metadata: the
// unattended freshness key may only extend index.timestamp / bump
// index.version over content the client last root-verified. A freshness-only
// re-sign that changes nothing but a tier or a license must be refused with
// CodeIndexIntegrity, exactly like any other content change, so the freshness
// key can never relabel a community connector as certified.
func TestVerify_FreshnessOnlyResignChangingTierOrLicenseRejected(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(p *testPayload)
	}{
		{"connector tier relabelled", func(p *testPayload) { p.Connectors[0].Tier = index.TierCertified }},
		{"connector tier added", func(p *testPayload) { p.Connectors[1].Tier = index.TierCertified }},
		{"connector tier removed", func(p *testPayload) { p.Connectors[0].Tier = "" }},
		{"connector license changed", func(p *testPayload) { p.Connectors[0].License = "MIT" }},
		{"processor tier relabelled", func(p *testPayload) { p.Processors[0].Tier = index.TierCertified }},
		{"processor license changed", func(p *testPayload) { p.Processors[0].License = "Apache-2.0 OR MIT" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			b := newTestIndexBuilder(t)
			payload := tierLicensePayload()

			// The client last root-verified this content.
			lastHash, err := index.HashContentSubtree(payload.Connectors, payload.Processors)
			is.NoErr(err)
			_, err = index.Verify(b.sign(t, payload, "root"), b.anchors(t), "")
			is.NoErr(err)

			// Freshness-only re-sign: bumped version and timestamp, plus one
			// tier/license edit and nothing else.
			tt.mutate(&payload)
			payload.Index.Version = 2
			payload.Index.Timestamp = time.Now().UTC()
			raw := b.sign(t, payload, "freshness")

			_, err = index.Verify(raw, b.anchors(t), lastHash)
			is.True(err != nil)
			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.Equal(ce.Code.Reason(), index.CodeIndexIntegrity.Reason())

			// The same edit under a root signature is a legitimate content
			// change and verifies.
			_, err = index.Verify(b.sign(t, payload, "root"), b.anchors(t), lastHash)
			is.NoErr(err)
		})
	}
}

// TestHashContentSubtree_TierAndLicenseDriftAcrossVersions pins a known,
// accepted consequence of hashing the typed content subtree, documented as a
// failure mode in docs/design-documents/20261008-registry-license-and-tier.md.
//
// HashContentSubtree marshals the client's own structs, so a build that
// predates license/tier drops those keys before hashing. Once an entry
// carries them, an older build and this build compute different content
// hashes for the same index bytes. A client upgraded or downgraded across
// this change therefore holds a lastVerifiedContentHash the other version
// does not reproduce, and a freshness-only index then fails with
// CodeIndexIntegrity even though nothing was tampered with. The published
// index is always root-signed (index-sign.yml offers no freshness role), so
// this does not occur today; the next root-signed index clears it.
//
// For entries without the fields, omitempty keeps both hashes identical,
// which is why the upgrade is drift-free until the first entry uses them.
func TestHashContentSubtree_TierAndLicenseDriftAcrossVersions(t *testing.T) {
	is := is.New(t)

	plain := defaultTestPayload()
	current, err := index.HashContentSubtree(plain.Connectors, plain.Processors)
	is.NoErr(err)
	is.Equal(legacyContentHash(t, plain), current) // no fields: no drift

	withFields := tierLicensePayload()
	current, err = index.HashContentSubtree(withFields.Connectors, withFields.Processors)
	is.NoErr(err)
	is.True(legacyContentHash(t, withFields) != current) // fields set: drift
}

// tierLicensePayload is defaultTestPayload with a processor, a second
// connector without the new fields, and tier/license set on the first
// connector and the processor.
func tierLicensePayload() testPayload {
	p := withProcessor(defaultTestPayload())
	second := p.Connectors[0]
	second.Name = "example-two"
	p.Connectors = append(p.Connectors, second)
	p.Connectors[0].Tier = index.TierCommunity
	p.Connectors[0].License = "Apache-2.0"
	p.Processors[0].Tier = index.TierCommunity
	p.Processors[0].License = "Apache-2.0"
	return p
}

// legacyContentHash reproduces HashContentSubtree as a build before license
// and tier would compute it: unmarshal into the pre-change shapes (which drop
// the unknown keys), then marshal, canonicalize and hash the same way.
func legacyContentHash(t *testing.T, p testPayload) string {
	t.Helper()
	is := is.New(t)
	raw, err := json.Marshal(struct {
		Connectors []index.Connector `json:"connectors"`
		Processors []index.Processor `json:"processors"`
	}{p.Connectors, p.Processors})
	is.NoErr(err)
	var legacy struct {
		Connectors []legacyConnectorV1 `json:"connectors"`
		Processors []legacyProcessorV1 `json:"processors"`
	}
	is.NoErr(json.Unmarshal(raw, &legacy))
	out, err := json.Marshal(legacy)
	is.NoErr(err)
	canonical, err := index.Canonicalize(out)
	is.NoErr(err)
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}
