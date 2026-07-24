# Contributing to Conduit

Thank you so much for contributing to Conduit. We appreciate your time and help!
As a contributor, here are the guidelines we would like you to follow.

## Asking questions

If you have a question or you are not sure how to do something, please
[open a discussion](https://github.com/ConduitIO/conduit/discussions) or hit us up
on [Discord](https://discord.meroxa.com)!

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

## License

Apache 2.0, see [LICENSE](LICENSE.md).

## Code of Conduct

Conduit has adopted [Contributor Covenant](https://www.contributor-covenant.org/)
as its [Code of Conduct](https://github.com/ConduitIO/.github/blob/main/CODE_OF_CONDUCT.md).
We highly encourage contributors to familiarize themselves with the standards we want our
community to follow and help us enforce them.
