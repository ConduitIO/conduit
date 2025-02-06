---
name: 🚢 Conduit release checklist
about: Use this template to guide you through the Conduit release process.
title: "[Release] Conduit vX.Y.Z"
labels: release
assignees: ''
---

# Conduit Release Checklist

This issue serves as a checklist for releasing a new version of Conduit. Follow the steps below to ensure a smooth release process.

## General Information

A Conduit release includes:

- A GitHub release with packages for different OS and architectures, checksums, a changelog, and source code.
- A GitHub package for the official Docker image, available on GitHub's Container Registry, tagged with `latest`.

## Before a Release

### Update Dependencies

Update dependencies in the following order, ensuring all repositories are cloned in the same directory:

- [ ] **[`conduit-commons`](https://github.com/ConduitIO/conduit-commons)**: Run `scripts/get-compare-link.sh ../conduit-commons/` to compare the latest tag and `main` branch. If changes are needed, push a new tag.
- [ ] **[`conduit-connector-protocol`](https://github.com/ConduitIO/conduit-connector-protocol)**: Update `conduit-commons` if necessary: `go get github.com/conduitio/conduit-commons@vX.Y.Z`. Run `scripts/get-compare-link.sh ../conduit-connector-protocol/` and tag if needed.
- [ ] **[`conduit-connector-sdk`](https://github.com/ConduitIO/conduit-connector-sdk)**: Update dependencies (`conduit-commons`, `conduit-connector-protocol`) as needed. Run `scripts/get-compare-link.sh ../conduit-connector-sdk/` and tag if needed.
- [ ] **[`conduit-processor-sdk`](https://github.com/ConduitIO/conduit-processor-sdk)**: Update `conduit-commons` if necessary. Run `scripts/get-compare-link.sh ../conduit-processor-sdk/` and tag if needed.
- [ ] **[`conduit-schema-registry`](https://github.com/ConduitIO/conduit-schema-registry)**: Update `conduit-commons` if necessary. Run `scripts/get-compare-link.sh ../conduit-schema-registry/` and tag if needed.
- [ ] **[Connector SDK in `conduit-connector-template`](https://github.com/ConduitIO/conduit-connector-template)**: Bump the Connector SDK dependency.
- [ ] **[`conduit-connector-file`](https://github.com/ConduitIO/conduit-connector-file)**: Bump the Connector SDK `scripts/bump-sdk-in-connectors.sh vX.Y.Z`. Run `scripts/get-compare-link.sh ../conduit-connector-file/` and tag if needed.
- [ ] **[`conduit-connector-kafka`](https://github.com/ConduitIO/conduit-connector-kafka)**: Bump the Connector SDK `scripts/bump-sdk-in-connectors.sh vX.Y.Z`. Run `scripts/get-compare-link.sh ../conduit-connector-kafka/` and tag if needed.
- [ ] **[`conduit-connector-generator`](https://github.com/ConduitIO/conduit-connector-generator)**: Bump the Connector SDK `scripts/bump-sdk-in-connectors.sh vX.Y.Z`. Run `scripts/get-compare-link.sh ../conduit-connector-generator/` and tag if needed.
- [ ] **[`conduit-connector-s3`](https://github.com/ConduitIO/conduit-connector-s3)**: Bump the Connector SDK `scripts/bump-sdk-in-connectors.sh vX.Y.Z`. Run `scripts/get-compare-link.sh ../conduit-connector-s3/` and tag if needed.
- [ ] **[`conduit-connector-postgres`](https://github.com/ConduitIO/conduit-connector-postgres)**: Bump the Connector SDK `scripts/bump-sdk-in-connectors.sh vX.Y.Z`. Run `scripts/get-compare-link.sh ../conduit-connector-postgres/` and tag if needed.
- [ ] **[`conduit-connector-log`](https://github.com/ConduitIO/conduit-connector-log)**: Bump the Connector SDK `scripts/bump-sdk-in-connectors.sh vX.Y.Z`. Run `scripts/get-compare-link.sh ../conduit-connector-log/` and tag if needed.
- [ ] **Bump built-in connectors on Conduit**: Run `scripts/bump-builtin-connectors.sh`
- [ ] **Release Conduit** (see instructions below).

## Documentation

- [ ] Write a blog post.
- [ ] Regenerate processor documentation on [`conduit-site`](https://github.com/ConduitIO/conduit-site) by running `cd src/processorgen/ && make generate`.
- [ ] Update the banner on the [website](https://github.com/ConduitIO/conduit-site). ⚠️ Remember to bump the `announcementBar.id` in `docusaurus.config.ts`.
- [ ] Create a changelog on the [website](https://github.com/ConduitIO/conduit-site).
- [ ] Search and replace the latest version in [`conduit-site`](https://github.com/ConduitIO/conduit-site).
- [ ] Search and replace the latest version in [README.md](https://github.com/ConduitIO/conduit/blob/main/README.md).

## Releasing Conduit

Use the script [scripts/tag.sh](https://github.com/ConduitIO/conduit/blob/main/scripts/tag.sh) to ensure version conformity.

```sh
scripts/tag.sh 1.2.3
```

## After a Release

- [ ] Run `brew update conduit` and check latest version.
- [ ] Check release artifacts are available for `OSX Darwin`, `Linux`, and `Windows`.
- [ ] Pull Docker images.
- [ ] Run a few [testing pipelines](https://github.com/ConduitIO/conduit/tree/main/examples/pipelines) to make sure things are still operational.

## Additional information

### Nightly Builds

- Nightly builds (binaries and Docker images) are provided and kept for 7 days.
- The latest nightly Docker image is tagged with `latest-nightly`.

### Implementation

- The GitHub release is created with [GoReleaser](https://github.com/goreleaser/goreleaser/).
- Nightly builds are triggered by a GitHub action, defined in [trigger-nightly.yml](/.github/workflows/trigger-nightly.yml).

### Notes

- The "Trigger nightly build" GitHub action requires a personal access token, not the GitHub token provided by Actions.

For more information, refer to [Triggering a workflow from a workflow](https://docs.github.com/en/actions/using-workflows/triggering-a-workflow#triggering-a-workflow-from-a-workflow).

---

Please ensure each step is completed before closing this issue.