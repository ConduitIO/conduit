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

// This file backs `conduit connectors audit` (plan-v2/step5 §4): the
// mechanism that protects ALREADY-installed connectors. install-time
// refusal only stops a new bad install; a connector installed last month
// whose version is yanked next week, or whose publisher is revoked, keeps
// running silently until something re-checks it. audit is that re-check.
//
// # Reuses PR-2's verification exactly — no lower-trust shortcut
//
// audit fetches and verifies the index through the SAME IndexVerifier
// (registry.TrustedVerifier, in production) install.go uses — same trust
// anchors, same rollback/staleness/integrity checks, same error codes. There
// is no second, lower-assurance "audit index fetch" path: an index this
// command can't cryptographically verify is a hard failure for the whole
// run (see RunAudit), never a degraded partial result. Named connectoraudit
// to avoid colliding with audit.go, which is the unrelated append-only
// install/uninstall event log (AppendAuditEvent) — this file has nothing to
// do with that one.
package registry

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	json "github.com/goccy/go-json"

	"github.com/conduitio/conduit/pkg/conduit/check"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry/index"
	"github.com/conduitio/conduit/pkg/registry/trust"
)

// AuditFinding is the specific condition an audited entry triggered, or ""
// for a clean pass. These are local-integrity/informational states or
// registry-trust findings — NOT new conduiterr codes in their own right
// (plan-v2 §4): REVOKED_PUBLISHER/YANKED_VERSION reuse
// trust.CodeIdentityRevoked/index.CodeVersionYanked verbatim via
// AuditResultEntry.Code, exactly as an install-time refusal would.
type AuditFinding string

const (
	FindingNone             AuditFinding = ""
	FindingDelisted         AuditFinding = "DELISTED"
	FindingUnknownVersion   AuditFinding = "UNKNOWN_VERSION"
	FindingYankedVersion    AuditFinding = "YANKED_VERSION"
	FindingRevokedPublisher AuditFinding = "REVOKED_PUBLISHER"
	FindingMissingArtifact  AuditFinding = "MISSING_ARTIFACT"
	FindingDrifted          AuditFinding = "DRIFTED"
)

// AuditStatus mirrors pkg/conduit/check.Status (pass/warn/fail) — audit
// deliberately reuses that package's aggregation pattern (see
// AuditReport.ExitCode) rather than inventing a parallel one.
type AuditStatus string

const (
	AuditStatusPass AuditStatus = "pass"
	AuditStatusWarn AuditStatus = "warn"
	AuditStatusFail AuditStatus = "fail"
)

// AuditResultEntry is one installed connector's audit outcome.
type AuditResultEntry struct {
	Name             string       `json:"name"`
	InstalledVersion string       `json:"installedVersion"`
	Digest           string       `json:"digest,omitempty"`
	Finding          AuditFinding `json:"finding"`
	Status           AuditStatus  `json:"status"`
	Reason           string       `json:"reason,omitempty"`
	Suggestion       string       `json:"suggestion,omitempty"`
	// Code is the registered conduiterr reason string backing a Fail
	// finding (REVOKED_PUBLISHER -> trust.CodeIdentityRevoked,
	// YANKED_VERSION -> index.CodeVersionYanked) — AuditReport.ExitCode
	// resolves this exactly the way pkg/conduit/check.Report.ExitCode
	// resolves CheckResult.Code.
	Code string `json:"code,omitempty"`

	// MissingArtifact/Drifted are the independent local-integrity signals
	// (step5 §6) — always populated regardless of which Finding "won" the
	// slot above, so a caller/test can assert on them directly even when a
	// registry-trust finding (yanked/revoked/delisted/unknown-version) also
	// fired for the same entry.
	MissingArtifact bool `json:"missingArtifact,omitempty"`
	Drifted         bool `json:"drifted,omitempty"`

	// UnsignedInstall reports whether this exact name@version was ever
	// recorded in the append-only unsigned-installs.log — NOT derived from
	// manifest.json's own Signed/AllowUnsigned fields, which are locally
	// rewritable by anyone with filesystem access (plan-v2 §13 P2 nit).
	UnsignedInstall bool `json:"unsignedInstall,omitempty"`
}

// AuditReport is RunAudit's full result.
type AuditReport struct {
	Findings []AuditResultEntry `json:"findings"`
}

// ExitCode aggregates every Fail finding to one process exit code via the
// SAME worst-bucket rule as pkg/conduit/check.Report.ExitCode: each Fail
// finding's Code is classified independently via conduiterr.LookupCode +
// exitcode.ExitCode, and the largest bucket wins — never "first finding
// wins." Zero Fail findings returns exitcode.OK.
func (r AuditReport) ExitCode() int {
	return toCheckReport(r).ExitCode()
}

// AuditOptions is RunAudit's configuration.
type AuditOptions struct {
	ConnectorsPath string
	IndexURL       string
	IndexFile      string
	// IndexVerifier MUST be the real trust-core verifier (registry.TrustedVerifier)
	// in production — the same one install.go wires in. audit reuses it
	// exactly; there is no lower-trust alternative for this field (plan-v2
	// §7 step5 note, and this PR's own scope guardrail).
	IndexVerifier IndexVerifier
}

// RunAudit re-fetches and re-verifies the current signed index (steps 1),
// loads the local manifest (step 2), and checks EVERY installed connector
// against the verified index data (step 3): a connector whose publisher has
// since been revoked, or whose installed version has since been yanked, or
// that has vanished from the index entirely, or whose exact version is no
// longer listed — plus the independent local-integrity checks (missing
// artifact, drifted digest).
//
// A hard index-verification failure (unreachable, tampered/integrity,
// stale, rollback, or an unrecognized trust anchor) fails THE WHOLE RUN —
// never a per-connector finding, and never "audit half the connectors
// against a trusted index and half against nothing": you cannot partially
// trust an unverified index. This is returned as an error (a HARD command
// failure at the CLI layer), never folded into AuditReport.
func RunAudit(ctx context.Context, opts AuditOptions) (*AuditReport, error) {
	raw, err := fetchIndexRawFrom(ctx, opts.IndexURL, opts.IndexFile)
	if err != nil {
		return nil, err
	}

	verified, err := opts.IndexVerifier.VerifyIndex(ctx, raw)
	if err != nil {
		return nil, err
	}
	if !verified.Verified {
		// Structural belt-and-suspenders (mirrors install.go's own
		// assumption): a build wired with FailClosedVerifier (trust core
		// disabled) must never let audit treat unverified data as a basis
		// for a yank/revocation decision.
		return nil, conduiterr.New(CodeVerificationUnavailable,
			"the registry index could not be cryptographically verified — refusing to audit installed "+
				"connectors against untrusted data")
	}

	m, err := LoadManifest(manifestPath(opts.ConnectorsPath))
	if err != nil {
		return nil, conduiterr.Wrap(conduiterr.CodeInternal, "could not read install manifest", err)
	}

	unsigned, err := loadUnsignedInstallSet(unsignedInstallsLogPath(opts.ConnectorsPath))
	if err != nil {
		return nil, conduiterr.Wrap(conduiterr.CodeInternal, "could not read unsigned-installs audit log", err)
	}

	byName := make(map[string]*index.Connector, len(verified.Payload.Connectors))
	for i := range verified.Payload.Connectors {
		byName[verified.Payload.Connectors[i].Name] = &verified.Payload.Connectors[i]
	}

	keys := make([]string, 0, len(m.Installs))
	for k := range m.Installs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	findings := make([]AuditResultEntry, 0, len(keys))
	for _, k := range keys {
		entry := m.Installs[k]
		findings = append(findings, auditEntry(opts.ConnectorsPath, entry, byName[entry.Name], unsigned))
	}
	return &AuditReport{Findings: findings}, nil
}

// auditEntry applies the index schema doc's §e decision table (concretized
// by plan-v2/step5 §4) to one installed manifest entry.
func auditEntry(connectorsPath string, entry ManifestEntry, conn *index.Connector, unsigned map[string]bool) AuditResultEntry {
	res := AuditResultEntry{
		Name: entry.Name, InstalledVersion: entry.Version, Digest: entry.Digest,
		UnsignedInstall: unsigned[entry.Name+"@"+entry.Version],
	}

	switch {
	case conn == nil:
		res.Finding = FindingDelisted
		res.Status = AuditStatusWarn
		res.Reason = "connector name not present in the current registry index"
		res.Suggestion = "confirm whether this connector was intentionally retired from the registry; if not, investigate before continuing to run it"
	case conn.Publisher.Revoked != nil:
		res.Finding = FindingRevokedPublisher
		res.Status = AuditStatusFail
		res.Reason = conn.Publisher.Revoked.Reason
		res.Code = trust.CodeIdentityRevoked.Reason()
		res.Suggestion = fmt.Sprintf("uninstall %s@%s — its publisher identity has been revoked", entry.Name, entry.Version)
	default:
		v, ok := findConnectorVersion(conn, entry.Version)
		switch {
		case !ok:
			res.Finding = FindingUnknownVersion
			res.Status = AuditStatusWarn
			res.Reason = "installed version not found in the current registry index"
			res.Suggestion = "confirm this version was legitimately retired/pruned from the index, or install a currently-listed version"
		case v.Yanked != nil:
			res.Finding = FindingYankedVersion
			res.Status = AuditStatusFail
			res.Reason = v.Yanked.Reason
			res.Code = index.CodeVersionYanked.Reason()
			res.Suggestion = yankedSuggestion(conn, entry)
		}
	}

	artifactPath := connectorArtifactPath(connectorsPath, entry)
	missing, drifted := checkArtifactHealth(artifactPath, entry.Digest)
	res.MissingArtifact = missing
	res.Drifted = drifted

	if res.Finding == FindingNone {
		switch {
		case missing:
			res.Finding = FindingMissingArtifact
			res.Status = AuditStatusWarn
			res.Suggestion = fmt.Sprintf(
				"re-run `conduit connectors install %s@%s` or `conduit connectors uninstall %s@%s` to clean up the manifest entry",
				entry.Name, entry.Version, entry.Name, entry.Version)
		case drifted:
			res.Finding = FindingDrifted
			res.Status = AuditStatusWarn
			res.Suggestion = "the on-disk artifact no longer matches the digest recorded at install time — investigate before trusting it"
		default:
			res.Status = AuditStatusPass
		}
	}
	return res
}

func connectorArtifactPath(connectorsPath string, entry ManifestEntry) string {
	if entry.ArtifactFile == "" {
		return connectorsPath
	}
	return filepath.Join(connectorsPath, entry.ArtifactFile)
}

// toCheckReport converts an AuditReport into a pkg/conduit/check.Report so
// AuditReport.ExitCode can reuse check.Report.ExitCode's worst-bucket
// aggregation verbatim (doctor already establishes this exact pattern; see
// that package's doc comment) instead of re-implementing it.
func toCheckReport(r AuditReport) check.Report {
	results := make([]check.CheckResult, 0, len(r.Findings))
	for _, f := range r.Findings {
		status := check.StatusPass
		switch f.Status {
		case AuditStatusPass:
			status = check.StatusPass
		case AuditStatusWarn:
			status = check.StatusWarn
		case AuditStatusFail:
			status = check.StatusFail
		}
		results = append(results, check.CheckResult{
			Name:    f.Name + "@" + f.InstalledVersion,
			Status:  status,
			Message: f.Reason,
			Code:    f.Code,
		})
	}
	return check.Report{Checks: results}
}

// loadUnsignedInstallSet reads policy's append-only unsigned-installs.log
// (pkg/registry/policy.AppendUnsignedInstallEvent's output) and returns the
// set of "name@version" entries it ever recorded — WITHOUT importing
// pkg/registry/policy itself (this file only needs the two JSON fields it
// cares about, and the PolicyBypass depguard rule in .golangci.yml
// deliberately restricts which files may import that package at all; this
// function has no reason to be one of them). A missing file is not an
// error: it means no unsigned install has ever been logged, matching an
// empty set.
func loadUnsignedInstallSet(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, cerrors.Errorf("could not read unsigned-installs log %q: %w", path, err)
	}

	set := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev struct {
			Connector string `json:"connector"`
			Version   string `json:"version"`
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // tolerate a malformed line rather than failing the whole audit run over it
		}
		if ev.Connector != "" {
			set[ev.Connector+"@"+ev.Version] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, cerrors.Errorf("could not scan unsigned-installs log %q: %w", path, err)
	}
	return set, nil
}

// findConnectorVersion looks up version (semver-equal, tolerating a leading
// "v" — §5) within conn.Versions.
func findConnectorVersion(conn *index.Connector, version string) (index.ConnectorVersion, bool) {
	want, err := NormalizeVersion(version)
	if err != nil {
		return index.ConnectorVersion{}, false
	}
	for _, v := range conn.Versions {
		got, err := NormalizeVersion(v.Version)
		if err != nil {
			continue
		}
		if got.Equal(want) {
			return v, true
		}
	}
	return index.ConnectorVersion{}, false
}

// yankedSuggestion names a newer, non-yanked, compatible version to install
// instead, when one exists in conn — falling back to a plain uninstall
// suggestion when none does (plan-v2/step5 §4 step 5).
func yankedSuggestion(conn *index.Connector, entry ManifestEntry) string {
	latest := latestNonYankedVersion(*conn)
	if latest == "" {
		return fmt.Sprintf("uninstall %s@%s — no compatible non-yanked replacement is available", entry.Name, entry.Version)
	}
	return fmt.Sprintf("install %s@%s", entry.Name, latest)
}
