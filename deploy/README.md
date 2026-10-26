# Deployment examples

Worked examples for running Conduit outside `conduit run` on a laptop. These are
**examples, not supported products**: Conduit does not ship, version or test an
official systemd package, ECS module or Nomad pack. Treat them as a starting point
you read and adapt. Only the Docker Compose quickstart is exercised in CI.

| Example | What it shows | Tested |
| --- | --- | --- |
| [`docker-compose/`](docker-compose/) | Conduit plus a running pipeline in one command | CI (`deploy-quickstart`) |
| [`systemd/`](systemd/) | A hardened unit for a VM or bare-metal host | `systemd-analyze verify` only |
| [`ecs/`](ecs/) | A Fargate task definition using Postgres for state | JSON syntax only |
| [`nomad/`](nomad/) | A Nomad job with a readiness check | `nomad job run -output` parse only |

## Choices every example makes

- **Image pinned by version**, `ghcr.io/conduitio/conduit:v0.20.6`, never `latest`.
  Release images are signed with cosign (keyless, GitHub OIDC). To verify before you
  deploy, see [`docs/releases.md`](../docs/releases.md); to pin harder, replace the
  tag with the `@sha256:` digest of the signed index.
  The pin is bumped for you: when a stable release is published, the `bump-image-pin`
  workflow verifies the new image and its signature, then opens a PR that updates the
  pin here and in the README. Nightlies and prereleases are never pinned, and a pin is
  never moved backwards. It only targets `main`; to bump a release branch that has
  `deploy/`, run the workflow manually with `base` set to that branch.
- **Stop timeout above 30 s.** `SIGTERM` drains in-flight records and checkpoints
  before exit, and the runtime gives up after 30 s
  ([guarantees](../docs/architecture/guarantees.md#stop-time)). Every example sets
  its orchestrator's kill timeout to 45 s so a drain is not cut short by `SIGKILL`.
  `SIGKILL` is recoverable (at-least-once, records replay), but it is the fallback,
  not the plan.
- **State on durable storage.** Positions live in the Conduit database (`db.*`). If
  that is on an ephemeral filesystem, a restart replays from the start of the source.
  Compose uses a named volume, systemd a `StateDirectory`, Nomad the sticky alloc
  disk, ECS Postgres.
- **Probes**: `/healthz` for liveness, `/readyz` for readiness
  ([health checks](../docs/health_check.md)).
- **No authentication on the API.** Conduit's HTTP and gRPC APIs are unauthenticated.
  The examples bind the published port to loopback or leave exposure to your network
  policy; put an authenticating proxy in front before exposing it.
- **No secrets in the files.** Credentials come from an env file, Secrets Manager or
  Nomad variables, shown as placeholders.
