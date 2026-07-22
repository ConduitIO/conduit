# `conduit generate "<natural language>"` — AI-assisted pipeline generation

## Summary

`conduit generate` turns a plain-English request into a pipeline config: it grounds a prompt in
Conduit's own schema/connector/error catalog (`llms.txt`/`llms-full.txt`), asks a pluggable LLM
provider for a candidate, gates the candidate through the **existing** `validate` engine plus a
new deterministic semantic-intent checker, and — only at an interactive terminal, only after the
human explicitly confirms — deploys it through the **existing** `deploy`/`apply` plan+hash
machinery. Nothing new is invented for schema checking or deployment; this doc adds a generation
engine, three provider adapters, a semantic-intent checker, and the CLI/MCP shells that wire them
to engines that already exist.

This is a v0.19 roadmap item (`ROADMAP.md`, Agent-native section) with a committed acceptance bar
already on record: "≥ 90% of a 25-request benchmark set produce a config that passes `validate`;
every output is `validate`-gated before display; unknown connector → closest-match + install
suggestion, never a fabricated plugin name" (`docs/design-documents/20260704-phase-1-execution-plan.md:195`).
This doc satisfies that bar and adds the harder bar the v0.19 DX review raised on top of it:
schema-valid is not the same as _correct_ — a pipeline can validate and confidently do the wrong
thing — so the committed eval set also asserts semantic intent match, not just `validate` pass.

**Status: design only. No code in this PR**, per CLAUDE.md's design-doc-before-code rule (this
touches a public contract — CLI surface, error codes, and a new provider config surface — and is
a multi-day build). Tier 2 (features/CLI) for the command itself; the provider-selection and
never-auto-apply boundary get the Tier-1 level of scrutiny in review because they are the
prompt-injection defense, even though `generate` never itself touches a running pipeline or the
data path.

## Context

### What this depends on, and its current state (verified against the tree)

- **`llms.txt` / `llms-full.txt` exist and are CI-regenerated** (`cmd/conduit/internal/llmsgen`,
  `docs/design-documents/20260712-llms-txt-generation.md`). `llms-full.txt` already carries the
  full config schema, the 6 built-in connectors with their parameters, all 46 error codes, and the
  13 MCP tool schemas — the exact grounding material `generate`'s prompt needs, already
  deterministic and never hand-maintained. `generate` is a _consumer_ of this file, not a second
  source of connector/schema truth.
- **`validate.Run` / `validate.RunWithOptions`** (`cmd/conduit/internal/validate/engine.go:46,89`)
  return a `Report` (`report.go:89`) of per-file `Finding`s with `Code`/`ConfigPath`/`Suggestion`/
  `Fix`. This is the _only_ schema gate `generate` uses — no parallel validator.
- **`deploy`/`apply`** (`cmd/conduit/internal/deploy/`, `docs/design-documents/20260708-cli-pipeline-deploy-apply.md`)
  already implement diff-first, plan-hash-bound deployment with its own Tier-1 discipline on a
  running pipeline. `generate` never reimplements this — it hands a file to it.
- **The MCP server** (`cmd/conduit/internal/mcp/`) already separates always-on read tools from
  `--allow-mutations`-gated write tools (`server.go:157-176`), with the write flag **operator-set,
  never agent-passable** (`server.go:43-50`). `generate`'s MCP tool follows this split exactly:
  read-only, full stop — though "read-only" bounds pipeline mutation, not provider spend; see
  Failure Modes, "MCP cost amplification."
- **No closest-match / "did-you-mean" utility exists anywhere in the tree** (confirmed: no
  Levenshtein or fuzzy-match code under `pkg/` or `cmd/`). The repair design doc flagged this
  exact gap for the same reason — "Connector plugin not found: the correct plugin name is not
  mechanically knowable... Deferred until a did-you-mean index exists"
  (`docs/design-documents/20260712-repair-command.md`, §6). `generate`'s AC ("unknown connector →
  closest-match, never a fabricated plugin name") makes this utility a hard prerequisite, not
  optional polish — see Decision §7.
- **An LLM provider dependency already exists in the tree, twice, for different reasons.**
  `github.com/sashabaranov/go-openai v1.41.2` is already vendored (used by the `openai` built-in
  processor, `pkg/plugin/processor/builtin/impl/openai/textgen.go`). The `ollama` built-in
  processor (`pkg/plugin/processor/builtin/impl/ollama/ollama.go`) talks to a local Ollama server
  over a **plain `net/http` client with no SDK at all** — `HTTPClient httpClient` is a two-method
  interface, hand-rolled JSON request/response. Both patterns are precedent this design reuses
  rather than introduces (Decision §1).
- **An interactive confirm already exists, and it's the anti-pattern `generate` must not copy.**
  `pipelines deploy --apply` prompts via `confirm()` (`cmd/conduit/root/pipelines/deploy.go:198-212`),
  which reads a line from `cmd.InOrStdin()` **unconditionally — no isatty gate on stdin or stdout
  at all**. That means `echo y | conduit pipelines deploy --apply pipeline.yaml` (no `--yes` flag)
  reads the piped `y` as an affirmative answer in a fully non-interactive invocation. That shape is
  tolerable for `deploy` only because `--yes` is its documented explicit-bypass path and the
  confirm is a convenience layered on top of it, not the sole safety boundary. `generate` has no
  `--yes` and its confirm _is_ the sole safety boundary (§4), so it cannot inherit this shape:
  `generate` MUST isatty-gate **both** stdin and stdout before ever attempting to read a
  confirmation response, and MUST NOT call, or structurally mirror, `deploy.confirm()`'s
  unconditional-read pattern. `connector new`'s `--yes` flag comment ("interactive confirmation is
  not implemented yet — accepted for forward compatibility", `cmd/conduit/internal/scaffoldcmd/newcmd.go`)
  is further evidence the rest of the CLI has consistently deferred a _safe_ interactive confirm —
  `generate` is the first command to actually build one, not the first to prompt at all. See
  Decision §4.
- **TTY detection already has one shared primitive**: `isatty.IsTerminal`/`IsCygwinTerminal`
  (`cmd/conduit/internal/ui/renderer.go:93`), used today for glyph-vs-ASCII selection. `generate`
  reuses it for the interactive-vs-non-interactive branch, not a second detector.
- **`conduit doctor`'s check engine** (`pkg/conduit/doctor`, `docs/design-documents/20260707-cli-doctor.md`)
  is a `Run(ctx, checks) Report` over a `Check` interface, already reused by both the CLI and (per
  its own doc) a future MCP `doctor` tool. `generate`'s provider-onboarding story is one more
  `Check` in this engine, not a bespoke preflight (Decision §2).

### Why this needs a design doc before code

It touches a public contract in three places: a new top-level CLI verb (`conduit generate`), new
stable error codes, and a new provider-config surface in `conduit.yaml`. It also introduces
Conduit's first LLM-in-the-generation-loop, first-ever interactive TTY confirmation, and first
committed model-output eval harness — each is a "≥2 days" item on its own by the CLAUDE.md
threshold.

### Why now (v0.19, not earlier)

`llms.txt` (the grounding) and the MCP server (a template for tool-shaped output) landed in
v0.17–v0.18. `generate` was correctly sequenced to need both — see the phase-1 execution plan's
own note that it "needs MCP surface + templates + llms.txt as grounding." This doc is written
ahead of the v0.19 cut so the harder review (provider trust boundary, prompt-injection defense,
eval harness design) happens before implementation starts, not mid-PR.

## Goals / Non-goals

### Goals

- Turn a free-form NL request into a **`validate`-passing** pipeline config, using the existing
  schema/connector catalog as the only source of truth for what's real (never fabricate a plugin,
  field, or connector ID).
- Collapse the "generate → validate → dry-run → deploy → apply" journey into **one interactive
  command** when a human is at a terminal, without adding a second, divergent deployment code path.
- Ship a **pluggable, vendor-neutral** provider model with a genuine zero-API-key path (local
  Ollama), and a `doctor`-style check that names exactly what to set when nothing is configured.
- Make the "why" of a generated pipeline machine-legible: every output (human or `--json`) carries
  a `rationale` and an `assumptions[]` list, not just YAML.
- Ship a committed, ≥25-request eval set that asserts **semantic intent match** (the right source,
  destination, direction, and requested transforms), not only schema validity — directly answering
  the DX review's "a pipeline can validate and confidently do the wrong thing."

### Non-goals

- **Not a chat interface.** One NL string in, one pipeline candidate out (plus retries internal to
  one invocation). No multi-turn conversation, no session state. A bad result means re-running with
  a clearer prompt, not "iterating" inside `generate`.
- **Not a replacement for `deploy`/`apply`.** `generate` produces a config file candidate; getting
  it into a running engine is still `deploy`/`apply`'s job, unchanged, including its own Tier-1 gate
  on an already-running pipeline.
- **Not a bespoke transformation DSL.** Generated processors are drawn from the existing built-in
  processor catalog (or standalone WASM processors already installed); `generate` does not invent
  processor config syntax or emit inline transformation code (CLAUDE.md: no bespoke DSL).
- **Not a training/fine-tuning effort.** Providers are used as hosted/local inference only; no
  fine-tuning, no prompt-tuning infrastructure.
- **No automatic deployment from a non-interactive context, ever, under any flag.** This is the
  hard line the whole design boundary rests on — see Decision §4.
- **No performance/cost claims in this doc.** Token cost and latency depend on provider, model, and
  grounding size — none of that is benchmarked here, and CLAUDE.md forbids asserting numbers
  without a committed benchi/reproducible result. Cost is _bounded_ (Decision §6, §8) but not
  _quantified_ until the implementation is benchmarked.

## Decision

### 1. Provider model: pluggable, vendor-neutral, minimal new dependency surface

A small interface, implemented by three adapters:

```go
// cmd/conduit/internal/generate/provider/provider.go
package provider

type Provider interface {
    // Name is the stable identifier used in config, --provider, and error
    // messages: "openai" | "anthropic" | "ollama".
    Name() string
    // Complete sends a grounded prompt and returns the raw model text.
    // Implementations do not parse the response — that is generate's job
    // (Decision §3), so a provider adapter never needs to know the pipeline
    // schema.
    Complete(ctx context.Context, req CompletionRequest) (CompletionResult, error)
}

type CompletionRequest struct {
    System string // the grounding: schema + connector catalog + few-shot examples
    Prompt string // the user's NL request, verbatim
    Model  string // provider-specific model identifier
}

type CompletionResult struct {
    Text       string
    TokensUsed int // if the provider reports it; 0 otherwise — never estimated
}
```

**New dependency: none beyond what's already vendored.** This is the load-bearing engineering
decision, and it mirrors precedent already in the tree:

| Provider | Implementation | Dependency |
| --- | --- | --- |
| `openai` | Wraps the **already-vendored** `github.com/sashabaranov/go-openai` client (same one the `openai` processor uses). | Zero new. |
| `anthropic` | A thin `net/http` JSON client against the Messages API (`POST /v1/messages`, `x-api-key` + `anthropic-version` headers) — no SDK. | Zero new. |
| `ollama` | A thin `net/http` JSON client against a local Ollama server (`/api/generate`), **the same shape as the existing `ollama` built-in processor's `httpClient` interface** — copy the pattern, don't fork a second one. | Zero new. |

Anthropic and Ollama get hand-rolled HTTP clients instead of official SDKs deliberately: both APIs
are simple JSON-over-HTTP for a single-turn completion, an SDK would be a heavy dependency for two
methods, and the tree already has a working precedent for exactly this shape (the `ollama`
processor). OpenAI reuses the existing client because it's already a dependency and already
maintained by someone else — adding a second OpenAI HTTP client would be the YAGNI violation, not
avoiding one.

**No hardcoded default vendor.** Conduit's broker-neutrality principle ("Switzerland... no broker
required at all") extends here: picking a permanent default LLM vendor is the same kind of
favoritism the roadmap forbids for brokers. Resolution is deterministic, in this order:

1. **Explicit.** `--provider` flag or `generate.provider` in `conduit.yaml`.
2. **Explicit via env**, for CI/agent use: `CONDUIT_GENERATE_PROVIDER`.
3. **Auto-detect exactly one resolvable candidate**, checked in this fixed order for
   reporting purposes only (order does not imply preference — see below):
   - `ANTHROPIC_API_KEY` set → `anthropic` is a candidate.
   - `OPENAI_API_KEY` set → `openai` is a candidate.
   - Ollama reachable at `OLLAMA_HOST` (default `http://localhost:11434`) → `ollama` is a
     candidate.
   - **Exactly one candidate** → used, no prompt, no flag needed. This is the 5-minute-wow path:
     a laptop with `ollama serve` running and nothing else configured just works.
   - **Zero candidates** → `generate.no_provider_configured` (§8), message names all three ways to
     fix it.
   - **More than one candidate** → `generate.ambiguous_provider_configuration` (§8): refuse and
     name the candidates found, rather than silently picking one. A silent pick here is exactly
     the kind of hidden-favoritism decision this design avoids — it would also make a real support
     question ("why did it use provider X") return a wrong answer every time behavior changed.

**Model selection.** Each provider adapter has a documented recommended default model, overridable
by `--model`/`generate.model`. Exact model IDs are a code-level constant, not pinned in this doc —
naming a specific model here would date the document the moment providers rev their lineup; the
adapter's own tests pin what they're tested against.

**Per-call timeout.** Each `Complete` call runs under a default per-attempt context deadline
(proposed default 30s, overridable via `--provider-timeout`/`generate.provider_timeout`), so a
hung provider connection fails deterministically to `generate.provider_error` (§8) instead of
wedging the whole invocation indefinitely. This is a per-attempt deadline, not a total-invocation
budget — each retry (§3) gets a fresh deadline, since a slow-but-eventually-successful attempt
should not be penalized for a previous attempt's slowness. The two levers that determine a single
invocation's token cost are grounding-prompt size (this section's `System` field, dominated by how
much of `llms-full.txt` is included) and retry count (§3's `--max-retries`) — named here as the
dials to tune, not quantified, per the Non-goals "no cost claims without a benchmark" rule.

### 2. Zero-config onboarding: a `doctor` check, not a bespoke preflight

Add `generate.provider` to `pkg/conduit/doctor`'s `DefaultChecks` (`docs/design-documents/20260707-cli-doctor.md`),
reusing its `Check` interface rather than writing a second preflight system:

- **pass** — exactly one provider candidate resolvable (§1's algorithm, run read-only: check env
  vars, HEAD the Ollama endpoint with a short timeout).
- **warn** — Ollama reachable but no hosted key set: "local-model path available (ollama); a
  hosted provider may produce better results for complex requests — set ANTHROPIC_API_KEY or
  OPENAI_API_KEY to add one."
- **fail** — zero candidates: "no generation provider configured. Set one of: ANTHROPIC_API_KEY,
  OPENAI_API_KEY, or run `ollama serve` locally (default <http://localhost:11434>, override with
  OLLAMA_HOST)." — names every option, per the doctor convention of a failing check always naming
  its `ConfigPath`/fix.
- **fail (ambiguous)** — more than one candidate: names which ones, and that `--provider` or
  `generate.provider` picks.

`conduit generate` runs this exact check (not a copy) as its own first step before ever calling a
provider, so the CLI error and `doctor`'s report are always consistent — one engine, two callers,
same rule the doctor doc itself establishes ("CLI is a thin renderer... future MCP doctor tool
imports 1:1").

### 3. The generation pipeline: NL → grounded prompt → candidate → gates → preview

```text
                    ┌─────────────────────────────────────────────┐
NL prompt ────────► │ 1. Assemble grounded prompt                  │
                    │    (llms-full.txt schema + connector catalog │
                    │     + few-shot template examples)            │
                    └───────────────────┬───────────────────────────┘
                                        ▼
                    ┌─────────────────────────────────────────────┐
                    │ 2. provider.Complete → raw candidate text     │
                    └───────────────────┬───────────────────────────┘
                                        ▼
                    ┌─────────────────────────────────────────────┐
                    │ 3. Parse candidate as a pipeline config       │
                    │    (same YAML parser validate/deploy use)     │
                    └───────────────────┬───────────────────────────┘
                        parse failed ◄──┤── parse ok
                     (retry, §5)        ▼
                    ┌─────────────────────────────────────────────┐
                    │ 4. validate.Run (the SAME engine `pipelines   │
                    │    validate` uses — no second schema checker) │
                    └───────────────────┬───────────────────────────┘
                     invalid ◄──────────┤── valid
                     (retry, §5)        ▼
                    ┌─────────────────────────────────────────────┐
                    │ 5. Semantic-intent check (new, deterministic, │
                    │    heuristic — §4)                            │
                    └───────────────────┬───────────────────────────┘
                     mismatch ◄─────────┤── match
                     (retry, §5)        ▼
                    ┌─────────────────────────────────────────────┐
                    │ 6. Preview: rationale + assumptions + diff +  │
                    │    inline dry-run (validate.RunWithOptions    │
                    │    Enriched, the SAME engine `dry-run` uses)  │
                    └───────────────────┬───────────────────────────┘
                                        ▼
                     TTY? ──── no ────► emit only, never deploy (§4)
                       │
                      yes
                       ▼
                 human confirms? ── no ──► emit only, never deploy
                       │
                      yes
                       ▼
                 deploy.Plan → apply.Apply (the SAME engine
                 `pipelines deploy`/`apply` use — no divergent path)
```

Steps 3–5 form a **bounded retry loop** (default `--max-retries 3`, configurable): a failure at any
gate feeds the specific failure (parse error, the `validate.Report`'s findings, or the semantic
mismatch detail) back into the next `Complete` call as corrective context — "you referenced
connector `postgre`, which does not exist; valid connectors are: file, generator, kafka, log,
postgres, s3" is exactly the kind of feedback that both fixes hallucinated names and is the
consumer of the closest-match utility (§7). Exhausting retries surfaces a terminal error naming the
**last** attempt's specific failure — never a generic "generation failed" with no detail (§8).

**Nothing between steps 1–5 touches disk or the pipeline store.** The candidate lives in memory
until step 6's preview; if the user declines to write it (`--out` not given and TTY confirm says
no), nothing is left behind. This mirrors `repair`'s "the mutation is a text edit, reviewable as a
diff" safety property — here the "mutation" is a brand-new file, which is strictly less risky than
`repair`'s in-place edit, but the write-only-after-review discipline is identical.

### 4. The never-auto-apply boundary — the security-critical decision

**There is no flag, anywhere, that causes `generate` to deploy without an interactive human
confirmation at both a TTY stdout and a TTY stdin.** Concretely:

- **TTY path (`isatty.IsTerminal` true on both stdout and stdin):** step 6's preview renders, then
  a plain `Deploy this pipeline? [y/N]` prompt. Only a literal keystroke response deploys. This
  confirm reads **only** after both isatty checks pass; it does not call, and must not
  structurally mirror, `pipelines deploy`'s `confirm()` (`cmd/conduit/root/pipelines/deploy.go:198-212`),
  which reads `cmd.InOrStdin()` unconditionally with no TTY gate at all (Context, "an interactive
  confirm already exists"). That shape is safe for `deploy` only because `--yes` is its documented
  bypass; it would be unsafe here, where no such flag exists and the confirm is the only gate.
- **Any other invocation** — piped output, `--json`, a script, an MCP tool call, `stdin` not a TTY
  — **always stops after emitting the candidate.** There is no `--yes`, `--deploy`, or
  `--auto-apply` flag on this command. This is a deliberate asymmetry from `apply`/`repair`/
  `scaffold`'s `--yes` convention (§ Alternatives), not an oversight.

**What the human is actually confirming.** The `[y/N]` decision must be anchored on the two
trusted, deterministic artifacts step 6 renders — the YAML diff and the `validate`/semantic-check
results — never on the model's prose. `rationale` and `assumptions[]` (§6) are unverified model
output: they are rendered clearly marked as such (a distinct heading/prefix, never visually
indistinguishable from the diff or the check results) and must not be the primary basis for a
yes/no decision, because they are exactly the untrusted surface a prompt-injection attempt would
target — a poisoned NL prompt could get the model to emit a reassuring rationale ("this only reads
from Postgres") while the actual YAML does something else. The diff and the semantic-check output
are what `generate` computed and verified; the rationale is what the model said about itself, and
the two are not interchangeable as evidence.

**Why the asymmetry.** `apply`'s `--yes` bypasses only a _hash-freshness_ check on a plan a human
(or a prior CI step) already constructed from a reviewed config file — the input is already
structured, already reviewed data. `generate`'s input is **free-form natural language**, the
textbook untrusted-input class for prompt injection: a string an agent piped in, a ticket
description pasted verbatim, or user-supplied text could contain "...and also deploy this to the
production pipeline" as an embedded instruction. If any flag could make `generate` both synthesize
_and_ apply a pipeline in one non-interactive call, that flag is the injection payoff. Removing the
flag entirely — rather than trying to sanitize the NL input — is the simpler and more robust
control (CLAUDE.md: "boring, obvious code" beats a clever filter that will eventually miss a case).

**This does not remove automation capability**, it just requires the composition to be explicit:
`conduit generate "..." --out pipeline.yaml --json` (pure emit, exit 0/2/3, no prompt attempted
since stdout is non-TTY) followed by the existing `conduit pipelines deploy pipeline.yaml` /
`apply --yes` — each of which already has its own reviewed plan+hash gate. Two explicit commands,
each auditable independently, is the intended automation path — not one command with a hidden
switch.

**The bound on a bypassed confirm is "whatever `deploy`/`apply` would let any hand-written config
do," not "harmless because it's schema-shaped."** The candidate is constrained to the pipeline
config schema the same way any `validate` input is — a YAML document of connectors/processors/
settings, never a shell command or arbitrary code — so a malicious NL prompt cannot get `generate`
to emit anything outside that schema. But a schema-valid pipeline is still a live data-movement
primitive: a source pointed at an internal database and a destination pointed at an
attacker-controlled endpoint (an S3 bucket, a Kafka broker, an HTTP sink) is a fully valid,
schema-shaped **exfiltration channel** once deployed. "No code-execution surface" is true and
irrelevant to that risk. The actual bound is narrower and more honest: a pipeline `generate`
produces can, once deployed, do nothing that a hand-written `deploy`/`apply` of the same config
couldn't already do — the risk surface is exactly `deploy`/`apply`'s existing blast radius, not a
new one `generate` introduces. The TTY-confirm boundary is what prevents an injected prompt from
reaching that blast radius without a human reviewing the diff first; it is defense against
exfiltration and destructive configs specifically, not merely against code execution.

### 5. CLI surface

```text
conduit generate "<natural language>" [--provider p] [--model m] [--out FILE]
    [--max-retries N] [--json] [--no-color]
```

No `--yes`, `--deploy`, `--apply` flags exist (§4). `--out` defaults to a name derived from the
inferred pipeline name — the model-controlled name is **sanitized to a bare basename before use**:
path separators (`/`, `\`) and any `..` segment are stripped/rejected, producing a plain filename
written relative to the current working directory (deduplicated, never silently overwriting an
existing file — `--force` required, matching the scaffolding convention). This sanitization is
independent of the TTY/confirm boundary (§4) — writing the candidate file to disk is a side effect
that needs no TTY or human confirmation at all, so it needs its own hard rule: a model-influenced
string must never be interpretable as a path that escapes the working directory. An explicit
user-supplied `--out PATH` is taken as given (the user, not the model, chose it) and is not subject
to the basename restriction, but is still resolved relative to the invocation's working directory
like every other CLI file argument.

Human TTY transcript (abridged):

```text
$ conduit generate "stream new orders from postgres into a kafka topic, only orders over $100"
Grounding prompt with 6 built-in connectors, config schema v2.2...
Generated candidate (attempt 1/3)... validate: 0 errors, 0 warnings
Semantic check: source=postgres ✓ destination=kafka ✓ filter-processor present ✓

Rationale:
  Reads via Postgres logical replication (CDC) after an initial snapshot, filters
  records where amount > 100 using a builtin field-filter processor, and writes
  JSON-encoded records to a Kafka topic named "orders".

Assumptions:
  - "new orders" → snapshot + ongoing CDC, not CDC-only (not specified)
  - amount field assumed named "amount" (not specified — verify against your schema)
  - Kafka topic name derived from source table name ("orders")
  - encoding: JSON (not specified)

--- pipeline.yaml (new file) ---
version: "2.2"
pipelines:
  - id: orders-to-kafka
    connectors:
      - id: pg-source
        type: source
        plugin: builtin:postgres
        ...
+++ (32 lines) +++

Dry-run: 1 pipeline, 2 connectors, 1 processor, worker count 1 (default), no DLQ configured.

Deploy this pipeline? [y/N] n
Wrote ./pipeline.yaml (not deployed).

Next steps:
  conduit pipelines validate ./pipeline.yaml
  conduit pipelines dry-run ./pipeline.yaml
  conduit pipelines repair ./pipeline.yaml    # if validate finds anything to fix
  conduit pipelines deploy ./pipeline.yaml
```

The footer always lists `validate`/`dry-run`/`repair`/`deploy` in that order — `repair` included
per the shipped `conduit pipelines repair` (`docs/design-documents/20260712-repair-command.md`),
so a generated file with a fixable finding routes to the same repair path a hand-written one would.

### 6. `--json` envelope: `rationale` and `assumptions[]` are first-class, not comments

Follows the shared envelope (`docs/design-documents/20260707-cli-output-conventions.md`) exactly:

```json
{
  "command": "generate",
  "ok": true,
  "summary": { "attempts": 1, "maxRetries": 3, "connectors": 2, "processors": 1 },
  "result": {
    "provider": "anthropic",
    "model": "…",
    "path": "pipeline.yaml",
    "pipeline": "version: \"2.2\"\npipelines:\n  - id: orders-to-kafka\n    ...",
    "rationale": "Reads via Postgres logical replication (CDC) after an initial snapshot, filters records where amount > 100 using a builtin field-filter processor, and writes JSON-encoded records to a Kafka topic named \"orders\".",
    "assumptions": [
      "\"new orders\" -> snapshot + ongoing CDC, not CDC-only (not specified)",
      "amount field assumed named \"amount\" (not specified — verify against your schema)",
      "Kafka topic name derived from source table name (\"orders\")",
      "encoding: JSON (not specified)"
    ],
    "validate": { "files": [ { "path": "pipeline.yaml", "ok": true, "findings": [] } ] },
    "semanticChecks": [
      { "check": "source_connector_match", "ok": true, "detail": "postgres" },
      { "check": "destination_connector_match", "ok": true, "detail": "kafka" },
      { "check": "filter_intent_covered", "ok": true, "detail": "amount > 100 -> builtin:field-filter" }
    ],
    "deployed": false,
    "nextSteps": [
      "conduit pipelines validate pipeline.yaml",
      "conduit pipelines dry-run pipeline.yaml",
      "conduit pipelines repair pipeline.yaml",
      "conduit pipelines deploy pipeline.yaml"
    ]
  },
  "error": null
}
```

`rationale`/`assumptions[]` are populated by asking the provider to emit them alongside the YAML in
one structured response (not a second call) — the same request produces both, so there's no extra
round trip and no risk of the explanation drifting from the actual config. An agent (or the future
TTY preview renderer, and later the UI per `docs/design-documents/20260713-greenfield-built-in-ui.md`'s
non-goal boundary — read-only rendering, not an authoring surface) reads `rationale`/`assumptions`
directly; it never has to parse YAML comments to explain the pipeline, which was the DX review's
explicit requirement.

`deployed` is always `false` in `--json` output — stated explicitly in the schema (not merely
omitted) so a consumer never has to wonder whether a JSON emission silently deployed something.

### 7. New shared utility: closest-match (`did-you-mean`) — a genuine prerequisite

The AC "unknown connector → closest-match + install suggestion, never a fabricated plugin name"
cannot be met with what exists today (§ Context — confirmed, no such utility anywhere in the tree).
This is new, small, shared infrastructure, not a `generate`-only helper:

```go
// pkg/foundation/fuzzymatch/fuzzymatch.go (new, tiny, no dependency —
// Levenshtein distance over ASCII plugin names is a ~30-line algorithm)
package fuzzymatch

// Suggest returns the closest name(s) in candidates to want, by
// case-insensitive Levenshtein edit distance, capped to maxSuggestions, and
// only above a similarity floor: edit distance <= 2, OR edit distance <=
// 30% of len(want), whichever is looser (never suggests an unrelated name
// just because the list is short — a flat 2-edit tolerance on a 4-char
// name would reject nearly everything, while a flat 30% on a 20-char name
// would accept too much; the max of an absolute and a relative bound
// covers both ends).
func Suggest(want string, candidates []string, maxSuggestions int) []string
```

**Plain Levenshtein, not Damerau-Levenshtein, is the v1 choice.** Connector name typos in practice
are substitutions/omissions/insertions (`postgre` vs `postgres`, `kafak` vs `kafka`), not
transpositions where Damerau's extra adjacent-swap operation would matter; plain Levenshtein is
simpler to implement and audit, and nothing in the acceptance bar requires the transposition case.
Matching is **case-insensitive** (`Postgres`/`postgres`/`POSTGRES` all compare equal) since
connector names in NL prompts have no reliable casing convention.

Consumed by:

- `generate`'s retry-feedback loop (§3): if a candidate references a plugin not in the catalog,
  the next retry prompt says "connector `postgre` does not exist; did you mean `postgres`? valid
  connectors: …" — turning a hallucination into a self-correction within the retry budget instead
  of a terminal failure.
- If retries exhaust with an unknown-connector reference still present, it surfaces as a
  `validate_failed` (§8) result whose finding's `Suggestion` includes the closest match — the
  **same finding shape `validate`/`repair` already render**, just with a non-empty `Suggestion` for
  the first time on this class of error.
- **This closes the exact gap the repair design doc flagged as its own out-of-scope reason**
  ("Connector plugin not found... no closest-match logic in the tree... Deferred until a
  did-you-mean index exists" — `20260712-repair-command.md`, §6). Once this lands, `repair`'s v2
  scope should revisit that row — noted here so it isn't lost, not undertaken in this PR.

### 8. Error taxonomy

New codes, registered the existing way (`conduiterr.Register`, one `codes.go` in the new
`generate` package), each mapped through the existing exit-code classifier
(`pkg/conduit/exitcode`) with no new bucket:

| Code (reason) | gRPC category | Exit | Raised when |
| --- | --- | --- | --- |
| `generate.no_provider_configured` | `Unauthenticated` | 3 (env) | Zero resolvable providers (§1). Message names all three fixes. |
| `generate.ambiguous_provider_configuration` | `InvalidArgument` | 2 (validation) | More than one provider candidate resolvable, no explicit `--provider`/config selection. |
| `generate.ambiguous_prompt` | `InvalidArgument` | 2 | Raised in two places, deliberately conservative pre-call (§9): (a) **pre-call**, only when the prompt is clearly empty or self-contradictory — a legitimate-but-terse prompt is sent to the provider, not refused on a heuristic's inability to parse it; (b) **post-hoc**, when a candidate's source/destination still can't be matched against anything extractable from the prompt after retries — the same point a `semantic_mismatch` would fire. |
| `generate.provider_error` | `Unavailable` | 3 (env) | The provider call failed: network, timeout, rate limit, or an auth rejection from the provider itself. Message includes the provider name and the underlying status, never a raw stack trace. |
| `generate.parse_failed` | `Internal` | 1 (runtime) | The provider replied, but the response could not be parsed into a pipeline config after retries — an unclassified failure of the boundary, not a bad user request. |
| `generate.validate_failed` | `FailedPrecondition` | 2 | The parsed candidate never passed `validate` within `--max-retries` attempts. The **last** attempt's `validate.Report` is attached in full (never discarded). |
| `generate.semantic_mismatch` | `FailedPrecondition` | 2 | The candidate passed `validate` but failed the semantic-intent checker (§4 of the generation pipeline, e.g. wrong direction, a clearly-requested connector/filter missing) within the retry budget. |

Exit codes fall out of the existing classifier (`fromGRPCCode`, `pkg/conduit/exitcode/exitcode.go`)
verbatim — no new bucket, no new mapping logic, matching CLAUDE.md's "errors are API" and the
existing convention that every code carries a stable reason, a gRPC category, and (via
`Suggestion`) an actionable fix.

### 9. Semantic-intent checking — deterministic heuristics, not an LLM judge

A small, auditable checker, run after `validate` passes:

- **Extraction from the NL prompt**: a synonym table maps mentioned nouns to connector categories
  (`postgres`/`postgresql`/`pg` → `postgres`; `kafka` → `kafka`; `s3`/`bucket` → `s3`; etc., sourced
  from the same connector catalog `llms-full.txt` already lists — never a second hand-maintained
  list) and simple directional phrasing (`"from X ... into/to Y"`, `"X source"`, `"Y destination"`)
  to an expected `{source, destination}` pair. Filter/transform intent (`"only orders over $100"`)
  extracts as a small set of required-capability tags (`filter`, `field-comparison`), not exact
  processor names.
- **Assertion against the candidate**: does the candidate's actual source/destination plugin match
  the extracted category (when the prompt was unambiguous about it)? Is a processor present that
  covers each required-capability tag? Is the direction not swapped (source category found on the
  destination connector or vice versa)?
- **Pre-call refusal is deliberately conservative.** Extraction only refuses up front
  (`generate.ambiguous_prompt`, §8, before ever calling a provider — no wasted call) when the
  prompt is **clearly empty or self-contradictory** — blank/whitespace-only input, or a prompt that
  names mutually exclusive directions for the same connector (e.g. "from kafka to kafka as the
  destination, source is postgres"). A prompt that is merely under-specified or terse (no explicit
  destination named, informal phrasing) is **not** refused pre-call — it is sent to the provider,
  and the resulting candidate is judged the normal way: `validate` (§3 step 4), then this semantic
  check. If the candidate's source/destination still can't be matched against anything extractable
  from the prompt, `generate.ambiguous_prompt` is raised **post-hoc**, at the same point a
  `semantic_mismatch` would be, with the same retry-then-surface behavior — it is a post-hoc code
  as much as a pre-filter, not only the latter. This bias (let it through, judge the result) avoids
  rejecting a legitimate-but-terse request before the model — materially more capable at reading
  intent than a synonym table — ever gets a chance at it.

**Why heuristic, not an LLM-as-judge** (a second model call scoring the first model's output): a
nondeterministic grader over a nondeterministic generator compounds flakiness in exactly the
artifact (the committed eval set, §10) that has to gate a release train reliably. A keyword/category
match is deterministic, cheap, fully auditable in a code review, and directly targets the DX
review's named failure mode ("validates and confidently does the wrong thing" — wrong connector,
wrong direction, missing an explicitly-requested filter). It will miss subtler mismatches; that's
an accepted v1 scope limit, not a hidden gap — see Alternatives.

### 10. Eval / benchmark harness: the committed ≥25-request semantic-intent set

- **Location**: `cmd/conduit/internal/generate/testdata/eval_requests.yaml` — committed fixtures,
  not generated, so the bar being measured is stable and reviewable in a diff like any other test
  data.
- **Shape**, one entry per request:

  ```yaml
  - id: postgres-cdc-to-kafka-filtered
    prompt: "stream new orders from postgres into a kafka topic, only orders over $100"
    expect:
      sourceCategory: postgres
      destinationCategory: kafka
      requiredCapabilities: [filter]
    notes: "direction + filter intent; the DX review's canonical failure case"
  ```

- **Two run modes, matching the fuzzing convention's "CI (short) + scheduled (long)" split**
  (CLAUDE.md testing standards):
  - **Every PR (fast, deterministic):** replay recorded provider transcripts for each of the 25+
    fixtures (captured once per provider/model, committed alongside the fixture) through the
    parse → validate → semantic-check pipeline. This catches regressions in _Conduit's_ code
    (prompt assembly, parsing, the semantic checker itself) without a live API call, keeping it
    fast and flake-free. **This is a Conduit-code regression gate, not a provider-quality
    signal** — the recorded transcripts are frozen at capture time, so if a provider's model
    quality regresses after capture, the old (still-passing) transcript replays unchanged and PR CI
    stays green; only the scheduled live-provider run below would surface an actual provider
    regression. A green PR CI run means "`generate`'s own code still processes a known-good
    transcript correctly," not "the model still generates correct pipelines."
  - **Scheduled (nightly/weekly, live providers, each configured provider):** run the full pipeline
    against the real provider APIs, refresh the recorded transcripts if they still meet the bar,
    and report the two headline metrics below. Gated the same way the chaos suite and 24h soak are
    (Process maturity table) — a real signal, but not yet a per-PR blocking gate until it's proven
    stable; it does gate the v0.19 release train, same spirit as the committed `≥90% validate-pass`
    AC already on record.
- **Two metrics, tracked and reported separately** (never collapsed into one number, since they
  measure different things):
  1. **`validate`-pass rate** — the AC already committed in the execution plan: **≥ 90%** of the
     25+ requests must produce a candidate (within the retry budget) that passes `validate`.
  2. **Semantic-intent-match rate** — new bar this doc adds: the generated pipeline's
     source/destination/capabilities match `expect`. Proposed v1 floor: **≥ 70%**, deliberately set
     below the validate-pass bar because it is measuring a strictly harder property (schema-valid
     is necessary but not sufficient for "did the right thing") — ratchet upward as the fixture set
     and heuristics mature. **This 70% answers only the coarse mismatch class the checker is built
     to catch — wrong connector, wrong direction, a missing requested capability — not fine
     predicate correctness** (the right connector and direction but the wrong filter condition, the
     wrong table/topic name, an off-by-one on a threshold). Do not read "≥70% semantic-intent-match"
     as "≥70% of generated pipelines are correct" — it means ≥70% clear the coarse bar this
     deterministic heuristic checks; a pipeline can pass this metric and still get a fine-grained
     predicate wrong (§9 names this as an accepted v1 scope limit, not a hidden gap). This is a
     scope decision recorded here, not a benchmarked performance claim.
- **A regression on either metric versus the last scheduled run is a release blocker for the
  cutting train**, following the same discipline benchi regressions get (>10% throughput regression
  fails the build unless waived with justification) — here, waiving requires the same explicit,
  documented justification in the release PR, not a silent skip.
- **The fixture set itself is the spec of "what generate is for."** Growing it (more connector
  combinations, ambiguous prompts that should refuse, adversarial prompts embedding injected
  instructions) is high-leverage the same way expanding the SDK acceptance suite is "high-leverage"
  per CLAUDE.md — it is the compatibility contract for this feature.

## Alternatives considered

- **`--yes`/`--deploy` bypass flag, matching `apply`/`repair`/`scaffold`'s convention.** Rejected
  (§4): those commands' `--yes` bypasses a freshness check on already-reviewed structured input;
  `generate`'s input is free-form NL, the textbook prompt-injection target. A bypass flag here is
  the injection payoff, not a convenience. The two-explicit-command composition preserves
  automation without the shortcut.
- **A single hardcoded default provider** (always OpenAI, or always Anthropic). Rejected: conflicts
  with the roadmap's broker-neutrality principle applied to LLM vendors, creates a single point of
  failure for the 5-minute-wow if that vendor has an outage or the user has no key for it, and
  quietly advantages one vendor with no technical justification. The zero-key Ollama path plus
  ambiguity-refusal (rather than a silent pick) keeps the decision explicit and reviewable.
- **LLM-as-judge for semantic-intent checking** (a second model call scores the first's output
  against the request). Rejected for v1 (§9): doubles cost and latency per request, and makes the
  committed eval harness's pass/fail nondeterministic on top of an already-nondeterministic
  generator — exactly the flakiness a release-gating benchmark can't afford. Deterministic
  keyword/category heuristics are strictly less capable but fully auditable; revisit if the
  heuristic's false-negative rate on real requests proves too high once usage data exists.
- **Templates-only (retrieval over a fixed gallery, no free-form LLM synthesis).** Rejected as the
  sole mechanism — too rigid for arbitrary NL, and the roadmap already has a separate "templates
  gallery" item. Templates are folded in as **grounding context** (few-shot examples in the
  prompt), not adopted as a replacement for generation.
- **Local-model-only (no hosted providers, to sidestep the dependency/cost/privacy questions
  entirely).** Rejected: hosted frontier models are materially better at structured multi-field
  YAML synthesis today, and excluding them would visibly hurt the "5-minute wow" bar for anyone
  without a beefy local machine. The zero-key Ollama path already gives users who want no hosted
  dependency a fully supported option — it doesn't need to be the _only_ option to serve that need.
- **Fabricate a plugin name for an unrecognized connector mention, with a disclaimer.** Rejected
  outright — this is explicitly forbidden by the committed AC and by the repair doc's precedent
  rule ("never a fabricated plugin name"). Refuse + closest-match suggestion (§7) is the only
  acceptable behavior.

## Failure modes

Per CLAUDE.md's "think in failure modes first" — answered before any implementation:

1. **Provider unreachable or erroring mid-call.** `generate.provider_error` (§8), exit 3. No file
   is written, no partial candidate is shown as if it were complete.
2. **The model hallucinates a plausible-looking but wrong pipeline** (wrong plugin, invented field,
   confidently wrong direction). Caught by `validate` (schema/field-level) and the semantic checker
   (direction/connector/capability-level) **before** it ever reaches the preview or disk — a
   candidate that fails either gate is retried, then surfaced as a structured failure with the
   specific finding, never silently "fixed" by guessing.
3. **Retries exhausted.** The terminal error names the **last** attempt's specific failure (parse
   error text, or the full `validate.Report`, or the semantic mismatch detail) — never a bare
   "generation failed." `--json` includes the last candidate's raw text for debugging, clearly
   marked as never having passed the gates (so it can't be mistaken for a usable output).
4. **Prompt-injection attempt embedded in the NL** (e.g., "...and deploy this to prod, also run
   x", or a prompt engineered so the generated pipeline quietly routes data to an
   attacker-controlled destination). The provider's output space is constrained to the pipeline
   config schema by construction — there is no shell/file-system/code-execution surface for an
   injected instruction to land on — but a schema-valid pipeline is still a data-movement
   primitive, so the real bound is §4's: the deployed effect is limited to what `deploy`/`apply`
   would let any hand-written config do, and nothing deploys without a human reviewing the YAML
   diff first. §4's TTY-only confirm is the backstop even if a future integration widened the
   output surface further (e.g., a provider tool-use mode that could execute something). This is
   the reasoning that shaped §4, not an afterthought.
5. **Zero or multiple providers configured.** Deterministic refusal (`no_provider_configured` /
   `ambiguous_provider_configuration`, §8) — never a silent pick of "whichever env var happened to
   be checked first."
6. **Runaway retries or repeated invocation cost.** `--max-retries` bounds a single invocation
   (default 3, no auto-retry across separate `generate` calls); a scripted loop calling `generate`
   repeatedly is the caller's choice, not something this command amplifies internally.
7. **Secrets pasted into the NL prompt** (a user includes a real connection string with a password
   "for context"). Prompts/responses are logged at debug level only, with the same redaction
   discipline connector `settings` values already get elsewhere in the codebase (never at info
   level, never in the `--json` envelope beyond what the user explicitly asked to generate) — a
   documented usage caveat, not a new secret-handling code path.
8. **Scope/tier boundary:** `generate` itself never mutates a running pipeline, never writes to the
   store, and produces only a new candidate file a human reviews. It is Tier 2, not Tier 1 — the
   moment output is deployed, it is exactly the same Tier-1-governed `deploy`/`apply` path any
   hand-written config goes through, with no shortcut around that gate.
9. **MCP cost amplification.** The MCP `generate` tool is registered read-only (no pipeline
   mutation — Context, "the MCP server") — but "read-only" describes its effect on a running
   pipeline, not its cost. Every invocation issues at least one outbound LLM call on the operator's
   configured provider key, and any connected agent can call the tool repeatedly with
   agent-controlled prompts. `--max-retries` bounds the number of provider calls **within one
   invocation**; it does nothing to bound the number of invocations an agent session makes. An
   agent in a loop (deliberately, or by a bug in its own logic) can run up real provider cost with
   no code-execution or data-path risk at all. Mitigation lever: a per-session (or per-time-window)
   call budget on the MCP tool specifically — tracked separately from `--max-retries` — is what to
   add if usage data shows this happening; until implemented, this is a **documented reliance on
   the provider's own rate-limiting/spend-cap controls** (e.g. an Anthropic or OpenAI usage cap
   configured out-of-band), not a Conduit-side control. Operators wiring `generate` into an MCP
   server whose callers they don't fully trust should set a provider-side spend cap.

## Upgrade / rollback

Purely additive — no serialized-format, protocol, or config-schema change to anything that exists
today.

- **New CLI command, new package, new error codes.** Removing/disabling `generate` removes only
  this surface; it writes no state anywhere else and has no on-disk format of its own (the output
  is an ordinary pipeline config file, already versioned and migrated by the existing
  `provisioning/config` machinery).
- **New `conduit.yaml` keys** (`generate.provider`, `generate.model`, `generate.max_retries`) are
  additive with documented defaults; an older binary simply ignores them, a newer one without them
  falls back to auto-detection (§1).
- **Provider adapters are isolated.** A provider's API revving (e.g., Anthropic bumping its message
  API version) is contained to that one adapter file — never a protocol or public-contract change
  for Conduit itself.
- **The eval fixture set can grow without migration** — it's test data, not a wire format.
- **No rollback hazard**: the never-auto-apply boundary (§4) means this feature cannot itself have
  put a running pipeline into a bad state; the worst outcome of a `generate` bug is a bad candidate
  file a human was going to review anyway before either `deploy` or the (unaffected) TTY confirm
  would act on it.
- A future ADR should record the never-auto-apply boundary (§4) once implemented — it's an
  architectural safety decision on the level of `repair`'s file-not-store ADR
  (`20260713-repair-edits-file-not-store.md`), and future contributors should be able to
  reconstruct _why_ there's no `--yes` here without archaeology. That ADR must explicitly capture:
  (a) why the confirm gates on isatty for **both** stdin and stdout before ever reading a response,
  why `generate` has no `--yes`/`--deploy`/`--auto-apply` flag at all, and why `generate` does not
  reuse `deploy.confirm()`'s unconditional-read shape (the injection-payoff reasoning in §4 and
  Context, "an interactive confirm already exists"); and (b) why the default `--out` filename is
  sanitized to a bare basename within the working directory rather than trusting the
  model-inferred name directly (§5). Both are load-bearing safety decisions this design doc
  motivates but which a future contributor should not have to reconstruct from a diff.

## Testing

- **Provider adapters**: unit tests per adapter against a mocked HTTP client (mirrors the existing
  `ollama` processor's `mock/http_client_mock.go` pattern) — request shape, auth header, error
  mapping to `generate.provider_error` for 4xx/5xx/timeout.
- **Provider resolution**: table test over env var combinations and Ollama-reachable/unreachable,
  asserting the exact deterministic outcome (§1) for zero/one/many candidates, including the
  `doctor` check (§2) producing the identical verdict the CLI preflight does.
- **Prompt assembly**: a golden test asserting the grounding text is built from the current
  `llms-full.txt`/connector catalog (not a stale hand-copied snippet) — regenerating `llms.txt`
  and not updating the prompt template would fail this test, keeping the two in sync structurally.
- **Retry loop**: a fake provider returning invalid/mismatched candidates on purpose asserts the
  loop retries exactly `--max-retries` times, feeds back the specific failure each time, and
  terminates with the _last_ attempt's detail attached (never silently discarded, never an infinite
  loop).
- **Closest-match utility**: table test over known-connector-name typos, asserting a suggestion is
  offered above the similarity floor and withheld below it (never suggesting an unrelated name).
- **The never-auto-apply boundary (the load-bearing security test)**: an explicit test asserts that
  with `--json` set, or with stdout/stdin not a TTY, `generate` **never** calls
  `deploy.Plan`/`apply.Apply` — verified by asserting no such call occurs even when a test harness
  simulates a "yes" being available on stdin (proving there is no code path that would read it in
  a non-interactive context, not just that no flag currently triggers it). This guards specifically
  against reintroducing `deploy.confirm()`'s unconditional-read shape
  (`cmd/conduit/root/pipelines/deploy.go:198-212`) inside `generate`'s own confirm path — the
  hazard named in Context, "an interactive confirm already exists."
- **`--out` filename sanitization**: table test over model-inferred names containing path
  separators and `..` segments (e.g. `../../etc/pipeline`, `foo/bar`), asserting the resolved
  output path is always a bare basename under the working directory, and that an explicit
  user-supplied `--out PATH` is passed through unsanitized (§5).
- **MCP tool**: asserts `generate` registers as a **read-only** tool (parity with `deploy`'s
  always-on / non-mutating registration) and that its implementation contains no call to
  `deploy`/`apply` internals at all — an agent that wants to deploy a generated pipeline must
  invoke the existing `deploy`/`apply` MCP tools separately, each with their own gate.
- **The eval harness** (§10): the replay-mode run is part of the standard CI suite (fast,
  deterministic); the live-provider scheduled run is a separate job per the Process maturity
  table's "scheduled (long)" pattern, reporting both the validate-pass and semantic-intent-match
  rates as tracked metrics, gating the v0.19 release cut.
- **`--json` envelope**: schema test asserting `rationale`, `assumptions[]`, and `deployed: false`
  are always present (never omitted) on every success/failure shape, per the shared envelope
  convention (`docs/design-documents/20260707-cli-output-conventions.md`).

## Related

- `docs/design-documents/20260704-phase-1-execution-plan.md` (the committed `conduit generate` AC
  this doc satisfies, plus the "needs MCP surface + templates + llms.txt" sequencing).
- `docs/design-documents/20260712-llms-txt-generation.md` (the grounding source; explicitly names
  `conduit generate` as a consumer).
- `docs/design-documents/20260705-conduit-error-and-structured-output.md` (the `ConduitError`/`Fix`
  model this doc's error taxonomy and semantic-check findings reuse verbatim).
- `docs/design-documents/20260707-cli-output-conventions.md` (the `--json` envelope, exit-code
  routing, and flag vocabulary this doc follows).
- `docs/design-documents/20260707-cli-doctor.md` (the check engine `generate.provider` extends).
- `docs/design-documents/20260708-cli-pipeline-deploy-apply.md` and
  `20260708-live-server-deploy-apply.md` (the deploy/apply engine `generate` hands a candidate to,
  unchanged).
- `docs/design-documents/20260708-mcp-server.md` (the read/write tool split `generate`'s MCP tool
  follows; the "same engine, no divergent logic" rule).
- `docs/design-documents/20260712-repair-command.md` (the closest-match gap this doc's §7 utility
  closes; the diff-first/next-steps-footer conventions this doc reuses).
- `docs/design-documents/20260706-quickstart-command.md` (the "5-minute wow" framing the zero-key
  Ollama path serves).
- `docs/design-documents/20260707-connector-processor-scaffolding.md` (the "next steps" footer
  convention and toolchain-preflight precedent this doc's provider preflight follows).
- `docs/design-documents/20260713-greenfield-built-in-ui.md` (states the UI is observe/operate,
  not authoring — a future UI rendering of `generate`'s output stays within that non-goal: it can
  display `rationale`/`assumptions`/preview, but does not itself become a second authoring path).
- ROADMAP.md, Agent-native section (`conduit generate "<natural language>"`) and Phase-1
  execution plan §2/§9 (agent-native, errors-teach-and-are-actionable through-line).
