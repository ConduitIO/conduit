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

import "time"

// MaxSupportedSchemaVersion is the highest payload.schemaVersion this build
// understands. ParseUnverified refuses (CodeSchemaTooNew) anything higher,
// "upgrade Conduit", per R-1 §a's schema-confusion-downgrade rationale for
// keeping schemaVersion inside the signed payload.
const MaxSupportedSchemaVersion = 1

// Payload is the typed schemaVersion-1 shape of the signed payload object —
// the ONLY types index/trust/registry construct a security decision from,
// field-for-field matching
// docs/design-documents/registry-index/index-schema.json. Every field a
// client trusts (urls, sha256, expectedIdentity*, versions, revocations,
// the monotonic counter, the timestamp) lives in here, per R-1 §a.
//
// These types are defined exactly once in the module — pkg/registry and
// pkg/registry/trust reference this package's types rather than declaring
// their own parallel shapes, which was itself one of plan-v2's coherence
// fixes over the original step-plans (§0 item 2).
type Payload struct {
	SchemaVersion int         `json:"schemaVersion"`
	Index         IndexMeta   `json:"index"`
	Connectors    []Connector `json:"connectors"`
	// Processors is the standalone-WASM-processor collection, added additively
	// under schemaVersion 1 (design doc 20260727-registry-processor-artifacts,
	// D1). It is forward-compatible: a client built before this field existed
	// canonicalizes and signature-verifies the WHOLE payload it received —
	// processors[] included — and then, unmarshalling into its older typed
	// Payload, silently ignores the unknown field while still reading
	// connectors[]. MaxSupportedSchemaVersion therefore STAYS 1.
	//
	// The `omitempty` is load-bearing, not cosmetic: a connector-only index
	// (Processors nil/empty) must marshal to bytes byte-identical to the
	// pre-processor schema, so existing connector-only golden fixtures and any
	// persisted content-subtree hash over them do not drift (design doc failure
	// mode 6). Enforced by TestConnectorOnlyIndex_OmitemptyKeepsBytesIdentical.
	Processors []Processor `json:"processors,omitempty"`
}

// IndexMeta is the freeze/rollback-protection metadata (R-1 §b): Version is
// a monotonically increasing counter bumped on every signed rebuild
// (content OR heartbeat re-sign); Timestamp is the RFC 3339 build time
// checked against maxStaleness.
type IndexMeta struct {
	Version   int64     `json:"version"`
	Timestamp time.Time `json:"timestamp"`
}

// Connector is one registered connector name's entry.
type Connector struct {
	Name        string             `json:"name"`
	DisplayName string             `json:"displayName,omitempty"`
	Description string             `json:"description,omitempty"`
	Repository  string             `json:"repository,omitempty"`
	Publisher   Publisher          `json:"publisher"`
	Versions    []ConnectorVersion `json:"versions"`
}

// Publisher is the per-name identity pinning — the actual root-of-trust
// decision for this connector name (R-1 §c). Changing ExpectedOIDCIssuer or
// ExpectedIdentityPattern for an already-registered name requires the same
// human-reviewed path as first registration (R-1 §d).
type Publisher struct {
	ExpectedOIDCIssuer      string      `json:"expectedOIDCIssuer"`
	ExpectedIdentityPattern string      `json:"expectedIdentityPattern"`
	Revoked                 *Revocation `json:"revoked,omitempty"`
}

// Revocation invalidates every version under a connector name regardless of
// individual Yanked status — the compromise is at the identity level, not
// the artifact level (R-1 §e).
type Revocation struct {
	Reason    string     `json:"reason"`
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
	RevokedBy string     `json:"revokedBy,omitempty"`
}

// YankReason marks a single bad release; it does not affect sibling
// versions of the same connector (R-1 §e).
type YankReason struct {
	Reason   string     `json:"reason"`
	YankedAt *time.Time `json:"yankedAt,omitempty"`
	YankedBy string     `json:"yankedBy,omitempty"`
}

// ConnectorVersion is one published release. Entries are append-only once
// published: index-CI rejects a PR that mutates any field of an
// already-present version other than Deprecated, Yanked (R-1 §d item 4).
type ConnectorVersion struct {
	Version            string         `json:"version"`
	ReleasedAt         *time.Time     `json:"releasedAt,omitempty"`
	MinConduitVersion  string         `json:"minConduitVersion"`
	MinProtocolVersion string         `json:"minProtocolVersion"`
	Artifacts          []Artifact     `json:"artifacts"`
	SLSAProvenance     *ProvenanceRef `json:"slsaProvenance,omitempty"`
	// Deprecated has no omitempty: the schema's documented default is false,
	// and the frozen sample index writes it explicitly even when false —
	// dropping it on marshal would fail the golden round-trip test against
	// that fixture.
	Deprecated bool        `json:"deprecated"`
	Yanked     *YankReason `json:"yanked,omitempty"`
}

// Processor is one registered standalone-WASM-processor name's entry. It
// reuses the exact Publisher/Revocation identity-pinning shapes Connector
// does — a processor's root-of-trust decision behaves identically to a
// connector's (design doc D1). It is deliberately a SEPARATE top-level
// collection rather than a Connector with a new artifact kind, so the
// connector selector (registry.SelectArtifact, host-(GOOS,GOARCH)-matched)
// stays untouched and a WASM processor structurally cannot acquire a
// per-platform artifact list (design doc D1, "why a separate collection").
type Processor struct {
	Name        string             `json:"name"`
	DisplayName string             `json:"displayName,omitempty"`
	Description string             `json:"description,omitempty"`
	Repository  string             `json:"repository,omitempty"`
	Publisher   Publisher          `json:"publisher"`
	Versions    []ProcessorVersion `json:"versions"`
}

// ProcessorVersion is one published processor release. Unlike ConnectorVersion
// it carries exactly ONE arch-neutral Artifact (a single GOOS=wasip1
// GOARCH=wasm build runs on every host wazero supports), not a per-(os,arch)
// Artifacts list — so a processor entry cannot smuggle host-targeted artifact
// selection (design doc D2, failure mode 3). Entries are append-only once
// published, exactly like ConnectorVersion: index-CI rejects a PR mutating any
// field of an already-present version other than Deprecated / Yanked.
//
// The Artifact must be selected via registry.SelectProcessorArtifact, which
// asserts Kind == "wasm-processor" and (OS, Arch) == ("wasip1", "wasm") and
// never consults runtime.GOOS/GOARCH.
type ProcessorVersion struct {
	Version           string     `json:"version"`
	ReleasedAt        *time.Time `json:"releasedAt,omitempty"`
	MinConduitVersion string     `json:"minConduitVersion"`
	// MinProtocolVersion is part of the frozen schema and every published
	// entry carries it, but it is NOT consulted by processor resolution
	// (registry.checkProcessorCompatible): there is no
	// conduit-processor-protocol module for a processor build to report a
	// version of, so there is nothing honest to compare it against (issue
	// #2818). It is present-but-ignored deliberately, rather than removed
	// from the type, so this stays a pure additive/client-side fix: dropping
	// the field would require re-signing every already-published entry
	// (a role:root-gated, maintainer-approved operation), where ignoring it
	// on read does not.
	MinProtocolVersion string `json:"minProtocolVersion"`
	// Artifact is the single arch-neutral (wasip1/wasm) build for this version,
	// with Kind == "wasm-processor". Not a list: see the type doc.
	Artifact       Artifact       `json:"artifact"`
	SLSAProvenance *ProvenanceRef `json:"slsaProvenance,omitempty"`
	// Deprecated has no omitempty for the same reason ConnectorVersion.Deprecated
	// does not (see above): the schema's documented default is false, the frozen
	// sample index writes it explicitly even when false, and dropping it on
	// marshal would fail the golden round-trip against that fixture.
	Deprecated bool        `json:"deprecated"`
	Yanked     *YankReason `json:"yanked,omitempty"`
}

// Artifact is one (os, arch) build for a version. Signature is per-artifact
// (not per-version) because a cosign blob signature is inherently over one
// specific artifact's digest — R-1's OPEN QUESTIONS documents the
// divergence from the design doc's literal version-level field grouping.
type Artifact struct {
	OS             string         `json:"os"`
	Arch           string         `json:"arch"`
	Kind           string         `json:"kind"`
	URL            string         `json:"url"`
	SHA256         string         `json:"sha256"`
	Size           int64          `json:"size"`
	Signature      SignatureRef   `json:"signature"`
	SLSAProvenance *ProvenanceRef `json:"slsaProvenance,omitempty"`
}

// SignatureRef points at the Sigstore bundle covering one artifact's own
// digest — offline-verifiable (cert chain + Rekor inclusion proof/SET
// embedded), no live Fulcio/Rekor query required at install time.
type SignatureRef struct {
	BundleURL     string `json:"bundleURL"`
	RekorLogIndex *int64 `json:"rekorLogIndex,omitempty"`
}

// ProvenanceRef points at a SLSA provenance attestation bundle, verifiable
// offline the same way as SignatureRef.
type ProvenanceRef struct {
	BundleURL     string `json:"bundleURL"`
	PredicateType string `json:"predicateType"`
}
