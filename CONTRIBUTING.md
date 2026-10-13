# Contributing to Conduit

Thank you so much for contributing to Conduit. We appreciate your time and help!
As a contributor, here are the guidelines we would like you to follow.

## Asking questions

If you have a question or you are not sure how to do something, please
[open a discussion](https://github.com/ConduitIO/conduit/discussions).

## Filing a bug or feature

1. Before filing an issue, please check the existing
   [issues](https://github.com/ConduitIO/conduit/issues) to see if a
   similar one was already opened. If there is one already opened, feel free
   to comment on it.
2. Otherwise, please [open an issue](https://github.com/ConduitIO/conduit/issues/new)
   and let us know, and make sure to include the following:
   - If it's a bug, please include:
     - Steps to reproduce
     - Copy of the logs.
     - Your Conduit version.
   - If it's a feature request, let us know the motivation behind that feature,
      and the expected behavior of it.

## Submitting changes

We also value contributions in the form of pull requests. When opening a PR please ensure:

- You have followed the [Code Guidelines](https://github.com/ConduitIO/conduit/blob/main/docs/code_guidelines.md).
- There is no other [pull request](https://github.com/ConduitIO/conduit/pulls) for the same update/change.
- You have written unit tests.
- You have made sure that the PR is of reasonable size and can be easily reviewed.

Also, if you are submitting code, please ensure you have adequate tests for the feature,
and that all the tests still run successfully.

- Unit tests can be run via `make test`.
- Integration tests can be run via `make test-integration`, they require
  [Docker](https://www.docker.com/) to be installed and running. The tests will
  spin up required docker containers, run the integration tests and stop the
  containers afterwards.

We would like to ask you to use the provided Git hooks (by running `git config core.hooksPath githooks`),
which automatically run the tests and the linter when pushing code.

## Adding a vendored pipeline template

`conduit pipelines init --template <name>` scaffolds a runnable pipeline from a small,
permanently-maintained, embedded gallery (`cmd/conduit/root/pipelines/templates/`,
`cmd/conduit/root/pipelines/template_gallery.go`) — this is the vendoring model the project
intends to keep long-term (cargo-new / rustup-init style), not a stopgap ahead of a future
connector/template registry. Adding a template is a normal Tier-2 PR: one reviewer approval,
green CI, docs in the same PR — it is **not** the registry's signed-publish flow (there is no
`conduit templates publish`, and this contribution path has nothing to do with registry trust or
signing).

To propose a new template:

1. **Use only built-in connectors.** Every source/destination the template configures must be a
   key in `builtin.DefaultBuiltinConnectors` (`pkg/plugin/connector/builtin/registry.go`) — a
   template requiring a manual connector download reintroduces exactly the cliff this gallery
   exists to avoid, and `TestValidateGalleryCatalog_RejectsNonBuiltinConnector`-style checks will
   fail the build if it doesn't.
2. **Add a directory** under `cmd/conduit/root/pipelines/templates/<name>/` with a `pipeline.yaml`
   (the literal, fully-rendered pipeline config — this is embedded via `go:embed` and is the exact
   bytes `init` writes to disk) and a `README.md` covering: a config reference table, a
   delivery-semantics note (state only what the underlying connectors actually guarantee — see
   CLAUDE.md's Invariant 3, at-least-once is the floor), and a runnable example.
3. **Register it** in `galleryCatalogSpec()` (`template_gallery.go`) with a one-line
   `Description` and `DeliverySemantics` string (surfaced by `--template list --json`). The name
   must never be `list` — that value is reserved to mean "enumerate the catalog".
4. **Add an end-to-end test**, not just a parse check: spin up whatever infra the template needs
   (reuse `test/compose-templates.yaml`'s pattern if it needs its own containers), scaffold the
   template for real, run it against the real engine, and assert records actually land at the
   destination.
5. Reviewer checklist (Tier 2): built-in connectors only (1), README sections present and
   accurate against what the new CI job actually asserts (2), catalog entry registered (3), and a
   green end-to-end test that asserts on destination-side data (4) — "the YAML parses" alone is
   not sufficient for merge.

### Quick steps to contribute

1. Fork the project
2. Download your fork to your machine
3. Create your feature branch (`git checkout -b my-new-feature`)
4. Make changes and run tests
5. Commit your changes
6. Push to the branch
7. Create new pull request

## Changing a pipeline engine

The engines are `pkg/lifecycle` (v1, default) and `pkg/lifecycle-poc` (arch-v2, behind
`--preview.pipeline-arch-v2`), plus the layer under them: `pkg/connector` (source and destination
wrappers, the position persister) and the plugin boundary in `pkg/plugin`. Read
[Life of a record](docs/architecture/life-of-a-record.md) and
[Guarantees](docs/architecture/guarantees.md) first. The second page says which promises a test enforces
and which do not; a change that touches a promise updates its row in the same PR.

> The tier guidance, the design-doc template and the proof steps below are **proposals**, distilled from
> `CLAUDE.md` and the existing design docs. They are not yet agreed project policy.

### Pick the tier

Every PR declares one in its description.

- **Tier 1, data path.** Record flow, ack, position or checkpoint logic, the state layer, the connector
  protocol, serialization formats. Needs a design doc or ADR (or an explicit waiver for a small fix), a
  failure-mode analysis in the PR (what could this break, which metric or alert would show it, how do we
  roll back), chaos and upgrade tests updated or justified as unaffected, and sign-off from a human who did
  not write the change. No merge on automated review alone, and none in the 48 hours before a release cut.
- **Tier 2, features.** Connectors, processors, CLI, UI, registry. One reviewer, tests green, docs in the
  same PR.
- **Tier 3, chore.** Dependencies, CI, typos, comments. One approval, green CI.

If you are unsure, it is Tier 1. Test-only and CI-only changes to the engine packages are Tier 2 or 3.

### Design-doc template

Non-trivial engine work starts with a doc in `docs/design-documents/`, named `YYYYMMDD-slug.md`. A doc that
does not enumerate failure modes is not done.

```markdown
# Title

## Summary
Two or three sentences: what changes and why.

## Context
The problem, and the constraints (compatibility, performance, invariants 1 to 7 that the change touches).

## Decision
What we will do, in enough detail to implement. State each invariant at the place it is enforced.

## Alternatives
At least two, each with why it lost.

## Consequences
What gets easier, what gets harder, what is now a public contract.

## Failure modes
For each of: crash mid-operation, network partition, duplicate delivery, out-of-order delivery, malformed
input, disk full, slow or withheld acks. What happens, and which test shows it.

## Upgrade / rollback
How state, config and protocol move between versions, and how to go back.

## Observability
Metrics, logs and error codes an operator would use to see this working or failing.

## Related
ADRs, issues, PRs.
```

A significant decision also gets an immutable ADR in `docs/architecture-decision-records/`.

### Prove the change is safe

In this order, cheapest first:

1. **Write the invariant test on both engines.** `pkg/internal/enginetest` is an in-process kit of fake
   sources and destinations you control: record count and batch size, read, write and ack latency, acks in
   chunks or through a buffered pump, SDK-style destination batching, nacks, errors, stalls and crashes.
   `pkg/internal/enginetest/engines` runs the same pipeline on v1 and v2. Nothing waits on a sleep: tests
   block on signals (`WaitAcked`, `WaitDelivered`) and gates. A `Ledger` shared by the source and
   destinations records a **violation** at any source `Ack` that arrives before the destination handed the
   engine its ack for that record; it does not fail the test by itself, so assert that `ledger.Violations()`
   is empty at the end (or wait with `Ledger.Failed()` to stop at the first one). It requires you to name the
   destinations with `Require(...)`; with none named it reports a violation rather than pass vacuously.

   ```go
   ledger := enginetest.NewLedger().Require("dst")
   src := enginetest.NewSource(enginetest.SourceConfig{ID: "src", Total: 10, Ledger: ledger})
   dst := enginetest.NewDestination(enginetest.DestinationConfig{ID: "dst", Ledger: ledger})
   dst.AckGate.Close() // the destination holds its acks back

   run, _ := eng.Start(ctx, engines.Topology{
       Sources:      []*enginetest.Source{src},
       Destinations: []*enginetest.Destination{dst},
   })
   _ = dst.WaitDelivered(ctx, 1)
   dst.AckGate.Open()
   _ = src.WaitAcked(ctx, 10)
   _ = run.Stop(ctx)
   _ = run.Wait(ctx)
   // ledger.Violations() must be empty: no source ack preceded its destination ack.
   ```

   Copy the closest case from `pkg/internal/enginetest/engines/invariants_test.go`. A bug fix ships with
   the test that fails without it. If the case only fails on one engine, say so with a skip that links the
   issue, as the existing ones do.
2. **Run the engine packages with the race detector:**
   `go test -race ./pkg/internal/... ./pkg/lifecycle/... ./pkg/lifecycle-poc/... ./pkg/connector/...`
3. **Run the chaos suite** for anything on the data path, as CI does:
   `go test -race -count=3 -shuffle=on ./tests/chaos/...`. It kills a real child process with SIGKILL and
   checks the position store. The engine tests do not replace it: they use no persister and no process
   boundary.
4. **Measure with `make bench-engine-ab`**, which compares your working tree with `origin/main` (or
   `BASE=<ref>`) in interleaved rounds and prints the A/A noise floor next to the delta. Do not compare two
   sequential `make bench-engine` runs: drift between invocations shows up as a significant delta on
   identical code. `make bench-engine` alone gives one snapshot. The benchmark runs seven reference shapes
   (1x1, 2x2, 4x4, source batch 100, ack latency, buffered acks, destination batching) on both engines; one
   pass is about 15 s, so `bench-engine-ab` at the default 5 rounds takes a few minutes. On an engine PR a CI job posts the same comparison as a comment, with a noise floor measured on
   unchanged code. Both are indicative: they drive fakes, so they show engine overhead and say nothing about
   connector throughput. A throughput claim needs a benchi run with committed configs and an environment spec.
5. **Update the docs.** The matching row in `docs/architecture/guarantees.md`, the flow in
   `docs/architecture/life-of-a-record.md` if the behaviour changed, and `llms.txt` if anything user-facing did.

## License

Apache 2.0, see [LICENSE](LICENSE.md).

## Code of Conduct

Conduit has adopted [Contributor Covenant](https://www.contributor-covenant.org/)
as its [Code of Conduct](https://github.com/ConduitIO/.github/blob/main/CODE_OF_CONDUCT.md).
We highly encourage contributors to familiarize themselves with the standards we want our
community to follow and help us enforce them.
