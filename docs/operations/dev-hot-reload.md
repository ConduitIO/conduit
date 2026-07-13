# Dev-mode hot-reload: `conduit run --dev`

`conduit run --dev` runs Conduit and watches the pipelines directory, applying config
changes to running pipelines on save — so authoring a pipeline is an edit-see-edit-see loop
instead of stop / edit / restart / wait. It is the developer-facing surface over the live
in-place apply engine (see
[`docs/design-documents/20260712-pipeline-dev-hot-reload.md`](../design-documents/20260712-pipeline-dev-hot-reload.md)).
`conduit pipelines dev [dir]` is a thin alias that runs `conduit run --dev` with dev-tuned
defaults.

## What it does on each save

1. Debounces rapid saves (a save-storm coalesces into one apply).
2. Parses every pipeline in the changed file and runs the same enrich + validate the CLI uses.
3. For each pipeline, computes the diff and applies it:
   - A **processor config** or pipeline **name/description** change applies **in place** — the
     pipeline keeps running, the source never restarts, positions are continuous, no record is
     lost or reordered.
   - A **connector**, **DLQ**, or **topology** change applies via a **graceful drain-and-restart**
     (the same no-loss stop-drain-restart `apply` uses), and the output says so.
4. Ensures the pipeline is running afterward (a brand-new pipeline file, or one left stopped by a
   prior failed apply, is started — unless the config declares it `stopped`).

Each apply prints whether it was in-place or a restart, the diff, the outcome, and timing;
`--json` emits the same as structured events.

## The authorization model

`--dev` is the operator authorization. Applying any change to a running pipeline normally
requires the process-level `--api.allow-live-restart-apply` gate (see
[`live-restart-apply.md`](live-restart-apply.md)); `--dev` authorizes the applies **its own
watcher drives from local file edits**, because the operator ran `--dev`, is watching the
terminal, and sees every diff. It does **not** enable the API/MCP `apply` gate — a remote agent
still can't apply to a running pipeline over the network unless that separate flag is set.

This is the same trust boundary as `--pipelines.path` provisioning at startup: whoever can edit
the watched files and run `--dev` on this box can already restart these pipelines.

## Failure modes (by design)

- **Syntax or validation error on save** → the error is printed with its code and path; the
  running pipeline is **untouched** and keeps running the last-good config. Fix and save again.
- **A new config fails at open/start** (e.g. an unreachable source) → the pipeline is left stopped
  with the error; the next good save recovers it (ensure-running restarts it).
- **A watched file is deleted** → the pipeline is **left running** and a message is logged;
  `--dev` never deletes a running pipeline because a file vanished (e.g. a `git stash`). Stop it
  explicitly if that was the intent.
- **`Ctrl-C`** → the watcher is cancelled as part of graceful shutdown; in-flight records drain.

## Operational notes

- **Not a production deployment mechanism.** `--dev` is a foreground, single-author, local
  convenience. For unattended or remote apply, use the API/MCP `apply` path with its own operator
  gate.
- **Startup is normal.** Existing pipelines are provisioned and started exactly as `conduit run`
  does; the watcher only handles subsequent edits. An empty pipelines directory is fine — creating
  the first file live works.
- **In-place is scoped.** Only processor-config and name/description changes apply without a
  restart today; connector/DLQ/topology changes restart. See the design doc for why (the engine
  swaps a processor node live but not a source/destination's position/connection state).

## Related

- [`docs/design-documents/20260712-pipeline-dev-hot-reload.md`](../design-documents/20260712-pipeline-dev-hot-reload.md)
  — design, failure-mode analysis, and the engine PR1 built.
- [`live-restart-apply.md`](live-restart-apply.md) — the process-level gate for the API/MCP apply
  path, which `--dev` deliberately does not enable.
