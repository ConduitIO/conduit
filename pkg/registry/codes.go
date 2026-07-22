// Copyright © 2026 Meroxa, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package registry

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// Registry error codes — the "registry"-owned rows of the canonical
// registry error table (plan-v2 §4). Registered in full here, even though
// most are not triggered by any PR-0 code path yet (the install pipeline is
// PR-1; trust verification is PR-2) — plan-v2 §2.3 asks for the whole table
// to exist from the first PR, so docs/llms.txt generation has one complete,
// stable source of truth from day one, and so PR-1/PR-2 never have to
// invent a code plan-v2 already named.
//
// See pkg/registry/index's codes.go and pkg/registry/trust's codes.go for
// the "index"- and "trust"-owned rows respectively — those two packages
// register their own codes locally (rather than here) specifically to
// avoid an import cycle, since this package imports both of them.
var (
	// CodeConnectorNotFound is raised when an exact-match name lookup in
	// the index fails.
	CodeConnectorNotFound = conduiterr.Register("registry.connector_not_found", codes.NotFound)
	// CodeVersionNotFound is raised when the connector exists but the
	// requested @version does not.
	CodeVersionNotFound = conduiterr.Register("registry.version_not_found", codes.NotFound)
	// CodeIncompatibleVersion is raised when an explicit @version pin is
	// below the running Conduit's min-versions, or (see ManifestKey) when a
	// version string fails to parse as semver at all.
	CodeIncompatibleVersion = conduiterr.Register("registry.incompatible_version", codes.FailedPrecondition)
	// CodeNoPlatformArtifact is raised when no artifact exists for the
	// host (os, arch).
	CodeNoPlatformArtifact = conduiterr.Register("registry.no_platform_artifact", codes.NotFound)
	// CodeCorruptDownload is raised when the received-bytes sha256 does not
	// match the index's declared sha256 — DataLoss, matching gRPC's literal
	// "unrecoverable data loss or corruption" semantics for a digest
	// mismatch; a retry is a fresh download attempt, not a recovery of the
	// same lost bytes.
	CodeCorruptDownload = conduiterr.Register("registry.corrupt_download", codes.DataLoss)
	// CodeInstallLocked is raised when the per-target install lock is not
	// acquired within --lock-timeout.
	CodeInstallLocked = conduiterr.Register("registry.install_locked", codes.Unavailable)
	// CodeArchiveInvalid is raised when a downloaded archive has zero or
	// more than one candidate executable, a path-traversal entry, or a
	// symlink entry.
	CodeArchiveInvalid = conduiterr.Register("registry.archive_invalid", codes.Internal)
	// CodeVerificationUnavailable is raised by FailClosedVerifier — no
	// longer this package's production wiring as of PR-2 (see
	// TrustedVerifier), but still the correct, intentional behavior for any
	// caller that explicitly constructs Install with FailClosedVerifier
	// (e.g. a build with the trust core deliberately disabled), and for the
	// structural belt-and-suspenders refusal if a future ArtifactVerifier
	// implementation ever returns success without actually signing.
	CodeVerificationUnavailable = conduiterr.Register("registry.verification_unavailable", codes.Unimplemented)
	// CodeConnectorNotInstalled is raised on an uninstall/manifest lookup
	// miss.
	CodeConnectorNotInstalled = conduiterr.Register("registry.connector_not_installed", codes.NotFound)
	// CodeConnectorInUse is raised when an uninstall is refused because a
	// pipeline still references the exact name@version.
	CodeConnectorInUse = conduiterr.Register("registry.connector_in_use", codes.FailedPrecondition)
	// CodeAmbiguousUninstall is raised when "uninstall <name>" is given
	// with no version and more than one version of that name is installed
	// (§3.1 — a new behavior versus every original step-plan, introduced by
	// the manifest's name@version key change).
	CodeAmbiguousUninstall = conduiterr.Register("registry.ambiguous_uninstall", codes.InvalidArgument)
	// CodeBundleStale is raised when an offline --bundle is older than
	// maxStaleness and --allow-stale-bundle was not set.
	CodeBundleStale = conduiterr.Register("registry.bundle_stale", codes.FailedPrecondition)
	// CodeDownloadFailed is raised on a non-2xx artifact download, a
	// redirect loop, or a connection reset.
	CodeDownloadFailed = conduiterr.Register("registry.download_failed", codes.Unavailable)
)
