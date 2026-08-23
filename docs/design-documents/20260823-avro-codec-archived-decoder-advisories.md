# Avro codec (hamba/avro) is archived with three unfixed decoder advisories: what we do about it

## Summary

`github.com/hamba/avro/v2` — the only Avro codec Conduit uses, in the engine's own built-in Avro
processor as well as everywhere `conduit-commons/schema` decodes Avro — was **archived by its
maintainer on 2026-01-18** (final release v2.31.0) carrying **three unfixed decoder advisories**,
all about decoding untrusted input, all `Fixed in: N/A`:

| ID | Summary |
| --- | --- |
| GO-2026-5046 | CPU exhaustion in the array/map decoders |
| GO-2026-5047 | Integer overflow in cumulative-size arithmetic |
| GO-2026-5048 | Unbounded map allocation |

**Decision: replace the codec by switching the module import from `github.com/hamba/avro/v2` to
`github.com/iskorotkov/avro/v2`** (an actively maintained fork that has fixed all three
advisories), **and keep the input-bounding mitigation from
[`ConduitIO/conduit-commons#278`](https://github.com/ConduitIO/conduit-commons/pull/278) as
permanent defense-in-depth regardless of codec.** Self-forking is rejected (permanent maintenance
burden a solo maintainer should not take on when a maintained alternative exists). Replacing with
`linkedin/goavro` is rejected (that project has declared itself in maintenance mode and its own
maintainers moved to hamba/avro — the thing we're trying to get away from). An official Apache
Avro Go SDK does not exist to evaluate. Accepting the risk indefinitely without replacing is
rejected as a terminal state, though it is exactly the right _near-term_ action, which is why the
mitigation PR already shipped it independently of this decision.

This doc covers the replace decision. #278 (already open, not merged) covers the mitigation and
is not gated on this doc landing — the two are independently useful.

## How it was found

Filed as [`ConduitIO/conduit#2817`](https://github.com/ConduitIO/conduit/issues/2817), found while
adding a `govulncheck` gate (#2816) and preparing `conduit-connector-pgvector` for its first
signed release. `go mod why -m github.com/hamba/avro/v2` traces reachability through
`pkg/plugin/processor/builtin/impl/avro/internal` → `conduit-commons/schema` →
`conduit-commons/schema/avro` — the engine's own built-in processor, not a dead SDK branch.

The issue's author checked `github.com/iskorotkov/avro/v2` and concluded it was "not an escape
hatch" because it appears in the same advisories as an affected package. That's true as of when
the advisories were published, but incomplete: the advisories mark it **fixed** at v2.33.0, while
hamba/avro is marked fixed nowhere, ever. This doc treats that as an open question worth actually
resolving rather than a closed one — the rest of the doc is that resolution.

## Problem

hamba/avro cannot be patched (repository archived, no path to a new release) and carries three
advisories reachable by decoding Avro bytes from an upstream Conduit does not control — exactly
the pipeline engine's job. Every future `govulncheck` run, every SLSA/provenance conversation
about a signed release, and every security-conscious user's dependency audit will flag these
three findings forever, because "Fixed in: N/A" never changes for an archived project. A decision
that ends in "we bounded the input and moved on" is a decision to carry that forever.

## Constraints

- **Solo maintainer.** DeVaris + Claude. Any option whose ongoing cost scales with "own an Avro
  codec" competes directly against every other roadmap item, indefinitely.
- **Serialized-data compatibility.** Whatever decodes Avro bytes today must keep decoding the same
  bytes the same way. Avro records already written to Kafka topics, files, or downstream stores by
  existing pipelines must remain readable.
- **Blast radius.** The codec is used in `conduit-commons/schema/avro` (a separate, versioned
  repo), the engine's built-in Avro processor, and transitively by every consumer of the SDK's
  schema support (connectors, per the issue's `conduit-connector-pgvector` trace). A replacement
  that changes the public API multiplies the migration cost by every consumer.
- **No connector-protocol change.** Swapping an internal codec dependency must not touch
  `conduit-connector-protocol` — connectors don't see hamba/avro directly, they see
  `conduit-commons/schema`'s `Serde` type.
- **The near-term mitigation (#278) is already in flight** and intentionally decoupled from this
  decision — it should not be blocked on, or used as an excuse to defer, the replace decision.

## Options considered

### 1. Fork and maintain hamba/avro ourselves — rejected

Forking means owning a full Avro codec permanently: wire-format correctness (logical types,
unions, schema resolution, `reflect2`-based unsafe internals), performance parity, and picking up
any _future_ advisories with no upstream to compare against or pull fixes from. That is a
disproportionate, open-ended commitment for a solo maintainer, and it's unnecessary now that an
actively maintained alternative exists that has already done this work (see Option 3). Forking
would only become the right call if that alternative later goes stale too — see "Re-evaluation
trigger" below.

### 2a. Replace with `linkedin/goavro` — rejected

`linkedin/goavro` is not archived (1,071 stars, more than hamba ever had), but its own README says,
as of its most recent commit (2026-01-21, three days after hamba's archival):

> Internally, most of LinkedIn has moved over to use <https://github.com/hamba/avro> for Avro
> serialization/deserialization needs as we found it to be significantly more performant in
> large-scale scenarios. **goavro is in maintenance mode.**

LinkedIn's own maintainers moved _to_ hamba/avro — the library we're trying to get away from —
and have declared goavro itself in maintenance mode. Its commit history is small bug-fix PRs, not
active development; 85 open issues. Migrating to a codec whose own stewards call it a legacy
option, in exchange for the largest possible migration cost, is a bad trade:

- **Different API paradigm.** goavro decodes into untyped native Go values via
  `Codec.NativeFromBinary([]byte) (any, []byte, error)`, not hamba's reflection-based
  `Unmarshal(schema, data, &v)` binding to arbitrary Go types. `conduit-commons/schema/avro`'s
  `Serde`, `unionResolver`, `extractor`, and `avro_builder` are all built around hamba's
  `avro.Schema` type hierarchy (`NewRecordSchema`, `NewMapSchema`, `NewUnionSchema`, ...) and would
  need to be substantially rewritten, not import-swapped.
- No compensating maintenance benefit for that cost.

### 2b. Replace with an official Apache Avro Go SDK — does not exist

Checked `apache/avro`'s `lang/` directory directly: `c++, c, csharp, java, js, perl, php, py,
ruby, rust`. No `go`. Apache does not maintain a Go Avro implementation; there is nothing here to
evaluate. (This is why hamba/avro became the de facto standard in the first place — it filled a
gap Apache never covered for Go.)

### 2c. Replace with `github.com/iskorotkov/avro/v2` — recommended

A fork of hamba/avro created specifically in response to the archival. Its README states this
directly:

> hamba/avro was archived in January 2026 and will receive no further updates or bug fixes. There
> is no other actively maintained Go Avro library that matches its performance. This fork exists
> to keep the library maintained, fix bugs, and improve it further.

Verified, not assumed:

- **Not archived**, pushed as recently as 2026-08-19 (4 days before this doc). Real ongoing
  development, not just security patches: a refactor replacing `json-iterator` with stdlib
  `encoding/json` (2026-08-19), a buffer-ownership fix in its new SOE codec with a regression test
  (2026-08-17/18), routine dependency bumps via Dependabot, CI via GitHub Actions, coverage
  tracked via Coveralls.
- **Fixes all three advisories.** GO-2026-5046, GO-2026-5047, and GO-2026-5048 are each listed
  `fixed: 2.33.0` for `github.com/iskorotkov/avro/v2` in the Go vulnerability database (checked
  directly against `vuln.go.dev`), with linked fix commits. Current release is v2.34.0
  (2026-08-18).
- **Credited by the same people who found the bugs.** The advisories credit Daniel Błażewicz
  (reporter) and Ivan Korotkov (the fork's maintainer) jointly — this isn't a random fork picking
  up someone else's CVEs, the fork's maintainer is part of the fix.
- **Byte-identical wire format.** It's a fork, not a rewrite — no serialized-data compatibility
  question. Avro bytes written by hamba/avro today decode identically (module-path swap
  notwithstanding — verified below).
- **Near-zero API migration cost, verified directly**, not assumed: cloned `conduit-commons`,
  mechanically replaced every `github.com/hamba/avro/v2` import with
  `github.com/iskorotkov/avro/v2` (module path only, zero other code changes), ran
  `go get github.com/iskorotkov/avro/v2@v2.34.0 && go mod tidy`, and built and tested the whole
  repo:
  - `go build ./...` — clean, no code changes needed beyond the import path.
  - `go test ./... -race` — **one failure**: `TestSerde_MarshalUnmarshal/[]any_(no_data)`. hamba
    decodes an empty Avro array into `[]any(nil)`; iskorotkov's fork decodes it into `[]any{}`.
    Notably, `conduit-commons`'s own existing test already carries the comment
    `wantValue: []any(nil), // TODO: smells like a bug, should be []any{}` on this exact case —
    the fork's behavior matches what `conduit-commons`' own maintainers flagged as the _correct_
    behavior. This is a real, if narrow, compatibility difference to account for in the migration
    PR (update the test expectation; audit whether any consumer branches on nil-vs-empty for a
    decoded empty array — the built-in Avro processor and connector SDK are the places to check),
    not a "purely mechanical, zero-risk" swap.
  - **Forward-compatible with the #278 mitigation as written.** The fork's `Config` struct is a
    superset of hamba's — it adds `MaxMapAllocSize` (the exact knob GO-2026-5048's advisory says
    is the fix, opt-in, defaulting to unbounded) alongside the existing `MaxByteSliceSize` and
    `MaxSliceAllocSize`. `limits.go`'s `avro.Config{MaxByteSliceSize: ..., MaxSliceAllocSize:
    ...}.Freeze()` compiles unchanged against the fork (Go struct literals with named fields
    tolerate new fields elsewhere), and gains a real path to closing the map-cardinality gap by
    adding one more field — which #278 could not do against hamba/avro because the field didn't
    exist there.
  - **Toolchain bump.** The fork's `go.mod` declares `go 1.24.13` (`conduit-commons` currently pins
    `go 1.24.2`). Routine, but real — `go.mod`'s `go` directive is a minimum toolchain constraint,
    and this is one more line item for the migration PR, not a blocker.

**Residual risk of depending on iskorotkov/avro:** it is still a single-maintainer project, and
could itself go quiet someday — the same risk category as any small dependency, including the one
it replaces. The difference is degree, not kind: adopting it costs us nothing we don't already
have (we are not maintaining it), and if it does go stale, we are in exactly today's position,
except with months or years of runway behind us and a smaller, well-understood diff from
hamba/avro to catch up on if we ever do need to fork. See "Re-evaluation trigger" below for what
would move this from "acceptable risk" to "act now."

### 3. Bound the input and accept the residual (no replace) — rejected as a terminal state

This is what #278 already does, and it remains the correct thing to have shipped immediately
regardless of this decision — it reduces real exposure today without waiting on a codec
migration, and #278's `limits.go` documents in detail which of hamba/avro's advisories are and
aren't addressable this way (short version: input-size and array-allocation bounds are
enforceable via `Config`; map-cardinality and recursion-depth bounds are not, because no such
`Config` field exists in any hamba/avro release). What this option rejects is treating that as the
_final_ state: it leaves Conduit's own built-in processor permanently carrying three
un-clearable vulnerability-scanner findings, with the most severe of the three (unbounded map
allocation) mitigated only by bounding blast radius, never closed. With Option 2c available at
near-zero cost, there's no reason to settle for "bounded but permanently vulnerable" as the end
state.

## Decision

**Replace `github.com/hamba/avro/v2` with `github.com/iskorotkov/avro/v2` across
`conduit-commons/schema/avro`, `conduit`'s built-in Avro processor, and any other direct
consumers.** Keep #278's `MaxInputSize` / tightened `Config` bounds in place afterward as
permanent defense-in-depth — they cost nothing to keep, and "the codec we depend on is well
maintained" is not a reason to stop bounding untrusted input. Once on the fork, add
`MaxMapAllocSize` to `decodeAPI` in `limits.go`, closing the one gap #278 could not close against
hamba/avro.

This is a **Tier 1** change per `CLAUDE.md` (data path: it changes what decodes every Avro record
flowing through the engine) and requires a design doc — this one — plus DeVaris's sign-off before
the migration PR, and chaos/upgrade tests updated or justified as unaffected (see below).

### Scope of the migration PR (separate from this doc)

1. `conduit-commons`: module-path swap in `go.mod` and all `github.com/hamba/avro/v2` imports;
   fix the one known test divergence (`[]any(nil)` → `[]any{}`); audit `union.go`,
   `avro_builder.go`, `extractor.go` for any other nil-vs-empty or similar edge-case divergence
   beyond what the existing test suite already covers — the one found here was caught by an
   existing test, but the audit should not stop at "the tests still pass," given hamba/avro's own
   `TODO` comment shows at least one previously-undetected edge case existed for years.
   `go.mod`'s `go` directive bump to at least 1.24.13.
2. `conduit`: bump the `conduit-commons` dependency; verify the built-in Avro processor's
   acceptance tests and any Avro-specific integration tests pass unchanged.
3. Any other direct `hamba/avro/v2` consumer surfaced by a repo-wide `go mod why` sweep across
   `ConduitIO/*` (the issue's own investigation found `conduit-connector-pgvector` reaching it
   transitively through the SDK's schema support — that path updates automatically once
   `conduit-commons` is bumped, no separate connector-side change expected, but should be
   confirmed with a real build+test run, not assumed).
4. Add `MaxMapAllocSize` to `limits.go`'s `decodeAPI`, with a justified value following the same
   pattern as the existing `MaxByteSliceSize`/`MaxSliceAllocSize` comments.

### Re-evaluation trigger

Revisit this decision (most likely toward forking `iskorotkov/avro` ourselves, since by then the
diff from a common ancestor will be small and well understood, rather than starting a fresh
fork-vs-replace search) if any of the following happens:

- `iskorotkov/avro` goes 6 months without a commit, or
- A new advisory is filed against it with no fix landed within a normal patch cadence (weeks, not
  months), or
- Its maintainer signals (via README, an issue, or an archival) that it is itself winding down.

This is a standing check, not a one-time judgment call — put it on whatever cadence `govulncheck`
or dependency-review already runs on, since a `govulncheck` hit against `iskorotkov/avro/v2` would
surface the "new advisory, no fix" case automatically.

## Failure-mode analysis

1. **Migration introduces a silent decode difference beyond the one already found.** Mitigated by
   the audit called for in the migration PR scope above, plus running the full existing
   `schema`/`schema/avro` test suite (which already caught the one known divergence) and the
   built-in Avro processor's acceptance tests before merging. If a divergence reaches production
   undetected, the failure mode is a wrong decoded value for some field shape, not a crash —
   invariant 6 ("schema handling never silently mangles data") is the one to keep in mind
   reviewing this migration specifically, given the nil-vs-empty precedent.
2. **`iskorotkov/avro` has a bug hamba/avro didn't.** Possible for any dependency swap. Mitigated
   by: it's a fork, not a rewrite, so the surface of genuinely new code is small (mostly the fixed
   advisories plus incidental refactors like the json-iterator removal); the existing
   `schema/avro` test suite and the built-in processor's acceptance tests exercise the shared
   decode path either way; and #278's input-bounding mitigation stays in place as a backstop
   regardless of which codec is behind it.
3. **`iskorotkov/avro` is later archived too.** Covered by the re-evaluation trigger above — not
   a silent failure, since `govulncheck` (once #2816 lands) or routine dependency review surfaces
   an unmaintained-with-open-advisory state the same way it surfaced hamba/avro's.
4. **Migration PR ships without updating every consumer, leaving a stale `hamba/avro/v2` import
   somewhere in the org.** Mitigated by the repo-wide `go mod why` sweep called for in scope item
   3, and by the fact that `govulncheck` (#2816) would immediately flag any repo still on
   hamba/avro post-migration — this failure mode is self-detecting once that gate exists, and
   should be checked manually in the interim since #2816 isn't confirmed live yet.
5. **Toolchain bump (`go 1.24.13`) breaks a CI image or a contributor's local toolchain pin.**
   Low risk (patch-level Go bump, not a major version), but real; the migration PR should call it
   out explicitly and confirm CI's Go version already satisfies it or bump CI alongside.

## Backward compatibility & rollback

- **No serialized-Avro-data compatibility concern.** `iskorotkov/avro` is a fork of the exact
  decoder logic in use today; wire format is unchanged. Records written by hamba/avro remain
  readable, and vice versa (relevant only during a rollback window, not as an ongoing
  cross-version concern).
- **No connector-protocol change.** Connectors interact with `conduit-commons/schema.Serde`, not
  the underlying codec directly; nothing crosses the protocol boundary here.
- **Rollback is a dependency revert.** If the migration surfaces a problem post-merge, reverting
  `conduit-commons`'s `go.mod`/imports back to `github.com/hamba/avro/v2` and re-bumping
  `conduit`'s dependency restores exactly today's (archived-but-working) state. No data migration
  either direction — this is purely a code-dependency change, not a state-format change, so none
  of the position/checkpoint versioning machinery in `CLAUDE.md`'s backward-compatibility section
  applies here.
- **`MaxInputSize` and the rest of #278 are independent of this decision** and need no rollback
  consideration tied to it — they work identically against either codec.

## Observability

- Once #2816's `govulncheck` gate is live, a stale `hamba/avro/v2` reference anywhere in the org
  becomes self-reporting (the three advisories keep firing until every consumer is migrated).
  Until then, the `go mod why` sweep in the migration PR is the manual substitute — call this out
  explicitly in that PR's description so it isn't silently skipped.
- No new runtime metric is warranted by this change alone — it's a dependency swap behind an
  unchanged `Serde` API, not a new capability with its own failure modes to instrument. The
  existing `ErrInputTooLarge`/`ErrUnsupportedType`/`ErrSchemaValueMismatch` error paths from #278
  and the pre-existing `schema/avro` package remain the operator-facing signal for a rejected or
  malformed Avro payload.
- Add a note to the migration PR's description recording the `iskorotkov/avro` version pinned and
  the date of the last verified "not archived, has a recent commit" check, so a future reviewer
  auditing dependency health doesn't have to redo this doc's research from scratch.

## Rollout

1. This doc → DeVaris Tier-1 sign-off on the replace decision (fork target: `iskorotkov/avro/v2`).
2. Migration PR against `conduit-commons` (scope above): module-path swap, fix the known test
   divergence, audit for others, bump `go.mod`'s `go` directive, add `MaxMapAllocSize` to
   `limits.go`. Tier 1: human sign-off, failure-mode analysis restated against the actual diff.
3. Bump `conduit`'s `conduit-commons` dependency; run the built-in Avro processor's acceptance
   tests and any Avro-touching integration/chaos suites.
4. Repo-wide `go mod why -m github.com/hamba/avro/v2` sweep across `ConduitIO/*` to confirm no
   other direct consumer was missed.
5. No feature flag — this is a dependency substitution behind an unchanged API, not a new
   capability; the existing test suites are the gate.

## Risk tier & related

**Tier 1** (data path: changes the decoder behind every Avro record the engine processes,
including its own built-in processor). Requires DeVaris's explicit sign-off on the replace
decision before the migration PR, per `CLAUDE.md`.

- [`ConduitIO/conduit#2817`](https://github.com/ConduitIO/conduit/issues/2817) — the issue this doc
  and #278 jointly resolve.
- [`ConduitIO/conduit-commons#278`](https://github.com/ConduitIO/conduit-commons/pull/278) — the
  near-term input-bounding mitigation, open, not merged, not gated on this doc.
- `ConduitIO/conduit#2816` — the `govulncheck` gate whose absence is why this required manual
  `go mod why` archaeology instead of an automated finding.
- `github.com/iskorotkov/avro/v2` — <https://github.com/iskorotkov/avro>, the recommended
  replacement.
