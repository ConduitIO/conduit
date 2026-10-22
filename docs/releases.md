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

## Release notes

The human-readable release notes for each release live in `docs/releases/vX.Y.Z.md` (for example
`docs/releases/v0.21.0.md`). They are mirrored on the [Conduit.io changelog](https://conduitdata.io/changelog)
and in the GitHub release body. The GitHub release's changelog is generated from conventional commits; the notes
file is the curated summary a human would actually want to read.

## Release gates

`make release-check TAG=vX.Y.Z` runs in the `release-check` job of `release.yml` before anything is published
(nightly tags skip it). A failure stops the GitHub release and the Docker image.

- **Fixes the release must contain** (`.github/scripts/release_fixes.py`). `.github/release-fixes.txt` on the release
  branch lists `module | commit | note` per fix; `conduit` means this repository. Each commit must be an ancestor of
  the tag of that module's version in `go.mod` (or of the release commit). A patch release (`vX.Y.Z`, Z > 0) must
  have at least one entry that is new since the previous stable tag, so a list of old commits does not pass. A
  `replace` of a listed module to a fork or a local path fails, because the version then says nothing about the code
  that ships; waive it with `waive-replace | <module> | <reason>`. A committed `go.work` is treated the same way
  (its `replace` lines count, and a `use` of anything but the root needs `waive-replace | go.work | <reason>`). An unresolvable module, tag or commit fails. The
  gate lives in the tagged commit's
  `release.yml`, so a release branch has it only once this change is backported to it.

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

## Release branches

Each release line has a long-lived branch, `release/vX.Y.x` (the first is `release/v0.20.x`). Stable tags for that line
(`v0.20.1`, `v0.20.2`, ...) are cut from the branch, and `main` is free to move on to the next minor (v0.21 engine
work) without freezing merges. Nightlies still come from `main` (see "Nightly builds on release branches" below).

### Branching

- Cut the branch from `main` at a commit that is already on `main`, by pushing the SHA, not by creating the branch
  from a local checkout: `git push origin <sha>:refs/heads/release/vX.Y.x`. Record the SHA in the release issue.
- Protect it like `main`: the same required status contexts with strict mode, the same review rule, the same
  `enforce_admins` setting (so the admin-merge path in CLAUDE.md applies), no force pushes, no deletion.
- Add the branch to [`.github/release-branches.txt`](/.github/release-branches.txt). That one file drives the
  scheduled checks below. Remove it from the file when the line is retired.
- The branch must contain the CI workflows. `pull_request` runs for PRs targeting `release/**` and `push` runs on
  `release/**` are enabled in every required workflow.

### What goes on a release branch

Every fix lands on `main` first, then is cherry-picked. The exception is a fix that only makes sense on the release
line (for example a revert of something that never reached `main`, or a pinned-dependency bump for a CVE that `main`
already fixed differently). Say so in the PR.

1. Merge the fix to `main` as usual (with its regression test).
2. Cherry-pick it onto a branch off the release branch, recording the original commit:

   ```sh
   git fetch origin
   git switch -c backport/<short-name> origin/release/v0.20.x
   git cherry-pick -x <main-commit-sha>
   ```

   `-x` appends `(cherry picked from commit <sha>)` to the message. Keep it; the release notes and audits rely on it.
3. Open a PR to `release/v0.20.x`. Title it as the original (`fix: ...`), put the original PR number in the body, and
   label it with the original PR (`backport: #NNNN`). The PR template risk tier is the original's tier; Tier 1 still
   needs human sign-off in a separate session.
4. The same rules as `main` apply: every required check green, no bypass of failing or pending checks, `--admin`
   merge only under the CLAUDE.md "Merging" conditions. Squash-merge so one backport is one commit.

If a cherry-pick conflicts, resolve it on the backport branch and say in the PR what differed from the original.
A conflict in data-path code is a signal to re-run the chaos suite deliberately, not just trust the green check.

Only fixes go on the release branch: no features, refactors or dependency churn beyond a security fix. Dependabot
only targets `main`; take dependency fixes the same way as any other fix (merge on `main`, cherry-pick).

### Forward-porting

The rule is the mirror image: nothing may exist on a release branch that is not on `main`, except release-only
changes. Before tagging, list what the branch carries that `main` does not:

```sh
git fetch origin
git log --oneline --no-merges origin/main..origin/release/v0.20.x
```

Every commit there is either a `cherry picked from commit` backport (already on `main`) or a release-only change that
the release issue explains. A release-only fix that also applies to `main` is forward-ported with a PR to `main`
(`git cherry-pick -x` in the other direction).

### Scheduled checks and the quiet window

GitHub only runs `schedule` triggers on the default branch, so chaos, govulncheck and the flake hunt would never run
against a release branch on their own.
[`scheduled-release-branches.yml`](/.github/workflows/scheduled-release-branches.yml) runs nightly (06:30 UTC), reads
the branch list from `.github/release-branches.txt`, dispatches `chaos.yml`, `govulncheck.yml` and `flake-hunt.yml`
(`workflow_dispatch`) against each listed branch, waits for them, and fails if any of them fails. The branch's own copy
of each workflow runs, so chaos keeps its always-report/fail-closed semantics (a dispatched run always executes the
suite). The workflows are not modified for this, so their required check names are unchanged. To run it by hand:
`gh workflow run scheduled-release-branches.yml -f ref=release/v0.20.x`.

**The quiet window is measured on the release branch.** Before tagging, the two most recent consecutive nightly runs
of the dispatcher must be green for the branch, with no commit landing on the branch between the first of those runs
and the tag. A green nightly on `main` says nothing about the release branch. Check it with:

```sh
for w in chaos.yml govulncheck.yml flake-hunt.yml; do
  gh run list --workflow "$w" --branch release/v0.20.x --event workflow_dispatch --limit 3 \
    --json conclusion,createdAt,headSha --jq '.[] | [.createdAt, .conclusion, .headSha[0:8]] | @tsv'
done
```

Merging a backport restarts the clock: the window needs two green nightlies on the SHA that will be tagged.

### Tagging from the release branch

1. Confirm the quiet window and the forward-port check above.
2. Tag the branch head: `git tag -a vX.Y.Z -m "vX.Y.Z" <sha> && git push origin vX.Y.Z`. The tag must point at a
   commit on `release/vX.Y.x`.
3. `release.yml` is unchanged by any of this. It triggers on the `v*` tag push, and the tag, not the branch, is what
   it builds and signs, so the cosign identity stays
   `.../.github/workflows/release.yml@refs/tags/vX.Y.Z` (see "Verifying a release"). Do not rename or move that
   workflow.
4. Do not merge anything to the branch between the quiet-window check and the tag push.

### Nightly builds on release branches

`trigger-nightly.yml` checks out the default branch, takes the highest non-nightly tag, bumps the minor and tags
`vX.(Y+1).0-nightly.YYYYMMDD`. Once `v0.20.x` exists, nightlies are therefore the **v0.21** train from `main`, and the
release branch gets no nightly build. That is intentional for now: `latest-nightly` and the keep-5 cleanup both assume
one nightly stream, and this change does not alter it.

Recommendation, not implemented: if we want the release pipeline itself (GoReleaser, signing, image build) exercised
from the release branch before a tag, make `trigger-nightly.yml` also tag the release branch head as
`vX.Y.(Z+1)-nightly.YYYYMMDD`. Doing that needs decisions first: whether `latest-nightly` keeps pointing at `main`
(release-branch nightlies would have to skip that tag in `release.yml`), and how the "keep latest 5" cleanup splits
between the two streams. Until then, the dispatcher's green runs are the quiet-window signal, and the first tag is the
first end-to-end run of `release.yml` from the branch.

## How to release

In order to create a new Conduit release, you'll need to create a new issue using the [Conduit release template](https://github.com/ConduitIO/conduit/issues/new?assignees=&labels=release&projects=&template=4-conduit-release.md&title=%5BRelease%5D+Conduit+vX.Y.Z).

The issue will guide you through the process of creating a new release.

It will also provide you with a checklist to make sure you don't forget anything.
