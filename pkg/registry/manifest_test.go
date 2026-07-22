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

package registry_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/matryer/is"

	"github.com/conduitio/conduit/pkg/registry"
)

func TestManifestKey_NormalizesLeadingV(t *testing.T) {
	is := is.New(t)

	withV, err := registry.ManifestKey("postgres", "v0.14.1")
	is.NoErr(err)
	withoutV, err := registry.ManifestKey("postgres", "0.14.1")
	is.NoErr(err)

	is.Equal(withV, withoutV)
	is.Equal(withV, "postgres@0.14.1")
}

func TestManifestKey_InvalidVersion(t *testing.T) {
	is := is.New(t)
	_, err := registry.ManifestKey("postgres", "not-a-version")
	is.True(err != nil)
}

func TestLoadManifest_MissingFileIsEmpty(t *testing.T) {
	is := is.New(t)
	m, err := registry.LoadManifest(filepath.Join(t.TempDir(), "manifest.json"))
	is.NoErr(err)
	is.Equal(m.SchemaVersion, registry.ManifestSchemaVersion)
	is.Equal(len(m.Installs), 0)
}

// TestSaveAndLoadManifest_RoundTrip covers two distinct versions of the
// SAME connector name coexisting under two different keys — the entire
// point of the name@version key change (§3.1): this must never collapse to
// one entry the way a bare-name-keyed manifest would.
func TestSaveAndLoadManifest_RoundTrip(t *testing.T) {
	is := is.New(t)
	path := filepath.Join(t.TempDir(), "manifest.json")

	now := time.Now().UTC().Truncate(time.Second)
	m := &registry.Manifest{
		SchemaVersion: registry.ManifestSchemaVersion,
		Installs: map[string]registry.ManifestEntry{
			"postgres@0.14.1": {
				Name: "postgres", Version: "0.14.1", Kind: "standalone",
				OS: "darwin", Arch: "arm64",
				ArtifactFile:       "conduit-connector-postgres_0.14.1",
				Digest:             "sha256:2ba79aa9",
				Size:               13893710,
				InstalledAt:        now,
				InstalledBy:        "devaris",
				SourceIndexVersion: 42,
				Source:             "index",
				Signed:             false,
				VerifiedIdentity:   "",
				AllowUnsigned:      false,
			},
			"postgres@0.13.0": {
				Name: "postgres", Version: "0.13.0", Kind: "standalone",
				OS: "linux", Arch: "amd64",
				ArtifactFile:       "conduit-connector-postgres_0.13.0",
				Digest:             "sha256:aaaa",
				Size:               1000,
				InstalledAt:        now,
				SourceIndexVersion: 40,
				Source:             "index",
			},
		},
	}

	is.NoErr(registry.SaveManifest(path, m))

	got, err := registry.LoadManifest(path)
	is.NoErr(err)
	is.Equal(len(got.Installs), 2)
	is.Equal(got.Installs["postgres@0.14.1"].Digest, "sha256:2ba79aa9")
	is.Equal(got.Installs["postgres@0.13.0"].Digest, "sha256:aaaa")

	// A normal PR-1 install can never produce a signed/verified entry (see
	// FailClosedVerifier) — enforced here as a data-shape expectation, not
	// by this package (which has no install path at all).
	is.Equal(got.Installs["postgres@0.14.1"].Signed, false)
	is.Equal(got.Installs["postgres@0.14.1"].VerifiedIdentity, "")
}

func TestManifest_InstalledVersions_SortsAscendingBySemver(t *testing.T) {
	is := is.New(t)
	m := &registry.Manifest{
		Installs: map[string]registry.ManifestEntry{
			"postgres@0.14.1": {Name: "postgres", Version: "0.14.1"},
			"postgres@0.13.0": {Name: "postgres", Version: "0.13.0"},
			"postgres@1.0.0":  {Name: "postgres", Version: "1.0.0"},
			"kafka@0.12.3":    {Name: "kafka", Version: "0.12.3"},
		},
	}

	got := m.InstalledVersions("postgres")
	is.Equal(got, []string{"0.13.0", "0.14.1", "1.0.0"})

	is.Equal(m.InstalledVersions("kafka"), []string{"0.12.3"})
	is.Equal(len(m.InstalledVersions("does-not-exist")), 0)
}

// TestSaveManifest_ConcurrentReadsNeverSeeATornFile is the race-detector
// property PR-0 owns: a writer using atomicfile's temp+rename never exposes
// a partial file to a concurrent reader. Full concurrent-WRITER correctness
// (locking multiple installers racing to update the SAME manifest) is
// pkg/registry install pipeline's job once gofrs/flock is promoted in PR-1
// — out of scope here, per this PR's guardrails.
func TestSaveManifest_ConcurrentReadsNeverSeeATornFile(t *testing.T) {
	is := is.New(t)
	path := filepath.Join(t.TempDir(), "manifest.json")

	base := &registry.Manifest{SchemaVersion: 1, Installs: map[string]registry.ManifestEntry{
		"postgres@0.14.1": {Name: "postgres", Version: "0.14.1"},
	}}
	is.NoErr(registry.SaveManifest(path, base))

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			m := &registry.Manifest{SchemaVersion: 1, Installs: map[string]registry.ManifestEntry{
				"postgres@0.14.1": {Name: "postgres", Version: "0.14.1"},
				"kafka@0.12.3":    {Name: "kafka", Version: "0.12.3"},
			}}
			_ = registry.SaveManifest(path, m)
		}
		close(stop)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			m, err := registry.LoadManifest(path)
			if err != nil {
				t.Errorf("reader observed a torn/corrupt manifest: %v", err)
				return
			}
			if len(m.Installs) == 0 {
				t.Errorf("reader observed an impossible empty manifest mid-write")
				return
			}
		}
	}()

	wg.Wait()

	_, err := os.ReadDir(filepath.Dir(path))
	is.NoErr(err)
}
