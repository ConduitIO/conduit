#!/usr/bin/env bash
# Verify that the release image for a stable tag exists and is signed by the
# release workflow for exactly that tag. Exits nonzero if either check fails.
#
#   .github/scripts/verify_release_image.sh v0.21.0
#
# Needs docker (buildx) and cosign. Verifies the multi-arch index by DIGEST, never
# by tag: a tag can move, a digest cannot (the same reason release.yml signs by
# digest). The certificate identity is the Release workflow at this very tag, so a
# signature from another workflow, branch or tag does not pass.
set -euo pipefail

tag="${1:?usage: verify_release_image.sh vX.Y.Z}"
if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "::error::'$tag' is not a stable release tag (vX.Y.Z)" >&2
  exit 1
fi

image="ghcr.io/conduitio/conduit"
digest="$(docker buildx imagetools inspect "$image:$tag" --format '{{.Manifest.Digest}}')"
if [[ ! "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "::error::could not resolve a digest for $image:$tag (got '$digest')" >&2
  exit 1
fi

cosign verify "$image@$digest" \
  --certificate-identity "https://github.com/ConduitIO/conduit/.github/workflows/release.yml@refs/tags/$tag" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com >/dev/null

echo "verified $image:$tag -> $digest"
