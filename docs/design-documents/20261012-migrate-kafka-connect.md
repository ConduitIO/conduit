# `conduit migrate kafka-connect`, `conduit kc lint`, `conduit kc diff` (v1)

## Summary

Three offline commands that take a Kafka Connect (KC) deployment as input and produce, respectively, a
Conduit pipeline plus a machine-readable compatibility report (`migrate kafka-connect`), a list of risks in
the KC config itself (`kc lint`), and a semantic diff between two versions of a connector config, with an
optional schema-compatibility check against a Schema Registry (`kc diff`).

The rule that shapes everything below: **every KC key in the input ends up in the report exactly once, as
`translated`, `approximated` (with the reason) or `unsupported` (with the reason and the fix).** The command
never drops a key, never invents a value, never writes a secret, and never produces a pipeline that quietly
behaves differently from the connector it replaces. Where Conduit cannot match KC behavior, the pipeline is
written but held in `status: stopped` until a human accepts the gap.

This is roadmap item "v0.21: migrate-design" (ROADMAP.md, v0.21: `migrate kafka-connect` v1, `kc lint`,
`kc diff`). It is a design document only; no code lands with it. Risk tier of this PR: Tier 3 (docs). The
implementation PRs it describes are Tier 2 (CLI), except the pieces that adopt a Debezium replication slot or
Kafka consumer-group offsets, which are Tier 1 (see _Failure modes_).

## Context

### What exists today

Inventoried from `main` (b1da04a6), the roadmap branch (#2945), the catalog inventory (#2951) and the
connector repositories.

- **No migration code.** `grep` finds no `migrate` or `kc` command, package or test in `ConduitIO/conduit`.
  The only Kafka Connect code is the unwrap processor (`unwrap.kafkaconnect`) and the separate
  `conduit-kafka-connect-wrapper` repository (basis for the v0.23 JAR host).
- **CLI conventions to reuse, not reinvent.** `cmd/conduit/root` has a command per verb;
  `docs/design-documents/20260707-cli-output-conventions.md` fixes the `--json` envelope (family A), the
  glyph rendering of located findings, `--quiet`/`--strict`, and exit codes via `pkg/conduit/exitcode`.
  `cmd/conduit/internal/validate` is the parse/enrich/validate engine behind `pipelines validate|lint|dry-run`.
  Stable codes come from `pkg/foundation/cerrors/conduiterr` (`domain.reason` form).
- **Pipeline config** is YAML version 2.2 (`pkg/provisioning/config/yaml/v2`): connectors with
  `plugin: builtin:<name>` and `settings`, connector-level and pipeline-level `processors`, per-processor
  `condition`, and a `dead-letter-queue` block (`plugin`, `settings`, `window-size`,
  `window-nack-threshold`). `${VAR}` references are expanded at parse time (`hooks.go`, `os.ExpandEnv`).
- **Built-in processors** (registry): `avro.decode|encode`, `base64.*`, `clone`, `custom.javascript`,
  `error`, `field.convert|exclude|rename|set`, `filter`, `json.decode|encode`, `split`, `unwrap.debezium`,
  `unwrap.kafkaconnect`, `unwrap.opencdc`, `webhook.http`, plus embedding/LLM processors that are irrelevant
  here. Standalone: `ai.chunk`, `ai.embed`. `condition` (a Go template yielding a bool) is the predicate
  mechanism and is the equivalent of KC predicates.
- **SMT parity** is already tabulated in `docs/catalog-inventory.md` (#2951, "Kafka Connect SMT parity").
  This document consumes that table; it does not redo it.
- **Connectors** (settings verified against each `connector.yaml` / `paramgen.go`):

  | Connector | Where | Role in migration | Notable settings |
  | --- | --- | --- | --- |
  | `postgres` | built-in (go.mod pins v0.14.3; target is v0.15.0) | Debezium Postgres, JDBC source (CDC approximation), JDBC sink | source: `url`, `tables`, `cdcMode`, `snapshotMode` (`initial`, `never`), `logrepl.slotName`, `logrepl.publicationName`, `logrepl.autoCleanup`; destination: `url`, `table` (template, default from `opencdc.collection`) |
  | `kafka` | built-in v0.12.5 | every KC sink's input and every KC source's output; MirrorMaker | source: `servers`, `topics`, `groupID`, `readFromBeginning`, SASL/TLS settings; destination: `servers`, `topic` (template), `acks`, `compression`, SASL/TLS settings |
  | `s3` | built-in v0.9.4 | S3 sink | destination: `aws.bucket`, `aws.region`, `aws.accessKeyId`, `aws.secretAccessKey`, `aws.endpoint`, `aws.pathStyle`, `prefix`, `format` (`json`, `parquet`) |
  | `mysql` | `conduitio-labs`, v0.3.0, binlog | Debezium MySQL, JDBC MySQL | source: `dsn`, `tables`, `snapshot.enabled`, `cdc.disableLogs`; destination: `dsn` |
  | `elasticsearch` | `conduitio-labs`, v0.4.0 | Elasticsearch sink | destination: `host`, `version`, `index`, `type`, `username`, `password`, `APIKey`, `cloudID`, `bulkSize`, `retries` |
  | `sql-server`, `oracle`, `db2`, `sap-hana` | `conduitio-labs` | JDBC sink/source for other databases | trigger-based CDC; not v0.21 targets |

- **Related open work this design depends on or hands off to** (not designed here):
  Debezium offset import and the Postgres CDC completion work (separate lane); Protobuf decode for Schema
  Registry (#2882 design sign-off); Go toolchain and Avro library replacement; #2988 (secrets passed as
  `${VAR}` are stored expanded, in plaintext, in Conduit's DB); the v0.22 SMT pack; Debezium-compatible
  output mode (v0.22); KC REST API compatibility and the JAR host (v0.23).

### Problem

A team running Kafka Connect cannot evaluate Conduit without hand-translating every connector: learning a
second config vocabulary, guessing at which SMT maps to which processor, and finding out only in production
that a key they did not know mattered (`slot.name`, `errors.tolerance`, `snapshot.mode`) was silently
different. "Moving off Kafka Connect should be a command" (roadmap principle 5) is only true if the command is
trustworthy about what it did not translate.

### Constraints

- Offline and read-only by default. `migrate` and `kc *` write files and read inputs; they never connect to a
  database, never create a slot, never write to Kafka or the Connect worker.
- Data integrity invariants (CLAUDE.md) apply to the _output_: a generated pipeline must not weaken ack
  correctness, at-least-once, ordering or schema handling relative to what the report claims. Where a KC
  feature gives a stronger guarantee than Conduit's (for example S3 sink exactly-once via deterministic file
  names), the report says so.
- No bespoke DSL, no parallel config dialect: output is ordinary pipeline YAML 2.2 that `pipelines validate`
  accepts.
- Every user-facing error has a stable code, a config path and a suggested fix (CLAUDE.md "Errors are API").
  Every command supports `--json`.
- No performance claims. Nothing in this document asserts a throughput or latency number.
- Broker-neutral: Kafka is the default target because KC is Kafka-only, but the generated Kafka destination
  is one `plugin:` line; nothing in the translator privileges a vendor.

## Decision

### Command surface

```text
conduit migrate kafka-connect [flags] <input>...
conduit kc lint  [flags] <input>...
conduit kc diff  [flags] <old> <new>
```

`<input>` is one or more of: a file or directory, `-` for stdin, or `--from-rest URL` (see _Inputs_). Input
format is detected, not declared; `--format {properties,json,rest-export,strimzi}` overrides detection.

`migrate kafka-connect` flags:

| Flag | Meaning |
| --- | --- |
| `--out DIR` | Output directory (default `./conduit-migration`). Refuses to overwrite without `--force` (code `migrate.output_exists`). |
| `--dry-run` | Produce the report only, write no files. This is the "zero-risk front door". |
| `--worker FILE` | Worker properties (or a Strimzi `KafkaConnect` CR) that supply defaults for converters, client security and config providers. Optional; without it, worker-level defaults are reported as unknown rather than assumed. |
| `--registry-url URL` | Schema Registry URL, used only to fill the `conduit.yaml` fragment and the report. Never contacted by `migrate`. |
| `--slot-mode {new,adopt}` | Postgres only. `new` (default): the pipeline uses its own slot and publication names. `adopt`: reuse the Debezium slot (see _Debezium Postgres_ and _Failure modes_). |
| `--accept-gaps` | Write blocking pipelines as `status: running` instead of `stopped`. Recorded in the report. |
| `--json`, `--quiet`, `--strict` | Per the CLI output conventions. `--strict` exits 2 on any `approximated` entry too. |

`migrate` never applies anything: no `--apply`, no calls to a running Conduit. Handing the output to
`conduit pipelines validate|dry-run|deploy` is a separate, explicit step. This keeps v1 rollback trivial.

Output directory:

```text
conduit-migration/
  pipelines/<connector-name>.yaml     one file per KC connector (connector + its tasks = one pipeline)
  conduit.yaml.fragment               schema-registry and other engine-level settings derived from the worker
  secrets.env.template                names of the environment variables the pipelines reference, no values
  report.json                         the compatibility report (also printed under --json)
  REPORT.md                           human rendering of the same report
```

### Concept mapping

| Kafka Connect | Conduit | Notes |
| --- | --- | --- |
| Worker | Conduit instance | Worker-level keys become `conduit.yaml` fragment entries or are inherited into each pipeline; `group.id`, `config.storage.topic`, `status.storage.topic`, `rest.*` have no equivalent and are reported as `translated` (no-op, reason: "Conduit has no worker cluster") rather than hidden. |
| Connector + its tasks | One pipeline | `tasks.max` greater than 1 is `approximated`: a Conduit pipeline reads a source with one reader; parallelism arrives with partition claims (v0.24). Sinks get `sdk.batch.size` tuning guidance in the fix text. |
| SMT chain | Connector-level processors | Source SMTs attach to the source connector, sink SMTs to the destination connector, preserving order. |
| Predicate | Processor `condition` | |
| Converter | Schema/format configuration | See _Converters_. |
| DLQ (`errors.*`) | `dead-letter-queue` | See _Error handling_. |

### Inputs

All inputs normalize to one internal representation (IR): `Worker{props}` and `Connector{name, class, role,
config, tasksMax, state}`, where `config` is an ordered list of `(key, value, origin)` with `origin` carrying
file, line and column (or JSON pointer) so that every report entry and lint finding is located.

1. **Worker properties** (`connect-distributed.properties`, `connect-standalone.properties`): Java
   properties syntax including `\` continuation and `:`/`=` separators. Parsed by our own small parser (the
   Java properties grammar is simple and we need line numbers); fuzzed.
2. **Connector config**, as the Connect REST API accepts it (`{"name": ..., "config": {...}}`), the bare
   config map, and the standalone `.properties` form (`name=`, `connector.class=`).
3. **REST export**: `GET /connectors?expand=info&expand=status` output, or `--from-rest URL` which performs
   exactly those read-only GETs (and `GET /connector-plugins` to learn installed classes) with optional basic
   auth from `${VAR}`. No other verb is ever sent. The Connect REST API returns secret values masked or
   resolved depending on the worker's config provider setup; `${provider:path:key}` references pass through
   as references (see _Secrets_).
4. **Strimzi `KafkaConnector` custom resource** (`apiVersion: kafka.strimzi.io/*`, `spec.class`,
   `spec.tasksMax`, `spec.config`, `spec.state`; multi-document YAML supported) and, via `--worker`, the
   `KafkaConnect` CR's `spec.config`. Strimzi `${secrets:ns/name:key}` and `${strimzidir:...}` references are
   config-provider references and are handled as in _Secrets_. Strimzi-only fields (`spec.autoRestart`,
   `resources`, labels) are reported `unsupported` with the reason "Kubernetes lifecycle concern; the
   operator and Helm chart (v0.22+) own this".

### Output: pipeline YAML

Pipeline files are ordinary version 2.2 YAML. Provenance goes in `description` (YAML comments do not survive
the API): `Migrated from Kafka Connect connector "<name>" (<connector.class>) by conduit <version>; report
sha256:<hash>`.

A source KC connector (Debezium, JDBC source) becomes `source: <db connector>` plus
`destination: builtin:kafka`, because a KC source always writes to Kafka and consumers downstream depend on
those topics. A sink KC connector becomes `source: builtin:kafka` plus `destination: <target>`.
MirrorMaker-style Kafka to Kafka becomes `source: builtin:kafka` plus `destination: builtin:kafka`.

Pipeline `status` is `stopped` when the connector has any **blocking** report entry (below), otherwise
`running`. A pipeline for a KC connector whose `spec.state`/REST state was `PAUSED` is also written `stopped`.

### Output: the compatibility report

One JSON document, also the `result` of the CLI envelope. Schema is versioned (`reportVersion: 1`) and
published as a JSON Schema alongside the golden test, per the CLI contract schema-golden practice.

```json
{
  "command": "migrate.kafka-connect",
  "ok": false,
  "summary": {"connectors": 2, "keys": 41, "translated": 30, "approximated": 7, "unsupported": 4, "blocking": 2},
  "result": {
    "reportVersion": 1,
    "generator": {"conduit": "0.21.0"},
    "connectors": [{
      "name": "orders-cdc",
      "class": "io.debezium.connector.postgresql.PostgresConnector",
      "role": "source",
      "pipeline": "pipelines/orders-cdc.yaml",
      "pipelineStatus": "stopped",
      "target": {"source": "builtin:postgres", "destination": "builtin:kafka",
                 "certification": {"postgres": "required-v0.21", "kafka": "required-v0.21"}},
      "entries": [
        {"key": "database.hostname", "status": "translated",
         "origin": {"file": "orders.json", "pointer": "/config/database.hostname"},
         "target": {"configPath": "/pipelines/0/connectors/0/settings/url"}},
        {"key": "database.password", "status": "translated", "secret": true,
         "target": {"configPath": "/pipelines/0/connectors/0/settings/url", "env": "KC_ORDERS_CDC_DATABASE_PASSWORD"}},
        {"key": "topic.prefix", "status": "approximated", "code": "kc.key_approximated",
         "reason": "Conduit writes the table name; the Debezium topic is <prefix>.<schema>.<table>. A destination topic template reproduces the name, but the message format is OpenCDC, not the Debezium envelope.",
         "target": {"configPath": "/pipelines/0/connectors/1/settings/topic"},
         "blocking": true, "fix": "Accept OpenCDC output (--accept-gaps) or wait for Debezium-compatible output mode (v0.22)."},
        {"key": "heartbeat.interval.ms", "status": "unsupported", "code": "kc.key_unsupported",
         "reason": "...", "blocking": false, "fix": "..."}
      ]
    }],
    "transforms": [ {"alias": "route", "class": "org.apache.kafka.connect.transforms.RegexRouter",
                     "status": "translated", "processor": "field.set"} ],
    "resume": {"strategy": "new-slot", "snapshot": "will-run", "notes": ["..."]},
    "unreadKeys": []
  },
  "error": null
}
```

Entry fields: `key`, `status` (`translated` | `approximated` | `unsupported`), `origin`, optional `target`
(`configPath` is a JSON pointer into the generated pipeline file), `code`, `reason` (required for
approximated and unsupported), `fix` (required for unsupported), `blocking`, `secret`.

Definitions, applied by the translators and enforced by tests:

- **translated**: the Conduit configuration is behaviorally equivalent for the documented semantics of the
  KC key. Includes keys that are no-ops by design (reason recorded).
- **approximated**: Conduit does something close, the difference is documented in `reason`, and the
  difference cannot corrupt, drop or silently reshape data (changed topic layout, batching vs file-rotation
  granularity, retry timing). Output-format changes that break downstream consumers are `blocking`.
- **unsupported**: no Conduit equivalent in this release. `fix` states what to do: a processor to write, a
  later release that adds it, or a manual step.
- **blocking**: the unmigrated or approximated behavior could change what lands downstream. A blocking entry
  makes the envelope `ok: false` and the pipeline `stopped`. Non-blocking examples: `poll.interval.ms`,
  `max.request.size`, metrics keys. Which entries are blocking is a property of the translator rule, listed
  in the rule table and covered by golden files; it is not a runtime heuristic.

**Completeness invariant (Invariant 6 applied to config).** The translator is a registry of rules keyed by
`(connector class, key pattern)`. After all rules run, any key not claimed by a rule is emitted as
`unsupported` with code `kc.key_unknown` and `blocking: true`. A property test asserts the multiset of input
keys equals the multiset of report entry keys for every corpus input and every fuzz-generated input. A key
cannot fall off the report, including keys on connector classes we do not recognize
(`kc.connector_class_unsupported`: all keys listed, no pipeline file written for that connector).

Exit codes (via `exitcode`): 0 when the report has no blocking entries (approximations alone do not fail;
`--strict` makes them fail); 2 when any blocking entry exists or input is invalid; 3 when `--from-rest`
cannot reach the worker; 1 for internal errors.

### Connector mapping

"Certification" below is the registry quality tier from the catalog track (Certified = acceptance suite plus
integration test against the real system in CI, kill -9 chaos test, committed benchi configuration with a
recorded run, documented delivery semantics). **The v0.21 theme is that every connector `migrate` emits is
Certified in the same release.** Each row is therefore a v0.21 dependency owned by the catalog lane, not by
this document. `migrate` reads the certification state from the registry index at run time and reports
`kc.target_not_certified` (non-blocking, warning) for any target that is not Certified when the command runs,
so the command stays honest if a certification slips.

| KC connector class | Conduit target | Certification dependency (v0.21) | Notes |
| --- | --- | --- | --- |
| `io.debezium.connector.postgresql.PostgresConnector` | `builtin:postgres` v0.15.0 source, `builtin:kafka` destination | postgres v0.15.0: add SDK acceptance suite, Kafka-sink kill test, re-run committed benchi configs; Postgres CDC completion (flush gate, monotonic flush reporting) | Detailed below. |
| `io.debezium.connector.mysql.MySqlConnector` | `mysql` source (labs, move to `ConduitIO`), `builtin:kafka` destination | mysql: move to `ConduitIO`, add `LICENSE`, acceptance suite, port the postgres kill harness, registry entry. Roadmap currently places "MySQL CDC beta" in v0.22 | Gaps below. |
| `io.confluent.connect.jdbc.JdbcSourceConnector` | `postgres` or `mysql` source (CDC), `builtin:kafka` destination | same as the two rows above | Approximation; detailed below. |
| `io.confluent.connect.jdbc.JdbcSinkConnector` | `builtin:kafka` source, `postgres` destination or `mysql` destination | postgres destination and mysql destination certified. Roadmap currently places the JDBC sink family in v0.22 | Other databases: `kc.connector_class_unsupported` plus fix text pointing to the labs trigger-based connectors (not certified) and the v0.23 JAR host. |
| `io.confluent.connect.s3.S3SinkConnector` | `builtin:kafka` source, `builtin:s3` destination | s3: acceptance suite, destination kill test, benchi configuration, registry entry | Detailed below. |
| `io.confluent.connect.elasticsearch.ElasticsearchSinkConnector` | `builtin:kafka` source, `elasticsearch` destination (labs) | elasticsearch: fix failing CI, bump SDK from v0.12, ES and OpenSearch coverage, kill test, benchi. Roadmap currently places this in v0.22 | OpenSearch: same class family; mapped to `elasticsearch` with `version` per the connector; if an OpenSearch mode is added it is selected by `connection.url` probing at run time, not by `migrate`. |
| `org.apache.kafka.connect.mirror.MirrorSourceConnector` (and `MirrorCheckpointConnector`, `MirrorHeartbeatConnector`) | `builtin:kafka` source, `builtin:kafka` destination | kafka: already Certified path per inventory (acceptance and integration present) | Detailed below. |

**Debezium Postgres.**

| KC key | Conduit | Status |
| --- | --- | --- |
| `database.hostname`, `.port`, `.user`, `.dbname`, `.password` | source `url` (`postgres://user:${KC_..._PASSWORD}@host:port/db`) | translated. `database.sslmode` and friends become URL query parameters. |
| `table.include.list` (comma-separated regexes of `schema.table`) | source `tables` | translated when every entry is a literal name; a regex metacharacter makes it `unsupported` with fix "list the tables explicitly, or use `*`" (blocking). `table.exclude.list`: same. |
| `schema.include.list` | folded into `tables` | approximated when combined with a table list, otherwise `unsupported`. |
| `slot.name`, `publication.name` | `logrepl.slotName`, `logrepl.publicationName` | translated under `--slot-mode adopt`; under the default `new` they receive generated names (`conduit_<pipeline>`) and the entry is approximated with reason "separate slot so KC and Conduit can run side by side and rollback stays possible". |
| `plugin.name` | none | `pgoutput`: translated (no-op); `decoderbufs`, `wal2json`: unsupported (blocking), Conduit reads `pgoutput`. |
| `snapshot.mode` | `snapshotMode` (`initial`, `never`) | `initial`: translated; `never`: translated, but `resume` section requires the slot-adopt flow or reports `kc.resume_unavailable`; `always`, `initial_only`, `when_needed`, `no_data`, `exported`, `custom`: unsupported (blocking). |
| `topic.prefix` (and legacy `database.server.name`) | `kafka` destination `topic` template | approximated (blocking, output format differs). Template reproduces `<prefix>.<schema>.<table>` where the schema comes from the table list. |
| `topic.naming.strategy`, `topic.delimiter` | | unsupported for non-default values. |
| `skipped.operations` | `filter` processor with a `condition` on `.Operation` | translated. |
| `column.exclude.list`, `column.include.list` | `field.exclude` on `.Payload.After.<col>` and `.Payload.Before.<col>` with a `condition` on the collection | `exclude`: translated; `include`: unsupported until include-list `ReplaceField` lands (v0.22 pack). |
| `tombstones.on.delete`, `decimal.handling.mode`, `time.precision.mode`, `binary.handling.mode`, `hstore.handling.mode`, `interval.handling.mode`, `provide.transaction.metadata` | | unsupported (blocking): these shape the Debezium envelope; Debezium-compatible output mode is v0.22. |
| `heartbeat.interval.ms`, `heartbeat.action.query` | none | unsupported, non-blocking only if the report also verifies the table list is high-traffic; otherwise blocking. Confirm with the Postgres CDC completion lane whether the connector covers idle-database slot advancement. |
| `signal.*`, `incremental.snapshot.*`, `snapshot.select.statement.overrides`, `snapshot.locking.mode` | | unsupported. |
| `max.batch.size`, `max.queue.size`, `poll.interval.ms` | `sdk.batch.size` / none | approximated (non-blocking). |

_Resume._ A migrated pipeline can start in one of three ways, and the report states which, in
`result.resume`:

1. `new-slot` (default): new slot, `snapshotMode` as translated. A `snapshot.mode=initial` connector
   re-snapshots. Safe to run next to the original connector; rollback is "stop Conduit".
2. `adopt` with offset import: the pipeline reuses the Debezium slot and publication and its position is
   seeded from the connector's offset record in `offset.storage.topic` (`connect-offsets`), so there is no
   re-snapshot. The offset import itself (format, versioning, validation against the slot's
   `confirmed_flush_lsn`) is designed in its own document and is Tier 1. This document only fixes the
   contract: `migrate` emits `result.resume.import = {source: "connect-offsets", topic, key}` and the YAML
   carries no offset data; the import is a separate command run against the Kafka cluster and the
   stopped KC connector.
3. `adopt` without offset import available: refused. `migrate` exits 2 with `kc.resume_unavailable` and the
   fix "use `--slot-mode new` (re-snapshot) or wait for offset import". It never emits an adopt pipeline that
   would silently re-snapshot or skip.

**Debezium MySQL.** Source keys map to labs `mysql`: `database.hostname|port|user|password` to `dsn`,
`table.include.list` to `tables`, `snapshot.mode=initial` to `snapshot.enabled=true`, `schema_only`/`never` to
`snapshot.enabled=false`. Known gaps, all reported unsupported and blocking: `schema.history.internal.*`
(Conduit's connector has no durable schema history, noted in the Debezium roadmap design doc), `database.server.id`
and GTID settings (not exposed), `include.schema.changes`, all envelope-shaping keys, and offset import
(MySQL binlog coordinates are out of scope of the offset-import lane as scoped; MySQL migrations re-snapshot
in v0.21). If the MySQL certification does not land in v0.21, `migrate` still reports this connector but
sets `kc.target_not_certified` and the pipeline `stopped`.

**JDBC source.** There is no polling JDBC source in the catalog. For `connection.url` pointing to PostgreSQL
or MySQL, `migrate` emits the CDC connector as an **approximation**: log-based capture also sees updates
and deletes that `mode=timestamp|incrementing` polling misses, needs `wal_level=logical` (or binlog
access) that polling did not need, and emits OpenCDC records, not the flat rows the JDBC source produced.
Always blocking, code `kc.key_approximated` on `mode`, with the fix text listing the prerequisites. `mode=bulk`
(repeated full table reads) is `unsupported`. Other databases: `kc.connector_class_unsupported`. `query`,
`timestamp.column.name`, `incrementing.column.name`, `topic.prefix` (applied as above) are reported
individually.

**JDBC sink.** Kafka source with `groupID` set to the KC sink's consumer group, `connect-<name>`, and
`readFromBeginning: false`, so a sink picks up from its committed offsets. This is a design assumption that
needs a test before it ships (see _Failure modes_). `connection.url` (`jdbc:postgresql://...`) to destination
`url`/`dsn`; `table.name.format` to `table` (template: `${topic}` becomes the collection metadata);
`insert.mode` `insert` is translated, `upsert` and `update` depend on the destination's key handling and are
approximated until the certification acceptance tests cover them; `pk.mode` and `pk.fields` map to key
handling where the destination supports it, else unsupported; `delete.enabled` translated only with
`pk.mode=record_key`; `auto.create` and `auto.evolve`: unsupported (blocking) because schema drift policy is
not silent coercion (Invariant 6), fix: create the table ahead of time. `batch.size` to `sdk.batch.size`
(approximated, non-blocking). `topics` to `topics`; `topics.regex` unsupported with fix "list topics".

**S3 sink.** Kafka source plus `builtin:s3` destination: `s3.bucket.name` to `aws.bucket`, `s3.region` to
`aws.region`, `store.url` to `aws.endpoint`, `s3.path.style.access` to `aws.pathStyle`, `topics.dir` to
`prefix`, `format.class` `JsonFormat` to `format: json` and `ParquetFormat` to `format: parquet`
(`AvroFormat`, `BytesFormat`: unsupported, blocking). Reported as a blocking approximation regardless of
the other keys: **the object layout differs** (key names, partitioning, file rotation) and downstream readers
(Athena tables, Spark jobs) depend on it. Specifically: `partitioner.class` and `path.format`
(`TimeBasedPartitioner`, `FieldPartitioner`) unsupported; `flush.size` and `rotate.interval.ms` approximated
via `sdk.batch.size` and `sdk.batch.delay` (batch size is not file size); `exactly.once` behavior of the KC
sink (deterministic object names) is not reproduced; Conduit delivers at least once, and the report states
this in a connector-level note. Credentials: `aws.access.key.id` and `aws.secret.access.key` follow _Secrets_;
`s3.credentials.provider.class` is unsupported with fix "use the default AWS credential chain in the
environment" (the connector's static-key requirement is a known issue tracked with #2988).

**Elasticsearch sink.** `connection.url` to `host`, `connection.username` and `connection.password` to
`username`, `password` (secrets), `index.name` or `topic.index.map` to `index` (template where possible),
`batch.size` to `bulkSize`, `max.retries` to `retries`, `type.name` to `type`. `key.ignore=true` (random IDs)
is approximated: document ID semantics differ, fix text explains idempotence loss on replay. `write.method=upsert`,
`behavior.on.null.values`, `behavior.on.malformed.documents`, `drop.invalid.message`, `compact.topic.behavior`
are reported individually; `behavior.on.malformed.documents=warn|ignore` maps to the DLQ, never to
silently skipping a record (Invariant 3). `schema.ignore`, `data.stream.*`: unsupported until the certified
connector defines them.

**MirrorMaker-style Kafka to Kafka.** `MirrorSourceConnector` with `source.cluster.alias`,
`target.cluster.alias`, `source.cluster.bootstrap.servers`, `topics`, `replication.factor`: Kafka source
(`servers`, `topics`) plus Kafka destination with `topic` template that reproduces the MirrorMaker 2 naming
policy (`DefaultReplicationPolicy` prefixes the source alias; `IdentityReplicationPolicy` keeps the name).
Unsupported and blocking: `sync.topic.configs.enabled`, `sync.topic.acls.enabled`, consumer-group offset
translation (`MirrorCheckpointConnector`, `emit.checkpoints.enabled`, `sync.group.offsets.enabled`),
heartbeats, and partition preservation (the Kafka destination partitions by key). The report says plainly
that MirrorMaker's offset translation, the reason many teams run it, has no equivalent in v0.21; a migration
that relies on consumers failing over between clusters must not move.

### Single message transforms and predicates

Principle: **emit-if-exact.** A translator emits a processor only when it can show the given configuration
falls inside the subset where the processor's behavior equals the SMT's. Anything else is `unsupported`
(blocking) with the v0.22 SMT-pack item named in `fix`. An SMT is never "approximately" translated into a
lossy processor, because transforms change data, which is Invariant 6 territory.

Placement: source-connector SMTs run on the source connector's `processors`, sink-connector SMTs on the
destination's, in `transforms=` order. `predicate` and `negate` become the processor `condition`. `$Key`
variants operate on `.Key`, `$Value` variants on `.Payload.After` (or `.Payload` for raw records).

Mapped in v0.21 (existing processors, from the SMT parity table in the catalog inventory):

| SMT | Processor | v0.21 behavior |
| --- | --- | --- |
| `Filter` + any predicate | `filter` + `condition` | translated |
| Predicates `TopicNameMatches`, `HasHeaderKey`, `RecordIsTombstone`, `negate` | `condition` template | translated |
| `DropHeaders` | `field.exclude` on `.Metadata.<header>` | translated |
| `InsertHeader`, `HeaderFrom` | `field.set` on `.Metadata.<header>` (+ `field.exclude` for `operation=move`) | translated for string literals and field copies; non-string typed literals unsupported |
| `RegexRouter` | `field.set` on `.Metadata["opencdc.collection"]` with sprig `regexReplaceAll` | translated; Java regex features outside RE2 make it unsupported (reason lists the construct) |
| `Cast` | `field.convert` | emitted only for a single field to `string`, `int`, `float` or `bool`; width-specific types (`int8`..`int64`, `bytes`) and the whole-value form: unsupported |
| `ReplaceField` | `field.exclude` + `field.rename` | emitted for `exclude` and `renames`; `include` unsupported |
| `TimestampConverter` | `field.convert` (`time`) | emitted only when no `format` and no unit change is requested |
| `Debezium ExtractNewRecordState` | none | when the pipeline source is a native Conduit CDC connector the record is already flat; translated (no-op) for `drop.tombstones` default and no `add.fields`; any option from that set: unsupported |
| `Debezium Filter`, `HeaderToValue`, `ContentBasedRouter` simple form | `filter`/`condition`, `field.set` | per parity table; scripting-language (`language.*`) forms unsupported |
| Any other SMT, including `ExtractField`, `HoistField`, `InsertField`, `ValueToKey`, `MaskField`, `TimestampRouter`, `Flatten`, `ExtractChangedRecordState`, `EventRouter`, `PartitionRouting`, `TimezoneConverter`, `ByLogicalTableRouter` and custom classes | none | unsupported, blocking, code `kc.smt_unsupported`, `fix` names the v0.22 pack entry or "write a processor" with a link to the processor SDK |

`field.set` produces strings, so every `ExtractField`/`HoistField`/`InsertField`/`ValueToKey` is unsupported
until the typed-value work in the v0.22 pack. The v0.22 SMT pack extends this table; adding an SMT is one new
rule plus golden files, with no change to the report schema.

### Converters and Schema Registry

Converters are read from the connector (`key.converter`, `value.converter`, `header.converter`) and fall
back to the worker file; if neither is available the report says `kc.converter_unknown` rather than assuming.

| KC converter | Conduit | Status |
| --- | --- | --- |
| `io.confluent.connect.avro.AvroConverter` | `avro.encode` (sink side: `avro.decode`) with `schema.strategy`, and the engine's Confluent Schema Registry in `conduit.yaml.fragment` (`schema-registry.type: confluent`, connection string, basic or bearer auth from `${VAR}`) | translated; `value.converter.schema.registry.url` and `.basic.auth.*` feed the fragment; `value.subject.name.strategy` `TopicNameStrategy` translated, `RecordNameStrategy` and `TopicRecordNameStrategy` approximated or unsupported depending on `avro.encode`'s subject options (resolved at implementation); `auto.register.schemas=false` translated via `schema.preRegistered.*`; `use.latest.version` and `schema.registry.ssl.*` reported individually |
| `org.apache.kafka.connect.json.JsonConverter` | `json.encode` / `json.decode`, or raw payload | `schemas.enable=false`: translated. `schemas.enable=true` on the **source** side (writes `{schema, payload}` envelopes): unsupported, blocking (no producer-side processor; `unwrap.kafkaconnect` is the inbound half only). On the sink side: translated via `unwrap.kafkaconnect` |
| `org.apache.kafka.connect.storage.StringConverter`, `ByteArrayConverter` | raw payload | translated |
| `io.confluent.connect.protobuf.ProtobufConverter` | decode via the Protobuf support in the Schema Registry integration | translated for sink input once Protobuf decode ships (v0.21 item); source-side encode: unsupported |
| `io.confluent.connect.json.JsonSchemaConverter` | none | unsupported (JSON Schema support is v0.22) |
| Custom converter class | none | unsupported, blocking, `kc.converter_unsupported` |

Schema Registry credentials never appear in the report or pipeline: they are `${VAR}` references. `migrate`
does not contact the registry; `kc diff --registry` does (read-only, below).

### Error handling and the DLQ

`errors.tolerance=none` (default): `dead-letter-queue.window-nack-threshold: 0`, so the first failed record
stops the pipeline, matching KC. `errors.tolerance=all`: a DLQ is mandatory for sinks and `migrate` refuses to
emit `all` without one: if `errors.deadletterqueue.topic.name` is absent the entry is `unsupported`,
blocking, code `kc.errors_all_without_dlq`, fix "set a DLQ topic or choose `none`", because translating it
literally means silently dropping records (Invariant 3). With a DLQ topic: `dead-letter-queue.plugin:
builtin:kafka` with that topic and the connection settings inherited from the pipeline's Kafka connector;
`window-size`/`window-nack-threshold` are set so that the pipeline does not stop on failures, with exact
values pinned by golden files. `errors.deadletterqueue.context.headers.enable` is approximated (Conduit DLQ
records carry the error in metadata). `errors.retry.timeout` and `errors.retry.delay.max.ms` are approximated
by Conduit's pipeline recovery (non-blocking). `errors.log.enable`, `errors.log.include.messages`: translated
(no-op). Source connectors have no KC DLQ, so `errors.tolerance=all` on a source is reported as unsupported
with the reason that Conduit's source-side DLQ arrives in v0.22.

### Worker and Kafka client settings

`bootstrap.servers` to `servers`; `security.protocol` `SSL`/`SASL_SSL` to `tls.enabled: true`;
`sasl.mechanism` to `saslMechanism`; `sasl.jaas.config` is parsed for `username`/`password` into
`saslUsername`/`saslPassword` (password via _Secrets_); `ssl.truststore.*`/`ssl.keystore.*` (JKS/PKCS12
stores) are unsupported with fix "export PEM and set `caCert`, `clientCert`, `clientKey`" because the Kafka
connector takes PEM. `producer.override.*`, `consumer.override.*`, `admin.override.*` are reported
individually with a lint finding (see below). `producer.*`/`consumer.*` worker defaults: `acks`,
`compression.type` to destination `acks`, `compression`; others approximated or unsupported per key.

### Secrets

Rules, all enforced by tests:

1. **No plaintext in any output.** A key is a secret if it matches a built-in list (`password`, `secret`,
   `token`, `key` suffixes, `sasl.jaas.config`, `*.credentials`, `aws.secret.access.key`,
   `connection.password`, `basic.auth.user.info`, …), if its value is a config-provider reference, or if the
   connector's schema marks it as password type. A false negative is the dangerous direction, so the list is
   conservative and `--secret-key REGEX` adds keys.
2. **References, not values.** A secret becomes `${KC_<CONNECTOR>_<KEY>}` (upper-cased, non-alphanumerics to
   `_`) in the pipeline YAML. `secrets.env.template` lists each variable name with an empty value and a
   comment naming the origin key. `report.json` carries `secret: true` and the variable name, never the value.
3. **Config-provider references pass through by name.** `${file:/path:key}`, `${env:NAME}`,
   `${secrets:ns/name:key}`, `${strimzidir:/opt/kafka/...:key}`, `${vault:...}`: `migrate` does not resolve
   them. `${env:NAME}` becomes `${NAME}`; others become `${KC_..}` variables with the original reference in the
   report's `reason` so the operator knows where the value lives. Secret manager integrations (Vault, KMS,
   Kubernetes Secrets) arrive in v0.22; the report names the provider and the fix is "populate the environment
   variable from it".
4. **Input handling.** Plaintext secrets found in the input are not echoed in logs, error messages, panics
   (values are wrapped in a type whose `String`/`Format` redact), JSON output or diffs. The CLI warns once with
   `kc.secret_plaintext` (it is also a lint rule): the input file is itself a leak.
5. **#2988.** Today `${VAR}` in a pipeline YAML is expanded at parse time and the expanded value is stored in
   Conduit's database in plaintext. So a pipeline `migrate` generates is safe at rest only as a file; once
   provisioned, the secret lands in the store. Until #2988's design (store the reference and resolve at
   connector open, or reuse the `CONDUIT_SECRET_*` resolver, or encrypt at rest) ships, `migrate` emits a
   connector-level note `kc.secret_stored_expanded` on every pipeline that carries a secret, and the
   REPORT.md "before you deploy" section says so. We do not invent a workaround in `migrate`; the fix is in
   the engine. This design needs no change when #2988 lands: the contract `${VAR}` in the file stays.

### `conduit kc lint`

Offline lint over the same inputs, usable by teams that never migrate. Output follows the located-finding
shape (`severity`, `code`, `message`, `configPath`, `suggestion`, optional `fix`) so findings render like
`pipelines lint` and are consumable by MCP. Rule codes are `kc.lint.<rule>`. `--rule`/`--ignore` select
rules; `--strict` escalates warnings; severity is per rule.

| Rule | Severity | Detects |
| --- | --- | --- |
| `kc.lint.plaintext_secret` | error | secret-typed key with a literal value instead of a config-provider reference |
| `kc.lint.unknown_key` | warning | key not in the known set for the connector class (typos like `tasks.maxx`); known sets ship as data files per supported class and are version-tagged |
| `kc.lint.deprecated_key` | warning | Debezium 2.x/3.x renamed keys: `database.server.name` to `topic.prefix`, `table.whitelist`/`table.blacklist` to `.include.list`/`.exclude.list`, `database.history.*` to `schema.history.internal.*`, `column.blacklist` family |
| `kc.lint.errors_all_without_dlq` | error | `errors.tolerance=all` without `errors.deadletterqueue.topic.name` on a sink: records silently dropped |
| `kc.lint.dlq_replication` | warning | DLQ topic configured without `errors.deadletterqueue.topic.replication.factor` (defaults to 3 and fails on small clusters) or without `errors.deadletterqueue.context.headers.enable`, so failed records arrive with no error context |
| `kc.lint.tasks_max_exceeds_source_limit` | warning | `tasks.max` greater than 1 on Debezium connectors (they run one task; extra tasks are ignored or rejected) |
| `kc.lint.client_override_policy` | warning | `producer.override.*`, `consumer.override.*`, `admin.override.*` in a connector when the worker's `connector.client.config.override.policy` is `None` (the default, overrides are rejected at validation) or absent from the provided worker file (unknown); and the converse, `All` policy on a shared worker where any connector can retune clients |
| `kc.lint.kip1188_override_risk` | warning (placeholder) | reserved for KIP-1188 override-related behavior; see Open question 4: the rule body is not specified until the KIP text and the worker versions affected are confirmed against primary sources |
| `kc.lint.postgres_slot_drop_on_stop` | warning | Debezium Postgres `slot.drop.on.stop=true` outside test setups: a restart re-snapshots or loses position |
| `kc.lint.postgres_no_heartbeat` | warning | Debezium Postgres with `heartbeat.interval.ms` unset or 0 and a table list on a low-traffic table: idle-slot WAL retention |
| `kc.lint.postgres_snapshot_never_without_slot` | warning | `snapshot.mode=never` where the config also sets `slot.drop.on.stop` or gives no slot name: first start has nothing to resume from |
| `kc.lint.converter_mismatch` | warning | connector converter differs from worker default in a way that changes subject names or wire format (for example Avro on key, JSON on value) |
| `kc.lint.json_schemas_enabled` | info | `JsonConverter` with `schemas.enable=true`: per-message schema overhead and a Conduit migration blocker |
| `kc.lint.jdbc_sink_auto_evolve` | warning | `auto.create`/`auto.evolve=true`: DDL driven by message shape |
| `kc.lint.jdbc_sink_upsert_without_pk` | error | `insert.mode=upsert` with `pk.mode=none` |
| `kc.lint.s3_sink_exactly_once_assumption` | info | S3 sink with deterministic naming relied on for idempotent replays; flagged because it does not survive migration |
| `kc.lint.migration_blocker` | info | any key or SMT that `migrate` would classify `unsupported`/blocking, with the migrate code, so lint is a cheap preview of the migrate report |

`kc.lint.client_override_policy` is based on KIP-458 (connector client config override policy). KIP-1188 is
deliberately not described beyond its placeholder; see Open questions.

Exit codes: 0 clean; 2 when any `error` finding exists (or any finding under `--strict`); 3 on unreachable
`--from-rest`.

### `conduit kc diff`

Two jobs, one command.

**1. Config-version diff.** `conduit kc diff old new` where each side is any supported input (two files, two
git revisions via `old=git:HEAD~1:path`, a file against a live connector via `--from-rest`). The diff is
semantic, not textual: keys are normalized (property vs JSON, ordering, `${...}` references kept verbatim),
and each change is classified:

| Class | Examples | Why it matters |
| --- | --- | --- |
| `identity` | `name`, `connector.class`, `topic.prefix`, `slot.name`, `publication.name`, `database.server.id`, `group.id`-affecting keys, converter changes, `value.subject.name.strategy`, `table.include.list` additions | changes where state or data lives: a new slot or consumer group restarts from scratch, a changed topic or subject orphans consumers |
| `behavior` | `snapshot.mode`, `errors.tolerance`, `insert.mode`, SMT list or order, predicates, `tasks.max` | changes what the connector does to records |
| `tuning` | batch sizes, poll intervals, timeouts | no data effect |
| `secret` | any secret-typed key | value compared by SHA-256 only; the report says `changed` or `unchanged`, never prints a value or a hash prefix |

Each change carries a located finding with the same schema as lint, and re-runs the lint rules on the new
side only to show findings the change introduced (`introduced`, `resolved`, `unchanged`). `--fail-on
identity|behavior|any` sets the exit code, so it runs as a pre-merge CI check on a GitOps repo of connector
configs, which is the "useful without switching runtimes" case.

**2. Schema compatibility against a registry** (`--registry-url URL`, optional `--registry-auth ${VAR}`).
Only read-only registry requests are made: `GET /subjects/{subject}/versions/latest`, `GET /config/{subject}`
and `POST /compatibility/subjects/{subject}/versions/latest` (which evaluates, and does not register).
The command:

1. Derives, per side, the expected subjects from converter, `subject.name.strategy` and topic names
   (`<topic>-key`, `<topic>-value` for the default strategy), applying `RegexRouter`-style topic changes
   found in the SMT chain.
2. Reports **subject drift**: subjects present only on the new side (`kc.diff.subject_added`: no history, so
   no compatibility guarantee for existing consumers) or only on the old side (`kc.diff.subject_orphaned`).
3. For a candidate schema supplied with `--schema SUBJECT=FILE` (typically the new schema a deployment
   will produce), runs the compatibility check at the subject's configured level (falling back to the
   global level) and reports `kc.diff.schema_incompatible` with the registry's own message, the level
   (`BACKWARD`, `FORWARD`, `FULL`, `*_TRANSITIVE`, `NONE`) and the subject. `NONE` is reported as a
   warning: the check cannot fail.
4. Can compare a KC deployment's subjects with those a migrated Conduit pipeline would write
   (`--against-pipeline pipelines/orders-cdc.yaml`), using Conduit's schema extraction subject defaults
   (`sdk.schema.extract.*`), to catch the case where migrating changes subject names. This is the diff the
   migration harness runs.

A registry that is unreachable is `kc.diff.registry_unreachable` (exit 3), never a pass.

### Error codes and `--json`

All commands use the family A envelope. Hard failures set `error` and exit non-zero; domain findings
(report entries, lint findings, diff changes) are in `result`. Codes follow the existing `domain.reason`
form and are public contracts from first release.

| Code | Where | Meaning | Fix hint (always populated) |
| --- | --- | --- | --- |
| `migrate.input_unreadable` | migrate, lint, diff | file missing, unreadable or not valid UTF-8 | path and parser location |
| `migrate.input_unrecognized` | all | format detection failed | pass `--format` |
| `migrate.rest_unreachable` | all with `--from-rest` | worker not reachable or returned non-2xx | URL, status; check auth |
| `migrate.output_exists` | migrate | output directory non-empty | `--force` or another `--out` |
| `kc.connector_class_unsupported` | migrate | class not in the mapping table; all keys listed unsupported, no pipeline file | installed JAR host (v0.23) or custom connector |
| `kc.key_unknown` | migrate | key claimed by no rule | blocking by default |
| `kc.key_unsupported` | migrate | known key, no equivalent | per-rule fix |
| `kc.key_approximated` | migrate | translated with a documented difference | reason states the difference |
| `kc.smt_unsupported` | migrate | SMT, or SMT configuration, outside the exact subset | v0.22 pack entry or custom processor |
| `kc.converter_unsupported`, `kc.converter_unknown` | migrate | converter has no equivalent, or none found | set converter explicitly |
| `kc.errors_all_without_dlq` | migrate, lint | tolerance all without DLQ | add DLQ topic |
| `kc.secret_plaintext` | migrate, lint | literal secret in the input | use a config provider reference |
| `kc.secret_stored_expanded` | migrate | #2988 caveat applies to this pipeline | see #2988 |
| `kc.resume_unavailable` | migrate | adopt requested without offset import | `--slot-mode new` or wait |
| `kc.target_not_certified` | migrate | emitted target is not Certified in the registry index | warning |
| `kc.lint.*` | lint | rule findings as tabled | per rule |
| `kc.diff.*` | diff | `subject_added`, `subject_orphaned`, `schema_incompatible`, `registry_unreachable`, `change_identity`, `change_behavior` | per finding |

Codes are asserted by a table test that fails if a code is emitted but not registered, and a registry of
codes is published in the generated docs (CLAUDE.md: error codes are public).

### Package layout

```text
pkg/kcmigrate/          IR, parsers (properties, rest-export, strimzi), rule registry, report types   (no I/O)
pkg/kcmigrate/rules/    one file per connector class, SMT and converter rules, with the known-key sets as data
cmd/conduit/root/migrate/   kafka_connect.go
cmd/conduit/root/kc/        lint.go, diff.go
```

`pkg/kcmigrate` has no dependency on the engine except the pipeline YAML model and `cmd/conduit/internal/validate`,
which the command runs over its own output: the generated pipelines are validated with
`--resolve-plugins` before the command reports success. A generated file that fails validation is a bug in
the translator and surfaces as `internal.error`, never as a pipeline handed to the user. `doc.go` for each
package states the completeness and no-secrets invariants. Known-key sets are versioned data, so supporting a
new Debezium release is a data change and a golden-file update.

## Testing

Tier 2 baseline (unit, integration, race) plus the following, because the failure this command can cause is
a plausible-looking pipeline that behaves differently.

1. **Golden files.** `pkg/kcmigrate/testdata/<case>/input/*` to `expected/pipelines/*.yaml`,
   `expected/report.json`, `expected/secrets.env.template`, regenerated with `-update` and reviewed in the
   diff like code. Corpus sources, in order of value: configs from design-partner migrations (the
   early-adopter discussion in the roadmap), vendor documentation examples for every class in the mapping
   table (Debezium, Confluent connector docs, Strimzi examples, MirrorMaker 2), and synthetic minimum and
   maximum configs generated from each rule's key set. Real-world configs are scrubbed of hostnames and
   credentials before commit; a scrub check is part of the PR template.
2. **Properties** (native Go loops with seeded generators; rapid/gopter are not in the tree and we do not add
   a dependency for this): _completeness_ (input keys equal report keys, multiset), _no secret leaks_
   (inject canary values into every secret-typed key, assert the canary appears in no output byte, log line
   or error), _determinism_ (byte-identical output for the same input, independent of map order),
   _validity_ (every generated pipeline passes `pipelines validate --resolve-plugins`), _monotone strictness_
   (adding an unknown key can only add entries, never change existing ones).
3. **Fuzzing** on every parser (properties, JSON, Strimzi YAML, REST export) with CI short runs and a
   scheduled long run, per the repository testing standards.
4. **Migration harness** (`tests/migrate`, docker-compose, nightly and on changes under `pkg/kcmigrate`;
   it is not part of the per-PR required set until stable, and this document does not claim it is a live gate):
   for each corpus case that has a runnable fixture, (a) start the source/target systems (Postgres, MySQL,
   Kafka in KRaft mode, Schema Registry, MinIO, Elasticsearch) and a real Kafka Connect worker with the
   original config as the oracle; (b) run `migrate`; (c) start Conduit with the generated pipeline against the
   same inputs; (d) normalize and diff the outputs (topic contents, S3 objects, table rows, index documents)
   with `kc diff`-style semantic comparison; (e) require every difference to be explained by a report entry
   with `approximated` or `unsupported` status that covers that field. An unexplained difference fails the
   case. This turns the report from a claim into a tested property. Harness runs against real Debezium for
   CDC envelope differences until Debezium-compatible output mode lands, at which point the expected set of
   differences shrinks and the cases are tightened.
5. **Resume cases** (Tier 1, with the offset-import lane): `kill -9` Conduit mid-snapshot after adopting a
   slot, restart, verify no gap and no loss; sink group-offset adoption with a still-running KC consumer to
   confirm rebalance behavior and that no record is skipped.
6. **CLI contract tests**: envelope schema golden for all three commands, exit codes, `--json` output has no
   stray stdout, `NO_COLOR`.
7. **Docs.** `conduit.io` docs source and `llms.txt` updated in the same implementation PRs (migration
   guide, per-connector guides, error-code reference, `kc lint` rule reference), plus
   `docs/operations/kafka-connect-cutover.md` runbook (symptom, diagnosis, remediation entries for
   "two consumers on one slot", "report says blocking", "subject drift").

## Alternatives considered

1. **Kafka Connect REST-compatible API instead of a migration command (v0.23).** Conduit would accept
   `POST /connectors` with KC JSON, so Strimzi, Terraform providers and Kafka UI keep working unchanged.
   Lost for v0.21, and kept for v0.23, because it needs the same class-to-connector mapping this document
   defines (it is a client of `pkg/kcmigrate`, not a rival), it hides rather than surfaces the gaps (a
   `POST` cannot return a ten-page compatibility report), and it makes Conduit pretend to be a worker
   without giving the team a pipeline it understands. The REST API is the "drop-in" end state; the report
   is what makes drop-in safe. The package layout above keeps the translator callable from a REST handler.
2. **JVM sidecar that runs the actual KC connector JARs (v0.23 JAR host).** Highest fidelity for any
   connector, including Oracle, Db2, SQL Server and HANA CDC, and the escape hatch for classes we mark
   unsupported. Lost as the v1 answer because it keeps KC semantics, the JVM and the schema-history store
   problem in the operational surface (against "boring to operate"), it is a preview that needs its own
   correctness bar (Debezium roadmap DBZ-7), and it migrates nothing: the team has moved the problem, not
   solved it. It is the fix text for `kc.connector_class_unsupported`, which is the right role.
3. **Accept KC JSON/properties directly as a pipeline config dialect.** Smallest code, but a second
   configuration language with its own semantics forever, and the gaps would be silent at runtime instead of
   loud at migration. Violates "no bespoke DSL" and "do not invent parallel conventions".
4. **LLM-assisted translation (`conduit generate`) as the migrator.** Good at assembling a first draft,
   unacceptable as the source of truth: non-deterministic, no completeness guarantee, no golden files, and a
   hallucinated setting is exactly a silent drop. The report and `kc lint` are, however, ideal input for an
   agent or `generate` to propose fixes for the unsupported entries; the structured `--json` and stable
   codes exist for that.
5. **Separate repository or standalone binary for the migrator.** Rejected: it would drift from the pipeline
   schema and the plugin registry it depends on (it reads the registry index for certification state), and
   users get it with the binary they already download. Revisit if `kc lint`/`kc diff` gain a non-Conduit
   audience that does not want the engine.

## Failure modes

| Failure | Consequence | Mitigation |
| --- | --- | --- |
| Key falls through translation | Silent behavior change | completeness invariant: unknown keys are `kc.key_unknown`, blocking; property test over corpus and fuzz |
| Secret leaks into output, logs or diffs | Credential exposure | redacting value type, canary property test, `kc diff` compares secrets by hash only and never prints it |
| Generated pipeline subtly differs from KC (typed field becomes string, topic layout changes) | Downstream breakage or corrupted data | emit-if-exact for SMTs; blocking approximations stop the pipeline; migration harness requires every observed difference to be in the report |
| **Two consumers on one Postgres slot** (adopt mode while KC still runs) | Slot fights or split delivery | default is `--slot-mode new`; adopt requires offset import, which checks the KC connector is stopped (REST state) and the slot is inactive; runbook |
| **Rollback after adopt**: Conduit advances the slot's confirmed flush, then Debezium is restarted from its older stored offset | Debezium resumes from the slot's later position and the interval is lost to it | state this in the report and runbook: after adopt, rollback means a new slot and a re-snapshot, not "restart KC". This is why `new` is the default and why adopt is Tier 1 with its own design |
| Sink adopts KC's consumer group `connect-<name>` | If the KC sink is still running, rebalances; if Conduit's Kafka source does not honor group offsets on start, it replays from `readFromBeginning` (duplicates) or the latest (loss) | design assumption must be proven by the resume test before the group-adopt behavior ships; until then, `readFromBeginning` is set explicitly and the report says "replays from the beginning", at-least-once preserved |
| At-least-once vs KC's stronger guarantees (S3 sink, exactly-once source, KIP-618) | Duplicates after crash | connector-level note in the report; never claimed equivalent |
| Output-format change (OpenCDC, not Debezium envelope; S3 layout) | Consumers break | blocking entries stop the pipeline; Debezium-compatible output mode (v0.22) removes the Debezium case |
| Input is itself malformed or hostile (huge file, deep JSON, ReDoS in key regexes) | Crash or hang | size and depth limits, fuzzing, RE2 only |
| `--from-rest` against a production worker | Load or exposure | GET only, bounded concurrency, no connector secrets requested beyond what the API returns; a read of a masked value is reported as `[hidden]` and becomes a secret variable |
| Certification slips (a mapped connector is not Certified at release) | The release story ("every emitted connector is certified") breaks | `kc.target_not_certified` warning and `stopped` pipeline; the registry index, not a constant in code, is the source |
| Known-key sets drift as Debezium/Confluent connectors release | False `unknown_key`/`key_unknown` | versioned data files, a CI job that diffs against upstream docs on a schedule |
| Roadmap and catalog drift (see Open questions 1-2) | Plan says v0.22 for items this design treats as v0.21 | resolved before implementation, in ROADMAP.md |

## Upgrade, rollback and compatibility

- Report and lint/diff outputs are public contracts: `reportVersion` bumps only on removal or meaning
  changes; adding entries, rules, codes or fields is additive. Deprecation of a code or field follows
  announce, warn, remove across at least two minor versions.
- Generated pipelines target YAML version 2.2 (the latest at the time of writing) and are validated against
  the running binary's parser; a future config version change is a translator update plus golden files.
- Rule data (known keys, mapping versions) is versioned in-tree; the command prints the Debezium and
  Connect versions its rules were written against.
- `migrate` has no state and applies nothing; rolling back the command means deleting the output directory.
  Rolling back a cutover is covered in Failure modes and the runbook.
- No serialized engine format is introduced or changed by this document. Offset import and slot adoption,
  which touch positions, are specified separately and are Tier 1.

## Observability

- The command emits no telemetry. Its observability is its output: the report, the exit code, and
  `--json`.
- Generated pipelines carry provenance in `description` (connector name, class, tool version, report hash)
  so `pipelines describe` and the UI show where a pipeline came from.
- Operations runbook entries (symptom, diagnosis, remediation) for cutover failures, in
  `docs/operations/kafka-connect-cutover.md`, written with the implementation: lag on the adopted slot,
  `pipelines inspect` for a pipeline held `stopped` by a blocking entry, DLQ growth after `errors.tolerance=all`
  translation, and subject drift visible in Schema Registry.
- Migration progress as a product metric is a number of migrations completed by design partners, tracked
  in the early-adopter discussion, not in the binary.

## Open questions

1. **Release placement.** ROADMAP.md (#2945) puts offset import, MySQL CDC beta, JDBC-sink certification and
   Elasticsearch certification in v0.22, while the v0.21 theme requires every emitted connector certified in
   v0.21 and Postgres resume from Debezium offsets. Confirm v0.21 scope and amend the roadmap.
2. **JDBC source default.** Emit log-based CDC as a blocking approximation (this design), or treat the JDBC
   source as unsupported until a polling source exists?
3. **Blocking semantics.** Write blocking pipelines as `status: stopped` (this design) or refuse to write
   them at all?
4. **KIP-1188.** The roadmap names "KIP-1188 override risks" but the KIP text could not be confirmed from
   primary sources while writing this. The rule is a placeholder; the design needs the KIP's actual
   semantics (which override, which versions) before `kc.lint.kip1188_override_risk` has a body.
5. **#2988 gate.** Ship `migrate` with the `kc.secret_stored_expanded` warning before #2988 is fixed, or
   gate secret-bearing output on it?
6. **Debezium output default.** Is a Postgres CDC pipeline whose output is OpenCDC rather than the Debezium
   envelope blocking until v0.22 (this design), or only a warning?
7. **Heartbeats and idle slots.** Does the Postgres v0.15.0 CDC completion work cover idle-database slot
   advancement? It decides whether `heartbeat.interval.ms` is non-blocking.
8. **MySQL.** No schema history and no binlog offset import in v0.21; acceptable to ship MySQL migration as
   re-snapshot-only?
9. **Kafka group adoption.** Does the `kafka` source start from committed group offsets when `groupID`
   already exists? If not, the sink resume story needs a connector change (Tier 1).
10. **Strimzi API versions.** The design accepts any `kafka.strimzi.io/*` version for `KafkaConnector` and
    `KafkaConnect`; confirm which versions design partners run.

## Related

- `ROADMAP.md` (v0.21 to v0.23; PR #2945), `docs/catalog-inventory.md` (PR #2951; SMT parity, golden paths)
- `docs/design-documents/20260707-cli-output-conventions.md`, `20260708-cli-pipeline-inspect-lint-dryrun.md`,
  `20260722-debezium-compete-roadmap.md`, `20260823-protobuf-schema-support.md`,
  `20260823-avro-codec-archived-decoder-advisories.md`
- Issue #2988 (secrets stored expanded)
- Separate lane: Debezium offset import design (Tier 1)
