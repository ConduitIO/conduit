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
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// InUseRef identifies one connector instance (in a pipeline, running or
// merely provisioned) whose plugin reference exactly matches the
// name@version an uninstall is about to remove.
//
// Deliberately plain data, not an interface Uninstall calls out to: the
// CALLER (cmd/conduit/root/connectors/uninstall.go) is responsible for
// collecting this list — via the running engine's ConnectorServiceClient if
// reachable, falling back to a direct scan of provisioned pipeline configs
// via pkg/provisioning/config if not (step5 §2 step 2) — exactly the same
// "collect primitive signals at the CLI layer" pattern install.go already
// uses for TTY/CIEnv/EnvVarSet. This keeps pkg/registry free of a gRPC
// client or pkg/provisioning dependency, and keeps Uninstall itself a pure,
// easily-unit-tested function of already-known facts.
type InUseRef struct {
	PipelineID  string `json:"pipelineId"`
	ConnectorID string `json:"connectorId"`
}

// UninstallOptions is Uninstall's full configuration.
type UninstallOptions struct {
	// Name is the connector name to uninstall.
	Name string
	// Version is an optional exact version ("0.14.1" or "v0.14.1"). Empty
	// auto-resolves ONLY if exactly one version of Name is currently
	// installed; if more than one is installed, an empty Version refuses
	// with CodeAmbiguousUninstall (plan-v2 §3.1) rather than guessing.
	Version string

	// ConnectorsPath is the standalone connectors directory
	// (--connectors.path) this uninstall operates against.
	ConnectorsPath string

	// Force proceeds even when InUseRefs is non-empty. The result still
	// reports every affected pipeline via Warnings — this is a loudly
	// surfaced warning, not a silent flag flip (step5 §2 step 4).
	Force bool

	// InUseRefs is every connector instance (across every pipeline) whose
	// plugin reference is exactly standalone:<name>@<version> — see
	// InUseRef's doc comment for why the caller, not this function,
	// collects it.
	InUseRefs []InUseRef

	// InstalledBy identifies the operator for the audit event (best-effort).
	InstalledBy string

	// LockTimeout bounds how long Uninstall waits to acquire the
	// per-connector-name lock and the manifest lock. Zero uses
	// DefaultLockTimeout.
	LockTimeout time.Duration
}

func (o *UninstallOptions) lockTimeout() time.Duration {
	if o.LockTimeout > 0 {
		return o.LockTimeout
	}
	return DefaultLockTimeout
}

// UninstallResult is Uninstall's success-path result.
type UninstallResult struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest,omitempty"`

	Forced   bool     `json:"forced"`
	Warnings []string `json:"warnings"`

	// DriftDetected is true when the on-disk artifact's sha256 no longer
	// matched the manifest's recorded digest at uninstall time (step5 §6) —
	// informational: the artifact is removed either way.
	DriftDetected bool `json:"driftDetected,omitempty"`
	// ArtifactAlreadyMissing is true when the artifact file was already gone
	// before Uninstall ran (e.g. removed manually with rm) — not a failure;
	// Uninstall still cleans up the manifest entry.
	ArtifactAlreadyMissing bool `json:"artifactAlreadyMissing,omitempty"`
}

// Uninstall removes an installed connector's artifact and manifest entry.
//
// Invariant 5 (atomic state/checkpoint writes): the manifest is only ever
// rewritten via SaveManifest's atomic temp+rename — a crash mid-uninstall
// leaves either the pre-uninstall manifest (artifact possibly already
// deleted, in which case a retry finishes the job — os.Remove tolerates
// "already gone") or the fully-updated post-uninstall manifest, never a
// torn one.
func Uninstall(opts UninstallOptions) (*UninstallResult, error) {
	if opts.Name == "" {
		return nil, conduiterr.New(conduiterr.CodeInvalidArgument, "connector name is required")
	}
	if opts.ConnectorsPath == "" {
		return nil, conduiterr.New(conduiterr.CodeInvalidArgument, "--connectors.path is required")
	}

	// Serializes against a concurrent install/uninstall of ANY version of
	// this name — the same per-name granularity AcquireTargetLock already
	// uses for install (only Name, not Name@Version), which is intentional:
	// InstalledVersions/CodeAmbiguousUninstall's resolution below reads
	// every version of Name from the manifest, so it must not race a
	// concurrent install that is about to add or remove one.
	targetLock, err := AcquireTargetLock(opts.ConnectorsPath, opts.Name, opts.lockTimeout())
	if err != nil {
		return nil, err
	}
	defer targetLock.Unlock() //nolint:errcheck // best-effort; flock also releases at process exit

	m, err := LoadManifest(manifestPath(opts.ConnectorsPath))
	if err != nil {
		return nil, conduiterr.Wrap(conduiterr.CodeInternal, "could not read install manifest", err)
	}

	version, err := resolveUninstallVersion(m, opts.Name, opts.Version)
	if err != nil {
		return nil, err
	}

	key, err := ManifestKey(opts.Name, version)
	if err != nil {
		return nil, err
	}
	entry, ok := m.Installs[key]
	if !ok {
		return nil, notInstalledError(opts.Name, version)
	}

	var warnings []string
	forced := false
	if len(opts.InUseRefs) > 0 {
		if !opts.Force {
			return nil, inUseError(entry.Name, entry.Version, opts.InUseRefs)
		}
		forced = true
		warnings = append(warnings, inUseWarning(entry.Name, entry.Version, opts.InUseRefs))
	}

	driftDetected, artifactAlreadyMissing, err := removeArtifact(opts.ConnectorsPath, entry)
	if err != nil {
		return nil, err
	}

	if err := deleteManifestEntry(opts.ConnectorsPath, key, opts.lockTimeout()); err != nil {
		return nil, err
	}

	if err := AppendAuditEvent(auditLogPath(opts.ConnectorsPath), AuditEvent{
		Event: "connector_uninstall", Connector: entry.Name, Version: entry.Version,
		Digest: entry.Digest, Operator: opts.InstalledBy, Timestamp: time.Now().UTC(),
		Forced: forced,
	}); err != nil {
		return nil, conduiterr.Wrap(conduiterr.CodeInternal, "connector uninstalled but could not append the audit log entry", err)
	}

	return &UninstallResult{
		Name: entry.Name, Version: entry.Version, Digest: entry.Digest,
		Forced: forced, Warnings: warnings,
		DriftDetected: driftDetected, ArtifactAlreadyMissing: artifactAlreadyMissing,
	}, nil
}

// resolveUninstallVersion implements plan-v2 §3.1's ambiguous-uninstall
// resolution: an explicit version is used as-is (existence is checked by the
// caller's manifest lookup); an empty version auto-resolves only when
// exactly one version of name is installed.
func resolveUninstallVersion(m *Manifest, name, version string) (string, error) {
	if version != "" {
		return version, nil
	}

	versions := m.InstalledVersions(name)
	switch len(versions) {
	case 0:
		return "", notInstalledError(name, "")
	case 1:
		return versions[0], nil
	default:
		err := conduiterr.New(CodeAmbiguousUninstall, fmt.Sprintf(
			"connector %q has more than one installed version (%s) — specify which one",
			name, strings.Join(versions, ", ")))
		err.Suggestion = fmt.Sprintf("re-run as `conduit connectors uninstall %s@<version>`, e.g. %s@%s",
			name, name, versions[len(versions)-1])
		return "", err
	}
}

func notInstalledError(name, version string) error {
	target := name
	if version != "" {
		target = name + "@" + version
	}
	err := conduiterr.New(CodeConnectorNotInstalled, fmt.Sprintf("connector %q is not installed", target))
	err.Suggestion = "check `conduit connectors list --installed` for the exact installed name@version"
	return err
}

func inUseError(name, version string, refs []InUseRef) error {
	err := conduiterr.New(CodeConnectorInUse, fmt.Sprintf(
		"connector %s@%s is in use by %s", name, version, describeInUseRefs(refs)))
	err.Suggestion = "stop or reconfigure these connectors first, or re-run with --force to remove the artifact " +
		"anyway (pipelines referencing it will fail to start/restart until reconfigured)"
	return err
}

func inUseWarning(name, version string, refs []InUseRef) string {
	return fmt.Sprintf("removed %s@%s while still in use by %s — pipelines referencing it will fail to "+
		"start/restart until reconfigured", name, version, describeInUseRefs(refs))
}

func describeInUseRefs(refs []InUseRef) string {
	parts := make([]string, 0, len(refs))
	for _, r := range refs {
		parts = append(parts, fmt.Sprintf("pipeline %s (connector %s)", r.PipelineID, r.ConnectorID))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// removeArtifact deletes the installed artifact file, tolerating "already
// gone" (step5 §6: a manually-`rm`'d artifact is not a failure) and
// reporting whether the on-disk bytes had drifted from the manifest's
// recorded digest before removal (step5 §6: informational, never blocking).
func removeArtifact(connectorsPath string, entry ManifestEntry) (driftDetected, artifactAlreadyMissing bool, err error) {
	if entry.ArtifactFile == "" {
		return false, true, nil
	}
	path := filepath.Join(connectorsPath, entry.ArtifactFile)

	missing, drifted := checkArtifactHealth(path, entry.Digest)
	if missing {
		return false, true, nil
	}

	if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
		return drifted, false, cerrors.Errorf("could not remove installed artifact %q: %w", path, rmErr)
	}
	return drifted, false, nil
}

// deleteManifestEntry removes key from the manifest under the manifest
// lock, atomically rewriting the whole file — the mirror image of
// install.go's writeManifestEntry.
func deleteManifestEntry(connectorsPath, key string, lockTimeout time.Duration) error {
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
	delete(m.Installs, key)
	if err := SaveManifest(path, m); err != nil {
		return conduiterr.Wrap(conduiterr.CodeInternal, "could not write install manifest", err)
	}
	return nil
}
