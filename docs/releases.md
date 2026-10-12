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

We provide nightly builds (binaries and Docker images). The last 5 nightly releases are kept, and nightly Docker images
for 7 days (see "Nightly image cleanup" below). The latest nightly Docker image is tagged
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

### Nightly image cleanup

The `cleanup-nightly-images` job in `trigger-nightly.yml` deletes nightly images whose `vX.Y.Z-nightly.YYYYMMDD` tag
is more than 7 days old, together with everything attached to them: the per-platform manifests, buildx attestation
manifests, and the cosign signature and attestation artifacts. GHCR has no OCI referrers API, so cosign stores those
under a `sha256-<signed digest>` tag that points at an index of Sigstore bundles. That tag does not say "nightly", so
the planner (`.github/scripts/ghcr_nightly_cleanup.py`) follows digests instead of matching tag names:

- an image is deleted only if **every** tag on it is an expired nightly tag. `latest-nightly`, `latest` or a stable
  `vX.Y.Z` tag on the same digest keeps it;
- nothing reachable from a kept image is deleted, so a stable release's signature and attestations are never touched;
- a `sha256-<digest>` tag whose subject no longer exists in the registry is deleted as an orphan;
- right before each delete it re-reads the version's live tags and skips it if a tag was added since the plan. This
  recheck covers **tagged versions only**; an untagged manifest that a release pushes after the plan is read is not
  visible to it, so do not apply while a stable release is being pushed. The job runs after the nightly tag push
  (`needs`) and never overlaps another cleanup (concurrency group). It cannot wait for `release.yml`, which the tag
  push starts separately; that is fine because the nightly being built has today's date and is never expired.

Before it deletes anything the script refuses to act on a registry snapshot that lacks the `latest` tag or any stable
`vX.Y.Z` tag, has fewer than 50 tags, or is much smaller than the version list GitHub's Packages API reports (exit
code 2).

The job is a dry run until the repository variable `GHCR_CLEANUP_APPLY` is `true`; a manual run can also apply with
the `cleanup-apply` input. Each run writes its plan to the job summary. To see the plan locally (anonymous registry
access, no token needed):

```sh
python3 .github/scripts/ghcr_nightly_cleanup.py --image ghcr.io/conduitio/conduit --keep-days 7
```

Apply mode is rate-limited on purpose. The first apply has about 2,200 versions to delete, and `GITHUB_TOKEN` gets
roughly 1,000 API requests an hour (GitHub also limits content-changing requests, DELETE included, to about 80 a
minute and 500 an hour). A run therefore deletes at most `--max-deletes` versions (default 400), about 0.8 s apart, and
stops without error when it hits the cap. It also stops on an HTTP 429, on a 403 that carries `Retry-After` or
`X-RateLimit-Remaining: 0`, and when `X-RateLimit-Remaining` drops to 25. The next night's run re-plans from a fresh
snapshot and continues, so the backlog drains over about six nights. A plain 403 (no permission), or three network
errors in a row, stops the run and fails the job. Exit codes: 0 done, or stopped early after progress; 1 a delete
failed or the run could not continue; 2 the planner refused to act.

An image is deleted as a unit, in dependency order: every manifest after everything it lists (a signature index after
its bundle, a platform manifest after its attestations), the tagged root last. The cap is applied between images,
never inside one (a run can stop a few versions under the cap). Both rules exist because the untagged children of an
image are only findable through its tagged root; deleting the root first, or stopping after it, would strand them for
good. For the same reason, if any delete in an image fails (an HTTP error or a network error), the rest of that image
is abandoned and its root is never deleted; the next run plans the image again and finishes it. An abandoned image
fails the job. If the live-tag recheck finds that an image's root gained a tag, the whole image is kept, not just the
root.

A run also stops after 5 failed deletes in a row with no success in between (for example a token without the Admin
role, where every delete is a 404), and it paces failures like successes. Failed attempts count against the cap.

**Stopping early on a rate limit exits 0.** If a run makes progress and then hits the cap or a rate limit, the job is
green and carries a `::warning::` annotation, because the next run is expected to continue. That also means a drain
that keeps being rate limited nightly stays green. Check the "stopped early ... planned versions left" line in the job
summary while the backlog drains. Making the job fail after several consecutive rate-limited nights needs state
between runs (for example the conclusion of earlier runs) and is a follow-up, not part of this change.

Deleting needs a token that can delete versions of the `conduit` package. The job uses `GITHUB_TOKEN` with
`packages: write`, which requires the repository to have the **Admin** role on the package (package settings, "Manage
Actions access"). If deletes fail, the job fails and lists the HTTP status of each failed delete.

GitHub does not let anyone delete a version of a **public** package that has more than 5,000 downloads. A nightly
image that popular fails every night, with the HTTP status and message in the job log. It counts as a failed delete and
keeps the job red until GitHub support removes it; the other deletes in the run are unaffected (a 403 whose message
mentions downloads does not stop the run).

## Signatures and SBOMs

Starting with v0.20.0, every release and nightly is signed with [Sigstore](https://www.sigstore.dev/) keyless
signing. There is no signing key: the release workflow exchanges its GitHub Actions OIDC token for a short-lived
certificate that names this repository, the `release.yml` workflow and the tag that triggered it, and the signature is
recorded in the public Rekor transparency log.

- `checksums.txt` is signed by GoReleaser with `cosign sign-blob`. The signature, certificate and transparency-log entry
  are in `checksums.txt.sigstore.json`, a Sigstore bundle. `checksums.txt` lists every archive, Linux package and SBOM,
  so verifying it and then the checksums covers all of them.
- Each archive has an SPDX SBOM next to it, `<archive>.sbom.json`, generated by [Syft](https://github.com/anchore/syft).
- The Docker image is signed with `cosign sign` by digest, after it is pushed. All tags of one build (`vX.Y.Z`, `vX.Y`,
  `latest`, `latest-nightly`) point at the same digest and share that signature.
- Starting with v0.20.1, each platform image (`linux/amd64`, `linux/arm64`) has an SPDX SBOM generated by Syft from that
  image and attached to **that platform's digest** as a signed cosign attestation (`cosign attest --type spdxjson`).
  The multi-arch index has no SBOM of its own: one SBOM cannot describe two different images.

## Verifying a release

You need [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) v3 or later; signatures are in the Sigstore
bundle format, which v3 reads by default.
Replace `v0.20.0` with the tag you downloaded.

Download `checksums.txt`, `checksums.txt.sigstore.json` and the files you want into one directory, then:

```sh
cosign verify-blob \
  --certificate-identity 'https://github.com/ConduitIO/conduit/.github/workflows/release.yml@refs/tags/v0.20.0' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  --bundle checksums.txt.sigstore.json \
  checksums.txt

# only once the signature verifies (on macOS: shasum -a 256 --check --ignore-missing)
sha256sum --check --ignore-missing checksums.txt
```

The Docker image:

```sh
cosign verify \
  --certificate-identity 'https://github.com/ConduitIO/conduit/.github/workflows/release.yml@refs/tags/v0.20.0' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  ghcr.io/conduitio/conduit:v0.20.0
```

`cosign verify` resolves the tag to a digest and checks the signature on that digest. To pin what you run, use the
digest it prints: `ghcr.io/conduitio/conduit@sha256:...`.

The image SBOM (v0.20.1 and later) is attached to the platform image, so first resolve the digest for your platform,
then verify the attestation and extract the SBOM from it:

```sh
DIGEST=$(docker buildx imagetools inspect --raw ghcr.io/conduitio/conduit:v0.20.1 \
  | jq -r '.manifests[] | select(.platform.os == "linux" and .platform.architecture == "arm64") | .digest')
# or: DIGEST=$(crane digest --platform linux/arm64 ghcr.io/conduitio/conduit:v0.20.1)

cosign verify-attestation \
  --type spdxjson \
  --certificate-identity 'https://github.com/ConduitIO/conduit/.github/workflows/release.yml@refs/tags/v0.20.1' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  "ghcr.io/conduitio/conduit@$DIGEST" \
  | jq -r '.payload | @base64d | fromjson | .predicate' > conduit-linux-arm64.spdx.json
```

`verify-attestation` against the tag or the index digest finds nothing. That is expected, because the attestations are
on the platform digests.

To accept any release or nightly from this repository instead of one exact tag, swap `--certificate-identity` for:

```sh
--certificate-identity-regexp '^https://github\.com/ConduitIO/conduit/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+.*$'
```

Releases up to and including v0.19.0 and nightlies built before signing landed are not signed; for those only
`checksums.txt` is available.

## Pinned release tooling

`release.yml` pins everything that runs with release permissions to an exact version, so a tag builds with the same
tools the nightlies before it used:

- every action by full commit SHA, with the version in a trailing comment (`uses: owner/action@<sha> # vX.Y.Z`).
  Dependabot's `github-actions` updates move the SHA and the comment together.
- GoReleaser by exact version (`version:` input of `goreleaser/goreleaser-action`), and cosign and syft by exact version
  in their installer steps. Dependabot does not see these; bump them by hand.

Bump a tool in its own PR, never in the run-up to a release tag, and let at least one nightly go green on it before
tagging. The nightly is the only end-to-end test of the release pipeline.

## How to release

In order to create a new Conduit release, you'll need to create a new issue using the [Conduit release template](https://github.com/ConduitIO/conduit/issues/new?assignees=&labels=release&projects=&template=4-conduit-release.md&title=%5BRelease%5D+Conduit+vX.Y.Z).

The issue will guide you through the process of creating a new release.

It will also provide you with a checklist to make sure you don't forget anything.
