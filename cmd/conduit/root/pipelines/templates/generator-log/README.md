# Template: generator-log

Scaffold with:

```shell
conduit pipelines init --template generator-log
conduit run
```

No external infrastructure required — both connectors are built in and need no
credentials or running services.

## What it does

Generates a synthetic structured record once per second and logs each one to
stdout at `info` level. This is the fastest way to see a pipeline actually
move records end to end.

## Config reference

| Connector | Setting | Meaning |
| --- | --- | --- |
| `builtin:generator` (source) | `format.type` | `structured` — emits a record with typed fields, not raw bytes. |
| | `format.options.id` | Generates field `id` of type `int`. |
| | `format.options.name` | Generates field `name` of type `string`. |
| | `operations` | Record operation(s) to generate; `create` only. |
| | `rate` | Records per second (`"1"`). Raise or lower to change throughput. |
| `builtin:log` (destination) | `level` | Log level used for each record (`info`). One of `trace\|debug\|info\|warn\|error`. |
| | `message` | Optional static message prefix (unset here). |

## Runnable example

The exact bytes above are what `conduit pipelines init --template generator-log`
writes (module: `cmd/conduit/root/pipelines/templates/generator-log/pipeline.yaml`)
— this is not paraphrased or hand-copied, it is the literal fixture.

## Delivery semantics

- **At-least-once (Invariant 3).** The generator source has no upstream to
  lose — every generated record is acknowledged only after the log
  destination has processed it (Invariant 1: ack after durable handling
  downstream; here, "durable" means "written to stdout," which for a demo
  destination cannot itself fail short of a process crash).
- No ordering, dedup, or replay concerns: the generator has no persisted
  position to resume from across restarts, so a restart simply starts a new
  synthetic stream rather than reprocessing anything (Invariant 6 does not
  apply — there is no upstream schema to mangle).
- This template is a demo/smoke-test pipeline, not a production data path.
