# `migrate kafka-connect` v1: per-key mapping tables, report contract, harness corpus, MCP parity and lane plan

## Summary

`20261012-migrate-kafka-connect.md` (#3026, merged) is the design for `conduit migrate kafka-connect`,
`conduit kc lint` and `conduit kc diff`. It already fixes the command surface, the input formats, the
completeness invariant, secrets handling, the lint and diff rule sets, the error-code table and the test layers.
This document does not restate any of that. It closes the gaps between that document and the lane definition of
done (#3003, #3010), and corrects the rows of the base document that the later designs and the connector
certification findings have overtaken.

What this adds:

1. **Per-key mapping tables** for exactly the v0.21 certified set (Debezium Postgres, Debezium MySQL, JDBC
   source, JDBC sink, S3 sink, Elasticsearch/OpenSearch sink, MirrorMaker-style Kafka to Kafka). Every documented
   upstream key has a row: `translated`, `approximated` or `unsupported`. These tables replace the connector
   subsections of the base document's _Connector mapping_ where they differ.
2. **Rule data format.** The tables are the specification of the rule data files, which are also the known-key
   sets for `kc.key_unknown` and `kc.lint.unknown_key`, the `kc diff` classification, and the generated
   per-connector docs. One source, four consumers.
3. **The compatibility report as a public contract**: JSON Schema location and versioning, field semantics,
   ordering, exit-code matrix for all three commands, and the human rendering.
4. **Lane interfaces**: what `migrate` calls in the Postgres lane (#3004, #3029) and the output-mode lane
   (#3032), what the certification lanes (#3005 to #3009) hand to `migrate`, and what the harness (#3010)
   consumes.
5. **The harness corpus format** (#3010 builds the runner, this document fixes the data it reads).
6. **MCP tool parity** for the three verbs.
7. **KIP-1188** lint finding, now that the KIP text has been checked against a primary source.
8. **Lane plan**: the implementation PR sequence with tiers.

This is roadmap item "v0.21: migrate/kc CLI" (#3003), design-document deliverable. Risk tier of this PR: Tier 3
(docs only, no code). The implementation PRs are Tier 2 (CLI) except the offset-import and cutover wiring, which
is Tier 1 and owned by the Postgres lane (see _Lane plan_).

## Context

### What the base document leaves open

Checked against #3003's definition of done and the lane brief:

| Requirement | Base document | Gap |
| --- | --- | --- |
| Map every config key for the six families | Prose subsections for MySQL, JDBC, S3, ES and MirrorMaker; a table for Debezium Postgres only | Only the keys the prose names are classified. The completeness invariant would report the rest as `kc.key_unknown` (blocking), which is correct but unhelpful: the point of the mapping tables is that a known key is classified deliberately. |
| Compatibility report schema, exit codes, error codes | Example JSON, entry field list, error-code table | No schema file, no ordering or determinism rules, no exit-code matrix for `kc diff`, no statement of what the human output looks like. |
| Names of the interfaces to #3004 and #3032 | Described in prose across three documents | No single place says which package or setting `migrate` depends on. |
| `kc lint`: KIP-1188 | Placeholder rule, "not specified until verified" | Verified below. |
| Test strategy feeding #3010 | Golden files and a harness described in outline | No corpus format; "a connector `migrate` can emit with no certification entry fails the harness" has no mechanism. |
| MCP tool parity | One sentence | No tool list, input shape or exposure decisions. |
| Lane plan | None | Added. |

### Rows of the base document that are now stale

Three later documents change the mapping. #3033 asks that changes to the base tables be made in the base document
("Changes to #3026's mapping tables are made there, not here"); this document makes them in one place instead, so an
implementer has one table, and the base document gets a pointer to it:

- **Debezium-compatible output (#3033, merged).** `column.include.list`/`column.exclude.list`,
  `skipped.operations`, `topic.prefix` and the type-handling keys are `translated` into `debezium.*` source
  settings, not into `filter` and `field.exclude` processors. Several keys the base document marks unsupported
  (`message.key.columns`, `message.prefix.*`, `unavailable.value.placeholder`, `include.unknown.datatypes`,
  name adjustment, `extended.headers.enabled`, `header.converter`) are translated.
- **Certification findings (#3034, draft).** The Elasticsearch connector's `bulkSize` and `retries` are no-ops
  today (#155), so mapping `batch.size` and `max.retries` onto them would be a silent drop with a
  `translated` stamp on it. The S3 JSON format writes an OpenCDC-shaped line per record, not the bare value
  line of the Kafka Connect `JsonFormat`. The Postgres and MySQL destinations key on the table's keys or the
  record key, not on `pk.mode`/`pk.fields`.
- **Kafka mirror passthrough (#3038, draft).** MirrorMaker-style preservation of headers, partitions and
  timestamps needs `emitMirrorMetadata` on the Kafka source and `mirror` on the Kafka destination. The base
  document lists partition preservation as unsupported and blocking; with #3038 it is a translation to those two
  settings, still blocking until the option ships in the built-in connector.

One conflict is not resolvable by reading and is raised as an open decision (D1): the base document maps a
whole-table JDBC source to Postgres or MySQL CDC as `approximated` and non-blocking, and in the same paragraph
says the result is OpenCDC records instead of the flat rows the JDBC source produced. Its own definition of
`approximated` says output-format changes that break downstream consumers are blocking.

### Inventory (reuse, extend, consolidate, build)

| Need | Existing | Verdict |
| --- | --- | --- |
| CLI envelope, located findings, exit codes | `20260707-cli-output-conventions.md`, `cmd/conduit/cecdysis` (`envelope.schema.json`), `pkg/conduit/exitcode`, `conduiterr` | **Reuse.** Nothing new. |
| Pipeline validity check on generated output | `cmd/conduit/internal/validate` (`--resolve-plugins`) | **Reuse.** |
| MCP adapter | `cmd/conduit/internal/mcp`, `20260708-mcp-server.md` (content-in, read/write split) | **Extend** with three read tools. |
| Diff-with-explained-differences harness | `tests/debezium` plan with `differences.yaml` (#3033, "Diff harness") | **Reuse the file format and loader** for the migrate harness's explained differences; do not invent a second one. |
| Real-Debezium resume harness | #341 / #3029 | **Reuse** for resume cases; not rebuilt here. |
| Docker-compose fixtures | `test/compose-postgres.yaml`, `test/compose-schemaregistry.yaml` | **Extend.** |
| Chaos / upgrade test homes | `tests/chaos`, `tests/upgrade` | Untouched; resume cases live with #3004. |
| Certification state | Registry index tier (`feat(registry): optional license and tier on index entries`, #2963) | **Reuse** as the source of truth; the harness checks against it. |
| Rule data, report schema, corpus runner | None | **Build.** These are the new surfaces. |

Not built here, by decision: a second config dialect, a separate migrator binary, an LLM translator (base
document, _Alternatives considered_ 3 to 5).

## Constraints

All constraints of the base document stand. Added:

- The mapping tables and the rule data must agree. The tables below are the specification for the first
  implementation; after it lands, the generated reference page (not this document) is the authority, and a test
  fails if the generated page and the rule data differ.
- A mapping row may say `translated` only for a Conduit setting that is implemented and tested in the
  connector version `migrate` targets. A key that maps onto a setting that is a no-op or unreleased is
  `approximated` or `unsupported`, never `translated`.
- Upstream coverage is pinned. Rule data records the upstream version it was written against (Debezium 3.7.0.Final
  for the tables below; Confluent `kafka-connect-jdbc` 10.9.x; S3 and Elasticsearch sink versions in the
  tables). A newer upstream key is `kc.key_unknown` until a data update, which is the intended safe failure.
- No performance claims. None appear here.

## Decision

### Rule data

One YAML file per connector class under `pkg/kcmigrate/rules/data/`, embedded with `go:embed`. A rule is data,
the emission logic for a rule kind is code. Sketch:

```yaml
# pkg/kcmigrate/rules/data/debezium-postgres.yaml
rulesVersion: 1
class: io.debezium.connector.postgresql.PostgresConnector
role: source
upstream: {name: debezium, version: "3.7.0.Final"}
emits:                       # every Conduit plugin this class can produce; read by the harness
  - {plugin: builtin:postgres, role: source}
  - {plugin: builtin:kafka,    role: destination}
keys:
  - key: database.hostname
    status: translated
    emit: {kind: pg-url, part: host}
    diff: identity
  - key: {regex: '^column\.mask\.hash\.v2?\..*'}     # RE2 only
    status: unsupported
    blocking: true
    code: kc.key_unsupported
    reason: "Column hashing is not part of Debezium-compatible output mode in v0.21."
    fix: "Hash the column in a processor before the destination, or keep this connector on Kafka Connect."
    diff: behavior
```

Rule fields: `key` (exact string, `prefix`, or `regex`), `status`, `blocking`, `code`, `reason`, `fix`, `emit`
(kind and arguments, kinds are code), `values` (per-value overrides: the same key may be `translated` for one
value and `unsupported` for another, for example `snapshot.mode`), `diff` (`identity|behavior|tuning|secret`,
consumed by `kc diff`), `lint` (optional rule ids that read this key), `secret` (bool), `aliasOf` (legacy name of
another key). A file also has a `defaults:` section listing upstream defaults that Conduit does not share, each
with the connector-level note it produces (see _Mapping tables_).

**Matching.** A key matches at most one rule. Precedence is exact string, then the longest `prefix`, then `regex`;
two rules of the same kind that match one key are a build error, and a test generates a probe key per rule and
fails if any key matches more than one rule. **Values.** A rule with `values` must end with an `otherwise` branch,
and `otherwise` is `unsupported`, blocking, `kc.key_unsupported`, so a value the table does not list (a new
upstream enum member, a legacy name, a typo) is never silently accepted. **References as values.** A
config-provider reference (`${file:...}`, `${env:...}`) as the value of a key whose status depends on its value
cannot be evaluated offline: the entry is `unsupported`, blocking, `kc.key_unsupported`, fix "set the literal
value in the Kafka Connect config, or resolve it and re-run". For keys whose status does not depend on the value
(a password) the reference passes through, as in the base document.

**Upstream drift.** The committed snapshots (`rules/upstream/<class>.keys.yaml`) are checked two ways. A unit test
requires every snapshot key to match exactly one rule (snapshot keys are a subset of the known keys, so a snapshot
key can never be `kc.key_unknown`). A scheduled job (lane-plan PR 20) fetches the live upstream documentation
pages named in each snapshot header, extracts the key tables, diffs them against the snapshot and fails the nightly
when they differ, with the added, removed and changed-default keys in the failure message.

Consequences, all enforced by tests:

- The known-key set of a class is the key list of its file. `kc.key_unknown` (migrate) and
  `kc.lint.unknown_key` (lint) are the same lookup.
- A `translated` rule must name an `emit` kind; a test resolves every emitted setting against the pinned
  plugin specification (`connector.yaml`/`paramgen` of the built-in or labs connector at the pinned version) and
  fails if the setting does not exist. A connector lane that renames a setting breaks the migrate build, which is
  the intent.
- `blocking: true` rules carry `fix` (CLAUDE.md: every error has a suggested fix).
- The `emits` list is the enumeration the harness uses for the "emitted connector has a certification entry"
  gate (_Harness corpus_, rule 4). It is declared in data and cross-checked by a test against the plugins that
  `emit` kinds can actually produce.
- Docs: the per-connector guide's mapping table is generated from the same files.

### Mapping conventions

The tables follow in _Mapping tables_. Conventions used in all of them:

- **T** = translated, **A** = approximated (non-blocking unless marked), **A!** = approximated and blocking,
  **U** = unsupported (non-blocking, warning), **U!** = unsupported and blocking.
- "Target" names the Conduit setting. `src.` is the source connector, `dst.` the destination, `proc.` a processor.
- Secrets follow the base document's _Secrets_ section and are not repeated per row.
- Keys common to every Kafka Connect connector (`name`, `connector.class`, `tasks.max`, `transforms*`,
  `predicates*`, `key.converter`/`value.converter`/`header.converter`, `errors.*`, `topics`, `topics.regex`,
  `*.override.*`, `topic.creation.*`, `exactly.once.support` and friends) are in the shared table and are not
  repeated in the per-class tables.

### Mapping tables

Sources of the upstream key lists, all fetched on 2026-10-14: Debezium 3.7.0.Final reference (PostgreSQL and
MySQL "connector properties"); Confluent `kafka-connect-jdbc` docs and `master` config classes (latest tag
v10.9.9); Confluent S3 sink docs (changelog 12.1.5) and `kafka-connect-storage-*` `master`; Confluent
Elasticsearch sink 14.x/15.x docs and source, Aiven OpenSearch sink docs; Apache Kafka 4.2 generated docs for
MirrorMaker 2 and the Connect worker and connector configs. Keys that appear only in source and not in the
published docs are included and marked "(src)". Conduit-side settings were read from `connector.yaml` of
`builtin:postgres` v0.14.2 (target v0.15.0), `builtin:kafka` v0.13.0, `builtin:s3` v0.9.4 and
`ConduitIO/conduit-connector-mysql` v0.2.0, and from the `paramgen.go` of
`ConduitIO/conduit-connector-elasticsearch` v0.4.0 (that repository has no `connector.yaml`). Where a row names
a setting that the pinned connector does not have yet (marked **[#NNNN]**), the row's status is the status it has
once that lane merges; until then the rule's `requires` gate (below) downgrades it to `unsupported`, blocking.

**`requires` gate.** A rule may name what its target must provide, in three kinds: `param` (a setting exists in the
plugin specification `migrate` was built with), `capability` (a named behavior listed for that plugin in
`certification.yaml`, such as `debezium-offset-import` or `bulk-item-nack`), and `version` (the plugin version in
`certification.yaml` is at least the stated one). A param existing is not evidence that the behavior is
implemented, so rows whose correctness depends on behavior gate on a capability, which the connector lane adds to
`certification.yaml` in the PR that ships the behavior. Examples: `snapshot.mode` `never`/`no_data` requires
`debezium-offset-import` (the settings `logrepl.import.debeziumOffset` and `logrepl.requireExistingSlot` are not
in `builtin:postgres` v0.14.2); the Elasticsearch per-item nack rows require `bulk-item-nack` (#3007; the failing
item is acked today, `ConduitIO/conduit-connector-elasticsearch#153`). If a gate fails the entry is `unsupported`,
blocking, code `kc.target_capability_missing`, with the lane issue in `fix`. A capability is a behavior contract with
a proving test, not a name. Each is defined by what is observable and by
the test that proves it, and its `certification.yaml` entry references the test ID:

| Capability | Observable contract | Proving test |
| --- | --- | --- |
| `bulk-item-nack` | A failed bulk item is nacked and never acked, and the items after it in the batch are retried (redelivered, not dropped). Reproduces `conduit-connector-elasticsearch#153` | A destination test with a mocked bulk response in which item 0 and a middle item fail; it fails on the `n+1` code and passes after the fix |
| `debezium-offset-import` | A pipeline started with an imported Debezium offset resumes with no gap and no loss (at-least-once; duplicates only inside the documented window) | The #3004 resume test with `kill -9` of Conduit before and after the first ack, against a real Debezium |

```yaml
- plugin: builtin:postgres
  version: ">=0.15.0"
  capabilities:
    debezium-offset-import: {test: "conduit-connector-postgres:TestResume_DebeziumImport_Kill9", required_check: "tests/chaos (race, x3)"}
```

The runner fails the build when a listed capability has no `test`, when that test does not exist in the connector's
repository at the pinned version, or when its CI job is not in the required set of the branch (checked against the
branch-protection contexts of that repository in the nightly; offline the runner checks only that the field is
present and well formed). A capability cannot be claimed by editing a name into the file. For standalone connectors
(`mysql`, `elasticsearch`) the emitted `plugin:` reference pins the version from `certification.yaml`, so a pipeline
cannot start on a connector older than the one its rows were gated on. This is how rows that depend on an unmerged
lane stay honest, and why none of the lanes needs a flag-day with this one.

**Connector-level notes.** Some differences come from keys that are _absent_ from the input, because Kafka
Connect applies a default that Conduit does not. They have no input key to hang an entry on, so rules may emit a
connector-level note in `connectors[].notes[]`: `{code, reason, fix, blocking}`. Notes are outside the
completeness multiset (they are not input keys). Codes: `kc.default_differs` (an absent key's Kafka Connect
default differs from Conduit's behavior), `kc.output_shape_differs` (records or objects differ in shape or layout
from the original connector's), `kc.delivery_semantics_differ` (a stronger Kafka Connect guarantee is not
reproduced), `kc.setting_required` (a Conduit setting with no Kafka Connect counterpart that must be supplied,
emitted as a variable). Each note names the rule that emitted it and is listed in the corpus expectations.

**Absent keys are evaluated mechanically.** A row's status for a value applies equally when the key is absent and
that value is the upstream default. This is a test over data, not a reviewed section. Each class has
`pkg/kcmigrate/rules/upstream/<class>.keys.yaml`, a committed snapshot of the pinned upstream key list (`key`,
`default`, `source`, upstream version); it is the list the tables in this document were built from. For every key
in the snapshot that has a default, the test runs the rule engine on the input `key=<default>` and nothing else.
If the result is not plain `translated` (T and not blocking), the class's `defaults:` section must contain an
entry for that key that emits a note carrying the default's status and `blocking`, or names a connector-level
note that already covers it (`coveredBy: kc.output_shape_differs`, used where a whole family is already blocked
and per-key notes would be noise). A key without such an entry fails the build. A default that depends on other keys
is a `when:` clause (`when: {mode: incrementing}`); the test runs one case per combination of the clause's listed
values, each with those keys set and every other key absent, and once more with the clause keys at their own
upstream defaults. The reverse check also runs: a `defaults:` entry whose default
evaluates to plain `translated` is stale and fails. Moving to a new upstream version means regenerating the
snapshot; new defaults then surface as test failures. A second mechanical test covers the opposite direction,
required Conduit settings that have no upstream default: for every parameter marked required in a pinned plugin
specification, `migrate` run on a minimal valid input must either emit the setting or emit it as a variable with
the blocking note `kc.setting_required` (for example `aws.accessKeyId` on `builtin:s3` v0.9.4, or `version` on the
Elasticsearch destination); a required parameter that is neither fails the build. The list below is the expected
initial output, not the
mechanism:

| Family | Absent key (upstream default) | Note | Blocking |
| --- | --- | --- | --- |
| Postgres | `plugin.name` (`decoderbufs`) | decoder differences not emulated | no |
| Postgres | `table.include.list` (all tables) | `tables: "*"` schema coverage is connector-defined | no |
| Postgres | `skipped.operations` (`t`) | none: truncates skipped, equal to Conduit | not a note |
| MySQL | `database.ssl.mode` (`preferred`) | the emitted DSN would be plaintext: a TLS downgrade. Fix: decide `tls` explicitly | yes |
| MySQL | `include.schema.changes` (`true`), type-handling keys | `coveredBy: kc.output_shape_differs` | yes |
| JDBC sink | `delete.enabled` (`false`) | Conduit deletes rows | yes |
| JDBC sink | `replace.null.with.default` (`true`) | differs only if value schemas define defaults | no |
| JDBC source | table selection absent (every table, discovered continuously) | resolved once at start | yes |
| S3 | `s3.ssea.name` (empty: bucket default applies) | Conduit sends AES256, overriding an SSE-KMS bucket default (`conduit-connector-s3#1047`) | yes |
| S3 | `s3.credentials.provider.class` (`DefaultCredentialsProvider`, the default chain), with no static keys | `builtin:s3` v0.9.4 requires static keys: `kc.setting_required` for `aws.accessKeyId` and `aws.secretAccessKey` until capability `default-credential-chain` (#3009) | yes |
| S3 | `s3.path.style.access.enabled` (`true`) | emitted explicitly when `store.url` is set | not a note |
| Elasticsearch | `write.method` (`insert`) | Conduit merges (update with `doc_as_upsert`), Kafka Connect replaces the document | yes |
| Elasticsearch | `schema.ignore` (`false`) | mappings from the Connect schema vs dynamic mapping | yes |
| Elasticsearch | `key.ignore` (`false`) | `T` only for String or JSON-primitive key converters | when not |
| MirrorMaker | `topics` (`.*`) | every topic mirrored by default; Conduit needs a list | yes |
| MirrorMaker | `sync.topic.configs.enabled`, `sync.topic.acls.enabled`, `emit.checkpoints.enabled` (all `true`) | topic configs, ACLs and checkpoints are not synced | yes |
| MirrorMaker | `refresh.topics.enabled` (`true`) | new topics are not discovered | no |

**Type-handling pattern.** Keys that change how a value is represented (`numeric.mapping`, `db.timezone`,
`timestamp.granularity`, `date.calendar.system`, `time.precision.mode` on non-Debezium output, and the like) are
`unsupported` and blocking when set to a non-default value, and `unsupported` non-blocking (reason: "the
connector's own type mapping applies, certified by the lane's corpus cases") when set to the default. Never
`translated`: representation parity is a property of a connector pair that only a harness case can establish.

#### Shared: every Kafka Connect connector

Connector config keys common to all classes, and worker properties (`--worker`). Source: Kafka 4.2 generated
`connect_config`, `source_connector_config`, `sink_connector_config`.

| Upstream key | Status | Conduit | Notes |
| --- | --- | --- | --- |
| `name` | T | pipeline `id` and file name | |
| `connector.class` | T | selects the rule file | Unknown class: `kc.connector_class_unsupported`, all keys listed, no pipeline |
| `connector.plugin.version` | U | none | Conduit pins plugin versions in the registry, not per pipeline. Warning |
| `tasks.max` | T when 1; A when greater | none | Non-blocking. A pipeline has one reader (v0.24 partition claims). Sinks: fix text points to `sdk.batch.size` |
| `tasks.max.enforce` | T | none | No-op |
| `key.converter`, `value.converter`, `header.converter` and `.plugin.version`, `.*` subkeys | per _Converters_ | `avro.*`/`json.*` processors, schema-registry fragment | Base document |
| `transforms`, `transforms.<alias>.type`, `.predicate`, `.negate`, `.<param>`; `predicates`, `predicates.<alias>.type`, `.pattern`, `.<param>` | per _SMTs_ | connector `processors`, `condition` | Every sub-key gets its own entry; the `transforms[]` view indexes them |
| `config.action.reload` | T | none | No-op |
| `errors.tolerance`, `errors.log.enable`, `errors.log.include.messages`, `errors.retry.timeout`, `errors.retry.delay.max.ms`, `errors.deadletterqueue.*` | per _Error handling_ | `dead-letter-queue` | Base document, including the Debezium-format halting rule |
| `topics` | T | source `topics` (sinks only) | |
| `topics.regex` | U! | none | Fix: list topics |
| `topic.creation.groups`, `topic.creation.<group>.*` | U! | none | Conduit's Kafka destination creates topics with broker defaults; partition count, replication factor and `cleanup.policy` (compaction, tombstones) can differ. Fix: create topics ahead of time |
| `exactly.once.support` | U! when `required`; T otherwise | none | Conduit is at-least-once. `requested` is a no-op |
| `transaction.boundary`, `transaction.boundary.interval.ms` | U | none | Meaningful only with exactly-once |
| `offsets.storage.topic` | T | input to `--offsets-from topic` | Used by the Postgres import only. Other classes: U with note "offset not imported; re-snapshot" |
| `producer.override.acks`, `.compression.type`, `.client.id`, `.security.protocol`, `.sasl.*` | T | destination `acks`, `compression`, `clientID`, TLS and SASL settings | `acks` map: Kafka `all` and `-1` to `all`, `1` to `one`, `0` to `none`. Security keys follow _Worker and Kafka client settings_ |
| `consumer.override.auto.offset.reset` | T | source `readFromBeginning` (`earliest` true, `latest` false) | |
| `consumer.override.group.id`, `.client.id`, `.security.protocol`, `.sasl.*` | T | source `groupID`, `clientID`, TLS and SASL | |
| `producer.override.bootstrap.servers`, `consumer.override.bootstrap.servers` | T | destination / source `servers` | The connector's clients talk to other brokers than the worker's; never tuning (corrected 2026-10-15) |
| any other `producer.override.*`, `consumer.override.*`, `admin.override.*` | U | none | Tuning (`linger.ms`, `batch.size`, `max.request.size`, `fetch.*`, `max.poll.records`). Non-blocking; `kc.lint.client_override_policy` and `kc.lint.kip1188_override_risk` apply |
| Worker: `bootstrap.servers` | T | `servers` | |
| Worker: `group.id`, `config.storage.*`, `offset.storage.*`, `status.storage.*`, `plugin.path`, `plugin.discovery`, `listeners`, `admin.listeners`, `rest.*`, `access.control.*`, `offset.flush.*`, `task.shutdown.graceful.timeout.ms`, `heartbeat.interval.ms`, `rebalance.timeout.ms`, `session.timeout.ms`, `worker.*`, `scheduled.rebalance.max.delay.ms`, `connections.max.idle.ms`, `request.timeout.ms`, `retry.backoff.*`, `reconnect.backoff.*`, `metadata.*`, `receive.buffer.bytes`, `send.buffer.bytes`, `client.dns.lookup`, `metrics.*`, `metric.reporters`, `topic.tracking.*`, `response.http.headers.config`, `rest.extension.classes` | T | none | No worker cluster in Conduit; reason recorded. `offset.storage.file.filename` (standalone): U with fix "export offsets through the REST API" |
| Worker: `key.converter`, `value.converter`, `header.converter` | T | inherited by connectors that set none | |
| Worker: `config.providers`, `config.providers.<name>.*` | T | none | Handled by _Secrets_: references pass through by name |
| Worker: `connector.client.config.override.policy`, `connector.client.config.override.allowlist` | T | none | Read by the lint rules only |
| Worker: `security.protocol`, `sasl.*`, `ssl.*` (top level) | T | none | No-op: they configure the worker's own clients (internal topics, group membership) and never reach a connector's producer or consumer (corrected 2026-10-15, see the note below the table) |
| Worker: `producer.bootstrap.servers`, `consumer.bootstrap.servers` | T | destination / source `servers` | Override the worker's `bootstrap.servers` for every connector's producer or consumer |
| Worker: `producer.`/`consumer.` + `security.protocol`, `sasl.mechanism`, `sasl.jaas.config`, `client.id`; `producer.acks`, `producer.compression.type` | T | destination / source TLS, SASL, `clientID`, `acks`, `compression` | The middle layer of a connector's client config; a connector override wins over it |
| Worker: `producer.ssl.*`, `consumer.ssl.*` | U! | none | Fix "export PEM and set `caCert`, `clientCert`, `clientKey`" |
| Worker: `producer.*`, `consumer.*`, `admin.*` other | U | none | Tuning |
| Worker: `exactly.once.source.support` | U! when `enabled` or `preparing`; T when `disabled` | none | |

**How Kafka Connect layers a connector's client config (corrected 2026-10-15).** Apache Kafka 4.2.0,
`connect/runtime/src/main/java/org/apache/kafka/connect/runtime/Worker.java`: `baseProducerConfigs` (lines
837-874) and `baseConsumerConfigs` (lines 911-942) start from the worker's `bootstrap.servers` and Connect's own
client defaults, then add `config.originalsWithPrefix("producer.")` (line 862) or `("consumer.")` (line 931),
then the connector's `producer.override.`/`consumer.override.` keys. The worker's top-level `security.protocol`,
`sasl.*` and `ssl.*` are not in that chain; they configure only the worker's own clients. The first version of
this table mapped them onto every pipeline, which would have enabled TLS or SASL on pipelines whose Kafka Connect
clients ran without them, and treated `producer.bootstrap.servers` and `producer.override.bootstrap.servers` as
tuning, which would have written to the wrong cluster. Found in review of #3080. Connect's own client defaults
(`acks=all`, idempotence off, `max.in.flight.requests.per.connection=1`, `auto.offset.reset=earliest`, group
`connect-<name>`) are absent-key defaults of this layer and are tracked separately (they must be modelled before
a sink family ships).

**Which pipeline connector the worker reaches (corrected 2026-10-15).** The chain above builds Kafka Connect's own
client of a connector: the producer of a source connector, the consumer of a sink. In the pipeline that is the
`builtin:kafka` destination of a source, or the `builtin:kafka` source of a sink, and only that connector gets
worker keys. A pipeline's other Kafka connector is the connector's own client (MirrorMaker's consumer of the
source cluster): no worker key reaches it, not `bootstrap.servers` and not `consumer.*`, and a missing source
cluster is a blocking `kc.setting_required`, never the worker's cluster. Ownership of a setting is decided by
origin (worker or connector), key and layer, so a worker key and a connector key of the same name are different
keys. Rule files name layers (`client`, `cluster-client`, `override`, `fallback`) instead of numbers, and
`override` is above every other connector layer. A `fallback` cluster (MirrorMaker's `target.cluster.*`) is used
as a whole or not at all: when any other layer names the side's brokers, none of its keys is written and each
entry says so, so two clusters' settings never mix. Entries a higher layer replaced are marked after every layer
is applied, and a variable whose setting was replaced is not listed in `secrets.env.template`. Found in review
of #3117.

#### Debezium PostgreSQL

`io.debezium.connector.postgresql.PostgresConnector` to `builtin:postgres` source (v0.15.0) and `builtin:kafka`
destination, Debezium-compatible output mode on (`debezium.enabled: true`, **[#3032]**). A Kafka Connect source
always writes to Kafka and v1 has no destination override, so the destination is always `builtin:kafka`. The
supported values for each `debezium.*` setting are read from the plugin specification (`inclusion` validations),
not from this table: a value outside it is `unsupported`, blocking, whatever the row says.

| Upstream key | Status | Conduit | Notes |
| --- | --- | --- | --- |
| `database.hostname`, `.port`, `.user`, `.dbname` | T | source `url` | |
| `database.password` | T | source `url` (variable) | Secret |
| `database.sslmode` | T | `url` `sslmode` | Same value set as libpq |
| `database.sslcert`, `.sslkey`, `.sslrootcert` | A | `url` `sslcert`, `sslkey`, `sslrootcert` | Non-blocking. Paths are copied; they must exist on the Conduit host |
| `database.sslpassword` | T | `url` `sslpassword` | Secret |
| `database.sslfactory`, `database.connection.factory.class`, `driver.*` | U! | none | Custom connection and trust behavior. Fix: express the settings as libpq URL parameters |
| `database.tcpKeepAlive` | T | none | No-op: the Go dialer keeps connections alive |
| `database.initial.statements` | U! | none | Session statements (`SET TIME ZONE`, `SET ROLE`) can change what is read |
| `database.query.timeout.ms` | U | none | |
| `plugin.name` | `pgoutput` T; `decoderbufs` A; others U! | none | Conduit decodes with `pgoutput` only (#3033 non-goal: decoderbufs semantics). `decoderbufs`, Debezium's default and the value when the key is absent, is non-blocking and states that decoder-specific differences are not emulated. `--import-offset` accepts both `pgoutput` and `decoderbufs` offsets (#341 S4, the offset format is the same); other plugins are refused (`postgres.import.plugin_unsupported`) |
| `slot.name`, `publication.name` | A | `logrepl.slotName`, `logrepl.publicationName` | Generated names `conduit_<pipeline>`; reason "separate slot so rollback stays possible" |
| `slot.drop.on.stop` | `false` T; `true` U | `logrepl.autoCleanup: false` | `true`: warning in `migrate`, refusal at `--cutover` (`migrate.kafka_connect.slot_drop_on_stop`) |
| `slot.failover` | U! when `true` | none | A failover slot survives a primary switch; Conduit's slot does not. Fix: keep on Debezium until the connector creates failover slots |
| `offset.mismatch.strategy` | `no_validation` T; others U | none | Others: warning in `migrate`, refusal at `--cutover` (`slot_advance_on_start_unsupported`) |
| `lsn.flush.mode`, `flush.lsn.source` | `connector` / `true` T; `manual`, `connector_and_driver`, `false` U | none | Same cutover refusal as above (`lsn_flush_mode_unsupported`) |
| `lsn.flush.timeout.ms`, `lsn.flush.timeout.action` | U | none | |
| `slot.stream.params` | U! | none | Plugin options (for example table filters) change what is decoded |
| `slot.max.retries`, `slot.retry.delay.ms`, `retriable.restart.connector.wait.ms`, `errors.max.retries` | A | pipeline recovery | Non-blocking |
| `status.update.interval.ms`, `xmin.fetch.interval.ms`, `read.only` | U | none | |
| `publication.autocreate.mode` | A | none | The cutover creates a publication for the listed tables; Debezium's publication is not reused (v0.22 with slot reuse) |
| `publish.via.partition.root` | `false` T; `true` U! | none | Partitioned-table event shape; unsupported until #3032 lists it |
| `replica.identity.autoset.values` | U | none | Debezium would set replica identities on the source; existing settings persist, new tables are not altered |
| `topic.prefix`; legacy `database.server.name` | T | `debezium.topic.prefix` | Legacy name adds `kc.key_deprecated` (below) |
| `topic.delimiter`, `topic.naming.strategy` | default T; other U! | `debezium.topic.*` | |
| `topic.cache.size`, `topic.transaction`, `topic.heartbeat.prefix`, `topic.heartbeat.name` | U | none | |
| `schema.include.list`, `schema.exclude.list` | A when combined with a literal table list; U! otherwise | folded into `tables` | |
| `table.include.list` | T for literal `schema.table` entries; U! for regex | source `tables` | Regex metacharacters make it blocking. Fix: list tables explicitly |
| `table.exclude.list` | U! | none | `tables` has no exclusion. Fix: list the included tables |
| `column.include.list`, `column.exclude.list` | T | `debezium.column.include.list`, `.exclude.list` | Applied in the source so the schema descriptor matches |
| `message.key.columns` | T | `debezium.message.key.columns` | |
| `message.prefix.include.list`, `.exclude.list` | T | `debezium.message.prefix.*` | `--import-offset` appends the cutover marker to the exclude list |
| `skipped.operations` | T when `t` is in the list; otherwise U! | `debezium.skipped.operations` | Conduit's Postgres source never emits truncates (connector.yaml, postgres#344). A value that omits `t` means Debezium emitted truncate events that Conduit would drop. Default `t`: T. Relax when #3032 emits them |
| `skip.messages.without.change` | U! | none | Debezium suppresses events with no change in captured columns; Conduit would emit them |
| `tombstones.on.delete` | T | `debezium.encode` `tombstones.on.delete` | |
| `provide.transaction.metadata` | `false` T; `true` U! | `debezium.provide.transaction.metadata` | The `<prefix>.transaction` topic is not produced (#3033 D12) |
| `time.precision.mode`, `decimal.handling.mode`, `binary.handling.mode`, `interval.handling.mode`, `hstore.handling.mode`, `money.fraction.digits` | T for values in the spec; U! otherwise | `debezium.<same name>` | Example: `decimal.handling.mode=double` is U! |
| `unavailable.value.placeholder`, `include.unknown.datatypes`, `schema.name.adjustment.mode`, `field.name.adjustment.mode`, `extended.headers.enabled` | T | `debezium.<same name>` | `extended.headers.enabled` unset: `migrate` sets it from the plugin version (#3033 settings table) |
| `column.mask.with.<n>.chars`, `column.mask.hash.*`, `column.truncate.to.<n>.chars`, `column.propagate.source.type`, `datatype.propagate.source.type`, `converters`, `converters.<name>.*` | U! | none | Fix: mask or truncate in a processor before the destination, or keep the connector on Kafka Connect |
| `schema.refresh.mode` | U | none | |
| `snapshot.mode` | `initial` T; `never` and `no_data` (the same behavior; `never` is the older name) T with `--import-offset`, else A! (`kc.resume_unavailable`); `initial_only`, `always`, `when_needed`, `configuration_based`, `custom`, legacy `exported`, `schema_only`, `schema_only_recovery` and any unlisted value U! | `snapshotMode` (`initial`, `never`) | `never`/`no_data` require capability `debezium-offset-import` |
| `snapshot.mode.configuration.based.*`, `snapshot.mode.custom.name` | U when `snapshot.mode` is neither `configuration_based` nor `custom`; U! otherwise | none | |
| `snapshot.locking.mode` | `none` T; others U! | none | `snapshot.locking.mode.custom.name`: same |
| `snapshot.query.mode` | `select_all` T; `custom` U! | none | `snapshot.query.mode.custom.name`: same |
| `snapshot.select.statement.overrides`, `snapshot.select.statement.overrides.<table>` | U! | none | Snapshot content would differ |
| `snapshot.include.collection.list` | U! | none | |
| `snapshot.isolation.mode`, `snapshot.lock.timeout.ms`, `snapshot.delay.ms`, `streaming.delay.ms`, `snapshot.max.threads`, `snapshot.max.threads.multiplier`, `legacy.snapshot.max.threads` | U | none | |
| `snapshot.fetch.size` | T | `snapshot.fetchSize` | |
| `signal.data.collection`, `signal.enabled.channels`, `signal.kafka.*`, `signal.consumer.*`, `incremental.snapshot.*`, `notification.enabled.channels`, `notification.sink.topic.name` | U! | none | Ad hoc and incremental snapshots (#3033 D16) |
| `heartbeat.interval.ms`, `heartbeat.action.query` | U | none | Decision 7 of the base document: warning |
| `event.processing.failure.handling.mode` | `fail` T; `warn`, `skip` U! | none | `warn` and `skip` drop events (invariant 3) |
| `max.batch.size` | A | `sdk.batch.size` | Non-blocking |
| `max.queue.size`, `max.queue.size.in.bytes`, `poll.interval.ms`, `poll.dispatch.interval.ms`, `statistics.metrics.enabled`, `custom.metric.tags`, `custom.sanitize.pattern`, `guardrail.collections.max`, `guardrail.collections.limit.action` | U | none | |
| `schema.history.internal.*`, `database.history.*` | U | none | Not used by the Postgres connector |
| Legacy renames: `table.whitelist`/`blacklist`, `schema.whitelist`/`blacklist`, `database.whitelist`/`blacklist`, `column.whitelist`/`blacklist` | as the modern key | | `aliasOf` in rule data; adds `kc.key_deprecated` (A, non-blocking) |
| Legacy with changed values: `truncate.handling.mode`, `sanitize.field.names`, `toasted.value.placeholder` | U! | none | Fix: use `skipped.operations`, `field.name.adjustment.mode`, `unavailable.value.placeholder` |

Notes emitted: none when the `requires` gates pass. When `debezium.enabled` is not in the plugin specification the
whole connector carries the blocking note `kc.output_shape_differs` and each `debezium.*` row is `unsupported`,
blocking (`kc.target_capability_missing`). The `resume` object follows the base document (`will-run` without
`--import-offset`).

#### Debezium MySQL

`io.debezium.connector.mysql.MySqlConnector` to `ConduitIO/conduit-connector-mysql` source and `builtin:kafka`
destination. The connector emits OpenCDC records; the Debezium-compatible output mode for MySQL is v0.22
(#3032 scope). Every Debezium MySQL to Kafka migration therefore carries the blocking note
`kc.output_shape_differs` ("consumers of the Debezium envelope break") and re-snapshots (no offset import, v0.22).

| Upstream key | Status | Conduit | Notes |
| --- | --- | --- | --- |
| `database.hostname`, `.port`, `.user`, `.password` | T | source `dsn` | Password is a secret |
| `database.protocol`, `database.jdbc.driver` | U | none | JDBC only |
| `connect.timeout.ms` | A | `dsn` `timeout` | |
| `database.ssl.mode` | `disabled` T; others A!; absent (`preferred`) A! | `dsn` `tls` | Blocking until the binlog stream honors TLS (#3034 finding, #3005). A DSN is never emitted without an explicit `tls` decision |
| `database.ssl.keystore*`, `database.ssl.truststore*` | U! | none | Fix: export PEM |
| `database.include.list` | T for one literal database; U! otherwise | folded into `tables` | The connector reads one database |
| `database.exclude.list`, `table.exclude.list` | U! | none | |
| `table.include.list` | T for literal `db.table`; U! for regex | `tables` | Relax when #3005 documents the connector's pattern semantics |
| `table.ignore.builtin` | U | none | |
| `column.exclude.list` | T | `field.exclude` processors | Emit-if-exact |
| `column.include.list` | U! | none | |
| `snapshot.mode` | `initial` T; `always` A!; `never`, `schema_only`, `no_data`, `recovery` (current name), `schema_only_recovery` (legacy), `when_needed`, `configuration_based`, `custom`, `initial_only` and any unlisted value U! | `snapshot.enabled` | `initial_only` is U!, `kc.key_unsupported`: Debezium stops after the snapshot, and no Conduit setting stops a MySQL source after its snapshot (`cdc.disableLogs` only silences the canal driver's logging, `source.go`). `never`, `schema_only` and `no_data` would start at the current binlog position, so events between Debezium's last position and the pipeline's first start are not delivered (invariant 3); blocking with code `kc.resume_unavailable`, and `snapshot.enabled: false` is not emitted. `always` re-snapshots on every restart (#158) |
| `snapshot.fetch.size` | T | `snapshot.fetchSize` | |
| `snapshot.locking.mode` | `minimal` A; others U! | none | The connector takes a brief global read lock |
| `snapshot.tables.order.by.row.count`, `snapshot.max.threads*`, `legacy.snapshot.max.threads`, `min.row.count.to.stream.results`, `snapshot.delay.ms`, `streaming.delay.ms`, `snapshot.lock.timeout.ms`, `snapshot.query.mode`, `snapshot.mode.*` | U | none | `snapshot.query.mode=custom`, `snapshot.mode.custom.name`: U! |
| `snapshot.select.statement.overrides*`, `snapshot.include.collection.list` | U! | none | |
| `database.server.id` | U | none | Warning. The connector does not expose it; two replicas with one id disconnect each other. Verify the pipeline's id differs (#3005) |
| `gtid.source.includes`, `gtid.source.excludes`, `gtid.ignore.on.recovery` | U! | none | Positions are binlog file and offset |
| `binlog.buffer.size`, `binlog.net.read.timeout`, `binlog.net.write.timeout`, `connect.keep.alive*`, `enable.time.adjuster`, `use.nongraceful.disconnect`, `read.only` | U | none | |
| `signal.*`, `incremental.snapshot.*`, `notification.*` | U! | none | |
| `include.schema.changes` | `false` T; `true` or absent U! | none | Schema-change events on the `<prefix>` topic; also covered by `kc.output_shape_differs` |
| `include.schema.comments`, `include.query`, `source.struct.version`, `message.key.columns`, `skip.messages.without.change`, `tombstones.on.delete` (Kafka destination), `provide.transaction.metadata=true` | U! | none | Envelope-shaping |
| `decimal.handling.mode`, `time.precision.mode`, `bigint.unsigned.handling.mode`, `binary.handling.mode`, `schema.name.adjustment.mode`, `inconsistent.schema.handling.mode`, `event.deserialization.failure.handling.mode`, `event.converting.failure.handling.mode`, `event.processing.failure.handling.mode`, `ddl.parser.type`, `converters`, `converters.<name>.*`, `column.mask.*`, `column.truncate.*`, `column.propagate.source.type` | U! | none | Type-handling pattern, applied even at the default: the connector's DECIMAL, NULL and unsigned handling is not Debezium's (#3040) |
| `topic.prefix`; legacy `database.server.name` | A! | `kafka` destination `topic` template `<prefix>.<db>.<table>` | Topic names are reproduced; the format is not (note above) |
| `topic.delimiter`, `topic.naming.strategy` | U! | none | |
| `topic.cache.size`, `topic.transaction`, `topic.heartbeat.*`, `heartbeat.*` | U | none | |
| `schema.history.internal.*`, `database.history.*` | U | none | Not needed for a re-snapshot; the connector reads the live schema. DDL replayed across an `ALTER` during the cutover window is untested (#3005) |
| `max.batch.size` | A | `sdk.batch.size` | |
| `max.queue.size*`, `poll.interval.ms`, `poll.dispatch.interval.ms`, `statistics.metrics.enabled`, `custom.metric.tags`, `custom.sanitize.pattern`, `guardrail.*`, `database.query.timeout.ms`, `errors.max.retries`, `signal.consumer.*` | U | none | `errors.max.retries`: A |
| `database.initial.statements`, `driver.*` | U! | none | |
| Legacy renames (`*.whitelist`, `*.blacklist`, `database.history.*`) | as modern key | | `kc.key_deprecated` |

#### JDBC source

`io.confluent.connect.jdbc.JdbcSourceConnector`, PostgreSQL and MySQL URLs only, to the `postgres` or `mysql`
source and `builtin:kafka` destination. Other databases: `kc.connector_class_unsupported`.

The connectors have no polling mode. `builtin:postgres` v0.14.2 has `cdcMode` `auto|logrepl` (`auto` is logical
replication) and the MySQL connector reads the binlog, so the mapping is snapshot plus change capture, as the
base document decides. Issue #3006 words this "snapshot plus polling"; the wording is stale (D2).

All modes carry the blocking note `kc.output_shape_differs` until D1 resolves (OpenCDC records instead of flat
rows, with no recipe yet that reproduces the original topic's value format). What change capture adds over polling
(updates and deletes) is stated in the `mode` entry's reason, not as a second note.

| Upstream key | Status | Conduit | Notes |
| --- | --- | --- | --- |
| `connection.url` | T for `jdbc:postgresql://` and `jdbc:mysql://`; otherwise the connector is unsupported | source `url` / `dsn` | JDBC parameters outside host, port, database, `sslmode`/`ssl`: U! |
| `connection.user`, `connection.password` | T | in `url` / `dsn` | Password is a secret |
| `jdbc.credentials.provider.class` | `DefaultJdbcCredentialsProvider` T; else U! | none | |
| `connection.attempts`, `connection.backoff.ms`, `query.retry.attempts`, `transaction.isolation.mode`, `table.monitoring.startup.polling.limit.ms`, `table.poll.interval.ms`, `timestamp.delay.interval.ms`, `max.buffer.size` (src) | U | none | |
| `table.whitelist`, `table.include.list` (src) | T for literal names; U! for regex | `tables` | An absent selection means every table, discovered continuously: A! (`kc.default_differs`), `tables` is resolved at start |
| `table.blacklist`, `table.exclude.list` (src), `catalog.pattern`, `schema.pattern` | U! | none | |
| `table.types` | `TABLE` T; others U! | none | |
| `mode` | `bulk` A!; `incrementing`, `timestamp`, `timestamp+incrementing` A!; empty U! | `snapshotMode: initial`, `cdcMode: logrepl` (MySQL: `snapshot.enabled: true`) | `bulk` re-reads the whole table every poll interval; Conduit snapshots once, then streams changes. The incrementing and timestamp modes become change capture: updates and deletes are also delivered, `wal_level=logical` or binlog access is required, the first start re-snapshots (no offset import for JDBC offsets in v0.21) |
| `incrementing.column.name`, `timestamp.column.name`, `incrementing.column.mapping` (src), `timestamp.columns.mapping` (src) | U | none | Subsumed by `mode`; change capture needs no tracking column. Reason names the late-arriving-row difference |
| `timestamp.initial` | U! when set | none | A lower bound on what is read; the snapshot would deliver older rows |
| `validate.non.null` | T | none | No-op |
| `query`, `query.masked` (src), `query.suffix` | A! | tables named in the query, if literal | Whole-table sync replaces a custom query. Blocking. Never dropped |
| `topic.prefix` | T | `kafka` destination `topic` template `<prefix><table>` | Table name without schema; verified by the corpus |
| `poll.interval.ms`, `poll.linger.ms` | U | none | Change capture is push-based |
| `batch.max.rows` | A | `snapshot.fetchSize` | Docs say 100, `master` says 1000 |
| `numeric.mapping`, `numeric.precision.mapping`, `timestamp.granularity`, `db.timezone`, `date.calendar.system` | type-handling pattern | none | |
| `dialect.name` | `PostgreSql`/`MySql` consistent with the URL T; else U! | none | |
| `quote.sql.identifiers` | U | none | |
| `csfle.enabled` | U! when `true` | none | |

#### JDBC sink

`io.confluent.connect.jdbc.JdbcSinkConnector` to `builtin:kafka` source and the `postgres` or `mysql`
destination. Source side: `groupID` is `connect-<name>` and `readFromBeginning` follows the open question in the
base document (Decision 9). Other databases: `kc.connector_class_unsupported`.

| Upstream key | Status | Conduit | Notes |
| --- | --- | --- | --- |
| `connection.url` | T for `jdbc:postgresql://`, `jdbc:mysql://` | destination `url` / `dsn` | |
| `connection.user`, `connection.password` | T | in `url` / `dsn` | Secret |
| `jdbc.credentials.provider.class` | default T; else U! | none | |
| `connection.attempts`, `connection.backoff.ms`, `max.retries`, `retry.backoff.ms` | A | pipeline recovery | Non-blocking. Neither destination retries inside `Write` |
| `dialect.name` | consistent with the URL T; else U! | none | |
| `insert.mode` | `insert` A; `upsert` A when `pk.mode=record_key`, else A!; `update` U! | none | Conduit `postgres` destination: `INSERT` without a key, `ON CONFLICT` upsert with one. `mysql`: always `ON DUPLICATE KEY UPDATE`. `insert` on a replayed record fails in Kafka Connect and succeeds in Conduit |
| `pk.mode` | `record_key` T (`postgres`), A (`mysql`: the table's keys are used); `none` A!; `kafka`, `record_value` U! | none | `none` is not translated: the `postgres` destination upserts whenever a record key is present, so rows that Kafka Connect appended are overwritten |
| `pk.fields` | A | none | Names come from the record key. Reason: verify they equal the table's key columns |
| `delete.enabled` | `true` with `pk.mode=record_key` T; `true` otherwise U!; `false` U! | none | Conduit applies delete records; absent or `false` Kafka Connect ignored them. Both destinations delete unconditionally. Fix: add a `filter` processor on delete operations, or accept |
| `table.name.format` | `${topic}` T; other `postgres` formats with `${topic}` T; other `mysql` formats U! | `table` template | `mysql` takes `opencdc.collection` only |
| `table.types` | `TABLE` T; others U! | none | |
| `auto.create`, `auto.evolve` | `false` T; `true` U! | none | Invariant 6. Fix: create the table ahead of time |
| `batch.size` | A | `sdk.batch.size` | |
| `fields.whitelist` | U! | none | Conduit has no include list (v0.22 pack) |
| `db.timezone`, `date.timezone`, `timestamp.precision.mode`, `timestamp.fields.list`, `date.calendar.system`, `string.output.value.column.name` | type-handling pattern | none | |
| `replace.null.with.default` | U | none | Non-blocking. Differs only if value schemas define defaults |
| `quote.sql.identifiers` | A | none | Identifier case must match what the connector quotes; corpus cases cover it |
| `mssql.use.merge.holdlock`, `trim.sensitive.log` | U | none | |
| `csfle.enabled` | U! when `true` | none | |

Notes: `kc.default_differs` (blocking, absent `delete.enabled`): "Kafka Connect ignored delete records; Conduit
deletes the row".

#### S3 sink

`io.confluent.connect.s3.S3SinkConnector` to `builtin:kafka` source and `builtin:s3` destination. Every S3
migration carries the blocking note `kc.output_shape_differs` (object layout, naming and, for `JsonFormat`, the
line shape) and `kc.delivery_semantics_differ` (non-blocking: no deterministic object names, so no exactly-once).
Parity lane: #3009 (a parity matrix is its deliverable; this table follows it).

| Upstream key | Status | Conduit | Notes |
| --- | --- | --- | --- |
| `s3.bucket.name`, `s3.region` | T | `aws.bucket`, `aws.region` | |
| `store.url` | T | `aws.endpoint` | |
| `s3.path.style.access.enabled` | T | `aws.pathStyle` | Kafka Connect defaults to `true`; Conduit to `false`. When `store.url` is set and the key is absent, `migrate` emits `aws.pathStyle: true` and records the default |
| `aws.access.key.id`, `aws.secret.access.key` | T | `aws.accessKeyId`, `aws.secretAccessKey` | Secrets |
| `s3.credentials.provider.class` | default provider T when `builtin:s3` accepts empty static keys **[#3009]**, else A!; other classes U! | none | `builtin:s3` v0.9.4 requires static keys |
| `sts.role.arn`, `sts.role.external.id`, `sts.role.session.name` | U! | none | Assume-role is not supported |
| `topics.dir` | A | `prefix` | Layout differs (note above) |
| `directory.delim`, `file.delim`, `filename.offset.zero.pad.width` | U! | none | Key naming |
| `format.class` | `JsonFormat` A!; `ParquetFormat` A!; `AvroFormat`, `ByteArrayFormat` U! | `format` (`json`, `parquet`) | `json`: one OpenCDC-shaped object per line, not the bare value. `parquet`: fixed string columns, GZIP only |
| `format.bytearray.extension`, `format.bytearray.separator`, `avro.codec`, `enhanced.avro.schema.support`, `connect.meta.data`, `schema.cache.config`, `schema.compatibility`, `schema.generator.class`, `allow.optional.map.keys`, `format.json.schema.enable` (src), `json.decimal.format` (src) | U! | none | Formats Conduit does not write |
| `parquet.codec` | `gzip` A; others U! | none | Conduit writes GZIP; Kafka Connect defaults to `snappy` |
| `flush.size` | A | `sdk.batch.size` | Records per `Write`, one object per `Write`. Required key in Kafka Connect |
| `rotate.interval.ms` | A | `sdk.batch.delay` | |
| `rotate.schedule.interval.ms` | U! | none | |
| `partitioner.class` | `DefaultPartitioner` A!; others U! | `prefix` | |
| `partition.field.name`, `partition.duration.ms`, `path.format`, `locale`, `timezone`, `timestamp.extractor`, `timestamp.field`, `partitioner.max.open.files` (src), `rotate.file.on.partition.change` (src), `s3.schema.partition.affix.type` (src) | U! | none | |
| `s3.part.size`, `s3.part.retries`, `s3.retry.backoff.ms`, `retry.backoff.ms`, `s3.elastic.buffer.*`, `shutdown.timeout.ms` (src), `max.write.duration.ms` (src), `s3.send.digest` (src), `s3.http.send.expect.continue`, `s3.wan.mode`, `max.files.scan.limit` (src), `s3.compression.level` | U | none | Each write is one `PutObject` |
| `s3.compression.type` | `none` T; `gzip` U! | none | |
| `s3.proxy.url`, `s3.proxy.user`, `s3.proxy.password` | U! | none | Traffic path |
| `s3.ssea.name` | `AES256` T; empty or absent U!; else U! | none | Conduit hardcodes `AES256` (`destination/writer/s3.go:125`). Empty means the bucket default applies, so an SSE-KMS bucket default is silently replaced by SSE-S3 (`conduit-connector-s3#1047`). Fix: keep this connector on Kafka Connect, or accept |
| `s3.sse.customer.key`, `s3.sse.kms.key.id`, `s3.acl.canned`, `s3.object.tagging`, `s3.object.tagging.key.value.pairs`, `s3.object.behavior.on.tagging.error` | U! | none | |
| `store.kafka.keys`, `keys.format.class`, `store.kafka.headers`, `headers.format.class` | U! when the `store.*` key is `true`; U otherwise | none | |
| `behavior.on.null.values` | A | none | Conduit stores the delete record; Kafka Connect `fail`, `ignore` or `write`. Reason names the value |
| `report.null.values.to.dlq`, `tombstone.encoded.partition` | U | none | |
| `enable.conditional.writes` (src) | U | none | Exactly-once naming; see the delivery note |
| `storage.class` | `S3Storage` T; else U! | none | |
| `mode` (src) | U | none | |
| `hive.integration` | U! when `true`; U otherwise | none | `hive.metastore.uris`, `hive.conf.dir`, `hive.home`, `hive.database`: same |
| `csfle.enabled` | U! when `true` | none | |

#### Elasticsearch and OpenSearch sink

`io.confluent.connect.elasticsearch.ElasticsearchSinkConnector` to `builtin:kafka` source and the
`elasticsearch` destination (`ConduitIO/conduit-connector-elasticsearch`, which has moved to the `ConduitIO`
organization, #3042). The base document names `index.name` and maps `batch.size` and `max.retries` onto
`bulkSize` and `retries`; none of the three holds (the Confluent sink derives the index from the topic, and
`bulkSize` and `retries` are no-ops, #155). Corrected below. OpenSearch, class
`io.aiven.kafka.connect.opensearch.OpenSearchSinkConnector`: the rows below apply with the renames in the last
group; the target selector is owned by #3007. Until the destination's spec lists one, that class is
`kc.connector_class_unsupported`.

| Upstream key | Status | Conduit | Notes |
| --- | --- | --- | --- |
| (index name: derived from the topic) | A | `index` template | Kafka Connect lowercases the topic and replaces characters invalid in index names; Conduit uses the collection verbatim. Reason states the difference |
| `connection.url` | T for one URL; U! for a list | `host` | The destination's `version` has no Kafka Connect counterpart and is not derivable offline (`migrate` never probes the cluster): emitted as `${KC_<NAME>_ES_VERSION}` with the blocking note `kc.setting_required` |
| `connection.username`, `connection.password` | T | `username`, `password` | Secrets |
| `connection.compression`, `connection.timeout.ms`, `read.timeout.ms`, `max.connection.idle.time.ms`, `log.sensitive.data` | U | none | |
| `connection.ssl.*`, `elastic.https.ssl.*` (Aiven), `connection.trust.all.certificates` | U! | none | The destination takes a certificate fingerprint only. A plain `https` URL with default trust needs none of these |
| `elastic.security.protocol` | `PLAINTEXT` T; `SSL` A | none | With `SSL` the URL scheme must be `https` |
| `kerberos.user.principal`, `kerberos.keytab.path`, `proxy.*` | U! | none | |
| `aws.*` | U! | none | SigV4 signing |
| `batch.size` | A | `sdk.batch.size` | `bulkSize` is a no-op (#155) |
| `linger.ms` | A | `sdk.batch.delay` | |
| `max.in.flight.requests`, `max.buffered.records`, `flush.timeout.ms`, `flush.synchronously`, `bulk.size.bytes`, `max.retries`, `retry.backoff.ms` | U | none | Retries are a no-op today; a transient error stops the pipeline (nothing is dropped) |
| `key.ignore` | `false` T only when `key.converter` is `StringConverter` or a `JsonConverter` with primitive keys; `false` with any other key converter A!; `true` A! | none | Kafka Connect generates ids; Conduit uses the record key bytes as the document id. An Avro or struct key yields a different id than Kafka Connect's. Absent means `false`: same status |
| `key.ignore.id.strategy`, `use.autogenerated.ids`, `topic.key.ignore` | U! | none | |
| `schema.ignore` | `true` T; `false` or absent A! | none | `false` creates mappings from the Connect schema; Conduit relies on dynamic mapping |
| `topic.schema.ignore`, `compact.map.entries` | U! | none | |
| `write.method` / `index.write.method` | `upsert` T; `insert` or absent A! | none | `insert` replaces the document; Conduit always sends an update with `doc_as_upsert` (a merge), so removed fields persist |
| `behavior.on.null.values` | A! | none | Null values arrive as empty payloads, not deletes; unsafe until #3007 defines the behavior |
| `behavior.on.malformed.documents` | `fail` T; `warn`, `ignore` A | `dead-letter-queue` | Requires capability `bulk-item-nack` (#3007): a failed bulk item is never acked. Never skipped silently |
| `drop.invalid.message` | `false` T; `true` A | `dead-letter-queue` | Same |
| `behavior.on.version.conflict`, `external.version.header` | U! | none | |
| `data.stream.*`, `routing.type*`, `data.streams.existing.index.template.name`, `existing.resource.type`, `topic.to.existing.resource.mapping`, `external.resource.usage`, `topic.to.external.resource.mapping`, `max.external.resource.mappings` | U! | none | Data streams and external resources |
| Legacy (Confluent below 11): `type.name` | T | `type` | ES 5 and 6 only |
| Legacy: `topic.index.map`, `auto.create.indices.at.start`, `flush.timeout.ms` (old default) | U! / U / U | none | `topic.index.map`: fix "use the `index` template" |
| `csfle.enabled` | U! when `true` | none | |

#### MirrorMaker-style Kafka to Kafka

`org.apache.kafka.connect.mirror.MirrorSourceConnector` to `builtin:kafka` source (source cluster) and
`builtin:kafka` destination (target cluster). The destination needs `mirror: true` and the source
`emitMirrorMetadata: true` to preserve headers, partitions and timestamps **[#3038]**; without them the gate
fails and the connector carries the blocking note `kc.output_shape_differs` ("partition numbers, headers and
timestamps are not preserved"). `MirrorCheckpointConnector` produces no pipeline (offset translation has no
equivalent in v0.21): every key `unsupported`, blocking, class-level `kc.connector_class_unsupported`.
`MirrorHeartbeatConnector` produces no pipeline either, non-blocking. A dedicated-mode `mm2.properties` file
(`clusters`, `A->B.*`) is not an input format in v1 (D4).

| Upstream key | Status | Conduit | Notes |
| --- | --- | --- | --- |
| `source.cluster.alias` | T | destination `topic` template prefix | `DefaultReplicationPolicy`: `<alias><separator><topic>` |
| `target.cluster.alias` | T | none | Metrics label |
| `replication.policy.class` | Default T; `IdentityReplicationPolicy` T; else U! | destination `topic` template | |
| `replication.policy.separator` | T | in the template | |
| `replication.policy.internal.topic.separator.enabled`, `forwarding.admin.class`, `admin.timeout.ms`, `config.providers` | U | none | `forwarding.admin.class` non-default: U! |
| `source.cluster.bootstrap.servers`, `target.cluster.bootstrap.servers` | T | source `servers`, destination `servers` | |
| `source.cluster.<client key>`, `target.cluster.<client key>` (`security.protocol`, `sasl.*`, `ssl.*`) | per _Worker and Kafka client settings_ | source / destination TLS and SASL | JKS: U! |
| `topics` | T for a literal list; U! for regex | source `topics` | Absent key means `.*`: blocking `kc.default_differs` ("every topic is mirrored by default; Conduit needs a list") |
| `topics.exclude`, `topic.filter.class` (non-default) | U! | none | |
| `refresh.topics.enabled`, `refresh.topics.interval.seconds` | U | none | New topics are not discovered. Absent means `true`: same status |
| `enabled` | `false` T; `true` T | pipeline `status` | `false` writes the pipeline `stopped` |
| `sync.topic.configs.enabled`, `sync.topic.configs.interval.seconds`, `config.properties.exclude`, `config.property.filter.class`, `sync.topic.acls.enabled`, `sync.topic.acls.interval.seconds` | U! | none | Topic configs and ACLs are not synced. Fix: create destination topics ahead of time. Absent means `true`: same status |
| `replication.factor` | U | none | |
| `emit.offset-syncs.enabled`, `offset-syncs.topic.location`, `offset-syncs.topic.replication.factor`, `offset.lag.max`, `heartbeats.replication.enabled`, `consumer.poll.timeout.ms` | U | none | |
| `groups`, `groups.exclude`, `group.filter.class`, `emit.checkpoints.enabled`, `emit.checkpoints.interval.seconds`, `sync.group.offsets.enabled`, `sync.group.offsets.interval.seconds`, `refresh.groups.*`, `checkpoints.topic.replication.factor` | U! | none | Consumer-group offset translation; `emit.checkpoints.enabled` absent means `true`: same status. The report says plainly that a migration relying on consumer failover between clusters must not move |
| `emit.heartbeats.*`, `heartbeats.topic.replication.factor` | U | none | Heartbeat connector only |

#### Legacy and alias handling

A rule may declare `aliasOf: <key>`. An aliased key is translated exactly as its modern name and additionally
reports `kc.key_deprecated` (approximated, non-blocking, reason "renamed in the upstream version named in
`generator`", `fix`: the new name). A key that appears twice in one `.properties` input is reported twice: the
earlier occurrences as
`translated` with code `kc.key_duplicate` and reason "overridden by line N (Java properties: the last value
wins)". The completeness multiset stays equal. `kc.key_deprecated` and `kc.key_duplicate` are additions to the
base document's code table.

### The compatibility report as a contract

The example JSON in the base document stands. This section fixes what makes it a public contract.

**Schema.** `pkg/kcmigrate/schema/report.v1.schema.json`, embedded with `go:embed`, `$id`
`https://conduit.io/schemas/kc-report/v1.json`. It describes `result` of the family A envelope
(`cmd/conduit/cecdysis/testdata/envelope.schema.json` keeps describing the envelope). A schema-golden test
validates every report the corpus produces against it and compares the schema file against a committed copy, so
a schema change is a visible diff in review, the same practice as the envelope schema. The schema is written by
hand, not reflected from Go types, so an accidental struct change cannot silently change the contract.

**Versioning.** `reportVersion` is an integer, `1` for v0.21. Adding a field, an enum member of `code`, a
rule or a note is additive and does not bump it. Removing or renaming a field, or changing the meaning of
`status` or `blocking`, bumps it, ships with a migration note, and follows the base document's deprecation
policy (announce, warn, remove across at least two minor versions). Consumers must ignore unknown fields. The
same applies to `kc lint` and `kc diff` results (`lintVersion`, `diffVersion`, both `1`).

**Semantics, in one place.**

| Field | Meaning |
| --- | --- |
| `summary.keys` | Number of entries. Equals the number of input keys, counting every occurrence in every input file |
| `summary.translated`, `.approximated`, `.unsupported` | Partition of `keys` by `status` |
| `summary.blocking` | Entries and notes with `blocking: true`. `ok` is `blocking == 0` and `error == null` |
| `connectors[]` with `role: "worker"` | Home of worker-property entries (one element per worker file; `name` is the file label, no `pipeline`). Counted in `summary` like any connector |
| `connectors[].entries[]` | One per input key occurrence, in input order (file order, then line). The earlier of two duplicate keys has code `kc.key_duplicate` |
| `connectors[].notes[]` | Differences with no input key (absent-key defaults, output shape, delivery semantics, required settings). Not part of the completeness multiset |
| `connectors[].transforms[]` | An index view of the SMT chain. Each SMT sub-key still has its own entry in `entries` |
| `entries[].origin` | `file` plus `line`/`column` (properties) or `pointer` (JSON, YAML). Never a value |
| `entries[].target.configPath` | JSON pointer into the generated pipeline file |
| rendered `configPath` | The `origin` pointer. A finding about a Kafka Connect key points at the user's config, which is the failing config path in the sense of CLAUDE.md; the pipeline pointer is in `target` |
| `entries[].secret` | Value was wrapped and redacted; `target.env` names the variable |
| `resume`, `output_compat` | As in the base document and #3033 |

**Determinism.** The report is a function of the inputs and the binary: connectors in input order, entries in
origin order, no timestamps, hostnames or absolute paths. `origin.file` is relative to the nearest common parent
directory of all inputs, with forward slashes; stdin is `<stdin>`; a test runs each case from two working
directories and requires identical reports. `generator` records the Conduit version, the `rulesVersion` of each rule
file and the upstream versions they were written against. The pipeline `description` carries `sha256` of the
canonical report JSON; the report does not carry a hash of the pipeline files, which would be circular.

**Exit codes.** Findings are domain results, not errors, so they reduce to the worst bucket of the
`pkg/conduit/exitcode` classifier (environment 3 over validation 2 over runtime 1), by synthesizing one
`conduiterr` per failing result, as the CLI conventions require.

| Condition | `migrate kafka-connect` | `kc lint` | `kc diff` |
| --- | --- | --- | --- |
| Success | 0: no blocking entry or note | 0: no `error` finding | 0: no change at or above `--fail-on`, no `kc.diff.schema_incompatible` |
| Domain failure | 2: a blocking entry or note, or any `approximated` entry under `--strict` | 2: an `error` finding, or any finding under `--strict` | 2: a change at or above `--fail-on`, or an incompatible schema |
| Bad input | 2: `migrate.input_unreadable`, `migrate.input_unrecognized`, `migrate.output_exists` | 2 | 2 |
| Cannot reach a dependency | 3: `migrate.rest_unreachable`, `migrate.kafka_connect.offsets_unreachable` | 3: `--from-rest` | 3: `--from-rest`, `kc.diff.registry_unreachable` |
| Internal error | 1: a translator bug, including a generated pipeline that fails `pipelines validate` | 1 | 1 |

`kc diff` has no default `--fail-on`: a diff is a report, and only an incompatible schema fails it unless the
caller opts in (`--fail-on identity|behavior|any`). `--dry-run` has the same exit codes as a real run.

`--accept-gaps` (migrate only): blocking entries and notes no longer fail the run. The report lists them in
`result.acceptedGaps`, pipelines are written `running`, `ok` is true and the exit code is 0. `--strict` still exits
2 on any accepted gap and on any `approximated` entry. Tightening a rule (a key moving to a weaker status) can
change a user's exit code; it happens only in minor releases and is listed in the changelog.

**Human output.** The shared located-finding rendering, one block per entry that is not plain `translated`
(`--verbose` adds the translated ones), grouped by connector, then the summary line and one imperative line:

```text
orders-cdc  io.debezium.connector.postgresql.PostgresConnector  ->  postgres source, kafka destination
✗ kc.key_unsupported   /config/decimal.handling.mode
    orders.json:14: "double" is not an encoding Debezium-compatible output mode supports
    → use "precise" or "string", or keep this connector on Kafka Connect
⚠ kc.key_approximated  /config/slot.name
    orders.json:9: Conduit uses its own slot "conduit_orders_cdc" so rollback stays possible
    → none needed; see docs: migrating Debezium Postgres
Summary: 41 keys · 30 translated · 7 approximated · 4 unsupported · 2 blocking
Pipelines: 1 stopped (blocking entries), 1 written running. Fix the ✗ items above, then re-run.
```

The ASCII fallback, `--quiet` and `NO_COLOR` follow the CLI conventions. `REPORT.md` is the same content, ordered
by connector, with a "before you deploy" section (secrets, `kc.secret_stored_expanded`, notes).

**Error codes.** New in this document, in addition to the base table: `kc.key_deprecated` (A), `kc.key_duplicate`
(T, no-op), `kc.default_differs`, `kc.output_shape_differs`, `kc.delivery_semantics_differ`,
`kc.setting_required` (notes), `kc.topic_name_unsupported` (note: a topic naming the destination's topic
template cannot express), `kc.target_capability_missing` (U!, from the `requires` gate), and the lint and
diff codes below. All are registered through `conduiterr.Register`; the registry-completeness test in
`pkg/conduit/exitcode` already fails a registered code with no exit bucket. Every code has a `fix` text. A table
test fails if a code is emitted by a rule file but not registered, or registered but never emitted by any rule,
note, lint rule or diff path.

### Lane interfaces

What `migrate` depends on in other lanes, and what it hands them. No new Go interface is introduced except
`OffsetReader`, which has three implementations on day one (REST, `connect-offsets` topic, saved file).

| Seam | Owner | Contract | Direction |
| --- | --- | --- | --- |
| Offset parsing and resume point | #3004 (connector) and #3029 (CLI) | Go: the connector's exported `source/dbzoffset` package, called by `pkg/kcmigrate/resume` so the dry run runs the connector's own code. Setting: `logrepl.import.debeziumOffset` plus `snapshotMode: never`, `cdcMode: logrepl`, `logrepl.autoCleanup: false`, `logrepl.requireExistingSlot: true`. Codes: `postgres.import.*` and `migrate.kafka_connect.*`, passed through unchanged | migrate calls, never reimplements |
| Reading Debezium's offset | `pkg/kcmigrate/resume.OffsetReader` | `Read(ctx, Connector) (RawOffset, error)` with `rest` (default, KIP-875), `topic` and `file` implementations. Read-only | internal |
| Cutover, rollback check | #3029 | `--cutover`, `--check-rollback` and their flags and codes, specified in #3029. Not designed here. Tier 1 | owned by the Postgres lane |
| Debezium-compatible output | #3032 | Settings `debezium.*` on the Postgres source, the `debezium.encode` processor, the Kafka plan destination and the `result.output_compat` fields, all in #3033. `migrate` reads supported values from the plugin specification (`inclusion` validations on `debezium.*` parameters), never from constants, and gates every row on `requires` | migrate reads the spec |
| Kafka mirror | #3038 | `emitMirrorMetadata` (source), `mirror` (destination), gated by `requires` | migrate reads the spec |
| Certification state | connector lanes #3005 to #3009; registry index (#2963) | Registry tier per plugin version. `migrate` reports `kc.target_not_certified` (non-blocking) from the index at run time; the harness fails on it for the v0.21 set | migrate and harness read |
| Rule files | this lane | The tables in this document, as `pkg/kcmigrate/rules/data/*.yaml` | connector lanes review their own rows |
| Corpus cases | each connector lane | A case directory per behavior it certifies (below) | lanes write, harness reads |
| Real-Debezium harness | #3004 and #3032 | Resume cases and envelope diffs. The migrate harness shares the compose fixtures and the `differences.yaml` loader and never forks them | shared |

A connector lane that changes a setting name breaks the rule-data resolution test of this lane (rule data
resolves every emitted setting against the pinned plugin specification). That is deliberate: the break is
visible in the PR that causes it, and the fix is one data line.

### `kc lint` additions

The base document's rule table stands. Changes:

- **Rules read rule data.** `kc.lint.unknown_key` and `kc.lint.deprecated_key` use the same key lists and
  `aliasOf` fields as the migrate rules, so they cannot disagree with `migrate` about what a key is.
- **`kc.lint.kip1188_override_risk` is specified.** KIP-1188, "New ConnectorClientConfigOverridePolicy with
  allowlist of configurations", is accepted and released in Apache Kafka 4.2.0 (2026-02-17; JIRA KAFKA-19824). It
  adds the `Allowlist` policy (`connector.client.config.override.policy=Allowlist`) with one setting,
  `connector.client.config.override.allowlist` (a list; the default is empty, which allows no override at all),
  and states that `Allowlist` becomes the default from Kafka 5.0, replacing `All`. The deprecated `Principal` policy
  is slated for removal in 5.0; its equivalent is the allowlist `sasl.jaas.config,sasl.mechanism,security.protocol`.
  Verified on 2026-10-14 against the KIP page (last modified 2025-10-24), the 4.2 source of
  `AllowlistConnectorClientConfigOverridePolicy` and the 4.2 upgrade notes. Not verified: Kafka 5.0 itself has not
  shipped, so its default is the KIP's and the upgrade notes' statement; the Confluent Platform default of `None`
  is from a search snippet only and is treated as "policy unknown"; behavior of the allowlist key outside the worker
  file is unverified.

  | Condition | Severity | Finding |
  | --- | --- | --- |
  | Worker file sets `Allowlist` and a connector carries an override key (prefix stripped) not in the allowlist | warning | Connector validation will reject it. `fix` carries the exact allowlist value needed: the sorted union of the connector's override keys |
  | Worker file sets `Principal` | warning | Deprecated, removed in 5.0. `fix`: the equivalent allowlist |
  | Policy absent or `All`, connector carries any override key | info | These connectors fail validation when the worker moves to Kafka 5.0 unless the allowlist is set. `fix`: the exact value |
  | Any `producer.override.sasl.jaas.config`, `.security.protocol`, `.sasl.mechanism` (and `consumer`/`admin` forms) | warning | Security-sensitive override, the class of issue the KIP addresses |

  The rule complements `kc.lint.client_override_policy` (KIP-458: `None` and `All`); for one key and one worker
  setting only the more specific finding is emitted. The base document's Decision 4 is resolved by this
  verification. After `migrate`, the overrides are translated (shared table) and the risk is gone, which is the
  finding's `fix` text for teams that are leaving Kafka Connect anyway.
- **Debezium event-dropping modes.** One rule added: `kc.lint.dbz_event_processing_drops` (warning): Debezium
  `event.processing.failure.handling.mode` (or the MySQL `event.deserialization`/`event.converting` variants) set
  to `warn` or `skip` silently drops events on the Kafka Connect side. Worth knowing before a migration compares
  record counts.

### `kc diff` additions

The base document's two jobs and the exit-code rule stand. Added:

- **Classification is data.** `diff:` in each rule (`identity|behavior|tuning|secret`) decides the class. A key
  with no rule defaults to `behavior`, never `tuning`, so a missing classification can only over-report. A test
  fails any rule file entry without `diff`.
- **Result shape.** `result` is `{diffVersion, old, new, changes[], lint{introduced,resolved,unchanged},
  registry{subjects[], findings[]}}`. A change is `{key, kind: added|removed|changed, class, old?, new?, finding}`.
  For `secret` keys `old`/`new` are omitted and `kind` is `changed` or `unchanged` by SHA-256 comparison, never
  printed, never prefixed.
- **No registry traffic without a URL.** `--registry-url` is the only network flag. Requests are the three
  read-only calls in the base document, bounded in size and time, redirects not followed, credentials from
  `${VAR}`.
- **Harness use.** `kc diff --against-pipeline` is the check the harness runs per case to catch migrations that
  change subject names; its output is part of the case expectations.

### Harness corpus format

Issue #3010 builds the runner; this fixes the data it reads. One corpus, two consumers: the offline runner (a Go test
in `pkg/kcmigrate`, per PR, required) and the compose runner (`tests/migrate`, nightly and on changes under
`pkg/kcmigrate`, not required until stable, as in the base document). Both read `tests/migrate/corpus/`.
This replaces the base document's `pkg/kcmigrate/testdata/<case>` for committed cases; unit-level fixtures for
parsers and fuzz seeds stay in `testdata`.

```text
tests/migrate/corpus/
  certification.yaml                  declared tier of every emitted plugin (rule 4)
  <family>/<case-id>/
    case.yaml                         manifest (below)
    input/                            exactly what a user would hand to the command
      connector.json | connector.properties | rest-export.json | strimzi.yaml
      worker.properties               optional
    expected/
      report.json                     byte-for-byte golden
      pipelines/<name>.yaml           byte-for-byte golden
      conduit.yaml.fragment
      secrets.env.template
      REPORT.md                       human golden, one per family is enough
      exit_code                       integer
      lint.json                       golden of `kc lint --json` over the same input (optional)
    oracle/                           present only when the case is runnable
      compose.yaml                    fixture services (Postgres, MySQL, Kafka, Registry, MinIO, Elasticsearch)
      workload.<sql|yaml>             seeded, committed
      normalize.yaml                  JSON paths to ignore as volatile, with a reason each
      explained.yaml                  differences that are allowed, each tied to a report entry or note
```

`family` is one of `debezium-postgres`, `debezium-mysql`, `jdbc-source`, `jdbc-sink`, `s3-sink`,
`elasticsearch-sink`, `kafka-mirror`, `common` (parsers, secrets, SMTs, converters).

`case.yaml`:

```yaml
schemaVersion: 1
id: dbz-pg-orders-avro
family: debezium-postgres
description: Orders table, Avro converter, Debezium 3.7 defaults, offset import.
provenance: {source: vendor-doc|design-partner|synthetic, ref: "<url or partner alias>"}
sanitized: {scrubbed: true, check: "scrub-v1"}          # the scrub check must pass in CI
upstream: {debezium: "3.7.0.Final", connect: "4.2"}
command: {args: ["migrate", "kafka-connect", "--worker", "input/worker.properties", "input/connector.json"]}
expect:
  exitCode: 2
  counts: {translated: 30, approximated: 7, unsupported: 4, blocking: 2}
  notes: ["kc.output_shape_differs"]                    # codes of connector-level notes
covers:                                                 # rule keys this case exercises (rule 3)
  - {class: io.debezium.connector.postgresql.PostgresConnector, keys: [database.hostname, snapshot.mode]}
emits: [{plugin: builtin:postgres, role: source}, {plugin: builtin:kafka, role: destination}]
run: {tier: offline}                                    # offline | compose | debezium
```

**Rules the runners enforce.**

1. **Golden.** `migrate` over `input/` produces `expected/` byte for byte; `-update` regenerates, and the diff is
   reviewed like code. Two values are normalised before comparison, to `<VERSION>` and `<REPORT_SHA256>`: the
   Conduit version in `generator`, and the report `sha256` in each pipeline `description`. Nothing else is: the
   `rulesVersion` and upstream versions in `generator` stay, so a rules change is a visible golden diff.
2. **Unaccounted key.** The multiset of input keys equals the multiset of `report.json` entry keys (the base
   document's completeness property), checked on every case, so a bad hand-edited golden cannot hide a missing
   key. Any `kc.key_unknown` in a v0.21-family case fails the case unless the case is tagged
   `expectUnknown: [key]`, which exists for the negative tests.
3. **Key coverage.** For each class of the v0.21 set, every non-pattern key of its rule file appears in some
   case's `covers`, or in `uncovered:` in `corpus/coverage.yaml` with a reason. A new rule with no case fails the
   build. This is how "unit tests per connector mapping" is enforced without writing one test per row.
4. **Certification.** The runner collects the plugins in the `emits` field of every rule file (declared in data,
   cross-checked against what the emit kinds can produce) and of every case, and requires each to have an entry
   in `certification.yaml`:

   ```yaml
   - {plugin: builtin:postgres, version: ">=0.15.0", tier: certified, lane: 3004, registry: "<index id>"}
   - {plugin: mysql,            version: ">=0.2.0",  tier: certified, lane: 3005, registry: "<index id>"}
   ```

   A plugin that `migrate` can emit and has no entry fails the harness. That is the release-bar check in #3002.
   The connector lane that certifies its connector edits its own entry in the PR that completes certification.
   Offline, the runner checks only that the entry exists. In the nightly, it also compares each entry with the
   live registry index tier and fails if the file claims more than the index says, so the file cannot lie.
5. **Explained differences (compose tier).** After running the original connector (oracle) and the generated
   pipeline against the same inputs, the runner normalizes both outputs (topic records, S3 objects, table rows,
   index documents) into ordered JSON lines per stream and diffs them. Each observed difference must match an
   entry of `explained.yaml` by JSON pointer, in the same shape as `differences.yaml` of the Debezium diff
   harness (#3033) and read by the same loader:

   ```yaml
   - path: /value/source/lsn
     kind: volatile                    # ignored, needs a reason
     reason: "LSN differs per slot"
   - path: /value/after/amount
     kind: reported
     report: {key: decimal.handling.mode, status: unsupported}   # must exist in report.json with that status
   ```

   An unexplained difference fails the case and prints a minimal reproduction. An `explained.yaml` entry whose
   difference never occurred fails too (stale), unless marked `optional`. A `reported` entry whose `report` key is
   missing or has a weaker status fails: the difference was not disclosed. This turns the report from a claim
   into a tested property.
6. **Harness self-test.** A deliberately wrong rule (one key flipped from `unsupported` to `translated`) must make
   rule 2, 3 or 5 fail; otherwise the harness cannot fail.
7. **Sanitization.** Real configs are scrubbed of hostnames, credentials and customer identifiers before commit.
   `scrub-v1` is a Go test over `corpus/**/input/**` that rejects: any value matching secret-key names that is not
   a placeholder, hostnames outside `example.*` and `*.internal.example`, and IP literals other than documentation
   ranges.
8. **Adding a case** is a README procedure: copy the nearest case, replace `input/`, run `-update`, read the
   diff, fill `covers`, `emits` and `provenance`. The connector lane's PR carries the cases for its connector;
   a lane cannot claim "covered by a case in the migration harness" without them.

**What the offline tier does not prove.** Output equivalence. It proves the report is complete, deterministic,
secret-free and valid. Equivalence needs the compose tier, and the compose tier needs a runnable fixture per
family. Families without one say so in `corpus/coverage.yaml` (`run: offline` only) and the connector's
certification page says "mapping unverified against a live oracle". Resume cases are owned by #3004.

### MCP tool parity

The three verbs become three read tools in `cmd/conduit/internal/mcp`, registered in `catalog.go` next to the
existing tools, so `llms.txt` lists them without hand edits (`llmsgen` reads `Catalog()`):

| Tool | Same engine as | Input | Output |
| --- | --- | --- | --- |
| `kc_migrate` | `conduit migrate kafka-connect` | `inputs[]` of `{name, content, format?}`, optional `worker` `{name, content}`, `strict`, `dry_run`, `secret_keys[]` | `result` of the CLI `--json` envelope, plus `files[]` of `{path, content}` for the pipelines, fragment, template and `REPORT.md` (empty when `dry_run`) |
| `kc_lint` | `conduit kc lint` | same inputs, `rules[]`, `ignore[]`, `strict`, `for_cutover` | the CLI `--json` envelope |
| `kc_diff` | `conduit kc diff` (config diff only) | `old`, `new` `{name, content, format?}`, `fail_on` | the CLI `--json` envelope |

Rules:

- **One code path.** The CLI and the tools call `kcmigrate.Run(ctx, Request) (Result, error)`. The CLI writes
  `Result.Files` to `--out`; the MCP adapter returns them. A parity test runs every offline corpus case through
  both and requires identical `result` and identical file bytes.
- **Content in, content out.** As in `20260708-mcp-server.md`: the tools take content, not paths, and write
  nothing to the host. `name` is a label for locations in findings and is never opened.
- **Not exposed, by decision:** `--from-rest`, `--import-offset`, `--offsets-from`, `--cutover`,
  `--check-rollback`, `--slot-strategy`, `--registry-url`, `--accept-gaps`, `--out`, `--force` and path-valued
  `--worker`. The first six reach a network or a database or mutate Debezium and Postgres state, and `--registry-url`
  reaches the network: `--from-rest` and the registry flag would make the MCP host an SSRF proxy for the agent's
  chosen URL, and `--cutover` is a Tier 1 mutation that needs a human. `--accept-gaps` would let an agent turn a
  blocking report into a running pipeline. An agent that needs a REST export obtains it itself and passes the
  content. A request that names an unexposed option fails schema validation (`additionalProperties: false`), so
  the catalog is what the agent can do.
- **All three are read tools.** They mutate nothing, so they are registered without `--allow-mutations`
  (`Mutates: false`), like `lint` and `dry_run`.
- **Limits.** Total input is capped (proposed: 1 MiB and 64 files, the same bounds as the CLI's file reader); output is
  capped likewise. Over the cap: `migrate.input_too_large`.
- **Secrets.** Inputs may contain plaintext secrets. Results and errors are redacted by the same value type as
  the CLI; a canary test covers the MCP path as well.
- **Agent path.** A session with only MCP and `llms.txt` goes `kc_lint` -> `kc_migrate` -> (writes the returned
  files itself) -> `dry_run` -> `deploy`, using existing tools after the first two. `llms.txt` carries a short
  worked example, per the north-star test in CLAUDE.md.

## Alternatives considered

The base document's alternatives (REST-compatible API, JVM sidecar, accepting KC config directly, LLM
translation, separate binary) are not repeated.

1. **Rule data format.**
   - _Chosen:_ YAML rule files, embedded, one per class; emission logic in code keyed by `emit.kind`.
   - _Go code per class_ (a switch of key names). Lost: the known-key set, the diff classes, the generated docs
     and the lint rules all want the same list; in code they become four lists that drift, and a key addition is
     a code review of logic when it is a data change. Reviewers for connector lanes would have to read Go to check
     their own row.
   - _Generate the key lists from the connector JARs' `ConfigDef`._ Lost for v1: needs a JVM at build time, ties
     the migrator to the JAR host (v0.23), and captures keys but not mapping decisions, which are the content.
     Revisit as a way to _check_ the data files for drift (the base document's scheduled upstream diff).
2. **Corpus layout.**
   - _Chosen:_ one corpus, an offline runner and a compose runner.
   - _Two corpora_ (unit goldens in `testdata`, harness cases in `tests/migrate`). Lost: the same config would be
     written twice, and the two copies would disagree on what the expected output is.
   - _Harness only, with a live Connect worker for everything._ Lost: it makes the completeness and
     secret-leak gates depend on docker and a nightly, and a per-PR gate must be hermetic.
3. **Absent-key defaults.**
   - _Chosen:_ connector-level notes outside the completeness multiset.
   - _Synthetic entries for defaulted keys._ Lost: breaks the invariant that entries equal input keys, so the
     property test can no longer catch a dropped key.
   - _Ignore them._ Lost: a silent drop, the one thing the command must not do (`delete.enabled`, MirrorMaker's
     `topics=.*`, S3 path style).
4. **MCP exposure.**
   - _Chosen:_ three offline read tools, content in and content out.
   - _One `kc` tool with a `subcommand` argument._ Lost: the catalog is how an agent discovers what it can do,
     and one tool with a verb parameter hides the read-only property of each.
   - _Mirror every CLI flag, including `--from-rest` and `--registry-url`._ Lost: SSRF through the MCP host, and
     a path to `--cutover`, which is a human decision.
5. **Report schema.**
   - _Chosen:_ hand-written JSON Schema, embedded, golden-compared.
   - _Reflect the schema from Go types._ Lost: an accidental struct change would change a public contract with a
     green build.
   - _No schema, rely on the example._ Lost: "a versioned JSON schema" is part of the lane's definition of done.
6. **JDBC source default blocking** (D1).
   - _Chosen:_ blocking until a documented recipe reproduces the original value format and the harness proves it.
   - _Non-blocking approximation with the difference stated_ (the base document's text). Lost: it contradicts the
     base definition of `approximated` (a change that breaks downstream consumers is blocking) and would let a
     pipeline that rewrites every consumer's input shape run by default.

## Failure modes

Rows specific to this document. The base document's table stands.

| Failure | Consequence | Mitigation |
| --- | --- | --- |
| A rule says `translated` for a setting the connector no longer has or does not implement | Silent drop with a green stamp | Rule data resolves every emitted setting against the pinned plugin spec; `requires` gates rows that depend on an unmerged lane; the corpus golden shows the setting in the pipeline and `pipelines validate --resolve-plugins` runs on it |
| A connector lane renames a setting | `migrate` build breaks | Intended; the resolution test names the rule and the lane |
| Upstream adds a key (Debezium 3.8, Confluent 10.10) | A real config reports `kc.key_unknown`, blocking | Safe direction. The upstream-drift job (PR 20) fails the nightly when the live docs differ from the committed snapshots, and the update is a data PR; the snapshot header records the version |
| Absent-key default differs and no rule emits a note | The one silent difference the completeness invariant cannot see | The mechanical defaults test over the pinned upstream key lists (_Absent keys are evaluated mechanically_) fails the build for any default whose row is not `T` and has no note; the compose tier would also observe the difference |
| Corpus case contains a real secret or hostname | Credential or customer leak in a public repo | `scrub-v1` test, PR template checkbox, review of `input/` in the diff |
| `certification.yaml` claims more than the registry says | Release-bar check passes falsely | Nightly comparison with the live index; the check is the failure, not a warning |
| Explained-difference file used to paper over a real bug | Harness green, behavior wrong | Every `reported` entry must point at a report entry of at least that severity; a stale entry fails; entries are reviewed in the lane's PR |
| Compose tier flakes | Nightly noise, ignored failures | Not a required check until it passes a de-flake soak; the offline tier carries the per-PR gates |
| MCP tool used to exfiltrate or reach internal hosts | Host read or SSRF | Content-only input, no network flags, no path arguments, size caps, schema with `additionalProperties: false` |
| `--accept-gaps` used by habit | Blocking gaps run | Records `acceptedGaps` in the report with the list of blocking entries; REPORT.md repeats them; not available in MCP |
| JDBC source or S3 pipeline run with the blocking output-shape note ignored | Consumers read a different format | `status: stopped` until accepted; the note's `fix` names the recipe |
| Two lanes both claim a code or the same rule row | Conflicting reports | Rows live in one file per class; CODEOWNERS routes each file to the lane that owns the connector |

## Upgrade, rollback and compatibility

- No serialized engine format changes. Report, lint and diff results are versioned public contracts (above).
- Rule files carry `rulesVersion`; the report prints it. A rules change that moves a key between statuses is a
  visible golden diff and a changelog line, because it can change a user's exit code. Moving a key from
  `translated` to a weaker status needs a release note.
- `certification.yaml` and the corpus are test data; they have no runtime effect and roll back with the commit.
- MCP tools are additive to the catalog; their input schemas follow the same additive rule.
- Rollback of any implementation PR is a revert: nothing in this lane writes state, except the Postgres lane's
  `--cutover`, which has its own runbook.
- Deprecations: a key's `aliasOf` entry is removed only after two minor versions of `kc.key_deprecated`.

## Observability

- Output only, as in the base document: report, exit code, `--json`.
- `report.generator` records versions, so a bug report is reproducible from the report alone.
- The harness publishes a summary per run (cases, unexplained differences, key coverage per class, certification
  entries and their live tiers) to the nightly job summary, so the release-bar check is a number someone reads.
- Runbook additions for `docs/operations/kafka-connect-cutover.md`: "report says blocking", "unexplained
  difference in the migration harness", "certification entry stale".

## Lane plan

Implementation PRs for #3003, in order. Each is small (target under about 600 changed lines excluding goldens and
rule data), conventional-commit titled, milestone v0.21.0, and carries its docs (`conduit.io` source, `llms.txt`,
changelog), its tier, and an adversarial self-review. Tiers: 2 = CLI/features, 1 = data path.

| # | PR | Tier | Depends on | Notes |
| --- | --- | --- | --- | --- |
| 0 | `docs(design): migrate kafka-connect mapping tables, report contract, harness corpus, MCP parity` (this) | 3 | #3026 | Draft until DeVaris signs off the open decisions |
| 1 | `feat(kcmigrate): input IR, parsers and report types` | 2 | 0 | Properties, JSON/REST export. Report schema file and golden. `doc.go`. Fuzz targets. No rules, no command |
| 2 | `feat(cli): migrate kafka-connect skeleton with --json and exit codes` | 2 | 1 | Rule engine, completeness and secret-canary and determinism properties, `kc.connector_class_unsupported` for everything, `--dry-run`, registered codes, schema-golden entry. Corpus runner (offline) lands here with the `common` family |
| 3 | `feat(kcmigrate): shared rules, converters, error handling, worker settings` | 2 | 2 | The shared table. Secrets handling |
| 4 | `feat(kcmigrate): Kafka to Kafka (MirrorMaker) rules` | 2 | 3, #3038 spec for the gate | `requires` gate means it can land before #3038 |
| 5 | `feat(kcmigrate): Debezium Postgres rules` | 2 | 3, #3032 spec for the gate | No offset import yet |
| 6 | `feat(kcmigrate): Debezium MySQL and JDBC source rules` | 2 | 3 | Blocking notes per D1 |
| 7 | `feat(kcmigrate): JDBC sink rules` | 2 | 3 | |
| 8 | `feat(kcmigrate): S3 sink rules` | 2 | 3 | Tracks the #3009 parity matrix |
| 9 | `feat(kcmigrate): Elasticsearch and OpenSearch sink rules` | 2 | 3 | |
| 10 | `feat(kcmigrate): SMT and predicate subset` | 2 | 3 | Emit-if-exact table of the base document |
| 11 | `feat(cli): kc lint` | 2 | 3 | Including KIP-1188 |
| 12 | `feat(cli): kc diff, config diff` | 2 | 3 | |
| 13 | `feat(cli): kc diff schema compatibility against a registry` | 2 | 12 | The only network client in the lane besides `--from-rest` |
| 14 | `feat(cli): Strimzi input and --from-rest` | 2 | 2 | Read-only GETs, bounded |
| 15 | `feat(mcp): kc_migrate, kc_lint, kc_diff` | 2 | 3, 11, 12 | Parity test against the corpus |
| 16 | `ci(migrate): compose harness and certification gate` | 2 | 2; #3010 | Owned by #3010; listed because it gates 4 to 9 |
| 17 | `feat(cli): --import-offset and --dry-run --peek` | **1** | 5; #3004 connector release | Specified in #3029. Failure-mode analysis and DeVaris sign-off in a separate session |
| 18 | `feat(cli): --cutover, --check-rollback` | **1** | 17 | Same; owned by the Postgres lane |
| 19 | `docs: Migrating from Kafka Connect concept page, per-connector guides, error-code reference` | 3 | 4 to 13 | States plainly that only Postgres resumes from imported offsets in v0.21. Each PR above adds its own section; this one assembles the page and `llms.txt` |
| 20 | `ci(kcmigrate): upstream-drift job and snapshot-subset-of-rules test` | 2 | 2 | Owned by this lane. Starts once the first rule file and snapshot exist (PR 5); lands before the release branch so the snapshots are checked at the release bar |

Sequence rule: no PR from 4 to 9 merges without its corpus cases (offline tier) and its `emits` entry in
`certification.yaml`. Nothing here blocks the connector lanes, because rule files only read plugin specs; the
`requires` gate keeps rows honest while they land. PRs 1 to 3 are the critical path; 4 to 10 parallelize.
What this lane does not own: the connector settings, the offset import, the output mode, the compose fixtures for
the Debezium and resume cases, and certification itself.

## Decisions needed

Recommendations are in bold. These need DeVaris before the PR leaves draft.

1. **D1. JDBC source output shape.** The base document maps a whole-table JDBC source to Postgres or MySQL CDC as
   non-blocking while saying the output is OpenCDC, not the flat rows the original produced. **Keep it blocking in
   v0.21** (this document does), and downgrade per converter combination only when a documented processor recipe
   passes a harness case. The alternative ships a pipeline that changes every consumer's input shape by default.
2. **D2. #3006 wording.** The lane says "snapshot plus polling" and a "polling path certified". The Postgres connector
   has `cdcMode` `auto|logrepl` and no polling mode, and the MySQL connector reads the binlog. It also speaks of the
   "labs SQL connectors"; `conduitio-labs` has no postgres, mysql or JDBC connector (the targets are `builtin:postgres`
   and `ConduitIO/conduit-connector-mysql`). **Reword #3006 to "snapshot plus change capture"**, drop its polling
   crash-and-resume item, correct the labs reference, and keep the base document's mapping.
3. **D3. Corrections to the base document's rows.** `decoderbufs` is `approximated`, not `translated` (#3033
   non-goal); MySQL `never`/`schema_only`/`no_data` are blocking (start-position gap, invariant 3) instead of
   `snapshot.enabled=false`; MySQL `initial_only` is unsupported (no Conduit setting stops after the snapshot;
   `cdc.disableLogs` only silences logging); Elasticsearch `batch.size`/`max.retries` do not map onto the no-op
   `bulkSize`/`retries`; MySQL schema history is non-blocking instead of blocking; Postgres `skipped.operations` is
   `T` only when `t` is skipped. Also a factual fix to the base document: `kc.lint.client_override_policy` said
   `None` is the default override policy, and the Apache Kafka 4.2 default is `All` (corrected in place). **Approve
   these corrections** and let this document take precedence over the base connector subsections where they differ.
   If D3 is rejected, the in-place edit to the base document's Kafka override-policy default is reverted with it.
4. **D4. MirrorMaker dedicated-mode input.** Most MM2 deployments run `connect-mirror-maker.sh` with an
   `mm2.properties` (`clusters`, `A->B.*`), not the connector form. **Out of v1**: detect it and fail with
   `migrate.input_unrecognized` and a fix naming the connector form; the migration guide says plainly that most MM2
   deployments use `mm2.properties`. Adding it later is a parser change.
5. **D5. MCP scope.** Three offline read tools; no network flags, no registry check, no `--accept-gaps`. **Approve**,
   and revisit the registry check only with an operator-set startup flag.
6. **D6. Notes outside the completeness multiset.** Needed for absent-key defaults. **Approve only in its
   mechanical form**: a test over the pinned upstream key lists that fails when a default's row is not `T` and no
   note covers it (_Absent keys are evaluated mechanically_), not a reviewed list.
7. **D7. Where the corpus lives.** `tests/migrate/corpus/`, one corpus, two runners, replacing the
   `pkg/kcmigrate/testdata` location in the base document, with the golden normalisation of rule 1 (version and
   report hash only). **Approve** so #3010 and the connector lanes build to one layout.

## Related

- `docs/design-documents/20261012-migrate-kafka-connect.md` (#3026, the base design)
- `docs/design-documents/20261012-migrate-debezium-postgres-offset-import.md` (#3029),
  `20261012-debezium-compatible-output.md` (#3033)
- `docs/design-documents/20260707-cli-output-conventions.md`, `20260708-mcp-server.md`
- #3003 (this lane), #3004, #3005 to #3009, #3010, #3032, #3002 (tracking)
- Open drafts this document reads: #3034 (certification gaps), #3038 (Kafka mirror passthrough),
  #3040 (MySQL type semantics), #2963 (registry tier)
- KIP-1188: <https://cwiki.apache.org/confluence/x/2IkvFg>
