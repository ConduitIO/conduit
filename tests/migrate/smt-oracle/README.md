# SMT oracle

What real Kafka Connect and real Debezium do to a record, captured and committed, so the
`conduit migrate kafka-connect` SMT translators can be tested against it instead of against
someone's reading of the Kafka source. Design:
[`20261110-kafka-connect-smt-pack.md`](../../../docs/design-documents/20261110-kafka-connect-smt-pack.md)
(B11), roadmap item "SMT compatibility pack" (#2960).

A translator PR cannot merge without its case here, and its case is "diff clean": replay the
committed Conduit records through the processors the translator emits, and compare with what
Kafka Connect produced, after the normalizations below and no others.

## What is captured

One capture run, against one fixture database (`pg/*.sql`), produces three kinds of file:

| Path | What | Produced by |
| --- | --- | --- |
| `fixtures/debezium-postgres.json` | every message real Debezium Postgres 3.7.0.Final wrote to a real Kafka 4.2.0 broker (JsonConverter, `schemas.enable=true`, `replace.null.with.default=false`): key, value, both with their Connect schemas | `java/Consume.java` |
| `fixtures/conduit-postgres.json`, `fixtures/conduit-schemas.json` | every OpenCDC record Conduit's built-in Postgres source (built from this checkout) produced for the same snapshot and the same changes, and every schema it registered | `cmd/normalize`, `cmd/registry` |
| `goldens/<suite>.json` | the output of the Kafka Connect 4.2.0 and Debezium 3.7.0.Final SMT classes for each chain in `cases/<suite>.json`, over hand-written records and over the Debezium messages above | `java/Oracle.java` |

`fixtures/` are the inputs of a translator case, `goldens/` the expected output. Both are
committed. `goldens/provenance.json` names the image digests they came from.

Only what differs on every capture is normalized when the fixtures are written: Debezium's
`ts_ms`/`ts_us`/`ts_ns`, `lsn`, `txId`, `xmin` and `sequence`, the Kafka record timestamp, the
`__debezium.context.runId` header (a new UUID per connector start),
Conduit's `position` (it embeds the WAL position) and `opencdc.readAt`. Nothing else is touched.

The capture fails if Debezium and Conduit disagree about the workload: per table, in order, every
Debezium change needs a Conduit record with the same operation and key (`smtoracle.Pairs`).

### Why the SMT classes are called directly

The SMT stage does not run a Connect worker. `java/Oracle.java` instantiates each `Transformation`
and `Predicate` class, configures it with the string properties a worker would pass, and applies
the chain with the same condition as `TransformationStage.apply` (`predicate == null || negate ^
predicate.test(record)`). The source stage does run the real worker (Debezium) and the real broker.

We chose this over running the SMTs inside a worker because the SMTs are pure functions of one
record: a worker adds a broker, REST calls, timing and a converter, and none of them is under test.
Direct calls make a run deterministic (two runs differ in nothing; the nightly workflow relies on
it), fast, and offline. What it gives up: converter behaviour on the wire. The oracle's own
`JsonConverter` is configured with `replace.null.with.default=false` for that reason: with the
default it writes a schema's default in place of a null, which hides whether the SMT did.

The Kafka classes run on the `apache/kafka:4.2.0` image's own Java and jars. The Debezium SMTs run
from the Debezium 3.7.0.Final jars of the pinned Debezium image, on those same Kafka 4.2.0 jars
(the Debezium image ships Connect 4.3.1; 4.2.0 is the version under test).

## Findings the first capture produced

These are facts about the pinned versions, each backed by a committed file. Translator PRs rely on
them.

**Debezium Postgres propagates column defaults into the Connect schema.** `items.note` (default
`'none'`) and `items.qty` (default `5`) are `"default": "none"` and `"default": 5` in
`fixtures/debezium-postgres.json`, also when the row holds an explicit NULL. Kafka Connect then
substitutes the default in some SMTs and not in others (`goldens/pg-reshape.json`, record
`items@1`, a row with `note` and `qty` NULL):

| SMT after `ExtractNewRecordState` | `note`, `qty` |
| --- | --- |
| `ExtractNewRecordState` itself | null, null |
| `HoistField` | null, null |
| `Flatten` | **"none", 5** (no option to turn it off) |
| `ReplaceField` with `replace.null.with.default=true` (the default) | **"none", 5** |
| `ReplaceField` with `replace.null.with.default=false` | null, null |
| `ExtractField` of `note`, default true / false | **"none"** / null |

So on a Debezium source `Flatten` cannot be translated (Conduit would write the NULL KC does not),
`HoistField` can, and the others only with `replace.null.with.default=false` set explicitly.

**The jsonb fixture matches, after one normalization.** Debezium writes jsonb as a string holding
the JSON text; Conduit writes the base64 of the same text. With `Options.JSONFields` both parse to
the same value for `items.attrs` and `strict.attrs` in every captured change
(`TestReplay_NativeSourceEqualsUnwrap`). Key order and spacing come out identical here, but the
normalization parses, so it would not depend on it.

**Conduit's Postgres source (connector v0.20.0) has gaps the fixture runs into.** None is fixed
here; the capture works around each, and each is why the fixture database is split the way it is.
They are reported because translators and migration claims depend on them.

- With its default Avro schema attached, the source halts on the first NULL in a nullable column
  (`avro: *avro.null is unsupported for Avro string`): every column is written as a non-null Avro
  type. Only `strict`, which has no nullable column, is read that way. Everything else is read
  with `logrepl.withAvroSchema: "false"`, so those records carry no schema.
- It cannot build a schema for an array column (`cannot resolve field type "_text"`) or a column
  with a dotted name (`invalid name a.b`). Those live in `noschema`.
- With the schema attached, an update that leaves a TOASTed column unchanged halts the source
  (`avro: missing required field big`). `strict` is updated with its TOASTed column rewritten.
  Without the schema attached it works, and Conduit omits the unchanged column where Debezium
  writes `__debezium_unavailable_value` (`ToastAbsent` normalizes this).
- It renders `numeric` as a fraction (`"25/2"` for 12.50) and trims trailing zeros of a
  `timestamptz` (`...20Z` against Debezium's `...20.000000Z`).

**Kafka Connect 4.2 behaviours the design depends on** (`goldens/`):

- `ValueToKey` on a null value throws (`Only Struct objects supported ... found: null`), and on a
  record that lacks the field (`Field does not exist`). `HoistField` wraps a null (`{"f": null}`).
  `ExtractField` of a field the record lacks gives null on a schemaless value and throws on a struct
  (`Unknown field`).
- `ExtractField after` of a delete yields a null value, so a following `Filter` on
  `RecordIsTombstone` drops deletes as well as Debezium's own tombstones (`pg-envelope`).
  `ExtractNewRecordState` with its default `delete.tombstone.handling.mode` (3.7: `tombstone`) does
  the same (`pg-unwrap`).
- `InsertField` onto an existing field throws in schema mode (`Cannot create field because of field
  name duplication`). Renames that swap (`a:b,b:a`) work.
- `RegexRouter` matches the whole topic but replaces with `replaceFirst`, which searches again: with
  `a|ab` the topic `ab` becomes `Xb`, not `X` (`regexrouter`, `regexrouter-unicode`). `(?i)k` does
  not match the Kelvin sign (U+212A) unless `(?u)` is also set; Go's `(?i)` folds it.
- Renaming `after` before `ExtractNewRecordState` makes the unwrap fail (`Unknown field: after`).
- Debezium 3.7 no longer defines `delete.handling.mode` or `drop.tombstones`; Connect ignores the
  unknown keys, so they have no effect (`dbz-extractnewrecordstate`).
- `delete.tombstone.handling.mode` takes `drop`, `tombstone`, `rewrite`, `rewrite-with-tombstone`
  and `delete-to-tombstone`.

## Layout

```text
capture.sh                 the whole capture; see "Re-capturing"
compose.yaml               Postgres, Kafka and Kafka Connect (Debezium) for the source stage
pg/01-schema.sql           fixture database and the rows the snapshot reads
pg/02-stream.sql           changes made while both sources stream
java/Oracle.java           runs SMT chains over records, writes goldens
java/Consume.java          reads Debezium's topics into fixtures/debezium-postgres.json
cmd/registry               in-memory schema registry Conduit runs against; dumps its schemas
cmd/normalize              Conduit's file output -> fixtures/conduit-postgres.json
cases/<suite>.json         inputs: chains, and records (inline or "recordsFrom" a fixture)
goldens/<suite>.json       captured Kafka Connect / Debezium output, one result per record
fixtures/                  what the real sources produced
smtoracle.go, fixtures.go  Go: load cases, goldens and fixtures; pair Debezium and Conduit records
replay/                    Go: replay a Conduit chain over the fixtures, and the diff helper
```

Suites: `filter-predicates`, `regexrouter`, `regexrouter-unicode`, `replacefield`, `insertfield`,
`extractfield`, `hoistfield`, `valuetokey`, `flatten`, `replace-null-with-default`, `chains` (all
over hand-written records, schemaless and with schemas), `dbz-extractnewrecordstate` and
`chains-debezium` (hand-built Debezium envelopes, schemaless and with schemas), and `pg-unwrap`,
`pg-reshape`, `pg-envelope` (over what real Debezium Postgres wrote).

v0.23 SMTs (`Cast`, `TimestampConverter`, `TimezoneConverter`, `ExtractChangedRecordState`,
`EventRouter`) are not captured; adding them is a new suite, not a change to the program.

## Using it from a translator PR

Everything below is Docker-free and runs in the ordinary `test` job.

```go
suite, _ := smtoracle.Load("pg-reshape")          // the golden
pairs, _ := smtoracle.Pairs()                     // Debezium change <-> Conduit record, per table
recs, _ := replay.Records()                       // the committed OpenCDC records
r, _ := replay.New(ctx, schemas)                  // in-memory schema registry loaded with the capture
outs, _ := r.Run(ctx, []replay.Step{{             // the chain the translator emitted
    Plugin: "builtin:field.exclude", Settings: map[string]string{"fields": ".Payload.After.big"},
    Condition: `{{ ne (printf "%v" .Operation) "delete" }}`,
}}, recs)
for _, p := range pairs {
    res, _ := suite.Find("exclude", p.Debezium)
    kc, _ := replay.FromKafkaConnect(*res)
    cd, _ := replay.FromConduit(outs[p.Conduit])
    diffs, _ := replay.Compare(kc, cd, replay.PostgresOptions(p.Table))
    // diffs must be empty
}
```

`replay.Run` goes through `processor.RunnableProcessor`: the condition, the deep copy the engine
hands every processor, the schema middleware and the built-in registry behave as in a pipeline. A
processor that resolves schemas (`avro.encode`, `error`, a registry assertion) reads the in-memory
registry, which holds the captured schemas at the versions they had. Filtered and failed records are
reported as such. `replay_test.go` shows the pattern, including a chain that must differ (a delete
turned into `{}`, #3147) so the harness is known to be able to fail.

### Normalizations

`replay.Options` is the complete list. Each one has a test showing both representations becoming one
value, and a real difference surviving it. They cover how the two Postgres _sources_ represent a
column, never what an SMT did.

| Option | Debezium | Conduit |
| --- | --- | --- |
| tagged Java types (always) | `{"$int32": 5}`, `{"$date": ...}`, `{"$bytes": ...}` | plain JSON; ints are widened |
| `JSONFields` | JSON text in a string | base64 of the JSON text |
| `DecimalFields` (column, scale) | decimal number | fraction, `"999/100"` |
| `TimestampFields` | `...20.000000Z` | `...20Z` |
| `ToastAbsent` | `__debezium_unavailable_value` | the column is missing |
| `NullAsAbsent` | null | missing |

`replay.PostgresOptions(table)` declares which columns of the fixture database need which.
`replay.Records()` also turns the typed nil that decoding JSON null produces into an untyped nil: a
source leaves the interface nil, and the engine's per-processor `Clone` would otherwise turn a typed
nil into `{}` before any processor ran.

Representation differences the options cannot cover are not hidden. A translator that needs a new
normalization adds it to `replay.Options` with its test in the same PR.

### Adding a case

1. Add the chain to a suite in `cases/` (or a new `cases/<suite>.json`). A run is `{"run": name,
   "chain": [{"alias", "type", "config", "predicate", "negate"}], "predicates": {...}}`. `type` is a
   short name under `org.apache.kafka.connect.transforms` (`Cast$Value`, `Filter`) or a full class
   name (`io.debezium.transforms.ExtractNewRecordState`). `config` values are strings, as in a
   connector config. Records are inline (`records`) or `"recordsFrom": "debezium-postgres.json"`.
   Inline records are JSON; a Java type JSON cannot tell apart is a one-key tagged object
   (`{"$int32": 5}`, `{"$int8"}`, `{"$int16"}`, `{"$float32"}`, `{"$double": "NaN"}`, `{"$decimal"}`,
   `{"$bytes": base64}`, `{"$date": ISO-8601}`); `keySchema`/`valueSchema` give a record a Connect
   JSON schema; `"recordType": "source"` makes `SourceRecord`s (the default is `SinkRecord`).
2. `tests/migrate/smt-oracle/capture.sh` (Docker, a few minutes) rewrites `goldens/` and
   `fixtures/`. To change only the SMT stage, which needs no database and finishes in seconds, run
   `SKIP_SOURCE=1 tests/migrate/smt-oracle/capture.sh`.
3. Commit the new golden. `go test ./tests/migrate/smt-oracle/...` checks that every case has a
   result for every record and that the fixtures pair up.
4. In the translator's test, replay the emitted chain and `Compare`.

To change the fixture database, edit `pg/*.sql` and run a full capture. The capture needs no
changes for a new table, but the Conduit pipeline in `capture.sh` lists the tables it reads, and a
table Conduit cannot build a schema for goes in the `pg` connector (schema off), not `strict`.

## Re-capturing

```sh
tests/migrate/smt-oracle/capture.sh              # rewrite goldens/ and fixtures/ in place
tests/migrate/smt-oracle/capture.sh /tmp/out     # write them elsewhere
git diff tests/migrate/smt-oracle                # what changed
```

Needs Docker with Compose, Go, and about 3 GB of free memory; it takes 4 to 8 minutes. Every
container, network and volume it creates is named `smtoracle-<pid>` and is removed when it exits;
nothing else on the host is touched and no host port is fixed. The nightly workflow
(`.github/workflows/smt-oracle.yml`, also `workflow_dispatch`) runs it and fails on any difference
from the committed files, then opens (or comments on) an issue. A difference is never re-captured
over without finding out why: the files are the specification.

### Pinned images

| | Image | Version |
| --- | --- | --- |
| Kafka Connect SMT classes, broker | `apache/kafka@sha256:9516fb7634bad307d17c33b589fde9023003b0cb761374f500002b980a3149b9` | 4.2.0 |
| Debezium Postgres connector and SMTs | `quay.io/debezium/connect@sha256:b9b983eda59570c526ddffa86ff73e56c53180d2676158ba5a5184cfd1b59a79` | 3.7.0.Final |
| Postgres | `quay.io/debezium/postgres@sha256:16f982a4ae010d1795db1974a0c995292869755ec2902586512a274d8d9971bd` | 16 |
| Compiling the Java | `eclipse-temurin@sha256:ebfbb18d11ff163ee83b21258acf12248af48d22a0fdfdc68fa1a1516c05976a` | 21-jdk |

All four are manifest-list digests, set at the top of `capture.sh` (and written to
`goldens/provenance.json`). Bumping one is a commit that changes the digest and re-captures;
review the golden diff as the behaviour change it is.

### If an image disappears

`quay.io/debezium/connect@sha256:65a33b21...`, the digest `tests/debezium/capture.sh` pinned for
3.7.0, no longer resolves on quay.io (the manifest is gone; the `3.7.0.Final` tag now points at the
digest above). Tag-only pins are not enough, and neither is a digest on a registry we do not
control. Two fallbacks, in order:

1. **Mirror.** Anyone with push access to a registry the project controls copies the exact manifest
   list, which keeps the digest:

   ```sh
   docker buildx imagetools create --tag ghcr.io/conduitio/mirror/debezium-connect:3.7.0.Final \
     quay.io/debezium/connect@sha256:b9b983eda59570c526ddffa86ff73e56c53180d2676158ba5a5184cfd1b59a79
   ```

   (and likewise for the other three), then run with `DEBEZIUM_IMAGE=ghcr.io/conduitio/mirror/debezium-connect@sha256:b9b983...`.
   The mirror is not set up yet: it needs a maintainer with package-write access. Do it before the
   first release that relies on this oracle.
2. **Jars.** The SMT stage needs only jars. Fetch `org.apache.kafka:connect-api`,
   `connect-transforms`, `connect-json`, `connect-runtime`, `kafka-clients` and their dependencies
   at 4.2.0 (or take `libs/` from the `kafka_2.13-4.2.0.tgz` release archive), and the `debezium-*.jar`
   files of the 3.7.0.Final Postgres connector (group `io.debezium` on Maven Central; the SMTs are in
   `debezium-connect-plugins`), and run
   `KAFKA_LIBS_DIR=... DEBEZIUM_JARS_DIR=... SKIP_SOURCE=1 capture.sh`. That reproduces the goldens
   from the committed fixtures (it was tested to produce identical output to the image run). Only
   re-capturing the fixtures needs Debezium Postgres itself, which has no jar-only form; if its
   image is lost for good, the committed fixtures stay valid as inputs and the goldens stay
   reproducible, and the fixtures are re-captured against whichever 3.7.x image is available, with
   the diff reviewed.

PR CI never needs any of this: it runs `go test ./tests/migrate/smt-oracle/...` over the committed
files.
