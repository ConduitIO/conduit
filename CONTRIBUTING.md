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
