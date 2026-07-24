# Template: generator-file

Scaffold with:

```shell
conduit pipelines init --template generator-file
conduit run
```

No external infrastructure required. Update the destination's `path` setting
before running if `./generator-file-output.ndjson` (relative to wherever you
invoke `conduit run` from) isn't where you want the output.

## What it does

Generates a synthetic structured record once per second and appends each one,
as a line of JSON, to a local file — the simplest way to see records
persisted somewhere durable rather than just printed.

## Config reference

| Connector | Setting | Meaning |
| --- | --- | --- |
| `builtin:generator` (source) | `format.type` | `structured` — emits a record with typed fields. |
| | `format.options.id` | Generates field `id` of type `int`. |
| | `format.options.name` | Generates field `name` of type `string`. |
| | `operations` | Record operation(s) to generate; `create` only. |
| | `rate` | Records per second (`"1"`). |
| `builtin:file` (destination) | `path` | File path records are appended to. Must be writable; the file is created if it does not exist. |

## Runnable example

The exact bytes above are what `conduit pipelines init --template generator-file`
writes (module: `cmd/conduit/root/pipelines/templates/generator-file/pipeline.yaml`)
— this is not paraphrased or hand-copied, it is the literal fixture.

## Delivery semantics

- **At-least-once (Invariant 3).** A record is acknowledged upstream only
  after the file connector has written and flushed it (Invariant 1).
- The file destination appends; it never truncates or overwrites existing
  content, so re-running the pipeline against the same path grows the file
  rather than losing prior output.
- No ordering, dedup, or replay concerns: the generator source has no
  persisted position, so a restart begins a new synthetic stream.
- This template is a demo/smoke-test pipeline, not a production data path.
