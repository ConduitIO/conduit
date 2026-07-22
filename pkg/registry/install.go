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
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry/boundedfetch"
	"github.com/conduitio/conduit/pkg/registry/index"
	"github.com/conduitio/conduit/pkg/registry/trust"
)

// DefaultIndexURL is the well-known registry index URL Install fetches from
// when the caller does not set InstallOptions.IndexFile (plan-v2 §8: the
// same GitHub Pages deployment that serves the human-facing site).
const DefaultIndexURL = "https://registry.conduit.io/index.json"

// MaxBundleBytes caps a fetched signature/provenance bundle fetch (P0-2
// item 1, plan-v2 §2.4): a Sigstore bundle with a cert chain and Rekor
// inclusion proof is normally a few KB; 1 MiB is generous headroom.
const MaxBundleBytes int64 = 1 * 1024 * 1024

// InstallSourceIndex is the only InstallOptions/ManifestEntry Source value
// this PR produces. "offline-bundle" is PR-4's scope (offline/air-gapped
// installs from a locally bundled index snapshot).
const InstallSourceIndex = "index"

// InstallOptions is Install's full configuration.
//
// IndexVerifier and ArtifactVerifier are REQUIRED, no-default constructor-
// style arguments (plan-v2 §2.2): the one production call site
// (cmd/conduit/root/connectors/install.go) passes FailClosedVerifier{} for
// both, explicitly and visibly, until PR-2 lands. Nothing in this package
// supplies a default — a nil verifier is a caller bug, not a "verification
// skipped" default, and Install.validate refuses it outright.
type InstallOptions struct {
	// Name is the connector name to install (exact match only).
	Name string
	// Version is an optional version constraint ("0.14.1" or "v0.14.1").
	// Empty selects the newest version compatible with
	// RunningConduitVersion/RunningProtocolVersion.
	Version string

	// ConnectorsPath is the standalone connectors directory
	// (--connectors.path) the binary is installed into, and under which
	// .registry/ bookkeeping lives.
	ConnectorsPath string

	// IndexURL is the index to fetch over HTTP(S). Ignored if IndexFile is
	// set.
	IndexURL string
	// IndexFile reads the index from a local path instead (offline mode);
	// takes priority over IndexURL when both are set.
	IndexFile string

	IndexVerifier    IndexVerifier
	ArtifactVerifier ArtifactVerifier

	// RunningConduitVersion/RunningProtocolVersion are this build's own
	// versions, compared against each candidate version's
	// minConduitVersion/minProtocolVersion (see resolve.go).
	RunningConduitVersion  string
	RunningProtocolVersion string

	// InstalledBy identifies the operator for the manifest entry and audit
	// event (best-effort; e.g. the OS user running the command).
	InstalledBy string

	// LockTimeout bounds how long Install waits to acquire the per-target
	// and manifest locks. Zero uses DefaultLockTimeout.
	LockTimeout time.Duration

	// DryRun performs resolution and platform selection only — no
	// download, no filesystem write under ConnectorsPath.
	DryRun bool

	// HTTPClient overrides the download/bundle-fetch client. nil uses a
	// redirect-bounded default (newDownloadClient). Primarily a test seam.
	HTTPClient *http.Client

	// GOOS/GOArch override runtime.GOOS/runtime.GOARCH for platform
	// selection — a test seam only, never a user-facing flag (installing
	// for a different host than the one running `install` is out of
	// scope).
	GOOS, GOArch string
}

func (o *InstallOptions) validate() error {
	if o.Name == "" {
		return conduiterr.New(conduiterr.CodeInvalidArgument, "connector name is required")
	}
	if o.ConnectorsPath == "" {
		return conduiterr.New(conduiterr.CodeInvalidArgument, "--connectors.path is required")
	}
	if o.IndexURL == "" && o.IndexFile == "" {
		return conduiterr.New(conduiterr.CodeInvalidArgument, "one of --index-url or --index-file is required")
	}
	if o.IndexVerifier == nil || o.ArtifactVerifier == nil {
		// Should never happen from cmd/conduit (its one call site always
		// passes FailClosedVerifier{} at minimum) — a nil verifier here is
		// a programming error, not a user-input problem, but it must still
		// fail closed rather than nil-pointer-panic deeper in the pipeline.
		return conduiterr.New(CodeVerificationUnavailable, "no verifier configured for this install")
	}
	return nil
}

func (o *InstallOptions) goos() string {
	if o.GOOS != "" {
		return o.GOOS
	}
	return runtime.GOOS
}

func (o *InstallOptions) goarch() string {
	if o.GOArch != "" {
		return o.GOArch
	}
	return runtime.GOARCH
}

func (o *InstallOptions) lockTimeout() time.Duration {
	if o.LockTimeout > 0 {
		return o.LockTimeout
	}
	return DefaultLockTimeout
}

func (o *InstallOptions) httpClient() *http.Client {
	if o.HTTPClient != nil {
		return o.HTTPClient
	}
	return newDownloadClient()
}

// InstallResult is Install's success-path result, also used (with DryRun or
// AlreadyInstalled set) for the two non-error early-exit paths.
type InstallResult struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`

	// Deprecated mirrors the resolved version's index.ConnectorVersion.Deprecated
	// flag — informational only; it never causes a refusal (see resolve.go).
	Deprecated bool `json:"deprecated,omitempty"`

	DryRun      bool   `json:"dryRun,omitempty"`
	ArtifactURL string `json:"artifactUrl,omitempty"`
	Size        int64  `json:"size,omitempty"`

	AlreadyInstalled bool `json:"alreadyInstalled,omitempty"`

	ArtifactFile       string `json:"artifactFile,omitempty"`
	Digest             string `json:"digest,omitempty"`
	SourceIndexVersion int64  `json:"sourceIndexVersion,omitempty"`
}

// Install runs the full non-crypto install pipeline: fetch+shape-check the
// index, resolve name/version, select a host platform artifact, download it
// into a private per-install staging directory, check byte-for-byte
// integrity, run the verification gate, then — only past every one of
// those — atomically install the binary and record the manifest/audit
// entries.
//
// # Fail-closed by construction
//
// Step 6 below (ArtifactVerifier.VerifyArtifact) is the ONLY authorization
// gate between an integrity-checked download and an installed, runnable
// binary. With the production FailClosedVerifier (verify.go), this call
// ALWAYS returns ErrVerificationNotConfigured — so no code path in this
// function can reach extraction, the final rename, or a manifest write in a
// normal build. This is verified directly by install_test.go's
// TestInstall_FailClosedByConstruction (using FailClosedVerifier, the exact
// production wiring cmd/conduit/root/connectors/install.go uses) alongside
// TestInstall_FullPipeline (using a test-only pass-through verifier
// injected via this same InstallOptions field — never a separate code
// path — to exercise resolve/download/stage/sha256/manifest end to end).
func Install(ctx context.Context, opts InstallOptions) (*InstallResult, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}

	// 1-3: fetch + shape-check the index, resolve name/version, select a
	// host platform artifact.
	verified, resolved, artifact, err := resolveInstall(ctx, opts)
	if err != nil {
		return nil, err
	}

	if opts.DryRun {
		return &InstallResult{
			Name: resolved.Connector.Name, Version: resolved.Version.Version,
			OS: artifact.OS, Arch: artifact.Arch,
			Deprecated:  resolved.Version.Deprecated,
			DryRun:      true,
			ArtifactURL: artifact.URL,
			Size:        artifact.Size,
		}, nil
	}

	// 4-9: lock, download, verify, extract, atomically install, record.
	return installResolved(ctx, opts, verified, resolved, artifact)
}

// resolveInstall runs steps 1-3: fetch+shape-check the index (via
// IndexVerifier), resolve name/version, and select the host platform
// artifact. No filesystem write under opts.ConnectorsPath happens here —
// this is exactly what --dry-run stops after.
func resolveInstall(ctx context.Context, opts InstallOptions) (*index.VerifiedIndex, *ResolvedVersion, *index.Artifact, error) {
	raw, err := fetchIndexRaw(ctx, opts)
	if err != nil {
		return nil, nil, nil, err
	}
	verified, err := opts.IndexVerifier.VerifyIndex(ctx, raw)
	if err != nil {
		return nil, nil, nil, err
	}

	resolved, err := Resolve(verified.Payload, ResolveOptions{
		Name:                   opts.Name,
		Version:                opts.Version,
		RunningConduitVersion:  opts.RunningConduitVersion,
		RunningProtocolVersion: opts.RunningProtocolVersion,
	})
	if err != nil {
		return nil, nil, nil, err
	}

	artifact, err := SelectArtifact(resolved.Connector.Name, resolved.Version, opts.goos(), opts.goarch())
	if err != nil {
		return nil, nil, nil, err
	}
	return verified, resolved, artifact, nil
}

// installResolved runs steps 4-9 of Install against an already-resolved
// connector version and platform artifact: acquire the per-target lock,
// short-circuit if already installed, download+verify+extract+atomically
// install, then record the manifest entry and audit event.
func installResolved(ctx context.Context, opts InstallOptions, verified *index.VerifiedIndex, resolved *ResolvedVersion, artifact *index.Artifact) (*InstallResult, error) {
	key, err := ManifestKey(resolved.Connector.Name, resolved.Version.Version)
	if err != nil {
		return nil, err
	}

	// 4. Per-target lock: serializes the ENTIRE remaining pipeline for two
	// concurrent installs of the same connector name.
	targetLock, err := AcquireTargetLock(opts.ConnectorsPath, resolved.Connector.Name, opts.lockTimeout())
	if err != nil {
		return nil, err
	}
	defer targetLock.Unlock() //nolint:errcheck // best-effort; flock also releases at process exit

	// Re-check "already installed" AFTER acquiring the lock, not before —
	// a concurrent install of the same name that started earlier may have
	// just finished; reading the manifest before the lock could observe a
	// write in flight and give a stale answer.
	entry, alreadyInstalled, err := lookupManifestEntry(opts.ConnectorsPath, key)
	if err != nil {
		return nil, err
	}
	if alreadyInstalled {
		return &InstallResult{
			Name: entry.Name, Version: entry.Version, OS: entry.OS, Arch: entry.Arch,
			AlreadyInstalled:   true,
			ArtifactFile:       entry.ArtifactFile,
			Digest:             entry.Digest,
			SourceIndexVersion: entry.SourceIndexVersion,
		}, nil
	}

	entry, err = downloadVerifyAndInstall(ctx, opts, verified, resolved, artifact)
	if err != nil {
		return nil, err
	}

	if err := writeManifestEntry(opts.ConnectorsPath, key, entry, opts.lockTimeout()); err != nil {
		return nil, err
	}

	// 9. Audit event — appended only AFTER the rename and manifest write
	// above have both succeeded (see AppendAuditEvent's invariant comment).
	if err := AppendAuditEvent(auditLogPath(opts.ConnectorsPath), AuditEvent{
		Event: "connector_install", Connector: entry.Name, Version: entry.Version,
		Digest: entry.Digest, Operator: opts.InstalledBy, Timestamp: entry.InstalledAt,
		Signed: entry.Signed, VerifiedIdentity: entry.VerifiedIdentity, AllowUnsigned: entry.AllowUnsigned,
	}); err != nil {
		return nil, conduiterr.Wrap(conduiterr.CodeInternal, "connector installed but could not append the audit log entry", err)
	}

	return &InstallResult{
		Name: entry.Name, Version: entry.Version, OS: entry.OS, Arch: entry.Arch,
		Deprecated:         resolved.Version.Deprecated,
		ArtifactFile:       entry.ArtifactFile,
		Digest:             entry.Digest,
		SourceIndexVersion: entry.SourceIndexVersion,
	}, nil
}

// downloadVerifyAndInstall runs steps 5-8: stage, download, corruption
// check, the verification gate, extract, and the atomic rename — returning
// the ManifestEntry to record (steps 8-9's write/audit stay in
// installResolved, since they're identical regardless of how the entry was
// produced).
func downloadVerifyAndInstall(ctx context.Context, opts InstallOptions, verified *index.VerifiedIndex, resolved *ResolvedVersion, artifact *index.Artifact) (ManifestEntry, error) {
	// 5. Staging directory: a private (0700), uniquely-named SUBDIRECTORY
	// of ConnectorsPath — never the OS temp dir. This is required, not
	// just tidy, so the final os.Rename below is a same-filesystem,
	// therefore atomic, rename: a temp directory on a different volume
	// would make that rename a non-atomic copy and could fail outright
	// with EXDEV.
	stagingRoot := stagingRootPath(opts.ConnectorsPath)
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		return ManifestEntry{}, conduiterr.Wrap(CodeDownloadFailed, "could not create staging root directory", err)
	}
	stagingDir, err := os.MkdirTemp(stagingRoot, "install-*")
	if err != nil {
		return ManifestEntry{}, conduiterr.Wrap(CodeDownloadFailed, "could not create staging directory", err)
	}
	// Cleaned up on EVERY exit path: on success the binary has already
	// been renamed OUT of stagingDir, leaving only an empty extracted/
	// tree; on any failure, nothing under ConnectorsPath outside
	// .registry/staging was ever touched, and this removes the evidence of
	// the attempt so a stale staging directory never accumulates.
	defer os.RemoveAll(stagingDir)
	if err := os.Chmod(stagingDir, 0o700); err != nil {
		// Defensive: os.MkdirTemp already creates 0700 before umask: this
		// asserts that property rather than relying on it silently.
		return ManifestEntry{}, conduiterr.Wrap(CodeDownloadFailed, "could not set staging directory permissions", err)
	}

	archivePath := filepath.Join(stagingDir, "artifact.tar.gz")
	dl, err := Download(ctx, opts.httpClient(), artifact.URL, archivePath, artifact.Size)
	if err != nil {
		return ManifestEntry{}, err
	}
	fireChaos(chaosPointDownloadComplete)

	// 6a. Corruption check — integrity, not trust (corruption.go). Run
	// FIRST and SEPARATELY from the verification gate below.
	if err := CheckCorruption(dl.Digest, artifact.SHA256); err != nil {
		return ManifestEntry{}, err
	}

	verifyResult, err := runVerificationGate(ctx, opts, dl, resolved, artifact)
	if err != nil {
		return ManifestEntry{}, err
	}

	binaryPath, err := extractAndGuard(archivePath, stagingDir)
	if err != nil {
		return ManifestEntry{}, err
	}

	finalName := fmt.Sprintf("conduit-connector-%s_%s", resolved.Connector.Name, resolved.Version.Version)
	finalPath := filepath.Join(opts.ConnectorsPath, finalName)

	// Invariant 5 (atomic state/checkpoint writes): os.Rename within one
	// filesystem is a single atomic syscall — there is no OS-observable
	// "mid-rename" state. A crash before it returns leaves the OLD binary
	// (if any) untouched at finalPath; a crash after leaves the NEW binary
	// fully present. This is exactly why staging must share a filesystem
	// with ConnectorsPath (see the MkdirTemp call above) — the guarantee
	// does not hold across devices (EXDEV).
	if err := os.Rename(binaryPath, finalPath); err != nil {
		return ManifestEntry{}, conduiterr.Wrap(CodeArchiveInvalid, fmt.Sprintf("could not install %q", finalName), err)
	}
	if err := os.Chmod(finalPath, 0o755); err != nil {
		return ManifestEntry{}, conduiterr.Wrap(CodeArchiveInvalid, "could not set installed binary permissions", err)
	}
	fireChaos(chaosPointPostRenamePreManifest)

	now := time.Now().UTC()
	digestHex := fmt.Sprintf("%x", dl.Digest)
	return ManifestEntry{
		Name: resolved.Connector.Name, Version: resolved.Version.Version,
		Kind: StandaloneArtifactKind, OS: artifact.OS, Arch: artifact.Arch,
		ArtifactFile: finalName, Digest: "sha256:" + digestHex, Size: dl.Size,
		InstalledAt: now, InstalledBy: opts.InstalledBy,
		SourceIndexVersion: verified.Payload.Index.Version, Source: InstallSourceIndex,
		Signed: verifyResult.Signed, VerifiedIdentity: verifyResult.VerifiedIdentity, AllowUnsigned: false,
	}, nil
}

// runVerificationGate is step 6b: fetch the signature/provenance bundles
// (bounded, P0-2) and hand the corruption-checked, in-memory digest — never
// a path — to ArtifactVerifier.
//
// Invariant (fail-closed by construction): with the production
// FailClosedVerifier, VerifyArtifact ALWAYS returns
// ErrVerificationNotConfigured — nothing past this function executes in a
// normal build.
func runVerificationGate(ctx context.Context, opts InstallOptions, dl DownloadResult, resolved *ResolvedVersion, artifact *index.Artifact) (VerifyResult, error) {
	ref, err := fetchArtifactRef(ctx, opts, dl.Digest, resolved.Version, artifact)
	if err != nil {
		return VerifyResult{}, err
	}
	identity := trust.PinnedIdentity{
		OIDCIssuer:      resolved.Connector.Publisher.ExpectedOIDCIssuer,
		IdentityPattern: resolved.Connector.Publisher.ExpectedIdentityPattern,
	}

	verifyResult, err := opts.ArtifactVerifier.VerifyArtifact(ctx, ref, identity)
	if err != nil {
		return VerifyResult{}, err
	}
	// Second, structural belt-and-suspenders check (plan-v2 §2.2): this PR
	// has no --allow-unsigned wiring at all (that lands in PR-2), so any
	// non-error VerifyResult that isn't Signed would mean some future
	// ArtifactVerifier implementation returned success without actually
	// authorizing the artifact — refuse rather than silently install.
	if !verifyResult.Signed {
		return VerifyResult{}, conduiterr.New(CodeVerificationUnavailable,
			"artifact verifier returned success without signing the artifact — refusing to install "+
				"(no --allow-unsigned path exists in this build)")
	}
	return verifyResult, nil
}

// extractAndGuard is step 7: extract the single candidate binary, then
// verify-via-O_NOFOLLOW-fd immediately before the caller's rename.
//
// Invariant: the O_NOFOLLOW-opened fd protects the FINAL RENAME (in the
// caller) from a symlink swapped into extractDir's binary name between
// ExtractBinary returning and os.Rename running. It does NOT re-verify the
// artifact's trust or contents — that already happened in
// runVerificationGate, over an in-memory digest computed from the
// downloaded bytes directly, never re-read from a path. ExtractBinary
// itself already refused any symlink/hardlink TAR ENTRY outright, and
// extractDir is private (0700), uniquely named, and freshly created by this
// call, so the realistic TOCTOU window here is narrow — but the check is
// real: an O_NOFOLLOW open of a symlink fails with ELOOP (unix) rather than
// silently following it.
func extractAndGuard(archivePath, stagingDir string) (string, error) {
	extractDir := filepath.Join(stagingDir, "extracted")
	if err := os.MkdirAll(extractDir, 0o700); err != nil {
		return "", conduiterr.Wrap(CodeArchiveInvalid, "could not create extraction directory", err)
	}
	binaryPath, err := ExtractBinary(archivePath, extractDir)
	if err != nil {
		return "", err
	}
	fireChaos(chaosPointExtractComplete)

	guardFD, err := openRegularNoFollow(binaryPath)
	if err != nil {
		return "", err
	}
	fireChaos(chaosPointPrerenameFDOpened)
	guardFD.Close()

	return binaryPath, nil
}

func fetchIndexRaw(ctx context.Context, opts InstallOptions) ([]byte, error) {
	if opts.IndexFile != "" {
		return index.FetchFile(opts.IndexFile)
	}
	return index.Fetch(ctx, opts.IndexURL)
}

// fetchArtifactRef fetches the signature bundle (always expected) and the
// applicable SLSA provenance bundle (version-level or artifact-level,
// whichever is present) into an ArtifactRef, bounded per MaxBundleBytes —
// this package never hands ArtifactVerifier a bare URL; it fetches the
// bytes itself so the interface stays crypto-library-agnostic.
func fetchArtifactRef(ctx context.Context, opts InstallOptions, digest [32]byte, v index.ConnectorVersion, a *index.Artifact) (ArtifactRef, error) {
	ref := ArtifactRef{Digest: digest}

	if a.Signature.BundleURL != "" {
		sig, err := fetchBundle(ctx, a.Signature.BundleURL)
		if err != nil {
			return ArtifactRef{}, err
		}
		ref.SignatureBundle = sig
	}

	provRef := a.SLSAProvenance
	if provRef == nil {
		provRef = v.SLSAProvenance
	}
	if provRef != nil && provRef.BundleURL != "" {
		prov, err := fetchBundle(ctx, provRef.BundleURL)
		if err != nil {
			return ArtifactRef{}, err
		}
		ref.ProvenanceBundle = prov
	}

	return ref, nil
}

func fetchBundle(ctx context.Context, url string) ([]byte, error) {
	data, err := boundedfetch.Fetch(ctx, url, MaxBundleBytes)
	if err != nil {
		if cerrors.Is(err, boundedfetch.ErrTooLarge) {
			return nil, conduiterr.Wrap(trust.CodeBundleTooLarge, "signature/provenance bundle exceeds the maximum allowed size", err)
		}
		return nil, conduiterr.Wrap(CodeDownloadFailed, fmt.Sprintf("could not fetch bundle %q", url), err)
	}
	return data, nil
}

func lookupManifestEntry(connectorsPath, key string) (ManifestEntry, bool, error) {
	m, err := LoadManifest(manifestPath(connectorsPath))
	if err != nil {
		return ManifestEntry{}, false, conduiterr.Wrap(conduiterr.CodeInternal, "could not read install manifest", err)
	}
	entry, ok := m.Installs[key]
	return entry, ok, nil
}

func writeManifestEntry(connectorsPath, key string, entry ManifestEntry, lockTimeout time.Duration) error {
	lock, err := AcquireManifestLock(connectorsPath, lockTimeout)
	if err != nil {
		return err
	}
	defer lock.Unlock() //nolint:errcheck // best-effort; flock also releases at process exit

	path := manifestPath(connectorsPath)
	m, err := LoadManifest(path)
	if err != nil {
		return conduiterr.Wrap(conduiterr.CodeInternal, "could not read install manifest", err)
	}
	if m.Installs == nil {
		m.Installs = map[string]ManifestEntry{}
	}
	m.Installs[key] = entry
	if err := SaveManifest(path, m); err != nil {
		return conduiterr.Wrap(conduiterr.CodeInternal, "could not write install manifest", err)
	}
	return nil
}
