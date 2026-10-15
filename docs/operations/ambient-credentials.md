# Ambient cloud credentials for built-in connectors

A built-in connector that is given no static keys can fall back to the cloud identity of the
process running Conduit. For AWS that is the default credential chain: environment variables,
shared config and SSO, web identity (IRSA), the ECS task role and the EC2 instance role.

Conduit's API is unauthenticated by default, so whoever can create a pipeline would act as the
Conduit host. That is why this is an operator setting, off by default, and why pipeline configs
cannot turn it on.

Design: [`20261012-builtin-connector-ambient-credentials.md`](../design-documents/20261012-builtin-connector-ambient-credentials.md).
Decision: [ADR](../architecture-decision-records/20261012-operator-policy-for-builtin-ambient-credentials.md).

## Settings

| Key | Env | Default | Meaning |
| --- | --- | --- | --- |
| `connectors.ambient-credentials.enabled` | `CONDUIT_CONNECTORS_AMBIENT_CREDENTIALS_ENABLED` | `false` | Built-in connectors may use the process's ambient identity when a pipeline sets no static keys |
| `connectors.ambient-credentials.allow-resources` | `CONDUIT_CONNECTORS_AMBIENT_CREDENTIALS_ALLOW_RESOURCES` | empty | Exact resource names (for `s3`, bucket names) the ambient identity may use. Required when enabled. No wildcards, ARNs or aliases |
| `connectors.ambient-credentials.allow-endpoints` | `CONDUIT_CONNECTORS_AMBIENT_CREDENTIALS_ALLOW_ENDPOINTS` | empty | The only custom endpoints that may receive ambient-credential-signed requests: exact `https://host[:port]` entries |
| `connectors.ambient-credentials.allow-proxy` | `CONDUIT_CONNECTORS_AMBIENT_CREDENTIALS_ALLOW_PROXY` | `false` | Allow ambient credentials while `HTTP_PROXY`, `HTTPS_PROXY` or `ALL_PROXY` is set |

```yaml
connectors:
  ambient-credentials:
    enabled: true
    allow-resources: ["acme-ingest-prod", "acme-ingest-staging"]
    # Only for S3-compatible stores, VPC/PrivateLink endpoints or other custom endpoints:
    allow-endpoints: ["https://minio.internal:9000"]
```

What the policy enforces when a pipeline sets no static keys:

- Off: the connector fails at configure time with `connector.ambient_credentials_disabled`, before
  it loads any credentials.
- On: the bucket must be an exact `allow-resources` entry. Access-point ARNs and aliases
  (`arn:...`, `-s3alias`, `--ol-s3`, `--op-s3`, `--x-s3`, `.mrap`) are always refused.
- The endpoint the AWS SDK resolves (from `aws.endpoint`, `AWS_ENDPOINT_URL_S3`, `AWS_ENDPOINT_URL`
  or a shared-config profile) must be https and an exact `allow-endpoints` entry. No suffix
  matching: `https://x.execute-api.us-east-1.amazonaws.com` is not "an AWS endpoint" for this
  purpose, because anyone can create one. With no custom endpoint, the SDK's own regional endpoint
  is used and is not checked against the list.
- Redirects to another host or to http are refused and not retried.
- No connection is made to link-local or metadata addresses (`169.254.0.0/16`, `fe80::/10`,
  `fd00:ec2::254`, `fd00:ec2::23`, and their IPv4-mapped forms), even for an allowlisted host,
  except under `allow-proxy` (the dial check then sees only the proxy).
- Exactly one static key set is `connector.credentials_incomplete`. It never falls back to the
  host identity.

Static keys in a pipeline work as before, with any endpoint. The policy is not consulted.

The policy reaches only built-in connectors. A pipeline must say `builtin:s3`: a bare `s3`
resolves to an installed standalone copy first, and standalone plugins never receive the policy,
so they always refuse ambient credentials.

IAM least privilege on the host role is the real boundary. `allow-resources` limits which buckets
a pipeline can name; it does not replace a role policy scoped to those buckets.

Changes take effect on the next engine start. There is no live reload; running connectors keep
the client they built.

## Seeing what is on

- `conduit doctor --check connectors.ambient_credentials` (add `--json` for machines) shows the
  effective policy and fails on any combination `conduit run` refuses.
- At startup the engine logs `ambient credentials policy` with `enabled`, `allow_endpoints_count`,
  `allow_resources_count` and `allow_proxy`.
- Each connector that uses ambient credentials logs `ambient credentials in use` (component
  `connector.AmbientCredentials`, fields `connector_id`, `plugin`, `region`, `endpoint_host`).
  This is the audit line. It never contains keys, session tokens or role ARNs.
- Each refusal logs `ambient credentials denied` at WARN with `decision=deny`, `reason` (the
  error code), `config_path`, `plugin` and `connector_id`.
- Metric `conduit_connector_ambient_credentials_total{plugin, outcome}`, where outcome is `used`,
  `denied_disabled`, `denied_endpoint`, `denied_redirect`, `denied_link_local`,
  `denied_resource` or `denied_proxy`.

## Runbook

### Pipeline fails with `connector.ambient_credentials_disabled`

- Symptom: a pipeline with no `aws.accessKeyId`/`aws.secretAccessKey` fails at create or start.
- Diagnosis: `conduit doctor --check connectors.ambient_credentials`. Either the setting is off,
  or the pipeline uses a standalone `s3` (check the plugin name: it must be `builtin:s3`).
- Fix: set static keys in the pipeline, or enable the setting with the buckets listed, and use
  `builtin:s3`.

### Startup fails on `connectors.ambient-credentials.allow-resources`

- Symptom: `conduit run` exits with `common.invalid_argument` or `config.ambient_resource_wildcard`
  naming `connectors.ambient-credentials.allow-resources`.
- Diagnosis: the list is empty while `enabled=true`, or an entry contains `*`, an ARN or an alias.
- Fix: list exact bucket names. There is no "any bucket" setting.

### Startup fails with `config.ambient_proxy_env`

- Symptom: `conduit run` exits naming one or more proxy variables.
- Diagnosis: a proxy is configured in the engine's environment while ambient credentials are on.
  Through a proxy, Conduit cannot check where a connection really goes.
- Fix: unset the proxy variables for the Conduit process, or set `allow-proxy: true` if the proxy
  is trusted to reach only intended destinations. Under `allow-proxy`, the metadata address
  refusal does not apply.

### Pipeline fails with `connector.endpoint_not_allowed`

- Symptom: the error names `aws.endpoint`, or "SDK-configured endpoint" when the endpoint came from
  `AWS_ENDPOINT_URL*` or the shared config.
- Fix: add the exact `https://host[:port]` to `allow-endpoints`, or use static keys for that
  endpoint. An `http://` endpoint is never accepted with ambient credentials.

### Pipeline fails with `connector.resource_not_allowed`

- Fix: use a bucket from `allow-resources` or ask the operator to add it. Access points, aliases
  and directory buckets are not supported with ambient credentials.

### Pipeline fails with `connector.redirect_refused` or `connector.endpoint_link_local`

- Symptom: requests to an allowed endpoint were redirected to another host, or the endpoint
  resolves to a link-local or metadata address.
- Diagnosis: treat as a misbehaving or hostile endpoint. Check DNS for the endpoint host.
- Fix: none on the pipeline side; correct the endpoint or its DNS. The record that hit the refusal
  was not acknowledged and follows the pipeline's normal error policy.

### `connector.proxy_not_allowed` at pipeline configure time

- The connector found a proxy variable that the engine's startup check did not see (the
  connector runs with a different environment). Same fix as `config.ambient_proxy_env`.

## Out of scope

- `AWS_ENDPOINT_URL_STS`, `AWS_ENDPOINT_URL_SSO`, `AWS_EC2_METADATA_SERVICE_ENDPOINT` and the
  matching profile keys redirect where credentials are fetched. They are not checked. Treat them
  as sensitive engine configuration.
- Standalone plugins: see issue [#3043](https://github.com/ConduitIO/conduit/issues/3043).
