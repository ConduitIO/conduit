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

## How to release

In order to create a new Conduit release, you'll need to create a new issue using the ["Release" template](https://github.com/ConduitIO/conduit/issues/new?template=release-checklist.yml&title=%5BRelease%5D%20Conduit%20vX.Y.Z).

The issue will guide you through the process of creating a new release.

It will also provide you with a checklist to make sure you don't forget anything.
