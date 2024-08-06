# Releases

## General information

A Conduit release has the following parts:

- a GitHub release, which further includes
  - packages for different operating systems and architectures
  - a file with checksums for the packages
  - a changelog
  - the source code
- a GitHub package, which is the official Docker image for Conduit. It's available on GitHub's Container Registry. The
latest Docker image which is not a nightly is tagged with `latest`.

## Before a release

### Update dependencies

Dependencies should be updated in the order described below. The instructions
assume that this repository and the other Conduit repositories are all cloned in
the same directory.

1. [`conduit-commons`](https://github.com/ConduitIO/conduit-commons)
    - Run `scripts/get-compare-link.sh ../conduit-commons/` to compare the latest tag and the `main` branch.
    - If the changes should be released/tagged, push a new tag.
1. [`conduit-connector-protocol`](https://github.com/conduitio/conduit-connector-protocol)
    - Update `conduit-commons` if needed: `go get github.com/conduitio/conduit-commons@vX.Y.Z`
    - Run `scripts/get-compare-link.sh ../conduit-connector-protocol/` to compare the latest tag and the `main` branch.
    - If the changes should be released/tagged, push a new tag.
1. [`conduit-connector-sdk`](https://github.com/ConduitIO/conduit-connector-sdk)
    - Update `conduit-commons` if needed: `go get github.com/conduitio/conduit-commons@vX.Y.Z`
    - Update `conduit-connector-protocol` if needed: `go get github.com/conduitio/conduit-connector-protocol@vX.Y.Z`
    - Run `scripts/get-compare-link.sh ../conduit-connector-sdk/` to compare the latest tag and the `main` branch.
    - If the changes should be released/tagged, push a new tag.
1. [`conduit-processor-sdk`](https://github.com/ConduitIO/conduit-processor-sdk)
    - Update `conduit-commons` if needed: `go get github.com/conduitio/conduit-commons@vX.Y.Z`
    - Run `scripts/get-compare-link.sh ../conduit-processor-sdk/` to compare the latest tag and the `main` branch.
    - If the changes should be released/tagged, push a new tag.
1. [`conduit-schema-registry`](https://github.com/ConduitIO/conduit-schema-registry/)
   - Update `conduit-commons` if needed: `go get github.com/conduitio/conduit-commons@vX.Y.Z`
   - Run `scripts/get-compare-link.sh ../conduit-schema-registry/` to compare the latest tag and the `main` branch.
   - If the changes should be released/tagged, push a new tag.
1. Bump the Connector SDK dependency on [`conduit-connector-template`](https://github.com/ConduitIO/conduit-connector-template)
1. Bump the Connector SDK in all the built-in connectors: `scripts/bump-sdk-in-connectors.sh vX.Y.Z`
1. For each of the built-in connectors (file, kafka, generator, s3, postgres, log):
    - Run `scripts/get-compare-link.sh ../conduit-processor-sdk/` to compare the latest tag and the `main` branch.
    - If the changes should be released/tagged, push a new tag.
1. Bump the built-in connectors: `scripts/bump-builtin-connectors.sh`
1. Conduit itself
    - Update `conduit-schema-registry` if needed
    - Update `conduit-connector-sdk` if needed
    - Update `conduit-processor-sdk` if needed
    - Update `conduit-connector-protocol` if needed
    - Update `conduit-commons` if needed
    - Release Conduit (see instructions below)

## Documentation

1. Write a blog post.
2. Regenerate processor documentation on [`conduit-site`](https://github.com/ConduitIO/conduit-site).
3. Update banner on the web-site ([example](https://github.com/ConduitIO/conduit-site/pull/47/files#diff-cc8abb6104e21d495dc8f64639c7b03419226d920d1c545df51be9b0b73b2784)).

## Releasing Conduit

A release is triggered by pushing a new tag which starts with `v` (for example `v1.2.3`). Everything else is then
handled by GoReleaser and GitHub actions. To push a new tag, please use the script [scripts/tag.sh](https://github.com/ConduitIO/conduit/blob/main/scripts/tag.sh),
which also checks if the version conforms to SemVer. Example:

```sh
scripts/tag.sh 1.2.3
```

## Nightly builds

We provide nightly builds (binaries and Docker images) and keep them for 7 days. The latest nightly Docker image is tagged
with `latest-nightly`.

## Implementation

The GitHub release is created with [GoReleaser](https://github.com/goreleaser/goreleaser/). GoReleaser _can_ build
Docker images, but we're building those "ourselves" (using Docker's official GitHub actions), since GoReleaser doesn't
work with multi-stage Docker builds.

Nightly builds are created in the same way, it's only the triggering which is different. Namely, we have a GitHub action
(defined in [trigger-nightly.yml](/.github/workflows/trigger-nightly.yml)) which is creating nightly tags once in 24 hours.
A new nightly tag then triggers a new release. The mentioned GitHub action also cleans up older tags, releases and
Docker images.

The "Trigger nightly build" GH action requires a personal access token, and _not_ a GitHub token provided by Actions. The
reason is that a workflow which produces an event using a GitHub token cannot trigger another workflow through that event.
For more information, please check [Triggering a workflow from a workflow](https://docs.github.com/en/actions/using-workflows/triggering-a-workflow#triggering-a-workflow-from-a-workflow).
