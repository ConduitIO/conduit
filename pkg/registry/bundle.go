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

// This file implements the offline/air-gapped install path (plan-v2/step5
// §7): Bundle prepares a self-contained tarball on a networked machine,
// running the full real install-equivalent verification before ever writing
// the tarball; InstallFromBundle installs from that tarball on a machine
// with NO network access at all, re-verifying everything against the
// offline binary's own compiled-in trust anchors — it NEVER skips
// verification just because the network is down.
//
// # Soundness
//
// A bundle is a carrier for an already-completed, already-verified
// installation, replayed later on a different machine — never a way to
// defer verification to a machine that can't fully perform it. Bundle
// itself refuses to package an artifact that would not itself pass a normal
// `install` (VerifyArtifact must return Signed: true); InstallFromBundle
// re-runs index verification (via the SAME registry.TrustedVerifier code
// path a live fetch uses — index verification has no network dependency of
// its own once the bytes are in hand) and artifact signature/provenance
// verification (via the same sigstore-go `--bundle` offline verification
// the online path already uses — see trust/sigstore.go's doc comments: it
// never makes a live Fulcio/Rekor query, bundle or not).
package registry

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	json "github.com/goccy/go-json"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry/index"
	"github.com/conduitio/conduit/pkg/registry/policy"
	"github.com/conduitio/conduit/pkg/registry/trust"
)

// BundleFormatVersion is the bundleFormatVersion this build writes and the
// maximum InstallFromBundle accepts — its own small compat field, same
// announce/warn/remove discipline as the index's schemaVersion: a too-new
// bundle format on an old conduit binary says "upgrade conduit," never
// mis-parses.
const BundleFormatVersion = 1

// InstallSourceOfflineBundle is the ManifestEntry.Source value recorded by
// InstallFromBundle — see InstallSourceIndex (install.go) for the online
// counterpart.
const InstallSourceOfflineBundle = "offline-bundle"

// maxBundleTarEntryBytes bounds any single entry InstallFromBundle reads out
// of a bundle tarball — generous for a single connector artifact plus its
// small signature/provenance bundles and index snapshot, and a hard refusal
// (never a silent truncation) if exceeded, mirroring extract.go's
// decompression-bomb guard for the same reason.
const maxBundleTarEntryBytes int64 = 256 * 1024 * 1024 // 256 MiB

// StaleBundleEnvVar is the non-interactive escape hatch for
// --allow-stale-bundle (gated identically to --allow-unsigned per DeVaris's
// ratification, plan-v2/step5 §7 step 3, §9 decision item 2).
const StaleBundleEnvVar = "CONDUIT_ALLOW_STALE_BUNDLE"

// BundleManifest is the small manifest.json entry inside a bundle tarball —
// distinct from (and much smaller than) registry.ManifestEntry, the install
// manifest's own per-connector record.
type BundleManifest struct {
	BundleFormatVersion int       `json:"bundleFormatVersion"`
	Name                string    `json:"name"`
	Version             string    `json:"version"`
	OS                  string    `json:"os"`
	Arch                string    `json:"arch"`
	SHA256              string    `json:"sha256"`
	Size                int64     `json:"size"`
	CreatedAt           time.Time `json:"createdAt"`
}

// BundleOptions is Bundle's configuration.
type BundleOptions struct {
	Name       string
	Version    string
	OutputPath string

	IndexURL  string
	IndexFile string

	GOOS, GOArch string

	// IndexVerifier/ArtifactVerifier are REQUIRED (mirroring
	// InstallOptions) — bundling is a normal, fully-verified install whose
	// output is redirected into a tarball instead of --connectors.path; it
	// is never a trust shortcut, so there is no FailClosedVerifier-style
	// default here either.
	IndexVerifier    IndexVerifier
	ArtifactVerifier ArtifactVerifier

	RunningConduitVersion  string
	RunningProtocolVersion string

	HTTPClient *http.Client
}

// BundleResult is Bundle's success-path result.
type BundleResult struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	OutputPath string `json:"outputPath"`
	Digest     string `json:"digest"`
	Size       int64  `json:"size"`
}

// Bundle prepares an offline install bundle: fetch+verify the index
// (identical to install.go's own pipeline), resolve name/version, select
// the host-requested platform artifact, download and corruption-check it,
// fetch its signature/provenance bundles, and run FULL identity-pinned
// verification — all while online — before packaging everything (including
// the complete signed index snapshot, so the offline machine can itself
// re-check yank/revocation status) into a single tarball.
func Bundle(ctx context.Context, opts BundleOptions) (*BundleResult, error) {
	if opts.Name == "" {
		return nil, conduiterr.New(conduiterr.CodeInvalidArgument, "connector name is required")
	}
	if opts.OutputPath == "" {
		return nil, conduiterr.New(conduiterr.CodeInvalidArgument, "--output is required")
	}
	if opts.IndexVerifier == nil || opts.ArtifactVerifier == nil {
		return nil, conduiterr.New(CodeVerificationUnavailable, "no verifier configured for bundle prep")
	}

	raw, err := fetchIndexRawFrom(ctx, opts.IndexURL, opts.IndexFile)
	if err != nil {
		return nil, err
	}
	verified, err := opts.IndexVerifier.VerifyIndex(ctx, raw)
	if err != nil {
		return nil, err
	}
	if !verified.Verified {
		return nil, conduiterr.New(CodeVerificationUnavailable,
			"the registry index could not be cryptographically verified — refusing to bundle from untrusted data")
	}

	goos, goarch := opts.GOOS, opts.GOArch
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}

	resolved, err := Resolve(verified.Payload, ResolveOptions{
		Name: opts.Name, Version: opts.Version,
		RunningConduitVersion: opts.RunningConduitVersion, RunningProtocolVersion: opts.RunningProtocolVersion,
	})
	if err != nil {
		return nil, err
	}
	artifact, err := SelectArtifact(resolved.Connector.Name, resolved.Version, goos, goarch)
	if err != nil {
		return nil, err
	}

	tmpDir, err := os.MkdirTemp("", "conduit-bundle-*")
	if err != nil {
		return nil, conduiterr.Wrap(CodeArchiveInvalid, "could not create a temp directory for bundle prep", err)
	}
	defer os.RemoveAll(tmpDir)

	client := opts.HTTPClient
	if client == nil {
		client = newDownloadClient()
	}

	archivePath := filepath.Join(tmpDir, "artifact.tar.gz")
	dl, err := Download(ctx, client, artifact.URL, archivePath, artifact.Size)
	if err != nil {
		return nil, err
	}
	if err := CheckCorruption(dl.Digest, artifact.SHA256); err != nil {
		return nil, err
	}

	// fetchArtifactRef's InstallOptions parameter carries no fields it
	// actually reads (bundle fetches always go through boundedfetch's own
	// http.DefaultClient) — passed as a zero value deliberately, not an
	// oversight.
	ref, err := fetchArtifactRef(ctx, InstallOptions{}, dl.Digest, resolved.Version, artifact)
	if err != nil {
		return nil, err
	}

	identity := trust.PinnedIdentity{
		OIDCIssuer:      resolved.Connector.Publisher.ExpectedOIDCIssuer,
		IdentityPattern: resolved.Connector.Publisher.ExpectedIdentityPattern,
	}
	verifyResult, err := opts.ArtifactVerifier.VerifyArtifact(ctx, ref, identity)
	if err != nil {
		return nil, err
	}
	if !verifyResult.Signed {
		return nil, conduiterr.New(CodeVerificationUnavailable,
			"bundle prep requires a fully verified (signed) artifact — refusing to package an unsigned artifact "+
				"(a bundle is a carrier for an already-verified install, never a way to defer verification)")
	}

	manifest := BundleManifest{
		BundleFormatVersion: BundleFormatVersion,
		Name:                resolved.Connector.Name,
		Version:             resolved.Version.Version,
		OS:                  artifact.OS,
		Arch:                artifact.Arch,
		SHA256:              fmt.Sprintf("sha256:%x", dl.Digest),
		Size:                dl.Size,
		CreatedAt:           time.Now().UTC(),
	}

	if err := writeBundleTar(opts.OutputPath, manifest, archivePath, ref.SignatureBundle, ref.ProvenanceBundle, raw); err != nil {
		return nil, err
	}

	return &BundleResult{
		Name: manifest.Name, Version: manifest.Version, OS: manifest.OS, Arch: manifest.Arch,
		OutputPath: opts.OutputPath, Digest: manifest.SHA256, Size: manifest.Size,
	}, nil
}

func writeBundleTar(outputPath string, manifest BundleManifest, archivePath string, signatureBundle, provenanceBundle, indexSnapshotRaw []byte) error {
	out, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return conduiterr.Wrap(CodeArchiveInvalid, "could not create bundle output file", err)
	}
	defer out.Close()

	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)

	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return conduiterr.Wrap(conduiterr.CodeInternal, "could not marshal bundle manifest", err)
	}
	if err := addTarFile(tw, "manifest.json", manifestBytes); err != nil {
		return err
	}

	artifactBytes, err := os.ReadFile(archivePath)
	if err != nil {
		return conduiterr.Wrap(CodeArchiveInvalid, "could not read staged artifact for bundling", err)
	}
	if err := addTarFile(tw, "artifact", artifactBytes); err != nil {
		return err
	}

	if len(signatureBundle) > 0 {
		if err := addTarFile(tw, "signature.bundle", signatureBundle); err != nil {
			return err
		}
	}
	if len(provenanceBundle) > 0 {
		if err := addTarFile(tw, "provenance.bundle", provenanceBundle); err != nil {
			return err
		}
	}
	if err := addTarFile(tw, "index-snapshot.json", indexSnapshotRaw); err != nil {
		return err
	}

	if err := tw.Close(); err != nil {
		return conduiterr.Wrap(CodeArchiveInvalid, "could not finalize bundle tar", err)
	}
	if err := gz.Close(); err != nil {
		return conduiterr.Wrap(CodeArchiveInvalid, "could not finalize bundle gzip", err)
	}
	return nil
}

func addTarFile(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(data))}
	if err := tw.WriteHeader(hdr); err != nil {
		return conduiterr.Wrap(CodeArchiveInvalid, "could not write bundle tar header for "+name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return conduiterr.Wrap(CodeArchiveInvalid, "could not write bundle tar entry for "+name, err)
	}
	return nil
}

// readBundleTar extracts every entry (bounded per maxBundleTarEntryBytes)
// from a bundle tarball into memory — bundles are single-connector-sized,
// never the multi-hundred-MB scale that would make this unreasonable.
func readBundleTar(bundlePath string) (manifest BundleManifest, artifactBytes, signatureBundle, provenanceBundle, indexSnapshotRaw []byte, err error) {
	f, err := os.Open(bundlePath)
	if err != nil {
		return BundleManifest{}, nil, nil, nil, nil, conduiterr.Wrap(CodeArchiveInvalid, "could not open bundle file", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return BundleManifest{}, nil, nil, nil, nil, conduiterr.Wrap(CodeArchiveInvalid, "bundle is not valid gzip", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	var manifestBytes []byte
	for {
		hdr, terr := tr.Next()
		if terr == io.EOF {
			break
		}
		if terr != nil {
			return BundleManifest{}, nil, nil, nil, nil, conduiterr.Wrap(CodeArchiveInvalid, "bundle archive is corrupt", terr)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		data, rerr := readCappedTarEntry(tr, maxBundleTarEntryBytes)
		if rerr != nil {
			return BundleManifest{}, nil, nil, nil, nil, rerr
		}

		switch filepath.Clean(hdr.Name) {
		case "manifest.json":
			manifestBytes = data
		case "artifact":
			artifactBytes = data
		case "signature.bundle":
			signatureBundle = data
		case "provenance.bundle":
			provenanceBundle = data
		case "index-snapshot.json":
			indexSnapshotRaw = data
		}
	}

	if manifestBytes == nil || artifactBytes == nil || indexSnapshotRaw == nil {
		return BundleManifest{}, nil, nil, nil, nil, conduiterr.New(CodeArchiveInvalid,
			"bundle is missing one or more required entries (manifest.json, artifact, index-snapshot.json)")
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return BundleManifest{}, nil, nil, nil, nil, conduiterr.Wrap(CodeArchiveInvalid, "bundle manifest.json is not valid JSON", err)
	}
	return manifest, artifactBytes, signatureBundle, provenanceBundle, indexSnapshotRaw, nil
}

func readCappedTarEntry(tr *tar.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(tr, maxBytes+1))
	if err != nil {
		return nil, conduiterr.Wrap(CodeArchiveInvalid, "could not read bundle tar entry", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, conduiterr.New(CodeArchiveInvalid, "bundle tar entry exceeds the maximum allowed size")
	}
	return data, nil
}

// InstallBundleOptions is InstallFromBundle's configuration.
type InstallBundleOptions struct {
	BundlePath     string
	ConnectorsPath string

	// Verifier MUST be a real *TrustedVerifier — offline install never uses
	// FailClosedVerifier-style verification-disabled wiring; there is no
	// "trust it because we can't check" fallback for the offline path (see
	// this file's doc comment). Concrete, not the IndexVerifier/
	// ArtifactVerifier interfaces, because the stale-bundle carve-out below
	// needs to construct a staleness-relaxed COPY of the same verifier for
	// exactly one retry — an interface value can't be adjusted that way.
	Verifier *TrustedVerifier

	InstalledBy string
	LockTimeout time.Duration

	// RunningConduitVersion/RunningProtocolVersion are this build's own
	// versions, compared against the bundled version's minConduitVersion/
	// minProtocolVersion exactly as an online install would (resolve.go) —
	// an offline install must refuse an incompatible pinned version just as
	// readily as an online one; being offline is not a compatibility
	// exemption.
	RunningConduitVersion  string
	RunningProtocolVersion string

	// AllowStaleBundle requests tolerating a bundled index snapshot older
	// than the verifier's MaxStaleness — gated identically to
	// --allow-unsigned (plan-v2/step5 §7 step 3, §9 decision item 2): TTY/
	// operator-policy gated, forbiddable, never silently flippable. Only
	// ever consulted after a first verification attempt fails SPECIFICALLY
	// with index.CodeIndexStale — every other index verification failure
	// (tampering, rollback, an unrecognized trust anchor) is refused
	// unconditionally, with no override of any kind.
	AllowStaleBundle bool
	// TTY, CIEnv, IsMCP, EnvVarSet, TypedConfirmation, OperatorAllowStaleBundle
	// are the primitive signals policy.StaleBundleContext needs, collected
	// by the CLI layer (which never imports pkg/registry/policy directly —
	// see the PolicyBypass depguard rule) and passed through here. Only
	// consulted when AllowStaleBundle is true AND the bundle actually turns
	// out to be stale.
	TTY                      bool
	CIEnv                    bool
	IsMCP                    bool
	EnvVarSet                bool
	TypedConfirmation        bool
	OperatorAllowStaleBundle bool
}

func (o *InstallBundleOptions) lockTimeout() time.Duration {
	if o.LockTimeout > 0 {
		return o.LockTimeout
	}
	return DefaultLockTimeout
}

// InstallFromBundle installs a connector fully offline: NO network call of
// any kind is made by this function or anything it calls (index
// verification and Sigstore bundle verification both operate purely over
// bytes already in hand — see this file's doc comment) — asserted, not
// merely assumed: if index-snapshot.json is malformed, this function fails
// closed rather than ever falling back to a live index fetch, which would
// silently reintroduce the network dependency this path exists to avoid.
func InstallFromBundle(ctx context.Context, opts InstallBundleOptions) (*InstallResult, error) {
	if opts.BundlePath == "" {
		return nil, conduiterr.New(conduiterr.CodeInvalidArgument, "--bundle path is required")
	}
	if opts.ConnectorsPath == "" {
		return nil, conduiterr.New(conduiterr.CodeInvalidArgument, "--connectors.path is required")
	}
	if opts.Verifier == nil {
		return nil, conduiterr.New(CodeVerificationUnavailable, "no verifier configured for bundle install")
	}

	manifest, artifactBytes, signatureBundle, provenanceBundle, snapshotRaw, err := readBundleTar(opts.BundlePath)
	if err != nil {
		return nil, err
	}
	if manifest.BundleFormatVersion > BundleFormatVersion {
		return nil, conduiterr.New(conduiterr.CodeInvalidArgument, fmt.Sprintf(
			"bundle format version %d is newer than this build supports (max %d) — upgrade conduit",
			manifest.BundleFormatVersion, BundleFormatVersion))
	}

	verified, err := verifyBundleIndex(ctx, opts, snapshotRaw)
	if err != nil {
		return nil, err
	}

	resolved, artifact, err := resolveBundleArtifact(verified, manifest, opts)
	if err != nil {
		return nil, err
	}

	sum, verifyResult, err := verifyBundleArtifact(ctx, opts, bundleArtifactMaterial{
		artifactBytes: artifactBytes, manifestSHA256: manifest.SHA256,
		signatureBundle: signatureBundle, provenanceBundle: provenanceBundle,
	}, resolved, artifact)
	if err != nil {
		return nil, err
	}

	key, err := ManifestKey(resolved.Connector.Name, resolved.Version.Version)
	if err != nil {
		return nil, err
	}

	targetLock, err := AcquireTargetLock(opts.ConnectorsPath, resolved.Connector.Name, opts.lockTimeout())
	if err != nil {
		return nil, err
	}
	defer targetLock.Unlock() //nolint:errcheck // best-effort; flock also releases at process exit

	entry, alreadyInstalled, err := lookupManifestEntry(opts.ConnectorsPath, key)
	if err != nil {
		return nil, err
	}
	if alreadyInstalled {
		return &InstallResult{
			Name: entry.Name, Version: entry.Version, OS: entry.OS, Arch: entry.Arch,
			AlreadyInstalled: true, ArtifactFile: entry.ArtifactFile, Digest: entry.Digest,
			SourceIndexVersion: entry.SourceIndexVersion,
		}, nil
	}

	stagingDir, err := createBundleStagingDir(opts.ConnectorsPath)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stagingDir)

	archivePath := filepath.Join(stagingDir, "artifact.tar.gz")
	if err := os.WriteFile(archivePath, artifactBytes, 0o600); err != nil {
		return nil, conduiterr.Wrap(CodeArchiveInvalid, "could not stage bundled artifact", err)
	}

	entryToSave, err := finalizeArtifactInstall(finalizeInstallOptions{
		connectorsPath:     opts.ConnectorsPath,
		stagingDir:         stagingDir,
		archivePath:        archivePath,
		name:               resolved.Connector.Name,
		version:            resolved.Version.Version,
		os:                 artifact.OS,
		arch:               artifact.Arch,
		digest:             sum,
		size:               int64(len(artifactBytes)),
		installedBy:        opts.InstalledBy,
		sourceIndexVersion: verified.Payload.Index.Version,
		source:             InstallSourceOfflineBundle,
		bundleIndexVersion: verified.Payload.Index.Version,
		verifyResult:       verifyResult,
	})
	if err != nil {
		return nil, err
	}

	if err := writeManifestEntry(opts.ConnectorsPath, key, entryToSave, opts.lockTimeout()); err != nil {
		return nil, err
	}

	if err := AppendAuditEvent(auditLogPath(opts.ConnectorsPath), AuditEvent{
		Event: "connector_install", Connector: entryToSave.Name, Version: entryToSave.Version,
		Digest: entryToSave.Digest, Operator: opts.InstalledBy, Timestamp: entryToSave.InstalledAt,
		Signed: entryToSave.Signed, VerifiedIdentity: entryToSave.VerifiedIdentity, AllowUnsigned: entryToSave.AllowUnsigned,
	}); err != nil {
		return nil, conduiterr.Wrap(conduiterr.CodeInternal, "connector installed but could not append the audit log entry", err)
	}

	return &InstallResult{
		Name: entryToSave.Name, Version: entryToSave.Version, OS: entryToSave.OS, Arch: entryToSave.Arch,
		Deprecated: resolved.Version.Deprecated, ArtifactFile: entryToSave.ArtifactFile,
		Digest: entryToSave.Digest, SourceIndexVersion: entryToSave.SourceIndexVersion,
	}, nil
}

// resolveBundleArtifact resolves manifest.Name/Version against the
// verified snapshot and selects the (os, arch) artifact the bundle claims —
// factored out of InstallFromBundle purely to keep that function's
// cyclomatic complexity down; no independent behavior of its own.
func resolveBundleArtifact(verified *index.VerifiedIndex, manifest BundleManifest, opts InstallBundleOptions) (*ResolvedVersion, *index.Artifact, error) {
	resolved, err := Resolve(verified.Payload, ResolveOptions{
		Name: manifest.Name, Version: manifest.Version,
		RunningConduitVersion: opts.RunningConduitVersion, RunningProtocolVersion: opts.RunningProtocolVersion,
	})
	if err != nil {
		return nil, nil, err
	}
	artifact, err := SelectArtifact(resolved.Connector.Name, resolved.Version, manifest.OS, manifest.Arch)
	if err != nil {
		return nil, nil, err
	}
	return resolved, artifact, nil
}

// bundleArtifactMaterial bundles the raw bytes verifyBundleArtifact needs,
// so its own signature stays short.
type bundleArtifactMaterial struct {
	artifactBytes    []byte
	manifestSHA256   string
	signatureBundle  []byte
	provenanceBundle []byte
}

// verifyBundleArtifact runs the digest check (twice — against the bundle's
// own manifest.json claim AND the verified snapshot's per-artifact declared
// sha256, plan-v2/step5 §7 step 7) and the identity-pinned signature/
// provenance verification, exactly as install.go's runVerificationGate does
// for a live artifact — factored out of InstallFromBundle purely to keep
// that function's cyclomatic complexity down.
func verifyBundleArtifact(ctx context.Context, opts InstallBundleOptions, m bundleArtifactMaterial, resolved *ResolvedVersion, artifact *index.Artifact) ([32]byte, VerifyResult, error) {
	sum := sha256.Sum256(m.artifactBytes)
	if err := CheckCorruption(sum, m.manifestSHA256); err != nil {
		return sum, VerifyResult{}, err
	}
	if err := CheckCorruption(sum, artifact.SHA256); err != nil {
		return sum, VerifyResult{}, err
	}

	identity := trust.PinnedIdentity{
		OIDCIssuer:      resolved.Connector.Publisher.ExpectedOIDCIssuer,
		IdentityPattern: resolved.Connector.Publisher.ExpectedIdentityPattern,
	}
	verifyResult, err := opts.Verifier.VerifyArtifact(ctx, ArtifactRef{
		Digest: sum, SignatureBundle: m.signatureBundle, ProvenanceBundle: m.provenanceBundle,
	}, identity)
	if err != nil {
		return sum, VerifyResult{}, err
	}
	if !verifyResult.Signed {
		return sum, VerifyResult{}, conduiterr.New(CodeVerificationUnavailable,
			"bundle artifact verification did not return a signed result — refusing to install")
	}
	return sum, verifyResult, nil
}

// createBundleStagingDir creates the private (0700), uniquely-named
// staging subdirectory of connectorsPath InstallFromBundle stages the
// bundled artifact into before extraction — same discipline as install.go's
// own staging directory (see downloadVerifyAndInstall's doc comment for why
// it must be a subdirectory of connectorsPath, not the OS temp dir).
func createBundleStagingDir(connectorsPath string) (string, error) {
	stagingRoot := stagingRootPath(connectorsPath)
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		return "", conduiterr.Wrap(CodeDownloadFailed, "could not create staging root directory", err)
	}
	stagingDir, err := os.MkdirTemp(stagingRoot, "install-bundle-*")
	if err != nil {
		return "", conduiterr.Wrap(CodeDownloadFailed, "could not create staging directory", err)
	}
	if err := os.Chmod(stagingDir, 0o700); err != nil {
		return "", conduiterr.Wrap(CodeDownloadFailed, "could not set staging directory permissions", err)
	}
	return stagingDir, nil
}

// verifyBundleIndex verifies snapshotRaw via opts.Verifier.VerifyIndex —
// exactly as a live-fetched index would be — with ONE gated exception: if
// (and only if) the failure is SPECIFICALLY index.CodeIndexStale, the
// stale-bundle policy gate (policy.DecideStaleBundle, identical rigor to
// --allow-unsigned) may authorize a single retry with staleness disabled
// for this one verification. Every other failure (integrity, rollback,
// trust-anchor) is refused unconditionally — there is no override for
// those, ever.
func verifyBundleIndex(ctx context.Context, opts InstallBundleOptions, snapshotRaw []byte) (*index.VerifiedIndex, error) {
	verified, err := opts.Verifier.VerifyIndex(ctx, snapshotRaw)
	if err == nil {
		if !verified.Verified {
			return nil, conduiterr.New(CodeVerificationUnavailable,
				"the bundled index snapshot could not be cryptographically verified — refusing to install")
		}
		return verified, nil
	}

	ce, ok := conduiterr.Get(err)
	if !ok || ce.Code != index.CodeIndexStale {
		// Integrity, rollback, trust-anchor-expired, or an unrecognized
		// error — never overridable, regardless of AllowStaleBundle.
		return nil, err
	}

	if !opts.AllowStaleBundle {
		staleErr := conduiterr.New(CodeBundleStale,
			"the bundled index snapshot is older than the maximum allowed staleness window — a stale bundle "+
				"could be installing a since-yanked version you would refuse if you knew")
		staleErr.Suggestion = "re-run with --allow-stale-bundle if this air-gapped bundle's age is expected"
		return nil, staleErr
	}

	dec, reason := policy.DecideStaleBundle(policy.StaleBundleContext{
		TTY: opts.TTY, CIEnv: opts.CIEnv, IsMCP: opts.IsMCP,
		OperatorAllowStaleBundle: opts.OperatorAllowStaleBundle,
		EnvVarSet:                opts.EnvVarSet, TypedConfirmation: opts.TypedConfirmation,
	})
	if !dec.Allowed() {
		staleErr := conduiterr.New(CodeBundleStale, "stale bundle installation was not approved: "+reason)
		staleErr.Suggestion = fmt.Sprintf("confirm interactively, or set %s=I_UNDERSTAND in a non-interactive context", StaleBundleEnvVar)
		return nil, staleErr
	}

	// Retry with staleness effectively disabled for this ONE verification —
	// a relaxed COPY of the verifier, never mutating the caller's shared
	// instance. Rollback/integrity/trust-anchor checks inside VerifyIndex
	// are NOT relaxed by this — only CheckStaleness's comparison is, since
	// that is the specific, gated exception this path exists for.
	relaxed := *opts.Verifier
	relaxed.MaxStaleness = 100 * 365 * 24 * time.Hour // effectively unbounded for this one retry
	verified, err = relaxed.VerifyIndex(ctx, snapshotRaw)
	if err != nil {
		return nil, err
	}
	if !verified.Verified {
		return nil, conduiterr.New(CodeVerificationUnavailable,
			"the bundled index snapshot could not be cryptographically verified — refusing to install")
	}

	if logErr := AppendAuditEvent(auditLogPath(opts.ConnectorsPath), AuditEvent{
		Event: "stale_bundle_override", Timestamp: time.Now().UTC(), Operator: opts.InstalledBy,
	}); logErr != nil {
		return nil, conduiterr.Wrap(conduiterr.CodeInternal, "stale bundle override approved but could not append the audit log entry", logErr)
	}

	return verified, nil
}
