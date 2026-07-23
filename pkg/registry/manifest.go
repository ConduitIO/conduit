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
	"os"
	"sort"
	"time"

	json "github.com/goccy/go-json"

	"github.com/conduitio/conduit/pkg/foundation/atomicfile"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// ManifestSchemaVersion is the manifest.json schemaVersion this build
// writes, and the value LoadManifest expects.
const ManifestSchemaVersion = 1

// Manifest is the persisted <connectors.path>/.registry/manifest.json
// format (plan-v2 §3): every installed connector, keyed by "name@version"
// so two pipelines can pin two different versions of the same connector
// simultaneously. This is the load-bearing fix over every original
// step-plan's single-version-per-name shape (§3.1) — resolved by DeVaris
// decision, collapsing step2's and step5's two divergent manifest formats
// into one.
type Manifest struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Installs      map[string]ManifestEntry `json:"installs"`
}

// ManifestEntry is one installed connector version.
//
// Signed/VerifiedIdentity/AllowUnsigned were present from PR-0/PR-1 even
// though only FailClosedVerifier existed then (every entry a PR-1 build
// could produce had Signed: false, VerifiedIdentity: "", AllowUnsigned:
// false, and PR-1 could not, by construction, produce ANY entry at all in a
// normal build). As of PR-2 (the real trust core), these fields carry their
// intended meaning: Signed/VerifiedIdentity reflect a real
// signature/provenance verification outcome, and AllowUnsigned is true only
// for an install that went through the full policy.Decide gate (plan-v2
// §6) — see pkg/registry/policy and install.go's unsignedInstallGate. No
// schema change, no migration: the field shapes are unchanged from PR-0.
type ManifestEntry struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	Kind         string `json:"kind"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	ArtifactFile string `json:"artifactFile"`
	Digest       string `json:"digest"`
	Size         int64  `json:"size"`

	InstalledAt time.Time `json:"installedAt"`
	InstalledBy string    `json:"installedBy,omitempty"`

	// SourceIndexVersion is the payload.index.version this install was
	// resolved against — needed by audit (PR-4) to know whether an
	// install's rollback high-water mark came from a live or bundled index
	// snapshot.
	SourceIndexVersion int64 `json:"sourceIndexVersion"`
	// Source is "index" or "offline-bundle".
	Source string `json:"source"`
	// BundleIndexVersion is set only when Source == "offline-bundle".
	BundleIndexVersion int64 `json:"bundleIndexVersion,omitempty"`

	Signed           bool   `json:"signed"`
	VerifiedIdentity string `json:"verifiedIdentity"`
	AllowUnsigned    bool   `json:"allowUnsigned"`
}

// ManifestKey builds the "name@version" manifest key, going through
// NormalizeVersion so a given install always produces the same key
// regardless of whether the caller wrote "v0.14.1" or "0.14.1" (§5) —
// manifest keys themselves are stored WITHOUT a "v" prefix.
func ManifestKey(name, version string) (string, error) {
	v, err := NormalizeVersion(version)
	if err != nil {
		return "", conduiterr.Wrap(CodeIncompatibleVersion, "invalid version for manifest key: "+version, err)
	}
	return name + "@" + v.String(), nil
}

// LoadManifest reads and parses a manifest file. A missing file is not an
// error: it returns an empty, schema-current Manifest, matching "no
// connectors installed yet".
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Manifest{SchemaVersion: ManifestSchemaVersion, Installs: map[string]ManifestEntry{}}, nil
		}
		return nil, cerrors.Errorf("could not read manifest %q: %w", path, err)
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, cerrors.Errorf("could not parse manifest %q: %w", path, err)
	}
	if m.Installs == nil {
		m.Installs = map[string]ManifestEntry{}
	}
	return &m, nil
}

// SaveManifest writes m to path atomically (temp file + rename in the same
// directory, via pkg/foundation/atomicfile), so a crash mid-write can never
// leave a torn manifest (Invariant 5).
func SaveManifest(path string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return cerrors.Errorf("could not marshal manifest: %w", err)
	}
	if err := atomicfile.WriteFile(path, data, 0o644); err != nil {
		return cerrors.Errorf("could not write manifest %q: %w", path, err)
	}
	return nil
}

// InstalledVersions returns every version of name currently installed,
// sorted ascending by semver. Used by the eventual uninstall command's
// CodeAmbiguousUninstall path (§3.1, PR-4: "conduit connectors uninstall
// <name>" without a version refuses when more than one version is
// installed) and by "list --installed"'s per-name grouping — both PR-4
// concerns; this is the pure data-query primitive they build on.
func (m *Manifest) InstalledVersions(name string) []string {
	var versions []string
	for _, e := range m.Installs {
		if e.Name == name {
			versions = append(versions, e.Version)
		}
	}
	sort.Slice(versions, func(i, j int) bool {
		vi, erri := NormalizeVersion(versions[i])
		vj, errj := NormalizeVersion(versions[j])
		if erri != nil || errj != nil {
			return versions[i] < versions[j] // never reached for well-formed entries
		}
		return vi.LessThan(vj)
	})
	return versions
}
