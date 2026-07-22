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

// This file backs `conduit connectors list --installed` (plan-v2/step5 §3):
// enumerating the local install manifest, independent of whether any
// pipeline uses an entry or whether the engine is even running — a
// fundamentally different thing from the existing `connectors list`, which
// queries the running engine's pipeline connector INSTANCES. See
// cmd/conduit/root/connectors/list.go for why the two render as visibly
// distinct tables.
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry/index"
)

// InstalledStatus is list --installed's per-row status (step5 §3).
type InstalledStatus string

const (
	InstalledStatusOK               InstalledStatus = "ok"
	InstalledStatusUpdateAvailable  InstalledStatus = "update-available"
	InstalledStatusMissingArtifact  InstalledStatus = "missing-artifact"
	InstalledStatusDrifted          InstalledStatus = "drifted"
	InstalledStatusIndexUnreachable InstalledStatus = "index-unreachable"
	InstalledStatusNotInIndex       InstalledStatus = "not-in-index"
)

// InstalledConnector is one row of `list --installed` — a plugin artifact
// recorded in the local manifest, not a pipeline connector instance.
type InstalledConnector struct {
	Name            string          `json:"name"`
	Version         string          `json:"version"`
	Signed          bool            `json:"signed"`
	Source          string          `json:"source"`
	InstalledAt     time.Time       `json:"installedAt"`
	Digest          string          `json:"digest,omitempty"`
	LatestAvailable string          `json:"latestAvailable,omitempty"`
	Status          InstalledStatus `json:"status"`
}

// ListInstalledResult is ListInstalled's return shape.
type ListInstalledResult struct {
	Installed []InstalledConnector `json:"installed"`
	// IndexUnreachable is true when the registry index could not be
	// fetched/parsed AT ALL for this run — every row still renders (with a
	// blank LatestAvailable) rather than failing the whole command, per
	// step5 §3: this command degrades gracefully because checking for
	// updates is informational, not a security check (contrast with
	// `audit`, which hard-fails on an unreachable/untrusted index).
	IndexUnreachable bool `json:"indexUnreachable"`
}

// ListInstalledOptions is ListInstalled's configuration.
type ListInstalledOptions struct {
	ConnectorsPath string
	IndexURL       string
	IndexFile      string
}

// ListInstalled enumerates every entry in the local install manifest.
//
// The index is consulted best-effort, and ONLY for the informational
// LatestAvailable/update-available signal — never for a schema-version or
// signature check, and the index bytes are never cryptographically
// verified here (index.ParseUnverified, a shape/schema check only): an
// operator who wants a verified re-check of installed connectors against
// yank/revocation runs `conduit connectors audit`, which does real
// verification and hard-fails when it can't.
func ListInstalled(ctx context.Context, opts ListInstalledOptions) (*ListInstalledResult, error) {
	m, err := LoadManifest(manifestPath(opts.ConnectorsPath))
	if err != nil {
		return nil, conduiterr.Wrap(conduiterr.CodeInternal, "could not read install manifest", err)
	}

	latestByName, indexUnreachable := bestEffortLatestVersions(ctx, opts.IndexURL, opts.IndexFile)

	keys := make([]string, 0, len(m.Installs))
	for k := range m.Installs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	rows := make([]InstalledConnector, 0, len(keys))
	for _, k := range keys {
		e := m.Installs[k]
		row := InstalledConnector{
			Name: e.Name, Version: e.Version, Signed: e.Signed, Source: e.Source,
			InstalledAt: e.InstalledAt, Digest: e.Digest,
		}

		missing, drifted := checkArtifactHealth(filepath.Join(opts.ConnectorsPath, e.ArtifactFile), e.Digest)
		switch {
		case missing:
			row.Status = InstalledStatusMissingArtifact
		case drifted:
			row.Status = InstalledStatusDrifted
		case indexUnreachable:
			row.Status = InstalledStatusIndexUnreachable
		default:
			latest, ok := latestByName[e.Name]
			if !ok {
				row.Status = InstalledStatusNotInIndex
			} else {
				row.LatestAvailable = latest
				if isNewerVersion(latest, e.Version) {
					row.Status = InstalledStatusUpdateAvailable
				} else {
					row.Status = InstalledStatusOK
				}
			}
		}
		rows = append(rows, row)
	}

	return &ListInstalledResult{Installed: rows, IndexUnreachable: indexUnreachable}, nil
}

// checkArtifactHealth reports whether the artifact at path is missing, or
// present but drifted from wantDigest (an optionally "sha256:"-prefixed hex
// string) — the shared local-integrity check `list --installed`, `audit`,
// and `uninstall` all apply the same way (step5 §6).
func checkArtifactHealth(path, wantDigest string) (missing, drifted bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return true, false
	}
	sum := sha256.Sum256(data)
	want := strings.TrimPrefix(wantDigest, "sha256:")
	if !strings.EqualFold(hex.EncodeToString(sum[:]), want) {
		return false, true
	}
	return false, false
}

// fetchIndexRawFrom fetches raw index bytes from indexFile (if set) or
// indexURL — the same file-takes-priority convention install.go's
// fetchIndexRaw uses, factored out here since ListInstalled and RunAudit
// both need it without depending on InstallOptions' unrelated fields.
func fetchIndexRawFrom(ctx context.Context, indexURL, indexFile string) ([]byte, error) {
	if indexFile != "" {
		return index.FetchFile(indexFile)
	}
	return index.Fetch(ctx, indexURL)
}

// bestEffortLatestVersions fetches and shape-parses (never cryptographically
// verifies — see ListInstalled's doc comment) the index, returning the
// newest non-yanked version per connector name. Any failure at all (fetch,
// parse) reports indexUnreachable: true and a nil map, never a hard error —
// this function backs ONLY the informational list --installed path.
func bestEffortLatestVersions(ctx context.Context, indexURL, indexFile string) (map[string]string, bool) {
	raw, err := fetchIndexRawFrom(ctx, indexURL, indexFile)
	if err != nil {
		return nil, true
	}
	payload, err := index.ParseUnverified(raw)
	if err != nil {
		return nil, true
	}

	out := make(map[string]string, len(payload.Connectors))
	for _, c := range payload.Connectors {
		if latest := latestNonYankedVersion(c); latest != "" {
			out[c.Name] = latest
		}
	}
	return out, false
}

// latestNonYankedVersion returns the newest version string in c.Versions
// that is not yanked, or "" if c has no such version.
func latestNonYankedVersion(c index.Connector) string {
	var best string
	bestParsed := false
	for _, v := range c.Versions {
		if v.Yanked != nil {
			continue
		}
		nv, err := NormalizeVersion(v.Version)
		if err != nil {
			continue
		}
		if !bestParsed {
			best, bestParsed = v.Version, true
			continue
		}
		bv, err := NormalizeVersion(best)
		if err == nil && nv.GreaterThan(bv) {
			best = v.Version
		}
	}
	return best
}

// isNewerVersion reports whether latestStr is a strictly greater semver
// than installedStr — an unparsable version on either side reports false
// (never claims an update is available over data it can't compare).
func isNewerVersion(latestStr, installedStr string) bool {
	latest, err1 := NormalizeVersion(latestStr)
	installed, err2 := NormalizeVersion(installedStr)
	if err1 != nil || err2 != nil {
		return false
	}
	return latest.GreaterThan(installed)
}
