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
| Host check | On a loopback bind the `Host` header must be `localhost`, `127.0.0.1`, `[::1]` or a name in `api.http.allowed-hosts`; otherwise `403 api.host_forbidden` (DNS rebinding protection). |
| Connector utilities server | The internal gRPC server plugins use binds `127.0.0.1` on an ephemeral port instead of all interfaces. |
| Container image | Runs as the non-root user `conduit` (uid/gid 10001) and listens on `0.0.0.0` inside the container. |

Open without a token, deliberately: `GET /healthz`, `GET /readyz`, CORS preflights (`OPTIONS` with
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

The token must be at least 16 characters. Setting both `api.token` and `api.token-file` is an
error. The token is read once at startup; restart Conduit to rotate it. `conduit config` prints it
as `[redacted]`.

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
published on the host's loopback unless an authenticating proxy sits in front:

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
or both `api.token` and `api.token-file` are set. The message never contains the token.

**Remediation.** Fix the file path and permissions (readable by the Conduit user), use a longer
token (`openssl rand -hex 32`), and set only one source.

### `api.unauthenticated`: HTTP 401 or gRPC `Unauthenticated`

**Diagnosis.** The request carried no bearer token or the wrong one. The server logs each rejection
at `warn` with the remote address (never the presented credential). Repeated rejections from one
address are a guessing attempt.

**Remediation.** Send the token (see _Clients_). A token in a query string, cookie or `Basic`
header is not accepted. A browser page cannot set `Authorization` on a WebSocket; use a non-browser
client for the inspector stream when a token is set.

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
`api.http.allowed-hosts`. The check does not run on non-loopback binds, where the token protects
the API.

### `api.content_type_unsupported`: HTTP 415

**Diagnosis.** A `POST`, `PUT`, `PATCH` or `DELETE` with a body used a content type other than
`application/json`. Browsers can send `text/plain`, form and multipart bodies cross-site without a
preflight, which is why they are refused.

**Remediation.** Send `Content-Type: application/json`.

## Residual risk

- The token is a shared secret over plaintext HTTP and gRPC. Conduit does not terminate TLS. Over
  an untrusted network put TLS in front (reverse proxy, service mesh, VPN). There is no mTLS
  option in Conduit itself.
- The built-in UI does not yet prompt for a token. With a token set, the UI loads but its API calls
  are rejected. Use the CLI, or the UI on an instance without a token on loopback.
- Any local user or process on the host can reach a loopback-bound API without a token, and the
  connector utilities server (which serves schema lookups to plugins, with no authentication) is
  reachable by any local process. Set a token on shared hosts.
- gRPC server reflection stays enabled; it is behind the token when one is set.
