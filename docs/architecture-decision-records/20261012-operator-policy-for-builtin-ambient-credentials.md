# Ambient cloud credentials in built-in connectors are an operator decision

## Summary

Built-in connectors may use the host's ambient cloud identity (the AWS default credential chain) only
when the operator enables it in the engine configuration. The setting is off by default, cannot be
changed from a pipeline config, and reaches connectors through the call context. When on, custom
endpoints must be listed exactly by the operator and use https, cross-host redirects and link-local
addresses are refused, and a resource allowlist is required.

## Context

Making `aws.accessKeyId` and `aws.secretAccessKey` optional in the S3 connector (s3#1027) would let any
pipeline author act as the Conduit host. The HTTP API has no authentication and binds all interfaces by
default, and a user-set `aws.endpoint` would receive requests signed with the host's credentials. A
suffix rule for "AWS hosts" does not help, because tenants control hosts under `amazonaws.com`. The full
analysis, alternatives and failure modes are in
`docs/design-documents/20261012-builtin-connector-ambient-credentials.md`.

## Decision

- Add `connectors.ambient-credentials.{enabled,allow-endpoints,allow-resources,allow-any-resource}`,
  default off. `enabled=true` requires a non-empty `allow-resources` or `allow-any-resource`.
- Pass the policy to built-in connectors on the context, through a small additive
  `conduit-commons/hostpolicy` package. Absent policy means deny. No `conduit-connector-sdk` or
  `conduit-connector-protocol` change.
- Accept a pipeline-supplied endpoint only on an exact, https `allow-endpoints` match; otherwise use only
  the SDK's endpoint resolution. Refuse cross-host redirects and `169.254.0.0/16` / `fd00:ec2::254`.
- Standalone and external connectors are out of scope for v0.21. Protection there is cooperative only,
  because the policy is not delivered to them and standalone plugins inherit the engine environment.

## Consequences

- Operators who want ambient credentials must configure an allowlist; "any bucket" needs an explicit
  setting.
- A pipeline must say `builtin:s3` to get the built-in connector when a registry `s3` is installed,
  because a bare name resolves to the standalone plugin first.
- The registry copy of `s3` cannot use the ambient chain until a v0.22 follow-up.
- Static keys with a custom internal endpoint are unchanged and remain out of scope.
- `conduit-commons` gains a package and needs a release before the `conduit` and `s3` changes.

## Related

- `docs/design-documents/20261012-builtin-connector-ambient-credentials.md`
- `conduit-connector-s3#1021`, `#1027`
- `docs/design-documents/20260726-wasm-host-egress-capability.md`
