# CLI: `conduit pipeline inspect` + completing `lint` / `dry-run`

## Summary

Three verbs finishing the v0.17 CLI-as-product surface (§3): `lint` and `dry-run`
extend the already-merged offline validate engine
(`cmd/conduit/internal/validate`), and `inspect` is the ONE online verb — the
CLI peer of MCP `inspect_pipeline` — reporting a running pipeline's live status
and per-stage state. **Tier 2.** The offline/online split is the defining design
line: `lint`/`dry-run` never dial the API; `inspect` requires a running server.

## Context — what exists

| Need | Where | Note |
| --- | --- | --- |
| Validate engine (parse→enrich→validate) | `cmd/conduit/internal/validate` (merged) | `lint`/`dry-run` are more verbs on the same engine |
| Parser warnings (line/col) | `config/yaml/parser.go:~146`, `linter.go:~118` | collected then **discarded**; `warning` type unexported — the `lint` gap |
| Enrichment (final IDs, DLQ defaults, workers) | `config/enrich.go` | `dry-run`'s enriched-graph source |
| Builtin plugin registry | builtin connectors/processors maps | `dry-run --resolve-plugins` checks refs |
| 5-value status enum | `pkg/pipeline/instance.go:25-29` (`Running/SystemStopped/UserStopped/Degraded/Recovering`) + `status_string.go` | pin as a display AC |
| Pipeline live state | API `GetPipeline` (`pipeline_v1.go:68`), `GetDLQ` (`:189`) | status + config |
| Per-stage live records | API `InspectConnector` (`connector_v1.go:88`, server-stream), `InspectProcessor` | bounded record sample |
| Online command pattern | `cmd/conduit/root/pipelines/{describe,list}.go` + `cecdysis.CommandWithExecuteWithClientResult` | `inspect` mirrors these (dials the API) |
| Record inspector | `pkg/inspector/inspector.go` | backs the Inspect* streams |

## Decision

### `lint <path>` — validate + advisory warnings (offline)

Extend the validate engine to surface parser warnings as advisory findings
(`severity: warning`, with `line`/`column`). Additive refactor: export the
`warning` type (or a mapped result) and RETURN warnings from the parse step
instead of only logging them — **must not change `run` behavior** (the run path
keeps logging; the new return channel is additive). Warnings → exit 0 unless
`--strict` → exit 2. Deprecated/renamed/unknown fields + version fallback are the
warning sources.

### `dry-run <path>` — validate + enriched graph + plugin resolution (offline)

Validate, then report the **enriched** pipeline (final connector/processor IDs,
injected DLQ defaults, worker counts from `enrich.go`) and, with
`--resolve-plugins` (default on), resolve builtin plugin refs — unknown builtin →
`connector.plugin_not_found`, exit 2. Standalone (non-builtin) refs are marked
**advisory** ("not statically verifiable"), never a false failure. No server, no
side effects.

### `inspect <pipeline-id>` — live status + per-stage state (ONLINE)

Dials the API (mirrors `describe`/`list` via the client-result decorator).
Reports: pipeline status (the 5-value enum, rendered), config summary, DLQ state
(`GetDLQ`), and per-connector/processor live state. With `--records N` it opens
the `InspectConnector`/`InspectProcessor` stream for a **bounded** sample of
in-flight records at each stage (default 0 = no record tap). This is the CLI peer
of MCP `inspect_pipeline`, so both share the same API/inspector — MCP wraps it
1:1 in Wave 3.

## CLI UX

```text
$ conduit pipeline inspect orders
Pipeline "orders"  [●running]              (uptime 4h12m)
  source     pg-source     builtin:postgres    ✓ 1.2M records · 0 errors
  processor  redact-pii    standalone          ✓ 1.2M in / 1.2M out
  dest       s3-sink       builtin:s3          ✓ 1.2M records · lag 0
  DLQ        (none)                            0 records
```

Status glyph/label uses the 5-value enum (`running/system-stopped/user-stopped/
degraded/recovering`) via `internal/ui`. `--json` per verb, conforming to the
conventions envelope:
- `lint`/`dry-run`: same `files[]`/`findings[]` shape as `validate`, warnings as
  `severity:"warning"`; `dry-run` adds `result.enriched[]` (final IDs, DLQ, workers).
- `inspect`: `result:{pipelineID,status,uptime,stages:[{role,id,plugin,status,in,out,errors}],dlq}`.

Flags: `lint` — `--json`, `--strict`, `--quiet`. `dry-run` — `--json`,
`--resolve-plugins` (default on), `--quiet`. `inspect` — `--json`, `--records N`,
`--quiet`; online, so it takes the standard API-address flags.

Exit codes (via `exitcode`): lint 0 (2 under `--strict` w/ warnings); dry-run 0 /
2 (bad ref); inspect 0 / 2 (missing pipeline → `not_found`) / 3 (no server).

## File-level plan

- `cmd/conduit/root/pipelines/{lint,dry_run,inspect}.go` (+ tests); register in `pipelines.go`.
- Extend `cmd/conduit/internal/validate`: add warnings to the report type + a
  `lint`/`dry-run` mode on the engine (one engine, all three verbs).
- Additive refactor in `pkg/provisioning/config/yaml`: export/return parser
  warnings (guard: `run` behavior unchanged — covered by a test).
- `inspect` uses `CommandWithExecuteWithClientResult` (online) — NOT the offline
  decorator; reuse `describe.go`'s client pattern.

## Acceptance criteria

1. `lint` on a file with a deprecated field → one `warning` finding with `line`+`column`; exit 0; `--json` `severity:"warning"`.
2. `lint --strict` on the same file → exit 2; warning promoted to failure in the summary.
3. `lint` on a file with an error + a warning → exit 2 (error dominates), both rendered.
4. `run` behavior is unchanged by the warnings-exposure refactor — a test asserts a provision/run path still logs (not returns) warnings as before.
5. `dry-run` on a valid file → exit 0; output shows enriched IDs (`pipelineID:connectorID`), injected DLQ defaults, worker counts.
6. `dry-run --resolve-plugins` with an unknown builtin plugin → `connector.plugin_not_found`, exit 2; with a standalone ref → advisory line, exit 0 (not a false fail).
7. `lint`/`dry-run` are **offline** — asserted no API dial.
8. `inspect <running-pipeline>` → status (correct enum value), per-stage rows, `--json` matches schema; exit 0.
9. `inspect <missing-pipeline>` → coded `not_found` finding, exit 2 (not a panic).
10. `inspect` with no server reachable → exit 3 (environment), actionable message.
11. `inspect --records 5` → streams ≤5 in-flight records per inspectable stage, then stops (bounded, doesn't hang).
12. The 5-value status enum renders all of `running/system-stopped/user-stopped/degraded/recovering` (table-driven test over the enum) — the pinned §3 status-display AC.
13. All three `--json` outputs conform to the shared envelope (`command/ok/summary/result/error`).

## Failure modes

- Warnings-exposure must be strictly additive — if it changes what `run`
  provisions or how it logs, that's a regression (AC-4 guards).
- `dry-run` standalone-plugin false negatives → `--resolve-plugins` only asserts
  builtins; standalone stays advisory.
- `inspect` against a pipeline mid-restart / just-stopped → report the current
  enum status honestly (don't error); against a torn/streaming inspector that
  closes early → end cleanly, don't hang (AC-11 bounds it).

## Risk tier & tests

**Tier 2.** Unit + integration; the warnings-refactor gets the `run`-unchanged
regression test (AC-4); `inspect` gets an integration test against a running
in-memory pipeline. No serialized-format or data-path change.

## Related

- Execution plan §3 (CLI verb parity, the status-enum AC).
- `20260707-cli-pipeline-validate.md` (the engine these extend),
  `20260707-cli-output-conventions.md`.
- `inspect` feeds MCP `inspect_pipeline` (Wave 3, wraps the same API 1:1).

## Review outcome (2026-07-08) — SOUND (technical) / SHIP-WITH-UX-CHANGES (inline)

Verified against code:
- **lint**: warnings are already collected locally (`config/yaml/parser.go:90` `var warn warnings`) and only logged — exposing them via the parser return is a clean additive change (AC-4 guards `run` behavior).
- **inspect**: live-state APIs all exist — `GetPipeline`/`GetDLQ` (`pipeline_v1.go:68/189`), the 5-value enum (`pkg/pipeline/instance.go:25-29`), `InspectConnector` server-stream (`connector_v1.go:88`) backed by `pkg/inspector`. The online (inspect) / offline (lint, dry-run) split is correct.
- **UX fixes (minor):** the 5-value status enum renders via the shared `ui` helper (consistent label/glyph + ASCII fallback), and `validate`/`lint`/`dry-run` `--help` cross-reference each other so the three-verb split is discoverable.
