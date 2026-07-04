# Conduit Roadmap

**Mission:** Make Conduit the default way to move and transform data in real time — a Kafka
Connect replacement that works with any broker (Kafka, NATS, Redpanda, Hazelcast, Pulsar) or no
broker at all, runs anywhere from a laptop to Kubernetes to embedded inside your application, and
lets you build connectors and processors in real programming languages.

This roadmap is a living document. Items move as we learn. Dates are targets, not promises — but
the monthly release cadence is a promise.

---

## Principles

1. **Apache-2.0, forever.** No relicensing, no enterprise-only connectors, no rug pulls. Open
   governance with a public contributor ladder.
2. **Real languages, no bespoke DSL.** Transformations are code you can test, version, and reuse
   — written in Python, Rust, TypeScript, or Go and compiled to WASM — not a config-language
   dialect you have to learn. Prebuilt processors cover the common 90% with zero code.
3. **Broker-neutral.** Conduit is Switzerland. Every streaming provider is a peer; none is
   privileged. No broker required at all for point-to-point pipelines.
4. **Boring to operate.** Single static binary, no JVM, no ZooKeeper, no worker cluster.
   Observability built in. Replay and recovery are first-class verbs, not incident-response
   archaeology.
5. **Migration is a product.** Moving off Kafka Connect should be a command, not a quarter-long
   project.
6. **Agent-legible by design.** Structured output, deterministic machine-actionable errors, and
   an MCP server — because the next generation of users includes AI agents building and
   repairing pipelines.
7. **Right-sized state.** Conduit handles the stateful processing most pipelines actually need
   (dedup, enrichment, simple windows) without distributed-snapshot complexity. For heavy
   stateful workloads, Conduit integrates with streaming SQL engines rather than reinventing
   them.
8. **Single-node engine, scale-out by scheduling.** The engine never grows membership protocols,
   leader election, or consensus. Distribution — running many pipelines across many instances, or
   one hot pipeline across several — is a scheduling problem solved a layer above
   (operator/control plane). This keeps the engine embeddable, boring to operate, and free of
   rebalance-protocol misery.
9. **Open core with a bright line.** The engine, all connectors, SDKs, registry, CLI, UI, Helm
   chart, and the Kubernetes operator are Apache-2.0 — a team can run Conduit in production at
   any scale for free, forever. Commercial products live above the open source (org-scale
   governance and federation), never inside it, and nothing shipped as open source is ever moved
   behind a paywall.

---

## Phase 0 — Revival (Weeks 0–8)

_Goal: an unmistakable signal that Conduit is active, maintained, and shipping._

- [x] Triage all open issues and PRs — every item closed, merged, or labeled with a decision
- [x] **Cut v0.15.0 stable** off the running nightly train: dependency upgrades, security
      patches, bug fixes, Go version bump
- [ ] Publish this roadmap + public GitHub Project board with milestones — _roadmap published;
      Project board still to do_
- [x] Governance doc: Apache-2.0 commitment, maintainer ladder, decision process
- [x] Seed `docs/architecture-decision-records/` with the foundational decisions already made:
      single-node engine (distribution via the scheduling layer), no bespoke DSL, WASM component
      model, local-state-only
- [ ] Begin CNCF Sandbox application process
- [ ] Revive Discord; monthly community call on a public calendar
- [ ] Hold the monthly release train (cadence over scope) — _v0.15.0 is the first; next cadence
      release due ~2026-08_

**Definition of done:** zero untriaged issues, v0.15.0 stable shipped, roadmap and governance
published.

**Status — 2026-07-04:** Halfway through Phase 0. Shipped: **v0.15.0**, the first release under
active maintenance — dependency + security refresh (~90 updates incl. `x/net`), Go 1.25, four
provisioning/connector bug fixes (#1999, #2255, #1274, #2061) and a metrics perf win (#2268);
triage complete; governance and the four foundational ADRs published. Release tooling was hardened
along the way — CI now cross-builds `linux/386` and the release Docker image, so release-only build
breaks are caught on PRs. **Remaining:** public Project board, CNCF Sandbox application, and
community (Discord + monthly call) — the non-code items that pair with the launch announcement, and
recruiting a co-maintainer (the gate on the deferred Tier-1 lifecycle work: #1659, arch-v2).

## Phase 1 — Developer Experience Core (Months 1–4)

_Goal: the best first-hour and first-week experience of any data integration tool — for humans
and for their agents._

### The 5-minute wow

- [ ] `brew install conduit` / `curl | sh` / single-binary downloads for all platforms
- [ ] `conduit init` — scaffolds a working pipeline (e.g., Postgres CDC → file/S3) with zero
      manual config
- [ ] `conduit init --template <name>` — template gallery of one-command recipes
- [ ] Refreshed built-in UI (a rebuild, not a polish — see below): live record flow, per-stage
      inspection, pipeline graph

### CLI as product

- [ ] `conduit pipeline validate | lint | dry-run`
- [ ] `conduit doctor` — environment and config diagnostics
- [ ] Hot-reload of pipeline configs in dev mode
- [ ] `conduit pipeline dev` — local dev loop with record inspector
- [ ] `--json` structured output on every command
- [ ] Deterministic, machine-actionable errors: error code + failing config path + suggested fix

### Agent-native (a Phase 1 priority)

- [ ] Official Conduit MCP server: agents can scaffold, validate, deploy, inspect, and repair
      pipelines
- [ ] `llms.txt` + single-page condensed documentation dump for LLM context
- [ ] `conduit generate "<natural language>"` — AI-assisted pipeline generation from plain English

### Plugin scaffolding

- [ ] `conduit connector new --lang go|python|rust|ts` — full repo: SDK wiring, tests, CI,
      release workflow, acceptance-test harness
- [ ] `conduit processor new` — same treatment
- [ ] Target: **first working custom connector in under 30 minutes**

### WASM everywhere

- [ ] WASM **connectors** (today: WASM processors only) — in-process, sandboxed, single portable
      artifact, no gRPC sidecar
- [ ] WASI Preview 2 / component model adoption
- [ ] Language SDK rollout, in priority order:
  - [ ] **Go** — reference SDK (exists; keep current with protocol)
  - [ ] **Python** — gRPC standalone path first (WASM fast-follow); first `libconduit` binding
  - [ ] **Rust** — full WASM component-model path; proves the WASM connector architecture
  - [ ] **TypeScript** — WASM via componentize-js
  - [ ] Java: served via the Kafka Connect wrapper short-term; native SDK is a Phase 3+ decision
  - [ ] C# / Ruby: demand-driven only — not speculatively built

### Connector registry

- [ ] Public registry: `conduit connectors install <name>` pulls signed binaries/WASM artifacts
- [ ] Community publishing via GitHub Action + signing
- [ ] Registry web UI with search, verified badges, download stats

### Templates

- [ ] Template gallery shipped with the registry: `postgres-iceberg`, `mysql-snowflake`,
      `postgres-pgvector-rag`, `shopify-warehouse`, `kafka-clickhouse`, and more
- [ ] Community-contributed templates with the same publishing flow as connectors

### Deployment fundamentals (12-factor citizenship — the universal answer)

- [ ] Env-var configuration for all engine settings
- [ ] `/healthz` and `/readyz` endpoints; Prometheus metrics endpoint
- [ ] Graceful SIGTERM drain (checkpoint, then exit) — see data-integrity invariants
- [ ] Official minimal container image; docker-compose quickstart
- [ ] systemd unit file for VM deployments
- [ ] `conduit run --pipelines <dir>` — run a directory of pipeline configs (GitOps-friendly)
- [ ] `deploy/` directory with documented examples: docker-compose, systemd, ECS task definition,
      Nomad job spec (examples, not supported products — promoted only on demand)

### Long-lead protocol design (build in Phase 3, design now)

- [ ] Design doc + protocol RFC: **partition claims** — sources declare their partitionable units
      so a scheduler can run one hot pipeline across multiple instances. Touches
      `conduit-connector-protocol`, so the seam ships early to avoid a breaking rev later

### Embedded v1

- [ ] Stable, documented Go library API for embedding Conduit in applications
- [ ] C ABI shared library (`libconduit`) as the base for cross-language bindings
- [ ] Python and Node.js bindings (Java/Ruby to follow on demand)

## Phase 2 — Kafka Connect Migration, Connector Coverage & AI Pipelines (Months 3–8)

_Goal: switching from Kafka Connect is a command; keeping a RAG index fresh is 10 lines of YAML._

### Migration tooling

- [ ] `conduit migrate kafka-connect` — reads Kafka Connect worker/connector JSON (Debezium,
      JDBC, S3, etc.), emits Conduit pipeline config + compatibility report; never silently drops
      config it can't translate
- [ ] SMT compatibility pack: 1:1 processor equivalents for standard Kafka Connect SMTs
- [ ] Hardened Kafka Connect wrapper: run existing KC connector JARs inside Conduit for day-one
      catalog parity (also the Java-team on-ramp)

### CDC (the moat)

Production-grade change data capture — snapshot + streaming, schema evolution, heartbeats,
tombstones:

- [ ] PostgreSQL (harden existing)
- [ ] MySQL
- [ ] MongoDB
- [ ] SQL Server
- [ ] Oracle

### The AI data pipeline (first-class use case)

The canonical pipeline — CDC → chunk → embed → vector store — as a headline Conduit workload:

- [ ] Chunking processors (document splitting strategies for RAG)
- [ ] Embedding processors: OpenAI, Voyage, local models
- [ ] Vector destinations: pgvector, Qdrant, Pinecone, Turbopuffer
- [ ] "Keep your RAG index fresh from Postgres" quickstart + template
- [ ] Positioning docs: Conduit as the data layer for AI applications

### Iceberg-first lakehouse story

- [ ] Best-in-class Apache Iceberg destination: upserts, compaction-friendly writes, catalog
      support (REST, Glue, Nessie)
- [ ] Headline use case: operational DB → lakehouse in real time, no Kafka required

### Priority native connectors (rough order, after Iceberg)

- [ ] Snowflake
- [ ] S3 / GCS / Azure Blob with Parquet support
- [ ] ClickHouse
- [ ] BigQuery
- [ ] Databricks / Delta Lake
- [ ] Elasticsearch / OpenSearch
- [ ] NATS JetStream
- [ ] Redpanda (native, tuned)
- [ ] Kinesis / SQS / SNS
- [ ] Google Pub/Sub
- [ ] Redis
- [ ] HTTP / webhooks (source + destination)
- [ ] DuckDB / MotherDuck

### Kubernetes (static scale-out)

- [ ] Helm chart: Deployment/StatefulSet, pipeline configs via ConfigMap or git-sync, HPA hooks,
      ServiceMonitor — covers static pipeline-to-instance assignment before the operator exists

### Enterprise correctness

- [ ] Confluent Schema Registry wire compatibility (Avro, Protobuf, JSON Schema)
- [ ] **Schema contracts & drift policy:** configurable behavior on schema drift — halt, DLQ, or
      auto-evolve — with drift surfaced in the UI
- [ ] Dead-letter queues
- [ ] Documented delivery semantics per source/destination pair
- [ ] Pipeline recovery, retry/backoff policies
- [ ] **Replay & backfill as first-class verbs:** `conduit pipeline replay --from <position>`,
      snapshot re-runs, offset inspection and reset in CLI and UI
- [ ] Secrets management: env, Vault, cloud KMS

## Phase 3 — Stateful Processing, Scale & Ecosystem (Months 6–12)

_Goal: capture the 80% of stream-processing workloads that don't need Flink, and run credibly at
enterprise fleet scale._

### Lightweight state layer ("the 80% of Flink")

Right-sized stateful processing — local state (embedded KV store), checkpointed with the
pipeline, no distributed snapshots or state backends to tune:

- [ ] Deduplication with TTL
- [ ] Lookup/enrichment tables: cached reference data from a database or topic
- [ ] Tumbling and sliding window aggregations (counts, sums, simple rollups)
- [ ] Clear documentation of what this is (most real-world jobs) and isn't (large joins, complex
      event-time processing)

### Streaming SQL partnerships (the other 20%)

- [ ] First-class ingest/egress integrations and reference architectures with RisingWave,
      Materialize, and ClickHouse
- [ ] Documented pattern: "Conduit + streaming SQL engine replaces Kafka Connect + Flink"

### Scale-out & high availability (open source)

The engine stays single-node (Principle 8); distribution is scheduling:

- [ ] **Kubernetes operator** (Apache-2.0, always): `Pipeline` CRD, bin-packing of pipelines
      across pods, health-based rescheduling, lag-based autoscaling
- [ ] **Checkpoint-aware rolling upgrades:** drain → checkpoint → reschedule, no data interruption
      during version bumps
- [ ] **Hot-pipeline parallelism:** scheduler assigns partition claims (protocol seam from Phase
      1) so one pipeline runs across multiple instances; Kafka-consumer-group sources get this
      natively
- [ ] **Active/passive HA:** instance dies → scheduler reassigns → pipeline resumes from
      checkpoint (correct by construction via the data-integrity invariants; no consensus, no
      warm standbys in v1)

### Fleet control plane (open source core)

- [ ] Lightweight console that registers many Conduit instances: fleet-wide visibility, health,
      versions
- [ ] GitOps-native: console reads state; pipeline config stays in git
- [ ] Shared scheduling brain with the operator — the operator is the control plane's Kubernetes
      backend
- [ ] Rolling upgrades across a fleet

### Performance

- [ ] Published, reproducible benchmarks (via [benchi](https://github.com/ConduitIO/benchi)) vs
      Kafka Connect and other stream processors, updated per release
- [ ] Pipeline-wide batching; allocation reduction; profiling as CI gate
- [ ] Investigate Arrow-based internal record format for columnar destinations

### Ecosystem tooling

- [ ] **Terraform provider for the Conduit API:** pipelines, connectors, processors as
      declarative resources. Infrastructure Terraform (EKS modules, etc.) stays in `deploy/` as
      examples, not products
- [ ] Production reference architectures
- [ ] OpenTelemetry traces + metrics, prebuilt Grafana dashboards

### AI-assisted connector development

- [ ] `conduit connector generate --from-openapi <spec>` — AI-assisted connector scaffolding from
      API specs

## Documentation (parallel track, starts Phase 0)

- [ ] Getting-started rewrite around the 5-minute path; per-broker quickstarts (Kafka, NATS,
      Redpanda, Hazelcast, none)
- [ ] "Migrating from Kafka Connect" section: concept mapping (worker→instance, task→pipeline,
      SMT→processor, converter→schema), per-connector guides for the top 10 KC connectors
- [ ] "AI data pipelines with Conduit": RAG sync, embedding pipelines, vector store patterns
- [ ] Connector development tutorial per supported language (Go, Python, Rust, TS)
- [ ] Processor cookbook: 30+ copy-paste recipes
- [ ] Embedding guide per language
- [ ] Honest comparison pages: vs Kafka Connect, vs Redpanda Connect / Bento, vs Flink (when you
      need it, when you don't), vs batch ELT tools
- [ ] Architecture deep-dive: ordering guarantees, end-to-end ack propagation, delivery
      semantics, state & checkpointing model
- [ ] `llms.txt` and LLM-optimized doc formats maintained alongside human docs

## UI note

Conduit's historical UI was Ember-based and later de-emphasized. "Refreshed built-in UI" means
**rebuild, not polish** — built modern and agent-friendly from scratch, on the same API the CLI
and MCP server use (no divergent code paths). UI surfaces by phase: Phase 1 = built-in
single-instance UI (live record flow, per-stage inspection, pipeline graph) + registry web UI;
Phase 2 = schema-drift visibility, replay/offset management; Phase 3 = fleet console
(multi-instance). Deeper org-scale console features are the commercial product.

---

## Open source vs. enterprise (the bright line, stated publicly)

We're an open-core project and we'd rather tell you where the line is than let you guess.

**Apache-2.0, forever — everything a team needs to run Conduit in production at any scale:**
engine · all connectors and processors · all SDKs · registry and templates · CLI · built-in UI ·
Helm chart · **Kubernetes operator** (scheduling, rescheduling, autoscaling, checkpoint-aware
rolling upgrades) · fleet console core

**Commercial (separate product, separate repo) — what an _organization_ needs to govern Conduit
at fleet scale:** multi-cluster/multi-region federation · SSO/SAML/SCIM, RBAC, audit logs · data
lineage, PII policy packs, org-level schema-contract enforcement, compliance reporting ·
cross-fleet upgrade orchestration, SLA alerting, cost/throughput analytics · support and SLAs ·
air-gapped and FIPS-hardened distributions

**The one-way ratchet:** nothing shipped as open source will ever be moved behind a paywall.
Commercial features may become open source over time; the reverse never happens.

---

## How to contribute

- Check the [GitHub Project board](https://github.com/orgs/ConduitIO/projects) for issues labeled
  `good first issue` and `help wanted`
- Build a connector — the scaffolding makes it a weekend project, and the registry gets it
  distributed
- Contribute a pipeline template — the gallery is community-driven
- Join the community call and Discord
- Everything here is open for discussion; open an issue against this roadmap

**North-star metrics we hold ourselves to:** time-to-first-pipeline < 5 minutes ·
time-to-first-custom-connector < 30 minutes · an AI agent can go from zero to a running pipeline
using only the MCP server and llms.txt · monthly release cadence · zero untriaged issues older
than 14 days.
