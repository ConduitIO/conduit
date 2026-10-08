# Connector and processor catalog inventory

As of 2026-10-08. This is a point-in-time inventory of every connector, processor and adapter that already exists
for Conduit, so that new catalog work promotes and certifies what is there instead of rebuilding it. It covers
`ConduitIO` and `conduitio-labs` on GitHub, the built-in processors on `main`, and the standalone processors that
live outside both organizations.

A machine-readable copy of the connector table, one row per repository, is in
[`docs/data/catalog-inventory.csv`](data/catalog-inventory.csv). It is meant to seed a future connector scorecard;
regenerate it rather than hand-editing it.

The public connector list for users stays on the documentation site
(<https://conduitdata.io/docs/using/connectors/list/>). This page is for maintainers planning catalog work.

## Summary

- **78 `conduit-connector-*` repositories** are connectors or connector prototypes: 9 in `ConduitIO`, 69 in
  `conduitio-labs`. None is archived. 72 are Go, 4 are Java (an SDK proof of concept, two SDK examples and
  `s3-iceberg`), 2 contain no code (`qdrant`, `socket`). 61 have a source, 57 a destination, 42 both.
- **Six connectors are in the registry index**: `file`, `generator`, `kafka`, `log`, `postgres`, `pgvector`, plus
  the `ai.chunk` and `ai.embed` processors. The built-in `s3` connector is not in the registry index.
- **Maintenance**: 9 connectors have had a human commit since the June 2026 restart (the 7 `ConduitIO` connectors
  with code, plus `mongo` and `mysql` in labs). 53 receive only automated dependency updates. 15 are stale or
  dormant (connector SDK older than v0.12, or no commits since 2024 or earlier).
- **SDK drift**: 27 connectors pin the current connector SDK (v0.14.x), 35 pin v0.12–v0.13, 10 pin something older.
- **Tests**: 37 call `sdk.AcceptanceTest`; 35 ship a docker-compose integration harness. The built-in `postgres`,
  `s3`, `generator` and `log` connectors and labs `mysql`, `salesforce`, `dynamodb` and `http` have no acceptance
  test.
- **CI on labs is weaker than it looks**: labs test workflows run on pull requests only, and dependabot merges do
  not trigger push builds, so no labs connector except `mongo` and `mysql` has a recent test run on its default
  branch. Tests that need cloud credentials skip themselves when the secret is absent, which is always the case on
  dependabot pull requests. 8 connectors have a failing latest test run.
- **Licensing**: 30 repositories have no `LICENSE` file that GitHub can detect, including the built-in `log`
  connector and labs `mysql`. Most carry Apache-2.0 headers in their source files.
- **Change capture**: 25 connectors read changes after an initial snapshot. Only 6 do it from a database log or
  native change stream (`postgres`, `mysql`, `mongo`, `vitess`, `dynamodb`, `spanner`); 5 use triggers or tracking
  tables (`sql-server`, `oracle`, `db2`, `sap-hana`, and Snowflake streams); the rest poll.
- **Real gaps** (nothing exists): a Go Iceberg destination, a BigQuery destination, an OpenSearch destination,
  Shopify and GitHub sources, MQTT, a Qdrant destination (the repository is a placeholder), and a crash-safe
  windowed aggregation.
- **Biggest reuse opportunities**: the Kafka Connect wrapper for the v0.23 JAR host; `mysql`, `mongo` and the
  labs SQL connectors for the CDC and JDBC golden paths; `salesforce`, `stripe` and `hubspot` for three of the five
  SaaS targets; the built-in processor set for most Kafka Connect SMTs.
- **Overlaps to consolidate**: three embedding implementations, four text-generation paths, `kafka` and
  `redpanda`, `generator` and `enhanced-generator`, the 2022 `benthos` prototype versus a new Bento adapter, the
  Java SDK proof of concept versus the planned Java SDK, and the out-of-tree `aggregate` processor versus the
  planned windowing work.

## Method and caveats

Everything below was read from live sources on 2026-10-08:

- Repository list, archive state, license and default branch: `gh repo list` for both organizations.
- Connector facts: a shallow clone of every repository. Source and destination support come from the
  `sdk.Connector{}` literal; SDK and Go versions from `go.mod`; acceptance tests from a search for
  `AcceptanceTest(` in Go files; integration harness from the presence of a docker-compose file; change-capture
  mechanism from each README and `connector.yaml`.
- Tags: `git ls-remote --tags` (semver tags only).
- CI: the latest run of each test or build workflow through the Actions API. For labs this is almost always a
  dependabot pull request, not the default branch.
- Last human commit: the newest commit on the default branch whose author is not a bot.
- Registry: `ConduitIO/conduit-connector-registry` `index/connectors/*.json` and `index/processors/*.json`.
- Built-in processors: `pkg/plugin/processor/builtin/registry.go` on `main`.

"Acceptance test present" means the repository calls the SDK acceptance suite. It does not mean the suite runs in
CI against a real system: for SaaS and cloud connectors it usually skips without credentials. "Certified" in this
document means acceptance suite plus integration test against the real system, both running in CI; a kill -9
chaos test; a committed benchi configuration with a recorded run; and user docs covering configuration, delivery
semantics and an example pipeline.

## Connectors

Legend: S = source, D = destination, Acc = calls `sdk.AcceptanceTest`, Int = docker-compose integration harness,
Reg = in the registry index, State: _active_ = human commit since 2026-06-01, _deps-only_ = only automated
dependency updates since, _stale_ = connector SDK older than v0.12, _dormant_ = no meaningful commit since 2024
or earlier or a prototype, _placeholder_ = no code.

### Core connectors (`ConduitIO`)

| Connector | S | D | Change capture | SDK | Acc | Int | CI (latest test run) | Latest tag | Reg | License | Last human commit | State |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| [ephemeris](https://github.com/ConduitIO/conduit-connector-ephemeris) | Y | - | - | v0.2.0 | - | - | none | - | - | none | 2022-07-28 | dormant |
| [file](https://github.com/ConduitIO/conduit-connector-file) (builtin) | Y | Y | - | v0.14.1 | Y | - | success 2026-07-23 | v0.10.8 | Y | Apache-2.0 | 2026-07-23 | active |
| [generator](https://github.com/ConduitIO/conduit-connector-generator) (builtin) | Y | - | - | v0.14.1 | - | - | success 2026-07-23 | v0.10.6 | Y | Apache-2.0 | 2026-07-23 | active |
| [kafka](https://github.com/ConduitIO/conduit-connector-kafka) (builtin) | Y | Y | - | v0.14.1 | Y | Y | success 2026-07-23 | v0.12.5 | Y | Apache-2.0 | 2026-07-23 | active |
| [log](https://github.com/ConduitIO/conduit-connector-log) (builtin) | - | Y | - | v0.14.1 | - | - | success 2026-07-23 | v0.7.5 | Y | none | 2026-07-23 | active |
| [pgvector](https://github.com/ConduitIO/conduit-connector-pgvector) | - | Y | - | v0.14.1 | Y | Y | success 2026-09-11 | v0.1.0 | Y | Apache-2.0 | 2026-08-24 | active |
| [postgres](https://github.com/ConduitIO/conduit-connector-postgres) (builtin) | Y | Y | log (logical replication) | v0.14.1 | - | Y | success 2026-10-08 | v0.14.3 | Y | Apache-2.0 | 2026-08-31 | active |
| [qdrant](https://github.com/ConduitIO/conduit-connector-qdrant) | - | Y | - | - | - | - | none | - | - | Apache-2.0 | 2026-07-26 | placeholder |
| [s3](https://github.com/ConduitIO/conduit-connector-s3) (builtin) | Y | Y | polling bucket | v0.14.1 | - | Y | success 2026-10-08 | v0.9.4 | - | Apache-2.0 | 2026-10-08 | active |

Notes:

- `file`, `generator`, `kafka`, `log`, `postgres` and `s3` are compiled into the Conduit binary
  (`pkg/plugin/connector/builtin/registry.go`).
- `postgres` has a connector-local SIGKILL chaos harness (`test/chaos`) that runs on every pull request and nightly;
  it is the only connector with crash testing against the real system. It does not call the SDK acceptance suite.
- `pgvector` is exercised end to end by the required `rag-e2e` check in this repository.
- `ephemeris` is a 2022 prototype on SDK v0.2.0. `qdrant` holds only a README and license.

### Labs connectors (`conduitio-labs`)

| Connector | S | D | Change capture | SDK | Acc | Int | CI (latest test run) | Latest tag | Reg | License | Last human commit | State |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| [activemq-artemis](https://github.com/conduitio-labs/conduit-connector-activemq-artemis) | Y | Y | - | v0.14.1 | Y | Y | success 2026-01-06 | v0.1.1 | - | none | 2025-06-24 | deps-only |
| [activemq-classic](https://github.com/conduitio-labs/conduit-connector-activemq-classic) | Y | Y | - | v0.14.1 | Y | Y | success 2026-01-06 | - | - | none | 2025-04-22 | deps-only |
| [airtable](https://github.com/conduitio-labs/conduit-connector-airtable) | Y | - | - | v0.2.1-0 | - | - | none | - | - | Apache-2.0 | 2022-09-26 | dormant |
| [algolia](https://github.com/conduitio-labs/conduit-connector-algolia) | - | Y | - | v0.12.0 | - | - | success 2026-02-23 | v0.3.0 | - | none | 2025-03-06 | deps-only |
| [azure-event-hub](https://github.com/conduitio-labs/conduit-connector-azure-event-hub) | Y | Y | - | v0.12.0 | - | Y | none | - | - | none | 2025-03-12 | deps-only |
| [azure-storage](https://github.com/conduitio-labs/conduit-connector-azure-storage) | Y | - | polling container | v0.12.0 | Y | Y | failure 2026-01-14 | v0.4.1 | - | Apache-2.0 | 2025-04-15 | deps-only |
| [benthos](https://github.com/conduitio-labs/conduit-connector-benthos) | Y | Y | - | v0.2.1-0 | - | - | none | - | - | Apache-2.0 | 2022-09-16 | dormant |
| [bigquery](https://github.com/conduitio-labs/conduit-connector-bigquery) | Y | - | polling incrementing column | v0.12.0 | Y | - | success 2026-03-16 | v0.3.0 | - | Apache-2.0 | 2025-03-06 | deps-only |
| [box](https://github.com/conduitio-labs/conduit-connector-box) | - | Y | - | v0.14.1 | - | Y | success 2026-01-06 | v0.1.1 | - | none | 2025-06-09 | deps-only |
| [cassandra](https://github.com/conduitio-labs/conduit-connector-cassandra) | - | Y | - | v0.12.0 | - | Y | success 2026-01-05 | v0.1.1 | - | Apache-2.0 | 2025-03-12 | deps-only |
| [chaos](https://github.com/conduitio-labs/conduit-connector-chaos) | Y | Y | - | v0.14.1 | - | - | success 2026-01-06 | v0.2.0 | - | none | 2025-02-12 | deps-only |
| [clickhouse](https://github.com/conduitio-labs/conduit-connector-clickhouse) | Y | Y | polling ordering column (inserts only) | v0.12.0 | Y | - | success 2026-08-25 | v0.1.0 | - | Apache-2.0 | 2025-03-11 | deps-only |
| [cosmos-nosql](https://github.com/conduitio-labs/conduit-connector-cosmos-nosql) | Y | - | polling (inserts only) | v0.12.0 | - | - | success 2026-03-20 | - | - | Apache-2.0 | 2025-03-12 | deps-only |
| [databricks](https://github.com/conduitio-labs/conduit-connector-databricks) | - | Y | - | v0.12.0 | - | - | success 2026-01-06 | v0.1.1 | - | none | 2025-03-06 | deps-only |
| [db2](https://github.com/conduitio-labs/conduit-connector-db2) | Y | Y | trigger + tracking table | v0.12.0 | Y | Y | success 2025-11-21 | - | - | Apache-2.0 | 2025-03-06 | deps-only |
| [discord](https://github.com/conduitio-labs/conduit-connector-discord) | Y | - | - | v0.6.0 | - | Y | none | - | - | Apache-2.0 | 2025-03-11 | stale |
| [dropbox](https://github.com/conduitio-labs/conduit-connector-dropbox) | Y | Y | API polling | v0.14.1 | - | - | success 2026-01-06 | v0.1.1 | - | none | 2025-06-03 | deps-only |
| [dynamodb](https://github.com/conduitio-labs/conduit-connector-dynamodb) | Y | Y | log (DynamoDB Streams) | v0.14.1 | - | Y | success 2026-05-29 | v0.4.3 | - | Apache-2.0 | 2025-07-23 | deps-only |
| [elasticsearch](https://github.com/conduitio-labs/conduit-connector-elasticsearch) | Y | Y | - | v0.12.0 | Y | Y | failure 2026-05-12 | v0.4.0 | - | Apache-2.0 | 2025-03-06 | deps-only |
| [enhanced-generator](https://github.com/conduitio-labs/conduit-connector-enhanced-generator) | Y | - | - | v0.14.1 | - | - | failure 2026-01-06 | v0.9.7 | - | Apache-2.0 | 2025-02-27 | deps-only |
| [file-java-poc](https://github.com/conduitio-labs/conduit-connector-file-java-poc) | - | Y | - | - | - | - | none | - | - | none | 2023-05-12 | dormant |
| [firebolt](https://github.com/conduitio-labs/conduit-connector-firebolt) | Y | Y | - | v0.7.2 | Y | - | success 2026-10-08 | - | - | Apache-2.0 | 2025-03-12 | stale |
| [gcp-pubsub](https://github.com/conduitio-labs/conduit-connector-gcp-pubsub) | Y | Y | - | v0.12.0 | Y | - | success 2026-03-31 | - | - | Apache-2.0 | 2025-03-12 | deps-only |
| [generator-java](https://github.com/conduitio-labs/conduit-connector-generator-java) | Y | - | - | - | - | - | none | - | - | none | 2023-05-12 | dormant |
| [google-cloudstorage](https://github.com/conduitio-labs/conduit-connector-google-cloudstorage) | Y | - | polling bucket | v0.12.0 | Y | - | success 2026-01-05 | v0.3.0 | - | Apache-2.0 | 2025-03-06 | deps-only |
| [google-drive](https://github.com/conduitio-labs/conduit-connector-google-drive) | - | Y | - | v0.14.1 | - | Y | success 2026-03-16 | v0.1.0 | - | none | 2025-05-08 | deps-only |
| [google-sheets](https://github.com/conduitio-labs/conduit-connector-google-sheets) | Y | Y | - | v0.12.0 | Y | - | success 2026-07-29 | v0.3.0 | - | Apache-2.0 | 2025-03-13 | deps-only |
| [grpc-client](https://github.com/conduitio-labs/conduit-connector-grpc-client) | - | Y | - | v0.12.0 | - | - | success 2026-08-03 | v0.1.0 | - | Apache-2.0 | 2025-03-11 | deps-only |
| [grpc-server](https://github.com/conduitio-labs/conduit-connector-grpc-server) | Y | - | - | v0.12.0 | - | - | failure 2026-04-01 | v0.1.0 | - | Apache-2.0 | 2025-03-11 | deps-only |
| [http](https://github.com/conduitio-labs/conduit-connector-http) | Y | Y | - | v0.14.1 | - | - | success 2026-01-06 | v0.4.0 | - | Apache-2.0 | 2025-06-03 | deps-only |
| [http-server](https://github.com/conduitio-labs/conduit-connector-http-server) | Y | - | - | v0.8.0 | - | - | none | - | - | Apache-2.0 | 2025-03-11 | stale |
| [hubspot](https://github.com/conduitio-labs/conduit-connector-hubspot) | Y | Y | API polling | v0.12.0 | Y | - | success 2026-03-31 | v0.1.0 | - | Apache-2.0 | 2025-03-11 | deps-only |
| [hyperion-x55](https://github.com/conduitio-labs/conduit-connector-hyperion-x55) | Y | - | - | v0.7.1 | - | - | none | - | - | none | 2025-03-11 | stale |
| [influxdb](https://github.com/conduitio-labs/conduit-connector-influxdb) | Y | Y | - | v0.14.1 | - | Y | success 2026-01-05 | v0.1.0 | - | none | 2025-05-28 | deps-only |
| [java-sdk-poc](https://github.com/conduitio-labs/conduit-connector-java-sdk-poc) | - | - | - | - | - | - | none | v0.1.0 | - | none | 2024-06-10 | dormant |
| [k8s-events](https://github.com/conduitio-labs/conduit-connector-k8s-events) | Y | - | - | v0.7.1 | - | - | none | - | - | none | 2025-03-11 | stale |
| [kinesis](https://github.com/conduitio-labs/conduit-connector-kinesis) | Y | Y | - | v0.14.1 | Y | Y | success 2026-09-28 | v0.2.0 | - | none | 2025-04-22 | deps-only |
| [marketo](https://github.com/conduitio-labs/conduit-connector-marketo) | Y | - | API polling | v0.12.0 | Y | - | none | v0.3.0 | - | Apache-2.0 | 2025-01-08 | deps-only |
| [materialize](https://github.com/conduitio-labs/conduit-connector-materialize) | - | Y | - | v0.12.0 | Y | Y | success 2026-03-31 | v0.3.0 | - | Apache-2.0 | 2025-03-12 | deps-only |
| [mongo](https://github.com/conduitio-labs/conduit-connector-mongo) | Y | Y | log (change streams) | v0.14.1 | Y | Y | success 2026-08-05 | v0.3.0 | - | Apache-2.0 | 2026-08-05 | active |
| [mysql](https://github.com/conduitio-labs/conduit-connector-mysql) | Y | Y | log (binlog) | v0.14.1 | - | Y | success 2026-09-07 | v0.3.0 | - | none | 2026-08-05 | active |
| [nats-jetstream](https://github.com/conduitio-labs/conduit-connector-nats-jetstream) | Y | Y | - | v0.12.0 | Y | Y | failure 2026-02-24 | v0.3.1 | - | Apache-2.0 | 2025-03-11 | deps-only |
| [nats-pubsub](https://github.com/conduitio-labs/conduit-connector-nats-pubsub) | Y | Y | - | v0.12.0 | Y | Y | success 2026-05-07 | v0.4.0 | - | Apache-2.0 | 2025-03-06 | deps-only |
| [neo4j](https://github.com/conduitio-labs/conduit-connector-neo4j) | Y | Y | - | v0.12.0 | Y | Y | success 2026-01-06 | v0.1.0 | - | Apache-2.0 | 2025-03-06 | deps-only |
| [notion](https://github.com/conduitio-labs/conduit-connector-notion) | Y | - | - | v0.7.2 | - | - | success 2026-01-06 | v0.4.0 | - | none | 2025-03-12 | stale |
| [openai-vectorstore](https://github.com/conduitio-labs/conduit-connector-openai-vectorstore) | - | Y | - | v0.14.1 | - | - | success 2026-01-06 | v0.1.0 | - | none | 2025-04-22 | deps-only |
| [oracle](https://github.com/conduitio-labs/conduit-connector-oracle) | Y | Y | trigger + tracking table | v0.12.0 | Y | Y | success 2026-02-23 | v0.0.0 | - | Apache-2.0 | 2025-03-06 | deps-only |
| [pinecone](https://github.com/conduitio-labs/conduit-connector-pinecone) | - | Y | - | v0.12.0 | - | - | success 2026-02-23 | v0.2.0 | - | none | 2025-01-14 | deps-only |
| [pulsar](https://github.com/conduitio-labs/conduit-connector-pulsar) | Y | Y | - | v0.14.1 | Y | Y | success 2026-02-23 | v0.1.0 | - | Apache-2.0 | 2025-04-22 | deps-only |
| [rabbitmq](https://github.com/conduitio-labs/conduit-connector-rabbitmq) | Y | Y | - | v0.14.1 | Y | Y | success 2026-01-05 | v0.4.0 | - | none | 2025-05-19 | deps-only |
| [redis](https://github.com/conduitio-labs/conduit-connector-redis) | Y | Y | polling key/stream | v0.12.0 | Y | - | success 2026-09-07 | v0.3.0 | - | Apache-2.0 | 2025-03-12 | deps-only |
| [redpanda](https://github.com/conduitio-labs/conduit-connector-redpanda) | Y | Y | - | v0.12.0 | Y | Y | failure 2026-07-06 | v0.1.0 | - | Apache-2.0 | 2025-03-06 | deps-only |
| [redshift](https://github.com/conduitio-labs/conduit-connector-redshift) | Y | Y | polling ordering column | v0.11.0 | Y | - | none | - | - | Apache-2.0 | 2024-12-04 | stale |
| [rsync](https://github.com/conduitio-labs/conduit-connector-rsync) | Y | Y | - | v0.14.1 | - | Y | success 2026-01-05 | - | - | none | 2025-06-26 | deps-only |
| [s3-iceberg](https://github.com/conduitio-labs/conduit-connector-s3-iceberg) | - | Y | - | - | - | Y | failure 2025-11-24 | - | - | none | 2024-07-03 | dormant |
| [salesforce](https://github.com/conduitio-labs/conduit-connector-salesforce) | Y | Y | Pub/Sub API (platform events) | v0.14.1 | - | - | success 2026-07-16 | v0.5.4 | - | Apache-2.0 | 2025-07-03 | deps-only |
| [sap-hana](https://github.com/conduitio-labs/conduit-connector-sap-hana) | Y | Y | trigger + tracking table | v0.12.0 | Y | - | success 2026-10-08 | v0.1.0 | - | Apache-2.0 | 2025-03-13 | deps-only |
| [sftp](https://github.com/conduitio-labs/conduit-connector-sftp) | Y | Y | - | v0.12.0 | Y | Y | success 2026-05-22 | v0.1.0 | - | none | 2025-03-13 | deps-only |
| [snowflake](https://github.com/conduitio-labs/conduit-connector-snowflake) | Y | Y | Snowflake stream + tracking table | v0.14.1 | Y | - | success 2026-10-07 | v0.4.0 | - | Apache-2.0 | 2025-02-28 | deps-only |
| [socket](https://github.com/conduitio-labs/conduit-connector-socket) | - | - | - | - | - | - | none | - | - | none | 2024-01-29 | dormant |
| [spanner](https://github.com/conduitio-labs/conduit-connector-spanner) | Y | - | log (change streams) | v0.12.0 | - | Y | success 2026-07-13 | - | - | none | 2025-04-03 | deps-only |
| [sql-server](https://github.com/conduitio-labs/conduit-connector-sql-server) | Y | Y | trigger + tracking table | v0.12.0 | Y | - | success 2026-10-08 | v0.1.0 | - | Apache-2.0 | 2025-03-12 | deps-only |
| [sqs](https://github.com/conduitio-labs/conduit-connector-sqs) | Y | Y | - | v0.14.1 | Y | Y | success 2026-09-28 | v0.3.0 | - | MIT | 2025-04-22 | deps-only |
| [stripe](https://github.com/conduitio-labs/conduit-connector-stripe) | Y | - | API polling (events) | v0.12.0 | Y | - | success 2026-01-06 | v0.3.0 | - | Apache-2.0 | 2025-03-06 | deps-only |
| [vitess](https://github.com/conduitio-labs/conduit-connector-vitess) | Y | Y | log (VStream) | v0.12.0 | Y | Y | success 2026-01-06 | v0.1.0 | - | Apache-2.0 | 2025-03-06 | deps-only |
| [weather](https://github.com/conduitio-labs/conduit-connector-weather) | Y | - | - | v0.12.0 | - | - | success 2026-01-06 | v0.1.0 | - | Apache-2.0 | 2025-03-12 | deps-only |
| [weaviate](https://github.com/conduitio-labs/conduit-connector-weaviate) | - | Y | - | v0.13.3 | - | Y | failure 2026-01-06 | v0.1.1 | - | none | 2025-03-17 | deps-only |
| [zendesk](https://github.com/conduitio-labs/conduit-connector-zendesk) | Y | Y | API polling | v0.12.0 | Y | - | success 2026-01-05 | v0.3.0 | - | Apache-2.0 | 2024-07-03 | deps-only |
| [zeromq](https://github.com/conduitio-labs/conduit-connector-zeromq) | Y | Y | - | v0.12.0 | Y | Y | success 2026-01-05 | - | - | none | 2025-03-06 | deps-only |

### Special repositories

| Repository | What it is | State | Relevance |
| --- | --- | --- | --- |
| `ConduitIO/conduit-kafka-connect-wrapper` | Java standalone plugin that loads Kafka Connect connector JARs from `libs/` and speaks the connector gRPC protocol (`connector.v1`) | Last human commit 2024-07-09, v0.4.3 (2024-02); JDK 20, Unix only; Debezium Postgres integration test in CI | Basis for the v0.23 connector JAR host. Its strengths and gaps (schema-history store, no SMTs, packaging) are analyzed in [the Debezium roadmap design doc](design-documents/20260722-debezium-compete-roadmap.md) |
| `conduitio-labs/conduit-connector-benthos` | Source and destination that run an embedded Benthos v4.6.0 stream from a YAML config | 2022 prototype, pre-release SDK | The source acknowledges messages to Benthos as soon as it hands them to Conduit, `Ack` is a no-op and positions are random UUIDs, so it does not uphold end-to-end acknowledgement or resume. Reference only |
| `conduitio-labs/conduit-connector-java-sdk-poc` | Java connector SDK proof of concept on Quarkus (`connector.v1` protocol), with example file, generator and MySQL connectors | Last commit 2024-06 | Input to the Java SDK design; not a release candidate |
| `conduitio-labs/conduit-connector-file-java-poc`, `conduit-connector-generator-java` | 2023 examples for the Java SDK proof of concept | Dormant | Superseded by the examples in `java-sdk-poc` |
| `conduitio-labs/conduit-connector-s3-iceberg` | Java (JDK 17) Iceberg destination; inserts, deletes by key, updates as delete then insert; REST, Hadoop and JDBC catalogs | Last human commit 2024-07; latest CI run failing | The only Iceberg writer that exists. Useful as a behavior reference for a native destination |
| `conduitio-labs/conduit-flink-connector` | Flink source and sink that run a Conduit pipeline through a Kafka topic | Last human commit 2024-10 | Partner integration, not a connector; no overlap with planned work |
| `ConduitIO/conduit-connector-sdk-python` | Python connector SDK (protocol v2, gRPC standalone) | Active; pre-alpha, no release yet | The v0.21 Python SDK GA item builds on this directly |

### Findings across connectors

- **Ack-before-durable in two prototypes.** `benthos` (source) and `http-server` (source; replies `201 Created`
  before Conduit acknowledges, and buffers in memory) both acknowledge upstream before the record is durably
  handled. Neither can be promoted without a redesign of the ack path (invariant 1).
- **Duplicate CDC implementations.** `sql-server`, `oracle`, `db2` and `sap-hana` each carry their own copy of the
  trigger + tracking-table pattern. A family consolidation would share one implementation and one test kit.
- **Inserts-only change capture.** `clickhouse` and `cosmos-nosql` describe their CDC mode as detecting new rows
  only; updates and deletes are not captured. Docs should say so before either is listed for a CDC path.
- **Benchmarks exist but are dated.** `ConduitIO/streaming-benchmarks` has benchi configs for Postgres, MySQL and
  MongoDB to Kafka (snapshot and CDC), Kafka to Snowflake and two chaos scenarios, several with Kafka Connect
  comparisons. They were last updated in 2025 and have not been re-run against current connector versions.
- **Forks outside the organizations.** The maintainer's personal account holds forks of `mysql`, `sql-server`,
  `box` and `influxdb` with recent pushes. Changes there should land upstream in labs before any promotion.

## Processors

### Built-in processors

Registered in `pkg/plugin/processor/builtin/registry.go`. All have unit tests and runnable examples
(`*_examples_test.go`). None keeps durable state.

| Name | What it does | External calls | Notes |
| --- | --- | --- | --- |
| `avro.decode`, `avro.encode` | Decode or encode a field with Avro, resolving schemas through the schema registry | Schema registry | Avro library upstream is archived; see [the avro advisory design doc](design-documents/20260823-avro-codec-archived-decoder-advisories.md) |
| `base64.decode`, `base64.encode` | Base64 a field | None | |
| `clone` | Emit N copies of each record | None | |
| `cohere.command`, `cohere.embed`, `cohere.rerank` | Text generation, embeddings and reranking through Cohere | Cohere API | Overlaps `ai.embed` (Cohere provider) |
| `custom.javascript` | Run user JavaScript per record | None | In-process JavaScript VM; any state a script keeps lives in memory and is lost on restart |
| `error` | Fail records (routes them to the DLQ) | None | |
| `field.convert` | Convert a field to `string`, `int`, `float`, `bool` or `time` | None | Key and payload only |
| `field.exclude` | Remove fields, including metadata | None | |
| `field.rename` | Rename fields | None | |
| `field.set` | Set a field from a Go template (with sprig functions) | None | Template output is a string, so typed values need `field.convert` after it |
| `filter` | Drop (acknowledge) records | None | Combine with `condition` |
| `json.decode`, `json.encode` | Parse or serialize a JSON field | None | |
| `ollama.request` | Send a prompt to an Ollama instance | Ollama | Specification name is `ollama`; registered as `ollama.request` |
| `openai.embed` | OpenAI embeddings | OpenAI API | Specification name is `openai.embeddings`; registered as `openai.embed`. Overlaps `ai.embed` |
| `openai.textgen` | Rewrite a field with an OpenAI chat model | OpenAI API | |
| `split` | Split an array field into one record per element | None | |
| `unwrap.debezium` | Turn a Debezium change event into an OpenCDC record | None | Equivalent of Debezium `ExtractNewRecordState` on the consuming side |
| `unwrap.kafkaconnect` | Turn a Kafka Connect schema+payload record into OpenCDC | None | |
| `unwrap.opencdc` | Unwrap an OpenCDC record stored in a field | None | |
| `webhook.http` | Send each record to an HTTP endpoint, optionally storing the response | Any HTTP endpoint | Overlaps the `http` connector destination |

**`condition`.** Every processor accepts a `condition`: a Go `text/template` (with sprig functions) evaluated per
record, whose output must parse as a boolean (`pkg/processor/processor_condition.go`). It is the predicate
mechanism for routing and filtering, and the equivalent of Kafka Connect predicates.

### Standalone processors

| Processor | Repository | What it does | State handling | Tests | Maintenance |
| --- | --- | --- | --- | --- | --- |
| `ai.chunk` | `ConduitIO/conduit-processor-ai` | Split text into chunk records (fixed size, sentence, recursive) | Stateless | Unit tests; no acceptance test | Active; v0.1.0 in registry |
| `ai.embed` | `ConduitIO/conduit-processor-ai` | Embeddings through OpenAI, Voyage, Ollama or Cohere using the host egress capability | Stateless | Unit, acceptance and opt-in live tests | Active; v0.1.0 in registry |
| `hl7` | `conduitio-labs/conduit-processor-hl7` | Convert FHIR Patient JSON to and from HL7 v2 and v3 | Stateless | Unit tests | Deps-only; processor SDK v0.4.3 |
| `textgen` | `conduitio-labs/conduit-processor-textgen` | Template stub: `Process` returns no records | n/a | None | No functionality |
| `aggregate` | `devarispbrown/conduit-processor-aggregate` (personal) | Tumbling and sliding windows with count, sum, average, min, max | In memory only, not checkpointed. Every input record is filtered (so it is acknowledged upstream) and held in process memory; window emission is only invoked from tests; late records are filtered | Unit and integration tests | Last commit 2025-06 |
| `sql` | `devarispbrown/conduit-processor-sql` (personal) | README describes SQL transforms; the latest commit turned it into a numeric threshold filter | Stateless | Unit tests | Last commit 2025-06 |
| `json.query` | `devarispbrown/conduit-processor-jsonquery` (personal) | Replace the payload with a jq or JMESPath query result | Stateless | Unit tests | Last commit 2025-06 |
| `json.cleanup` | Local only, no published repository | Decode base64 and strip Markdown fences from LLM output | Stateless | None | Unpublished |
| `pdf.totext` | Local only, no published repository | Extract text from a PDF payload | Stateless | None | Unpublished |
| Examples | `ConduitIO/conduit-processor-example`, `conduit-processor-template` | Simple and full processor examples; scaffold | n/a | Example tests | Deps-only |

The `aggregate` processor does not satisfy invariants 1, 3 and 6 (records are acknowledged before any output
exists, window state does not survive a restart, late data is dropped without a DLQ route). It is useful as a
configuration sketch for the planned windowing work, not as a code base to extend.

### Processor SDKs

- **Go** (`ConduitIO/conduit-processor-sdk`, v0.6.0, 2026-10-07): standalone processors compile to WASM and run
  in wazero (WASI Preview 1). Network access goes through the host egress capability added for `ai.embed`.
- **Python** (`ConduitIO/conduit-processor-sdk-python`): work in progress, last commit 2025-06-27, no README or
  release. It compiles Python to a WASM component with `componentize-py` against a WIT world
  (`conduit:processor@1.0.0`) that mirrors the Go SDK's raw ABI (exported `malloc` plus pointer/size functions).
  The commit log says it does not yet compile end to end. The planned gRPC out-of-process processor runtime is a
  different model; only the WIT and protobuf definitions carry over.

## Mapping tables

### Kafka Connect SMT parity

Status: _full_ = an existing processor or documented composition covers the common configuration; _partial_ =
covered with a stated limitation; _missing_ = no processor; only `custom.javascript` can emulate it. Only
_missing_ items, and the limitations of _partial_ ones, are SMT-pack work.

| SMT | Conduit equivalent | Status |
| --- | --- | --- |
| `Cast` | `field.convert` | Partial: no per-field type map in one processor, no `bytes`/`int8`–`int64` distinctions |
| `DropHeaders` | `field.exclude` on `.Metadata.*` | Full |
| `ExtractField` | `field.set` with a template, or `custom.javascript` | Partial: `field.set` yields strings |
| `Filter` (with predicates) | `filter` + `condition` | Full |
| `Flatten` | none | Missing |
| `HeaderFrom` | `field.set` on `.Metadata.*` (+ `field.exclude` for move) | Full |
| `HoistField` | `field.set` with a template | Partial: string output |
| `InsertField` | `field.set` (static values, or `.Metadata` for topic, partition, offset, timestamp) | Partial: string output |
| `InsertHeader` | `field.set` on `.Metadata.*` | Full |
| `MaskField` | `field.set` with a constant | Partial: no type-aware null/zero masking, no replacement per type |
| `RegexRouter` | `field.set` on `.Metadata["opencdc.collection"]` with sprig `regexReplaceAll` | Full |
| `ReplaceField` | `field.exclude` + `field.rename` | Partial: no include-list mode |
| `SetSchemaMetadata` | `avro.encode` schema options | Partial: no standalone schema name/version override |
| `TimestampConverter` | `field.convert` (`time`) | Partial: no format string or unit conversion |
| `TimestampRouter` | `field.set` on the collection with a time template | Partial: no record-timestamp formatting helper |
| `ValueToKey` | `field.set` on `.Key` | Partial: string output, one field per processor |
| Predicates `TopicNameMatches`, `HasHeaderKey`, `RecordIsTombstone` | `condition` templates | Full |
| Debezium `ExtractNewRecordState` | `unwrap.debezium` (consuming); native Conduit CDC already emits flat OpenCDC | Partial: no `add.fields`, `delete.handling.mode`, `drop.tombstones` options |
| Debezium `ExtractChangedRecordState` | none | Missing |
| Debezium `EventRouter` (outbox) | none | Missing |
| Debezium `ContentBasedRouter` | `field.set` on the collection with a conditional template | Partial: no scripting language |
| Debezium `Filter` | `filter` + `condition`, or `custom.javascript` | Full |
| Debezium `PartitionRouting` | none (Kafka destination partitions by key only) | Missing |
| Debezium `TimezoneConverter` | none | Missing |
| Debezium `ByLogicalTableRouter` | `field.set` on the collection with `regexReplaceAll` | Partial: no key augmentation to keep keys unique across merged tables |
| Debezium `HeaderToValue` | `field.set` from `.Metadata.*` | Full |

SMT-pack work, then, is: `Flatten`, `ExtractChangedRecordState`, `EventRouter`, `PartitionRouting`,
`TimezoneConverter`, plus typed output for the `field.set`-based equivalents (`ExtractField`, `HoistField`,
`InsertField`, `ValueToKey`), include-list `ReplaceField`, type-aware `MaskField`, format-aware
`TimestampConverter`, and the `unwrap.debezium` options. The no-bespoke-DSL ADR already commits to 1:1 processor
equivalents rather than an expression language.

### Golden paths

| Path | Existing connectors | Gap to Certified | Estimate |
| --- | --- | --- | --- |
| Postgres CDC → Kafka | `postgres` (core, log-based, chaos harness, integration, registry) + `kafka` (core, acceptance, integration, registry) | Add the SDK acceptance suite to `postgres`; a kill test with Kafka as the sink; re-run and commit the existing benchi configs against current versions | 2–3 engineer-weeks |
| MySQL CDC → Kafka | `mysql` (labs, binlog, integration harness, active) + `kafka` | Move to `ConduitIO`, add a `LICENSE`; acceptance suite; port the `postgres` kill harness; publish workflow and registry entry; re-run benchi | 4–6 engineer-weeks |
| Kafka → S3 (Parquet) | `kafka` + `s3` (core, Parquet and JSON output, MinIO integration) | Acceptance suite for `s3`; kill test on the destination; benchi config; registry entry | 2–3 engineer-weeks |
| Kafka → JDBC | `postgres` destination (core); labs `mysql`, `sql-server`, `oracle`, `db2`, `sap-hana` destinations; no generic JDBC destination | Certify `postgres` and `mysql` destinations first; the others need real-database CI (SQL Server and Oracle containers, Db2 and HANA are heavier); interim path is a JDBC sink through the Kafka Connect wrapper | 2–3 engineer-weeks for Postgres + MySQL; 2–3 per additional database |
| Kafka → Elasticsearch / OpenSearch | `elasticsearch` (labs, ES 5–8, acceptance per version, integration) | Fix the failing CI and bump the SDK; test against OpenSearch or add an OpenSearch mode; kill test; benchi | 3–4 engineer-weeks |
| CDC → Snowflake | `snowflake` (labs; destination stages files and merges, documented as early-stage; source uses Snowflake streams) | Destination hardening (idempotent merge on retry, schema drift policy); credentialed CI account; benchi (config exists in `streaming-benchmarks`) | 4–6 engineer-weeks |
| CDC → Iceberg | `s3-iceberg` (labs, Java, dormant) only | Build a native destination; use `s3-iceberg` as the behavior reference | 8–12 engineer-weeks |
| CDC → BigQuery | `bigquery` (labs) is source only | Build a destination | 4–6 engineer-weeks |
| CDC → ClickHouse | `clickhouse` (labs, source and destination, acceptance, no compose harness) | Integration harness, SDK bump, kill test, benchi; document inserts-only source CDC | 3–4 engineer-weeks |
| Postgres CDC → pgvector | `postgres` + `ai.chunk` + `ai.embed` + `pgvector` (all core, all in registry; `rag-e2e` required in CI) | Kill test across the chain; benchi; `ai.chunk` acceptance | 1–2 engineer-weeks |
| Postgres CDC → Qdrant | `qdrant` is a placeholder; labs has `pinecone`, `weaviate`, `openai-vectorstore` | Build Qdrant (the `pgvector` destination is the template) | 2–3 engineer-weeks |
| HTTP / webhooks → Kafka or NATS | `http-server` (labs, stale, acks before durable), `http` (labs, polling source + destination), `grpc-server`; `nats-jetstream`, `nats-pubsub` (labs, acceptance, integration) | Rebuild the webhook source so the HTTP response waits for the ack; fix `nats-jetstream` CI and bump SDK; kill tests; benchi | 3–4 weeks for the webhook source; 2–3 for NATS JetStream |

Estimates assume one engineer who knows the SDK and reuse of the `postgres` chaos harness pattern. They exclude
roadmap feature work on the same connectors (for example Debezium-compatible output, MySQL schema history).

### Redpanda Connect / Benthos components

Common components only. "Equivalent" means a Conduit connector or processor with the same role, not identical
configuration.

| Component | Conduit equivalent | Status |
| --- | --- | --- |
| `kafka`, `kafka_franz` (input/output) | `kafka` (core); `redpanda` (labs) | Exists |
| `amqp_0_9` | `rabbitmq` (labs) | Exists |
| `amqp_1` | `activemq-artemis` (labs) | Exists |
| `aws_s3` | `s3` (core) | Exists |
| `aws_sqs`, `aws_kinesis` | `sqs`, `kinesis` (labs) | Exists |
| `aws_dynamodb` | `dynamodb` (labs) | Exists |
| `gcp_pubsub` | `gcp-pubsub` (labs) | Exists |
| `gcp_cloud_storage` | `google-cloudstorage` (labs, source only) | Partial: no destination |
| `gcp_bigquery` (output), `gcp_bigquery_select` | `bigquery` (labs, source only) | Partial: no destination |
| `azure_blob_storage` | `azure-storage` (labs, source only) | Partial: no destination |
| `azure_queue_storage`, `azure_table_storage`, `azure_cosmosdb` | `cosmos-nosql` (labs, source, inserts only) | Partial |
| Event Hubs (via `kafka`) | `azure-event-hub` (labs) | Exists |
| `nats`, `nats_jetstream` | `nats-pubsub`, `nats-jetstream` (labs) | Exists |
| `nats_kv` | none | Missing |
| `mqtt` | none | Missing |
| `pulsar` | `pulsar` (labs) | Exists |
| `zmq4` | `zeromq` (labs) | Exists |
| `redis_list`, `redis_streams`, `redis_pubsub` | `redis` (labs) | Partial: single key / stream |
| `http_server`, `http_client` | `http-server`, `http`, `grpc-server`, `grpc-client` (labs) | Exists; webhook source needs rework |
| `file`, `stdin`, `stdout` | `file`, `log` (core) | Exists |
| `generate` | `generator` (core); `enhanced-generator` (labs) | Exists |
| `sql_select`, `sql_insert`, `sql_raw` | `postgres`, `mysql`, `sql-server`, `oracle`, `db2`, `sap-hana`, `snowflake`, `redshift`, `clickhouse` | Exists per database; no generic SQL driver connector |
| `postgres_cdc`, `mysql_cdc`, `mongodb_cdc` | `postgres` (core), `mysql`, `mongo` (labs) | Exists |
| `cassandra` | `cassandra` (labs, destination) | Exists |
| `elasticsearch` | `elasticsearch` (labs) | Exists |
| `opensearch` | none documented | Missing |
| `snowflake_put`, `snowflake_streaming` | `snowflake` (labs) | Exists (early-stage destination) |
| `sftp` | `sftp` (labs) | Exists |
| `discord` | `discord` (labs, stale) | Exists |
| `splunk_hec` | none | Missing |
| `questdb`, `influxdb` | `influxdb` (labs) | Partial |
| `qdrant`, `pinecone` | `pinecone` (labs); `qdrant` placeholder | Partial |
| `couchbase`, `nsq`, `beanstalkd`, `websocket` | none | Missing |
| Processors `mapping`, `mutation`, `bloblang` | `field.*` processors, `custom.javascript`, WASM processors | Not equivalent by design (no bespoke DSL) |
| `jq`, `jmespath` | `json.query` (personal repo, unreleased) | Needs a decision |
| `javascript`, `wasm` | `custom.javascript`; standalone WASM processors | Exists |
| `avro`, `schema_registry_decode`, `schema_registry_encode` | `avro.decode`, `avro.encode` | Exists |
| `protobuf` | none (Protobuf decode is a v0.21 item) | Missing |
| `parquet_encode`, `parquet_decode` | `s3` destination Parquet format only | Partial |
| `http`, `aws_lambda` | `webhook.http` | Partial: no Lambda invoke |
| `split`, `unarchive` | `split` | Partial |
| `branch`, `switch`, `workflow` | `condition` + `clone` + multiple destinations | Partial |
| `dedupe`, `cache`, `rate_limit` | none | Missing (dedup is a v0.25 state item) |
| `group_by`, windowed aggregation | none in tree; `aggregate` is a non-durable prototype | Missing (v0.27) |
| `openai_*`, `cohere_*`, `ollama_*` | `openai.*`, `cohere.*`, `ollama.request`, `ai.embed` | Exists |
| `compress`, `decompress`, `archive` | none | Missing |
| `xml`, `msgpack` | none | Missing |
| `grok` | none | Missing |

### Overlaps inside Conduit

| Overlap | Today | Recommendation |
| --- | --- | --- |
| Embeddings | Built-in `openai.embed` and `cohere.embed`; standalone `ai.embed` (OpenAI, Voyage, Ollama, Cohere) | Make `ai.embed` the documented path; keep the built-ins working, mark them deprecated with the announce → warn → remove policy once `ai.embed` is bundled or one-command installable |
| Text generation | Built-in `openai.textgen`, `cohere.command`, `ollama.request`; labs `textgen` stub | Fold into the v0.28 `ai.extract` / `ai.classify` / `ai.summarize` family; archive the labs stub now |
| HTTP out | `webhook.http` processor and `http` connector destination | Keep both, document the split: processor when the response feeds the record, destination when the endpoint is the sink |
| Kafka-API brokers | `kafka` (core) and `redpanda` (labs) | Verify `kafka` against Redpanda in CI and retire `redpanda` (broker neutrality, one code path) |
| Generators | `generator` (core) and `enhanced-generator` (labs) | Merge any missing features into `generator`, archive `enhanced-generator` |
| SQL-like processing | `sql` (personal, now a threshold filter) and `filter` + `condition` | Do not adopt `sql`; `filter` + `condition` covers it. Heavier SQL belongs in partner engines |
| Windowed aggregation | `aggregate` (personal, in memory) and planned v0.27 windows | Build v0.27 on the state API; borrow only the configuration shape |
| Benthos | 2022 `benthos` prototype and a planned Bento adapter | Build the adapter fresh; archive the prototype once it lands |
| Java connectors | `java-sdk-poc` (+ two examples) and the planned Java SDK | Reuse its spec/annotation design notes; the SDK targets protocol v2 and the planned gRPC processor runtime, so code reuse is low |
| Kafka Connect JAR host | `conduit-kafka-connect-wrapper` and the planned v0.23 host | Extend the wrapper (see recommendations) |
| Debezium formats | `unwrap.debezium` (reads Debezium events) and planned Debezium-compatible output (writes them) | Opposite directions, not duplicates; share the envelope test vectors |
| Python processors | WASM-based `conduit-processor-sdk-python` and the planned gRPC Python processor SDK | Pick one model before v0.24; the WIP repo should say which |

## Recommendations for planned catalog work

Verdicts: **REUSE** = promote and certify an existing asset; **EXTEND** = existing asset plus a named gap;
**CONSOLIDATE** = merge overlapping assets; **BUILD** = nothing usable exists.

| Planned item | Verdict | Existing asset | Gap |
| --- | --- | --- | --- |
| Family-based native connector library | CONSOLIDATE + REUSE | 72 Go connectors that group into SQL CDC, SQL sinks, brokers, object stores, warehouses, search and vector, SaaS | Shared family code and test kits (one trigger/tracking-table CDC implementation instead of four; one broker conformance kit); bump the 45 connectors below SDK v0.14; add `LICENSE` files; make labs CI run on the default branch |
| Golden-path certification | REUSE | See the golden-path table | Acceptance, kill tests and benchi per connector; Iceberg, BigQuery destination and Qdrant are BUILD |
| Kafka Connect JAR host (v0.23) | EXTEND | `conduit-kafka-connect-wrapper` | Protocol v2, schema-history store for Debezium MySQL/SQL Server/Oracle/Db2, SMT execution, container packaging, current JDK, revisit the strict FIFO ack assumption |
| SMT compatibility pack (v0.22) | EXTEND | Built-in `field.*`, `filter`, `condition`, `unwrap.debezium` | The _missing_ and _partial_ rows in the SMT table |
| `conduit migrate kafka-connect` (v0.21) | EXTEND | SMT table above; `unwrap.kafkaconnect`; the wrapper's connector config handling | Mapping rules per connector class; the compatibility report |
| Bento / Redpanda Connect adapter | BUILD | `benthos` prototype as reference only | Ack propagation and resumable positions from scratch; host inputs and outputs, leave Bloblang processors out per the no-bespoke-DSL ADR |
| Salesforce | EXTEND | `salesforce` (labs; Pub/Sub API platform events in, publish out) | Object snapshot (Bulk API) and Change Data Capture channels, acceptance suite, docs |
| Stripe | EXTEND | `stripe` (labs; source, events API polling) | Credentialed CI, webhook mode once the webhook source is rebuilt |
| HubSpot | EXTEND | `hubspot` (labs; source and destination, polling) | Credentialed CI, SDK bump |
| Shopify | BUILD | none | Consider `connector generate --from-openapi` (v0.24) |
| GitHub | BUILD | none | Same |
| Curated processor library | CONSOLIDATE | Built-ins, `ai.chunk`, `ai.embed`, `hl7` | Settle the embedding and text-generation overlaps; decide on `json.query` against the ADR; publish or drop `json.cleanup` and `pdf.totext`; keep `hl7` in labs |
| NATS JetStream native (v0.22) | EXTEND | `nats-jetstream` (labs) | Fix CI, SDK bump, move to `ConduitIO`, kill test |
| Iceberg destination (v0.22) | BUILD | `s3-iceberg` as behavior reference | Everything |
| MySQL CDC beta/GA (v0.22–v0.23) | EXTEND | `mysql` (labs, active) | See golden paths |
| SQL Server and MongoDB CDC (v0.24) | EXTEND (MongoDB), BUILD (SQL Server log-based) | `mongo` is log-based; `sql-server` uses triggers | MongoDB: certification. SQL Server: a log-based reader; the trigger implementation stays as a fallback |
| Qdrant (v0.24+) | BUILD | Placeholder repository | Everything; `pgvector` as the template |
| Windows and aggregations (v0.27) | BUILD | `aggregate` prototype (not durable) | Everything, on the state API |
| AI on streams (v0.28) | CONSOLIDATE | `openai.textgen`, `cohere.command`, `ollama.request`, `ai.embed` provider layer | Reuse the `ai.embed` provider and egress design for the new processors |
| Python connector SDK GA (v0.21) | REUSE | `conduit-connector-sdk-python` | Release per its README's GA definition |
| Java SDK (v0.25–v0.26) | BUILD (informed by POC) | `java-sdk-poc` | Protocol v2 and the gRPC processor runtime |

## Related

- [Debezium roadmap design doc](design-documents/20260722-debezium-compete-roadmap.md): the Kafka Connect wrapper
  analysis this inventory relies on
- [AI pipeline components design doc](design-documents/20260724-ai-pipeline-components.md): the `ai.chunk` and
  `ai.embed` reconciliation with the built-in embedding processors
- [No bespoke DSL ADR](architecture-decision-records/20260704-no-bespoke-dsl.md): why SMTs map to processors and
  why a Bento adapter leaves Bloblang out
- [Processors ride the connector registry ADR](architecture-decision-records/20260727-processors-ride-connector-registry.md)
- [Connector registry index schema](design-documents/20260714-connector-registry-index-schema.md)
- [`ROADMAP.md`](../ROADMAP.md)
