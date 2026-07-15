# Connector registry index schema and trust model

## Summary

This freezes the connector-registry index schema and trust model (step R-1 of
`20260713-connector-registry-mvp.md`) — the security-critical foundation everything else
(`install`, the publish Action, `audit`, index-CI) trusts. It defines a signed index envelope
(`payload` + `signatures[]`, JCS-canonicalized, verify-before-parse), per-name identity pinning, a
separate reviewer-gated **root** key vs. an unattended-nightly **freshness** key (à la TUF's
timestamp role), build-time-fixed trust anchors with server-side co-signature rotation, embedded
revocation, and the registration-vs-version-bump CI split. Companion files:
[`registry-index/index-schema.json`](registry-index/index-schema.json) (JSON Schema, Draft 2020-12)
and [`registry-index/sample-index.json`](registry-index/sample-index.json) (validates against it).

Decisions below were chosen by the maintainer and hardened by two independent security reviews (the
second folded in the freshness-key split, the never-runtime-trusted rotation + trust-anchor-
exhaustion failure mode, the offline root-key backup, and parse-time duplicate-key rejection).
Deferred refinements are listed in Open Questions; none block freezing the MVP schema.

## Resolved decisions — supersede the correspondingly-numbered Open Questions

The maintainer has chosen these defaults; they resolve the load-bearing open questions. The
OPEN QUESTIONS section below is retained for rationale but items 1, 2, 3, 9, 12 are now **decided**
as follows; items 4, 5, 6, 10, 11 remain deferred (refinements or blocked on epic step 4).

- **(OQ1) Root key bootstrap:** the registry **root public key(s) are compiled into the `conduit`
  binary** at release time. No first-fetch trust hole. **Trust anchors are fixed at build time and
  are NEVER updated at runtime** (see rotation) — this closes the security review's OQ1/OQ2
  reconciliation gap.
- **(OQ2) Root key rotation — server-side co-signature, never runtime-auto-trusted.** A rotated key
  reaches a client only when that client upgrades to a `conduit` build compiled with the new key.
  There is **no runtime "rotation statement" artifact** a running client fetches and applies (that
  would let a compromised old key forge a rotation to an attacker key). During rotation the _index_
  is signed by **both** the old and new keys (`signatures[]` carries both) for a **retention window
  that tracks Conduit's supported-version policy** (not a fixed "one release") so already-deployed
  older binaries keep verifying until they upgrade. When a client's compiled-in anchors no longer
  verify any signature on the fetched index → **fail closed with `ERR_TRUST_ANCHOR_EXPIRED`
  ("upgrade Conduit")**, never fall back to a stale cache past `maxStaleness`.
- **(OQ3) Freshness + a SEPARATE freshness-signing key (the security review's central fix).** The
  root key must NOT do double duty as both content authority and unattended nightly freshness
  authority — those contradict "reviewer-gated custody." So there are **two keys**: the reviewer-
  gated **root key** authorizes content (connectors/publishers/versions/revocations), and a
  narrowly-scoped **freshness key** (separate GitHub Environment, usable unattended by the nightly
  job) may **only** extend `index.timestamp` and bump `index.version` on a payload whose
  `connectors[]` subtree is **byte-identical** to the last root-signed content. Each entry in
  `signatures[]` carries a `role` (`root` | `freshness`); a client accepts a `freshness`-only
  signature **only** when the payload's `connectors[]` matches what it last verified under a `root`
  signature, else it requires `root`. `maxStaleness` default = **7 days** (operator-overridable); the
  nightly freshness re-sign (well inside 7 days) prevents false "stale" refusals in quiet periods,
  and because it bumps `index.version`, a same-version-newer-timestamp replay is impossible.
- **(OQ9) Yank/revoke authorization:** MVP routes **both `yanked` and `publisher.revoked` through
  human review** (never auto-merged), same gate as identity registration. To mitigate the "can't
  fast-yank a data-loss release" gap: yank/revoke PRs get their **own notified/paged review queue**
  (distinct from routine registration), and a **client-side emergency denylist** shipped via a
  Conduit hotfix release (same trust tier as the root key) is kept in reserve as a last-resort
  circuit breaker independent of index-review latency. A full self-service yank path is a follow-up.
- **(OQ12) Signature algorithm + custody:** **ed25519** for both the root and freshness keys. The
  **root** private key lives in a **GitHub Environment gated by required reviewers**; the
  **freshness** key in a separate Environment a scheduled job can use unattended. A **sealed offline
  backup of the root key** (separate custody) exists so accidental _loss_ doesn't force restarting
  the trust chain via a hard upgrade of the entire install base. Hardware backing (HSM/KMS) and
  full threshold/multi-key root signing are named v2 hardenings, not MVP.

Additional required additions from the final security review (folded into the sections below):

- **§a duplicate-key rejection:** the JSON parser used to extract `payload` — on the signing side AND
  every verifying side (client + index-CI) — **must reject any object containing a duplicate key at
  any nesting level**, before and independent of JCS canonicalization. JCS fixes serialization
  ambiguity, not parse-time duplicate-key resolution; without this, a producer/verifier parser
  differential is a signature-bypass primitive.
- **§d index-CI re-verification also runs §c step 7** (SLSA provenance subject-digest match +
  `builder.id`/`configSource.uri` binding), so a provenance/artifact mismatch is blocked at merge,
  not only at every individual client install.
- **Registration checklist adds pattern-tightness:** reject an `expectedIdentityPattern` whose
  literal (non-metachar) prefix doesn't contain the pinned `github.com/<owner>/<repo>/`, and reject
  inline regex flags (`(?m)`, `(?s)`) that could weaken `^`/`$` under Go's RE2. Anchoring alone
  (schema-enforced) doesn't stop an overly-broad `^.*$`.
- **Stated assumption:** this trust model rests on the integrity of Conduit's own binary-release
  pipeline (a compromised release could bake in an attacker root key) — out of scope for R-1, stated
  so it is not mistaken for covered. `artifact.url` is **inside** the signed payload (its schema note
  "outside the trust boundary" means only "not itself sufficient for trust," not "unsigned").
  Revocation/yank entries gain an optional `revokedBy`/`yankedBy` chain-of-custody field.
- **(OQ5) Canonicalization library:** a vetted Go RFC 8785 (JCS) implementation, chosen and pinned
  during R-3; the _same_ implementation must run on the build side and index-CI's re-verification
  side. Mechanism (JCS) fixed here; the specific library is an R-3 implementation detail.
- **(OQ6) Per-artifact vs per-version signature/provenance:** **deferred to epic step 4** (the
  publish Action determines the emitted shape); the schema already permits per-artifact `signature`
  and either-level `slsaProvenance`, so step 4 confirms/narrows rather than redesigns.
- **(OQ4/10/11):** TUF-lite→full-TUF trigger, connector delisting, and version pruning remain noted
  future decisions — none block freezing the MVP schema.

---

This implements epic step R-1 of `docs/design-documents/20260713-connector-registry-mvp.md`
("Freeze the index schema + trust model") — the security-critical foundation everything else
(`install`, the publish Action, `audit`, index-CI) trusts. Per that doc's step 1 acceptance
criteria: schema documented + a sample index validates (Draft 2020-12, checked); identity-
registration-vs-version-bump review paths specified; too-new `schemaVersion` refusal specified. The
Resolved-decisions section above records the maintainer's hardened choices; the deferred items
(canonicalization library, per-artifact-vs-version provenance shape, TUF-lite→full-TUF trigger,
delisting, pruning) are refinements that do not block freezing the MVP schema.

Companion files: `registry-index/index-schema.json` (JSON Schema, draft 2020-12), `registry-index/sample-index.json`
(validates against it — 2 connectors: `postgres` with 2 versions, one yanked; `example-vector-sink`
with a revoked publisher).

This is a design artifact, not running code. Nothing here invents a Conduit CLI surface beyond
what the merged design doc already specifies (`install`, `audit`, `uninstall`, `list --installed`).

---

## (a) What is signed, and how

### What must be inside the signature

Everything a client makes a trust decision from must live inside the signed `payload` object:
`schemaVersion`, `index.version`, `index.timestamp`, and the full `connectors[]` tree — names,
`publisher.expectedOIDCIssuer` / `expectedIdentityPattern` / `revoked`, every version's
`artifacts[]` (`url`, `sha256`, `signature`, `slsaProvenance`), `minConduitVersion` /
`minProtocolVersion`, `deprecated`, `yanked`.

**Explicitly outside the signature:** nothing, in this design. The only bytes outside `payload` are
the `signatures[]` array itself (a signature cannot cover itself). There is no unsigned top-level
field. This is a deliberate simplification over some signed-manifest designs that keep a
"convenience" unsigned mirror of a few fields (e.g. an unsigned `latestVersion` hint) for
cache-friendliness — we don't need that here, and every prior unsigned-mirror-field design has
eventually been the bug (a client trusts the mirror by accident, or the mirror drifts from the
signed truth). If a future perf need justifies an unsigned hint field, it must be re-derived and
re-verified against the signed payload before use, never trusted standalone.

`schemaVersion` is _inside_ `payload`, not a sibling of it, for a specific reason: if it lived
outside the signature, a compromised origin/CDN (not holding the root key) could rewrite it freely.
Setting it too high only causes a client to refuse (denial of service — acceptable). Setting it too
_low_ is the dangerous direction: it could point a client at an older/laxer parser for payload bytes
that are otherwise a legitimately-signed newer document, a schema-confusion downgrade. Keeping it
inside the signed payload closes that off entirely — schema selection only happens after the
signature over the bytes containing that same field has already been verified.

### Canonicalization: JCS (RFC 8785)

The signature is **detached**, computed over the canonicalized bytes of `payload`, not over the
literal bytes as served. Rationale:

- JSON has no single canonical byte representation — key order, whitespace, number formatting
  (`1.0` vs `1`), and Unicode escaping all vary between producers/re-serializers without changing
  "meaning." A signature over raw served bytes is fragile: any intermediate that reformats JSON
  (a proxy, a gzip/regenerate step, a git line-ending normalization) breaks verification even
  though nothing security-relevant changed.
- The opposite failure mode is worse: **parser-canonicalization mismatches have been real
  vulnerabilities** in other signed-JSON systems (duplicate keys resolved differently by producer
  vs. verifier, numeric precision differences, Unicode normalization differences). Canonicalizing
  to a single well-specified byte form before signing _and_ before verifying removes the ambiguity
  a class of confusion attacks depends on.
- **RFC 8785 (JCS)** is a narrow, already-standardized answer: sorted object keys (by UTF-16 code
  unit), a fixed number serialization, minimal escaping, no insignificant whitespace. It's
  implementable in a small, auditable amount of code (no giant canonicalization-framework
  dependency), and it's what several signed-manifest ecosystems already use (e.g. some JWS
  profiles, several SBOM/attestation formats). It is the recommended default here.
- **Alternative considered:** sign over raw served bytes, with a documented convention that
  producers must never reformat after signing. Rejected as the default — it's simpler (no
  canonicalization library needed) but pushes the fragility onto operational discipline ("nobody
  ever touches these bytes again") which is exactly the kind of invariant that erodes silently
  over time (a future CDN config, a future re-publish tool) with a failure mode of "verification
  mysteriously breaks," not a security hole per se, but an availability/trust footgun.
- **Alternative considered:** full TUF-style canonical JSON (a stricter ASCII-only variant used by
  the reference TUF implementation). Very close to JCS in spirit; not chosen only because JCS is
  an actual IETF RFC with more general tooling, and TUF-lite doesn't need TUF's exact wire format
  since it isn't adopting TUF's roles wholesale (see Alternatives in the design doc).

Concretely, the signing/verification procedure:

1. **Sign side (index build, CI holding the root key):** build `payload` as a Go struct → marshal
   to JSON → parse that JSON into a generic `map[string]any` (or equivalent) → apply JCS
   canonicalization → sign the resulting UTF-8 bytes with the root key → assemble
   `{payload, signatures: [{keyId, algorithm, signature}]}` and serve _that_ JSON (any
   serialization of the outer document is fine — only `payload`'s canonical form is
   signature-relevant, and it gets re-canonicalized on the verify side anyway).
2. **Verify side (`install`/`audit`):**
   a. Parse the outer document generically enough to extract the `payload` sub-object as a raw
      JSON value and the `signatures` array — **do not** unmarshal `payload` into any
      schema-version-specific typed struct yet.
   b. Canonicalize the extracted `payload` value with the same JCS implementation.
   c. For each entry in `signatures[]`, resolve `keyId` against the client's local trust-anchor
      set (see OPEN QUESTIONS — bootstrap/rotation) and verify `signature` over the canonical
      bytes from (b). **Refuse (`ERR_INDEX_INTEGRITY`) if no signature verifies against a
      currently-trusted key.**
   d. Only after (c) succeeds, unmarshal the canonical `payload` bytes into the typed struct
      selected by the now-trusted `payload.schemaVersion`, and proceed to the freeze/rollback and
      identity-pinning checks below.

Step (a)/(d) ordering is the load-bearing detail: verification happens on the _generic, untyped_
parse, before any schema-version-specific interpretation. This means a client never has to trust
`schemaVersion` (or anything else) to decide _how_ to check the signature — the check is the same
regardless of payload shape, and only succeeds/fails based on cryptography.

---

## (b) Freeze / rollback protection

Two distinct properties, two distinct client checks, two distinct error codes (per the design
doc's failure-modes list, which explicitly wants "stale/frozen" distinguished from "unreachable"):

1. **Rollback protection** (a verified-but-old index is replayed, e.g. by a compromised/misbehaving
   CDN edge that still has a stale cached copy, or a reverted git ref serving an old build):
   - The client persists the highest `index.version` it has successfully verified, locally (see
     OPEN QUESTIONS for exact storage location/format — out of scope for the index schema itself,
     but necessary context for this check to mean anything).
   - On a new fetch, **after** signature verification succeeds: if `payload.index.version <`
     the persisted high-water mark → refuse `ERR_INDEX_ROLLBACK` (distinct from staleness).
   - The high-water mark updates **only** after a fetch passes signature verification, schema
     check, and both freshness checks below — never on a rejected fetch, so an attacker can't
     ratchet the client's trusted floor forward with garbage.
   - **Known gap, stated not hidden:** a client with no prior state (first install, or a wiped
     cache) has no rollback protection on its very first fetch — it can only fall back to the
     staleness check below. This mirrors TUF's own bootstrap-trust limitation and is not solvable
     by the index format alone.

2. **Freshness / anti-freeze** (a verified index that is _legitimately_ the newest the attacker
   has, but is old — a CDN or MITM freezing distribution at a moment before a critical yank):
   - `payload.index.timestamp` must be within `now - maxStaleness` of wall-clock time.
   - **Concrete client check:**

     ```text
     if now() - payload.index.timestamp > maxStaleness:
         refuse ERR_INDEX_STALE  (distinct from ERR_INDEX_UNREACHABLE and ERR_INDEX_ROLLBACK)
     ```

   - `maxStaleness` is a client-side policy value (a sane built-in default, overridable by
     operator config, similar in spirit to `install.allowUnsigned`), **not** a field in the index
     itself — the index can't be trusted to declare its own acceptable staleness. Concrete value:
     **open question**, see below.

3. **Both checks are required**, not either/or: rollback protection alone doesn't stop a frozen
   index that was never rolled back (it's the highest version the attacker has ever served, just
   old); staleness alone doesn't stop a rollback to a recent-enough-to-pass-freshness older version
   if the attacker can still get a validly-signed old build within the staleness window (e.g. they
   captured a signed build from 2 hours ago and the window is 24h) — hence needing the monotonic
   counter _and_ the timestamp, matching TUF's timestamp+snapshot role split in spirit even though
   TUF-lite doesn't split them into separate signed roles (see OPEN QUESTIONS on the "heartbeat"
   tension this bundling creates).

---

## (c) Identity-pinning verification (client steps)

Per connector `name`, once the index itself is verified (§a) and fresh (§b), and the caller has
resolved a specific `(name, version, os, arch)` artifact:

1. Look up `connectors[]` by exact `name` (no fuzzy match). Not found → `ERR_NOT_FOUND`.
2. If `publisher.revoked` is present → refuse `ERR_IDENTITY_REVOKED` with `publisher.revoked.reason`,
   regardless of which version was requested. (This is also what `audit` checks post-install —
   see §e.)
3. Find the requested `version` in `versions[]`. Not found → `ERR_NOT_FOUND`. If found and
   `yanked` is present → refuse `ERR_VERSION_YANKED` with `yanked.reason` (unless the caller is
   resolving "latest compatible," in which case skip yanked versions during selection rather than
   erroring — an explicit `@version` pin to a yanked version is the only case that hard-refuses).
4. Select the artifact entry matching the host `os`/`arch`. Not found → `ERR_NO_ARTIFACT`, listing
   available platforms from the other entries in `artifacts[]`.
5. Download the artifact bytes (§ Decision 3 in the design doc covers staging/atomicity — out of
   scope here). Compute `sha256` of the _received_ bytes; compare to the artifact's `sha256`.
   Mismatch → `ERR_CORRUPT` (integrity, not trust — see the artifact schema's `sha256` field
   description for why this is deliberately a separate, weaker check from what follows).
6. Fetch the bundle at `artifact.signature.bundleURL` (or the per-artifact `slsaProvenance` if
   present, else the version-level `slsaProvenance` — see OPEN QUESTIONS on which shape the
   publish Action will actually emit). Run, conceptually:

   ```sh
   cosign verify \
     --certificate-identity-regexp "<publisher.expectedIdentityPattern>" \
     --certificate-oidc-issuer     "<publisher.expectedOIDCIssuer>" \
     --bundle <local-fetched-signature-bundle> \
     <path-to-downloaded-artifact-or-its-digest>

   cosign verify-attestation \
     --type                        "<slsaProvenance.predicateType>" \
     --certificate-identity-regexp "<publisher.expectedIdentityPattern>" \
     --certificate-oidc-issuer     "<publisher.expectedOIDCIssuer>" \
     --bundle <local-fetched-provenance-bundle> \
     <path-to-downloaded-artifact-or-its-digest>
   ```

   `expectedIdentityPattern` is schema-enforced to be a fully `^...$`-anchored regex (see
   `registry-index/index-schema.json`, `publisher.expectedIdentityPattern`) specifically so
   `--certificate-identity-regexp` can never partial-match an attacker identity that merely
   _contains_ the pinned repo/workflow substring — an unanchored pattern would be a real
   impersonation bypass, not a theoretical one.
   - A verification failure of either call → `ERR_UNSIGNED` (if there's simply no valid signature
     for this identity/issuer at all) or `ERR_IDENTITY_MISMATCH` (if there _is_ a validly-logged
     Rekor signature, but for a different certificate identity than the pinned one) — these must
     stay distinct error codes per the design doc's Decision 1 and Testing section.
7. **Beyond what `cosign verify-attestation` checks structurally:** the client must additionally
   parse the provenance predicate and confirm (a) its `subject[].digest.sha256` list contains an
   entry matching the exact artifact digest just computed in step 5 — not just that _some_ valid
   provenance exists for the version — and (b) `predicate.builder.id` /
   `predicate.invocation.configSource.uri` are consistent with the pinned identity (i.e. the SLSA
   generator that built this actually ran as the pinned repo+workflow, not merely that _a_
   validly-signed attestation exists pointing at unrelated build infra). The exact expected values/
   matching rule for `builder.id` is unresolved — see OPEN QUESTIONS.
8. Only if every check above passes: proceed to the atomic install (Decision 3, step 6 in the
   design doc — outside this document's scope).

---

## (d) Registration vs. version-bump review split, and what index-CI re-verifies

Two structurally different kinds of index PR, gated differently, matching the design doc's "the
trust axis is establishes trust vs. exercises established trust" framing:

**A. Registration / identity change (human-reviewed, never auto-merged):**

A PR is in this bucket if it does _any_ of:

- Adds a new entry to `connectors[]` (a brand-new `name`), or
- Modifies an existing connector's `publisher.expectedOIDCIssuer` or
  `publisher.expectedIdentityPattern` (an org move, workflow rename, key/identity change), or
- Adds or removes `publisher.revoked` (see §e — revocation is treated as trust-root-adjacent, not
  a routine content update, until the OPEN QUESTION on yank/revoke authorization below is
  resolved).

Index-CI's job on this class of PR is to **route it to human review and block auto-merge** — it is
not expected to _approve_ the identity itself (that's the human judgment call: "is this really the
maintainer of `postgres`"). It should still run the mechanical checks in bucket B where applicable
(e.g. if the PR also adds a first version under the new identity).

**B. Version bump from an already-pinned identity (auto-mergeable once CI passes):**

A PR is in this bucket only if the _entire_ diff is confined to adding new entries to an existing
connector's `versions[]` (or the narrow yank/deprecate mutation in §e), with **zero** changes to
`publisher.*` or to any other connector's entries. If a PR mixes a version bump with any
registration-class change, the _whole PR_ is routed to bucket A — a version bump can never be used
to smuggle an identity change past human review by riding alongside it.

For each new version entry, index-CI independently re-derives every trust-relevant field rather
than accepting the PR's assertions:

1. **Re-fetch** the artifact at the claimed `url` (following redirects) — don't trust a
   PR-supplied hash without fetching the actual bytes.
2. **Recompute `sha256`** from the fetched bytes; must equal the PR's claimed value.
3. **Re-run `cosign verify` / `cosign verify-attestation`** against the connector's **currently
   committed** `publisher.expectedOIDCIssuer` / `expectedIdentityPattern` — always the value
   already in `main`, never a value the same PR is trying to introduce (this is what makes bucket
   A vs. B routing security-relevant, not just organizational: re-deriving against the
   already-committed identity is what prevents a version-bump-shaped PR from also being a
   backdoor identity change).
4. **Confirm append-only:** the PR does not modify any field of an already-present `versions[]`
   entry other than `deprecated`, `yanked`/`yanked.reason`/`yanked.yankedAt` (see §e). This isn't
   explicit in the design doc's field list but follows necessarily from "tamper-evident" — without
   it, a compromised-but-still-identity-valid CI run could quietly swap a previously-published,
   already-cached version's `sha256`/`url` for a different, still-validly-signed artifact.
   Confirm with the maintainer this is intended (see OPEN QUESTIONS).
5. **Confirm blast radius:** the PR touches exactly one connector's `versions[]` array (plus,
   optionally, its own `deprecated`/`description`/`displayName` cosmetic fields), not
   `publisher.*`, not any other connector.
6. **`minConduitVersion`/`minProtocolVersion` sanity:** well-formed semver, both present. Whether
   CI should additionally _lint_ (not hard-block) a min-version that regresses below the
   connector's own previous release is a soft open question, not a hard gate — connectors can
   legitimately lower a floor (e.g. after a compatibility fix), so this should warn, not refuse,
   if implemented at all.

A PR failing any of 1–3 is **rejected before merge**, including for otherwise-routine
first-party version bumps — index-CI holds first-party publishers to exactly the same
re-verification as anyone else; "verified" is derived, never trusted from the PR description.

**Operational note:** recommend index-CI verify via `cosign verify --bundle <fetched-bundle>`
(offline verification using the bundle's embedded Rekor inclusion proof/SET) rather than live
Fulcio/Rekor queries, so merge-gating doesn't become flaky/dependent on the public-good Sigstore
instance's uptime during a PR check run. Whether Conduit uses the public-good Sigstore instance at
all (vs. a private Fulcio/Rekor) is itself unresolved — see OPEN QUESTIONS.

---

## (e) Revocation representation and `audit` consumption

**Representation (source of truth, embedded — no separate flattened list):**

- **Version-level:** `connectors[].versions[].yanked { reason, yankedAt? }` — a single bad
  release. Does not affect sibling versions.
- **Publisher-level:** `connectors[].publisher.revoked { reason, revokedAt? }` — invalidates
  _every_ version under that name, regardless of individual `yanked` status, because the
  compromise is at the identity level, not the artifact level.

This deliberately does **not** introduce a separate top-level `revocations[]`/`revocationList`
array duplicating this data. The design doc's language ("a signed revocation list ... is part of
the integrity-checked bundle, not a bolt-on unsigned field") is read here as describing a property
(revocation data is inside the signed payload, full stop) rather than mandating a distinct
structure — `audit` doesn't need a flattened list for performance; it needs an O(1) name lookup
into the already-fetched, already-verified `connectors[]` map, which embedding gives it directly
with no risk of the flattened copy drifting from the embedded source. **This is a judgment call,
flagged for confirmation** — see OPEN QUESTIONS; if there's a concrete reason the design doc wants
a genuinely separate revocation artifact (e.g. so `audit` can fetch a small revocations-only
document instead of the full index for a lightweight periodic check), that changes this shape.

**How `conduit connectors audit` consumes it (conceptually — no new API invented beyond what the
design doc already names):**

1. Run the _same_ verified-index fetch pipeline as `install` (§a–§c's index-level checks; no
   shortcut, no separate lower-trust "audit index" fetch path).
2. Read the local install manifest (`name@version` + resolved digest per install, already
   maintained by `install` per Decision 3 step 6).
3. Build an in-memory `name -> connector` map from the verified `connectors[]`.
4. For each installed entry:
   - Not found in the map at all → connector delisted from the registry. Behavior here is an
     **open question** (flag `UNKNOWN`/needs-investigation vs. treat as implicitly revoked) — see
     OPEN QUESTIONS.
   - `publisher.revoked` present → finding `REVOKED_PUBLISHER`, reason from `publisher.revoked`.
   - Else, installed `version` not found in `versions[]` → **open question** (pruned/retired old
     entry vs. genuine anomaly — see OPEN QUESTIONS); recommend surfacing as `UNKNOWN` rather than
     silently treating "not found" as "fine."
   - Else, `yanked` present on the matching version entry → finding `YANKED_VERSION`, reason from
     `yanked`.
   - Else → no finding.
5. Emit `--json` + human output: connector, installed version, resolved digest, finding type,
   reason, recommended remediation (`uninstall` / `install <name>@<newer-compatible>`).

---

## (f) OPEN QUESTIONS — decisions the maintainer must make

These are flagged rather than guessed, per the design doc's own bar ("a design doc that doesn't
enumerate failure modes is not done") and this task's explicit instruction not to paper over
ambiguity. Roughly ordered by how load-bearing they are.

1. **Root key bootstrap.** How does a client obtain and initially trust the registry root public
   key(s) at all? Options: compiled into the `conduit` binary at release time (simple, but ties
   root-key rotation to a Conduit release cadence — a rotated key doesn't take effect for any
   client until they upgrade); fetched once from a well-known URL and TOFU-pinned locally
   (avoids the release coupling, but reintroduces a first-fetch trust problem the schema itself
   can't solve). This is the actual foundation everything else in §a assumes an answer to, and the
   design doc doesn't specify it. **Needs a decision before R-1 can be considered truly "frozen."**

2. **Root key rotation mechanics.** Given (1), how does a client learn a _new_ key is now also
   trusted, and retire an old one, without reintroducing a bootstrap-trust problem on every
   rotation? A workable pattern: the _old_ key co-signs a rotation statement naming the new
   `keyId`, during an overlap window where the client trusts either; `signatures[]` is already
   shaped to carry multiple entries for exactly this. But the rotation-statement format, storage
   location, and overlap-window length aren't specified here and should be.

3. **Max-staleness value, and the heartbeat-resign tension.** No concrete duration is set. More
   importantly: because §b's timestamp freshness is bundled into the _same_ root-key signature as
   everything else (TUF-lite, not full TUF's separately-rotated timestamp role), keeping the index
   "fresh" during a quiet period (no connector changes for weeks) requires **periodically
   re-signing the index solely to refresh its timestamp**, or every client eventually refuses all
   installs as stale even though nothing is wrong. Two sub-questions: (a) what's the re-sign
   cadence (nightly? weekly?) relative to the staleness window (must be comfortably shorter); (b)
   does a no-op heartbeat rebuild bump `index.version`, and if it does _not_, is a
   same-version-newer-timestamp replay a rollback-check gap worth closing explicitly?

4. **TUF-lite → full TUF trigger.** The design doc names full TUF as "the v2 escalation" without a
   concrete trigger. Worth pinning down even loosely (e.g. "N registered publishers," "a real
   root-key-compromise incident," "the static index no longer fits in a single signed fetch") so
   it's a planned migration rather than a reactive one.

5. **Canonicalization library.** JCS is recommended (§a) but no specific Go implementation is
   named/vetted here. Whatever is chosen must be the _same_ implementation (or a verified
   interoperable one) on both the index-build side and, eventually, index-CI's independent
   re-verification side — a canonicalization mismatch between producer and verifier reintroduces
   exactly the class of bug JCS was chosen to avoid.

6. **Per-artifact vs. per-version `signature`/`slsaProvenance`.** The design doc's field list
   groups `signature` and `slsaProvenance` at the version level, alongside `artifacts[]`. This
   draft places `signature` per-artifact (a cosign blob signature is inherently over one specific
   digest, so one version-level signature can't cover multiple different OS/arch artifacts) and
   allows `slsaProvenance` at _either_ level (a single multi-subject provenance per version is a
   common goreleaser/slsa-github-generator shape, but a per-artifact attestation is equally
   plausible depending on how `conduitio/connector-publish-action` — not yet built, epic step 4 —
   ends up structuring its outputs). **This genuinely can't be resolved until step 4's Action
   design is settled; flagging now so step 4 confirms/corrects this shape rather than discovering
   a schema mismatch after the fact.**

7. **`expectedIdentityPattern` format.** This draft requires it to be a fully anchored (`^...$`)
   regex, enforced at the schema level, specifically to prevent a partial-match impersonation
   bypass via `--certificate-identity-regexp`. Confirm this is the intended mechanism (vs., e.g., a
   simpler glob format, or requiring `--certificate-identity` exact-string match for the common
   single-workflow case and reserving regex for genuinely pattern-based ref/tag matching).

8. **`builder.id` / `configSource.uri` expected-value matching (§c step 7).** The design doc
   requires provenance to bind `builder.id`/`configSource.uri` to the pinned identity but doesn't
   specify the matching rule. Is there one global expected `builder.id` (a single GitHub-hosted
   SLSA L3 generator identity all connectors share, since they're all meant to use the same
   reusable Action), or is this pinned per-connector like `publisher.*`? If it's global, does it
   belong in the schema at all, or is it a client-side constant?

9. **Yank/revoke authorization path.** Who can set `yanked` on a version or `publisher.revoked`,
   and through what review path? This draft's §d treats both as routed like registration-class
   changes (human-reviewed, never auto-merged) as the conservative default, but that means a
   publisher can't self-service-yank their own broken release without going through the same human
   gate as a brand-new identity registration — possibly too slow for "I just shipped a data-loss
   bug, pull it now." A faster self-service yank path needs its own authorization story (how does
   index-CI know a yank request really comes from the connector's own pinned identity, not
   someone else's index-repo PR access?) that isn't designed here.

10. **Connector delisting.** Can a `name` be removed from `connectors[]` entirely, and if so,
    under what review bar (this draft assumes it should be at least as sensitive as identity
    registration, but the design doc doesn't address it, and `audit`'s behavior on a delisted name
    (§e step 4) depends on the answer).

11. **Version-entry retention/pruning.** Does the index ever drop old, non-yanked version entries
    to bound file size as connectors accumulate years of releases, and if so, how does `audit`
    distinguish "pruned, presumed fine" from "genuinely can't verify anymore"? Left as `UNKNOWN`
    in §e pending a decision.

12. **Signature algorithm choice and root-key custody.** `ed25519` is recommended in the schema
    (§ schema `signature.algorithm`) with `ecdsa-p256-sha256` as an HSM-compatibility fallback, but
    the actual choice, and whether the root key lives as a bare GitHub Environment secret vs.
    something with hardware backing, is unresolved.

---

## Summary of what's frozen vs. open

**Frozen by this draft (subject to review):** the envelope shape (`payload` + `signatures[]`,
schemaVersion inside the signed payload), the freeze/rollback fields and two-distinct-error-code
check, the per-name identity-pinning field shapes and their anchoring requirement, the
registration-vs-version-bump PR routing rule and the five concrete things index-CI re-verifies, the
embedded (non-flattened) revocation representation, and the `audit` consumption logic.

**Not frozen — needs a maintainer decision before this can be called done:** root key bootstrap and
rotation (OQ 1–2), the max-staleness value and heartbeat-resign cadence (OQ 3), the
per-artifact-vs-per-version signature/provenance shape (OQ 6, blocked on epic step 4 anyway), and
the yank/revoke authorization path (OQ 9).
