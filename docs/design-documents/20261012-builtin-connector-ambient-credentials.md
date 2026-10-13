# Operator control over ambient credentials in built-in connectors

## Summary

A built-in connector that falls back to the process's ambient cloud identity (AWS default credential
chain: environment, `~/.aws`, web identity, ECS/EC2 role) lets any pipeline author act as the Conduit
host. Conduit's API is unauthenticated by default, so "pipeline author" means "anyone who can reach the
API". This design adds an operator-owned, engine-level policy that built-in connectors can read but
pipeline configs cannot change: ambient credentials are off by default; when on, custom endpoints must be
on an operator allowlist, over https only, and the AWS endpoint is never taken from the pipeline; and
cross-host redirects are never followed. The
engine hands the policy to built-in connectors through the call context, using a small shared package in
`conduit-commons`. No `conduit-connector-sdk` change and no `conduit-connector-protocol` change is
needed. Standalone and external connectors are out of scope for v0.21: the policy does not reach them
and any protection there is cooperative only.

This unblocks `conduit-connector-s3#1027` (default credential chain, issue #1021), which stays a draft on
hold until this is signed off.

Risk tier: Tier 1 (security boundary; touches how connectors authenticate to external systems). It does
not change record flow, acks, positions or serialization.

## Context

### What happened

`s3#1027` made `aws.accessKeyId` and `aws.secretAccessKey` optional and fell back to the AWS default
chain. That fallback has two problems:

1. **Confused deputy.** A pipeline author inherits the host's identity. With the API unauthenticated by
   default, anyone who can create a pipeline can read and write every bucket the host role can reach, and
   can point `bucket` at a bucket they own that grants the host's account write access, which is an
   exfiltration path that needs no custom endpoint.
2. **Exfiltration and SSRF.** `aws.endpoint` (s3#994) is user supplied. With ambient credentials the
   connector sends SigV4-signed requests, made with host credentials, to whatever host the author names.
   Go's HTTP client strips `Authorization` on a cross-host redirect but copies other headers, and SigV4
   also sends `X-Amz-Security-Token`; a redirect from an allowed host therefore leaks a session token
   unless the client refuses it.

What "the API is open" means in the engine today, read at `81c6306`: the HTTP API has no authentication
(`pkg/conduit/config.go:90`), and the default bind is `:8080`, all interfaces (`config.go:357`). The engine
does not refuse a non-loopback bind. The only startup refusal tied to `isLoopbackBind` is the CORS wildcard
`*` combined with a non-loopback bind (`config.go:454`), and a warning for the same combination
(`runtime.go:1209-1217`). That protects against a web page reaching the API; it does not stop a direct
network client. So on a default install, anyone who can reach port 8080 can create a pipeline. This design
does not change that; it limits what such a pipeline can do with the host's identity.

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
- Standalone plugins are out of scope for this design; plugin environment handling is covered by
  ConduitIO/conduit#3043. Standalone plugins run as the same OS user as the engine, so the policy below
  is cooperative only for them. This design does not depend on #3043.
- A pipeline that names a plain `s3` (no prefix) resolves standalone first: `PluginTypeAny` tries
  `standaloneReg.NewDispenser` and falls back to the built-in only on error
  (`pkg/plugin/connector/service.go:185-191`, `newDispenser`). `builtin:s3` selects the built-in
  (`service.go:183`).
- A third kind exists in code: `pkg/plugin/connector/external` dispenses a connector already running at a
  known address (go-plugin `Reattach`). At `81c6306` nothing outside its own tests imports it, so no
  pipeline can reach it yet; its `doc.go` limits it to loopback (Mode 1) for now.

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
- When on, the connector accepts a custom endpoint only if the operator listed that exact https host,
  refuses cross-host redirects, and never connects to link-local addresses.
- The mechanism is generic (any built-in connector that wants ambient identity uses it), and needs no
  per-connector code in the engine.
- Refusals are stable, machine-actionable errors; use is observable.

Non-goals:
- Authenticating the Conduit API (separate work).
- Governing standalone plugins (out of scope; see Decision 6).
- A policy language, per-pipeline grants, or multi-tenancy. This is a single operator ceiling.
- Changing how static keys work. Static keys in a pipeline config keep working exactly as today, with any
  endpoint, as they only expose what the author already holds. In particular, static keys plus a custom
  internal endpoint (an author pointing signed requests at an internal host) is out of scope here: it is
  a plain SSRF concern that the engine's connector layer does not address today, and this design does not
  claim to.

## Decision

### 1. Engine configuration

New keys under `connectors.ambient-credentials`, next to `connectors.path`:

| Key (flag / yaml) | Env | Default | Meaning |
| --- | --- | --- | --- |
| `connectors.ambient-credentials.enabled` | `CONDUIT_CONNECTORS_AMBIENT_CREDENTIALS_ENABLED` | `false` | Built-in connectors may use the process's ambient cloud identity |
| `connectors.ambient-credentials.allow-endpoints` | `CONDUIT_CONNECTORS_AMBIENT_CREDENTIALS_ALLOW_ENDPOINTS` | empty | The only custom endpoints that may receive ambient-credential-signed requests. `https://host[:port]` entries; exact host match, no suffix or wildcard matching. Parsed with `egress.ParseAllowEntry` (`pkg/plugin/processor/egress/policy.go:132`), with the additional rule that the scheme must be `https` |
| `connectors.ambient-credentials.allow-resources` | `CONDUIT_CONNECTORS_AMBIENT_CREDENTIALS_ALLOW_RESOURCES` | empty | Connector-defined resource names the ambient identity may touch; for `s3`, bucket names, exact match only. A `*` anywhere in an entry is a startup error (see below). Required non-empty when `enabled=true`, unless `allow-any-resource` is set |
| `connectors.ambient-credentials.allow-any-resource` | `CONDUIT_CONNECTORS_AMBIENT_CREDENTIALS_ALLOW_ANY_RESOURCE` | `false` | Explicit opt-out of the resource allowlist: any resource the host identity can reach is allowed. Logged at WARN at startup |
| `connectors.ambient-credentials.allow-proxy` | `CONDUIT_CONNECTORS_AMBIENT_CREDENTIALS_ALLOW_PROXY` | `false` | Explicit opt-in to use ambient credentials while an HTTP proxy is configured in the process environment (`HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`, either case). Logged at WARN at startup |

```yaml
connectors:
  ambient-credentials:
    enabled: true
    allow-resources: ["acme-ingest-prod", "acme-ingest-staging"]
```

Bucket names are globally registrable, so a pattern such as `acme-ingest-*` is unsafe: anyone can create
`acme-ingest-evil` in their own account, and a role that can write to it would then send data there. For
that reason `allow-resources` takes exact names and a wildcard is refused, not warned about: an entry
containing `*` is a startup error with the stable code `config.ambient_resource_wildcard`. Refusing is
cheaper than a warning nobody reads, and an operator with many buckets lists them or uses
`allow-any-resource` knowingly.

An operator who really wants "any bucket the role can reach" must say so (`allow-any-resource: true`); an
empty list never means "any".

Behavior:
- Validated in `Config.Validate` the way `egressCeiling` is: a malformed or non-https endpoint is a
  startup error; `enabled=true` with an empty `allow-resources` and `allow-any-resource=false` is a
  startup error (the zero value of a safety knob must not mean "everything"); a wildcard in
  `allow-resources` is a startup error (`config.ambient_resource_wildcard`); `enabled=true` with a proxy
  variable set and `allow-proxy=false` is a startup error (`config.ambient_proxy_env`); `allow-endpoints`,
  `allow-resources`, `allow-any-resource` or `allow-proxy` set while `enabled=false` is a startup warning, not an error
  (inert but harmless; failing would make a config that is off by default awkward to stage).
- **Endpoint rule: no suffix matching.** Under ambient credentials a pipeline-supplied `aws.endpoint` is
  accepted only if it exactly matches an `allow-endpoints` entry. A suffix rule such as `.amazonaws.com`
  is rejected as a design: tenants can create hosts under it that they control (API Gateway
  `*.execute-api.<region>.amazonaws.com`, ELB and EC2 public DNS names), so a suffix match would let an
  author receive SigV4-signed requests, which is the exfiltration this policy exists to stop. When
  `aws.endpoint` is unset, the connector uses only the AWS SDK's own endpoint resolution (from the
  validated `aws.region`), never a host derived from pipeline input. S3-compatible stores and non-standard
  partitions are reached by listing their exact endpoint in `allow-endpoints`. The AWS SDK also reads
  `AWS_ENDPOINT_URL` and the service-specific `AWS_ENDPOINT_URL_S3` from the process environment
  (`aws-sdk-go-v2/config@v1.33.5/env_config.go:76,417` and `:706`), and the S3 connector passes through
  to it when `aws.endpoint` is empty (`config/config.go` at `conduit-connector-s3@e8f04f2`). Decision: those
  values are checked against `allow-endpoints` exactly like `aws.endpoint`, not ignored. Reason: they are
  operator-controlled, so the check is not about a hostile author; it stops a stray variable from
  silently sending signed requests to an unlisted host, and the SDK offers no switch the connector can
  flip to ignore them from inside the process. The connector reads both variables itself, applies
  `CheckEndpoint` to the effective endpoint (`aws.endpoint`, else `AWS_ENDPOINT_URL_S3`, else
  `AWS_ENDPOINT_URL`) and fails with `connector.endpoint_not_allowed` naming the variable. Whether a
  profile-level `endpoint_url` in `~/.aws/config` is also honored is not verified; the implementation must
  test it and apply the same check if so.
- **https only.** Under ambient credentials every endpoint (allowlisted or SDK-resolved) must use
  `https`. An `http://` allowlist entry is a startup error, and `http://` redirect targets are refused.
- **Link-local is always refused.** Under ambient credentials no connection is made to
  `169.254.0.0/16` or `fd00:ec2::254`, for any endpoint, allowlisted or not, checked on the resolved
  address at dial time (see Decision 3). Reason: a custom endpoint must not become a path to IMDS.
- **Proxy variables are refused unless opted in.** The dial control sees only the address the client
  dials. The AWS SDK's default transport uses `http.ProxyFromEnvironment`
  (`aws-sdk-go-v2@v1.47.0/aws/transport/http/client.go:244`), so with `HTTPS_PROXY` set the client dials
  the proxy and the target host is named in a CONNECT request the guard never inspects; the link-local
  refusal and any DNS-based check would not apply to the real destination. Decision: with
  `enabled=true`, a non-empty `HTTP_PROXY`, `HTTPS_PROXY` or `ALL_PROXY` (upper or lower case) in the
  engine's environment is a startup error (`config.ambient_proxy_env`) unless `allow-proxy: true` is
  set. The connector also refuses at `Configure` with `connector.proxy_not_allowed` when
  `Policy.AllowProxy` is false and such a variable is set, so a connector run under a different
  environment than the engine's startup check still fails closed. With `allow-proxy: true` the operator
  accepts that the proxy is trusted to reach only intended destinations; the allowlist then constrains
  the requested host, not the network path. Redirect refusal still applies. Static keys are unaffected.
- The setting applies only to the ambient chain. If a pipeline supplies both static keys, the connector
  uses them and the policy is not consulted.

### 2. Passing the policy to connectors

A new package `github.com/conduitio/conduit-commons/hostpolicy` (name open, see Open questions):

```go
// Policy is the operator's decision about ambient credentials. The zero value denies.
type Policy struct {
    AmbientCredentials bool
    AllowEndpoints     []Endpoint // parsed https host/port entries, exact match
    AllowResources     []string
    AllowAnyResource   bool
    AllowProxy         bool
    Observe            func(ctx context.Context, d Decision) // engine metric/log hook; may be nil
}

func WithPolicy(ctx context.Context, p Policy) context.Context
func FromContext(ctx context.Context) Policy // zero (deny) when absent
func (p Policy) CheckEndpoint(u *url.URL) error  // https + exact allowlist match
func (p Policy) CheckResource(name string) error // allowlist / allow-any-resource; rejects ARN and alias forms
func (p Policy) CheckProxy(env func(string) string) error // refuses when a proxy variable is set and !AllowProxy
func RedirectGuard() func(*http.Request, []*http.Request) error // refuses any cross-host or non-https redirect
func DialControl() func(network, address string, c syscall.RawConn) error // refuses link-local at dial time
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

The error taxonomy, the redirect guard and the dial control live in the same package so every connector
refuses the same way. Provider-specific parts (the SDK `config.LoadDefaultConfig` call, building the
`http.Client`) stay in the S3 connector. There is no AWS host list anywhere.

### 3. Connector behavior (what `s3#1027` becomes)

When neither static key is set:

1. `policy.AmbientCredentials` false: fail at `Configure` with `connector.ambient_credentials_disabled`.
   No AWS call is made and the default chain is not loaded.
2. True: `aws.region` is validated against `^[a-z0-9-]+$`, `bucket` (`aws.bucket`) is checked with
   `CheckResource`, `CheckProxy` runs, and the client is built with `LoadDefaultConfig`.
   `CheckResource` accepts only a plain bucket name. S3 also accepts access-point ARNs
   (`arn:aws:s3:<region>:<account>:accesspoint/<name>`) and access-point or multi-region aliases
   (`...-s3alias`, `...--ol-s3`) in the bucket parameter; these resolve to a resource, often in another
   account, that a bucket-name allowlist cannot reason about. They are refused with
   `connector.resource_not_allowed` whatever the allowlist says, including under `allow-any-resource`
   (an ARN or alias entry in `allow-resources` is itself a startup error). If an operator needs access
   points, that is a separate, explicit feature with its own allowlist semantics. Static keys are
   unaffected. Whether S3 would otherwise resolve the alias forms the connector sees is not verified;
   the refusal is a syntactic check and does not depend on it.
3. If `aws.endpoint` is set it must pass `CheckEndpoint`: https and an exact `allow-endpoints` match;
   otherwise `connector.endpoint_not_allowed`. If it is unset, the effective endpoint from
   `AWS_ENDPOINT_URL_S3` / `AWS_ENDPOINT_URL` gets the same check (Decision 1); with neither set only the
   SDK's own resolution is used.
   Checked in `Configure`, so a bad pipeline fails when created or validated, not on the first record.
   Test case that must be in the suite: `https://x.execute-api.us-east-1.amazonaws.com` is refused.
4. The S3 service client (not the shared `aws.Config`) gets an `http.Client` with
   `CheckRedirect = hostpolicy.RedirectGuard()` and a `net.Dialer` with `Control = hostpolicy.DialControl()`.
   The guard refuses every redirect whose host differs from the original request's host and every
   non-https redirect, returning `connector.redirect_refused`; the dial control refuses link-local
   addresses on the resolved IP, which also covers a DNS name that resolves to `169.254.169.254`.
   Why the service client only: `config.LoadDefaultConfig` builds the credential providers from
   `cfg.HTTPClient`; for example `imds.NewFromConfig` copies `cfg.HTTPClient`
   (`aws-sdk-go-v2/feature/ec2/imds@v1.20.3/api_client.go:112`), and the instance-role provider uses that
   IMDS client (`config@v1.33.5/resolve_credentials.go:421-423`). A link-local guard on `cfg.HTTPClient`
   would block the credential fetch itself. The same applies to STS for web identity. Whether the S3
   client needs any cross-host redirect in normal operation (for example a region redirect) is not
   verified; the design assumes not and the S3 tests must show it.
5. A refused redirect is terminal. The error must not be retried on the same client: the SDK retryer wraps
   the HTTP client, so the implementation marks the error non-retryable, and a test with retries
   configured to the maximum asserts exactly one request reached the first server. The exact SDK
   mechanism for marking it is not verified here.
6. Error text from the AWS SDK that reaches a pipeline author (pipeline status, `Configure` errors) is
   scrubbed: role ARNs (`arn:aws...:<12-digit account>:...`) and account IDs are replaced with a
   placeholder. The 12-digit match is scoped to ARN and account-ID contexts (inside an `arn:` string, or
   directly after an `account`/`account id` label), not any 12-digit number, so request IDs and byte
   counts in error text are left alone. The unscrubbed text goes only to the engine log, which the operator owns. Applies
   to credential-resolution failures (STS `AssumeRoleWithWebIdentity`, IMDS) and to S3 `AccessDenied`
   messages, which can embed the assumed-role ARN.

The redirect guard and the dial control run on every request, not only at `Configure`, so a
server-chosen redirect or a DNS change cannot widen the set.

Note on Go's own behavior: `net/http` removes `Authorization`, `Www-Authenticate`, `Cookie` and `Cookie2`
on a redirect to a host that is not the same domain or a subdomain (`shouldCopyHeaderOnRedirect`, Go
1.25.5 `net/http/client.go:992`) and copies all other headers, including `X-Amz-Security-Token`. It also
keeps `Authorization` on a redirect to a subdomain. The guard above is stricter than both.

### 4. Errors

Stable codes, each with the failing config path and a suggested fix. They are registered in
`conduiterr` (`connector.*`, gRPC `PermissionDenied`) by the engine; the connector returns a typed error
from the shared package carrying the code string, `ConfigPath` and `Suggestion`, and the builtin adapter
wraps it into a `ConduitError` at the plugin boundary (the connector cannot import `conduiterr`, since
the engine imports the connector).

| Code | Config path | Message | Suggestion |
| --- | --- | --- | --- |
| `connector.ambient_credentials_disabled` | `aws.accessKeyId` | ambient credentials are not enabled on this Conduit instance | set `aws.accessKeyId` and `aws.secretAccessKey`, or ask the operator to set `connectors.ambient-credentials.enabled=true` |
| `connector.endpoint_not_allowed` | `aws.endpoint` | endpoint is not https, or is not on the ambient-credentials allowlist | use static credentials with this endpoint, or add it to `connectors.ambient-credentials.allow-endpoints` |
| `connector.redirect_refused` | `aws.endpoint` | refused redirect to a different host or a non-https URL | none for the pipeline author; report the endpoint to the operator |
| `connector.endpoint_link_local` | `aws.endpoint` | endpoint resolves to a link-local address, which is never permitted with ambient credentials | use a different endpoint |
| `connector.resource_not_allowed` | `aws.bucket` | resource is not on the operator's allowlist, or is an access-point ARN or alias | use an allowed plain bucket name or ask the operator to extend `connectors.ambient-credentials.allow-resources` |
| `connector.proxy_not_allowed` | `aws.endpoint` | a proxy is configured in the process environment and the operator has not opted in | ask the operator to unset the proxy variables or set `connectors.ambient-credentials.allow-proxy=true` |
| `config.ambient_resource_wildcard` | `connectors.ambient-credentials.allow-resources` | wildcard entries are not allowed; bucket names are globally registrable | list exact bucket names (startup error, not a pipeline error) |
| `config.ambient_proxy_env` | `connectors.ambient-credentials.allow-proxy` | a proxy variable is set while ambient credentials are enabled | unset the proxy variables or set `allow-proxy=true` (startup error) |

Messages never include credential values, and the engine's existing log redaction applies. Role ARNs and
account IDs in AWS error text are scrubbed before they reach a pipeline author (Decision 3, item 6); this
is a connector-side responsibility and needs its own test.

### 5. Observability

- INFO log once per connector `Open`: `ambient credentials in use` with connector ID, plugin name,
  resolved region, and the endpoint host (never keys, never the role ARN session token).
- WARN log per refusal with the code and config path.
- Counter `conduit_connector_ambient_credentials_total{plugin, outcome}` where outcome is `used`,
  `denied_disabled`, `denied_endpoint`, `denied_redirect`, `denied_link_local`, `denied_resource`, `denied_proxy`, driven through
  `Policy.Observe`. (Metric name to be checked against the existing `conduit_*` naming when
  implemented.)
- `conduit run` startup logs the effective policy (enabled, number of allowlisted endpoints and
  resources, `allow-any-resource`, `allow-proxy`) so an operator can see what is on.
- `conduit doctor` shows the same effective policy as a check, from the `conduit.Config` it already
  carries (`cmd/conduit/root/doctor/doctor.go:66,81`), and fails the check on the startup-error
  combinations above, so a bad config is found before `conduit run`. Allowlisted endpoints and resource
  patterns are shown; there are no secrets in them.
- Runbook entry under `docs/operations/` (symptom: pipeline fails with one of the codes; diagnosis; fix).

### 6. Standalone and external connectors

Out of scope for enforcement in v0.21. The policy does not reach them: a standalone process gets no
context value, so a cooperating connector sees the zero (deny) policy and refuses ambient credentials.
That is cooperative only; standalone plugins run as the same OS user as the engine, and this design does
not claim to stop one from using the host identity. Plugin environment handling is covered by
ConduitIO/conduit#3043.

Consequences for operators and pipeline authors:
- A bare `s3` resolves to an installed registry copy first (`service.go:185-191`), which gets no policy.
  A pipeline that wants the built-in `s3` and its ambient-credential policy must say `builtin:s3`. The
  runbook and the connector docs state this, and the `connector.ambient_credentials_disabled` suggestion
  mentions it. With a registry `s3` installed, a bare `s3` in a pipeline silently selects it.
- The registry copy of `s3` cannot use the ambient chain until a follow-up lands.
- External connectors (`pkg/plugin/connector/external`, Reattach-based) are not wired into the registry
  or `CreatePipeline` at `81c6306`, so there is nothing to enforce yet. The process is out of band and
  gets no context value, the same as standalone, so the policy would not apply. Whoever wires that
  package in must either carry the policy to it or document it as outside the policy. This design flags
  it and does not decide it.

Standalone plugins are out of scope for this design; plugin environment handling is covered by
ConduitIO/conduit#3043. Any later way to deliver the policy to standalone plugins needs its own design.

### 7. What this does and does not change in the SDK and protocol

| Component | Change | Notes |
| --- | --- | --- |
| `conduit` | Yes | config keys, validation, registry holds the policy, adapters add it to the context, error codes, metric, `conduit doctor` check, docs, ADR |
| `conduit-commons` | Yes, additive | new `hostpolicy` package; needs a release and a `go.mod` bump in `conduit` and `s3` |
| `conduit-connector-s3` | Yes | s3#1027 rebased onto the policy; the code moved into `config.LoadAWSConfig` stays |
| `conduit-connector-sdk` | No | context values already reach connector methods |
| `conduit-connector-protocol` | No | `PluginConfig` untouched; no versioning discussion needed |

## Alternatives considered

**A. The connector reads an operator environment variable** (for example
`CONDUIT_S3_ALLOW_AMBIENT_CREDENTIALS`). Pipeline authors cannot set process environment, so it holds
the main property, and it works with no engine change. Rejected as the design because it makes every
connector invent its own variable, name and parsing, with no common allowlist or redirect handling. It
is also invisible to `conduit run` (no startup validation, no policy log, no typo detection).


**B. Only STS assume-role with an explicit role ARN, never the whole chain.** Narrows the blast radius
because the pipeline names a role. Rejected: the call to STS is still made with the host identity, any
pipeline author can name any role the host may assume, and trust policies on the target role are not
written with "Conduit pipeline authors" in mind. It does not solve the confused deputy; it renames it.
It is still a good optional feature later, once this policy exists.

**C. Ambient credentials only for registry or standalone plugins.** Rejected: the standalone process
runs as the engine's OS user (see Context), so this does not remove the confused deputy, and it
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
| `enabled=true`, but the host has no credentials | The SDK resolves lazily; first call fails with the SDK error (scrubbed for authors), nothing is acked (invariant 1). Same as the original #1027 behavior |
| `enabled=true` with no `allow-resources` and no `allow-any-resource` | Startup error from `Config.Validate` |
| Author sets `aws.endpoint` to a tenant-controlled AWS-suffix host (`*.execute-api...`, ELB, EC2 DNS) | `connector.endpoint_not_allowed`; no suffix match exists |
| Allowlisted or custom hostname resolves to `169.254.169.254` / `fd00:ec2::254` | Dial refused (`connector.endpoint_link_local`) |
| Endpoint allowlist typo | Startup error for a malformed entry; a well-formed wrong entry causes `connector.endpoint_not_allowed` at pipeline `Configure` |
| Pipeline already running when the operator turns the setting off | Takes effect on the next engine start. The policy is read at engine start and held by the registry, like the egress ceiling; there is no live reload. Running connectors keep their already-built client |
| Proxy variable set with ambient credentials enabled | Startup error unless `allow-proxy: true`; connector also refuses at `Configure` |
| `allow-resources` contains a wildcard, or an ARN or alias | Startup error |
| `aws.bucket` is an access-point ARN or alias | `connector.resource_not_allowed`, even under `allow-any-resource` |
| `AWS_ENDPOINT_URL_S3` / `AWS_ENDPOINT_URL` names an unlisted host | `connector.endpoint_not_allowed` |
| Redirect to a non-permitted host mid-stream | Request is not sent, error returned, the record is not acked and the pipeline follows its normal error policy (invariants 1 and 3 hold; this is a refused write, not a dropped one) |
| DNS answers differently between check and dial (rebinding) | The allowlist is on the host name the operator listed, which the operator trusts. The link-local refusal is enforced on the dialed address (dial control), so rebinding to link-local fails. Other private ranges are not blocked for allowlisted hosts (an operator may list `https://minio.internal`); see Open questions |
| `aws.region` crafted to alter the endpoint | Region is validated against `^[a-z0-9-]+$`; SDK endpoint resolution also rejects invalid host labels |
| Engine restart after upgrade with old pipelines that used static keys | Unchanged; the policy is not consulted |
| Pipeline created before upgrade relied on the chain | Not possible: `aws.accessKeyId` and `aws.secretAccessKey` are `validate:"required"` in `conduit-connector-s3` `config/config.go:59-61` at `e8f04f2` (the code `conduit` v0.9.4 pins is the same source; the tag itself was not diffed) |
| Engine crash/kill -9 mid-write | Not affected; no state or position change |

## Upgrade and rollback

- Additive. New config keys default off; no state, position or pipeline-config format changes.
- Upgrade order: release `conduit-commons`, bump it in `conduit` and `s3`, merge the `conduit` change,
  then un-hold and rebase `s3#1027`, then bump `s3` in `conduit`. The intent is that each step is
  independently safe: a new engine with the old `s3` (static keys only) ignores the policy; a new `s3`
  with an engine that does not supply a policy denies ambient credentials. Both are consequences of the
  design, not yet of any code; the implementation PRs must carry tests for them.
- Rollback and downgrade behavior (what an older `conduit` does with a `conduit.yaml` containing
  `connectors.ambient-credentials.*`, and what happens to pipelines when the engine is reverted) is not
  verified. It is not claimed here. The implementation PR runs an N to N-1 config test and the release
  notes state the result.

## Testing

- `conduit`: config parse, defaults, env mapping and validation tests (malformed endpoint is a startup
  error, `enabled=false` with allowlists warns); a builtin-adapter test that the context carries the
  policy to `Configure`, `Open`, `Run` and `Write`; a test that a connector parameter named like a policy
  key does not change the policy.
- `conduit-commons`: allowlist matching table, redirect guard table including scheme downgrade,
  userinfo in URL, trailing dot, uppercase host, IDN and IP-literal hosts; fuzz target on endpoint parsing
  and matching (the project already fuzzes parsers).
- `s3`: each refusal returns its code and config path; static keys with a custom endpoint are unaffected;
  the redirect test above; ambient chain in tests isolated from developer credentials, as in
  the existing `credentials_test.go`.
- Security regressions that must exist before any ambient-credential code merges:
  - `https://x.execute-api.us-east-1.amazonaws.com` (and an ELB and an EC2 public DNS name) as
    `aws.endpoint` is refused with `connector.endpoint_not_allowed` under ambient credentials, and is
    accepted only if that exact host is in `allow-endpoints`.
  - `http://` endpoint and `http://` allowlist entry are refused.
  - `169.254.169.254`, `[fd00:ec2::254]`, and a hostname resolving to them are refused, including when
    the host is allowlisted; and the instance-role credential fetch still works (guard not on
    `cfg.HTTPClient`).
  - Redirect header leak, through the real wrapper: a test builds the connector with
    `sdk.NewSourcePlugin` / `sdk.NewDestinationPlugin` (the same wrapper the engine uses), points it at
    an `httptest` server that answers 307 to a second server, and asserts the second server receives
    zero requests. A control run with the guard disabled asserts the second server does receive
    `X-Amz-Security-Token`, so the test proves it can detect the leak. Retries are set to the maximum
    and the assertion is that the first server sees one request (a refused redirect is not retried).
  - Scrubbing: a fake error containing a role ARN and an account ID reaches the author with both
    replaced and still reaches the log in full; a 12-digit number outside those contexts (for example a
    request ID) is left intact.
  - Wildcard in `allow-resources` fails startup; access-point ARN and alias forms in `aws.bucket` are
    refused, including with `allow-any-resource=true`.
  - Proxy: `HTTPS_PROXY` set with `enabled=true` fails startup unless `allow-proxy`; the connector refuses
    at `Configure`; no request reaches a stub proxy in the refused case.
  - `AWS_ENDPOINT_URL_S3` and `AWS_ENDPOINT_URL` pointing at an unlisted host are refused.
  - `enabled=true` with empty `allow-resources` fails startup; `allow-any-resource=true` is accepted and
    warns.
- Policy cannot be flipped from a pipeline or from tooling:
  - MCP `validate` and `dry_run` with a config that includes `connectors.ambient-credentials.*`-style
    keys, or connector settings named like policy keys, do not change the effective policy and do not
    load the AWS default chain. At `81c6306` only `pkg/conduit/runtime.go` builds the built-in connector
    registry, and the MCP tools are offline engines; the test asserts this rather than relying on that
    reading.
  - Provisioning (pipeline YAML import and `conduit pipelines apply`) with such keys in connector
    settings is passed to the connector as ordinary settings and never reaches the policy.
- End to end in `conduit`: a pipeline using built-in `s3` without keys against an engine with the default
  config fails with `connector.ambient_credentials_disabled`; the same with the setting on, a resource
  allowlist and a stub https endpoint on the allowlist starts; `builtin:s3` versus bare `s3` with a
  standalone `s3` installed picks the expected implementation.
- Not run in CI: a real IRSA/instance-role run. Needs manual verification before release notes claim it.
- Chaos: the policy does not touch the data path, so the SIGKILL suite is unaffected; justify that in the
  implementation PR rather than extend it.

## Open questions for DeVaris

1. **Package home.** `conduit-commons/hostpolicy` is the proposal (no SDK or protocol change, both repos
   already depend on it). The alternative is `conduit-connector-sdk`, which gives connectors one import
   but couples this to an SDK release. Confirm.
2. **`allow-resources` is mandatory.** `enabled=true` requires a non-empty `allow-resources`
   or an explicit `allow-any-resource: true`, because a bucket in an attacker's account that trusts the
   host role is an exfiltration path the endpoint allowlist does not cover. Exact names only,
   no wildcards. Confirm the extra setting is acceptable and that startup should fail rather than warn.
3. **IP guard.** Proposed: refuse `169.254.0.0/16` and `fd00:ec2::254` for every endpoint
   under ambient credentials, allowlisted or not. `processors.egress` also blocks loopback and private
   ranges; for connectors an operator who allowlists `https://minio.internal:9000` has chosen a private
   address on purpose, so those are not blocked. Confirm. (Static keys plus a custom internal endpoint
   remain out of scope, stated above.)
4. **Metric and log naming** to be aligned with existing conventions during implementation.
5. **Proxy opt-in.** Ambient credentials are refused when a proxy variable is set, unless
   `allow-proxy: true`. Confirm a startup error is the right strictness.

## Related

- `conduit-connector-s3#1021` (credential chain), `#1022` (endpoint warning), `#994` (custom endpoint),
  `#1027` (draft, on hold until this is signed off), `#1028`.
- `docs/architecture-decision-records/20261012-operator-policy-for-builtin-ambient-credentials.md` (the decision recorded).
- ConduitIO/conduit#3043 (standalone plugin environment; out of scope here).
- `docs/design-documents/20260726-wasm-host-egress-capability.md` (the egress ceiling this mirrors).
- `docs/operations/live-restart-apply.md` (operator-only authorization flag precedent).
- `docs/catalog/v0.21-certification-gaps.md` (PR #3034), section "s3#1021 credential chain".
