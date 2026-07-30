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
	"sort"
	"strings"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// UninstallProcessorOptions is UninstallProcessor's full configuration. It is
// the processor twin of UninstallOptions: identical shape, but rooted at
// --processors.path and carrying processor-worded/coded errors. InUseRef is
// reused verbatim (the pipeline/processor-instance pair the caller collected)
// — see uninstall.go for why the caller, not this function, gathers it.
type UninstallProcessorOptions struct {
	// Name is the processor name to uninstall (the registry index name, which
	// is the name@version key the install manifest recorded — see
	// installprocessor.go on why that is authoritative even when the WASM's own
	// Specification().Version is a "(devel)" sentinel).
	Name string
	// Version is an optional exact version. Empty auto-resolves ONLY if exactly
	// one version of Name is installed; more than one refuses with
	// CodeAmbiguousUninstall rather than guessing (AC-8).
	Version string

	// ProcessorsPath is the standalone processors directory (--processors.path)
	// this uninstall operates against — its own .registry/ bookkeeping, never
	// commingled with the connector manifest.
	ProcessorsPath string

	// Force proceeds even when InUseRefs is non-empty; the result still names
	// every affected pipeline via Warnings (a loud warning, never a silent
	// flag flip).
	Force bool

	// InUseRefs is every processor instance (across every provisioned/running
	// pipeline) whose plugin reference is standalone:<name>[@<version>] in a
	// processors block — the caller (cmd/conduit/root/processorplugins/
	// uninstall.go) collects this by scanning the PROCESSORS blocks of
	// provisioned pipeline YAML, a distinct code path from the connector
	// uninstall's connectors-block scan (AC-8).
	InUseRefs []InUseRef

	// InstalledBy identifies the operator for the audit event (best-effort).
	InstalledBy string

	// LockTimeout bounds how long UninstallProcessor waits to acquire the
	// per-name lock and the manifest lock. Zero uses DefaultLockTimeout.
	LockTimeout time.Duration
}

func (o *UninstallProcessorOptions) lockTimeout() time.Duration {
	if o.LockTimeout > 0 {
		return o.LockTimeout
	}
	return DefaultLockTimeout
}

// UninstallProcessor removes an installed standalone WASM processor's artifact
// and manifest entry from --processors.path. It is the processor analogue of
// Uninstall and reuses every kind-neutral primitive it does — the per-name
// target lock (AcquireTargetLock), the ambiguous-version resolution
// (resolveUninstallVersion), the artifact removal + drift/missing detection
// (removeArtifact), and the atomic manifest-entry deletion (deleteManifestEntry)
// — differing only in the target directory, the audit-event label
// ("processor_uninstall"), and the processor-worded/coded not-installed and
// in-use errors.
//
// Invariant 5 (atomic state writes): the manifest is only ever rewritten via
// SaveManifest's atomic temp+rename (deleteManifestEntry), so a crash
// mid-uninstall leaves either the pre- or post-uninstall manifest, never a
// torn one.
func UninstallProcessor(opts UninstallProcessorOptions) (*UninstallResult, error) {
	if opts.Name == "" {
		return nil, conduiterr.New(conduiterr.CodeInvalidArgument, "processor name is required")
	}
	if opts.ProcessorsPath == "" {
		return nil, conduiterr.New(conduiterr.CodeInvalidArgument, "--processors.path is required")
	}

	// Per-name granularity (only Name, not Name@Version), matching
	// AcquireTargetLock's install-side use: resolveUninstallVersion below reads
	// every installed version of Name, so it must not race a concurrent install
	// adding/removing one.
	targetLock, err := AcquireTargetLock(opts.ProcessorsPath, opts.Name, opts.lockTimeout())
	if err != nil {
		return nil, err
	}
	defer targetLock.Unlock() //nolint:errcheck // best-effort; flock also releases at process exit

	m, err := LoadManifest(manifestPath(opts.ProcessorsPath))
	if err != nil {
		return nil, conduiterr.Wrap(conduiterr.CodeInternal, "could not read install manifest", err)
	}

	version, err := resolveProcessorUninstallVersion(m, opts.Name, opts.Version)
	if err != nil {
		return nil, err
	}

	key, err := ManifestKey(opts.Name, version)
	if err != nil {
		return nil, err
	}
	entry, ok := m.Installs[key]
	if !ok {
		return nil, processorNotInstalledError(opts.Name, version)
	}

	var warnings []string
	forced := false
	if len(opts.InUseRefs) > 0 {
		if !opts.Force {
			return nil, processorInUseError(entry.Name, entry.Version, opts.InUseRefs)
		}
		forced = true
		warnings = append(warnings, processorInUseWarning(entry.Name, entry.Version, opts.InUseRefs))
	}

	driftDetected, artifactAlreadyMissing, err := removeArtifact(opts.ProcessorsPath, entry)
	if err != nil {
		return nil, err
	}

	if err := deleteManifestEntry(opts.ProcessorsPath, key, opts.lockTimeout()); err != nil {
		return nil, err
	}

	if err := AppendAuditEvent(auditLogPath(opts.ProcessorsPath), AuditEvent{
		Event: "processor_uninstall", Connector: entry.Name, Version: entry.Version,
		Digest: entry.Digest, Operator: opts.InstalledBy, Timestamp: time.Now().UTC(),
		Forced: forced,
	}); err != nil {
		return nil, conduiterr.Wrap(conduiterr.CodeInternal, "processor uninstalled but could not append the audit log entry", err)
	}

	return &UninstallResult{
		Name: entry.Name, Version: entry.Version, Digest: entry.Digest,
		Forced: forced, Warnings: warnings,
		DriftDetected: driftDetected, ArtifactAlreadyMissing: artifactAlreadyMissing,
	}, nil
}

// resolveProcessorUninstallVersion is resolveUninstallVersion's processor twin
// (AC-8's ambiguous-uninstall rule): an explicit version is used as-is; an
// empty version auto-resolves only when exactly one version of name is
// installed, refusing with CodeAmbiguousUninstall otherwise.
func resolveProcessorUninstallVersion(m *Manifest, name, version string) (string, error) {
	if version != "" {
		return version, nil
	}

	versions := m.InstalledVersions(name)
	switch len(versions) {
	case 0:
		return "", processorNotInstalledError(name, "")
	case 1:
		return versions[0], nil
	default:
		err := conduiterr.New(CodeAmbiguousUninstall, fmt.Sprintf(
			"processor %q has more than one installed version (%s) — specify which one",
			name, strings.Join(versions, ", ")))
		err.Suggestion = fmt.Sprintf("re-run as `conduit processor-plugins uninstall %s@<version>`, e.g. %s@%s",
			name, name, versions[len(versions)-1])
		return "", err
	}
}

func processorNotInstalledError(name, version string) error {
	target := name
	if version != "" {
		target = name + "@" + version
	}
	err := conduiterr.New(CodeProcessorNotInstalled, fmt.Sprintf("processor %q is not installed", target))
	err.Suggestion = "check the exact installed name@version under --processors.path/.registry/manifest.json"
	return err
}

func processorInUseError(name, version string, refs []InUseRef) error {
	err := conduiterr.New(CodeProcessorInUse, fmt.Sprintf(
		"processor %s@%s is in use by %s", name, version, describeProcessorInUseRefs(refs)))
	err.Suggestion = "remove the processor from these pipelines first, or re-run with --force to remove the artifact " +
		"anyway (pipelines referencing it will fail to start/restart until reconfigured)"
	return err
}

func processorInUseWarning(name, version string, refs []InUseRef) string {
	return fmt.Sprintf("removed %s@%s while still in use by %s — pipelines referencing it will fail to "+
		"start/restart until reconfigured", name, version, describeProcessorInUseRefs(refs))
}

// describeProcessorInUseRefs is describeInUseRefs' processor-worded twin: the
// InUseRef.ConnectorID field carries the processor instance ID here, so the
// human-readable rendering names it "(processor <id>)", not "(connector <id>)".
func describeProcessorInUseRefs(refs []InUseRef) string {
	parts := make([]string, 0, len(refs))
	for _, r := range refs {
		parts = append(parts, fmt.Sprintf("pipeline %s (processor %s)", r.PipelineID, r.ConnectorID))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}
