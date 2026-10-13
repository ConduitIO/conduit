# Operator control over ambient credentials in built-in connectors

## Summary

A built-in connector that falls back to the process's ambient cloud identity (AWS default credential
chain: environment, `~/.aws`, web identity, ECS/EC2 role) lets any pipeline author act as the Conduit
host. Conduit's API is unauthenticated by default, so "pipeline author" means "anyone who can reach the
API". This design adds an operator-owned, engine-level policy that built-in connectors can read but
pipeline configs cannot change: ambient credentials are off by default; when on, custom endpoints must be
on an operator allowlist; and redirects are never followed to hosts the policy does not permit. The
engine hands the policy to built-in connectors through the call context, using a small shared package in
`conduit-commons`. No `conduit-connector-sdk` change and no `conduit-connector-protocol` change is
needed. Standalone connector plugins are out of scope for v0.21 and fail closed.

This unblocks `conduit-connector-s3#1027` (default credential chain, issue #1021), which stays a draft on
hold until this is signed off.

Risk tier: Tier 1 (security boundary; touches how connectors authenticate to external systems). It does
not change record flow, acks, positions or serialization.

## Context

### What happened

`s3#1027` made `aws.accessKeyId` and `aws.secretAccessKey` optional and fell back to the AWS default
chain. An automated security review found two problems:

1. **Confused deputy.** A pipeline author inherits the host's identity. With the API unauthenticated by
   default, anyone who can create a pipeline can read and write every bucket the host role can reach, and
   can point `bucket` at a bucket they own that grants the host's account write access, which is an
   exfiltration path that needs no custom endpoint.
2. **Exfiltration and SSRF.** `aws.endpoint` (s3#994) is user supplied. With ambient credentials the
   connector sends SigV4-signed requests, made with host credentials, to whatever host the author names.
   Go's HTTP client strips `Authorization` on a cross-host redirect but copies other headers, and SigV4
   also sends `X-Amz-Security-Token`; a redirect from an allowed host therefore leaks a session token
   unless the client refuses it.

### How built-in connectors are built today

Read from this repo at `81c6306`:

- `pkg/conduit/runtime.go` calls `conn_builtin.NewRegistry(logger, r.Config.ConnectorPlugins,
  connSchemaService)`. `ConnectorPlugins` defaults to `builtin.DefaultBuiltinConnectors` (file,
  generator, kafka, log, postgres, s3), compiled into the binary.
- `Registry.NewDispenser` returns a dispenser whose factory wraps the connector in
  `sdk.NewSourcePlugin(conn.NewSource(), cfg, params)` / `sdk.NewDestinationPlugin(...)`. `cfg` is
  `pconnector.PluginConfig` (`Token`, `ConnectorID`, `LogLevel`, `Grpc`). It carries no operator
  settings, and it is a protocol type.
- The engine-side adapters in `pkg/plugin/connector/builtin/source.go` and `destination.go` wrap every
  call with `withLogger(ctx)` and run it through `runSandbox`. That is a single choke point where the
  engine already decorates the context.
- The SDK adapter calls `internal.Enrich(ctx, cfg)` and then passes a context derived from the engine's
  (it uses `ccontext.Detach`, which keeps values) to `Open`, `Run`, `Write` and so on. Values the engine
  puts on the context reach the connector.
- The only existing engine-to-built-in channel is a package global: `NewRegistry` sets
  `schema.Service = service` in the SDK.
- Operator settings live in `conduit.Config` (`pkg/conduit/config.go`): flags, `conduit.yaml` and
  `CONDUIT_*` environment variables map to one struct. `processors.egress.*` is the precedent for a
  security ceiling (default deny, parsed and validated at startup, a malformed entry is a startup error,
  never a silent disable). `api.allow-live-restart-apply` is the precedent for a boolean operator
  authorization that no API caller can set. `CONDUIT_SECRET_*` is resolved only for the processor
  egress service and is not visible to connectors.
- Standalone connector plugins are separate processes started through
  `conduit-connector-protocol/pconnector/client`. The client sets `cmd.Env` to an empty slice with a
  comment saying "clean env", but go-plugin v1.8.0 then appends `os.Environ()` unless `SkipHostEnv` is
  set (`client.go:655`), and nothing sets it. So a standalone plugin inherits the host environment,
  including `AWS_*`, and can reach IMDS and `~/.aws` like any process on the host. The engine cannot
  stop a standalone plugin from using ambient credentials. It can only tell a cooperating plugin what
  the operator decided.

### Threat model

| Actor | Trusted? | Capability |
| --- | --- | --- |
| Operator (runs `conduit`, owns its config and environment) | Yes | Sets policy |
| Pipeline author (anyone with API/UI/pipeline-file access) | No, to the extent the API is open | Sets every connector setting in a pipeline config: `bucket`, `aws.endpoint`, `aws.region`, keys |
| Connector code (built-in, or installed by the operator) | Yes, cooperative | Reads policy and enforces it |
| Remote endpoint named by a pipeline author | No | Receives signed requests, may redirect |

In scope: a pipeline author using the host identity without the operator having allowed it; sending
host-credential-signed traffic to a host the operator did not approve; credential leakage through
redirects. Out of scope: a malicious connector binary (it already runs with the host's privileges); a
pipeline author with host shell access; limiting which buckets a permitted identity can reach beyond an
optional allowlist (IAM least privilege on the host role is the real boundary, and the docs must say so).

## Goals and non-goals

Goals:
- Ambient credentials are off unless the operator turns them on, and a pipeline config cannot turn them
  on.
- When on, the connector refuses non-AWS endpoints that the operator has not allowlisted, and refuses
  redirects to hosts the policy does not permit.
- The mechanism is generic (any built-in connector that wants ambient identity uses it), and needs no
  per-connector code in the engine.
- Refusals are stable, machine-actionable errors; use is observable.

Non-goals:
- Authenticating the Conduit API (separate work).
- Governing standalone plugins in v0.21.
- A policy language, per-pipeline grants, or multi-tenancy. This is a single operator ceiling.
- Changing how static keys work. Static keys in a pipeline config keep working exactly as today, with any
  endpoint, as they only expose what the author already holds.

## Decision

### 1. Engine configuration

New keys under `connectors.ambient-credentials`, next to `connectors.path`:

| Key (flag / yaml) | Env | Default | Meaning |
| --- | --- | --- | --- |
| `connectors.ambient-credentials.enabled` | `CONDUIT_CONNECTORS_AMBIENT_CREDENTIALS_ENABLED` | `false` | Built-in connectors may use the process's ambient cloud identity |
| `connectors.ambient-credentials.allow-endpoints` | `CONDUIT_CONNECTORS_AMBIENT_CREDENTIALS_ALLOW_ENDPOINTS` | empty | Extra non-AWS endpoints that may receive ambient-credential-signed requests. `scheme://host[:port]` entries, same syntax and parser as `processors.egress.allow` |
| `connectors.ambient-credentials.allow-resources` | `CONDUIT_CONNECTORS_AMBIENT_CREDENTIALS_ALLOW_RESOURCES` | empty (any) | Optional connector-defined resource names the ambient identity may touch; for `s3`, bucket names (exact match, or a single trailing `*`) |

```yaml
connectors:
  ambient-credentials:
    enabled: true
    allow-resources: ["acme-ingest-*"]
```

Behavior:
- Validated in `Config.Validate` the way `egressCeiling` is: a malformed endpoint is a startup error;
  `allow-endpoints` or `allow-resources` set while `enabled=false` is a startup warning, not an error (it
  is inert but harmless; failing would make a config that is off by default awkward to stage).
- Requests to AWS service endpoints (host suffixes `.amazonaws.com`, `.amazonaws.com.cn`, `.api.aws`)
  are always permitted when `enabled=true`. Partitions with other suffixes (for example ISO regions) and
  S3-compatible stores are reached through `allow-endpoints`. The suffix list lives in the S3 connector,
  because "what is an AWS host" is connector knowledge, not engine knowledge.
- The setting applies only to the ambient chain. If a pipeline supplies both static keys, the connector
  uses them and the policy is not consulted.

### 2. Passing the policy to connectors

A new package `github.com/conduitio/conduit-commons/hostpolicy` (name open, see Open questions):

```go
// Policy is the operator's decision about ambient credentials. The zero value denies.
type Policy struct {
    AmbientCredentials bool
    AllowEndpoints     []Endpoint // parsed scheme/host/port entries
    AllowResources     []string
    Observe            func(ctx context.Context, d Decision) // engine metric/log hook; may be nil
}

func WithPolicy(ctx context.Context, p Policy) context.Context
func FromContext(ctx context.Context) Policy // zero (deny) when absent
func (p Policy) CheckEndpoint(u *url.URL) error  // generic allowlist match
func (p Policy) RedirectGuard(allowHost func(host string) bool) func(*http.Request, []*http.Request) error
```

The engine's builtin `source.go` and `destination.go` adapters put the policy on the context next to
`withLogger`, for every plugin call, from a value held by the `Registry` and built once from
`conduit.Config` in `createServices`. A connector reads it with `hostpolicy.FromContext(ctx)` in
`Configure`/`Open` (source) and when it builds its client (destination), then keeps the decision with the
client it builds. Enforcement stays in the connector, because only the connector knows its HTTP client.

Properties that make this safe:
- **Absent means deny.** A connector run outside the engine, in a standalone plugin process, in a unit
  test, or by an older engine, sees the zero `Policy` and refuses ambient credentials. There is no
  "unset means allow" state to get wrong.
- **Pipeline authors cannot set it.** The context value is written by the engine, outside the config
  parsing path. A connector parameter cannot reach it, and the policy keys are not connector parameters.
- **Protocol and SDK untouched.** `pconnector.PluginConfig` stays as is; the SDK already forwards context
  values.

The error taxonomy and the redirect helper live in the same package so every connector refuses the same
way. Provider-specific parts (the AWS host suffixes, the SDK `config.LoadDefaultConfig` call, an
`http.Client` with `CheckRedirect` set) stay in the S3 connector.

### 3. Connector behavior (what `s3#1027` becomes)

When neither static key is set:

1. `policy.AmbientCredentials` false: fail at `Configure` with `connector.ambient_credentials_disabled`.
   No AWS call is made and the default chain is not loaded.
2. True: build the client with `LoadDefaultConfig` and an `http.Client` whose `CheckRedirect` is
   `policy.RedirectGuard(isAWSHost)`. A redirect to a host that is neither an AWS service host nor an
   allowlisted endpoint returns `connector.redirect_refused` and the request is not sent.
3. If `aws.endpoint` is set: it must be an AWS host or match `allow-endpoints`; otherwise
   `connector.endpoint_not_allowed`. Checked in `Configure`, so a bad pipeline fails when created or
   validated, not on the first record.
4. If `allow-resources` is non-empty, `bucket` must match; otherwise `connector.resource_not_allowed`.
5. `aws.region` is validated against `^[a-z0-9-]+$` before it can influence an endpoint.

Also checked on every dial/redirect, not only at `Configure`, so config reload or a server-chosen
redirect cannot widen the set.

### 4. Errors

Stable codes, each with the failing config path and a suggested fix. They are registered in
`conduiterr` (`connector.*`, gRPC `PermissionDenied`) by the engine; the connector returns a typed error
from the shared package carrying the code string, `ConfigPath` and `Suggestion`, and the builtin adapter
wraps it into a `ConduitError` at the plugin boundary (the connector cannot import `conduiterr`, since
the engine imports the connector).

| Code | Config path | Message | Suggestion |
| --- | --- | --- | --- |
| `connector.ambient_credentials_disabled` | `aws.accessKeyId` | ambient credentials are not enabled on this Conduit instance | set `aws.accessKeyId` and `aws.secretAccessKey`, or ask the operator to set `connectors.ambient-credentials.enabled=true` |
| `connector.endpoint_not_allowed` | `aws.endpoint` | endpoint host is not an AWS host and is not allowlisted for ambient credentials | use static credentials with this endpoint, or add it to `connectors.ambient-credentials.allow-endpoints` |
| `connector.redirect_refused` | `aws.endpoint` | refused redirect to a host that is not permitted | none for the pipeline author; report the endpoint to the operator |
| `connector.resource_not_allowed` | `bucket` | resource is not on the operator's allowlist | use an allowed resource or ask the operator to extend `connectors.ambient-credentials.allow-resources` |

Messages never include credential values, and the engine's existing log redaction applies.

### 5. Observability

- INFO log once per connector `Open`: `ambient credentials in use` with connector ID, plugin name,
  resolved region, and the endpoint host (never keys, never the role ARN session token).
- WARN log per refusal with the code and config path.
- Counter `conduit_connector_ambient_credentials_total{plugin, outcome}` where outcome is `used`,
  `denied_disabled`, `denied_endpoint`, `denied_redirect`, `denied_resource`, driven through
  `Policy.Observe`. (Metric name to be checked against the existing `conduit_*` naming when
  implemented.)
- `conduit run` startup logs the effective policy (enabled, number of allowlisted endpoints and
  resources) so an operator can see what is on.
- Runbook entry under `docs/operations/` (symptom: pipeline fails with one of the codes; diagnosis; fix).

### 6. Standalone plugins

Out of scope for enforcement in v0.21, and they fail closed by construction: a standalone process gets no
context value, so `FromContext` returns the deny policy. Consequence: the registry copy of `s3` cannot
use the ambient chain until a follow-up lands. Operators who need ambient credentials use the built-in
`s3`.

Follow-up for v0.22, needing its own short design: the engine adds `CONDUIT_CONNECTOR_AMBIENT_*`
variables to the plugin's environment next to the existing `EnvConduitConnectorToken` entries in
`standalone/registry.go`, and `conduit-connector-sdk`'s `Serve` converts them to the same context value.
That is an SDK change, not a protocol change. It stays cooperative, because a standalone plugin inherits
the host environment today and can ignore the policy. Setting `SkipHostEnv` in the protocol client would
make the "clean env" comment true, but it is a behavior change for every standalone plugin (they would
lose `PATH`, proxies, `AWS_*` they may rely on), so it needs its own decision and is not proposed here.

### 7. What this does and does not change in the SDK and protocol

| Component | Change | Notes |
| --- | --- | --- |
| `conduit` | Yes | config keys, validation, registry holds the policy, adapters add it to the context, error codes, metric, docs |
| `conduit-commons` | Yes, additive | new `hostpolicy` package; needs a release and a `go.mod` bump in `conduit` and `s3` |
| `conduit-connector-s3` | Yes | s3#1027 rebased onto the policy; the code moved into `config.LoadAWSConfig` stays |
| `conduit-connector-sdk` | No | context values already reach connector methods |
| `conduit-connector-protocol` | No | `PluginConfig` untouched; no versioning discussion needed |

## Alternatives considered

**A. The connector reads an operator environment variable** (for example
`CONDUIT_S3_ALLOW_AMBIENT_CREDENTIALS`). Pipeline authors cannot set process environment, so it holds
the main property, and it works with no engine change. Rejected as the design because it makes every
connector invent its own variable, name and parsing, with no common allowlist or redirect handling. It
is also invisible to `conduit run` (no startup validation, no policy log, no typo detection), and an
engine run in a container that forwards the whole environment to child processes would forward it too.
It remains acceptable as the shape of the v0.22 standalone follow-up, where there is no in-process
channel.

**B. Only STS assume-role with an explicit role ARN, never the whole chain.** Narrows the blast radius
because the pipeline names a role. Rejected: the call to STS is still made with the host identity, any
pipeline author can name any role the host may assume, and trust policies on the target role are not
written with "Conduit pipeline authors" in mind. It does not solve the confused deputy; it renames it.
It is still a good optional feature later, once this policy exists.

**C. Ambient credentials only for registry or standalone plugins.** Rejected: the standalone process
inherits the host environment (see Context), so this does not remove the confused deputy, and it
removes the built-in connector from the one place the engine can enforce policy.

**D. A pipeline-config flag** (for example `aws.allowAmbientCredentials: true`). Rejected immediately:
the pipeline author controls it, so it protects nothing.

**E. Put the policy in `pconnector.PluginConfig`.** Equivalent to the context approach for built-ins,
but `PluginConfig` is a protocol type shared with standalone plugins; adding fields forces a versioning
discussion for no gain. Rejected.

**F. A package-level setter in each connector module** (the engine calls `s3.SetPolicy(...)`). Works, but
puts per-connector code in the engine, which is the privileged integration this project avoids, and a
mutable global is shared by every pipeline in the process and by tests. Rejected. The existing
`schema.Service` global is a precedent we would rather not extend.

## Failure modes

| Failure | Behavior |
| --- | --- |
| Policy missing from context (older engine, standalone, test) | Deny. Ambient chain not loaded. Static keys still work |
| `enabled=true`, but the host has no credentials | The SDK resolves lazily; first call fails with the SDK error, nothing is acked (invariant 1). Same as the original #1027 behavior |
| Endpoint allowlist typo | Startup error for a malformed entry; a well-formed wrong entry causes `connector.endpoint_not_allowed` at pipeline `Configure` |
| Pipeline already running when the operator turns the setting off | Takes effect on the next engine start. The policy is read at engine start and held by the registry, like the egress ceiling; there is no live reload. Running connectors keep their already-built client |
| Redirect to a non-permitted host mid-stream | Request is not sent, error returned, the record is not acked and the pipeline follows its normal error policy (invariants 1 and 3 hold; this is a refused write, not a dropped one) |
| DNS answers differently between check and dial (rebinding) | The endpoint check is on the host name the operator allowlisted, which the operator trusts. For the AWS suffix match the name is checked on every request, not only at `Configure`. IP-level blocking (as in `processors.egress`) is not applied to AWS hosts or allowlisted hosts; see Open questions |
| `aws.region` crafted to alter the endpoint | Region is validated against `^[a-z0-9-]+$`; SDK endpoint resolution also rejects invalid host labels |
| Engine restart after upgrade with old pipelines that used static keys | Unchanged; the policy is not consulted |
| Pipeline created before upgrade relied on the chain | Not possible: before #1027 static keys were required |
| Engine crash/kill -9 mid-write | Not affected; no state or position change |

## Upgrade and rollback

- Additive. New config keys default off; no state, position or pipeline-config format changes.
- Upgrade order: release `conduit-commons`, bump it in `conduit` and `s3`, merge the `conduit` change,
  then un-hold and rebase `s3#1027`, then bump `s3` in `conduit`. Each step is independently safe: a new
  engine with the old `s3` (static keys only) ignores the policy; a new `s3` with an engine that does not
  supply a policy denies ambient credentials.
- Rollback: revert the `conduit` PR; the pipelines that used static keys continue to work. Pipelines that
  relied on ambient credentials stop working (fail at `Configure` with a clear error). That is the safe
  failure.
- Config rollback caveat: an older `conduit` binary reading a `conduit.yaml` that contains
  `connectors.ambient-credentials.*` may reject or ignore the unknown key. Not verified; check before
  the release notes claim either.

## Testing

- `conduit`: config parse, defaults, env mapping and validation tests (malformed endpoint is a startup
  error, `enabled=false` with allowlists warns); a builtin-adapter test that the context carries the
  policy to `Configure`, `Open`, `Run` and `Write`; a test that a connector parameter named like a policy
  key does not change the policy.
- `conduit-commons`: allowlist matching table, redirect guard table including scheme downgrade,
  userinfo in URL, trailing dot, uppercase host, IDN and IP-literal hosts; fuzz target on endpoint parsing
  and matching (the project already fuzzes parsers).
- `s3`: each refusal returns its code and config path; static keys with a custom endpoint are unaffected;
  `httptest` server that redirects to a second server proves no request reaches the second (and no
  `X-Amz-Security-Token` is sent there); ambient chain in tests isolated from developer credentials, as in
  the existing `credentials_test.go`.
- End to end in `conduit`: a pipeline using built-in `s3` without keys against an engine with the default
  config fails with `connector.ambient_credentials_disabled`; the same with the setting on and a stub
  endpoint on the allowlist starts.
- Not run in CI: a real IRSA/instance-role run. Needs manual verification before release notes claim it.
- Chaos: the policy does not touch the data path, so the SIGKILL suite is unaffected; justify that in the
  implementation PR rather than extend it.

## Open questions for DeVaris

1. **Package home.** `conduit-commons/hostpolicy` is my pick (no SDK or protocol change, both repos
   already depend on it). The alternative is `conduit-connector-sdk`, which gives connectors one import
   but couples this to an SDK release. Confirm.
2. **`allow-resources`.** I included it because a bucket in an attacker's account that trusts the host
   role is an exfiltration path that the endpoint allowlist does not cover. It adds one setting and a
   per-connector meaning. Ship in v0.21 or defer?
3. **IP guard for allowlisted endpoints.** `processors.egress` blocks loopback, link-local and private
   ranges at dial time. For connectors, an operator who allowlists `http://minio.internal:9000` has chosen
   a private address on purpose. I propose no IP guard for allowlisted hosts, and a guard that always
   blocks link-local (169.254.0.0/16, to protect IMDS from being reached through a custom endpoint) even
   when allowlisted. Confirm.
4. **Metric and log naming** to be aligned with existing conventions during implementation.
5. **Standalone follow-up** (environment variables read by the SDK) targeted at v0.22. Confirm the
   deferral, knowing the registry `s3` cannot use ambient credentials until then.

## Related

- `conduit-connector-s3#1021` (credential chain), `#1022` (endpoint warning), `#994` (custom endpoint),
  `#1027` (draft, on hold until this is signed off), `#1028`.
- `docs/design-documents/20260726-wasm-host-egress-capability.md` (the egress ceiling this mirrors).
- `docs/operations/live-restart-apply.md` (operator-only authorization flag precedent).
- `docs/catalog/v0.21-certification-gaps.md` (PR #3034), section "s3#1021 credential chain".
