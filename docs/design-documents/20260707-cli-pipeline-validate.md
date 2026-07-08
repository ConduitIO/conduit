# CLI: `conduit pipeline validate | lint | dry-run`

## Summary

The keystone of the v0.17 "CLI as product" work (execution plan §3): static,
**offline** verification of pipeline config files. It is a thin shell over the
machinery `conduit run`'s provisioning path already uses
(`parser.Parse → config.Enrich → config.Validate`), surfacing the `config.*`
`ConduitError` codes + JSON-pointer `configPath` from #2529, with `--json` and the
deterministic exit codes (validation failure → exit 2). Tier 2.

## Context — the reuse surface (everything needed already exists)

| Capability | Where | Note |
| --- | --- | --- |
| YAML parse + version dispatch | `pkg/provisioning/config/yaml/parser.go:72` | Offline; collects all per-doc errors, no fail-fast (#2255) |
| Default enrichment | `pkg/provisioning/config/enrich.go:20` | Rewrites connector/processor IDs to `pipelineID:connectorID` (`:71,95`) |
| Static field/ref/type validation | `pkg/provisioning/config/validate.go:35` | Collects every error via `cerrors.Join`, no fail-fast |
| Stable codes + `configPath` | `pkg/provisioning/config/codes.go:26` | `config.field_required/_invalid/_too_long/_id_duplicate` (#2529) |
| Exit-code classification | `pkg/conduit/exitcode` | `InvalidArgument → 2` automatically |
| Walk a joined error | `pkg/foundation/cerrors/cerrors.go:135` (`ForEach`) | needed — `conduiterr.Get` returns only the FIRST error |
| Dir-vs-file `.yml/.yaml` filtering | `pkg/provisioning/service.go:177,217` | correct semantics already |
| Group aliases singular | `cmd/conduit/root/pipelines/pipelines.go:29` (`Aliases: ["pipeline"]`) | both `pipeline` and `pipelines` resolve |

### Two gaps this plan must close (both additive, Tier 2)

1. **`Suggestion` is never populated** — `fieldError` (`codes.go:35`) takes no
   suggestion; the field exists on `ConduitError` but ships empty. The graded UX
   needs it → extend the domain validators to attach suggestions (so API/MCP get
   them too).
2. **Lint warnings are collected then discarded** — `ParseConfigurations` logs
   warnings (`parser.go:146`) and never returns them; the `warning` type is
   unexported (`linter.go:118`). `lint` needs the parser to expose them.

## Decision — verb design

- **`validate <file|dir>`** — Parse → Enrich → Validate. Static correctness only,
  pass/fail, fully offline, exit 0 or 2. Ships first (the ~90% case).
- **`lint <file|dir>`** — validate + advisory warnings (deprecated/renamed/unknown
  fields, version fallback). Warnings are advisory: exit 0 unless `--strict`. Needs
  gap-2 refactor.
- **`dry-run <file|dir>`** — validate + report the **enriched** graph (final IDs,
  injected DLQ defaults, worker counts) + optional builtin plugin-ref resolution
  (`connector.plugin_not_found`). No side effects, no server. Standalone plugin
  refs are marked "not statically verifiable" (advisory), not failed.

**Shipping order:** validate first (zero refactor beyond suggestions); lint +
dry-run in a second PR. All three share ONE internal engine so verb parity is
structural, not copy-paste. Register as subcommands of the existing
`PipelinesCommand`; the group already aliases `pipeline`, so no naming decision.

## CLI UX & ergonomics

```text
conduit pipelines validate <path> [flags]   # alias: conduit pipeline validate ...
conduit pipelines lint     <path> [flags]
conduit pipelines dry-run  <path> [flags]
```

`<path>` = one `.yml/.yaml` file OR a directory (recursed one level for
`.yml/.yaml`). No `--config.path`/gRPC flag — these are **offline** and must never
dial the API.

Flags: `--json` (all), `--strict` (lint/validate: warnings → exit 2),
`--check-plugins` (dry-run, default on), `-q/--quiet`.

**FAIL output — collect all, never fail-fast:**

```text
✗ pipelines/orders.yaml

  config.field_required   /connectors/0/plugin
    connector "pg-source": "plugin" is mandatory
    → set connectors[0].plugin (e.g. "builtin:postgres")

  config.id_duplicate     /connectors/2/id
    connector "pg-source": "id" must be unique
    → rename one of the connectors sharing id "pg-source"

3 problems in 1 file. (exit 2)
```

One block per finding, ordered by `configPath`: line 1 = `code` + `configPath`,
line 2 = validator `Message` (verbatim), line 3 = `→ Suggestion`. Directory input
groups blocks under `✗ <file>` headers. `lint` renders warnings identically with
`⚠`. The command sets `SilenceErrors` so cobra doesn't print a duplicate `Error:`
over the report.

**`--json` schema** (offline struct, not protojson): `{verb, ok, summary:{files,
pipelines, errors, warnings}, files:[{path, ok, pipelines[], findings:[{severity,
code, configPath, message, suggestion, fix?, line?, column?}]}]}`. Every field maps
to an existing source (`code`←`Code.reason`, `configPath`←`ConfigPath`,
`message`←`Message`, `suggestion`←`Suggestion` after gap 1, `fix`←`Fix` for the
future `repair` verb, `line/column`←`warning{}` for lint).

Exit codes: 0 clean; 2 any validation/parse/path error; warnings-only → 0 (2 under
`--strict`); dry-run builtin plugin-not-found → 2 (`NotFound → Validation`).

## File-level plan

- New: `cmd/conduit/root/pipelines/{validate,lint,dry_run}.go` (+ tests); register
  in `pipelines.go` `SubCommands()`.
- New shared engine `cmd/conduit/internal/validate/` — one `Run(paths, opts)
  Report` (resolve files → parse → enrich → validate → walk with `cerrors.ForEach`
  → `[]Finding`); both human render and `--json` marshal the one type.
- New offline result decorator `CommandWithResult` in
  `cmd/conduit/cecdysis/decorators.go` (the offline sibling of the client-result
  decorator — no API client). Register in `cli.go`.
- Refactors (additive): populate `Suggestion` in the provisioning validators;
  expose parser warnings; extract the dir/file resolution helper.

## Acceptance criteria

1. valid file → exit 0, `✓` per pipeline, "no problems"; `--json` `ok:true`, no findings.
2. file with N errors → exit 2; ALL N appear (not fail-fast) in human + json, each
   with non-empty code/configPath/message/suggestion.
3. **offline**: works with no server, no gRPC dial (assert no dial).
4. directory → checks every `.yml/.yaml`, aggregates per file, exit 2 if any error.
5. unparseable/unknown-version → exit 2 coded finding, not panic/exit 1.
6. `--json` valid on pass and fail, matches the schema; `code` == registered reason.
7. `lint` deprecated field → warning w/ line+column, exit 0; `--strict` → exit 2.
8. error + warning in one file → exit 2 (error dominates), both rendered.
9. `dry-run` valid → exit 0, shows enriched IDs + DLQ defaults; `--check-plugins`
   unknown builtin → exit 2; standalone ref → advisory, not a false fail.
10. `SilenceErrors` set — no duplicate cobra `Error:` line over the report.

## Failure modes

- **Enrich-before-validate order** must match `service.go:279-280` (enrichment
  mutates IDs) or validation diverges from `run`. Dry-run displays enriched IDs.
- Using `conduiterr.Get` instead of `cerrors.ForEach` silently drops all but the
  first finding (AC-2 guards this).
- Exposing warnings must NOT change `run` behavior (additive API only).
- dry-run standalone plugin false-negatives → scope `--check-plugins` to builtins.
- empty file/dir → exit 0 "0 pipelines checked", not an error.

## Optimization

Maximal reuse: validation logic, codes, `configPath`, exit-code mapping,
file resolution, `--json` decorator all exist. New code = the three command shells,
one shared render/JSON struct, the offline decorator, two additive refactors. Do
NOT write a second YAML schema, code set, `configPath` scheme, or exit-code map —
that forks the contract the API/MCP already speak (`status.go`).

## Related

- Execution plan §3 (CLI as product). #2529 (config codes). `pkg/conduit/exitcode`.
- Feeds MCP's `validate` tool (§2) and `conduit generate` (v0.19, validate-gated).

## Review outcome (2026-07-07) — SOUND-WITH-CONCERNS

Must-fix during implementation:

- **Cross-file duplicate-ID detection is NEW code, not reuse** — `findDuplicateIDs`
  (`service.go:100`) is outside `parse→enrich→validate` and returns a bare sentinel.
  Give it a `provisioning.pipeline_id_duplicate` code + `configPath`.
- **Call `cerrors.ForEach` on the UNWRAPPED Join**, never on an outer `%w` wrap — a
  re-wrapped Join collapses N findings to one (masked-bug risk).
- Populate `Suggestion` in the provisioning validators; expose parser warnings.
- Adopt the shared contract in `20260707-cli-output-conventions.md` (envelope,
  `--resolve-plugins` not `--check-plugins`, `--strict` on lint/dry-run only,
  `SilenceErrors`, no `(exit 2)` in human output, dir-run shows passing `✓` files).
