# `conduit generate` benchmark corpus and eval harness

**Status: design-only path deliverable for v0.19.** `conduit generate` itself is not implemented
in v0.19 — the command build is deferred to v0.20+ (the v0.19 build slot went to the Python
connector SDK; see `docs/design-documents/20260722-conduit-generate.md`'s Status section). What
ships now is this document, the committed benchmark corpus, and a scoring harness proven correct
against fixture data — so that when `generate` is built, the acceptance bar it must clear is
already frozen and reviewable, not invented mid-implementation.

No LLM provider client ships with this harness. It scores whatever candidate pipeline YAML it's
given; it never calls a model itself.

## Where things live

| Artifact | Path |
| --- | --- |
| Corpus (source of truth) | `cmd/conduit/internal/generate/testdata/eval_requests.yaml` |
| Scoring harness (Go) | `cmd/conduit/internal/generate/*.go` |
| Fixture tests proving the harness is correct | `cmd/conduit/internal/generate/score_test.go` |
| Fixture candidate pipelines (good and bad, by hand) | `cmd/conduit/internal/generate/testdata/candidates/` |

This file is a rendering of the corpus for human review. If the two ever disagree, the YAML file
is correct — it's what `generate.LoadRequests` and the future CI/scheduled eval job actually read.

## The two metrics — always reported separately

Per the design doc's §10, a schema-valid pipeline is not the same thing as a _correct_ one, so the
harness reports two independent numbers, never collapsed into one:

1. **Validate-pass rate** — does the candidate pass `cmd/conduit/internal/validate`'s `Run`, the
   exact engine `conduit pipelines validate` uses. Committed floor: **>= 90%** of the corpus.
2. **Semantic-intent-match rate** — does the candidate's actual source/destination connector
   category and processor set match the request's stated intent (`Expect`), even when it
   validates cleanly. A pipeline can be schema-valid and still point at the wrong connector, swap
   source/destination, or silently drop a requested filter — this is the failure mode a
   validate-pass number alone cannot see. Committed v1 floor: **>= 70%** (deliberately lower than
   the validate-pass floor — it measures a strictly harder, coarser property; see the design doc
   §10 for what it does and does not catch).

Both numbers are **medians over >= 3 repeated runs**, never best-of — the same discipline
CLAUDE.md requires of `benchi` throughput claims, applied here to model-output scoring
(`ScoreMedian` in `score.go`).

**This PR reports no live scores.** There is no provider integration yet, so there is nothing to
run the harness against in production. What's proven here is that the harness itself is correct:
given a known-good candidate it scores both metrics true, and given each specific way a candidate
can go wrong (bad schema, wrong connector, dropped filter, swapped direction, a fabricated/unknown
plugin) it scores false on exactly the axis that's wrong — see `score_test.go`. When v0.20 wires up
a provider, running it against this same corpus and reporting the resulting medians into this file
is the first real eval run.

## How a request is scored

Each corpus entry pairs a natural-language `prompt` with an `expect` block — the ground truth this
harness checks a candidate against, **not** re-derived from the prompt text by the scorer itself
(that NL-extraction heuristic is the future `generate` command's own semantic checker, design doc
§9 — a separate, harder problem this harness doesn't attempt):

```yaml
- id: postgres-cdc-to-kafka-filtered
  prompt: "stream new orders from postgres into a kafka topic, only orders over $100"
  expect:
    sourceCategory: postgres
    destinationCategory: kafka
    requiredCapabilities: [filter]
  notes: "the canonical direction + filter case named in the design doc's DX review (§1)"
```

Given a candidate pipeline YAML for this request, the scorer:

- Runs `validate.Run` against it (via a private temp-file shim — `validate.Run` has no in-memory
  entry point yet; see `validate_score.go`'s doc comment and the design doc's "disk seam"
  discussion, Decision §3).
- Parses it with the same YAML parser `validate` uses, and checks: does the source connector's
  plugin resolve to the `postgres` builtin category? The destination to `kafka`? Is there a
  processor (anywhere in the pipeline) matching each tag in `requiredCapabilities` — here,
  `filter`, which maps to the builtin `filter` processor plugin (`capability.go`)?

A candidate that validates but uses the wrong connector, swaps source and destination, drops a
required capability, or references a non-builtin/fabricated plugin name scores **fail** on the
semantic-intent axis even though it may score **pass** on validate — exactly the gap the two
separate numbers exist to expose.

A request with no candidate provided at all (e.g. a provider call that errored out every retry)
counts as a **fail on both metrics** — never silently excluded from the denominator, which would
otherwise hide the worst failure (no output at all) behind an artificially better rate.

## The corpus

28 requests (>= 25 required), every one grounded in one of Conduit's six real built-in connectors
— `file`, `generator`, `kafka`, `log`, `postgres`, `s3` — respecting what each connector actually
supports: `generator` is source-only, `log` is destination-only; the other four support both
roles. Coverage spans every connector in both a source and a destination role where that role is
real, plus nine processor capabilities (filter, mask, rename, set, convert, json-encode,
unwrap-debezium, unwrap-kafkaconnect, split, base64-decode).

| id | prompt | source | destination | required capabilities |
| --- | --- | --- | --- | --- |
| postgres-cdc-to-kafka-filtered | stream new orders from postgres into a kafka topic, only orders over $100 | postgres | kafka | filter |
| postgres-to-s3-json-export | export all customer records from postgres to an s3 bucket as json | postgres | s3 | json-encode |
| kafka-to-postgres-sync | sync messages from the kafka topic 'events' into a postgres table | kafka | postgres | — |
| kafka-to-s3-archive | archive every message from kafka into an s3 bucket as json files | kafka | s3 | json-encode |
| file-to-kafka-ingest | read lines from a local file and publish each one to a kafka topic | file | kafka | — |
| s3-to-postgres-load | load objects from an s3 bucket into a postgres table | s3 | postgres | — |
| generator-to-log-smoketest | generate some fake test records and print them to the log, just to check the pipeline runs | generator | log | — |
| postgres-to-log-debug-tap | tap postgres change events and print them to the log for debugging, don't write them anywhere else | postgres | log | — |
| kafka-to-kafka-retopic | copy every record from the kafka topic 'raw-events' into a different kafka topic 'processed-events' | kafka | kafka | — |
| file-to-s3-upload | upload files from a local directory to an s3 bucket | file | s3 | — |
| s3-to-file-backup | download objects from an s3 bucket and save them as local files for backup | s3 | file | — |
| postgres-cdc-debezium-unwrap-to-kafka | capture postgres change data and publish it to kafka, unwrapping the debezium envelope so the topic only has the actual row data | postgres | kafka | unwrap-debezium |
| kafka-connect-unwrap-to-postgres | consume kafka connect formatted messages from a topic and upsert them into postgres, unwrapping the kafka connect envelope first | kafka | postgres | unwrap-kafkaconnect |
| postgres-to-s3-mask-pii | export postgres customer records to s3, but remove the ssn field before writing | postgres | s3 | mask |
| kafka-to-s3-rename-field | archive kafka messages to s3, renaming the 'ts' field to 'timestamp' before writing | kafka | s3 | rename |
| file-to-log-tail | tail a log file and print each new line to the console | file | log | — |
| generator-to-kafka-loadtest | generate synthetic load-test records and publish them to a kafka topic | generator | kafka | — |
| postgres-to-postgres-replicate | replicate a table from one postgres database to another postgres database | postgres | postgres | — |
| kafka-to-file-export | export all messages from a kafka topic to a local file | kafka | file | — |
| s3-to-kafka-load | load objects from an s3 bucket and publish each one to a kafka topic | s3 | kafka | — |
| postgres-to-kafka-filter-and-encode | stream postgres orders to kafka as json, only include orders that are still pending | postgres | kafka | filter, json-encode |
| file-to-file-base64-decode | read a file of base64-encoded records and write the decoded content to another file | file | file | base64-decode |
| kafka-to-s3-split-batches | archive kafka messages to s3, splitting any batched records into individual records first | kafka | s3 | split |
| postgres-to-kafka-convert-types | stream postgres orders to kafka, converting the amount field to a floating point number before publishing | postgres | kafka | convert |
| generator-to-s3-loadtest | generate synthetic test data and write it to an s3 bucket for load testing | generator | s3 | — |
| kafka-to-postgres-filter-before-upsert | consume kafka events and upsert them into postgres, but skip any event marked as a test event | kafka | postgres | filter |
| s3-to-log-audit | read new objects from an s3 bucket and print an audit line to the log for each one, don't store them anywhere | s3 | log | — |
| postgres-to-kafka-set-derived-field | stream postgres orders to kafka, adding a derived 'processed_at' field with the current timestamp before publishing | postgres | kafka | set |

## Growing the corpus

Per the design doc §10, the fixture set is the spec of "what `generate` is for" — growing it (more
connector combinations, deliberately ambiguous prompts that should refuse, adversarial prompts
embedding an injected instruction) is high-leverage work, the same way expanding the SDK acceptance
suite is. Add an entry to `testdata/eval_requests.yaml`, keep this table in sync, and make sure
`generate.LoadRequests`' invariants still hold: unique, non-empty `id`, non-empty `prompt`, and (for
the committed corpus specifically) both `sourceCategory` and `destinationCategory` set to one of
the six real built-in connectors.

## Related

- `docs/design-documents/20260722-conduit-generate.md` — the full design (provider model, the
  never-auto-apply boundary, the disk-seam decision this harness's temp-file shim implements the
  fallback for).
- ROADMAP.md, Agent-native section — the v0.19 Workstream 1 item this PR advances (eval-harness
  path only; the command implementation is v0.20+).
