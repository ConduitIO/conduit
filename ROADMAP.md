# Conduit Roadmap

**Mission:** Make Conduit the best runtime and tooling for real-time data pipelines built on the
standards teams already run — the Kafka Connect REST API, existing connector JARs, the Debezium
change-event format, Confluent Schema Registry, the `connect-offsets` topic, Apache Iceberg,
OpenLineage and MCP. Conduit works with any broker (Kafka, NATS, Redpanda, Hazelcast, Pulsar) or no
broker at all, runs anywhere from a laptop to Kubernetes to inside your application, and lets you
build connectors and processors in real programming languages.

We embrace the Kafka Connect ecosystem's standards instead of replacing them. Much of what is
below — `conduit kc lint`, `conduit kc diff`, the compatibility reports — is useful to teams that
never switch runtimes. The connector protocol and its acceptance suite are open, so other runtimes
can adopt them too.

This roadmap is a living document. Items move as we learn. We aim for a monthly train, but a
release ships when it meets its bar, not on a date; when a release gets tight we cut scope, not the
bar.

---

## Who this is for

In priority order:

1. **Teams moving off Kafka Connect**, starting with Postgres and MySQL change data capture. What
   we offer: Debezium-grade CDC that is free and Apache-2.0 with no license key, broker-neutral, a
   single binary, and drop-in with what you already run.
2. **AI and data-application builders** who need curated, stateful, AI-ready streams: deduplicated,
   joined, quality-checked records feeding vector stores, feature stores, lakehouses and agents.

---

## Principles

1. **Apache-2.0, forever.** No relicensing, no enterprise-only connectors, no rug pulls. Open
   governance with a public contributor ladder.
2. **Real languages, no bespoke DSL.** Transformations are code you can test, version, and reuse
   — written in Go, Python, TypeScript, Rust, Java or C# — not a config-language dialect you have
   to learn. Processors run in-process as WASM, or out-of-process over gRPC when they need native
   libraries. Prebuilt processors cover the common 90% with zero code. Established standard query
   languages (jq, JMESPath, SQL) are allowed as processor parameters; they are never a pipeline
   configuration language.
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
7. **Right-sized state.** Conduit handles the stateful processing integration and AI pipelines
   need — dedup, lookup tables and stream-table joins, keyed upsert, windows and aggregates with
   bounded lateness — in a local embedded store that commits atomically with the pipeline
   checkpoint. No distributed snapshots, no global watermarks, no pluggable state backends.
   Stream-stream joins, large-state joins and complex event time are served by integrating with
   streaming SQL engines rather than reinventing them. See
   [ADR 20261008](docs/architecture-decision-records/20261008-state-layer-scope-windows-and-bounded-lateness.md).
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
10. **Standards over reinvention.** Conduit speaks the formats and APIs teams already depend on —
    the Connect REST API, connector JARs, Debezium change events, Schema Registry subjects,
    `connect-offsets`, Iceberg, OpenLineage, MCP — so adopting it never means abandoning them.

---

## Language support

Six languages are officially supported, in two tiers. Official SDKs are maintained by the
project and must pass the same conformance suite in CI.

- **Full tier — Go, Python, TypeScript, Rust:** connectors over gRPC (standalone plugins),
  processors (WASM in-process and/or gRPC out-of-process), the processor state API, and an
  embedded client.
- **Enterprise tier — Java, C#:** connectors over gRPC (standalone plugins), processors on the gRPC
  out-of-process runtime including the state API, and an embedded client (generated gRPC bindings
  with a thin hand-written layer). WASM for Java and C# comes only once their component-model
  toolchains are production-grade; we re-evaluate that yearly.
- **Community tier — every other language** (Ruby, …): an open, documented protocol plus a
  conformance kit. Community SDKs that pass the kit are listed as community-maintained.

Java teams can also run existing Kafka Connect connector JARs (v0.23) and use the SMT compatibility
pack (v0.22); the Java SDK (v0.25–v0.26) is the native path forward for Kafka Connect connector
authors.

**Two processor runtimes, one contract.** Processors run either in-process as WASM (sandboxed;
suited to Go, Rust, TypeScript and untrusted logic) or out-of-process over gRPC (needed for Python
with native libraries such as tokenizers, numpy or model clients, which cannot run in WASM). Both
runtimes implement the same language-neutral contract — protobuf for gRPC, WIT for the WASM
component model — including the processor state API. WASM processors today use the existing
processor ABI; WIT/component-model processors depend on the WASM connector/component host ADR
planned for v0.23, which supersedes
[ADR 20260722](docs/architecture-decision-records/20260722-wasm-component-model-deferred.md) in
part. Connectors in every official language ship over gRPC.

**Parity.** The connector and processor protocols are specified first, bindings are generated per
language, and a language-neutral conformance suite plus a published parity matrix show what each
SDK supports. After a protocol change, full-tier SDKs reach parity within one minor release and
enterprise-tier SDKs within two; until then the feature is labelled Go-only preview. Protocol spec
minor versions come at most every other release, and plugin spec v1 includes stable error codes.
Design: `docs/design-documents/20261008-sdk-parity-and-open-plugin-protocol.md` (#2948).

**Embedding, plainly stated.** True in-process embedding is Go-only. Python, TypeScript, Java, C#
and other clients manage a local or remote engine over the gRPC control API
([ADR 20260724](docs/architecture-decision-records/20260724-embed-bindings-via-grpc.md)); the
record data path never crosses into the host language.

---

## Catalog track

Reuse and certify first; build only real gaps. A connector inventory
(`docs/catalog-inventory.md`, #2951) found far more existing connectors — many in
`conduitio-labs` — than the registry lists. The catalog work below starts from that inventory.

Every connector carries a quality tier in the registry and on a public parity scorecard:

- **Certified** — everything in Verified, plus: built on a native family core, chaos-tested
  (kill and resume), a benchmark baseline, documented delivery semantics, and maintained by the
  Conduit project.
- **Verified** — passes the plugin conformance suite, and the publisher's identity is verified
  (signed registry publish).
- **Adapter** — runs through the Kafka Connect JAR host or the Bento/Redpanda Connect adapter;
  delivery semantics are inherited from the hosted component and documented.
- **Community** — passes the conformance suite; maintained by the community.

Catalog items are placed in the release train below, prefixed **Catalog**.

---

## Shipped so far (v0.15 – v0.20)

The project restarted active maintenance in mid-2026. v0.15.0 through v0.19.0 shipped in July
2026; the August and September trains were missed. v0.20.0 was released on 2026-10-12. v0.20.1 is a
patch release cut from `release/v0.20.x`; items marked _v0.20.1_ are on that branch and are not
released until it is tagged.

### Revival

- [x] Triage all open issues and PRs — every item closed, merged, or labeled with a decision
- [x] **v0.15.0 stable** off the nightly train: dependency upgrades, security patches, bug fixes,
      Go version bump
- [x] Roadmap published; release milestones on GitHub
- [x] Governance doc: Apache-2.0 commitment, maintainer ladder, decision process
- [x] Foundational ADRs in `docs/architecture-decision-records/`: single-node engine, no bespoke
      DSL, WASM component model, local-state-only (scope since amended by ADR 20261008)
- [x] Community discussion moved to
      [GitHub Discussions](https://github.com/ConduitIO/conduit/discussions)

### First hour and first week

- [x] Install script (`curl https://conduitdata.io/install.sh | bash`), Homebrew, and single-binary
      downloads for all platforms; deb/rpm packages
- [x] `conduit init` and `conduit quickstart` — a working pipeline with zero manual config
- [x] `conduit pipelines init --template <name>` — template gallery (v0.19; five templates as of
      v0.20: `generator-log`, `generator-file`, `postgres-s3`, `postgres-cdc-kafka`,
      `postgres-pgvector-rag`)
- [x] Built-in UI, rebuilt and embedded in the engine binary (v0.18): live record flow, per-stage
      inspection, pipeline graph, start/stop
- [x] `conduit pipelines validate | lint | dry-run | inspect | deploy | apply | repair`
- [x] `conduit doctor` — environment and config diagnostics
- [x] Hot-reload of pipeline configs in dev mode (`conduit run --dev`)
- [x] `--json` structured output on every command
- [x] Deterministic, machine-actionable errors: error code + failing config path + suggested fix
- [x] Official Conduit MCP server: agents can scaffold, validate, deploy, inspect, and repair
      pipelines
- [x] `llms.txt` + single-page condensed documentation dump for LLM context, CI-enforced
- [x] `conduit generate "<natural language>"` — AI-assisted pipeline generation, **preview** (v0.20)
- [x] `conduit connector new` (Go) and `conduit processor new`

### Registry, AI pipelines and embedding

- [x] Signed connector registry: `conduit connectors install`/`uninstall`/`audit`/`bundle` (v0.18).
      The published index must be re-signed inside the client's 7-day freshness window or
      installs fail with `registry.index_stale`; an unattended freshness design is approved and
      lands with v0.21 clients
- [x] `conduit processor-plugins install` for WASM processors from the registry (v0.20)
- [x] Chunking and embedding processors (`conduit-processor-ai`: OpenAI, Voyage, Ollama, Cohere)
- [x] pgvector destination (`conduit-connector-pgvector` v0.1.0)
- [x] "Keep your RAG index fresh from Postgres": `postgres-pgvector-rag` template (v0.20; requires
      the `--preview.pipeline-arch-v2` engine)
- [x] WASM host egress for processors, with an SSRF guard and host-injected secrets (v0.20)
- [x] Stable Go library API for embedding Conduit — the root `github.com/conduitio/conduit` package
      with a pipelines-in-code builder, on a frozen import path, with an
      [embedding guide](https://conduitdata.io/docs/developing/embedding-go)

### Operations and correctness

- [x] Env-var configuration for all engine settings
- [x] `/healthz`, `/readyz` and a Prometheus metrics endpoint
- [x] Graceful SIGTERM drain (checkpoint, then exit)
- [x] systemd unit file for VM deployments
- [x] `conduit run --pipelines <dir>` — run a directory of pipeline configs (GitOps-friendly)
- [x] Official container image (`ghcr.io/conduitio/conduit`); releases and images signed with
      keyless cosign, an SBOM per archive (v0.20) and a signed SBOM attestation per platform image
      (_v0.20.1_)
- [x] Pipeline error recovery with retry/backoff (`pipelines.error-recovery.*`)
- [x] Destination-side dead-letter queues; Confluent Schema Registry with Avro
- [x] Chaos suite (SIGKILL mid-batch and mid-checkpoint) as a required CI check; Postgres CDC
      correctness properties under crash
- [x] Partition-claims protocol RFC accepted (design; the protocol itself ships in v0.24)

### v0.20.1 patch release (on `release/v0.20.x`, not yet tagged)

- [x] _v0.20.1_ Built-in Postgres CDC no longer loses rows on restart: interleaved transactions,
      `COPY` and multi-row inserts, and a replication slot confirmed past rows still in flight
      (built-in connector v0.14.3, `conduit-connector-postgres`#331). The first restart after the
      upgrade can re-deliver records once, and downgrading to v0.20.0 is not safe; the release notes
      say how
- [x] _v0.20.1_ Lifecycle: a failed status write no longer orphans a live run, skips cleanup, or lets
      one pipeline run twice. `Start` is refused while a config write is in progress. One failed
      connector state write no longer fails every connector that shared its write
- [x] _v0.20.1_ A source failing during stop no longer hangs shutdown
- [x] _v0.20.1_ `"***"` in a settings update keeps the stored secret only when nothing else in the
      update changes
- [x] _v0.20.1_ Built-in S3 connector v0.9.4: `aws.endpoint` and `aws.pathStyle` for MinIO and other
      S3-compatible stores
- [x] _v0.20.1_ Maintained Avro codec in the schema path, with a decode limit
      (`schema.avro.max-elements`); the built-in connectors move off the archived library in v0.21
- [x] _v0.20.1_ arch-v2 refuses destinations with `sdk.batch.size` above 1, which could stall, and
      fails the run on a connector state-write error
- Known issue carried into v0.21: secrets passed as `${VAR}` are stored in plaintext in Conduit's
  database (#2988). The fix is below.

---

## Release train

Each release has a theme. Items are listed in the release they target; anything that slips moves
to the next release rather than holding the train.

**How a release ships.** A release is cut from a release branch when its bar is met, not on a date.
For v0.21 the bar is: a certified connector for every output `conduit migrate kafka-connect` emits,
and two green nightlies on the release branch. A lane that misses the bar drops to the next release;
the release does not wait for it.

### v0.21 — Migrate from Kafka Connect, part 1

One complete story: every connector that `conduit migrate kafka-connect` emits is certified in this
release, and a migrated Postgres pipeline resumes from its Debezium offsets without a re-snapshot.
**Only Postgres resumes from imported offsets in v0.21. Migrations of the other connectors still
re-snapshot;** offset import for them is v0.22.

#### Migrate

- [ ] `conduit migrate kafka-connect` v1: reads Kafka Connect worker and connector configs and
      emits Conduit pipeline config plus a compatibility report. Never silently drops config it
      can't translate. Covers Debezium Postgres and MySQL, JDBC source and sink, S3 sink, Elasticsearch and
      OpenSearch sink, and Kafka→Kafka (MirrorMaker-style)
- [ ] `conduit kc lint` — lint Kafka Connect configs, including KIP-1188 override risks
- [ ] `conduit kc diff` — diff connector-config versions and check schema compatibility against the
      registry. Both `kc` commands are useful without switching runtimes
- [ ] **Postgres CDC, Debezium parity** (connector v0.15.0), certified
- [ ] **Debezium offset import for Postgres**, so a migrated Postgres pipeline resumes without a
      re-snapshot. v0.21 ships the fresh-slot strategy only: Conduit creates its own replication
      slot at the imported position. Reusing the Debezium slot is v0.22
- [ ] **Debezium-compatible output mode for Postgres**: envelope, topic naming, key schema, Schema
      Registry subject strategy, tombstones, decimal and time encodings — with a harness that diffs
      Conduit's output against real Debezium on the same database
- [ ] **MySQL CDC beta**, certified from the labs `mysql` connector
- [ ] **JDBC sink family** certified from the labs SQL connectors, with the Postgres and MySQL
      destinations; **JDBC source** mapped onto the existing Postgres and MySQL connectors
      (snapshot plus polling) and certified on that path. Anything that cannot be mapped is
      reported as approximated, and that is blocking
- [ ] **Elasticsearch** certified from labs, with OpenSearch
- [ ] **Kafka→Kafka** with header, partition and topic preservation, plus a Redpanda profile
- [ ] **S3 sink** format and partitioning parity with the Kafka Connect S3 sink, and the AWS default
      credential chain (`conduit-connector-s3`#1021, #1022; S3 destination benchi baseline: PR #3097)
- [ ] Migration harness: runs real Kafka Connect configs through `migrate` and diffs the output

#### Connector platform

- [ ] SDK schema-extraction speedup (`conduit-connector-sdk`#391)
- [ ] Acceptance suite v2, the basis for "Conduit Certified": pipelined writes, slow acks, mixed
      batches, crash and replay, and a throughput smoke test
- [ ] Go 1.26 toolchain moves across the connector fleet, and the archived Avro library is removed
      from every connector
      ([design](docs/design-documents/20260823-avro-codec-archived-decoder-advisories.md))
- [ ] Connector observability: source lag, CDC slot lag, and runbooks
- [ ] A connector scaffold (`conduit connector new`) that passes acceptance v2

#### Engine

- [ ] arch-v2 grows into the single engine (design #2971, under revision). The default flip and
      deletion of the v1 engine are v0.22
- [x] Groundwork: a simulated-connector test kit, `make bench-engine`, and a per-PR benchmark comment
- [ ] Adaptive batching by default. No throughput claim without a committed benchi run
- [ ] Destination ack reader
- [ ] Bounded retries (#2997) and bounded stop (#2990)
- [ ] Stall reasons in `inspect`
- [ ] Processor batch API, and a `stateless` declaration for processors
- [ ] The guarantees page and the life-of-a-record guide

#### Formats and security

- [ ] Protobuf decode for Confluent Schema Registry, PB-1 to PB-6
      ([design](docs/design-documents/20260823-protobuf-schema-support.md))
- [ ] Secrets passed as `${VAR}` are no longer stored in plaintext (#2988); existing databases get a
      migration or a re-provision path

#### Languages

- [ ] Before the Python GAs: one merged Python distribution with `conduit.connector` and
      `conduit.client` namespaces
- [ ] Python connector SDK GA, with `conduit connector new --lang python`
- [ ] Python embedded client GA
- [ ] SDK parity: spec and conformance groundwork (#2948)

#### Catalog

- [x] Add missing `LICENSE` files
- [x] Labs connector CI runs on the default branch
- [x] Archive the empty labs `conduit-processor-textgen` stub
- [x] Retire the labs `redpanda` connector in favour of the `kafka` connector (its Redpanda profile
      is in the Kafka→Kafka item above)
- [ ] List every labs connector that passes the acceptance suite in the registry as Community tier
      with license metadata (from 6 listed connectors to roughly 30)
- [x] Quality tiers (Certified / Verified / Adapter / Community) and a public parity scorecard
      seeded from the inventory
- [ ] Consolidate embeddings on `ai.embed`: deprecate the built-in OpenAI and Cohere embedding
      processors (#2957; removed in v0.23)
- [ ] Fold `enhanced-generator` and the labs `textgen` test-data intent into the built-in
      `generator` connector (#2959)

#### AI

- [ ] `conduit generate`: processor-aware generation (#2937)
- [ ] `conduit generate`: fix the intent-judge false positives (#2943)
- [ ] Live-eval numbers published from the weekly eval, reported as medians with the model and run
      ID. In-sample numbers are not a claim

#### Operations and contributors

- [ ] Registry web UI with search, verified badges and download stats (built; deployment pending)
- [ ] docker-compose quickstart, and `deploy/` examples: docker-compose, systemd, ECS task
      definition, Nomad job spec (examples, not supported products)
- [x] Coverage floor and benchmark-regression gates in CI — coverage ratchet (`.github/workflows/coverage.yml`) and
      the benchi regression gate (`.github/workflows/bench-engine.yml`: >10% throughput regression beyond the A/A
      floor fails the build). Both are live and blocking; see the Process-maturity table in `CLAUDE.md` for what is
      and is not yet a required context
- [ ] An engine section in `CONTRIBUTING.md`
- [ ] Reliability follow-ups: standalone WASM processor timeout and CPU-loop containment (#2998,
      #3001), serialized config writes (#2992), bounded status writes on badger (#2964), and
      `Source.Read` honouring its context (#2949)
- [ ] Looking for three teams migrating off Kafka Connect to work with us as design partners — open
      a [discussion](https://github.com/ConduitIO/conduit/discussions)

### v0.22 — Migrate from Kafka Connect, part 2: zero-downtime, on one engine

- [ ] SMT compatibility pack: processor equivalents for the ~12 most-used Kafka Connect SMTs,
      mapped automatically by `migrate` — including the five missing today (Flatten,
      ExtractChangedRecordState, EventRouter, PartitionRouting, TimezoneConverter) and typed values
      for `field.set`
- [ ] Postgres offset import by reusing the Debezium replication slot (v0.21 ships the fresh-slot
      strategy only)
- [ ] Offset import for the other `migrate` outputs (MySQL and the rest), from Debezium offsets and
      `connect-offsets`, so they resume without a re-snapshot as Postgres does from v0.21
- [ ] **Debezium-compatible output mode for MySQL**, using the Postgres diff harness from v0.21
      against real Debezium on the same database (PR #3076 data model + JsonConverter)
- [ ] **Default engine flip and v1 engine deletion**: arch-v2 ships as `preview` in v0.21; this flips
      it to the default and removes the v1 engine, gated on the cross-engine benchi harness proving
      parity-or-faster at shipped defaults (PRs #2956/#3067). The #2978 staged batch engine and
      #3184 ProcessBatch land first as the performance substrate.
- [ ] **Protocol v2 deprecation:** warn on protocol v2 negotiation, announced in v0.21; protocol
      v2 is removed no earlier than v0.24
- [ ] NATS JetStream source and destination (certified from labs `nats-jetstream`)
- [ ] **Catalog:** Bento/Redpanda Connect adapter (preview) — a new build with ack propagation and
      resumable positions; the 2022 prototype acknowledges before the write is durable and is a
      reference only. Hosts inputs and outputs, not Bloblang
- [ ] Apache Iceberg destination beta — a new Go-native build (the labs Java connector is a behavior
      reference only): upserts, compaction-friendly writes, REST/Glue/Nessie catalogs — operational
      database to lakehouse in real time, no Kafka required
- [ ] Helm chart: Deployment/StatefulSet, pipeline configs via ConfigMap or git-sync,
      ServiceMonitor — static pipeline-to-instance assignment before the operator exists
- [ ] Secrets: Vault, AWS and GCP KMS, Kubernetes secrets (PR #3128 secretref is the first slice)
- [ ] Source-side dead-letter queue
- [ ] OpenTelemetry metrics and prebuilt Grafana dashboards, with metrics covering what Kafka
      Connect operators alert on: lag, task and pipeline status, error rate
- [ ] JSON Schema support in the Schema Registry integration
- [ ] **Catalog:** GCS and Azure Blob with Parquet, as object-storage family members alongside S3

### v0.23 — Drop-in

- [ ] Kafka Connect REST API compatibility: `/connectors` create, config, status, pause, resume,
      restart — so Strimzi `KafkaConnector` resources, Kafka Connect Terraform providers and Kafka UI
      keep working
- [ ] **Active/passive HA on Kubernetes**: lease, failover, resume from checkpoint, chaos-tested
- [ ] Kafka Connect connector JAR host (preview): an opt-in JVM sidecar over the plugin protocol,
      never inside the engine, built on the existing Kafka Connect wrapper. Meanwhile it is the path
      to Debezium-grade CDC for SQL Server, Oracle, Db2 and HANA, whose current connectors are
      trigger-based
- [ ] **Catalog:** certify Snowflake, ClickHouse and Redis from labs; the `mysql-snowflake` and
      `kafka-clickhouse` templates ship with their certified paths
- [ ] **Catalog:** BigQuery destination (new build) with the warehouse family
- [ ] **Catalog:** DuckDB / MotherDuck destination with the warehouse family: upsert and delete by
      source key, schema evolution. Ships as a standalone gRPC connector so the DuckDB CGO driver
      never enters the engine binary. A Postgres CDC → DuckDB/MotherDuck template is part of its
      definition of done. (The v0.22 Parquet object-storage and Iceberg outputs are already
      directly queryable by DuckDB.)
- [ ] **Catalog:** Bento/Redpanda Connect adapter GA
- [ ] **Catalog:** built-in OpenAI and Cohere embedding processors removed; use `ai.embed`
- [ ] MySQL CDC GA
- [ ] Rust SDK (preview): gRPC connectors and WASM processors on the existing processor ABI, with
      `conduit connector new --lang rust`
- [ ] ADR: WASM connector/component host choice (supersedes ADR 20260722 in part).
      WIT/component-model processors depend on it
- [ ] TypeScript embedded client
- [ ] Kafka Queues (share groups) source mode
- [ ] OpenLineage events
- [ ] Published, reproducible [benchi](https://github.com/ConduitIO/benchi) results vs Kafka Connect
- [ ] Profiling as a CI gate
- [ ] **Catalog:** HTTP/webhooks on the HTTP family core — a rebuilt source whose response waits for
      the ack, plus a destination
- [ ] Open connector-protocol spec and acceptance suite (v2, from v0.21) published as a certification
      ("Conduit Certified")

### v0.24 — Production at scale

- [ ] Kubernetes operator (Apache-2.0): `Pipeline` CRD, import of Strimzi resources, bin-packing of
      pipelines across pods, health-based rescheduling, lag-based autoscaling
- [ ] MongoDB CDC (certified from labs `mongo`) and SQL Server CDC (the trigger-based labs
      connector certified as an interim; log-based capture later)
- [ ] **Catalog:** certify Kinesis, SQS and Google Pub/Sub from labs; MQTT (new build) with the
      messaging family
- [ ] **Catalog:** certify Salesforce, Stripe and HubSpot from labs (three of five native SaaS
      targets)
- [ ] Apache Iceberg destination GA, with the `postgres-iceberg` template
- [ ] Checkpoint-aware rolling upgrades in the operator (drain → checkpoint → reschedule)
- [ ] Engine-wide schema contracts and drift policy — halt, DLQ or evolve on drift, generalizing
      the Postgres connector's policy, surfaced in the UI
- [ ] **Catalog:** SNS with the messaging family
- [ ] OpenTelemetry traces
- [ ] Exactly-once Kafka destination (transactional), and documented delivery semantics for every
      source/destination pair
- [ ] Community publishing to the registry (GitHub Action + signing) and private registries
- [ ] **Partition-claims protocol** shipped in the connector protocol
- [ ] **Protocol v2 removed**, no earlier than this release (announce v0.21 → warn v0.22 → remove
      v0.24)
- [ ] **Replay and backfill as first-class verbs**: `conduit pipeline replay --from <position>`,
      snapshot re-runs, offset inspection and reset in CLI and UI
- [ ] `conduit connector generate --from-openapi <spec>` — connector scaffolding from API specs
- [ ] gRPC out-of-process processor runtime and the Python processor SDK; the WASM-based
      `conduit-processor-sdk-python` is archived
- [ ] Processor protocol spec (protobuf + WIT) published beside the connector protocol spec
- [ ] SDK conformance suite and public parity matrix
- [ ] Not before this release: Node.js client, Qdrant destination (a new build; the repository
      is empty)

### v0.25 — State foundations

- [ ] State layer per
      [ADR 20261008](docs/architecture-decision-records/20261008-state-layer-scope-windows-and-bounded-lateness.md):
      embedded KV, partition-scoped, every state write atomic with the pipeline checkpoint
- [ ] Processor state API (get/put/TTL/timers) in Go, Python, TypeScript and Rust at once, on both
      processor runtimes
- [ ] Deduplication with TTL
- [ ] Stream-table joins: lookup/enrichment tables kept current from CDC or a topic
- [ ] Kill-mid-write chaos tests for every state feature
- [ ] Clear documentation of what the state layer is and isn't
- [ ] Arrow columnar record spike, gated on the cross-engine benchmark harness
- [ ] Java and C# embedded clients (generated gRPC bindings)
- [ ] Kafka consumer-group parallelism right after partition claims ship (full scheduler-driven
      parallelism is v0.30+)
- [ ] Terraform provider for the Conduit API (pipelines, connectors, processors as resources);
      until then, Connect REST API compatibility (v0.23) keeps existing Kafka Connect Terraform
      providers working
- [ ] **Catalog:** Shopify and GitHub connectors (new builds) on the HTTP/SaaS family core
- [ ] **Catalog:** log-based SQL Server CDC begins (v0.25 or later)
- [ ] Java SDK begins, informed by the labs Java SDK proof of concept: gRPC connectors and gRPC
      processors, including the state API (completes in v0.26)
- [ ] TypeScript connectors over gRPC, with `conduit connector new --lang ts` (full tier — pulled
      forward from v0.26 so full-tier TS and enterprise-tier Java land in the same release)

### v0.26 — Curate

- [ ] Keyed upsert and entity merge across sources
- [ ] Data-quality checks with quarantine to the DLQ
- [ ] PII redaction GA
- [ ] Schema harmonization across sources
- [ ] Curated Iceberg output with OpenLineage
- [ ] **Catalog:** Databricks/Delta Lake
- [ ] Templates such as "unify customers from Postgres, Salesforce and events"
- [ ] Pipelines-as-code builders in Python and TypeScript
- [ ] One-call local mode for non-Go embedded clients
- [ ] Java SDK complete (connectors, processors, state API), with `conduit connector new --lang java`

### v0.27 — Windows and aggregations

- [ ] Tumbling, sliding and session windows, built crash-safe on the state API (the existing
      `aggregate` prototype is a reference only)
- [ ] Processing time first; event time with **bounded lateness only** — late records go to the
      DLQ or a late-data output per policy, never silently dropped
- [ ] Aggregates: count, sum, min, max, avg, distinct (HLL), top-K, last
- [ ] Emit on window close, with optional early updates
- [ ] Published benchmark vs Kafka Streams for equivalent jobs
- [ ] C# SDK (gRPC connectors and processors, state API), scoped with early adopters

### v0.28 — AI on streams

- [ ] `ai.extract`, `ai.classify`, `ai.summarize` with schema-bound structured output, batching,
      rate limits and model routing
- [ ] One provider-pluggable text-generation processor in place of today's per-provider ones
- [ ] **Catalog:** certify Pinecone from labs; build a Turbopuffer destination
- [ ] Cost controls: token budgets, sampling
- [ ] LLM results cached by input hash, so replay is deterministic and cheap
- [ ] Windowed summarization
- [ ] Agent triggers (MCP, webhook, A2A) with human-in-the-loop routing
- [ ] **Read-only state lookups by key over the API and MCP** — agents read live context; no SQL,
      no scans

### v0.29 — Keep models fresh

- [ ] Embedding-model migration in one command: replay → dual-write a versioned index → verify →
      cut over
- [ ] Online feature-store destinations
- [ ] Versioned training and evaluation-set snapshots on Iceberg, with lineage
- [ ] Drift monitors: windowed aggregates over model inputs and outputs, with alerts

### v0.30+ — Scale and partners

- [ ] Keyed state across instances via partition claims
- [ ] Hot-pipeline parallelism: the scheduler assigns partition claims so one pipeline runs across
      several instances
- [ ] Reference architectures with RisingWave, Materialize and ClickHouse for stream-stream joins,
      large-state joins and complex event time — first-class ingest and egress for each
- [ ] Fleet console (open source core): registers many Conduit instances; fleet-wide visibility,
      health and versions; GitOps-native (the console reads state, pipeline config stays in git);
      shares a scheduling brain with the operator; rolling upgrades across a fleet

---

## Later

Kept on the list, not scheduled in a release yet:

- Public GitHub Project board
- CNCF Sandbox application
- Monthly community call on a public calendar
- `shopify-warehouse` template; community-contributed templates with the same publishing flow as
  connectors
- WASM connectors and WIT/component-model adoption beyond what the v0.23 host-choice ADR decides
  (deferred by [ADR 20260722](docs/architecture-decision-records/20260722-wasm-component-model-deferred.md))
- WASM support for Java and C# processors and connectors, once their component-model toolchains
  are production-grade (re-evaluated yearly)
- Official SDKs beyond the six official languages — community tier via the conformance kit; promoted
  only on demand
- Log-based CDC for Oracle, Db2 and HANA — they stay on the Debezium engine through the JAR host
  until there is demand
- Evaluate a DuckDB source and DuckLake support, on demand
- Production reference architectures beyond the streaming SQL partners

## Documentation (parallel track)

- [ ] Getting-started rewrite around the 5-minute path; per-broker quickstarts (Kafka, NATS,
      Redpanda, Hazelcast, none)
- [ ] "Migrating from Kafka Connect": concept mapping (worker→instance, task→pipeline,
      SMT→processor, converter→schema), per-connector guides for the top 10 Kafka Connect
      connectors — growing with `migrate` from v0.21
- [ ] "AI data pipelines with Conduit": RAG sync, embedding pipelines, vector store patterns, and
      Conduit as the data layer for AI applications
- [ ] Connector development tutorial per official language (Go, Python, TypeScript, Rust, Java, C#)
- [ ] Processor cookbook: 30+ copy-paste recipes
- [ ] Embedding guide per language, stating plainly that only Go embeds in-process
- [ ] Honest comparison pages: vs Kafka Connect, vs Redpanda Connect / Bento, vs Flink (when you
      need it, when you don't), vs batch ELT tools
- [ ] Architecture deep-dive: ordering guarantees, end-to-end ack propagation, delivery
      semantics, state and checkpointing model
- [x] `llms.txt` and LLM-optimized doc formats maintained alongside human docs

## UI note

Conduit's historical UI was Ember-based and later de-emphasized. The built-in UI was rebuilt from
scratch and ships embedded in the engine (v0.18): it observes and operates pipelines on the same API
the CLI and MCP server use, and config-as-code stays the source of truth. We keep the built-in UI
minimal. Later UI surfaces: replay and offset management alongside the v0.24 replay verbs,
schema-drift visibility (v0.24), the registry web UI (v0.21), and the fleet console (v0.30+).
Deeper org-scale console features are the commercial product.

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

Where the two meet: open source Conduit emits lineage events (OpenLineage) and performs per-pipeline
PII redaction; the commercial offering is the org-level lineage graph and centrally governed policy
packs.

**The one-way ratchet:** nothing shipped as open source will ever be moved behind a paywall.
Commercial features may become open source over time; the reverse never happens.

---

## How to contribute

- Check the [GitHub Project board](https://github.com/orgs/ConduitIO/projects) for issues labeled
  `good first issue` and `help wanted`
- Build a connector — the scaffolding makes it a weekend project, and the registry gets it
  distributed
- Contribute a pipeline template — the gallery is community-driven
- Join the conversation in [GitHub Discussions](https://github.com/ConduitIO/conduit/discussions)
- Everything here is open for discussion; open an issue against this roadmap

**North-star metrics we hold ourselves to:** time-to-first-pipeline < 5 minutes ·
time-to-first-custom-connector < 30 minutes · an AI agent can go from zero to a running pipeline
using only the MCP server and llms.txt · monthly release cadence · zero untriaged issues older
than 14 days.
