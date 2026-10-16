# Docker Compose quickstart

Conduit plus a running pipeline, in one command. An example, not a supported
product: see [`../README.md`](../README.md).

```sh
docker compose up -d --wait
curl -s localhost:8080/v1/pipelines/quickstart
docker compose exec conduit tail -n 3 /data/records.ndjson
docker compose logs -f conduit
docker compose down            # keeps the state volume; add -v to delete it
```

What you get:

- `ghcr.io/conduitio/conduit:v0.20.3`, pinned. Verify its signature first, see
  [`docs/releases.md`](../../docs/releases.md). Set `CONDUIT_IMAGE` to try another build.
- The pipeline in [`pipelines/quickstart.yaml`](pipelines/quickstart.yaml): the built-in
  generator source writes five records per second to the built-in file destination at
  `/data/records.ndjson`. Edit it, or replace it with your own, and run
  `docker compose restart conduit`.
- Conduit's database on the `conduit-data` volume (`/data`), so `docker compose down`
  followed by `up` resumes the pipeline from its saved position instead of starting over.
- The HTTP API and web UI at <http://localhost:8080>, published on `127.0.0.1` only.
  Change the host port with `CONDUIT_HTTP_PORT`. The API has no authentication.
- A health check on `/readyz` and a 45 s stop grace period (the runtime drains for up to
  30 s on `SIGTERM`).

Connectors that are not built in (standalone plugins) go in a directory mounted at
`/app/connectors`; see the [connector docs](https://conduit.io/docs).

## Test

`./test.sh` brings the stack up under its own project name and a free port, waits for
the pipeline to run and records to land, restarts the container and checks the pipeline
resumes, then removes what it created. CI runs it on changes under `deploy/`.
