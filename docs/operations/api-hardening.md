# API hardening: loopback bind, API token, browser protections

Conduit's HTTP API (`:8080`), gRPC API (`:8084`) and the built-in UI can create, start, stop and
delete pipelines and connectors, and can read their configuration. Anyone who can call them can make
Conduit read local files, open network connections and run configured plugins on the host. This
page describes how the API is protected, what each refusal means and how to fix it.

## What changed

| Control | Behavior |
| --- | --- |
| Bind address | HTTP and gRPC default to `127.0.0.1:8080` and `127.0.0.1:8084`. |
| Startup refusal | A non-loopback `api.http.address` or `api.grpc.address` (for example `:8080`, `0.0.0.0:8080`, a LAN address) requires an API token. Without one Conduit exits at startup with `api.token_required`. |
| API token | When `api.token` or `api.token-file` is set, every HTTP request (including `/metrics`, `/openapi/` and the inspector WebSocket) and every gRPC call (including server reflection) must send `Authorization: Bearer <token>`. It is enforced on loopback too. |
| Origin check | A request with an `Origin` header that is neither in `api.http.cors.allowed-origins` nor the API's own origin gets `403 api.origin_forbidden`, on every method, including `OPTIONS` and WebSocket upgrades. |
| Content type | `POST`, `PUT`, `PATCH` and `DELETE` requests with a body must use `Content-Type: application/json` (`415 api.content_type_unsupported`). A request with no body needs no type. |
| Host check | On a loopback bind the `Host` header must be `localhost`, `127.0.0.1`, `[::1]` or a name in `api.http.allowed-hosts`; otherwise `403 api.host_forbidden` (DNS rebinding protection). **It is off on any non-loopback bind, including the container image's `0.0.0.0`; there only the token stops DNS rebinding** (see _Running in a container_). |
| Response headers | Every HTTP response carries `X-Frame-Options: DENY`, `Content-Security-Policy` with `frame-ancestors 'none'` and `script-src 'self'` (no inline script, no `eval`), `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`. The UI bundle and `conduit-auth.js` are same-origin files and load under it. |
| Connector utilities server | The internal gRPC server plugins use binds `127.0.0.1` on an ephemeral port instead of all interfaces. |
| Container image | Runs as the non-root user `conduit` (uid/gid 10001) and listens on `0.0.0.0` inside the container. |

Open without a token, deliberately: `GET /healthz`, `GET /readyz` (when a token is set, an unauthenticated
caller gets only `{"status":"ready"}` or the not-ready status; the pipeline summary, pipeline IDs and error
text need the token), CORS preflights (`OPTIONS` with
`Access-Control-Request-Method`; they carry no data) and the gRPC `grpc.health.v1.Health/Check`
method, so orchestrator probes keep working. The embedded UI's static files are also served without
a token, since they contain no instance data.

## Setting a token

Use a file or the environment, not a flag: flag values are visible in the process list.

```sh
openssl rand -hex 32 > /etc/conduit/api-token && chmod 600 /etc/conduit/api-token
conduit run --api.token-file /etc/conduit/api-token
# or
CONDUIT_API_TOKEN_FILE=/etc/conduit/api-token conduit run
CONDUIT_API_TOKEN="$(cat /etc/conduit/api-token)" conduit run
```

The token must be at least 16 characters and must not contain whitespace or control characters (leading
and trailing whitespace, such as a file's final newline, is trimmed). A whitespace-only `api.token` is an
error, not "no token". Setting both `api.token` and `api.token-file` is an
error. The token is read once at startup; restart Conduit to rotate it. `conduit config` prints it
as `[redacted]`.

WebSockets: a browser cannot set headers on a WebSocket, so the inspector stream also accepts the
token as a subprotocol: offer `conduit` and `conduit.token.<base64url(token)>` in
`Sec-WebSocket-Protocol`. The server checks it in constant time, never forwards it, and selects
`conduit`. A token in the query string is never accepted. Non-browser clients can simply send the
header.

Clients:

- **CLI** (`conduit pipelines ...`, `conduit doctor`, `conduit connectors uninstall`, and so on)
  read `--api.token-file`, `CONDUIT_API_TOKEN_FILE` or `CONDUIT_API_TOKEN`, in that order.
- **MCP server** (`conduit mcp --api-address ...`): `--api-token-file`, or the same environment
  variables.
- **HTTP**: `curl -H "Authorization: Bearer $(cat /etc/conduit/api-token)" localhost:8080/v1/pipelines`.
- **gRPC**: send the same value in the `authorization` metadata key.
- **Prometheus**: `authorization: { type: Bearer, credentials_file: /etc/prometheus/conduit-token }`
  in the scrape config. `/metrics` is protected.

## Running in a container

The image listens on `0.0.0.0` inside the container, so it needs a token, and the ports should be
published on the host's loopback unless an authenticating proxy sits in front.

**Because the bind is not loopback, the Host header check is off in the container, and the token is the only
thing that stops DNS rebinding.** A page that rebinds a name to the published port can reach the API
exactly as far as it can guess or obtain the token. The Origin and content-type checks still apply. Keep
the token secret, use a random 256-bit value, and do not reuse it. Without TLS the token travels in
plaintext on every request: publish on loopback only, or put a TLS-terminating proxy in front, before
using it across a network.

```sh
docker run \
  -e CONDUIT_API_TOKEN="$(openssl rand -hex 32)" \
  -p 127.0.0.1:8080:8080 \
  ghcr.io/conduitio/conduit:latest
```

Prefer a file (`-v ./api-token:/run/secrets/api-token:ro -e CONDUIT_API_TOKEN_FILE=/run/secrets/api-token`)
or your orchestrator's secret mechanism. `-p 8080:8080` publishes the API on every host interface;
with a token that is no longer open access, but it still exposes the port, so do not do it on an
untrusted network.

The process runs as uid/gid 10001. Volumes created by an older (root) image are owned by root and
the new image cannot write to them. Fix once with
`docker run --rm -u 0 -v <volume>:/data alpine chown -R 10001:10001 /data` (or `chown` the host
directory for a bind mount).

## Runbook

### `api.token_required`: Conduit exits at startup

```text
code: api.token_required
message: "api.http.address" is ":8080", which is reachable from the network, and no API token is configured
configPath: api.token-file
```

**Diagnosis.** Either address is not a loopback address and no token is set. The container image
sets both addresses to `0.0.0.0`.

**Remediation.** Set `api.token-file` / `CONDUIT_API_TOKEN_FILE` (or `CONDUIT_API_TOKEN`), or bind to
`127.0.0.1` and reach Conduit through an authenticating reverse proxy on the same host. There is
no flag to run an unauthenticated network API. If a proxy in front already authenticates callers,
still set a token and have the proxy send it as `Authorization: Bearer ...`.

### `api.token_invalid`: Conduit exits at startup

**Diagnosis.** The token file cannot be read or is empty, the token is shorter than 16 characters,
is only whitespace, contains whitespace or a control character (a paste error, for example a line break
in the middle), or both `api.token` and `api.token-file` are set. The message never contains the token.

**Remediation.** Fix the file path and permissions (readable by the Conduit user), use a longer
token (`openssl rand -hex 32`), and set only one source.

### `api.unauthenticated`: HTTP 401 or gRPC `Unauthenticated`

**Diagnosis.** The request carried no bearer token or the wrong one. Every refusal increments
`conduit_api_rejected_requests_total{code="api.unauthenticated"}` (the only label is the error code;
`api.origin_forbidden`, `api.host_forbidden` and `api.content_type_unsupported` have their own series).
A rate of 401s far above your own clients' is a guessing attempt: alert on
`rate(conduit_api_rejected_requests_total{code="api.unauthenticated"}[5m])`. The server also logs a `warn`
with the remote address, at most one line per code every 10 seconds, with a `suppressed_since_last`
count (never the presented credential). There is no request rate limiting; use a token from
`openssl rand -hex 32` and, on a network, a proxy that rate limits.

**Remediation.** Send the token (see _Clients_). A token in a query string, cookie or `Basic`
header is not accepted. The UI prompts for the token itself; a script or CLI must send it.

### `api.origin_forbidden`: HTTP 403

**Diagnosis.** The request has an `Origin` header (browsers always send one on cross-origin
requests and on `POST`) that is not the API's own origin and is not allowlisted. Either a page on
another site is calling the API, which is the attack this blocks, or a legitimate UI is served from a
different origin.

**Remediation.** If the caller is yours, serve it from the API's origin or add its exact origin to
`api.http.cors.allowed-origins` (`scheme://host[:port]`, no trailing slash). Do not use `*` except
on loopback for local development: it is refused on a non-loopback bind. If you did not expect the
caller, no action is needed; check who can reach the browser that sent it. Non-browser clients
(`curl`, the CLI, SDKs) send no `Origin` and are not affected.

### `api.host_forbidden`: HTTP 403

**Diagnosis.** On a loopback bind the `Host` header was not `localhost`, `127.0.0.1`, `[::1]` or a
listed name. Typical causes: a DNS-rebinding attack, or a legitimate local reverse proxy, tunnel or
`/etc/hosts` alias that forwards a different name.

**Remediation.** Address the API by a loopback name, or list the name (host only, no port) in
`api.http.allowed-hosts`. The check does not run on non-loopback binds (including the container image),
where the token alone protects the API.

The check fails closed. These are refused: an empty or missing `Host`; `localhost.` and any other name
with a trailing dot; any loopback address other than `127.0.0.1` and `::1` (`127.0.0.2`,
`[::ffff:127.0.0.1]`, `0.0.0.0`); and names with spaces, commas or other characters. Comparison is
case-insensitive and ignores the port. `api.http.allowed-hosts` entries are matched exactly (host only), so
list `conduit.internal`, not `conduit.internal.`.

### `api.content_type_unsupported`: HTTP 415

**Diagnosis.** A `POST`, `PUT`, `PATCH` or `DELETE` with a body used a content type other than
`application/json`. Browsers can send `text/plain`, form and multipart bodies cross-site without a
preflight, which is why they are refused.

**Remediation.** Send `Content-Type: application/json`.

## Residual risk and decisions

These are known, accepted limits. They are decisions, not oversights: the default is a loopback bind, and a
token is required as soon as the API leaves loopback.

1. **A loopback bind with no token trusts every local process.** Any process on the host, as any user, can
   call the HTTP and gRPC APIs on `127.0.0.1` and control pipelines. The loopback default keeps the API off the
   network, and the Origin and Host checks keep web pages from reaching it, but they do nothing against local
   code. On a shared host (multi-user machine, CI runner, a container sharing its network namespace with other
   workloads, a sidecar you do not trust) set `api.token-file`; with a token set it is enforced on loopback too.
2. **No TLS and no mTLS in Conduit.** The token is a shared secret sent in clear over plain HTTP and gRPC.
   Over any network you do not fully trust, terminate TLS (or mTLS) in front of Conduit: a reverse proxy, a
   service mesh sidecar, or a VPN. The decision to accept "token or mTLS" for a non-loopback bind is
   implemented as token only; an mTLS proxy in front still needs the token set on Conduit (the proxy can add it).
3. **gRPC server reflection stays enabled on the API server.** It is behind the token whenever a token is set,
   and on a loopback bind with no token it is open to local processes like everything else (point 1). The
   connector utilities server no longer has reflection at all.
4. **Same-user processes can read a token.** The token lives in the process environment or a file. A process
   running as the same OS user as Conduit can read the environment of Conduit and of its plugins
   (`/proc/<pid>/environ`) and so also obtain connector tokens (below). File permissions (`0600`) protect the
   token file from other users only.
5. **No request rate limiting.** Guessing is made visible (a counter and a rate-limited warning) but not
   slowed. Use a 256-bit token (`openssl rand -hex 32`) and, on a network, a proxy that rate limits.
6. **The UI keeps the token in `sessionStorage`,** readable by any script running in the page. Do not serve
   untrusted content from the API's origin.
7. **`/healthz`, `/readyz`, CORS preflights, the gRPC health `Check` and the UI's static files are open** by
   design (probes and the page that prompts for the token).

### The connector utilities server

Plugins that Conduit starts call a separate gRPC server (schema service) on `127.0.0.1` and an ephemeral port.
It needs no configuration and cannot be reached from the network. Every call to it, including streams, must
carry the per-connector token Conduit generated when it started that connector (256 bits from `crypto/rand`,
passed to the plugin in its environment, sent by the SDK in the `conduit-connector-token` metadata, compared by
SHA-256 digest, revoked when the connector stops or its plugin fails to start). Other local processes are refused, with `api.unauthenticated`,
counted in `conduit_api_rejected_requests_total`. Only the health `Check` is open. Design and the
alternatives considered: `docs/design-documents/20261021-connector-utilities-authentication.md`.

If a plugin logs `missing or invalid connector token`: it was started outside Conduit, or it outlived its
connector (the token is revoked when the connector is torn down, when its plugin fails to start, and
when Conduit cannot create the plugin at all), or its SDK predates the schema service. Plugins built on a
supported SDK need no change.
