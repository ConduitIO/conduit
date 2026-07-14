# Connector registry MVP: `conduit connectors install`

## Summary

The second half of the v0.18 "UI & extensibility" epic: a **connector registry** so a user (or an
agent) can go from "I need the Postgres connector" to a running, **signature-verified**,
version-compatible connector in one command:

```sh
conduit connectors install postgres          # latest compatible
conduit connectors install postgres@v0.14.0   # pinned
```

MVP shape, chosen for a small team and for GitOps-friendliness:

- **A static, git-backed index** (connector metadata: names, versions, per-OS/arch artifacts, min
  Conduit + protocol version, publisher, signature reference) served from a well-known URL —
  **no hosted service to run.**
- **Artifacts on GitHub Releases** of each connector repo (where they already live), fetched
  through the existing Scarf download gateway for stats.
- **Keyless signing (Sigstore/cosign)** at publish time via a reusable GitHub Action; `install`
  **verifies the signature and provenance before the artifact is ever made runnable**, refusing
  unsigned/unverifiable artifacts unless the user passes an explicit, loud `--allow-unsigned`.
- **Compatibility gate:** the index records each version's min Conduit + connector-protocol
  version; `install` refuses an incompatible artifact with an actionable error instead of
  installing something that will fail at pipeline start.
- **A static registry web UI** (search, verified badges, download stats) built from the same
  index, sharing the React component library with the built-in UI.

"Install" means: place the verified standalone-connector artifact into the configured
`--connectors.path` directory (default `<basePath>/connectors`) and cache it — the exact
directory Conduit already loads standalone connectors from. No new runtime loading path.

## Context

### What exists

- **Two connector kinds:** built-in (compiled into the `conduit` binary) and **standalone**
  (separate processes, gRPC over the connector protocol), loaded from `--connectors.path`
  (`pkg/conduit/config.go`). The registry targets standalone artifacts — the extensibility story.
- **Distribution today is manual:** each `conduit-connector-*` repo publishes GitHub Releases;
  the user downloads the right artifact for their OS/arch and drops it in the connectors dir
  themselves. No discovery, no one-command install, no signature/trust, no compatibility check.
- **`conduit connectors`** has `list`, `describe`, `new` — no `install`.
- **Download stats already flow through Scarf** (`conduit.gateway.scarf.sh`, see `.goreleaser.yml`);
  the registry reuses this rather than building a stats service.
- **No signing infra exists yet** (no cosign/sigstore/SLSA in `.github/`) — this is greenfield and
  is the single largest piece of the MVP.

### Why now / why this matters

Kafka Connect's ecosystem (Confluent Hub) is a real competitive point: "where do I get connectors
and how do I trust them." Conduit's answer must be at least as easy **and** safer by default
(signed, provenance-checked, no click-through-EULA hub). It also directly serves the agent-native
bet: an agent should be able to `install` a connector as a bounded, verifiable step, not "go find
a binary on the internet."

## Goals / Non-goals

### Goals

- `conduit connectors install <name>[@<version>]` — resolve from the index, fetch the artifact for
  the host OS/arch, **verify signature + provenance**, **check Conduit/protocol compatibility**,
  install into `--connectors.path`, and cache. `--json` output + stable error codes.
- **Refuse-by-default trust, pinned to identity.** An artifact installs only if its signature +
  provenance validate against the **identity registered for that connector name** (§ Decision 1).
  A valid signature by the _wrong_ identity refuses (`ERR_IDENTITY_MISMATCH`), distinct from
  `ERR_UNSIGNED`. `--allow-unsigned` is the only escape hatch and is gated (§ Failure modes) —
  never a plain flag an agent or script can flip.
- **A tamper-evident index (§ Decision 2).** The index that _stores_ the sha256 and the expected
  identity is itself signed and freeze-protected; `install` verifies it before trusting any field.
- **Actionable compatibility refusal:** incompatible min-version → a `ConduitError` naming the
  required vs running versions and the fix, not a silent install that breaks at pipeline start.
- **Publishing flow:** a **reusable GitHub Action** any connector repo drops in to build → sign
  (keyless) → emit SLSA provenance → open an index PR. "Verified" derives from identity + provenance
  (§ Decision 1), never a manual blessing; **first registration of a name is human-reviewed**.
- **Post-install revocation checking:** `conduit connectors audit` re-checks installed connectors
  against the current signed index + revocation list and flags any version that has since been
  yanked or whose publisher identity was revoked — the mechanism that protects _already-installed_
  systems, which install-time refusal alone does not.
- **Offline / air-gapped install:** a documented path to pre-fetch an artifact + its signature/
  provenance bundle and install from local disk, and a local cache so repeats don't re-download.
- **Static registry web UI:** search, per-connector page, verified badge, version list,
  compatibility, download stats — built from the index, sharing the built-in UI's design tokens.
- **`conduit connectors uninstall <name>` and `list --installed`** to round-trip the lifecycle.

### Non-goals (MVP)

- **No hosted dynamic registry API/service.** The index is static (git/CDN). A hosted service with
  live search/auth is a later option if the static index outgrows itself.
- **No private/paid registries, no auth-gated connectors.** (And per the one-way ratchet, the
  registry core is never paywalled.)
- **No auto-update / background upgrade.** Install is explicit; upgrade is `install @<newer>`.
- **No inter-connector dependency resolution** — standalone connectors are self-contained.
- **No WASM-connector distribution yet** — WASM connectors are a v0.19 go/no-go item; the index
  schema reserves an artifact `kind` field so WASM slots in later without a breaking change.
- **Not a general artifact store** — only Conduit connectors (later: processors, templates, reusing
  the same index shape).

## Decision

### 1. Trust root and identity registration (the security foundation)

This is the first-class decision the rest of the design hangs on, and getting it wrong makes every
signature check theater. Keyless cosign proves _who_ signed an artifact (an OIDC issuer + a
workflow-identity SAN) — it does **not** prove that _who_ is the entity allowed to publish the name
`postgres`. Without pinning, anyone who forks the publish Action into their own repo can produce a
perfectly valid, Rekor-logged signature for a connector named `postgres` and earn a green "verified"
badge. So:

- **Per-name identity pinning.** The index records, **per connector name**,
  `publisher.expectedOIDCIssuer` and `publisher.expectedIdentityPattern` (repo + workflow path,
  pinned to a ref/tag pattern). `install` verifies with cosign's `--certificate-oidc-issuer` and
  `--certificate-identity` set to those pinned values — **a valid signature by any _other_ identity
  refuses** (`ERR_IDENTITY_MISMATCH`), distinct from `ERR_UNSIGNED`.
- **`verified` is derived, never asserted.** A version is "verified" iff its signature +
  SLSA provenance validate against the name's pinned identity. It is never a hand-set field.
- **Registration is a human act; version bumps are not.** The trust axis is _establishes trust_ vs
  _exercises established trust_, not _community_ vs _first-party_:
  - **First registration of a name** (and any later change to its pinned identity — an org move,
    a workflow rename) requires a **human-reviewed** PR to the index and is **never auto-merged**,
    first-party included. This is the actual root-of-trust decision.
  - **A new version of an already-registered name, signed by its already-pinned identity,** may be
    low-touch/auto-merged once index-CI re-verification (below) passes.

### 2. Index format + integrity (a signed, tamper-evident trust store)

The `install`-time checks read the expected sha256 _and_ the expected identity _from the index_, so
**the index is itself a trust root** — an unsigned static JSON served over TLS has integrity against
network attackers but none against a compromised origin/CDN or a bad merged PR. TLS is not enough.
The MVP adopts "TUF-lite" (four properties below) rather than full
[TUF](https://theupdateframework.io/) (see Alternatives for why, and TUF as the v2 escalation):

1. **The built index is signed** with a **registry root key distinct from any connector's signing
   identity**, held in a GitHub Environment gated by required reviewers. `install` verifies this
   signature **before trusting any field in the index**, independent of TLS/CDN.
2. **Freeze/rollback protection:** the signed index carries a monotonically increasing version + a
   signed timestamp; `install` refuses an index older than the newest it has seen or past a
   max-staleness window (distinct error from "index unreachable"), defeating a CDN that replays a
   stale index still listing a since-yanked version as good.
3. **Index-CI re-verification:** before _any_ merge (including auto-merged first-party version
   bumps), index-repo CI independently re-fetches the artifact at the claimed `url`, recomputes
   `sha256`, and re-runs `cosign verify` against the name's pinned identity — the index never
   records an assertion it has not itself checked.
4. **Branch protection + required signed commits** on the index repo (necessary, not sufficient
   on its own — it gates _who pushes_, not _whether the JSON is true_).

Per-connector, per-version entry fields: `artifacts[] { os, arch, kind: "standalone", url
(Scarf-gatewayed), sha256, size }`, `minConduitVersion`, `minProtocolVersion`, `signature` (cosign
bundle / Rekor inclusion reference), `slsaProvenance` (attestation reference), `deprecated?`,
`yanked? { reason }`. A top-level `schemaVersion` lets `install` refuse a too-new index with
"upgrade Conduit" rather than mis-parse it (announce → warn → remove, matching config/protocol
policy). A signed **revocation list** (per-version yanks and a publisher-level
`publisher.revoked { reason }` that invalidates _all_ versions under a compromised identity) is part
of the integrity-checked bundle, not a bolt-on unsigned field. `kind` reserves room for WASM
(v0.19) without a breaking change.

### 3. `install` pipeline (verification is the authorization gate, not sha256)

1. Fetch + **verify the signed index** (§2) before trusting any field.
2. Resolve `<name>[@version]` **exact-match only** (no fuzzy/nearest-match — a typo'd name is a hard
   `ERR_NOT_FOUND`, never an auto-installed lookalike); if `@version` omitted, pick the newest
   version whose `minConduitVersion`/`minProtocolVersion` the running Conduit satisfies, so "latest"
   never means "latest-incompatible". Refuse a yanked/revoked selection with its reason.
3. Select the host-OS/arch artifact; **none → refuse**, listing available platforms.
4. Download (following any Scarf redirect) into a **per-install, `0700`, uniquely-named staging dir
   owned by the invoking user** — never a shared/predictable path.
5. Compute the digest **from the actual received bytes** and: (a) check it against the index's
   `sha256` — this is **corruption detection**, integrity against a bad download, _not_ a trust
   boundary; (b) run the **authorization gate** — `cosign verify` of the signature over the
   artifact's own digest **and** the SLSA attestation `subject` digest, against the name's **pinned
   identity** (§1) + Rekor inclusion. Any failure → delete staging, refuse. `--allow-unsigned`
   (§ Failure modes) skips only (b), keeps (a), and is gated as described there.
6. Only on full success: verify + rename via the same resolved file descriptor with `O_NOFOLLOW`
   (no symlink substitution between check and use); a **per-target lock** serializes concurrent
   installs so two can't interleave the rename + manifest write. The move is atomic (temp + rename)
   so a crash mid-install leaves either the old artifact or nothing, never a partial. Update the
   install manifest and cache only after the rename succeeds. Emit a structured audit event
   (connector, version, resolved digest, whether signed, operator, timestamp).

Once §2 index-signing lands, the index's `sha256` becomes trustworthy too and (a)/(b) converge on
the same fact — belt-and-suspenders, deliberately.

### 4. Publishing (reusable Action, keyless, SLSA)

A `conduitio/connector-publish-action` a connector repo's release workflow calls: build the OS/arch
matrix, sign each artifact with **cosign keyless** (GitHub OIDC — no long-lived keys to custody or
leak), emit **SLSA provenance** (target **Build L3**: GitHub-hosted ephemeral runners,
non-falsifiable provenance; L2 floor if a self-hosted runner is ever unavoidable) whose `subject`
digest and `builder.id`/`configSource.uri` bind to the artifact and the pinned identity, and open an
index PR. **New-name registration goes to human review (§1); a version bump from the pinned identity
can auto-merge once index-CI re-verification (§2.3) passes.**

### 5. Web UI (static, shares the component library)

A static site generated from the index: searchable connector list, per-connector page (versions,
compatibility matrix, verified badge, install command, Scarf-sourced download stats). No backend.
Uses the **shared React component library / design tokens** from the built-in-UI doc so the two
v0.18 React surfaces are one visual language.

## Alternatives considered

- **Hosted registry service (API + DB + auth), Confluent-Hub-style.** More capable (live search,
  richer stats, private registries) but a real always-on service to build, run, secure, and pay
  for — disproportionate for the MVP and for a small team, and it centralizes trust. The static
  index gets 90% of the value at near-zero operational cost and is GitOps-native. Revisit only if
  the static index demonstrably can't scale.
- **Long-lived signing keys instead of keyless.** Rejected: key custody/rotation is exactly the
  kind of standing liability a small team should avoid. Sigstore keyless ties trust to the CI
  identity + transparency log with nothing to leak.
- **Trust-on-first-use / checksum-only (no signatures).** Rejected as the default: a checksum
  proves integrity, not provenance — it doesn't stop a malicious artifact that matches its own
  hash. Signature + provenance is the default; checksum-only survives as the explicit
  `--allow-unsigned` escape hatch.
- **Bundle everything as built-in connectors instead of a registry.** Doesn't scale, bloats the
  binary, and defeats the any-language / community-authored extensibility bet.
- **Full [TUF](https://theupdateframework.io/) (The Update Framework) for the MVP.** TUF's
  root/targets/snapshot/timestamp roles with threshold signing are the gold standard for exactly
  this problem (compromise-resilient, freeze/rollback-proof software distribution). We adopt its
  _properties_ (§ Decision 2: a signed index, freeze/rollback protection, CI re-verification) but
  not its full role/ceremony for the MVP — the operational cost (key ceremonies, threshold signer
  coordination) is disproportionate for a small team and a static index. **Full TUF is the named
  v2 escalation** if the static index outgrows ad-hoc signing; the index schema and signing seam
  are designed so that migration is not a rewrite.

## Failure modes (the 80%)

- **Wrong-identity signature (the impersonation attack)** → a valid, Rekor-logged signature by an
  identity _other_ than the name's pinned one → refuse `ERR_IDENTITY_MISMATCH`. This is the attack
  keyless-without-pinning would wave through.
- **Unsigned / verification fails** → refuse `ERR_UNSIGNED`, delete staging. Never install.
- **Tampered index** (a field — `url`, `sha256`, `expectedIdentityPattern` — altered without a
  valid index signature) → `install` refuses `ERR_INDEX_INTEGRITY` before trusting any field.
- **Stale / frozen index** (a compromised CDN replays an old index still listing a since-yanked
  version) → refuse/warn distinctly from "unreachable" via the signed monotonic version + timestamp
  max-staleness check.
- **`--allow-unsigned` misuse** → gated, not a bare flag (see below).
- **Incompatible min-version** → refuse with required-vs-running versions + fix; resolution prefers
  a compatible version, so this only fires on an explicit incompatible `@`-pin.
- **No artifact for host OS/arch** → refuse, list available platforms.
- **Corrupt/truncated download** → sha256 (corruption check) mismatch → refuse + retry; staging
  cleaned up. (Trust still comes from the signature over the artifact's own digest, § Decision 3.)
- **Scarf gateway compromised/misbehaving** → the digest is recomputed from the _actual received
  bytes_ after following redirects, so a gateway can only cause a failed verification (denial of
  service), **never** a successful install of the wrong artifact — the gateway is outside the trust
  boundary. A direct-to-GitHub-Releases fallback covers a Scarf _outage_ (availability, not
  security).
- **Network down / air-gapped** → cache hit serves without network; documented offline install from
  a locally-provided artifact + signature/provenance bundle.
- **Yanked version or revoked publisher** → refuse with reason at install; **and** `conduit
  connectors audit` flags it on already-installed systems (revocation isn't only an install-time
  concern).
- **Index too new (`schemaVersion` ahead)** → "upgrade Conduit," never a mis-parse.
- **Typosquat / name confusion** → **exact-name resolution only** (no fuzzy nearest-match); `install`
  prints the resolved publisher identity before completing; a typo is `ERR_NOT_FOUND`.
- **Concurrent installs** → a per-target lock serializes the rename + manifest write; two
  simultaneous installs can't interleave.
- **Staging TOCTOU** → per-install `0700` uniquely-named staging owned by the invoking user;
  verify + rename on the same resolved fd with `O_NOFOLLOW` (no symlink substitution between check
  and use).
- **Crash mid-install** → atomic temp-write + rename; the connectors dir never holds a partial; the
  manifest updates only after the rename succeeds.
- **Disk full during staging/move** → fail before touching the live connectors dir.

### `--allow-unsigned` is a gated control, not a warning

A loud warning does not stop a rushed human or a prompt-injected agent. So:

- **TTY:** require a typed confirmation (re-type the connector name), not `y/N`.
- **Non-interactive (no TTY, `CI=true`, or invoked via MCP):** **hard-refuse** unless a _separate_,
  harder-to-reach env escape hatch is also set (e.g. `CONDUIT_ALLOW_UNSIGNED_INSTALL=I_UNDERSTAND`),
  so a CLI flag alone in a script/agent prompt cannot enable it.
- **Operator policy wins:** a site config (`install.allowUnsigned: false`) hard-disables the path
  regardless of any flag or env — the actual defense for the agent scenario, since the MCP tool
  shares the CLI code path and must not expose unsigned-install as a flippable tool parameter.
- **Audit:** every unsigned install emits a durable structured audit event (connector, version,
  resolved digest, operator, timestamp).

## Testing

The verification + integrity suite is the security warranty and the highest-leverage part of the
epic. Negative tests carry the weight — each attack refuses with its _distinct_ error code:

- **Identity/signature (the trust core):** a valid signed artifact by the pinned identity installs;
  an **unsigned** (`ERR_UNSIGNED`), a **tampered** artifact (digest mismatch), and a
  **valid-signature-by-a-different-identity** (`ERR_IDENTITY_MISMATCH`) all refuse. `--allow-unsigned`
  installs the unsigned one only through its gate (below).
- **Index integrity:** mutating a served index field (`url`/`sha256`/`expectedIdentityPattern`)
  without a valid index signature → `install` refuses `ERR_INDEX_INTEGRITY`. A **stale/replayed**
  index (older monotonic version / past max-staleness) refuses distinctly from "unreachable."
- **Index-repo CI:** an index PR whose claimed artifact fails re-fetch + sha256 + `cosign verify`
  against the pinned identity is rejected before merge (including for first-party version bumps).
- **`--allow-unsigned` gating:** refuses in a non-TTY/`CI`/MCP context absent the separate env
  escape hatch; an operator `install.allowUnsigned: false` overrides the flag; an unsigned install
  emits the audit event.
- **Revocation/audit:** `conduit connectors audit` flags an installed version later yanked or whose
  publisher was revoked.
- **Compatibility resolution:** newest-compatible selection; explicit incompatible `@`-pin refuses;
  no-platform-artifact refuses; **exact-name only** — a typo'd name is `ERR_NOT_FOUND`, never a
  fuzzy auto-install.
- **Concurrency + atomicity:** concurrent `install <name>` don't corrupt the manifest or race the
  target path; kill mid-install → connectors dir has the old artifact or nothing, never a partial.
- **Scarf untrusted:** a gateway redirected to a wrong artifact still fails verification (digest
  recomputed from received bytes); a Scarf outage falls back to direct GitHub Releases.
- **Offline/cache:** second install is cache-served; the documented offline path works with no
  network.
- **Publish-action E2E (north-star):** author a connector → publish (keyless sign + SLSA
  provenance) → a fresh `install` verifies against the pinned identity → run, all signed.

## Upgrade / rollback

- **Index `schemaVersion`** is the compatibility contract; additive fields are backward-compatible,
  a major bump follows announce → warn → remove.
- **`install` is reversible:** `uninstall` removes the artifact + manifest entry; nothing in the
  engine's serialized state changes (a connector artifact is a file in a directory).
- **The `artifact.kind` field** reserves room for WASM (v0.19) with no breaking index change.
- Rolling back the feature = the `install`/`uninstall` commands and the Action; no data migration.

## Epic / PR plan and acceptance criteria

The trust model — **deciding and recording, per connector name, which identity may sign it, in a
tamper-evident index** — is the hard part and the critical path, not "wire up `cosign verify`."
Sequenced so that trust lands before anything trusts it:

1. **Freeze the index schema + trust model** (`schemaVersion`; per-name `publisher.expectedOIDCIssuer`
   / `expectedIdentityPattern`; `sha256`/compat/`signature`/`slsaProvenance` per artifact; the
   signed revocation list + `publisher.revoked`; the index-signing + freeze/rollback design). _AC:
   schema documented + a sample index validates; identity-registration-vs-version-bump review paths
   specified; too-new `schemaVersion` refusal specified._ **Unblocker for parallel work — but the
   trust-model fields must be frozen here, not retrofitted.**
2. **`install` core: verified-index fetch → exact-match resolve → platform-select → download →
   sha256 (corruption) → per-target-locked atomic install (`O_NOFOLLOW`, `0700` staging) →
   manifest + audit event**, with the compatibility gate. _AC: newest-compatible resolution;
   incompatible/no-platform/yanked/typo refuse with distinct coded errors; concurrent installs
   don't race; crash-mid-install leaves no partial; `--json`._
3. **Trust core: index-signature verification + per-name identity-pinned `cosign verify` + SLSA
   provenance** (subject digest + `builder.id`/`configSource.uri` bound to the pinned identity +
   Rekor inclusion), wired as the gate before step-2's atomic move; the **gated** `--allow-unsigned`.
   _AC (the security warranty — see Testing): `ERR_UNSIGNED`, `ERR_IDENTITY_MISMATCH`,
   `ERR_INDEX_INTEGRITY`, stale-index, and `--allow-unsigned`-in-non-interactive all refuse/gate as
   specified; operator policy overrides the flag; the verify suite is green._ **Tier-1-adjacent
   (supply-chain) — highest review bar; do not let 4/6 gate it.**
4. **Publishing Action** (`conduitio/connector-publish-action`): build matrix → keyless sign → SLSA
   (target L3) provenance → index PR. **New-name registration is human-reviewed; a pinned-identity
   version bump auto-merges only after index-CI re-verification.** _AC: an author repo publishes a
   connector a fresh `install` verifies E2E; `verified` derives from identity; index-CI rejects a
   PR whose artifact fails re-fetch/verify._
5. **`uninstall`, `list --installed`, `audit`** + the local cache/offline path. _AC: round-trip
   lifecycle; `audit` flags a since-yanked/revoked installed version; offline install tested._
6. **Registry web UI** (static, shared design tokens): search, per-connector page, verified badge,
   compatibility, Scarf stats. _AC: generated from the index; shares the built-in UI's tokens; no
   backend._

**Parallelization:** once step 1 (schema + trust model) is frozen, step 2 (install plumbing), step 4
(publishing Action), and step 6 (web UI) proceed in parallel. **Step 3 (trust core) is the critical
path and highest risk — it gates any real install, and 2/4/6 must not be allowed to gate _it_.**
Step 5 follows step 2. Findings 1–2 from the security review (identity pinning + a signed index) are
merge-blockers for step 3: do not ship verification that treats "signed by anyone" as "verified."

## Related

- Phase 1 Execution Plan (v2): `docs/design-documents/20260704-phase-1-execution-plan.md` (§7).
- Built-in UI (shares the React component library): `docs/design-documents/20260713-greenfield-built-in-ui.md`.
- Connector/processor scaffolding (`conduit connector new`, the authoring side of extensibility):
  `docs/design-documents/20260707-connector-processor-scaffolding.md`.
- Connector protocol (compatibility metadata source): `ConduitIO/conduit-connector-protocol`.
