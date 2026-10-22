# Connector utilities server authentication

Status: implemented in the GHSA-v79j-j88p-m4ff fix. Tier 1 (touches the plugin protocol surface, so the
versioning note below is required). Needs DeVaris sign-off.

## Problem

Conduit runs a gRPC "connector utilities" server (`startConnectorUtils`, `pkg/conduit/runtime.go`) that
standalone connector plugins dial for the schema service. The audit found it bound to all interfaces,
unauthenticated, with reflection (H2). The bind is now `127.0.0.1:0`. The question here is the residual:
can any other local process on the host use it?

## What already exists (found before designing anything new)

The protocol already has per-connector authentication. This is not new in this change:

- `pconnutils.EnvConduitConnectorUtilitiesGRPCTarget` and `pconnutils.EnvConduitConnectorToken` are set in the
  plugin's environment by the standalone registry (`pkg/plugin/connector/standalone/registry.go`).
- The SDK reads both, and sends the token on every call in gRPC metadata `conduit-connector-token`
  (`conduit-connector-protocol/pconnutils/internal/metadata.go`, since protocol v0.11).
- `connutils.AuthManager` issues one token per connector instance (`PluginService.NewDispenser`) and
  deregisters it when the connector is torn down. `SchemaService.CreateSchema` and `GetSchema` reject a call
  whose token is not registered.
- Built-in connectors run in-process and call the `SchemaService` object directly with the token in the
  context. They never use the network path.

So an unauthenticated local client already cannot create or read a schema. What was still open:

1. The check is per handler. Anything else on the server is unauthenticated: gRPC reflection (it lists the
   services and messages), the health service, and any RPC added later that forgets to call `IsTokenValid`.
2. The token is a UUIDv4 inside a JSON wrapper: 122 bits, not the 256 asked for, looked up in a plain map
   (lookup timing depends on the presented value) and **echoed back in the error message** (`%q`), which the
   gRPC server also logs.

## Decision

No new secret, no new config, no protocol or SDK change. Enforce the existing per-connector token for the whole
server and harden how it is issued and checked:

- A server-wide unary and stream interceptor requires a registered connector token on every call. The only
  exemption is `grpc.health.v1.Health/Check`, which reports SERVING and nothing else.
- Reflection is removed from the utilities server (it is a developer convenience on the public API, which keeps
  it behind the API token; plugins never use it).
- Tokens are 256 bits from `crypto/rand` (hex), keeping the existing JSON wrapper because plugins treat the
  token as an opaque string. Tokens are stored as SHA-256 digests and looked up by the digest of the presented
  value, so lookup time does not depend on the presented token's relation to any real token, and a heap dump of
  the map does not contain usable tokens.
- Rejections never include the presented token. They use the stable code `api.unauthenticated` and are counted
  and logged through the same rate-limited path as the API server's rejections.

### Alternatives considered

1. **A per-process secret, as first proposed.** One 256-bit secret for the process, handed to every spawned
   plugin. Lost: it duplicates the per-connector token, loses the ability to revoke one connector, would need
   a new env var and an SDK release for the plugin to send it, and an old plugin (every SDK built so far) would
   keep sending only the connector token.
2. **Unix domain socket in a 0700 directory** instead of TCP (`unix:///...` is a valid gRPC target and the env
   var is named `..._GRPC_TARGET`). Removes the network path entirely and relies on filesystem permissions.
   Lost for now: SDKs and any plugin that parses the target as `host:port` would break, there is no equivalent
   on Windows, and it needs a compatibility test across the SDK versions in the wild. Worth a separate design;
   it composes with this change.
3. **Mutual TLS with a per-process CA.** Strongest, and a large change (certificate distribution to every
   plugin, rotation, SDK support). Not justified while the token is already required.

## Versioning note (protocol and SDK)

- **No wire or protocol change.** The metadata key, the env var names and the token as an opaque string are
  unchanged. `conduit-connector-protocol` and `conduit-connector-sdk` are not modified; no SDK PR is needed.
- **Older standalone plugins keep working.** Any plugin whose SDK can call the schema service already sends the
  connector token, because the handlers have always required it. Plugins that never call the utilities server
  are unaffected. There is therefore no "plugin without token support" population to either tolerate or
  refuse, and no fallback-with-WARN path is added: a call without a valid token is refused, as before.
- **What changes for a plugin author:** only a plugin that depended on unauthenticated reflection or an
  unauthenticated non-schema RPC of the utilities server breaks. None exists in the protocol today.
- Token format: the token is still `{"value":"...","connector_id":"..."}` as a string. `value` is now 64 hex
  characters. A plugin must not parse it (it never could rely on the format).
- Deprecation policy (announce, warn, remove over two minor versions) does not apply: nothing a conforming
  plugin could observe is removed.

## Failure modes

| Case | Result |
| --- | --- |
| Local process with no token calls any RPC | `Unauthenticated`, code `api.unauthenticated`, nothing logged beyond the rate-limited rejection line. |
| Local process with a guessed or stale token | Same. A token is deregistered when its connector is torn down. |
| Plugin spawned only to read its specification (token `irrelevant-token`) calls the utilities server | Refused. It does not call it. |
| Connector restarts | New dispenser, new token; the old one stops working. |
| An unregistered method on the utilities server | grpc-go answers `Unimplemented` before any interceptor runs; no handler executes. It reveals only that a method name does not exist. |
| Reflection used by a developer against the utilities server | `Unimplemented`/`Unauthenticated`. Use the API server's reflection with the API token. |
| Token store grows | One digest per live connector; removed on teardown. |
| Two connectors present each other's token | Out of scope: tokens identify a connector instance, not a process; a plugin can read its own environment only. A malicious plugin is already arbitrary code. |

## Residual risk

A process running as the same OS user can read a plugin's environment (`/proc/<pid>/environ`, `ps eww`) and
so obtain a live token. The token protects against other users and unrelated local processes, not against the
same user. A Unix socket with a 0700 directory has the same limit.

## Upgrade and rollback

Binary-only change. Roll back by deploying the previous binary; plugins need no change either way.

## Observability

Refusals are counted in `conduit_api_rejected_requests_total{code="api.unauthenticated"}` and logged at WARN,
rate limited (see the API hardening runbook).
