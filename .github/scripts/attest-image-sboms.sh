#!/usr/bin/env bash
# Generate one SPDX SBOM per platform of a multi-arch image and attach each to
# its platform digest as a signed cosign attestation (predicate type spdxjson).
#
# Usage: attest-image-sboms.sh <repository> <index digest> <platforms>
#   e.g. attest-image-sboms.sh ghcr.io/conduitio/conduit sha256:... linux/amd64,linux/arm64
#
# Why per platform: syft pointed at a multi-arch index catalogs only the one
# platform it resolves to, so an index-level SBOM would describe one image and
# be attached to all of them. Here each SBOM describes exactly the digest it is
# attached to.
#
# Extra cosign flags can be passed in COSIGN_ATTEST_ARGS (word-split), which
# is how the script is exercised locally with a key against a test registry.
# In release.yml it is empty: keyless signing with the workflow's OIDC token.
#
# Fails before attesting anything if the index's platforms are not exactly
# <platforms>, if a digest is malformed, or if an SBOM comes out empty.
set -euo pipefail

repo="${1:?repository}"
index="${2:?index digest}"
want="${3:?platforms, comma-separated}"
digest_re='^sha256:[0-9a-f]{64}$'

if [[ ! "$index" =~ $digest_re ]]; then
  echo "unexpected index digest: '$index'" >&2
  exit 1
fi

# "<os>/<arch>[/<variant>] <digest>" for each platform manifest. BuildKit also
# lists its own attestation manifests in the index (platform unknown/unknown,
# vnd.docker.reference.type attestation-manifest); those are not images and
# get no SBOM.
mapfile -t platforms < <(
  docker buildx imagetools inspect --raw "${repo}@${index}" | jq -r '
    .manifests[]
    | select(.annotations["vnd.docker.reference.type"] != "attestation-manifest")
    | select(.platform.os != "unknown")
    | "\(.platform.os)/\(.platform.architecture)\(if .platform.variant then "/" + .platform.variant else "" end) \(.digest)"' \
  | sort
)

got=$(printf '%s\n' "${platforms[@]%% *}" | paste -sd, -)
want=$(tr ',' '\n' <<<"$want" | sort | paste -sd, -)
if [[ "$got" != "$want" ]]; then
  echo "platforms in ${repo}@${index} are '$got', expected '$want'" >&2
  exit 1
fi

# Generate and check every SBOM before attesting any, so a scan failure
# leaves no partial set of attestations behind.
for entry in "${platforms[@]}"; do
  read -r platform digest <<<"$entry"
  file="sbom-${platform//\//-}.spdx.json"
  if [[ ! "$digest" =~ $digest_re || ! "$platform" =~ ^[a-z0-9/]+$ ]]; then
    echo "unexpected platform entry: '$entry'" >&2
    exit 1
  fi
  syft scan "registry:${repo}@${digest}" --output "spdx-json=${file}"
  packages=$(jq '.packages | length' "$file")
  if [[ "$packages" -lt 1 ]]; then
    echo "SBOM for $platform ($digest) lists no packages" >&2
    exit 1
  fi
  echo "$platform $digest: $packages packages"
done

for entry in "${platforms[@]}"; do
  read -r platform digest <<<"$entry"
  # shellcheck disable=SC2086 # COSIGN_ATTEST_ARGS is deliberately word-split
  cosign attest --yes ${COSIGN_ATTEST_ARGS:-} \
    --type spdxjson \
    --predicate "sbom-${platform//\//-}.spdx.json" \
    "${repo}@${digest}"
done
