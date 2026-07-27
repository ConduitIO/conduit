# Registry index: carrying WASM-processor artifacts

## Summary

This design doc extends the frozen connector-registry index schema
([20260714-connector-registry-index-schema](20260714-connector-registry-index-schema.md)) to carry
**standalone WASM-processor artifacts**, so `conduit processor-plugins install` can fetch, verify,
and place them through the same signed trust core connectors use. The decision that processors ride
that one trust core (rather than a parallel registry) is recorded in the companion ADR
([20260727-processors-ride-connector-registry](../architecture-decision-records/20260727-processors-ride-connector-registry.md));
this doc covers the _schema_ change — a signed, frozen, public-contract format — its failure modes,
and its upgrade/rollback story.

**Recommended shape:** add a top-level `processors []Processor` collection to `index.Payload`,
mirroring `connectors[]` and reusing the `Publisher` / `Revocation` / `YankReason` shapes, with a
single arch-neutral artifact per version selected by a fixed `(wasip1, wasm)` rule — **additive under
`schemaVersion` 1** (forward-compatible: older clients ignore it and keep installing connectors).
The load-bearing decision for DeVaris is forward-compat-vs-fail-closed (§ Upgrade/rollback, OQ-1).

This is a design artifact, not running code. It invents no CLI surface beyond what the ADR and the
`processor-plugins install` plan already name.

## Problem

The signed index is connector-only:

- `index.Payload{ SchemaVersion, Index, Connectors []Connector }` — one artifact-bearing collection,
  no processors (`pkg/registry/index/schema_v1.go:36-40`).
- Resolution iterates `payload.Connectors` by exact name (`pkg/registry/resolve.go:97-107`).
- The install pipeline recognizes one artifact `Kind`, `StandaloneArtifactKind = "standalone"`, and
  selects the artifact matching the host `(runtime.GOOS, runtime.GOARCH)`
  (`pkg/registry/platform.go:26-31, 38-47`).

A WASM processor cannot be described by any of that. It is arch-neutral (one `GOOS=wasip1
GOARCH=wasm` build runs everywhere), so host-platform selection would find nothing; and there is no
collection to resolve its name against. The runtime side is already fine — the standalone processor
registry discovers any `.wasm` placed under `--processors.path` and registers it under the name from
its own `Specification()` (`pkg/plugin/processor/standalone/registry.go:296-306`) — so the entire gap
is on the _index/publish_ side. Until it is closed, the `postgres-pgvector-rag` template's
`ai.chunk` / `ai.embed` processors have no verified install path
(`cmd/conduit/root/pipelines/templates/postgres-pgvector-rag/README.md:33-36`).

## Constraints (inherited from the frozen trust model — non-negotiable)

The extension must not weaken any property frozen by the R-1 schema doc:

1. **Everything a client trusts lives inside the signed `payload`.** The processor collection,
   including every artifact `url` / `sha256` / `signature` / `slsaProvenance` and every publisher
   identity, must be _inside_ `payload`, covered by the signature — no unsigned mirror.
2. **Verify-before-parse, on the generic untyped payload.** Signature verification canonicalizes
   (JCS / RFC 8785) and verifies the _whole_ `payload` object's bytes before any schema-version-typed
   unmarshal (R-1 §a). This is what makes the additive change forward-compatible (see below): an older
   client canonicalizes and verifies the exact bytes it received — including `processors[]` — even
   though it will not unmarshal that field into its typed struct.
3. **`schemaVersion` is inside the signed payload** precisely to prevent a schema-confusion downgrade
   (R-1 §a). Any change to how `schemaVersion` gates parsing is security-relevant.
4. **Append-only, tamper-evident.** index-CI rejects a PR mutating any field of an already-published
   version other than `deprecated` / `yanked` (R-1 §d item 4). The processor collection must inherit
   the same append-only rule and the same registration-vs-version-bump review split.
5. **Root vs freshness key split.** The reviewer-gated **root** key authorizes content; the
   unattended **freshness** key may only re-sign when the _content subtree_ is byte-identical to the
   last root-signed content (R-1 §a, OQ3). Today "content subtree" means `connectors[]`; it must be
   redefined to `connectors[]` **and** `processors[]`, or a freshness re-sign could silently authorize
   a changed processor tree.
6. **One trust core.** No new verifier, no forked signature/provenance/identity path (the ADR's
   decision 1).

## Decision

### D1 — A separate `processors []Processor` collection (recommended over a unified `kind`)

Add to `index.Payload`:

```go
type Payload struct {
    SchemaVersion int         `json:"schemaVersion"`
    Index         IndexMeta   `json:"index"`
    Connectors    []Connector `json:"connectors"`
    Processors    []Processor `json:"processors,omitempty"` // NEW
}

// Processor is one registered standalone-WASM-processor name's entry. It
// reuses Publisher/Revocation identity-pinning exactly as Connector does.
type Processor struct {
    Name        string             `json:"name"`
    DisplayName string             `json:"displayName,omitempty"`
    Description string             `json:"description,omitempty"`
    Repository  string             `json:"repository,omitempty"`
    Publisher   Publisher          `json:"publisher"`
    Versions    []ProcessorVersion `json:"versions"`
}

// ProcessorVersion is one published release. Unlike ConnectorVersion it has
// exactly one arch-neutral artifact (wasip1/wasm), not a per-(os,arch) list.
type ProcessorVersion struct {
    Version            string      `json:"version"`
    ReleasedAt         *time.Time  `json:"releasedAt,omitempty"`
    MinConduitVersion  string      `json:"minConduitVersion"`
    MinProtocolVersion string      `json:"minProtocolVersion"`
    Artifact           Artifact    `json:"artifact"` // single, kind:"wasm-processor"
    SLSAProvenance     *ProvenanceRef `json:"slsaProvenance,omitempty"`
    Deprecated         bool        `json:"deprecated"`
    Yanked             *YankReason `json:"yanked,omitempty"`
}
```

The `Artifact` struct is reused as-is; a processor artifact sets `Kind: "wasm-processor"`,
`OS: "wasip1"`, `Arch: "wasm"`. `Publisher`, `Revocation`, `YankReason`, `Artifact`,
`SignatureRef`, `ProvenanceRef` are shared verbatim — identity pinning, revocation, yank, and the
per-artifact Sigstore-bundle signature all behave identically to connectors.

**Why a separate collection rather than a unified artifact list with a new `kind`:** it keeps
`SelectArtifact`'s host-`(GOOS,GOARCH)` logic (correct for connectors) completely untouched and
isolates the "single arch-neutral artifact" selection in a dedicated `ProcessorVersion.Artifact`
field, so a wasm processor structurally _cannot_ accidentally acquire a per-platform artifact list or
be host-matched. A unified list forces `SelectArtifact` to branch on `kind` and keeps the arch-neutral
special case live inside the connector path. Fewer types is not worth reintroducing that coupling into
a security-critical selector.

### D2 — Arch-neutral selection

A processor version has exactly one artifact. A `SelectProcessorArtifact` returns
`ProcessorVersion.Artifact` after asserting `Kind == "wasm-processor"` and
`(OS, Arch) == (wasip1, wasm)` — it never consults `runtime.GOOS`/`runtime.GOARCH`. `SelectArtifact`
(connectors) is unchanged. A malformed processor entry carrying a per-host artifact list, or a host
os/arch, is a schema/validation error, not a silent skip.

### D3 — Resolution and the install pipeline

`Resolve` gains a processor path that iterates `payload.Processors` by exact name (anti-typosquat, no
fuzzy match), newest-non-yanked-compatible when `@version` is omitted, refusing revoked publisher /
yanked pinned version / incompatible min-versions — the _same_ logic as connectors, over the
processor collection. The install pipeline is parameterized (target dir `--processors.path`, filename
prefix `conduit-processor-`, artifact kind, audit label `processor_install`) per the ADR; the
download → verify → atomic-rename → manifest core is shared. See the `processor-plugins install` plan
for the pipeline generalization and install-time WASM validation.

### D4 — Freshness "content subtree" redefinition

The freshness-key acceptance rule (R-1 §a, OQ3) is extended: a `freshness`-only signature is accepted
only when **both** `connectors[]` and `processors[]` are byte-identical to what was last verified
under a `root` signature. Implementation: the freshness re-sign's byte-identical comparison covers the
full content subtree, not just `connectors[]`. Without this, the unattended freshness key could
re-sign a mutated processor tree — a content-authorization escalation.

## Alternatives considered

- **Unified artifact list + new `kind:"wasm-processor"` (rejected — see D1).** Fewer types, but
  forces the security-critical `SelectArtifact` to branch on kind and keeps arch-neutral handling
  inside the connector selector. Rejected to keep the connector path untouched.
- **Bump `schemaVersion` to 2 for the additive change (rejected as the default — see Upgrade).**
  Correct-feeling but costly: every already-deployed client has `MaxSupportedSchemaVersion = 1`
  (`schema_v1.go:23`) and would refuse the _entire_ index with `CodeSchemaTooNew` — breaking
  _connector_ installs for everyone until they upgrade, for a purely additive field they could safely
  ignore. Reserve a version bump for a genuinely non-additive change.
- **Separate processor-only index document served at a different URL (rejected).** Two indexes, two
  fetch/verify pipelines, two freshness stories, two rollback high-water marks. Doubles the trust
  surface for no benefit; contradicts the ADR's one-trust-core decision.
- **A second trust core / unsigned URL fetch (rejected in the ADR).** See the ADR's Alternatives.

## Failure modes

1. **Older client fetches an index that now contains `processors[]`.**
   With the additive-under-v1 choice: the client canonicalizes and verifies the whole payload
   (signature still valid), unmarshals
   into its `schemaVersion:1` struct, and Go silently ignores the unknown `processors` field. It keeps
   installing connectors normally; it simply has no processor-install command and never sees the field.
   _This is the forward-compat behavior — and the exact thing OQ-1 asks DeVaris to confirm is desired
   (vs. fail-closed)._ If instead we want old clients to _reject_ a processor-bearing index, that
   requires a `schemaVersion` bump and its breakage cost (above).
2. **Freshness key re-signs a mutated processor tree.** Prevented by D4 (content subtree covers
   `processors[]`). A test must assert a freshness signature is rejected when only `processors[]`
   differs from the last root-signed content.
3. **Processor entry carries a host-platform artifact list (malformed / spoofed).** `SelectProcessor
   Artifact` asserts the single arch-neutral artifact and `(wasip1, wasm)`; a per-host list or a host
   os/arch is a validation error, not a silent skip (contrast the connector path's deliberate
   skip-unknown-kind). Prevents a processor entry from smuggling host-targeted selection.
4. **Name collision across collections** (`connectors[]` and `processors[]` both have `foo`).
   Legitimate — they are different plugin types resolved by different commands
   (`conduit connectors install foo` vs `conduit processor-plugins install foo`) and land in different
   directories. No cross-collection uniqueness constraint; but index-CI should warn on a cross-collection
   name clash to reduce operator confusion.
5. **Duplicate `name` within `processors[]`.** Same rejection as duplicate connector names — a
   parse-time / index-CI error. The R-1 parse-time duplicate-_key_ rejection (a signature-bypass
   primitive) is unchanged and already covers duplicate JSON keys at any nesting level.
6. **Golden round-trip fixture drift.** Adding `processors[]` (even `omitempty`) changes the canonical
   bytes of any index that includes it. The `index/testdata` golden fixtures and `sample-index.json`
   must be regenerated with at least one processor entry, and the round-trip / canonicalization tests
   updated in the same PR. An `omitempty` empty `processors` must NOT appear for a connector-only index
   (so existing connector-only golden fixtures stay byte-identical) — verify this explicitly, because
   the `Deprecated bool` field's history shows omitempty/default choices are load-bearing for the
   golden tests (`schema_v1.go:98-102`).
7. **index-CI routing gap.** Registering a _processor_ publisher identity (e.g. `conduit-processor-ai`)
   must route to the human-reviewed registration bucket; adding a processor _version_ from an
   already-pinned identity is auto-mergeable only after index-CI re-fetches the artifact, recomputes
   sha256, and re-runs cosign verify/verify-attestation against the _currently committed_ identity —
   the exact §d split, extended to `processors[]`. A routing rule that only inspects `connectors[]`
   would let a processor identity change bypass human review. Must be updated in lockstep.
8. **`predicateType` / provenance shape for wasm.** The publish Action (out-of-repo, Tranche B) emits
   the processor's Sigstore bundle + SLSA provenance; the subject-digest binding (R-1 §c step 7) must
   match the `.wasm` artifact digest. No schema change — `Artifact`/`ProvenanceRef` are reused — but
   flagged so the Action's output shape is confirmed against this, not discovered later.

## Upgrade / rollback

- **Serialized-format change, Tier-1.** The signed index schema gains a collection. **Recommended:
  additive under `schemaVersion` 1**, forward-compatible — older clients ignore `processors[]` and
  keep verifying/installing connectors (failure mode 1). `MaxSupportedSchemaVersion` stays `1`. The
  freeze/canonicalization rules (`pkg/registry/index/freeze.go`, `canonicalize.go`) need no algorithm
  change (JCS already canonicalizes the whole payload), but the golden fixtures and
  `sample-index.json` are regenerated (failure mode 6), and the index-CI append-only + routing rules
  are extended to `processors[]` (failure modes 5, 7). **Ships with an upgrade test:** an older-schema
  client verifies and reads a processor-bearing index without error and still installs a connector
  from it; a current client installs a processor from it.
- **Rollback of the schema change** is the hard part (removing the field after clients have written
  processor manifests) — which is why it lands as its own reviewed, upgrade-tested PR (PR-A in the
  plan) _before_ the CLI, and why the field is additive (an additive field is far cheaper to freeze
  correctly than a structural change).
- **The processor install manifest** reuses `ManifestSchemaVersion = 1` unchanged — a new file at a
  new path (`<processors.path>/.registry/manifest.json`), no migration of existing connector
  manifests.

## Observability

- No new index-side observability surface; the index is a static signed document. Client-side, the
  processor `install`/`uninstall` emit structured `--json` results and `processor_install` /
  `processor_uninstall` audit events (per the plan), mirroring connectors — so a processor's
  provenance/identity/digest is auditable exactly as a connector's is via `conduit connectors audit`'s
  processor-aware sibling.
- index-CI logs the routing decision (registration vs version-bump) and the re-verification result for
  each processor PR, the same as connectors — so a processor identity change is visible in the merge
  trail.

## Open questions for DeVaris

1. **Forward-compat vs fail-closed (the load-bearing one).** Additive under `schemaVersion` 1 —
   recommended — means older clients silently ignore `processors[]` and keep working (they have no
   processor command anyway). The alternative — an older client should _reject_ a processor-bearing
   index — requires a `schemaVersion` bump and breaks _connector_ installs for every un-upgraded client
   until they upgrade. Recommend additive/forward-compat. Confirm.
2. **Schema shape:** separate `processors []Processor` collection (recommended, D1) vs unified list
   with `kind`. Confirm the separate collection.
3. **Cross-collection name clash** (failure mode 4): warn-only in index-CI (recommended) vs hard-reject
   a name present in both `connectors[]` and `processors[]`.
4. **Publish tooling home:** confirm processor publish belongs in `conduit-connector-registry`
   alongside connector publish (one publish pipeline, two artifact kinds) — this doc assumes yes.
5. **`slsaProvenance` level** for a single-artifact processor version: version-level `SLSAProvenance`
   (there is only one artifact, so per-artifact vs per-version collapses) — confirm the publish Action
   emits it at the version level.
