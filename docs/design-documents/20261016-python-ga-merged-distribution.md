# Python GA: one distribution, two namespaces

## Summary

Ship the Python connector SDK and the Python embedded client from a single
distribution whose top-level package is `conduit`, exposing two namespaces:
`conduit.connector` (the SDK) and `conduit.client` (the gRPC control client).
This resolves the current collision where two repositories each publish a
top-level `conduit/__init__.py`. The distribution is the vehicle for both GA
tags and for `conduit connector new --lang python`.

## Context

Two repos exist: `conduit-connector-sdk-python` and `conduit-client-python`.
Both install a top-level `conduit` package, so installing both is impossible.
The 20260724 embedding docs
(`20260724-embed-grpc-client-libraries.md`, ADR
`20260724-embed-bindings-via-grpc.md`) already decide that only Go embeds
in-process and that every other language talks to the engine over gRPC. Python
is the first second-language consumer, so it is where the distribution shape is
first made concrete.

## Decision

One Python package, `conduit`, with:

- `conduit.connector` — the connector SDK (Source, Destination, run entrypoint).
- `conduit.client` — the engine control client (pipelines, connectors,
  processors over the gRPC control API).

The `conduit` package is a namespace package (PEP 420) so a future independent
install of only one namespace does not break the other. The two repos are merged
into one distribution before either namespace is tagged GA; the connector SDK
and the client share the version and the support window.

`conduit client` manages a **local or remote** engine over gRPC. Docs state
plainly that only Go embeds in-process; Python is always out-of-process.

## Public API and versioning

- Typed public API (no untyped `dict` boundaries in the SDK surface).
- Semantic versioning on the one distribution; a breaking change to either
  namespace bumps the shared version.
- A documented Python support window (minimum supported Python version and how
  it advances), stated in the README and enforced in CI.

## Acceptance and scaffolding

- A Python connector passes acceptance suite v2 in CI against a real Conduit.
- `conduit connector new --lang python` scaffolds a connector that passes that
  same acceptance run. The scaffold uses the `conduit.connector` import, so the
  tutorial and the scaffold agree on the import path.

## Failure modes

- **Installing an old single-repo package over the merged one.** The old
  top-level `conduit` package is not import-compatible. The migration is a
  documented reinstall; no import-path shim is shipped (the old packages were
  not GA).
- **A connector written against the pre-merge import.** Same as above: the SDK
  was not GA, so the only supported import path is `conduit.connector`.
- **Only one namespace needed.** The namespace-package layout lets a tool or
  user depend on `conduit.connector` without pulling client dependencies and
  vice versa, within the one distribution's constraint.

## Upgrade / rollback

No stored state. Upgrade is a package reinstall; rollback is reinstalling the
previous version. The only compatibility contract is the Python API, which is
not GA before this ships, so no back-compat shim is required.

## Related

- #3019 (the lane), `conduit-connector-sdk-python#5` (the collision).
- `20260724-embed-grpc-client-libraries.md`,
  `20260724-embed-bindings-via-grpc.md`.
- SDK parity (#3020, #2948) lists Python as a full-tier language.
