#!/usr/bin/env bash
#
# Developer ID-sign and notarize a darwin build artifact.
#
# Runs as a GoReleaser post-build hook, once per build target. It is a no-op
# unless all of the following hold, so the release pipeline behaves exactly as
# it does today whenever signing is not configured:
#
#   * the target is darwin (nothing else can be notarized)
#   * the tag is not a nightly — notarization costs an Apple round-trip of
#     several minutes, the nightly train runs daily, and nobody
#     browser-downloads a nightly
#   * QUILL_SIGN_P12 is set (the Developer ID Application certificate)
#
# Why this exists: through v0.19.0 the darwin binaries were ad-hoc signed only
# (`codesign -dv` reports `Signature=adhoc`, `spctl -a` reports `rejected`). A
# browser download carries com.apple.quarantine, and Gatekeeper then kills the
# binary with no output whatsoever — exit 137, empty terminal, no dialog, no
# reason to search for. Homebrew and install.sh are unaffected because neither
# sets the quarantine attribute, which is exactly why this stayed invisible to
# CI and to curl-based testing.
#
# Note on stapling: a bare Mach-O executable cannot be stapled — stapling
# requires a container (.app/.dmg/.pkg). The notarization ticket is published
# by Apple and Gatekeeper fetches it on first run, so a notarized CLI in a
# tarball is accepted with network access. That is the standard shape for a
# notarized command-line tool and is not a gap in this script.
set -euo pipefail

binary="${1:?usage: sign-darwin.sh <binary> <target>}"
target="${2:?usage: sign-darwin.sh <binary> <target>}"

case "$target" in
  darwin_*) ;;
  *) exit 0 ;;
esac

if [[ "${GORELEASER_CURRENT_TAG:-}" == *nightly* ]]; then
  echo "sign-darwin: ${GORELEASER_CURRENT_TAG} is a nightly tag, skipping notarization"
  exit 0
fi

if [[ -z "${QUILL_SIGN_P12:-}" ]]; then
  echo "sign-darwin: QUILL_SIGN_P12 is unset — leaving ${binary} ad-hoc signed." >&2
  echo "sign-darwin: browser-downloaded macOS binaries will be quarantined." >&2
  exit 0
fi

echo "sign-darwin: signing and notarizing ${binary} (${target})"
quill sign-and-notarize "${binary}" --dry-run=false --ad-hoc=false -vv
