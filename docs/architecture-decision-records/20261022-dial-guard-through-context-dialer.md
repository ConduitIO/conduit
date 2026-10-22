# Hand the connector dial guard to built-in connectors through a context dialer

## Summary

The engine enforces `connectors.egress.allow` and the metadata-address refusal at dial time for
built-in connectors by putting `connegress.Policy.DialContext` on the call context with the
`conduit-commons/dialer` package. Connectors install it on every client they build. Standalone
connectors are out of scope for v0.21.

## Context

The setting check made at configure time can disagree with what a client library later dials (name
re-resolution, fallbacks, brokers discovered later). Decided 2026-10-22: configure-time checking alone is
not acceptable for v0.21. Details in `docs/design-documents/20261022-connector-dial-guard.md`.

## Decision

1. The seam is `dialer.WithDialContext` / `dialer.FromContext` in `conduit-commons`, absent means the
   connector keeps its library default (not a deny, not a substituted `net.Dialer`).
2. The engine applies it to the postgres, kafka and s3 built-in connectors; the policy used is the
   operator's `connegress.Policy`, with the metadata refusal always on.
3. Standalone connectors receive no dialer and are not restricted by the allowlist for v0.21; the engine
   warns at startup when the allowlist is set. An env-driven standalone path is a separate design.
4. Cooperative enforcement: a connector that does not read the seam is not guarded. The engine
   guards against missing adoption by tests that fail when a built-in connector in `go.mod` does not
   honor the dialer.

## Consequences

- The dial guard is live for built-in connectors once the engine depends on connector releases that read
  the seam. Until then the setting check is the only enforcement and the docs say so.
- DNS failures at dial now stop reconnects for restricted targets (they cannot be vetted).
- Standalone connectors, including MySQL, are unrestricted in v0.21.
- Backporting to v0.20.x is a separate maintainer decision.

## Related

- `docs/design-documents/20261022-connector-dial-guard.md`
- `docs/architecture-decision-records/20261021-deny-by-default-settings-and-outbound-access.md`
- `docs/operations/connector-egress.md`
