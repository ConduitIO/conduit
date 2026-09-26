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

In order to create a new Conduit release, you'll need to create a new issue using the [Conduit release template](https://github.com/ConduitIO/conduit/issues/new?assignees=&labels=release&projects=&template=4-conduit-release.yml&title=%5BRelease%5D+Conduit+vX.Y.Z).

The issue will guide you through the process of creating a new release.

It will also provide you with a checklist to make sure you don't forget anything.

## macOS signing and notarization

Through v0.19.0 the darwin binaries were **ad-hoc signed only**: `codesign -dv`
reports `Signature=adhoc` and `spctl -a -vv` reports `rejected`. A binary
downloaded through a browser carries `com.apple.quarantine`, so Gatekeeper
kills it with no output at all — exit 137, an empty terminal, no dialog. It is
invisible to CI and to `curl`-based testing because neither sets the quarantine
attribute, which is why it went unnoticed. Homebrew and `install.sh` are
unaffected for the same reason.

The release pipeline can now Developer ID-sign and notarize those binaries.
[quill](https://github.com/anchore/quill) does the work from the Linux release
runner, so no macOS runner is needed, and it is driven by
`scripts/sign-darwin.sh` as a GoReleaser post-build hook.

**Signing is off until the secrets below exist.** With `MACOS_SIGN_P12` unset
the hook logs why and exits 0, so the release behaves exactly as it does today.
Nightly tags always skip notarization: it costs an Apple round-trip of several
minutes, the nightly train runs daily, and nobody browser-downloads a nightly.

### Required repository secrets

These come from an **Apple Developer Program** membership (99 USD/year). There
is no way to notarize without one.

| Secret | What it is | Where it comes from |
| --- | --- | --- |
| `MACOS_SIGN_P12` | base64 of the **Developer ID Application** certificate + private key, exported as `.p12` | Apple Developer → Certificates → create a Developer ID Application cert, then export from Keychain Access |
| `MACOS_SIGN_P12_PASSWORD` | the password set on that `.p12` export | chosen at export time |
| `MACOS_NOTARY_KEY` | base64 of the App Store Connect API key (`.p8`) | App Store Connect → Users and Access → Integrations → Keys, role **Developer** |
| `MACOS_NOTARY_KEY_ID` | that key's Key ID | shown beside the key |
| `MACOS_NOTARY_ISSUER` | the Issuer ID (a UUID) | shown above the key list |

A **Developer ID Application** certificate is the specific type required.
"Apple Development" and "Apple Distribution" certificates cannot notarize
software distributed outside the App Store.

### Verifying a signed release

A bare Mach-O executable cannot be stapled — stapling needs a container
(`.app`, `.dmg`, `.pkg`). Apple publishes the notarization ticket and Gatekeeper
fetches it on first run, so a notarized CLI shipped in a tarball is accepted
given network access. That is the standard shape for a notarized command-line
tool, not a gap.

After a signed release, download the darwin tarball **through a browser** (a
`curl` download sets no quarantine attribute and will pass either way) and:

```sh
codesign -dv ./conduit          # expect: Authority=Developer ID Application: ...
spctl -a -vv ./conduit          # expect: accepted
xattr ./conduit                 # expect: com.apple.quarantine present
./conduit version               # expect: it runs
```

Before this change the last command produced no output and exit 137.
