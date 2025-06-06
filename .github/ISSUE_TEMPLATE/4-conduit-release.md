---
name: 🚢 Conduit release checklist
about: Use this template to guide you through the Conduit release process.
title: "[Release] Conduit vX.Y.Z"
labels: release
assignees: ''
---


This issue serves as a checklist for releasing a new version of Conduit.
Follow the steps below to ensure a smooth release process.

## General Information

A Conduit release includes:

- A GitHub release with packages for different OS and architectures, checksums,
  a changelog, and source code.
- A GitHub package for the official Docker image, available on GitHub's Container
  Registry, tagged with `latest`.

## Before a Release

### Update Dependencies

Update dependencies in the following order, ensuring all repositories are cloned
in the same directory:

- [ ] **[`conduit-commons`](https://github.com/ConduitIO/conduit-commons)**:
  - [ ] Run `scripts/get-compare-link.sh ../conduit-commons/` to compare the
    latest tag and `main` branch. If changes are needed, push a new tag.
- [ ] **[`conduit-connector-protocol`](https://github.com/ConduitIO/conduit-connector-protocol)**:
  - [ ] Update `conduit-commons` if necessary: `go get github.com/conduitio/conduit-commons@vX.Y.Z`.
  - [ ] Run `scripts/get-compare-link.sh ../conduit-connector-protocol/` and tag if needed.
- [ ] **[`conduit-connector-sdk`](https://github.com/ConduitIO/conduit-connector-sdk)**:
  - [ ] Update dependencies (`conduit-commons`, `conduit-connector-protocol`) as needed.
  - [ ] Run `scripts/get-compare-link.sh ../conduit-connector-sdk/` and tag if needed.
- [ ] **[`conduit-processor-sdk`](https://github.com/ConduitIO/conduit-processor-sdk)**:
  - [ ] Update `conduit-commons` if necessary.
  - [ ] Run `scripts/get-compare-link.sh ../conduit-processor-sdk/` and tag if needed.
- [ ] **[`conduit-schema-registry`](https://github.com/ConduitIO/conduit-schema-registry)**:
  - [ ] Update `conduit-commons` if necessary.
  - [ ] Run `scripts/get-compare-link.sh ../conduit-schema-registry/` and tag if needed.
- [ ] **[Connector SDK in `conduit-connector-template`](https://github.com/ConduitIO/conduit-connector-template)**:
  Bump the Connector SDK dependency.
- [ ] Bump the Connector SDK `scripts/bump-sdk-in-connectors.sh vX.Y.Z`.
- [ ] **[`conduit-connector-file`](https://github.com/ConduitIO/conduit-connector-file)**:
  - [ ] Run `scripts/get-compare-link.sh ../conduit-connector-file/`
  - [ ] Run `../conduit-connector-file/scripts/bump_version.sh` if needed.
- [ ] **[`conduit-connector-kafka`](https://github.com/ConduitIO/conduit-connector-kafka)**:
  - [ ] Run `scripts/get-compare-link.sh ../conduit-connector-kafka/`
  - [ ] Run `../conduit-connector-kafka/scripts/bump_version.sh` if needed.
- [ ] **[`conduit-connector-generator`](https://github.com/ConduitIO/conduit-connector-generator)**:
  - [ ] Run `scripts/get-compare-link.sh ../conduit-connector-generator/`
  - [ ] Run `../conduit-connector-generator/scripts/bump_version.sh` if needed.
- [ ] **[`conduit-connector-s3`](https://github.com/ConduitIO/conduit-connector-s3)**:
  - [ ] Run `scripts/get-compare-link.sh ../conduit-connector-s3/`
  - [ ] Run `../conduit-connector-s3/scripts/bump_version.sh` if needed.
- [ ] **[`conduit-connector-postgres`](https://github.com/ConduitIO/conduit-connector-postgres)**:
  - [ ] Run `scripts/get-compare-link.sh ../conduit-connector-postgres/`
  - [ ] Run `../conduit-connector-postgres/scripts/bump_version.sh` if needed.
- [ ] **[`conduit-connector-log`](https://github.com/ConduitIO/conduit-connector-log)**:
  - [ ] Run `scripts/get-compare-link.sh ../conduit-connector-log/`
  - [ ] Run `../conduit-connector-log/scripts/bump_version.sh` if needed.
- [ ] **Bump built-in connectors on Conduit**: Run `scripts/bump-builtin-connectors.sh`
- [ ] **Release Conduit** (see instructions below).

## Documentation

- [ ] Write a blog post.
- [ ] Regenerate processor documentation on [`conduit-site`](https://github.com/ConduitIO/conduit-site)
  by running `cd src/processorgen/ && make generate`.
- [ ] Update the banner on the [website](https://github.com/ConduitIO/conduit-site).
  ⚠️ Remember to bump the `announcementBar.id` in `docusaurus.config.ts`.
- [ ] Create a changelog on the [website](https://github.com/ConduitIO/conduit-site).
- [ ] Search and replace the latest version in [`conduit-site`](https://github.com/ConduitIO/conduit-site).
- [ ] Search and replace the latest version in [README.md](https://github.com/ConduitIO/conduit/blob/main/README.md).

## Releasing Conduit

Use [scripts/tag.sh](https://github.com/ConduitIO/conduit/blob/main/scripts/tag.sh) to ensure version conformity.

> [!IMPORTANT]  
> Make sure you have the latest changes.

```sh
scripts/tag.sh MAJOR.MINOR.PATCH
```

## After a Release

- [ ] Run `brew upgrade conduit` and check latest version.
  ([Homebrew formula](https://github.com/Homebrew/homebrew-core/blob/master/Formula/c/conduit.rb)).
- [ ] Check release artifacts are available for `OSX Darwin`, `Linux`, and `Windows`.
- [ ] Pull Docker images.
- [ ] Run a few [testing pipelines](https://github.com/ConduitIO/conduit/tree/main/examples/pipelines)
  to make sure things are still operational.

## Additional information

### Nightly Builds

- Nightly builds (binaries and Docker images) are provided and kept for 7 days.
- The latest nightly Docker image is tagged with `latest-nightly`.

### Implementation

- The GitHub release is created with [GoReleaser](https://github.com/goreleaser/goreleaser/).
- Nightly builds are triggered by a GitHub action, defined in [trigger-nightly.yml](/.github/workflows/trigger-nightly.yml).

> [!NOTE]  
> The "Trigger nightly build" GitHub action requires a personal access token, not the GitHub token provided by Actions.
> [GitHub documentation](https://docs.github.com/en/actions/using-workflows/triggering-a-workflow#triggering-a-workflow-from-a-workflow).
