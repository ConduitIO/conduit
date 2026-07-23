# CLI output & result conventions (v0.17 "CLI as product")

## Summary

The shared contract every new v0.17 command (`pipelines validate|lint|dry-run`,
`doctor`, `connector|processor new`) MUST follow, so the CLI reads as one product
and an MCP layer can wrap all of them uniformly. Both the technical and UX reviews
of the v0.17 plans converged on this: without a single contract, three commands
drift into three private output formats and three incompatible JSON shapes.

This is a convention doc, not a feature. It is the reference the command design
docs point at.

## 1. The `--json` envelope (one shape, all commands)

Every command's `--json` output is a single JSON object to stdout with this
top-level shape, **lowerCamelCase throughout** (matching existing protojson output
like `createdAt`):

```json
{
  "command": "pipelines.validate",   // stable dotted discriminator, ALWAYS present
  "ok": false,                       // the verdict, ALWAYS present on every command
  "summary": { "...": 0 },           // command-specific counts
  "result": { "...": "..." },        // command-specific payload
  "error": null                      // null on success; set on a HARD command failure
}
```

- `ok` and `error` are present on **all** commands — an agent writes one
  success check (`.ok`) for every tool.
- `error` is for a hard command failure (bad input, preflight failure, crash), NOT
  for domain findings. A validation run that finds problems is `ok:false` with the
  problems in `result`/`summary` and `error:null` — the run itself succeeded.
- `command` is the dotted path (`pipelines.validate`, `doctor`, `connector.new`).

### Shared sub-objects (reused across commands)

A **located finding / check result** (the §1.1 structured-error shape):

```json
{ "severity": "error|warning|pass|warn|fail",
  "code": "config.field_required",
  "message": "...",
  "configPath": "/connectors/0/plugin",   // conduit.yaml dotted key OR pipeline JSON pointer — see note
  "suggestion": "set connectors[0].plugin (e.g. \"builtin:postgres\")",
  "fix": { "configPath": "...", "op": "set", "value": "" } }   // optional, powers future `repair`
```

The **`error`** object uses the same fields (`code`, `message`, `suggestion?`,
`configPath?`, `fix?`) — it maps onto `conduiterr.ConduitError`. Scaffolding's
error MUST carry `suggestion` (its human output already shows remediation text);
a bare `{code,message}` is non-conformant.

> **`configPath` note:** its _syntax_ legitimately varies — a `conduit.yaml` dotted
> key (`db.badger.path`) for doctor, a pipeline-doc JSON pointer
> (`/connectors/0/plugin`) for validate. Same field, two address spaces; document
> this on the field, do not invent two field names.

## 2. Human output conventions

- **Glyphs:** `✓` pass · `⚠` warn · `✗` fail. ASCII fallback `[OK]` / `[WARN]` /
  `[FAIL]` when not a TTY, `--no-color`, or `NO_COLOR` is set.
- **A located finding renders identically across commands** (one template — the
  reviews flagged doctor's `└`-line vs validate's inline pointer as divergent):

  ```text
  ✗ config.field_required   /connectors/0/plugin
      connector "pg-source": "plugin" is mandatory
      → set connectors[0].plugin (e.g. "builtin:postgres")
  ```

  glyph + code + configPath on line 1; message indented 4; `→ suggestion`
  indented 4. No separate `└` line.
- **Summary line:** `Summary: N passed · N warnings · N failed` (or `N files · …`
  for directory runs). Where an action is needed, follow with ONE imperative line
  (`Fix the ✗ items above, then re-run.`). **Never print the exit code** in human
  output (the shell has `$?`).
- **A single shared helper** (`cmd/conduit/internal/ui`) owns glyph-vs-ASCII
  selection, `NO_COLOR`/`--no-color`/TTY detection, and color. No command
  hand-rolls glyph logic.

## 3. Flags (org-wide vocabulary — same meaning everywhere)

| Flag | Meaning | Commands |
| --- | --- | --- |
| `--json` | machine output (envelope §1). Canonical usage string `"output the result as JSON"`. | all |
| `-q, --quiet` | suppress passing/OK lines + progress chrome; print only warnings, failures, and the summary. Exit code unchanged. | all |
| `--strict` | warnings escalate to failure (exit 2). | `lint`, `dry-run` (NOT `validate` — it is errors-only) |
| `--check <name>` (repeatable) | select which checks run. Reserved org-wide for this meaning — do NOT reuse `--check*` for a boolean toggle. | `doctor` |
| `-y, --yes`, `--force` | non-interactive confirm / overwrite (never silently clobber). | mutating commands (scaffolding) |

Validate's builtin-plugin resolution toggle is `--resolve-plugins` (NOT
`--check-plugins`, which collides with `doctor --check`).

## 4. Exit codes — always via `pkg/conduit/exitcode`

Every command routes its exit through `exitcode.ExitCode` (0 ok · 1 runtime · 2
config/validation · 3 environment). No command invents "a distinct exit code."
Multi-result commands (doctor, validate) that must reduce N findings to one exit
code take the **worst** (max) bucket — this is _new_ aggregation logic, NOT free
reuse of the single-error classifier; own it explicitly (synthesize a
`*conduiterr.ConduitError` per failing result, classify each, take the max;
environment 3 > validation 2 > runtime 1).

## 5. Cobra hygiene

Every command sets `SilenceErrors` **and** `SilenceUsage` and renders its own
error output — otherwise cobra prints a duplicate `Error:` line over the report
and, under `--json`, corrupts the single-object stdout with a stderr `Error:`.
Fold this into the shared offline `CommandWithResult` decorator so it is structural.

Under `--json`, commands MUST NOT stream human progress/`✓` lines (they would
precede/corrupt the single JSON object) — capture progress in `result.steps[]`.

## 6. Two families, not one (v0.19 workstream 8 addendum)

Verifying the repo for the shared CLI contract schema-golden test (v0.19 workstream 8; see
`v019-plans/workstreams/cli-contract.md`) surfaced a correction to this doc's original framing:
there are **two distinct, both-deliberate `--json` shapes** in production, not one universal
envelope.

- **Family A — the envelope** (§1, above): `cecdysis.CommandWithResult`. Used by one-shot
  check/action commands: `doctor`, `pipelines validate|lint|dry-run|repair|apply|deploy|init`,
  `connectors audit|bundle|install|uninstall|new`, `processors new`, `init`. Validated against
  the one committed schema at `cmd/conduit/cecdysis/testdata/envelope.schema.json`.
- **Family B — client-result passthrough**: `cecdysis.CommandWithExecuteWithClientResult`.
  `--json` is the **raw result value** — a proto message via `protojson` (matching the HTTP
  API's JSON shape) or a composite Go struct via go-json — never wrapped in the envelope. Used
  by read/query commands: `pipelines list|describe|inspect|start|stop`,
  `connectors list|describe`, `processors list|describe`, `connectorplugins list|describe`,
  `processorplugins list|describe`. This is confirmed correct, intentional, load-bearing
  behavior (see `cmd/conduit/root/pipelines/list_test.go`'s own doc comment), not a bug or a
  migration backlog item — scripts and the MCP layer already consume this shape.

Forcing one schema across both was considered and rejected: Family B's payloads are
heterogeneous proto/API messages whose shape is dictated by the gRPC/HTTP API contract, not this
CLI layer. Migrating Family B onto the envelope is a breaking change to already-consumed output
and is out of scope for v0.19 (open question for a future roadmap decision).

`cmd/conduit/cli/schema_golden_test.go`'s completeness walk enforces this: it recursively walks
`(&root.RootCommand{}).SubCommands()` and classifies every leaf as Family A, Family B, or a
named exception (below) — a leaf that registers a `--json` flag but is neither classified nor
excepted fails the build. This also means any future `--json` surface (`generate`,
`pipelines init --template`, the embed CLI) is covered automatically the moment it registers in
the command tree.

### The one documented exception

`quickstart --json` (`cmd/conduit/root/quickstart/quickstart.go`) means "emit logs and records
as JSON instead of human-readable text" — a streaming log-format toggle predating this
convention, not the envelope. This is a known, pre-existing violation of §3's flag vocabulary,
left deliberately unfixed for v0.19: `quickstart` is a long-running interactive demo command,
structurally unlike the one-shot commands this contract targets, and renaming its `--json` flag
is a separate, larger discussion (tracked in `cli-contract.md` §12). It is a named exception in
`schema_golden_test.go`'s `familyExceptions` map, not a silent omission.

## Related

- Execution plan §3 (CLI as product), §2 (MCP wraps these 1:1).
- `20260707-cli-pipeline-validate.md`, `20260707-cli-doctor.md`,
  `20260707-connector-processor-scaffolding.md` (the commands this governs).
- `pkg/conduit/exitcode`, `pkg/foundation/cerrors/conduiterr` (the backbone).
