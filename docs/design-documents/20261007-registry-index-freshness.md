# Registry index freshness without an unattended root key

## Summary

The hosted registry index (`https://registry.conduitdata.io/index.json`) goes stale every 7 days
unless a human dispatches a root re-sign and approves the `registry-signing` Environment gate. That
has caused three install outages since August. This doc decides how to keep the index fresh without
ever using the root key unattended.

**Recommendation:** leave `index.json` exactly as it is, always root-signed. Add a small, separate
**liveness document** that a daily, ungated workflow signs **keylessly with Sigstore**, using the
workflow's own GitHub OIDC identity. The document names the exact root-signed index payload by its
digest. A v0.21+ client that finds the root timestamp older than `install.max-staleness` (7 days)
accepts the index anyway if a liveness document for that exact payload was logged in Rekor within
the last 7 days. A hard ceiling (`install.max-root-age`, default 30 days) caps how old the root
signature itself may get.

A liveness document cannot authorize content. It only vouches that an index the root key already
signed is still current, and only up to the ceiling. Old clients (v0.18.0 through v0.20.x) never
fetch the liveness document. They behave exactly as they do today, so this design neither helps nor
breaks them.

Risk tier: **Tier 1** review depth (registry signing and trust). This PR is the design only; no
code changes.

## Problem

`pkg/registry.TrustedVerifier.VerifyIndex` runs `index.Verify` and then `index.CheckStaleness`,
which refuses (`registry.index_stale`) any index whose `payload.index.timestamp` is older than
`install.max-staleness`. The default is `index.DefaultMaxStaleness`, 168h. The timestamp sits inside
the signed payload, so only a new signature can move it. Today the only signature that works is a
`role: "root"` signature, made by `index-sign.yml`. That workflow runs on `workflow_dispatch` only,
inside the `registry-signing` Environment, which requires the maintainer's approval.

The result is a 7-day cliff that one maintainer has to beat by hand, forever:

| Incident | What happened |
| --- | --- |
| 2026-08-21 | The index aged past 7 days without anyone noticing for 18 days. A scheduled `role=freshness` re-sign was tried by hand to clear it. Signing succeeded, the site deploy failed (`ERR_INDEX_INTEGRITY: index has no structurally valid root-role signature entry`), and the CDN kept serving the old index. Re-signing with `role=root` fixed it. |
| ~2026-09-12 to 2026-10-07 | The version 12 index (timestamp 2026-09-05T21:08:35Z) went stale around 09-12. `index-staleness-alarm.yml` opened `ConduitIO/conduit-connector-registry#33` and kept commenting on it daily (29 comments in all). Installs failed for every user for about 25 days, until a root re-sign on 2026-10-07. |

The alarm worked and the outage happened anyway. A reminder that fires every 3 days is a chore, and
chores slip. The current index (version 13, timestamp `2026-10-07T05:30:44Z`) goes stale at
**2026-10-14T05:30:44Z**.

### Why the existing freshness role cannot fix this

The trust model (`20260714-connector-registry-index-schema.md`, OQ3) already defines a
`freshness` key meant for exactly this job. `index.Verify` accepts a payload if a root signature
verifies, **or** if a freshness signature verifies **and** the payload's content subtree hashes to
the caller's persisted `State.LastVerifiedContentHash`. Read against the code, that has two problems
for the published index:

1. **Both roles sign the same canonical payload.** The timestamp is in the payload, so a heartbeat
   that moves the timestamp invalidates the old root signature. Keeping the old root entry "next
   to" a new freshness entry doesn't help. `ed25519.Verify` fails on the root entry, the loop
   `continue`s, and `rootVerified` stays false.
2. **A freshness-only index can never be the first index a client accepts.** The freshness path
   requires `lastVerifiedContentHash != ""`. That hash lives in
   `<connectors.path>/.registry/index-state.json`. Every new install, every CI runner and every
   container without a persisted connectors dir starts without one. Those clients get
   `registry.index_integrity` ("the index may be tampered or corrupted"): a false tamper alarm,
   which is worse than a stale error. `verify.go` documents this gap on purpose.

The site build's `verifyIndex.ts` requirement of a root entry is the visible symptom of the August
breakage, but fixing the site doesn't fix point 2. The client is what matters.

There is also a custody problem that has nothing to do with signature roles. `FRESHNESS_SIGNING_KEY`
lives in the same `registry-signing` Environment as the root key. GitHub applies Environment
protection rules to every job that references the Environment, whatever triggered it. So a
scheduled freshness job would park for approval every night.

## Constraints

- **The root key is never used unattended.** It authorizes content (connectors, publishers,
  versions, yanks, revocations). This is the core of OQ3 and does not change here.
- **Nothing that is not root-signed can change what a client installs.** Liveness may extend how
  long a root-signed index counts as fresh, and nothing else.
- **Trust anchors are fixed at build time** (OQ1/OQ2). No runtime-fetched key or identity updates.
- **Old clients must not regress.** v0.18.0, v0.19.0 and v0.20.x are in users' hands with today's
  `Verify`. Whatever is published must leave them exactly as well off as today.
- **No serialized-state change.** `index.State` / `index-state.json` keeps its format, so there is
  nothing to migrate and nothing to roll back.
- **Anti-freeze must stay meaningful.** The staleness check exists so that an attacker who controls
  distribution (CDN, DNS, a mirror) cannot freeze clients on an old index to hide a yank. Any design
  that weakens that bound has to say by how much, and under what compromise.
- **Solo maintainer.** The design has to cut human touches, not move them around.

## Decision

### D1. A detached, keyless-signed liveness document

The registry publishes two new static files next to `index.json`:

- `liveness.json`, the statement:

  ```json
  {
    "type": "conduit-registry-liveness",
    "version": 1,
    "index": {
      "version": 13,
      "payloadDigest": "sha256:<hex of SHA-256 over the JCS-canonical index payload>"
    },
    "issuedAt": "2026-10-08T03:10:00Z"
  }
  ```

- `liveness.json.sigstore.json`, a Sigstore bundle over the exact bytes of `liveness.json`: a Fulcio
  certificate, the signature, and a Rekor inclusion proof. This is the bundle shape
  `pkg/registry/trust` already verifies offline for every connector artifact.

`payloadDigest` is computed with `index.Canonicalize`, the same JCS function `index.Verify` signs
and verifies over. It covers the whole payload: content, `index.version` and `index.timestamp`. A
liveness document therefore vouches for exactly one root-signed index and for nothing else.

### D2. Who signs it: the workflow identity, not a key

A new workflow in the registry repo, `index-liveness.yml`, runs daily on `schedule:` (plus
`workflow_dispatch`) with `permissions: id-token: write, contents: write, actions: write`. It
references **no Environment and no secret**. Its steps:

1. Check out `main`. Run `index.Verify` (from the pinned Conduit module, the same pin
   `cmd/index-sign` already uses) on `index/index.json` against copies of the production root
   anchors. Refuse to vouch for an index that does not root-verify.
2. Refuse to vouch if the root timestamp is already older than the ceiling. A liveness document for
   it would be useless, and the alarm should be the thing that speaks.
3. Write `liveness.json` and sign it with `cosign sign-blob --bundle` (keyless). Fulcio issues a
   short-lived certificate whose SAN is the workflow identity
   `https://github.com/ConduitIO/conduit-connector-registry/.github/workflows/index-liveness.yml@refs/heads/main`.
4. Commit both files to `main` and dispatch `deploy.yml`, the same pattern `index-sign.yml` uses,
   because pushes made with `GITHUB_TOKEN` don't trigger workflows.

Conduit compiles in the pinned identity: issuer `https://token.actions.githubusercontent.com` and an
anchored SAN pattern for that one workflow file on `refs/heads/main`, checked by
`trust.ValidateIdentityPattern`. This is the same identity-pinning mechanism the client already uses
for artifacts.

### D3. Client verification (v0.21+)

`TrustedVerifier.VerifyIndex` keeps its current order and adds one branch:

1. `index.Verify`, unchanged: root signature required for first-time clients, as today.
2. `index.CheckRollback`, unchanged.
3. `index.CheckStaleness` on the root timestamp. **If it passes, stop here.** No liveness fetch,
   no added latency, and the behavior is identical to today.
4. If it fails with `registry.index_stale`, and the index came from `--index-url` (not
   `--index-file`, not `--bundle`), fetch `liveness.json` and its bundle from the same directory as
   the index URL. Use `boundedfetch` with a small cap (64 KiB each) and the existing timeouts.
5. Accept the index only if **all** of these hold:
   - The bundle verifies over the raw `liveness.json` bytes against the embedded Sigstore trusted
     root, with the pinned identity (`trust.VerifyArtifactSignature`'s machinery, using a SHA-256
     of the received bytes).
   - `liveness.json` passes `index.CheckNoDuplicateKeys`, has `type ==
     "conduit-registry-liveness"` and `version == 1`. A higher version is treated as absent, with a
     hint to upgrade.
   - `index.version` equals the fetched index's version, and `index.payloadDigest` equals SHA-256
     over the fetched index's canonical payload.
   - The liveness time **T is the Rekor-observed time** from the verified bundle
     (`VerifiedTimestamps`), not the self-reported `issuedAt`. The signer cannot backdate or
     future-date T. `issuedAt` is informational and must lie within 1h before T.
   - `now − T ≤ install.max-staleness` (the same 7-day knob as today).
   - `T ≥ index.timestamp − 5m`. A statement logged before the index existed is nonsense.
   - `now − index.timestamp ≤ install.max-root-age` (new, default 30 days).
6. Only after all checks pass, persist `State` exactly as today. The rollback high-water mark moves
   only on acceptance. `LastVerifiedContentHash` updates because the index _is_ root-verified.

Any liveness failure falls back to today's refusal. The client never accepts anything it would have
refused before, except a root-verified index whose freshness is now proven by a fresh liveness
statement.

### D4. Error codes and config (public contract, additive only)

| Code | When | Suggested fix |
| --- | --- | --- |
| `registry.index_stale` (existing, unchanged meaning) | Root timestamp older than `max-staleness` and no usable liveness statement (absent, unreachable, logged too long ago, or for a different index). The message names which. | The registry has not been refreshed. Retry later, or raise `--install.max-staleness` knowingly. |
| `registry.liveness_invalid` (new, `DataLoss`) | A liveness statement is present but its bundle, identity, shape or digest fails verification. | Treat like `registry.index_integrity`: possible tampering. Report it. Don't override. |
| `registry.index_root_too_old` (new, `FailedPrecondition`) | Liveness is valid but the root signature is older than `max-root-age`. | The registry operator owes a root re-sign. Wait, or raise `--install.max-root-age` knowingly. |

New config: `install.max-root-age` (`--install.max-root-age`, config file `install: max-root-age:`).
Setting it equal to `max-staleness` turns liveness off, which gives an operator who wants today's
strict 7-day bound an exact opt-out without another flag.

`install --json` and `audit --json` gain `index.freshness: {source: "root" | "liveness",
rootTimestamp, livenessLoggedAt}`.

### D5. What happens to the in-band `freshness` role

Nothing in v0.21. The compiled-in freshness anchor and `Verify`'s freshness path stay, unused by the
published index, because removing them is a public behavior change with its own deprecation clock.
Retiring them (announce, warn, remove, at least two minors) is a follow-up decision, not part of this
design. `FRESHNESS_SIGNING_KEY` stays where it is, in the gated Environment, unused.

## Security analysis

The question for any unattended signer: **what can someone who controls it do?** With keyless
signing there is no key to steal. The equivalent capability is "can run `index-liveness.yml` on
`refs/heads/main` of the registry repo", which means push access to `main` or a compromise of GitHub
Actions or Fulcio. The analysis below assumes that attacker, and in the worst case one who _also_
controls distribution (the CDN or the Pages deploy).

**Cannot:**

- **Substitute or add content.** Every byte a client installs from comes from a payload that passed
  root verification. The liveness statement carries a digest, not content. A statement naming a
  digest that no root signature covers matches nothing a client will accept.
- **Affect first-time-client trust bootstrapping.** First-time clients still need a root signature.
  Liveness never substitutes for one.
- **Roll back returning clients.** `CheckRollback` runs before liveness is consulted and is
  unchanged. A returning client never accepts a version below its high-water mark.
- **Extend freshness past the ceiling.** `max-root-age` is checked against the root-signed
  timestamp, which the liveness signer cannot change.
- **Forge the liveness time.** T comes from Rekor's log, not from the document.
- **Affect v0.18.0 through v0.20.x clients.** They never fetch the document.
- **Do it quietly.** Every keyless signature lands in the public Rekor log under the pinned
  identity, so a monitor on that identity sees any signature made outside the daily schedule.

**Can:**

- **Freeze clients on an older root-signed index for up to `max-root-age` (30 days) instead of 7.**
  If the attacker also controls distribution, they can serve an older root-signed index plus a
  fresh liveness statement for it. That hides a yank or revocation published after that index:
  - from first-time clients, for any root-signed index younger than the ceiling;
  - from returning clients, only by holding back newer versions, because rollback protection
    still applies.

  This is the full cost of the design, and it applies only while the liveness path is compromised.
  Without that compromise, a distribution-only attacker is still bounded by 7 days, because they
  can't produce a fresh statement.
- **Deny service.** Publishing garbage liveness statements makes stale indexes fail with
  `registry.liveness_invalid` instead of `registry.index_stale`. Fresh root-signed indexes are
  unaffected, since liveness is never consulted for them.

**Response to compromise:** disable `index-liveness.yml` and fix repo access. Clients fall back to
the 7-day root bound within one `max-staleness` window, because no new statements get logged. Then
root re-sign. The ceiling bounds the residual exposure even if nobody notices. No Conduit release is
needed, because there is no key to rotate. Changing the pinned identity (for example, renaming the
workflow file) does need a release, so the workflow path is frozen once v0.21 ships.

This is TUF's timestamp role without the rest of TUF. A short-lived statement from a low-privilege
signer vouches for a long-lived one from a high-privilege signer, and the long-lived one carries its
own expiry (the ceiling).

## Alternatives considered

### (a1) The same detached document, signed by a low-privilege ed25519 key in an ungated Environment

This was the plan of record in `index-sign.yml`'s header: a new `registry-liveness` Environment
with branch policy `main` and no required reviewers, a `LIVENESS_SIGNING_KEY` secret, a new
compiled-in liveness anchor, and verification through the same ed25519/JCS code as `index.Verify`.

**Why it lost to keyless (D2):** it has the same trust domain with worse failure properties.
Anyone who can push to `main` can edit the workflow to print the secret. After that the key forges
statements **offline, forever, with no log**, until a Conduit release removes its anchor. That
release is weeks away, and old builds keep the anchor. It also needs a key-generation ceremony and a
custody decision for an ungated secret. Keyless has no exportable secret, every signature is
publicly logged, and response is "fix repo access". What a1 does better: signing doesn't depend on
Sigstore being up. That is cheap to give up, because a Sigstore outage shorter than the 7-day window
costs nothing, and every artifact install already depends on Sigstore verification.

### (b) A freshness signature added alongside the root signature on `index.json`

**Not achievable as stated.** Checked against `index.Verify`: refreshing the timestamp changes the
canonical payload, so the existing root entry fails `ed25519.Verify` and is skipped. "Alongside"
only holds if the root key also re-signs the new payload, which is just an unattended root key with
extra steps. Fixing the site build to accept freshness-only would make the deploy go green and leave
every first-time client with a false `registry.index_integrity`. It does help returning v0.18 to
v0.20 clients, whose persisted hash would match. But it trades away new users, CI and containers, the
population that matters most, so it loses.

### (c) A longer default staleness window plus a reminder

Raise `DefaultMaxStaleness` to around 30 days in v0.21 and keep the alarm. This is the cheapest
option and it cuts touches to monthly. **Why it lost:** it moves the anti-freeze bound from 7 to 30
days for every v0.21+ client, against any attacker who controls distribution, with no compromise of
our signing path needed. D1 keeps that bound at 7 days unless the liveness signer is compromised.
D1 also lets us lengthen the ceiling later without touching the freeze bound. (c) can't. (c) still
needs a release, so it isn't faster to land either.

### (d) Future-dated root timestamps

Root-sign with `index.timestamp = now + N days`. `CheckStaleness` only checks `now − timestamp >
max`, so a future timestamp passes. This is the **only lever that reaches v0.18 to v0.20 clients**.
**Why it lost as a routine practice:** it is (c) applied to every client including old ones. It
makes the timestamp field a lie, and the staleness alarm already flags a future timestamp as
`MALFORMED`. It stays on the table only as an explicit, one-off break-glass. See the decision for
DeVaris below.

### (e) An unattended root key with a "timestamp-only" guard in the job

Use role=root on a schedule, and have the job refuse to sign if content changed. **Rejected:** the
guard runs in the same job, on the same runner, in the same trust domain as the root key. An
attacker who controls the job skips the guard. It defends against operator error, not adversaries,
and it gives the root key the widest blast radius of any option. It also needs the Environment gate
removed for the root key.

### (f) Adopt TUF properly (go-tuf: timestamp, snapshot, targets roles)

**Rejected for now:** it replaces the frozen R-1 trust model, adds a dependency, and forces every
client through a migration. D1 is the one TUF property we actually need, added without breaking R-1.

## Client compatibility

| Client | Root ≤ 7 days old | Root 7–30 days old, valid liveness | Root > 30 days old | Liveness missing/broken |
| --- | --- | --- | --- | --- |
| ≤ v0.17.x | no registry install support | n/a | n/a | n/a |
| v0.18.0, v0.19.0 (`Verify` with connectors-only freshness hash) | accepts | `registry.index_stale` (today's behavior) | `registry.index_stale` | unaffected (never fetched) |
| v0.20.x (`Verify` with content-subtree hash; ships before this work) | accepts | `registry.index_stale` (today's behavior) | `registry.index_stale` | unaffected |
| v0.21+ (this design) | accepts, no liveness fetch | accepts, `freshness.source: "liveness"` | `registry.index_root_too_old` | falls back to `registry.index_stale` at 7 days |

Every version from v0.18 on honors `--install.max-staleness`. Checked in the v0.18.0 and v0.19.0
sources, where `InstallFlags` embeds `conduit.Config` and passes `Install.MaxStaleness` to the
verifier, and on `main` the flag is listed in `conduit connectors install --help`. That is the
operator override for old clients.

**The transition cost, stated plainly:** liveness removes the human only for v0.21+ clients. While
v0.18 to v0.20 clients matter, the root still has to be re-signed every ≤ 7 days, and the 72h alarm
stays as it is. When to stop doing that is a maintainer decision (below).

## Failure modes

| # | Failure | Effect | Detection | Handling |
| --- | --- | --- | --- | --- |
| 1 | Liveness job stops running (workflow disabled, the 60-day inactivity rule, Actions outage) | v0.21+ clients fall back to the 7-day root bound | Alarm: liveness `T` older than 48h | Re-enable/dispatch. Daily liveness commits keep the repo active, so the 60-day auto-disable can't trigger while the job works. |
| 2 | Sigstore (Fulcio/Rekor) down at sign time | That day's statement is missing. The previous one is still valid for up to 7 days. | Job fails red. Alarm fires at 48h. | Next run retries. Only a Sigstore outage over 5 days reaches users. |
| 3 | CDN serves new `index.json` with old `liveness.json`, or the reverse | Digest mismatch. The statement is ignored. | Smoke check byte-compares both files. | Harmless: a just-re-signed index is fresh on its own. A stale index with a mismatched statement refuses as today. |
| 4 | Root re-sign and liveness commit race on `main` | The push is rejected. | Job red. | Separate concurrency group, never the shared `index-sign` group: a parked, approval-gated sign would block liveness, and a pending liveness run could cancel a pending sign. On a push conflict, rebase once, re-verify which index is on `main`, re-sign or exit. |
| 5 | Liveness job vouches for an index the CDN isn't serving | Statement useless, not harmful (digest binding) | Smoke check | Fix the deploy. This is today's "repo ahead of served" case. |
| 6 | Root age passes the ceiling | v0.21+ get `registry.index_root_too_old` | Alarm: root age > ceiling − 7d → reminder issue | Root re-sign (human, gated), now roughly monthly instead of every 3 days. |
| 7 | Client clock skew | Same exposure as today's staleness check. T comes from Rekor, but `now` is local. | — | Unchanged. The `T ≥ index.timestamp − 5m` check tolerates signer-side skew. |
| 8 | Liveness document from a future schema version | Treated as absent → `registry.index_stale`, with an upgrade hint | — | Additive versioning. Never guess at an unknown shape (Invariant 6's spirit). |
| 9 | Malformed or huge liveness document or bundle | Bounded fetch refuses. Duplicate keys refuse. `registry.liveness_invalid`. | Fuzz targets on the parser (implementation requirement) | Fail closed. |
| 10 | Mirror or custom `--index-url` with no liveness files | Clients behave as today | — | By design. Mirrors can copy both files. |
| 11 | Pinned identity changes (workflow renamed or moved) | Every v0.21+ client rejects new statements until upgraded | CI test pinning the identity string against the registry workflow path | Freeze the workflow path. A change follows OQ2-style overlap: compile in both identities for the retention window. |
| 12 | Crash between fetch and `SaveState` | Nothing persisted, same as today | — | Unchanged. `SaveState` is atomic (Invariant 5) and runs only after acceptance. |

## Upgrade and rollback

- **Order of rollout:** the registry side ships first. Publishing `liveness.json` is invisible to
  v0.18 to v0.20 clients, which never request it. The site build ignores it. The smoke check gains a
  byte-compare. Then Conduit v0.21 ships the client branch.
- **Client upgrade:** no state migration. `index-state.json` is unchanged in format and meaning.
- **Client downgrade (v0.21 → v0.20):** the client loses liveness and gets today's 7-day behavior.
  The state file stays readable.
- **Server rollback:** stop publishing liveness (disable the workflow, or delete the two files).
  v0.21+ clients get today's behavior within one `max-staleness` window. No client breaks.
- **Opt-out for operators:** `install.max-root-age` equal to `install.max-staleness`.

## Observability and runbook

- **Client:** an info-level line whenever liveness is used (`index version 13 root-signed
  2026-10-07T05:30:44Z, fresh by liveness statement logged 2026-10-11T03:10:02Z, rekor entry …`).
  `--json` carries `index.freshness`. The three error codes above are distinct, so "stale",
  "tampered statement" and "operator owes a re-sign" can't be confused.
- **Registry, `index-staleness-alarm.yml` reworked:**
  - **Anomaly (actionable):** the served liveness statement's T is older than 48h, or its digest
    doesn't match the served index.
  - **Reminder (periodic, roughly monthly):** root age is past `max-root-age − 7d`.
  - **Old-client cliff:** keep the current 72h root-age alarm until the maintainer retires the weekly
    root cadence (see decisions).
- **Optional follow-up:** a Rekor search on the pinned identity that flags any entry outside the
  scheduled window.
- **Runbook:** `docs/operations/registry-index-freshness.md` (added in this PR) covers today's
  symptom → diagnosis → remediation. The implementation PRs add the `liveness_invalid` and
  `index_root_too_old` entries.

## Immediate stopgap for the 2026-10-14T05:30:44Z expiry

None of this needs the root key unattended.

1. **Root re-sign no later than 2026-10-12T05:30Z**, which leaves 48h of margin. Dispatch
   `index-sign.yml` (role `root`), approve at `registry-signing`, then confirm the served
   `index.json` shows version 14 and a new timestamp. The alarm will open its issue on the
   2026-10-10 12:30 UTC run (about 79h old). Don't wait for it.
2. **Make the alarm reach a person, not a timeline.** Issue #33 collected daily alarm comments for
   about 25 days while installs were broken. A comment on an issue the maintainer is subscribed to evidently doesn't get
   read. A small, secret-free registry-repo change:
   - put the absolute expiry time (`stale at 2026-10-14T05:30:44Z`) in the issue title;
   - assign the issue and `@`-mention the maintainer when it opens;
   - post a second `@`-mention when age reaches 120h (48h before the cliff);
   - post a third at 168h ("installs are failing now").

   Assignment and mentions trigger email and mobile notifications. Plain comments don't.
3. **An out-of-GitHub reminder:** a recurring 5-day calendar entry for the root re-sign until
   liveness ships. It's crude, but it doesn't depend on reading GitHub notifications.
4. **User workaround (documented in the runbook):** `--install.max-staleness=336h` lets an
   operator knowingly accept an index up to 14 days old during an outage.

## Implementation plan (after sign-off)

1. **`ConduitIO/conduit`**, Tier 1:
   - `pkg/registry/index/liveness.go`: parse, digest and bind checks, with fuzz targets on the
     parser.
   - A liveness verifier in `pkg/registry` that reuses `trust`'s bundle verification and returns the
     Rekor-observed time.
   - The `VerifyIndex` branch, the two new codes, `install.max-root-age`, and the `--json` fields.
   - Tests: unit tests for every check in D3 (wrong digest, wrong version, wrong identity,
     PR-ref identity, T too old, T before index timestamp, root past ceiling, oversized document,
     duplicate keys, schema too new) and an e2e test against a virtual Sigstore.
   - Docs: the runbook entries, `llms.txt`, and the changelog.
2. **`ConduitIO/conduit-connector-registry`:**
   - `index-liveness.yml` (no Environment, no secret).
   - The alarm rework, copying both files into the site's `dist/`, and smoke-check coverage.
   - A CI test that the workflow path matches the identity pinned in Conduit.
3. **Later:** decide on retiring the in-band freshness role (D5).

## Decisions needed from DeVaris

1. **Approve an ungated, scheduled workflow that can extend liveness.** This is the custody decision
   `index-sign.yml`'s header defers. With keyless signing it holds no secret, and its worst case is
   the bounded freeze described above.
2. **`install.max-root-age` default: 30 days?** Shorter means less exposure under compromise and
   more root re-signs. 30 days matches the site build's existing staleness limit. 90 days would mean
   four re-signs a year.
3. **How long to keep the weekly root cadence for v0.18 to v0.20 clients after v0.21 ships.**
   Recommendation: until v0.23.0 (two minors, matching the deprecation policy). After that, old
   clients get `registry.index_stale` between day 7 and the next monthly re-sign, and the fix is to
   upgrade.
4. **Allow future-dated root timestamps as a break-glass?** For example, before a planned absence:
   at most +7 days, a recorded decision each time, and the alarm taught to accept it. Recommendation:
   no, unless an absence actually makes the weekly re-sign impossible.

## Related

- `docs/design-documents/20260714-connector-registry-index-schema.md`: R-1 trust model, OQ1/OQ2/OQ3.
- `docs/design-documents/20260727-registry-processor-artifacts.md`: D4, the content subtree hash.
- `docs/design-documents/20260713-connector-registry-mvp.md`
- `pkg/registry/index/verify.go`, `pkg/registry/trustverifier.go`, `pkg/registry/trust/sigstore.go`
- `ConduitIO/conduit-connector-registry`: `index-sign.yml` (header), `index-staleness-alarm.yml`,
  `deploy.yml`, `web/src/lib/verifyIndex.ts`, issue #33, PR #28.
