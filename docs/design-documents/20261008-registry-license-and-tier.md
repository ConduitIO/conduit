# Registry index: license and tier fields

## Summary

Add two optional fields to every connector and processor entry in the registry index: `license` (an SPDX identifier
or simple expression) and `tier` (`certified`, `verified`, `adapter` or `community`). Both are added additively under
`schemaVersion` 1, carry `omitempty`, and are display and filtering metadata only. No trust, resolution or install
decision reads them.

## Problem

The registry is about to list connectors from `conduitio-labs` next to the ones the Conduit project maintains
(catalog inventory, #2951). A user picking a connector needs to know two things the index cannot express today:

- whether the Conduit project stands behind it, or it is published as-is by someone else;
- what license the source is under. Thirty repositories in the catalog had no detectable license file until
  recently, and the registry should not imply a license it has not recorded.

Without fields for these, the only options are to keep community connectors out of the registry or to put this
information in free-text `description`, which neither the CLI nor the web UI can filter on.

## Constraints

- The payload is signed. Anything added lives inside `payload`, so it is covered by the root signature, and must not
  change the canonical bytes of entries that do not use it.
- Older clients must keep working: they verify the signature over the whole payload they received, then unmarshal into
  their own typed struct. Go ignores unknown keys, so an additive optional field is safe; a new required field or a
  bumped `schemaVersion` is not.
- The JSON Schema sets `additionalProperties: false`, so the registry's index-CI rejects the new keys until the schema
  declares them.
- Tier must not become a trust input. Signature, identity pinning and provenance stay the only things that decide
  whether an artifact installs.

## Decision

- `connector.license` and `processor.license`: optional string, 1–128 characters, matching an SPDX identifier or a
  simple `AND` / `OR` / `WITH` expression.
- `connector.tier` and `processor.tier`: optional, enum `certified | verified | adapter | community`, defined once in
  `$defs/tier`.
  - `certified`: maintained by the Conduit project and meets the certification bar (acceptance and integration tests
    against the real system in CI, kill -9 chaos test, committed benchi run, docs).
  - `verified`: acceptance suite in CI, signed artifacts and provenance, without the full certification bar.
  - `adapter`: a bridge to another plugin ecosystem, such as the Kafka Connect wrapper.
  - `community`: published as-is by its maintainers, no support commitment from the project.
- Go: `License string` and `Tier Tier` (a string type with constants) on `index.Connector` and `index.Processor`, both
  `omitempty`. The Go type does not reject unknown tier values; the enum is enforced by the JSON Schema at index-CI
  time. That keeps a future tier from breaking older clients.
- Tier is set by registry maintainers in the per-entry index files. A publisher's release workflow never writes it.
- `MaxSupportedSchemaVersion` stays 1.

## Alternatives considered

1. **Put the classification in `description` or a free-form `labels` map.** Rejected: not filterable without parsing,
   and a free-form map invites ad-hoc keys that become de facto contract without review.
2. **Bump `schemaVersion` to 2.** Rejected: every client before the bump would refuse the whole index with
   `CodeSchemaTooNew` for two informational fields. The processors[] addition set the precedent for additive optional
   fields under version 1.
3. **Keep tier outside the signed payload.** Rejected: anything outside `payload` is unauthenticated, and a tampered
   tier would let a mirror relabel a community connector as certified.
4. **A closed Go enum that rejects unknown values.** Rejected: an older client would then fail to parse an index that
   uses a tier added later. Validation belongs at index-CI, where the schema is current.

## Failure modes

1. **Older client receives an index with the new fields.** It verifies the signature over the bytes it received
   (fields included) and ignores the unknown keys when unmarshalling. Covered by
   `TestVerify_ForwardCompat_OlderClientIgnoresLicenseAndTier`.
2. **Entries without the fields change bytes on re-marshal.** Prevented by `omitempty`; asserted in
   `TestConnectorOnlyIndex_OmitemptyKeepsBytesIdentical`, so existing signed fixtures and content-subtree hashes do not
   drift.
3. **Bad values reach the index.** Index-CI validates against the schema: unknown tier, empty or free-text license are
   rejected (`TestFrozenSchema_RejectsBadLicenseAndTier`).
4. **Tier used as a trust signal.** No code path in `pkg/registry` reads it. If the UI or CLI shows it, it must sit
   next to, not replace, the verified-signature status.
5. **Wrong license recorded.** Informational only, but misleading. Entries are added by human-reviewed PRs to the
   registry repository; the reviewer checks the repository's `LICENSE` file.

## Upgrade / rollback

- Upgrade: clients built with this change read the fields; older clients ignore them. The registry repository must
  update its copy of `index-schema.json` (and its web types) before any entry uses the fields, or its index-CI rejects
  them.
- Rollback: removing the fields from entries and re-signing restores the previous bytes. Reverting this code leaves
  clients unable to see the fields but still able to verify and install.

## Observability

None needed at runtime: the fields do not affect any decision. Showing them in `conduit connectors` output and the
registry web UI, and filtering by tier, are follow-up work.
