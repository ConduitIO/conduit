# `conduit pipelines repair` + MCP `repair` — design/plan

_Status: proposed (design/plan only — no implementation). Advances the Phase 1 execution
plan (`docs/design-documents/20260704-phase-1-execution-plan.md`) §3 (CLI verb parity) and §2
(agent-native), which call for `conduit pipeline repair [--apply]` as a "thin applier of the
structured `fix` field, sharing MCP `repair`'s engine so the human isn't a second-class citizen
to the agent."_

> **The honest headline, up front.** The structured `Fix` field this feature applies **already
> exists** in the codebase and already round-trips over gRPC and JSON. What does **not** exist is
> any error site that *populates* it: every `Fix` produced by Conduit today is `nil`. `repair` is
> therefore **not** "wire a command over existing data." The load-bearing work is **fix
> synthesis** — teaching a scoped set of error sites to emit a real, machine-appliable `Fix`. This
> doc treats that as the feature and the command as the thin shell the plan already promised it
> would be. See §10 for the honest effort split.

---

## 1. Problem & use-case

Conduit already tells a user (and an agent) *what* is wrong and *where*: a `ConduitError` carries
a stable `Code`, a `ConfigPath` (JSON-pointer), and a human `Suggestion`
(`pkg/foundation/cerrors/conduiterr/conduiterr.go:164-172`). Roughly 40 error sites populate
`Suggestion` today (e.g. `pkg/provisioning/config/validate.go`, `pkg/connector/service.go:132`,
`cmd/conduit/internal/validate/engine.go:243`). What neither the human nor the agent can do is
**apply** the fix without hand-editing YAML.

The execution plan's cohesion through-line (§9) is "errors teach — what/where/how-to-fix — and
are **actionable**": CLI applies `fix`, UI one-click, MCP `repair`, *all sharing one fix engine*.
`repair` is the CLI+MCP half of that promise.

Two concrete use-cases:

- **Human, bare terminal.** `conduit pipelines validate orders.yaml` reports
  `config.field_invalid` at `/pipelines/0/connectors/1/type`. Instead of opening an editor,
  `conduit pipelines repair orders.yaml` shows the proposed edit; `--apply` writes it.
- **Agent, MCP.** An agent that just got a structured error with a `Fix` calls the `repair` tool,
  sees the same proposed edit (classified for safety), and — if the operator allowed mutations and
  the fix is not data-path-adjacent — applies it. If it *is* data-path-adjacent, the agent is
  refused and told to route to the human Tier-1 path (§7).

**Non-goal:** `repair` is not a config generator, not a linter-autoformatter, and not a substitute
for `deploy`/`apply`. It edits a pipeline **config file** to clear specific coded findings. Getting
the repaired config into a running engine is still `deploy`/`apply`'s job (and its Tier-1 gate).

---

## 2. What already exists (grounding — cite-checked)

| Thing | Where | State |
| --- | --- | --- |
| `Fix` struct `{ConfigPath, Op, Value}` | `pkg/foundation/cerrors/conduiterr/conduiterr.go:157-162` | **Exists.** JSON-tagged, matches §1.1's `{configPath, op, value}` exactly. |
| `ConduitError.Fix *Fix` field | `conduiterr.go:164-172` | **Exists.** Inherited through `Wrap`/`WithCode` (`conduiterr.go:178-179, 226`). |
| `Fix` over gRPC status (encode/decode) | `conduiterr/status.go:60-66, 120-127` | **Exists.** Carried as JSON in the `google.rpc.ErrorInfo` `fix` metadata key; round-trip test `status_test.go:TestRoundTrip_AllFields`. |
| `Fix` in the offline validate `Finding` shape | `cmd/conduit/internal/validate/report.go:44` | **Exists.** `Finding.Fix *conduiterr.Fix`, plumbed from the error at `engine.go:263, 277`. |
| Sites that *set* a real `Fix` value | — | **None.** Grep for `Fix:` / `.Fix =` in non-test code returns only the four *plumbing* copies (`validate/engine.go:263,277`, `mcp/result.go:106`, `cecdysis/result.go:326`) and the inheritance in `conduiterr.go`. **Every `Fix` is `nil` at runtime today.** |
| `Suggestion` (human text) population | ~40 sites | Exists — but `Suggestion` is prose, not machine-appliable. |
| Diff-first / plan-hash pattern to copy | `cmd/conduit/root/pipelines/apply.go`, `cmd/conduit/internal/deploy/`, `pkg/provisioning/plan.go` | **Exists.** `deploy` computes a `Diff`+`Hash` (read); `apply` re-computes and refuses unless the presented hash matches (`provisioning.CodePlanStale`). This is the exact UX `repair` mirrors. |
| MCP tool pattern + `--allow-mutations` gate | `cmd/conduit/internal/mcp/tools_deploy.go`, `server.go:157-176`, `root/mcp/mcp.go:57` | **Exists.** Read tools always registered; write tools (`apply`, `scaffold_*`) registered only when the operator started `conduit mcp --allow-mutations` — never an agent-passable argument (`server.go:43-50`). |
| Data-path classification primitive | `pkg/provisioning/plan.go:81-92` (`Effect`: `EffectInPlace`/`EffectRestart`), `Change.ConfigPaths` (`plan.go:94-106`) | **Exists** for the deploy/apply path — reusable to classify whether an *applied* fix would disturb a running pipeline. |
| Closest-match / "did you mean" for plugin names | — | **Does not exist.** No Levenshtein/suggest logic anywhere. This bounds which errors can get a deterministic fix in v1 (§6). |

**Conclusion:** the data model and the wire/offline plumbing for `Fix` are done and tested. The
applier and — critically — the *producers* are not.

---

## 3. Fix-data model (confirm the existing one; do not reinvent)

Use the existing type verbatim. Do **not** add a parallel model.

```go
// pkg/foundation/cerrors/conduiterr/conduiterr.go:157
type Fix struct {
    ConfigPath string `json:"configPath,omitempty"` // JSON-pointer to the field to change
    Op         string `json:"op,omitempty"`         // "set" | "remove" | "add"
    Value      string `json:"value,omitempty"`      // new value, for set/add
}
```

Two refinements this plan adds (both backward-compatible, additive):

1. **`Op` becomes a closed enum, validated.** Today `Op` is a free `string`. The applier must
   reject any `Op` outside `{set, remove, add}` rather than silently no-op. A `rename` op is
   *tempting* for deprecated-field migration but is expressible as `add` new-key + `remove`
   old-key; keep the op set at three and represent a rename as an ordered **`Fix` bundle** (below)
   so the applier stays trivial.

2. **A finding may carry an ordered list of fixes, not just one.** The wire field stays a single
   `*Fix` (unchanged public contract). For multi-edit fixes (rename = add+remove) the *producer*
   emits the primary `Fix` and the repair engine expands known compound codes into their edit
   sequence. Rationale: changing `ConduitError.Fix` to `[]Fix` is a public-contract change to the
   error shape (§1.1 froze it); a compound-fix registry keyed by `Code` avoids that. Documented as
   an explicit trade-off; revisit if compound fixes proliferate.

**`Value` typing.** `Value` is a string. For non-string YAML targets (e.g. `workers: 1`, a number)
the applier interprets `Value` per the target node's expected type from the config schema, not by
guessing. A `Fix` whose `Value` cannot be coerced to the target type is a producer bug and fails
the applier's own validation (§8 test plan), never a silent bad write.

---

## 4. The shared fix-applier engine (one code path, CLI + MCP)

New internal package **`cmd/conduit/internal/repair`**, sibling to `internal/deploy` and
`internal/validate`, following their established "engine that thin cobra/MCP shells call" shape
(`internal/deploy/deploy.go` package doc is the template). **Neither the CLI command nor the MCP
tool contains repair logic; both call this package.** That is the "human isn't second-class"
guarantee, made structural.

### 4.1 Surface

```go
package repair

// Collect runs the offline validate engine over path (validate.Run — the SAME
// engine validate/lint/dry-run/deploy already use, engine.go:46), gathers every
// Finding that carries a non-nil Fix, classifies each, and returns a Plan.
// Pure read: no file writes, no store access.
func Collect(ctx context.Context, path string) (Plan, error)

// CollectContent is the content-in variant for MCP (mirrors deploy's
// withTempConfigFile pattern, tools_deploy.go:38): same engine, YAML content
// instead of a path.
func CollectContent(ctx context.Context, content string) (Plan, error)

// Apply re-collects, verifies the presented hash still matches (else
// repair.plan_stale), verifies each selected fix STILL applies to the current
// bytes (else repair.fix_no_longer_applies), refuses any data-path-adjacent fix
// unless escalate==true (else repair.data_path_fix_refused), then performs the
// edits on the YAML preserving comments/formatting. Returns the repaired bytes
// and a per-fix applied/skipped report. Writing to disk is the caller's choice
// (CLI writes the file; MCP returns the bytes).
func Apply(ctx context.Context, in ApplyInput) (Result, error)

type Plan struct {
    Path     string          `json:"path,omitempty"`
    Hash     string          `json:"hash"`     // digest of (source bytes + ordered fixes); binds Apply
    Fixes    []ProposedFix   `json:"fixes"`
}

type ProposedFix struct {
    Code       string          `json:"code"`       // the finding's error code
    Message    string          `json:"message"`
    ConfigPath string          `json:"configPath"`
    Fix        conduiterr.Fix  `json:"fix"`
    Class      FixClass        `json:"class"`      // safe | restart | data_path
    // Before/After are the rendered single-line YAML snippets for the diff UX.
    // Never contains connector Settings values (secrets) — see plan.go Change doc.
    Before     string          `json:"before,omitempty"`
    After       string         `json:"after,omitempty"`
}

type FixClass string
const (
    FixClassSafe     FixClass = "safe"      // metadata/structural; auto-appliable
    FixClassRestart  FixClass = "restart"   // would be EffectRestart if deployed to a running pipeline
    FixClassDataPath FixClass = "data_path" // ack/position/checkpoint-adjacent; human Tier-1 only
)

type ApplyInput struct {
    Path, Content string   // exactly one set (path for CLI, content for MCP)
    Hash          string   // required; from a prior Collect
    Select        []string // configPaths to apply; empty = all SAFE fixes only
    Escalate      bool     // human-set: permit a data_path fix. NEVER settable by an MCP agent.
}
```

### 4.2 Why classify, and how (the Tier-1 crux)

A `Fix` carries only a `ConfigPath`. The engine classifies each fix's pointer against the
data-integrity invariants **before** offering to apply it:

- **`data_path`** — pointer matches a data-path-adjacent prefix: `/…/connectors/*/settings/*`
  (source table/collection/position-affecting keys live here), `/…/connectors/*/plugin`,
  `/…/connectors/*/type`, any DLQ config, and any connector/pipeline **`id`** on a pipeline that
  **already exists in the store** (connector positions are keyed by connector ID — renaming one
  orphans its saved position → invariant 2/3 risk). These are **refused for auto-apply** and routed
  to the human Tier-1 path (§7). This directly honors execution-plan §2: "agent `repair` touching
  ack/position/checkpoint-adjacent config still requires the human Tier-1 sign-off path."
- **`restart`** — would classify as `EffectRestart` (`plan.go:84-88`) if deployed against a
  running pipeline, but is not itself position/ack-adjacent. Auto-appliable to the **file**;
  flagged so the human/agent knows the subsequent `apply` will be refused on a running pipeline.
- **`safe`** — pipeline/processor metadata and structural fields with no data-path effect (§6).

The classification is deliberately **conservative / default-deny**: an unclassifiable pointer is
treated as `data_path` (refuse), never as `safe`. Mirrors `deploy.NewLocalService`'s default-deny
posture (`internal/deploy/service.go`).

### 4.3 What repair mutates, and what it does not

`repair` edits a **config file** (or returns edited content). It does **not** write to the pipeline
store and does **not** touch a running pipeline. This is the single most important safety property:
the mutation is a text edit to YAML, reviewable as a diff, and the only path from there into a live
engine is `deploy`/`apply`, which keeps its own plan-hash + Invariant-7 running-pipeline guard
(`provisioning.CodePipelineRunning`, `plan.go:250`). `repair` therefore *cannot* skip, corrupt, or
reorder a record directly; the worst it can do is write a bad config that `deploy` then refuses or
that `validate` catches on re-run (which the applier does automatically, §8).

The YAML edit MUST preserve comments and formatting (generated configs are commented — §1.2). Use a
node-level YAML editor over the existing parser's AST, not a marshal-from-struct round-trip (which
would strip comments). This is a real implementation cost, called out in §10.

---

## 5. Diff-first UX (mirror deploy/apply exactly)

### 5.1 CLI — `conduit pipelines repair`

Signature: `conduit pipelines repair <file> [--apply] [--plan-hash H] [--fix PATH]... [--escalate]
[--json] [--no-color]`.

- **Read (default, no `--apply`):** `Collect` → render the proposed fixes as a diff (before/after
  per finding, class-tagged), print the `Hash`. Mutates nothing. Exit 0 if fixes exist, exit 0 with
  an "already clean" summary if none.
- **Apply (`--apply`):** requires `--plan-hash H` (from the read step) *or* `--yes` to bind to a
  freshly recomputed plan — identical ergonomics to `apply.go:112-119, 138-152`. Applies **only the
  `safe` fixes** by default; `--fix <configPath>` selects specific ones; a `data_path` fix requires
  `--escalate` **and** prints the Tier-1 warning. Re-validates the result and writes the file
  atomically (temp + rename — never a torn config file). On `--plan-hash` mismatch: refuse with
  `repair.plan_stale` (exit 2), nothing written.
- `--json` on both, per CLI output conventions (`docs/design-documents/20260707-cli-output-conventions.md`).
  Result object reuses the `Plan`/`Result` shapes above through the `cecdysis` result path
  (`cmd/conduit/cecdysis/result.go`), so `--json` is O(1) per the framework route §1.1 flags.

Registered under the existing `pipelines` command group alongside `deploy`/`apply`
(`cmd/conduit/root/pipelines/pipelines.go`).

### 5.2 MCP — `repair` (read) + `repair_apply` (write, gated)

Mirror the `deploy`/`apply` two-tool split (`tools_deploy.go`, `server.go:117-176`), content-in:

- **`repair`** (always registered, read-only): input `{config: <yaml>}`; output `Plan` (proposed
  fixes, classes, hash). Mutates nothing — parity with the always-on `deploy` tool.
- **`repair_apply`** (registered only under `--allow-mutations`, `server.go:157`): input
  `{config, hash, select?}`; output repaired content + per-fix applied/skipped report. **Applies
  `safe` fixes only.** A `data_path` fix is **never** applied by this tool — it is returned as
  `skipped` with `repair.data_path_fix_refused` and the instruction to use the human CLI path with
  `--escalate`. There is deliberately **no** agent-settable `escalate` field: `ApplyInput.Escalate`
  is reachable only from the CLI, keeping execution-plan §2's Tier-1 gate real rather than theater
  (same philosophy as `AllowMutations` being process-set only, `server.go:43-50`).
- Hash required, no "skip the hash" escape hatch for MCP — identical to the apply tool's stance
  (`tools_deploy.go:71-79`).

---

## 6. Which error classes get a `Fix` in v1 (scoped — do not boil the ocean)

This is the real feature. **Selection rule: a fix ships in v1 only if its `Value` is
*deterministic* (no human judgement, no closest-match guess) AND its `ConfigPath` is not
data-path-adjacent.** That rule is strict on purpose — it is why the v1 set is small.

**In scope for v1 (fix producers to add):**

| # | Error / code | Site | Fix | Class |
| --- | --- | --- | --- | --- |
| 1 | Deprecated/renamed field | parser lint warnings (`config/yaml` linter, surfaced as `validate` warnings, `engine.go:warningFinding`) | rename old key → canonical new key (compound add+remove, §3.1). Semantics-preserving by definition. | `safe` |
| 2 | `config.field_invalid` on `/status` (`validate.go:55`) | invalid pipeline status enum | `set` to the canonical enum value (`running`/`stopped`) when the invalid value unambiguously maps (e.g. case/whitespace normalization); otherwise **no fix** (ambiguous). | `safe` (lifecycle metadata, not record path) |
| 3 | `config.field_invalid` on processor `/workers` negative (`validate.go:126`) | `set` to `1` — the ordering-preserving default (workers>1 can reorder within a key, inv. 4; 1 is the safe direction). | `restart` (flag it) |
| 4 | `config.field_too_long` on `/description` (`validate.go:51`) | `set` to the value truncated to the limit. Lossy but non-data-path; offered, never auto-applied silently (shown in diff). | `safe` |

**Explicitly OUT of scope for v1 (and why — the honest part):**

- **Connector plugin not found** (`validate/engine.go:242`, `connector/builtin/registry.go:202`):
  the correct plugin name is not mechanically knowable — there is **no closest-match logic in the
  tree** (§2). A fix here would be a guess. Deferred until a `did-you-mean` index exists (its own
  task). We keep the existing human `Suggestion`; we do not fabricate a plugin name (mirrors the
  §2 rule "unknown connector → never a fabricated plugin name").
- **Connector `/type` invalid** (`validate.go:93`): source-vs-destination is a semantic choice, not
  derivable from the file. No fix.
- **Missing required `/plugin`, `/id`, `/type`** (`validate.go:73,85,89`): no deterministic value
  to supply. No fix.
- **ID duplicate** (`validate.go:101,130`): a suffix-rename *looks* mechanical but changing a
  connector ID rekeys its stored position (data-path, inv. 2/3). Classified `data_path`; **no
  auto-fix in v1** even for a new pipeline, because the applier cannot cheaply prove the pipeline is
  new at collect time. Revisit once the store-state check is available to the engine.
- **Anything under connector `settings`**: data-path by construction. Never auto-fixed.

**v1 target: producers for classes 1–4 above, ~4–8 error sites.** That is the deliberate,
defensible starter set. Everything else keeps its human `Suggestion` and gets `nil` `Fix` (exactly
as today), so `repair` simply reports "no machine-appliable fix for this finding" — honest, not
broken.

---

## 7. Error codes (register in the existing `conduiterr` registry)

New codes, `conduiterr.Register` (same pattern as `provisioning/codes.go`), each with a stable
reason + gRPC category:

| Code (reason) | gRPC | Raised when |
| --- | --- | --- |
| `repair.plan_stale` | `FailedPrecondition` | presented `--plan-hash` ≠ freshly recomputed hash (file changed since the preview). Direct analogue of `provisioning.plan_stale`. |
| `repair.fix_no_longer_applies` | `FailedPrecondition` | a selected fix's target no longer matches (field already changed/removed) — even if the overall hash matched, a per-fix pre-check failed. |
| `repair.data_path_fix_refused` | `FailedPrecondition` | a `data_path`-class fix was selected without `--escalate` (CLI), or at all via MCP `repair_apply`. Message routes to the human Tier-1 path. |
| `repair.ambiguous_fix` | `FailedPrecondition` | more than one candidate fix targets the same `ConfigPath` and none was explicitly `--fix`-selected. |
| `repair.no_fixes_available` | `InvalidArgument` | `--apply` requested but the plan contains zero appliable fixes. (Read mode with no fixes is exit 0, not an error.) |

Exit-code mapping falls out of the existing gRPC-category → exit-code table
(`pkg/conduit/exitcode`): `FailedPrecondition`/`InvalidArgument` → exit 2 (config/validation),
consistent with `apply`.

---

## 8. Acceptance criteria (detailed, testable checklist)

**Data model & producers**

- [ ] **AC-1** For each v1 error class (§6 rows 1–4), the producing site emits a non-nil
      `ConduitError.Fix` with a correct `ConfigPath`, a valid `Op ∈ {set, remove, add}`, and a
      `Value` that, when applied, makes the file pass `validate` for that finding. (Test: golden
      per class — bad file in, `Fix` asserted field-by-field.)
- [ ] **AC-2** The `Fix` survives the full pipeline: producer → `validate.Report.Finding.Fix` →
      gRPC `ErrorInfo` `fix` metadata → `FromStatus` → identical `Fix`. (Extends the existing
      `status_test.go` round-trip to the real producers.)
- [ ] **AC-3** An `Op` outside `{set, remove, add}`, or a `Value` uncoercible to the target node
      type, is rejected by the applier with an internal error and **never written**. (Test:
      malformed-`Fix` fixture.)

**Shared engine (one path CLI+MCP)**

- [ ] **AC-4** `repair.Collect` and `repair.CollectContent` return byte-identical `Plan.Fixes` /
      `Plan.Hash` for the same config supplied as a file vs as content. (Test asserts CLI and MCP
      see the same plan.)
- [ ] **AC-5** Every fix in a `Plan` carries a `Class`; an unclassifiable `ConfigPath` is
      `data_path` (default-deny). (Table test over crafted pointers incl. an unknown one.)
- [ ] **AC-6** `repair.Apply` is a pure re-computation + edit: given the same inputs it produces
      byte-identical output; it performs **no** store access and **no** running-pipeline mutation.
      (Test: apply against a fixture with no engine/store wired at all.)

**Diff-first UX**

- [ ] **AC-7** Read mode (`repair <file>` / MCP `repair`) writes nothing to disk and returns a
      hash. (Test: file mtime/bytes unchanged; hash non-empty.)
- [ ] **AC-8** `--apply` without `--plan-hash` and without `--yes` is refused with an actionable
      `CodeInvalidArgument` (mirror `apply.go:112-119`). (Test.)
- [ ] **AC-9** `--apply --plan-hash H` where the file changed since the preview is refused with
      `repair.plan_stale` (exit 2), file unchanged. (Test: mutate file between collect and apply.)
- [ ] **AC-10** Applied output **passes `validate`** for every finding it claimed to fix, and does
      not introduce a new finding. (Test: re-run `validate.Run` on the result; assert clean for the
      targeted codes.) *This is the safety net that makes a bad producer visible.*
- [ ] **AC-11** The write is atomic (temp + rename); a simulated crash mid-write leaves the original
      file intact. (Test: inject a write failure; assert original bytes.)
- [ ] **AC-12** Comments and unrelated formatting in the config are preserved across a repair.
      (Test: commented fixture in, diff shows only the targeted line changed.)
- [ ] **AC-13** `--json` on read and apply emits one JSON object and nothing else on stdout
      (logs to stderr, per `deploy/service.go:stderrLogger`). (Test: stdout parses as one object.)

**Tier-1 / safety (the load-bearing ACs)**

- [ ] **AC-14** A `data_path`-class fix is **refused** by `repair --apply` unless `--escalate` is
      passed, with `repair.data_path_fix_refused` and a message naming the human Tier-1 path.
      (Test.)
- [ ] **AC-15** MCP `repair_apply` **never** applies a `data_path` fix — returns it `skipped` with
      `repair.data_path_fix_refused` — and exposes **no** field an agent could set to force it.
      (Test: attempt via tool input; assert refusal and that no such field exists in the schema.)
- [ ] **AC-16** MCP `repair_apply` is **not registered** unless the server was started with
      `--allow-mutations`; `repair` (read) always is. (Test mirrors `server_test.go` for
      apply/deploy.)
- [ ] **AC-17** `repair` (read) and `repair_apply` default to applying **`safe` fixes only**;
      `restart`/`data_path` require explicit selection (and `data_path` the CLI `--escalate`).
      (Test.)
- [ ] **AC-18** Applying a fix does not, by itself, start/stop/mutate any pipeline in the store.
      (Test: store snapshot unchanged after apply.)

**Failure modes**

- [ ] **AC-19** A fix whose target field was already hand-changed is skipped with
      `repair.fix_no_longer_applies`; other selected fixes still apply (partial success is reported
      per-fix, and the file write is all-or-nothing on the successfully-applied subset — never a
      half-written line). (Test.)
- [ ] **AC-20** Two candidate fixes for the same `ConfigPath` with no `--fix` selection →
      `repair.ambiguous_fix`, nothing applied. (Test.)
- [ ] **AC-21** `--apply` with zero appliable fixes → `repair.no_fixes_available` (exit 2); read
      mode with zero fixes → exit 0 "clean". (Test both.)

**Docs / conventions**

- [ ] **AC-22** New error codes appear in the error registry and pass the §1.1 CI code-presence
      guard. `llms.txt`/MCP tool catalog regenerated to include `repair`/`repair_apply`.
- [ ] **AC-23** Godoc on every new exported symbol; `repair` package `doc.go` states the
      file-edit-not-store-mutation invariant and the default-deny classification.

---

## 9. Failure modes (think-first, per CLAUDE.md)

1. **The file changed since the fix was computed.** Handled by the plan-hash (`repair.plan_stale`,
   AC-9) *and* a per-fix pre-application check (`repair.fix_no_longer_applies`, AC-19) — the hash
   catches whole-file drift, the per-fix check catches a targeted edit that happens to leave the
   hash's other inputs alone. Belt and suspenders because the cost of applying a stale fix (writing
   to the wrong line) is silent config corruption.
2. **The fix would touch data-path config.** Classified `data_path`, refused for auto-apply, routed
   to the human Tier-1 path (AC-14, AC-15). Default-deny on unclassifiable pointers (AC-5). This is
   the execution-plan §2 requirement, enforced, not documented-away.
3. **Multiple candidate fixes for one finding.** `repair.ambiguous_fix` — refuse and require
   explicit `--fix` selection (AC-20). Never silently pick one.
4. **A producer emits a wrong `Fix`.** The applier re-validates the result (AC-10); a fix that
   doesn't clear its finding, or introduces a new one, fails the run and the file is not left in a
   worse state. Producers are also golden-tested (AC-1). A bad producer is a bug caught in CI, not
   in a user's pipeline.
5. **Torn write / crash mid-apply.** Atomic temp+rename (AC-11); the config is one of {fully old,
   fully new}, never half.
6. **Lossy fix (truncation, row 4).** Shown in the diff before apply; never auto-applied without
   the user seeing the before/after. Classed `safe` but surfaced.
7. **Secrets in the diff.** `Before`/`After` snippets never render connector `settings` values
   (same rule the deploy `Change` follows, `plan.go:94-106`); and no `data_path` (=settings) fix is
   ever applied anyway.
8. **Agent tries to escalate.** Structurally impossible — no agent-settable escalate field (AC-15);
   the only escalate path is the human CLI.

---

## 10. Effort split & key risk — the honest part

**`repair` is ~15% command-wiring, ~85% fix-synthesis + safe YAML editing.** Breakdown:

| Workstream | Size | Notes |
| --- | --- | --- |
| CLI command + MCP two-tool shells over the engine | **S** | Copy-shape of `apply.go` + `tools_deploy.go`. The genuinely thin part the plan promised. |
| Shared `internal/repair` engine (Collect/Apply, hash, per-fix pre-check) | **M** | Reuses `validate.Run` and the deploy hash idiom; new classifier. |
| **Fix producers for the v1 starter set (§6)** | **M, and it is the dependency** | Touching ~4–8 error sites, each needing a golden test and a `validate`-passes-after assertion. Small in LoC, but it is *new error-contract surface* and the thing that must land first — the engine has nothing to apply until it exists. |
| **Comment-preserving YAML node editor** | **M–L, the sleeper cost** | A marshal-from-struct round-trip strips comments (violates §1.2's "generated configs are commented"). Needs node-level editing over the parser AST. This, not the command, is where the schedule risk actually lives. |
| Data-path classifier + default-deny table | **S–M** | Reuses `Effect` semantics; the risk is *completeness* of the deny-list — an omission is an invariant risk, so it is default-deny. |

**Top risk:** the YAML editor. If comment preservation proves expensive, the fallback is to ship
v1 applying fixes only to files it can edit losslessly and refusing (with a clear message) files
where it cannot — never a silent comment-stripping write. Stated here so it is a decision, not a
surprise.

**Second risk:** scope creep on producers. The §6 rule (deterministic value + non-data-path) is the
guardrail. Every "can't we also fix plugin names / types / IDs" request is answered by §6's
out-of-scope table: not without closest-match (doesn't exist) or a store-state check (not available
to the offline engine) — both separate tasks.

**Dependency ordering:** producers (§6) → engine (§4) → shells (§5). The command cannot be
meaningfully demoed until at least one producer lands, so build one producer + the engine + the CLI
read path as the first vertical slice, then fan out producers.

---

## 11. Alternatives considered

- **Apply fixes to the live store instead of the config file.** Rejected: that puts `repair`
  directly on the data path (store writes, running-pipeline mutation) and duplicates
  `deploy`/`apply`'s Tier-1 machinery. File-edit + existing `apply` is strictly safer and reuses
  the gate that already exists.
- **Free-form `Op` / a fourth `rename` op.** Rejected: keeps the applier's op-set at three;
  rename = add+remove compound (§3.1), avoiding a public error-shape change.
- **`ConduitError.Fix` → `[]Fix`.** Rejected for v1: it is a change to the frozen §1.1 error
  contract. Compound fixes handled by a `Code`-keyed expansion in the engine instead.
- **Auto-apply everything, no classes.** Rejected outright: violates execution-plan §2's Tier-1
  requirement and the data-integrity invariants.

## 12. Related

- `docs/design-documents/20260704-phase-1-execution-plan.md` §2, §3, §9 (the source requirement).
- `docs/design-documents/20260705-conduit-error-and-structured-output.md` (§1.1 — the `Fix` model).
- `docs/design-documents/20260708-cli-pipeline-deploy-apply.md` (diff-first + plan-hash pattern).
- `docs/design-documents/20260708-live-server-deploy-apply.md` (Tier-1 operator-authorization gate).
- `docs/design-documents/20260708-mcp-server.md` (`--allow-mutations`, read/write tool split).
- `docs/design-documents/20260707-cli-output-conventions.md` (`--json`, exit codes).
- A future ADR should record the "repair edits the config file, never the store" decision once
  built (it is an architectural boundary, per CLAUDE.md's ADR rule).
