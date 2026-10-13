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
implementation PRs it describes are Tier 2 (CLI), except the pieces that touch a Debezium replication slot,
position import or Kafka consumer-group offsets, which are Tier 1 (see _Failure modes_).
Reusing Debezium's own slot is v0.22; v0.21 resumes through a fresh Conduit slot and the Debezium offset import
(see _Resume_ and _Decisions_).

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
  output mode for Postgres (pulled into v0.21, see _Decisions_); KC REST API compatibility and the JAR host (v0.23).

### Problem

A team running Kafka Connect cannot evaluate Conduit without hand-translating every connector: learning a
second config vocabulary, guessing at which SMT maps to which processor, and finding out only in production
that a key they did not know mattered (`slot.name`, `errors.tolerance`, `snapshot.mode`) was silently
different. "Moving off Kafka Connect should be a command" (roadmap principle 5) is only true if the command is
trustworthy about what it did not translate.

### Constraints

- Offline and read-only by default. `migrate` and `kc *` write files and read inputs; they never connect to a
  database, never create a slot, never write to Kafka or the Connect worker. The one network read is
  `--import-offset` (and `--from-rest`), which only reads the Connect REST API or the `connect-offsets` topic.
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
| `--slot-strategy {fresh,reuse}` | Postgres only, same name and values as #341/#3029. `fresh` (default, and the only value that works in v0.21): Conduit creates its own slot and publication while Debezium keeps running. `reuse` (take over Debezium's own slot) is v0.22 and fails with `postgres.import.slot_reuse_unsupported` until then. |
| `--import-offset` | Debezium Postgres only. Reads the connector's committed offset (read-only; `--offsets-from rest` by default per KIP-875, or `topic` for `connect-offsets` or the connector's `offsets.storage.topic`, or `file` for a saved REST export) and writes `logrepl.import.debeziumOffset` plus `snapshotMode: never`, `cdcMode: logrepl`, `logrepl.autoCleanup: false`, `logrepl.requireExistingSlot: true` and the cutover-marker prefix in `debezium.message.prefix.exclude.list` into the pipeline (#3029, "Pipelines the CLI writes"). Alongside it, `--dry-run [--peek]`, `--cutover [--stop-connector] [--no-start]` and the read-only `--check-rollback [--watch]` are specified in #3029, with the other cutover flags listed under _Resume_ (`--without-import`, `--journal-dir`, `--slot-create-timeout`, `--cutover-wait-timeout`, `--start-timeout`, `--emit-cutover-marker`, `--pipelines-path`); this document fixes only how `migrate` feeds them. |
| `--accept-gaps` | Write blocking pipelines as `status: running` instead of `stopped`. Recorded in the report. |
| `--json`, `--quiet`, `--strict` | Per the CLI output conventions. `--strict` exits 2 on any `approximated` entry too. |

Plain `migrate` never applies anything: no `--apply`, no calls to a running Conduit. Handing the output to
`conduit pipelines validate|dry-run|deploy` is a separate, explicit step, which keeps rollback of the command
trivial. **`--cutover` does act**, by design (specified in #3029): it creates replication slots, writes and starts
pipelines, and stops the Debezium connector. It is the only mutating path, it is Postgres-only, and it is gated as
described under _Resume_. Going back to Debezium is a manual runbook in v0.21, gated by the read-only
`--check-rollback`; automated reset and rollback are v0.22 (#3044).

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
4. **Strimzi `KafkaConnector` custom resource** (`apiVersion: kafka.strimzi.io/v1beta2` by default; other
   `kafka.strimzi.io/*` versions are read on a
   best-effort basis with a `kc.input_version_unverified` note; `spec.class`,
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

A Debezium-sourced Postgres pipeline with a Kafka destination is emitted with Debezium-compatible output mode
enabled by default (the mode is a v0.21 deliverable, see _Decisions_). If the build `migrate` targets does not
list the mode, the output-format entry is blocking as in any other combination. Pipeline `status` is `stopped`
when the connector has any **blocking** report entry (below), otherwise `running`. A pipeline for a KC connector whose
`spec.state`/REST state was `PAUSED` is also
written `stopped`.

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
        {"key": "topic.prefix", "status": "translated",
         "reason": "A destination topic template reproduces <prefix>.<schema>.<table>; the pipeline uses Debezium-compatible output mode, so the message format matches too.",
         "target": {"configPath": "/pipelines/0/connectors/1/settings/topic"}},
        {"key": "decimal.handling.mode", "status": "unsupported", "code": "kc.key_unsupported", "blocking": true,
         "reason": "Value \"double\" is not among the encodings Debezium-compatible output mode supports.",
         "fix": "Use \"precise\" or \"string\", or keep consumers on the old encoding by not migrating this connector yet."},
        {"key": "heartbeat.interval.ms", "status": "unsupported", "code": "kc.key_unsupported",
         "reason": "...", "blocking": false, "fix": "..."}
      ]
    }],
    "transforms": [ {"alias": "route", "class": "org.apache.kafka.connect.transforms.RegexRouter",
                     "status": "translated", "processor": "field.set"} ],
    "resume": {"strategy": "fresh", "snapshot": "skipped",
               "import": {"setting": "logrepl.import.debeziumOffset", "source": "connect-offsets", "topic": "connect-offsets", "key": "[\"orders-cdc\",{\"server\":\"orders\"}]"},
               "preflights": "pending", "notes": ["..."]},
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
| `io.debezium.connector.mysql.MySqlConnector` | `mysql` source (labs, move to `ConduitIO`), `builtin:kafka` destination | mysql: move to `ConduitIO`, add `LICENSE`, acceptance suite, port the postgres kill harness, registry entry. MySQL CDC beta is in the v0.21 roadmap (see _Decisions_) | Gaps below; v0.21 is re-snapshot-only. |
| `io.confluent.connect.jdbc.JdbcSourceConnector` | `postgres` or `mysql` source, `builtin:kafka` destination | same as the two rows above | Translated where the config maps, otherwise blocking approximation; detailed below. |
| `io.confluent.connect.jdbc.JdbcSinkConnector` | `builtin:kafka` source, `postgres` destination or `mysql` destination | postgres destination and mysql destination certified. in the v0.21 roadmap (see _Decisions_) | Other databases: `kc.connector_class_unsupported` plus fix text pointing to the labs trigger-based connectors (not certified) and the v0.23 JAR host. |
| `io.confluent.connect.s3.S3SinkConnector` | `builtin:kafka` source, `builtin:s3` destination | s3: acceptance suite, destination kill test, benchi configuration, registry entry | Detailed below. |
| `io.confluent.connect.elasticsearch.ElasticsearchSinkConnector` | `builtin:kafka` source, `elasticsearch` destination (labs) | elasticsearch: fix failing CI, bump SDK from v0.12, ES and OpenSearch coverage, kill test, benchi. in the v0.21 roadmap (see _Decisions_) | OpenSearch is in the v0.21 scope: same class family, mapped to the same Conduit destination; how the target is selected (a `version` value or an OpenSearch mode) is fixed by the certification work, and `migrate` never probes the cluster. |
| `org.apache.kafka.connect.mirror.MirrorSourceConnector` (and `MirrorCheckpointConnector`, `MirrorHeartbeatConnector`) | `builtin:kafka` source, `builtin:kafka` destination | kafka: already Certified path per inventory (acceptance and integration present) | Detailed below. |

**Debezium Postgres.**

| KC key | Conduit | Status |
| --- | --- | --- |
| `database.hostname`, `.port`, `.user`, `.dbname`, `.password` | source `url` (`postgres://user:${KC_..._PASSWORD}@host:port/db`) | translated. `database.sslmode` and friends become URL query parameters. |
| `table.include.list` (comma-separated regexes of `schema.table`) | source `tables` | translated when every entry is a literal name; a regex metacharacter makes it `unsupported` with fix "list the tables explicitly, or use `*`" (blocking). `table.exclude.list`: same. |
| `schema.include.list` | folded into `tables` | approximated when combined with a table list, otherwise `unsupported`. |
| `slot.name`, `publication.name` | `logrepl.slotName`, `logrepl.publicationName` | approximated: the pipeline receives generated names (`conduit_<pipeline>`) with reason "separate slot so KC and Conduit can run side by side and rollback stays possible". Non-blocking. Reusing the Debezium names is v0.22. |
| `plugin.name` | none | `pgoutput` and `decoderbufs` (Debezium's default): translated (no-op). Under the fresh-slot strategy Conduit creates its own `pgoutput` slot, so Debezium's plugin only matters to the offset import, which accepts both (#341 S4). `wal2json`, `wal2json_streaming`: unsupported (blocking); the import refuses them (`postgres.import.plugin_unsupported`). |
| `snapshot.mode` | `snapshotMode` (`initial`, `never`) | `initial`: translated; `never`: translated; with `--import-offset` the pipeline resumes from the imported offset, without it a fresh slot has no history, so the entry is approximated and blocking (`kc.resume_unavailable`) with fix "re-run with `--import-offset`"; `always`, `initial_only`, `when_needed`, `no_data`, `exported`, `custom`: unsupported (blocking). |
| `topic.prefix` (and legacy `database.server.name`) | `kafka` destination `topic` template | translated for Postgres to a Kafka destination (Debezium-compatible output mode). Template reproduces `<prefix>.<schema>.<table>` where the schema comes from the table list. For any other combination (Debezium MySQL to Kafka) approximated and **blocking**, output format differs (OpenCDC). For a non-Kafka destination, approximated with a warning. |
| `topic.naming.strategy`, `topic.delimiter` | | unsupported for non-default values. |
| `skipped.operations` | `filter` processor with a `condition` on `.Operation` | translated. |
| `column.exclude.list`, `column.include.list` | `field.exclude` on `.Payload.After.<col>` and `.Payload.Before.<col>` with a `condition` on the collection | `exclude`: translated; `include`: unsupported until include-list `ReplaceField` lands (v0.22 pack). |
| `tombstones.on.delete`, `decimal.handling.mode`, `time.precision.mode`, `binary.handling.mode`, `hstore.handling.mode`, `interval.handling.mode`, `provide.transaction.metadata` | | translated when the pipeline uses Debezium-compatible output mode (the default for Debezium-sourced Kafka pipelines, which carries the envelope, tombstone and type-encoding behavior); for each option the mode does not yet support, unsupported and blocking. For non-Kafka destinations the keys are unsupported and a warning. The supported option set is defined by the output-mode design (v0.21) and `migrate` reads it from that mode's option list, not from a constant. |
| `heartbeat.interval.ms`, `heartbeat.action.query` | none | unsupported, **warning** (non-blocking): the v0.15.0 Postgres work found no idle-database slot-advancement problem and dropped the heartbeat writer. |
| `signal.*`, `incremental.snapshot.*`, `snapshot.select.statement.overrides`, `snapshot.locking.mode` | | unsupported. |
| `max.batch.size`, `max.queue.size`, `poll.interval.ms` | `sdk.batch.size` / none | approximated (non-blocking). |

_Resume._ For Debezium Postgres, v0.21 migrates with **no re-snapshot** by importing Debezium's committed
offset into a fresh Conduit slot. The design is in `conduit-connector-postgres#341` (Tier 1) and the CLI side
in #3029; this document only fixes how `migrate` feeds them. The sequence:

1. Conduit creates its own slot and publication (`conduit_<pipeline>`) while Debezium is still running.
2. The cutover waits until Debezium's committed offset has passed the new slot's consistent point.
3. Debezium is stopped (`--cutover --stop-connector`, or by the operator).
4. The pipeline starts with an empty position and `logrepl.import.debeziumOffset` set; the connector applies
   the setting only when the position is empty, and skips exactly what Debezium already delivered.

`migrate` emits this as follows:

- With `--slot-strategy fresh --import-offset`, the pipeline file contains `logrepl.import.debeziumOffset` with the
  raw offset read from the REST API (KIP-875), the `connect-offsets` topic or a saved export. This is the only
  place `migrate` talks to anything but its input files, it is read-only, and it never contacts the database.
  The report records `resume.strategy: "fresh"`, `resume.snapshot: "skipped"` and the import locator
  (`resume.import`: source, topic, key). The offset is not a secret.
- Without `--import-offset`, the pipeline has no import setting and the report says `resume.snapshot:
  "will-run"` in plain words: the connector re-snapshots. This is also what `--slot-strategy fresh` alone does, and it
  is safe to run beside the original connector.
- Plain `migrate` (no `--cutover`) writes the pipeline `stopped` and starts nothing. The `--cutover` path of
  #3029 runs in this order:
  1. **Every check that does not depend on Debezium's offset runs first, before Debezium is touched:** blocking
     report entries (a pipeline with blocking entries and no `--accept-gaps` is refused here, never after the
     stop), plugin availability, Postgres version, source identity, publication, tables, the CDC and
     snapshot mode, the Debezium slot-safety refusals below, literal masked secrets in the Debezium config
     (`migrate.kafka_connect.config_masked`), and an existing pipeline with the target name that the cutover
     journal did not write, whatever its position (`migrate.kafka_connect.pipeline_exists`).
  2. Create the Conduit slot while Debezium runs.
  3. Wait until Debezium's committed `lsn_commit` passes the slot's start.
  4. Stop Debezium (`--stop-connector`).
  5. Write the pipeline and **start it by default**, then wait for the import-complete signal. `--no-start`
     opts out and leaves the written pipeline `stopped`.

  **Debezium slot-safety refusals (pre-stop).** The cutover refuses, with a `migrate.kafka_connect.*` code, to
  touch a Debezium connector that is configured so that rollback or the overlap is unsafe:
  - `slot.drop.on.stop=true` (`migrate.kafka_connect.slot_drop_on_stop`): stopping Debezium would drop its
    slot, destroying the rollback path and the position the import depends on.
  - `lsn.flush.mode=manual` or `connector_and_driver`, or `flush.lsn.source=false` (the name depends on the
    Debezium version) (`migrate.kafka_connect.lsn_flush_mode_unsupported`): with `manual` or
    `flush.lsn.source=false` Debezium never moves its slot, so "Debezium has caught up" cannot be established;
    with `connector_and_driver` the driver flushes keepalive LSNs, so Debezium's slot passes its committed offset
    and a restarted Debezium would skip what lies between. #341 S5 and #3029 make the same decision.

  - Debezium 3.4+ `offset.mismatch.strategy` set to `trust_offset`, `trust_greater_lsn` or `trust_slot`, or
    Debezium 2.7 `slot.seek.to.known.offset.on.start=true` (`migrate.kafka_connect.slot_advance_on_start_unsupported`):
    each moves Debezium's slot or its offset at start, with no flushed offset behind the move, so a restart can skip
    changes (#341 S5 item 9).

  These checks run against the live config before the stop, again against the config the cutover saves before
  the stop, and `--check-rollback` runs them against the live config before an operator restarts Debezium by hand.

  **One code family per stage.** `kc.lint.postgres_slot_drop_on_stop` and `kc.lint.postgres_lsn_flush_mode` are
  the lint-time findings for the same configurations: warnings for plain `migrate` and `kc lint`, errors under
  `kc lint --for-cutover`. The `--cutover` refusal itself always emits the `migrate.kafka_connect.*` code above,
  never a `kc.lint.*` or `postgres.import.*` code. This rule covers these slot-safety refusals only; other
  import refusals, such as `postgres.import.slot_reuse_unsupported` or the connector's preflight codes, keep their
  `postgres.import.*` codes. The mapping is fixed: `kc.lint.postgres_slot_drop_on_stop` →
  `migrate.kafka_connect.slot_drop_on_stop`; `kc.lint.postgres_lsn_flush_mode` →
  `migrate.kafka_connect.lsn_flush_mode_unsupported`; `kc.lint.postgres_slot_advance_on_start` →
  `migrate.kafka_connect.slot_advance_on_start_unsupported`.

  The rule is that nothing that can be known to fail beforehand may fail after Debezium has stopped, since a
  failure after the stop is downtime. Checks that need the offset or the new slot's consistent point (steps 2-3)
  cannot move earlier; they are the connector's #341 preflights.
  (Decided 2026-10-12; #3029 states start-by-default.)
- Preflights belong to #341 and run in the connector and in `--dry-run`/`--cutover`, not offline: the
  Postgres minor-version gate for the partial-decoding fix, the publication check and the database checks.
  The report lists them as `resume.preflights: "pending"` with the connector's own descriptions, and the
  no-re-snapshot claim is explicitly conditional on them passing. A failed preflight is a `postgres.import.*`
  refusal, never a silent re-snapshot; re-snapshotting needs `--allow-resnapshot`.
- `snapshot.mode` interacts as in the mapping table above. Snapshot-in-progress and incremental-snapshot
  offsets are refused by the connector, so a Debezium connector that is mid-snapshot cannot be cut over yet.
- `--slot-strategy reuse` (taking over Debezium's own slot, which also avoids the second slot) is v0.22. In v0.21
  `migrate` fails with the single code `postgres.import.slot_reuse_unsupported` and the fix "use
  `--slot-strategy fresh --import-offset`". Its exit code comes from the same `postgres.import.*` mapping as every
  other import refusal (ADR 20260706, deterministic CLI exit codes), not from a rule of its own; the validation
  class (2) is expected, to be confirmed against how the connector registers the code.

**Going back to Debezium (scope cut, DeVaris 2026-10-12).** v0.21 has no `rollback` or `--reset` command. Seven
Tier 1 review rounds kept finding loss paths in automating them, so going back is a manual runbook (#3029, "Runbook:
manual reset and rollback"), and every step that could lose data is gated by the read-only `--check-rollback`
(#341 S12). A restarted Debezium resumes at the greater of its committed offset and its slot's confirmed position,
so the check:

1. reports `unsafe` if Debezium's slot is lost, invalidated, or recreated since the stop, if Debezium's slot was
   ahead of its offset at the stop and Conduit has not yet durably delivered past that point, if Debezium's live
   config fails the slot-safety checks or Kafka Connect's validation, or if both Debezium and the pipeline run;
2. tells the operator when to stop the pipeline, when to restart Debezium, and, with `--watch`, follows the restart
   to prove the slot Debezium streams from is the original one;
3. returns `ready_to_delete_pipeline` once Debezium's confirmed position has passed Conduit's slot, and
   `ready_to_drop` only after the pipeline is deleted, with Conduit's durable progress re-checked against Debezium's
   stop position in both; the operator drops Conduit's slot last.

It never drops a slot, never stops or starts anything and never deletes a pipeline. `--cutover` holds a per-slot
lock, a temporary replication slot that a pooler cannot share (`operation_in_progress`, `lock_unavailable`).
Loss is none for the pipeline's tables; tables Debezium captures outside the pipeline can miss changes Debezium itself
skips after an ungraceful stop, and the check warns about them. Automated reset and rollback are v0.22 (#3044).

**Cutover flags and codes.** #3029 is the source for each; this table is the index.

| Flag | Meaning (#3029 section) |
| --- | --- |
| `--without-import` | the fallback after `identity_mismatch`: start on Conduit's slot with no skip rule, require the slot to exist (Identity proof) |
| `--journal-dir` | where the crash-safe cutover journal lives; persistent in CI (Crash-safe cutover) |
| `--slot-create-timeout` (5m) | bounds `CREATE_REPLICATION_SLOT` (Crash-safe cutover) |
| `--cutover-wait-timeout` (30m) | bounds waiting for Debezium's offset to pass the new slot (`--cutover` starts the pipeline) |
| `--start-timeout` (10m) | bounds waiting for the durable import-complete signal (same) |
| `--emit-cutover-marker` | opt-in non-transactional marker message to move an idle Debezium during `--cutover` (Decisions, Q3) |
| `--pipelines-path DIR` | write the pipeline as a file in Conduit's `pipelines.path` instead of through the API; updates then need an engine reload, and deletes an engine restart (Pipelines the CLI writes) |
| `--check-rollback [--watch] [--watch-timeout 30m] [--pg-url] [--connect-url] [--conduit-url] [--json]`, `--check-rollback --abandon --reason <text>` | read-only apart from its watch log: the safety checks and verdict for going back to Debezium by hand, and ending a failed attempt so `--cutover` can go forward (Going back to Debezium) |

| Code (`migrate.kafka_connect.*`) | When |
| --- | --- |
| `slot_drop_on_stop`, `lsn_flush_mode_unsupported`, `slot_advance_on_start_unsupported`, `config_masked`, `pipeline_exists` | before the stop |
| `identity_mismatch`; `identity_ahead` (warning) | after the stop (Identity proof) |
| `slot_advanced`, `slot_in_use`, `slot_unowned`, `slot_create_blocked` | slot and journal checks, including re-runs |
| `start_failed`, `import_pending` | starting the pipeline |
| `operation_in_progress`, `lock_unavailable` | `--cutover`, while another holds the per-slot lock, or when the lock cannot be taken or is lost (Mutual exclusion) |
| `rollback_check_no_journal`, `rollback_check_stop_incomplete`, `rollback_check_cutover_running`, `rollback_check_slot_lost`, `rollback_check_slot_moved`, `rollback_check_conduit_behind`, `rollback_check_config_unsafe`, `rollback_check_two_writers`, `rollback_check_continuity_unproven`, `rollback_check_connector_state`, `rollback_check_privileges`, `rollback_check_not_caught_up` (`waiting`) | `--check-rollback` (Going back to Debezium) |
| `rollback_in_progress` | `--cutover` while a rollback watch log exists (Crash-safe cutover) |
| `postgres.import.*`, `postgres.logrepl.*` (passed through) | the connector's import refusals, and its slot preconditions outside the import (`slot_missing`, `slot_invalidated`, `slot_plugin`, `publication_missing`), with the connector's fix texts (#341 S13) |
| `offset_not_found`, `ambiguous_partition`, `connector_running`, `offsets_unreachable`, `cutover_timeout`, `cutover_marker_unsupported`, `lossy_source_config` (warning) | reading offsets and the cutover wait |

Because Debezium's slot and offsets are never touched by the fresh strategy, this path exists at all, which is why
fresh is the v0.21 default. #3029 owns the CLI mechanics and #341 the connector side.

**Debezium MySQL.** Source keys map to labs `mysql`: `database.hostname|port|user|password` to `dsn`,
`table.include.list` to `tables`, `snapshot.mode=initial` to `snapshot.enabled=true`, `schema_only`/`never` to
`snapshot.enabled=false`. Known gaps, all reported unsupported and blocking: `schema.history.internal.*`
(Conduit's connector has no durable schema history, noted in the Debezium roadmap design doc), `database.server.id`
and GTID settings (not exposed), `include.schema.changes`, all envelope-shaping keys, and offset import
(MySQL binlog coordinates are out of scope of the offset import, which is Postgres only; MySQL migrations re-snapshot
in v0.21). If the MySQL certification does not land in v0.21, `migrate` still reports this connector but
sets `kc.target_not_certified` and the pipeline `stopped`. For the v0.21 beta, MySQL migration is
re-snapshot-only, and the report says so plainly on every MySQL connector: `resume.snapshot: "will-run"`, with
the note "This migration re-snapshots the selected tables; binlog position is not imported."

**JDBC source.** Translated to the existing `postgres` or `mysql` source (snapshot, then log-based change
capture) where the KC config maps: `connection.url` points at PostgreSQL or MySQL, tables are named
(`table.whitelist`/`table.include.list`), and no free-form `query` is used. `mode=bulk` maps to the snapshot;
`mode=incrementing|timestamp|timestamp+incrementing` map to snapshot plus change capture on the same tables.
That is `approximated` and non-blocking, with the differences stated in `reason`: log-based capture also sees
updates and deletes that polling misses, needs `wal_level=logical` (or binlog access) that polling did not
need, and emits OpenCDC records, not the flat rows the JDBC source produced (the migration harness checks
the last point against the original connector). Where the config does not map (`query`, `mode` plus columns
that have no table-level equivalent, `validate.non.null`, unsupported column types), the entry is
`approximated` and **blocking**, code `kc.key_approximated`, and the pipeline is `stopped`. Other databases:
`kc.connector_class_unsupported`. `timestamp.column.name`, `incrementing.column.name`, `topic.prefix` (applied
as above) are reported individually. The poll-interval keys are approximated, non-blocking.

**JDBC sink.** Kafka source with `groupID` set to the KC sink's consumer group, `connect-<name>`, and
`readFromBeginning: false`, so a sink picks up from its committed offsets. This is a design assumption that
is **a marked dependency**: whether the `kafka` source starts from committed consumer-group offsets is
being checked by the certification lane (open question). Until it is confirmed, `readFromBeginning: true` is
emitted, the entry is approximated, and the report says the sink replays from the beginning (at-least-once
holds, duplicates are possible) (see _Failure modes_). `connection.url` (`jdbc:postgresql://...`) to destination
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
stops the pipeline, matching KC.

**Pipelines that output Debezium format** (Debezium-compatible output mode, #3033) always get
`dead-letter-queue.window-size: 1` and `window-nack-threshold: 0`, written explicitly, never left to the
default. Dead-lettering one event of a key breaks per-key order for consumers, and #3033 refuses any other window
at pipeline start with `debezium.dlq_not_halting`. `migrate` never writes a config that would then fail at
start, so on these pipelines `errors.tolerance=all` is not translated to "does not stop". The entry is
**`approximated`** (non-blocking), code `kc.errors_all_debezium_output`, reason "a Debezium-format pipeline halts
on the first failed record instead of skipping it, so per-key order is kept; the halt needs an operator". It is
approximated rather than blocking because the difference cannot drop or reshape data: halting is the stricter
behaviour. `--strict` escalates it, as it does every approximation. The pipeline is written with the halting DLQ
and its normal status. The code stem matches `kc.errors_all_without_dlq`. `kc lint` reports the same
configuration as `kc.lint.errors_all_debezium_output` (warning). The mapping:

| KC | Pipeline outputs Debezium format | Any other pipeline |
| --- | --- | --- |
| `errors.tolerance=none` | `window-size: 1`, `window-nack-threshold: 0` | `window-nack-threshold: 0` |
| `errors.tolerance=all`, DLQ topic set | `window-size: 1`, `window-nack-threshold: 0`; entry approximated, `kc.errors_all_debezium_output` | DLQ to the topic, window set so the pipeline does not stop (below) |
| `errors.tolerance=all`, no DLQ topic | as the row above | `kc.errors_all_without_dlq`, blocking (sinks) |

For other pipelines: `errors.tolerance=all`: a DLQ is mandatory for sinks and `migrate` refuses to
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
   provisioned, the secret lands in the store. If #2988 is not fixed by the v0.21 release, `migrate` ships
   with a connector-level warning `kc.secret_stored_expanded` on every pipeline that carries a secret, and the
   REPORT.md "before you deploy" section says so. The warning is emitted only when the running engine lacks
   the fix (`migrate` asks the build for the capability), so it disappears once the fix lands, without a new
   `migrate` release. #2988's design (store the reference and resolve at connector open, reuse the
   `CONDUIT_SECRET_*` resolver, or encrypt at rest) lives in the engine, not here. We do not invent a
   workaround in `migrate`, and the file contract `${VAR}` does not change when #2988 lands.

### `conduit kc lint`

Offline lint over the same inputs, usable by teams that never migrate. Output follows the located-finding
shape (`severity`, `code`, `message`, `configPath`, `suggestion`, optional `fix`) so findings render like
`pipelines lint` and are consumable by MCP. Rule codes are `kc.lint.<rule>`. `--rule`/`--ignore` select
rules; `--for-cutover` raises the rules marked "error when linting for cutover" to errors, and `--cutover`
runs the same check itself before stopping Debezium; `--strict` escalates warnings; severity is per rule.

| Rule | Severity | Detects |
| --- | --- | --- |
| `kc.lint.plaintext_secret` | error | secret-typed key with a literal value instead of a config-provider reference |
| `kc.lint.unknown_key` | warning | key not in the known set for the connector class (typos like `tasks.maxx`); known sets ship as data files per supported class and are version-tagged |
| `kc.lint.deprecated_key` | warning | Debezium 2.x/3.x renamed keys: `database.server.name` to `topic.prefix`, `table.whitelist`/`table.blacklist` to `.include.list`/`.exclude.list`, `database.history.*` to `schema.history.internal.*`, `column.blacklist` family |
| `kc.lint.errors_all_without_dlq` | error | `errors.tolerance=all` without `errors.deadletterqueue.topic.name` on a sink: records silently dropped |
| `kc.lint.errors_all_debezium_output` | warning | `errors.tolerance=all` on a Debezium Postgres connector whose migration outputs Debezium format: the migrated pipeline halts on the first failed record instead of skipping it |
| `kc.lint.dlq_replication` | warning | DLQ topic configured without `errors.deadletterqueue.topic.replication.factor` (defaults to 3 and fails on small clusters) or without `errors.deadletterqueue.context.headers.enable`, so failed records arrive with no error context |
| `kc.lint.tasks_max_exceeds_source_limit` | warning | `tasks.max` greater than 1 on Debezium connectors (they run one task; extra tasks are ignored or rejected) |
| `kc.lint.client_override_policy` | warning | `producer.override.*`, `consumer.override.*`, `admin.override.*` in a connector when the worker's `connector.client.config.override.policy` is `None` (the default, overrides are rejected at validation) or absent from the provided worker file (unknown); and the converse, `All` policy on a shared worker where any connector can retune clients |
| `kc.lint.kip1188_override_risk` | warning (placeholder) | reserved for KIP-1188 override-related behavior; placeholder by decision: the rule body is not specified until the KIP text is verified from a primary source |
| `kc.lint.postgres_slot_drop_on_stop` | warning; error when linting for cutover | Debezium Postgres `slot.drop.on.stop=true`: a restart re-snapshots or loses position, and `--cutover` refuses it before stopping Debezium |
| `kc.lint.postgres_lsn_flush_mode` | warning; error when linting for cutover | Debezium Postgres `lsn.flush.mode=manual` or `connector_and_driver`, or `flush.lsn.source=false`: the confirmed position cannot be relied on for cutover or rollback |
| `kc.lint.postgres_slot_advance_on_start` | warning; error when linting for cutover | Debezium Postgres `offset.mismatch.strategy` (`trust_offset`, `trust_greater_lsn`, `trust_slot`) or `slot.seek.to.known.offset.on.start=true`: the slot or the offset moves at start; maps to `migrate.kafka_connect.slot_advance_on_start_unsupported` at cutover |
| `kc.lint.postgres_no_heartbeat` | info | Debezium Postgres with `heartbeat.interval.ms` unset or 0 on a low-traffic table: idle-slot WAL retention on the Debezium side. Informational for migration, since Conduit's Postgres connector does not need it |
| `kc.lint.postgres_snapshot_never_without_slot` | warning | `snapshot.mode=never` where the config also sets `slot.drop.on.stop` or gives no slot name: first start has nothing to resume from |
| `kc.lint.converter_mismatch` | warning | connector converter differs from worker default in a way that changes subject names or wire format (for example Avro on key, JSON on value) |
| `kc.lint.json_schemas_enabled` | info | `JsonConverter` with `schemas.enable=true`: per-message schema overhead and a Conduit migration blocker |
| `kc.lint.jdbc_sink_auto_evolve` | warning | `auto.create`/`auto.evolve=true`: DDL driven by message shape |
| `kc.lint.jdbc_sink_upsert_without_pk` | error | `insert.mode=upsert` with `pk.mode=none` |
| `kc.lint.s3_sink_exactly_once_assumption` | info | S3 sink with deterministic naming relied on for idempotent replays; flagged because it does not survive migration |
| `kc.lint.migration_blocker` | info | any key or SMT that `migrate` would classify `unsupported`/blocking, with the migrate code, so lint is a cheap preview of the migrate report |

`kc.lint.client_override_policy` is based on KIP-458 (connector client config override policy). KIP-1188 is
deliberately not described beyond its placeholder; see _Decisions_.

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
| `kc.errors_all_debezium_output` | migrate | approximated: `errors.tolerance=all` on a pipeline that outputs Debezium format, which always halts on the first failed record (#3033 `debezium.dlq_not_halting`) | accept halting, or keep the connector on Kafka Connect; `--strict` fails on it |
| `kc.secret_plaintext` | migrate, lint | literal secret in the input | use a config provider reference |
| `kc.secret_stored_expanded` | migrate | #2988 caveat applies to this pipeline | see #2988 |
| `kc.resume_unavailable` | migrate | `snapshot.mode=never` without `--import-offset` (a fresh slot has no history to resume from) | re-run with `--import-offset` |
| `postgres.import.slot_reuse_unsupported` | migrate | `--slot-strategy reuse` requested; slot reuse is v0.22 | `--slot-strategy fresh --import-offset` |
| `migrate.kafka_connect.*`, `postgres.import.*` | migrate | offset-import errors defined in #3029 and the connector design (#341), passed through unchanged and indexed under _Resume_ ("Cutover flags and codes"); a failed import never falls back to a re-snapshot without `--allow-resnapshot`. Lint findings for the same Debezium settings are `kc.lint.*`; the `--cutover` refusal is the `migrate.kafka_connect.*` code | per code |
| `kc.input_version_unverified` | migrate, lint, diff | input API version other than the verified default (Strimzi `v1beta2`) | warning |
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
   CDC envelope differences with Debezium-compatible output mode on for Postgres; the expected differences for MySQL and
   non-Kafka destinations stay in the report.
5. **Resume cases** (Tier 1, using #341's real-Debezium harness): the migrate-to-cutover path end to end, with
   zero loss and duplicates equal to the `--peek` prediction, plus `kill -9` of Conduit before and after the
   first ack; (v0.22: the same after reusing the slot); sink group-offset adoption
   with a still-running KC consumer to confirm rebalance behavior and that no record is skipped.
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
| **Two consumers on one Postgres slot** | Slot fights or split delivery | v0.21 never reuses the Debezium slot: Conduit and Debezium each have their own, which is also what makes the overlap during cutover safe. `--slot-strategy reuse` (v0.22) will require the KC connector to be stopped and the slot inactive |
| **Both slots retain WAL while Debezium and Conduit run side by side** | Disk growth on the source database | the cutover waits for Debezium's offset to pass the new slot's consistent point and then stops Debezium; the runbook and `kc.lint` explain the window; mechanics in #341/#3029 |
| **Import applied against an unsafe state** (offset mid-snapshot, slot invalidated or ahead, unsupported Postgres minor version, publication mismatch) | A wrong skip rule loses records | #341's preflights and coded refusals (`postgres.import.*`); no silent fallback; `migrate` reports preflights as pending rather than claiming the result offline |
| **Rollback after cutover** | Debezium's slot is lost or invalidated, or Conduit's slot is dropped before Debezium has caught up | v0.21 has no automated rollback: the operator follows the runbook in #3029, and the read-only `--check-rollback` (S12 of #341) gates every step: it reports a lost, invalidated or recreated Debezium slot, Conduit behind Debezium's stop position, an unsafe Debezium config and two writers, follows slot continuity across the manual restart, and returns `ready_to_drop` only when dropping Conduit's slot is safe; automated reset and rollback are v0.22 (#3044); (v0.22) rollback after slot reuse is harder because the slot was advanced, and is part of that design |
| Sink adopts KC's consumer group `connect-<name>` | If the KC sink is still running, rebalances; if Conduit's Kafka source does not honor group offsets on start, it replays from `readFromBeginning` (duplicates) or the latest (loss) | marked dependency on the certification lane's check of the `kafka` source; until it is confirmed, `readFromBeginning: true` is set explicitly and the report says "replays from the beginning", at-least-once preserved |
| At-least-once vs KC's stronger guarantees (S3 sink, exactly-once source, KIP-618) | Duplicates after crash | connector-level note in the report; never claimed equivalent |
| Output-format change (OpenCDC, not Debezium envelope; S3 layout) | Consumers break | blocking entries stop the pipeline; Debezium-compatible output mode removes the Postgres-to-Kafka case in v0.21; Debezium MySQL and S3 layout remain blocking |
| Input is itself malformed or hostile (huge file, deep JSON, ReDoS in key regexes) | Crash or hang | size and depth limits, fuzzing, RE2 only |
| `--from-rest` against a production worker | Load or exposure | GET only, bounded concurrency, no connector secrets requested beyond what the API returns; a read of a masked value is reported as `[hidden]` and becomes a secret variable |
| Certification slips (a mapped connector is not Certified at release) | The release story ("every emitted connector is certified") breaks | `kc.target_not_certified` warning and `stopped` pipeline; the registry index, not a constant in code, is the source |
| Known-key sets drift as Debezium/Confluent connectors release | False `unknown_key`/`key_unknown` | versioned data files, a CI job that diffs against upstream docs on a schedule |
| Roadmap drift | ROADMAP.md (#2945) still lists items this design now treats as v0.21 | the roadmap amendment is tracked outside this PR; see _Decisions_ item 1 |

## Upgrade, rollback and compatibility

- Report and lint/diff outputs are public contracts: `reportVersion` bumps only on removal or meaning
  changes; adding entries, rules, codes or fields is additive. Deprecation of a code or field follows
  announce, warn, remove across at least two minor versions.
- Generated pipelines target YAML version 2.2 (the latest at the time of writing) and are validated against
  the running binary's parser; a future config version change is a translator update plus golden files.
- Rule data (known keys, mapping versions) is versioned in-tree; the command prints the Debezium and
  Connect versions its rules were written against.
- Plain `migrate` has no state and applies nothing; rolling back the command means deleting the output
  directory. `--cutover` does act; its inverse in v0.21 is the manual runbook gated by `--check-rollback` (see
  _Resume_), and automated rollback is v0.22 (#3044).
- No serialized engine format is introduced or changed by this document. The offset import (#341) and slot
  reuse, which touch positions, are specified separately and are Tier 1.

## Observability

- The command emits no telemetry. Its observability is its output: the report, the exit code, and
  `--json`.
- Generated pipelines carry provenance in `description` (connector name, class, tool version, report hash)
  so `pipelines describe` and the UI show where a pipeline came from.
- Operations runbook entries (symptom, diagnosis, remediation) for cutover failures, in
  `docs/operations/kafka-connect-cutover.md`, written with the implementation: lag and retained WAL on the two slots during
  cutover,
  `pipelines inspect` for a pipeline held `stopped` by a blocking entry, DLQ growth after `errors.tolerance=all`
  translation, and subject drift visible in Schema Registry.
- Migration progress as a product metric is a number of migrations completed by design partners, tracked
  in the early-adopter discussion, not in the binary.

## Decisions

Approved by DeVaris on 2026-10-12, in answer to the open questions in the first draft. The body of this
document has been updated to match; this section is the record.

1. **Scope.** The v0.21 roadmap includes MySQL CDC beta, JDBC sink and source, Elasticsearch and OpenSearch,
   Postgres offset import (via the fresh-slot strategy), and a Debezium-compatible output mode for Postgres (newly
   pulled in). ROADMAP.md
   (#2945) is amended separately; this PR does not edit it.
2. **JDBC source.** Translate to the existing Postgres or MySQL connectors where the Kafka Connect config maps
   (snapshot plus change capture); otherwise `approximated` and blocking.
3. **Blocking gaps.** The pipeline is written as `status: stopped`. Unchanged.
4. **KIP-1188.** `kc.lint.kip1188_override_risk` stays a placeholder until the KIP text is verified from a
   primary source. No semantics are specified.
5. **Secrets (#2988).** `migrate` ships with the `kc.secret_stored_expanded` warning if #2988 is not fixed by the
   release. The warning goes away once the fix lands.
6. **Debezium output format.** A Postgres CDC pipeline to a Kafka destination is a blocking gap unless it uses the
   Debezium-compatible output mode, which ships in v0.21. With the mode available, `migrate` emits it by
   default for Debezium-sourced Kafka-destination pipelines. For other destinations the difference is a warning.
7. **`heartbeat.interval.ms`.** A warning, not blocking. The Postgres v0.15.0 work found no idle-database
   slot-advancement problem and removed the heartbeat writer.
8. **MySQL.** Re-snapshot-only migration is acceptable for the v0.21 beta, and the report states it plainly.
9. **Kafka source resume from existing consumer-group offsets.** Still open; the certification lane is checking.
   Kept as a marked dependency (see _JDBC sink_ and _Failure modes_).
10. **Strimzi.** Default to `v1beta2`.
11. **Offset import (corrected 2026-10-12).** Offset import is usable from `migrate` in v0.21. The fresh-slot
    strategy (`--slot-strategy fresh`) is the v0.21 import path: Conduit creates its own slot while Debezium runs,
    waits until Debezium's committed offset passes the new slot's consistent point, Debezium is stopped, and the
    pipeline resumes from the imported offset (`logrepl.import.debeziumOffset`) with no re-snapshot, subject to
    #341's preflights (Postgres minor-version gate for the partial-decoding fix, publication and database
    checks). Design: `conduit-connector-postgres#341`; CLI: #3029. Only reusing Debezium's own slot
    (`--slot-strategy reuse`) is v0.22 and fails with `postgres.import.slot_reuse_unsupported` until then.
12. **Going back to Debezium (decided 2026-10-12, round-7 completion).** Automated `--reset` and `rollback` are cut
    from v0.21. Going back is a manual runbook (#3029) gated by the read-only `--check-rollback`; automated reset and
    rollback are v0.22 (#3044), to be built against a real Kafka Connect and Debezium harness.
13. **`logrepl.requireExistingSlot` (decided 2026-10-12).** Tri-state, as in #341 S13; `migrate` writes `true` on
    every pipeline it creates. The connector's default refuses slot creation for a non-empty position, a behaviour
    change with a release note.

## Open questions

1. **Kafka source and existing consumer groups** (Decision 9). Does the `kafka` source start from committed
   group offsets when `groupID` already exists? If not, resuming a migrated sink needs a connector change
   (Tier 1), and migrated sinks replay from the beginning in v0.21.
2. **Output-mode option set.** The list of Debezium envelope options the output mode supports (decimal, time,
   tombstone, binary, hstore, interval encodings) is defined by that mode's own design; the mapping rows here
   read it, and any option outside it stays unsupported and blocking.

## Related

- `ROADMAP.md` (v0.21 to v0.23; PR #2945), `docs/catalog-inventory.md` (PR #2951; SMT parity, golden paths)
- `docs/design-documents/20260707-cli-output-conventions.md`, `20260708-cli-pipeline-inspect-lint-dryrun.md`,
  `20260722-debezium-compete-roadmap.md`, `20260823-protobuf-schema-support.md`,
  `20260823-avro-codec-archived-decoder-advisories.md`
- Issue #2988 (secrets stored expanded)
- `conduit-connector-postgres#341` (Debezium offset import design, Tier 1) and #3029 (CLI side)
