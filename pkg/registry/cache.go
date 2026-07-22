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
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"time"

	json "github.com/goccy/go-json"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// cacheTmpMaxAge is how old an orphaned ".tmp-populate-*" directory must be
// before CacheSweepTmp removes it (plan-v2/step5 §5: a crash mid-populate
// leaves one behind; a periodic, best-effort sweep cleans it up).
const cacheTmpMaxAge = 24 * time.Hour

// cacheDir returns <connectorsPath>/.registry/cache — the root of the
// content-addressed download cache.
func cacheDir(connectorsPath string) string {
	return filepath.Join(registryDir(connectorsPath), "cache")
}

// cacheEntryDir returns the one directory a fully-populated cache entry for
// digestHex (lowercase hex sha256, no "sha256:" prefix) lives under.
func cacheEntryDir(connectorsPath, digestHex string) string {
	return filepath.Join(cacheDir(connectorsPath), digestHex)
}

func cacheArtifactPath(connectorsPath, digestHex string) string {
	return filepath.Join(cacheEntryDir(connectorsPath, digestHex), "artifact")
}

func cacheMetaPath(connectorsPath, digestHex string) string {
	return filepath.Join(cacheEntryDir(connectorsPath, digestHex), "meta.json")
}

// CacheMeta is the small companion file written alongside a cached
// artifact's bytes (step5 §5).
type CacheMeta struct {
	SHA256    string    `json:"sha256"`
	Size      int64     `json:"size"`
	CachedAt  time.Time `json:"cachedAt"`
	SourceURL string    `json:"sourceUrl,omitempty"`
}

// normalizeDigestHex strips an optional "sha256:" prefix and lowercases the
// rest, so a digest string from an index.Artifact.SHA256 field (which may
// carry either form) always maps to the same cache directory name that
// sha256.Sum's own hex.EncodeToString output would produce.
func normalizeDigestHex(sha256Hex string) string {
	return strings.ToLower(strings.TrimPrefix(sha256Hex, "sha256:"))
}

// CacheLookup returns the cached artifact bytes for digestHex (already
// normalized — see normalizeDigestHex), re-verifying the stored bytes' own
// digest before ever returning them — never trust-on-read. A miss (absent
// OR corrupt) reports (nil, false, nil); a corrupt entry (recomputed digest
// does not match the directory it is stored under) is evicted as a side
// effect, best-effort, so a miss also self-heals the cache for the next
// caller (step5 §5's corruption-handling requirement).
//
// The cache is purely a download optimization: a hit here still requires
// the caller to run the FULL verification gate exactly as it would for a
// fresh download (see install.go's stageArtifact) — this function makes no
// trust claim about the bytes it returns.
func CacheLookup(connectorsPath, digestHex string) ([]byte, bool, error) {
	path := cacheArtifactPath(connectorsPath, digestHex)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, cerrors.Errorf("could not read cache entry %q: %w", path, err)
	}

	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != digestHex {
		// Corrupt: partial write, bitrot, or tampering. Evict best-effort —
		// a failure to remove it is not itself an error worth propagating
		// (the caller already gets a cache-miss result and will re-download
		// into a staging path that does not depend on this entry existing).
		_ = os.RemoveAll(cacheEntryDir(connectorsPath, digestHex))
		return nil, false, nil
	}
	return data, true, nil
}

// CachePopulate atomically stores data under digestHex: written to a
// uniquely-named temp directory first, then renamed into place, so a crash
// mid-populate leaves an orphaned ".tmp-populate-*" directory (cleaned up by
// CacheSweepTmp) rather than a corrupt "<digest>/" entry masquerading as
// valid.
//
// data's own digest is recomputed and asserted to match digestHex — a
// defensive check, since every real caller already knows the digest from a
// just-corruption-checked download, but this function must never silently
// cache the wrong bytes under the wrong key.
//
// Two concurrent populates of the SAME digestHex (two installs racing to
// cache the identical content-addressed artifact) are safe: if the final
// rename loses the race because the destination directory now exists, that
// destination is — by construction of content-addressing — the identical
// bytes this call would have written, so losing the race is treated as a
// successful no-op, not an error.
func CachePopulate(connectorsPath, digestHex string, data []byte, sourceURL string) error {
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != digestHex {
		return cerrors.Errorf("cache populate: data digest %q does not match expected %q", got, digestHex)
	}

	root := cacheDir(connectorsPath)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return cerrors.Errorf("could not create cache directory %q: %w", root, err)
	}
	tmpDir, err := os.MkdirTemp(root, ".tmp-populate-*")
	if err != nil {
		return cerrors.Errorf("could not create cache staging directory: %w", err)
	}
	// Cleaned up on every path: once the rename below succeeds tmpDir no
	// longer exists, so this is a no-op; on any earlier failure this removes
	// the partial attempt immediately rather than waiting for
	// CacheSweepTmp's next run.
	defer func() { _ = os.RemoveAll(tmpDir) }()

	//nolint:gosec // tmpDir comes from os.MkdirTemp (never user input) and "artifact" is a literal; gosec's
	// taint analysis conservatively flags this because connectorsPath (a function parameter) feeds tmpDir's
	// parent directory, but MkdirTemp itself is what generates the actual path component being written to.
	if err := os.WriteFile(filepath.Join(tmpDir, "artifact"), data, 0o600); err != nil {
		return cerrors.Errorf("could not write cache artifact: %w", err)
	}
	meta := CacheMeta{SHA256: digestHex, Size: int64(len(data)), CachedAt: time.Now().UTC(), SourceURL: sourceURL}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return cerrors.Errorf("could not marshal cache meta: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "meta.json"), metaBytes, 0o600); err != nil {
		return cerrors.Errorf("could not write cache meta: %w", err)
	}

	finalDir := cacheEntryDir(connectorsPath, digestHex)
	if err := os.Rename(tmpDir, finalDir); err != nil {
		if _, statErr := os.Stat(finalDir); statErr == nil {
			// Lost a race to a concurrent populate of the identical
			// content-addressed entry — see doc comment above.
			return nil
		}
		return cerrors.Errorf("could not populate cache entry %q: %w", finalDir, err)
	}
	return nil
}

// CacheSweepTmp removes every ".tmp-populate-*" directory under the cache
// root older than cacheTmpMaxAge — best-effort cleanup of an orphaned
// partial-populate left by a crash (step5 §5). Never returns an error to a
// caller that treats the cache as a pure optimization: a sweep failure just
// means a leftover temp directory persists a bit longer, not a correctness
// problem, so this function swallows its own errors rather than failing an
// install over cache housekeeping.
func CacheSweepTmp(connectorsPath string, maxAge time.Duration) {
	root := cacheDir(connectorsPath)
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	now := time.Now()
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), ".tmp-populate-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > maxAge {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}

// cacheMetaFor is a small test/inspection helper reading back a cache
// entry's meta.json, unexported since no production caller needs it today.
func cacheMetaFor(connectorsPath, digestHex string) (CacheMeta, error) {
	data, err := os.ReadFile(cacheMetaPath(connectorsPath, digestHex))
	if err != nil {
		return CacheMeta{}, err
	}
	var m CacheMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return CacheMeta{}, err
	}
	return m, nil
}
