# Migration harness corpus

Real Kafka Connect configurations, sanitized, with the outcome
`conduit migrate kafka-connect` must produce for each. The harness runs them
through the command and checks that the generated pipelines validate, the
compatibility report is complete and conforms to its contract, and no input key
is silently dropped. Issue: #3010. Format and rules:
[`20261014-migrate-kafka-connect-mapping-and-harness.md`](../../docs/design-documents/20261014-migrate-kafka-connect-mapping-and-harness.md),
section "Harness corpus format".

## Layout

```text
corpus/
  certification.yaml      declared tier of every plugin migrate can emit (rule 4)
  coverage.yaml           runner tier per family; rule keys no case covers (rule 3)
  ATTRIBUTION.md          upstream repositories, licenses and notices
  <family>/<case-id>/
    case.yaml             manifest
    input/                exactly what a user hands to the command
    expected/             goldens, generated with -update once the command exists
    oracle/               compose fixture, only for runnable cases
harness/                  Go package: manifest reader, input parsers, scrub-v1, checks
```

Families: `debezium-postgres`, `debezium-mysql`, `jdbc-source`, `jdbc-sink`,
`s3-sink`, `elasticsearch-sink`, `kafka-mirror`, `common`.

## Running

```sh
go test ./tests/migrate/...                                   # corpus checks; runner checks pending
go build -o /tmp/conduit ./cmd/conduit
CONDUIT_BIN=/tmp/conduit go test -v ./tests/migrate/...       # the full harness
CONDUIT_BIN=/tmp/conduit go test ./tests/migrate/ -run TestMigrate_CLI -update   # rewrite goldens
```

Without `CONDUIT_BIN` (the normal `test` job) the tests check the corpus
itself: every manifest parses strictly, every input parses, every key a
manifest names is in its input, scrub-v1 passes, every cited repository is in
`ATTRIBUTION.md` with the declared license, and every declared plugin has a
certification entry. Everything that runs the command is reported pending.

With `CONDUIT_BIN`, `TestMigrate_CLI` runs each case through
`conduit migrate kafka-connect --json --out <tmp>` with a fixed environment
and checks, in order:

1. the `--json` envelope against `cmd/conduit/cecdysis/testdata/envelope.schema.json`;
2. the result against `pkg/kcmigrate/schema/report.v1.schema.json`;
3. the report contract: `summary` equals the entries, `ok` and the exit code
   follow the blocking entries and notes, `reason`/`fix`/`code` are present
   where required, a blocking connector is written `stopped`, secrets carry
   `target.env`, `report.json` equals the `--json` result;
4. never silently drop: per input file, the report entries are the input keys
   (as counted by the harness's own parsers) in input order at the same line or
   pointer; a rejected input must be refused with `expect.error` and write no
   pipeline; an unexpected `kc.key_unknown` fails;
5. the case's `expect`;
6. no `PLACEHOLDER_SECRET_` canary and no host path in stdout, stderr or any file;
7. determinism: a second run from another working directory with absolute
   input paths is byte-identical;
8. the goldens in `expected/` (rule 1 normalization only);
9. `conduit pipelines validate --json` on the generated pipelines;
10. after all cases, rule 4 over the plugins the pipelines actually reference
    (pending until a ready case declares an emitted plugin).

`TestMigrate_MCPParity` runs each case through the `kc_migrate` tool of
`conduit mcp` and requires the same `ok`, `summary`, `result`, `error` and
file bytes as the CLI. `TestValidator_Smoke` proves step 9 can pass and fail.

Pending is never passing. A check whose surface does not exist yet (no
`migrate kafka-connect`, no `kc_migrate`, no report schema) is skipped with
`PENDING:` and counted in the job summary. It becomes a failure when:

- `CONDUIT_BIN` is set and the rule data in `pkg/kcmigrate/rules/data` covers
  the case: its family's class file, the shared file for an unsupported-class
  case, the worker file for a `--worker` case. Families without a rule file
  stay pending, so the command landing with every class unsupported changes
  nothing, and each family's rule-file PR turns its own cases on. That PR must
  carry the goldens for those cases (`-update`), or the harness fails on
  `expected/ is missing`;
- for parity, additionally once a top-level file of `cmd/conduit/internal/mcp`
  registers `kc_migrate`;
- `MIGRATE_HARNESS_REQUIRE_CLI=1` / `MIGRATE_HARNESS_REQUIRE_MCP=1`: every case,
  ready or not;
- `MIGRATE_HARNESS_RELEASE=1`, the release gate, which also requires
  `tier: certified` for every emitted plugin.

CI: `.github/workflows/migrate-harness.yml`, on every PR (gated inside the job
by a fail-closed change detector), on push to `main` and nightly. Not yet a
required check.

Expectations describe the v0.21 target. A case whose rows depend on a lane
that has not merged (the `debezium.*` settings of lane #3032, the mirror
settings of design #3038) fails until that lane lands, and the failure names the row. That is the
release bar working, not a flaky case.

## Adding a case

1. Find a public configuration under a license on the allow-list
   (`Apache-2.0`, `MIT`, `MIT-0`, `BSD-2-Clause`, `BSD-3-Clause`, `CC0-1.0`).
   Configs from the Confluent connector repositories are under the Confluent
   Community License and are not accepted; their examples usually also exist in
   an Apache-2.0 demo repository. Design-partner configs need the partner's
   written consent, recorded by alias in `provenance.ref`.
2. Copy the nearest case directory to `corpus/<family>/<new-id>/`. The
   directory name is the case id (`[a-z0-9-]+`).
3. Put the upstream file in `input/` unchanged except for the scrub:
   - hosts become `<name>.internal.example` (any name under the reserved
     `.example` TLD, or `example.com`, `example.org`, `example.net` and their
     subdomains; `example.prod.acme.com` is a real domain and is rejected);
     IP literals must be in a documentation range (192.0.2.0/24,
     198.51.100.0/24, 203.0.113.0/24, 2001:db8::/32); `localhost` is rewritten
     too. This applies to the whole file, comments included: scrub-v1 also scans
     the raw bytes, and only a few public documentation hosts (apache.org,
     github.com, debezium.io, docs.confluent.io) are accepted there;
   - every secret value becomes exactly a `PLACEHOLDER_SECRET_<WHAT>` canary
     (`[A-Z0-9_]`) or exactly one config-provider reference (`${file:...}`),
     including passwords embedded in JDBC URLs, URL userinfo and JAAS configs. The runner fails the case if a canary appears
     in any byte the command outputs;
   - bucket names, account ids and customer identifiers become `example-*`.
   For a body embedded in docs or a shell script (a `curl -d` payload), extract
   it and name the file after the connector, since the name lived in the URL.
4. Fill `case.yaml`:
   - `provenance`: `source: vendor-doc`, `ref` pinned to a commit
     (`https://github.com/<owner>/<repo>/blob/<40-hex sha>/<path>`), `license`
     (SPDX), `copyright`, and `modifications` listing every change you made.
     Add the repository to `ATTRIBUTION.md` if it is new.
   - `command.args`: `[migrate, kafka-connect, ..., input/<file>]`. Do not pass
     `--json`, `--out`, `--force`, `--quiet` or `--no-color`; the runner adds
     what it needs. Every file in `input/` must be named here.
   - `expect`: assert only what the mapping tables decide. `exitCode`;
     `notes` (codes that must be among the connector-level notes); `entries`
     (per-key `status`, and `blocking`, `code`, `secret` where the table says);
     `error` for an input the command must reject (then no report is expected);
     `pipelines` for the number of pipeline files. Leave a field out when the
     design does not fix the outcome yet, with a comment saying what is open.
   - `expectUnknown`: keys you expect to be reported `kc.key_unknown`. Any other
     unknown key in a v0.21-family case fails it.
   - `covers`: the rule keys this case exercises, per connector class.
   - `emits`: the plugins the generated pipeline uses.
5. Run `go test ./tests/migrate/...` and fix what it reports.
6. Once `migrate kafka-connect` exists, generate the goldens with `-update`,
   read the diff like code, and commit `expected/`.

A connector lane carries the cases for its connector in the PR that certifies
it, and flips its `certification.yaml` entry to `certified` in the same PR.

## `case.yaml` fields beyond the design sketch

All additive; none changes the meaning of a field the design defines:
`provenance.license`, `provenance.copyright`, `provenance.modifications`,
`expect.entries`, `expect.error`, `expect.pipelines`. `certification.yaml` adds
the `pending` tier (an entry exists, certification is not done) next to the
registry's `certified`.

## What the input parsers count

The completeness check compares the report against keys counted by the
harness's own parsers, not by `pkg/kcmigrate`:

- `.properties`: every logical line that is not blank or a comment, with
  `java.util.Properties` rules (continuations, `=`/`:`/whitespace separators,
  escapes). Duplicate keys count twice.
- `.json`, REST wrapper `{"name": ..., "config": {...}}`: every `config` member.
  The wrapper members (`name`, `type`, `tasks`, `initial_state`) are not config
  keys and get no entry, as the report contract pins (#3069). Bare map: every
  member. REST export (`GET /connectors?expand=info`): the members of each
  `info.config` only. Duplicate members count twice; non-string values count
  like strings.
- A JSON document with no connector in it is "unrecognized" and its case must
  set `expect.error`.
