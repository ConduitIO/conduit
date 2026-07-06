# Health and readiness

Conduit exposes two distinct probes, following the standard liveness/readiness split
used by orchestrators such as Kubernetes:

- **`/healthz` (liveness)** — is the Conduit process alive and able to reach its state
  store? Use it to decide whether to restart the process.
- **`/readyz` (readiness)** — is the engine finished starting up and able to serve? Use
  it to decide whether to route traffic. A pipeline being _degraded_ does **not** make
  Conduit "not ready" — the engine can still serve; degraded pipelines are reported in
  the response body instead.

## Liveness — `/healthz`

The liveness check verifies that Conduit can successfully connect to the database it was
configured with (BadgerDB, PostgreSQL, SQLite, or the in-memory one). It is available at
the `/healthz` path. Here’s an example:

```bash
$ curl "http://localhost:8080/healthz"
{"status":"SERVING"}
```

You can also check individual services within Conduit. The following example checks if the PipelineService is running:

```bash
$ curl "http://localhost:8080/healthz?service=PipelineService"
{"status":"SERVING"}
```

The services which can be checked for health are: `PipelineService`, `ConnectorService`, `ProcessorService`, and
`PluginService`.

## Readiness — `/readyz`

The readiness check reports whether the engine can serve requests. It returns:

- **`503 Service Unavailable`** with `{"status":"starting"}` while the engine is still
  starting up (services initializing, pipelines provisioning, API coming online).
- **`503 Service Unavailable`** with `{"status":"unavailable","reason":"…"}` if the engine
  has started but its state store is unreachable.
- **`200 OK`** with `{"status":"ready", …}` once the engine can serve. Degraded pipelines
  are listed in the body but do **not** make Conduit not-ready.

```bash
$ curl "http://localhost:8080/readyz"
{
  "status": "ready",
  "pipelines": {
    "total": 3,
    "running": 2,
    "degraded": 1,
    "degradedPipelines": [
      {"id": "pg-to-kafka", "status": "degraded", "error": "source connector error"}
    ]
  }
}
```

Use `/readyz` as the readiness probe and `/healthz` as the liveness probe.

## Metrics

Prometheus metrics are exposed at `/metrics`.

## Configuration

Every engine setting is configurable three ways, in order of precedence
(**flag > environment variable > config file**):

- a CLI flag, e.g. `--api.http.address=:8080`;
- an environment variable with the `CONDUIT_` prefix and the flag path upper-cased with
  `.`/`-` replaced by `_`, e.g. `CONDUIT_API_HTTP_ADDRESS=:8080`;
- a key in the `conduit.yaml` config file.

`conduit run` with no configuration starts with sensible defaults (zero-config).
