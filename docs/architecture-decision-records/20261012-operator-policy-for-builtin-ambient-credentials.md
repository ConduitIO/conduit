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

- Add `connectors.ambient-credentials.{enabled,allow-endpoints,allow-resources,allow-any-resource,allow-proxy}`,
  default off. `enabled=true` requires a non-empty `allow-resources` (exact bucket names, no wildcards) or
  `allow-any-resource`, and refuses to start with a proxy variable set unless `allow-proxy` is true.
- Pass the policy to built-in connectors on the context, through a small additive
  `conduit-commons/hostpolicy` package. Absent policy means deny. No `conduit-connector-sdk` or
  `conduit-connector-protocol` change.
- Accept a pipeline-supplied endpoint only on an exact, https `allow-endpoints` match; otherwise use only
  the SDK's endpoint resolution (the check runs on the endpoint the SDK resolved, whatever its source:
  `AWS_ENDPOINT_URL*`, profile `endpoint_url`, `services` section). Refuse cross-host redirects and link-local
  addresses (`169.254.0.0/16`, `fd00:ec2::254`, `fd00:ec2::23`, IPv4-mapped forms), except under `allow-proxy`.
  Credential-acquisition overrides (`AWS_ENDPOINT_URL_STS`, `_SSO`, IMDS) are out of scope. Exactly one static key set is a config error, never a fall-through to ambient.
- Standalone plugins are out of scope; see #3043.

## Consequences

- Operators who want ambient credentials must configure an allowlist; "any bucket" needs an explicit
  setting.
- A pipeline must say `builtin:s3` to get the built-in connector when a registry `s3` is installed,
  because a bare name resolves to the standalone plugin first.
- The registry copy of `s3` cannot use the ambient chain through this policy.
- Static keys with a custom internal endpoint are unchanged and remain out of scope.
- `conduit-commons` gains a package and needs a release before the `conduit` and `s3` changes.

## Related

- `docs/design-documents/20261012-builtin-connector-ambient-credentials.md`
- `conduit-connector-s3#1021`, `#1027`
- `docs/design-documents/20260726-wasm-host-egress-capability.md`
